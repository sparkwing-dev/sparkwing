package store_test

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func requirementNames(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM sparkwing_requirements ORDER BY name`)
	if err != nil {
		t.Fatalf("read requirements: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("scan requirement: %v", err)
		}
		names = append(names, n)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate requirements: %v", err)
	}
	return names
}

func TestRequirements_FreshSQLiteRecordsDeclaredSet(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "fresh.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = st.Close() }()

	got := requirementNames(t, st.DB())
	want := store.KnownRequirements()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("requirements = %v, want %v", got, want)
	}
	listed, err := st.Requirements(context.Background())
	if err != nil {
		t.Fatalf("Requirements: %v", err)
	}
	if !reflect.DeepEqual(listed, want) {
		t.Errorf("Requirements() = %v, want %v", listed, want)
	}
}

func TestRequirements_FreshPostgresRecordsDeclaredSet(t *testing.T) {
	st := openPGTestStore(t)
	got := requirementNames(t, st.DB())
	if want := store.KnownRequirements(); !reflect.DeepEqual(got, want) {
		t.Errorf("requirements = %v, want %v", got, want)
	}
}

// A binary that predates a purely additive migration must keep working: the
// database records a version it does not know but lists no requirement it
// lacks, so it opens read/write, migrates nothing, and stamps nothing.
func TestRequirements_AdditiveFutureVersionOpensReadWrite(t *testing.T) {
	store.SetBinaryVersion("v0.38.2")
	t.Cleanup(func() { store.SetBinaryVersion("") })

	path := filepath.Join(t.TempDir(), "additive.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open#1: %v", err)
	}
	future := store.ExpectedSchemaVersion() + 1
	if _, err := st.DB().Exec(
		`INSERT INTO sparkwing_schema_version (version, applied_at) VALUES (?, ?)`,
		future, 1); err != nil {
		t.Fatalf("seed future version: %v", err)
	}
	if _, err := st.DB().Exec(
		`UPDATE sparkwing_meta SET value = 'v0.99.0' WHERE key = 'min_binary_version'`); err != nil {
		t.Fatalf("seed min version: %v", err)
	}
	_ = st.Close()

	reopened, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open#2 against an additively migrated database: %v", err)
	}
	defer func() { _ = reopened.Close() }()

	ctx := context.Background()
	if v, err := reopened.CurrentSchemaVersion(ctx); err != nil || v != future {
		t.Fatalf("CurrentSchemaVersion = %d, %v; want %d and no migration", v, err, future)
	}
	if got := reopened.MinBinaryVersion(ctx); got != "v0.99.0" {
		t.Errorf("min_binary_version = %q, want it left at v0.99.0 (nothing migrated)", got)
	}
	if _, err := reopened.DB().Exec(
		`INSERT INTO sparkwing_meta (key, value, updated_at) VALUES ('probe', 'written', 1)`); err != nil {
		t.Fatalf("write against an additively migrated database: %v", err)
	}
	var probe string
	if err := reopened.DB().QueryRow(
		`SELECT value FROM sparkwing_meta WHERE key = 'probe'`).Scan(&probe); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if probe != "written" {
		t.Errorf("probe = %q, want written", probe)
	}
}

func TestRequirements_BackfilledOnPreRequirementsDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open#1: %v", err)
	}
	if _, err := st.DB().Exec(`DROP TABLE sparkwing_requirements`); err != nil {
		t.Fatalf("drop requirements table: %v", err)
	}
	_ = st.Close()

	reopened, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open#2: %v", err)
	}
	defer func() { _ = reopened.Close() }()

	got := requirementNames(t, reopened.DB())
	if want := store.KnownRequirements(); !reflect.DeepEqual(got, want) {
		t.Errorf("backfilled requirements = %v, want %v", got, want)
	}
	if v, err := reopened.CurrentSchemaVersion(context.Background()); err != nil ||
		v != store.ExpectedSchemaVersion() {
		t.Fatalf("CurrentSchemaVersion = %d, %v; backfill must not rerun migrations", v, err)
	}
}

func TestRequirements_ConcurrentColdStartLeavesOneRowEach(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")

	const openers = 8
	errs := make([]error, openers)
	var wg sync.WaitGroup
	for i := range openers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s, err := store.Open(path)
			if err != nil {
				errs[i] = err
				return
			}
			_ = s.Close()
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("opener %d failed to cold-start: %v", i, err)
		}
	}

	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("reopen after concurrent cold start: %v", err)
	}
	defer func() { _ = st.Close() }()
	if got, want := requirementNames(t, st.DB()), store.KnownRequirements(); !reflect.DeepEqual(got, want) {
		t.Errorf("requirements = %v, want exactly %v", got, want)
	}
}

func TestRequirements_PostgresBackfilledOnPreRequirementsDatabase(t *testing.T) {
	st := openPGTestStore(t)
	if _, err := st.DB().Exec(`DROP TABLE sparkwing_requirements`); err != nil {
		t.Fatalf("drop requirements table: %v", err)
	}
	var searchPath string
	if err := st.DB().QueryRow(`SHOW search_path`).Scan(&searchPath); err != nil {
		t.Fatalf("read search_path: %v", err)
	}
	schema := strings.TrimSpace(strings.Split(searchPath, ",")[0])
	dsn := pgTestDSN(t)
	scoped := fmt.Sprintf("%s%ssearch_path=%s", dsn, querySep(dsn), schema)

	reopened, err := store.OpenPostgres(context.Background(), scoped)
	if err != nil {
		t.Fatalf("reopen postgres: %v", err)
	}
	defer func() { _ = reopened.Close() }()
	if got, want := requirementNames(t, reopened.DB()), store.KnownRequirements(); !reflect.DeepEqual(got, want) {
		t.Errorf("backfilled requirements = %v, want %v", got, want)
	}
}

func TestMissingRequirements_ReportsWhatTheOtherSideLacks(t *testing.T) {
	known := []string{"repo-scoped-secrets", "session-token-digest"}
	listed := []string{"unique-token-prefix", "session-token-digest", "inherited-holder-marker"}
	got := store.MissingRequirements(known, listed)
	want := []string{"inherited-holder-marker", "unique-token-prefix"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("MissingRequirements = %v, want %v", got, want)
	}
	if got := store.MissingRequirements(listed, nil); got != nil {
		t.Errorf("MissingRequirements(known, nil) = %v, want nil", got)
	}
	if got := store.UnknownRequirements(store.KnownRequirements()); got != nil {
		t.Errorf("UnknownRequirements(KnownRequirements()) = %v, want nil", got)
	}
}

func TestRequirements_FleetMigrationsDeclareWriterSafetyGates(t *testing.T) {
	preFleet := []string{
		"inherited-holder-marker",
		"repo-scoped-secrets",
		"session-token-digest",
		"unique-token-prefix",
	}
	want := []string{
		"agent-loss-attempt-fencing-v1",
		"executor-enrollment-v1",
		"executor-offer-arbitration-v1",
	}
	if got := store.MissingRequirements(preFleet, store.KnownRequirements()); !reflect.DeepEqual(got, want) {
		t.Fatalf("requirements unknown to a pre-fleet binary = %v, want %v", got, want)
	}
}

func TestRequirements_FleetCompositeAdvertisesAllWriterGatesFromWave2V29(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wave2-v29.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`DELETE FROM sparkwing_schema_version WHERE version >= 30`); err != nil {
		t.Fatal(err)
	}
	deleteFleetRequirements(t, st.DB())

	wouldAdd, err := st.RequirementsWritingWouldAdd(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"agent-loss-attempt-fencing-v1",
		"executor-enrollment-v1",
		"executor-offer-arbitration-v1",
	}
	if !reflect.DeepEqual(wouldAdd, want) {
		t.Fatalf("RequirementsWritingWouldAdd = %v, want %v", wouldAdd, want)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	ro, err := store.OpenReadOnly(path)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	defer func() { _ = ro.Close() }()
	if got, err := ro.CurrentSchemaVersion(context.Background()); err != nil || got != 29 {
		t.Fatalf("read-only schema = %d, %v; want unchanged v29", got, err)
	}
	if got, err := ro.Requirements(context.Background()); err != nil || len(got) != 4 {
		t.Fatalf("read-only requirements = %v, %v; want only the four pre-Fleet gates", got, err)
	}
}
