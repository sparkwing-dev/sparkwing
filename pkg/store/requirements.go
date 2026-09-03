package store

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"golang.org/x/mod/semver"
)

const requirementsTable = `CREATE TABLE IF NOT EXISTS sparkwing_requirements (
    name             TEXT NOT NULL,
    added_at         BIGINT NOT NULL,
    added_by_version TEXT NOT NULL,
    PRIMARY KEY (name)
);`

// schemaRequirement is one row of sparkwing_requirements: a feature the
// database relies on, and the binary version that first stamped it.
type schemaRequirement struct {
	name    string
	addedBy string
}

var knownRequirementSet = func() map[string]bool {
	set := make(map[string]bool)
	for _, names := range migrationRequirements {
		for _, name := range names {
			set[name] = true
		}
	}
	return set
}()

// KnownRequirements returns the schema requirement names this binary
// understands, sorted. A database that lists only these names opens
// whatever schema version it records, so a caller comparing two binaries
// (a client and the daemon it shares a store with) compares these sets
// rather than schema numbers.
func KnownRequirements() []string {
	names := make([]string, 0, len(knownRequirementSet))
	for name := range knownRequirementSet {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// UnknownRequirements returns the names in listed that this binary does
// not understand, sorted. An empty result means this binary can read a
// database listing exactly those requirements.
func UnknownRequirements(listed []string) []string {
	return MissingRequirements(KnownRequirements(), listed)
}

// MissingRequirements returns the names in listed that known does not
// contain, sorted. It answers "can a binary that knows this set read a
// store that lists that set" for any pair of binaries, not just the
// running one.
func MissingRequirements(known, listed []string) []string {
	have := make(map[string]bool, len(known))
	for _, name := range known {
		have[name] = true
	}
	var missing []string
	for _, name := range listed {
		if !have[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	return missing
}

// Requirements returns the schema requirement names the database lists,
// sorted. A database written before requirements shipped lists none, and
// so does one this binary opened read-only before any migration ran.
func (s *Store) Requirements(ctx context.Context) ([]string, error) {
	exists, err := s.hasRequirementsTable(ctx)
	if err != nil || !exists {
		return nil, err
	}
	rows, err := listRequirements(ctx, storeExecer{s: s})
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(rows))
	for _, r := range rows {
		names = append(names, r.name)
	}
	return names, nil
}

func (s *Store) hasRequirementsTable(ctx context.Context) (bool, error) {
	q := `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'sparkwing_requirements'`
	if s.dialect == DialectPostgres {
		q = `SELECT COUNT(*) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		     WHERE c.relname = 'sparkwing_requirements' AND c.relkind = 'r'
		       AND n.nspname = ANY (current_schemas(true))`
	}
	var count int
	if err := s.queryRow(ctx, q).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

func listRequirements(ctx context.Context, q migrationQueryExecer) ([]schemaRequirement, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT name, added_by_version FROM sparkwing_requirements ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []schemaRequirement
	for rows.Next() {
		var r schemaRequirement
		if err := rows.Scan(&r.name, &r.addedBy); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func insertRequirements(ctx context.Context, q migrationQueryExecer, names []string) error {
	stamp := resolveBinaryVersion()
	now := time.Now().UnixNano()
	for _, name := range names {
		if _, err := q.ExecContext(ctx,
			`INSERT INTO sparkwing_requirements (name, added_at, added_by_version) VALUES (?, ?, ?)
			 ON CONFLICT (name) DO NOTHING`,
			name, now, stamp); err != nil {
			return err
		}
	}
	return nil
}

// requirementsThrough returns every requirement the registry declares for
// schema versions at or below version, sorted.
func requirementsThrough(version int) []string {
	var names []string
	for v, declared := range migrationRequirements {
		if v <= version {
			names = append(names, declared...)
		}
	}
	sort.Strings(names)
	return names
}

// requirementsToBackfill returns the declared requirements for versions
// already applied that the database does not list yet. A database migrated
// before requirements shipped lists none of them, so the first
// requirements-aware binary to open it stamps its whole history.
func requirementsToBackfill(listed []schemaRequirement, version int) []string {
	have := make(map[string]bool, len(listed))
	for _, r := range listed {
		have[r.name] = true
	}
	var missing []string
	for _, name := range requirementsThrough(version) {
		if !have[name] {
			missing = append(missing, name)
		}
	}
	return missing
}

// requirementSkew returns the error to refuse an open with when the database
// lists a requirement this binary does not know, and nil otherwise. The
// version it names is the highest binary version that stamped one of the
// unknown requirements: every one of them is present in a build at least
// that new.
func requirementSkew(dbVersion int, listed []schemaRequirement) *SkewError {
	var unknown []string
	need := ""
	for _, r := range listed {
		if knownRequirementSet[r.name] {
			continue
		}
		unknown = append(unknown, r.name)
		if semver.IsValid(r.addedBy) && (need == "" || semver.Compare(r.addedBy, need) > 0) {
			need = r.addedBy
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	return &SkewError{
		DBVersion:        dbVersion,
		BinaryVersion:    expectedSchemaVersion,
		MinVersion:       need,
		InstalledVersion: resolveBinaryVersion(),
		Requirements:     unknown,
	}
}

func (e *SkewError) requirementMessage() string {
	installed := e.InstalledVersion
	if installed == "" || installed == "(devel)" {
		installed = "an older build"
	}
	verb := "need"
	if len(e.Requirements) == 1 {
		verb = "needs"
	}
	if e.MinVersion == "" || e.MinVersion == "(devel)" {
		return fmt.Sprintf(
			"sparkwing: this state database uses %s, which %s a newer build than %s. "+
				"Run `sparkwing update` to upgrade.",
			joinRequirements(e.Requirements), verb, installed)
	}
	return fmt.Sprintf(
		"sparkwing: this state database uses %s, which %s sparkwing >= %s; you have %s. "+
			"Run `sparkwing update` to upgrade.",
		joinRequirements(e.Requirements), verb, e.MinVersion, installed)
}

func joinRequirements(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	case 2:
		return names[0] + " and " + names[1]
	default:
		return strings.Join(names[:len(names)-1], ", ") + ", and " + names[len(names)-1]
	}
}
