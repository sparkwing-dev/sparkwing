package docs

import (
	"bufio"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/mod/semver"
)

// MigrationEntry describes one per-version migration guide shipped
// under docs/migrations/. Field shape (and order) mirrors the web's
// /migrations/index.json (minus url / raw_url, which are
// web-deployment artifacts) so an agent that learned the schema from
// either source can consume the other. Slug == Version to match the
// web; agents that want to feed a value to `sparkwing docs read
// --topic` should prefix with `migrations/`.
//
// Title is parsed from the file's first H1. Date and Summary are
// best-effort enrichment pulled from docs/migrations/README.md; both
// may be the empty string if the index is missing, malformed, or
// omits a row for this version.
type MigrationEntry struct {
	Version string `json:"version"`
	Slug    string `json:"slug"`
	Title   string `json:"title"`
	Date    string `json:"date"`
	Summary string `json:"summary"`
	Bytes   int    `json:"bytes"`
}

// MigrationsList returns every embedded migration guide in descending
// semver order (newest first). The Date and Summary fields are parsed
// from docs/migrations/README.md when available; a version with no
// row in the index still appears with empty enrichment.
//
// File-glob is the source of truth: any v*.md whose stem is a valid
// semver is included. README.md is excluded by name, as are files
// whose version doesn't parse via semver.IsValid.
func MigrationsList() []MigrationEntry {
	index := parseMigrationsIndex()
	var out []MigrationEntry
	_ = fs.WalkDir(allDocs, "content/migrations", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		base := path.Base(p)
		if base == "README.md" || !strings.HasSuffix(base, ".md") {
			return nil
		}
		version := strings.TrimSuffix(base, ".md")
		if !semver.IsValid(version) {
			return nil
		}
		body, rerr := fs.ReadFile(allDocs, p)
		if rerr != nil {
			return nil
		}
		title, _ := extractTitleSummary(body)
		entry := MigrationEntry{
			Version: version,
			Slug:    version,
			Title:   title,
			Bytes:   len(body),
		}
		if row, ok := index[version]; ok {
			entry.Date = row.date
			entry.Summary = row.summary
		}
		out = append(out, entry)
		return nil
	})
	sort.Slice(out, func(i, j int) bool {
		return semver.Compare(out[i].Version, out[j].Version) > 0
	})
	return out
}

// MigrationsRead returns the markdown body for one migration guide.
// Returns ErrNotFound when version isn't embedded. The body goes
// through the same cross-doc link rewriter as Read().
func MigrationsRead(version string) (string, error) {
	if !semver.IsValid(version) {
		return "", fmt.Errorf("docs: %q is not a valid semver (try v0.4.0): %w", version, ErrNotFound)
	}
	return Read("migrations/" + version)
}

// MigrationsBetween returns guides whose version is in (from, to],
// in ascending version order. Empty from defaults to v0.0.0 (every
// version up through to, inclusive). Empty to defaults to the highest
// embedded version. Returns nil with no error when the range is empty
// or inverted -- callers can warn.
func MigrationsBetween(from, to string) ([]MigrationEntry, error) {
	if from == "" {
		from = "v0.0.0"
	}
	if !semver.IsValid(from) {
		return nil, fmt.Errorf("--from %q is not a valid semver (e.g. v0.3.0)", from)
	}
	all := MigrationsList()
	if to == "" {
		if len(all) == 0 {
			return nil, nil
		}
		to = all[0].Version
	}
	if !semver.IsValid(to) {
		return nil, fmt.Errorf("--to %q is not a valid semver (e.g. v0.4.0)", to)
	}
	var picked []MigrationEntry
	for _, e := range all {
		if semver.Compare(e.Version, from) > 0 && semver.Compare(e.Version, to) <= 0 {
			picked = append(picked, e)
		}
	}
	sort.Slice(picked, func(i, j int) bool {
		return semver.Compare(picked[i].Version, picked[j].Version) < 0
	})
	return picked, nil
}

// MigrationsBetweenMarkdown formats the output of MigrationsBetween
// as a single markdown blob with a range header and `---` separators
// between releases. Designed for piping straight into agent context.
func MigrationsBetweenMarkdown(from, to string, entries []MigrationEntry) (string, error) {
	var b strings.Builder
	displayFrom := from
	if displayFrom == "" {
		displayFrom = "v0.0.0"
	}
	displayTo := to
	if displayTo == "" && len(entries) > 0 {
		displayTo = entries[len(entries)-1].Version
	}
	if displayTo == "" {
		displayTo = "(latest)"
	}
	fmt.Fprintf(&b, "# Migration: %s -> %s\n\n", displayFrom, displayTo)
	switch len(entries) {
	case 0:
		b.WriteString("(no migration guides apply in this range)\n")
		return b.String(), nil
	case 1:
		b.WriteString("(1 guide applies in this range)\n\n")
	default:
		fmt.Fprintf(&b, "(%d guides apply in this range)\n\n", len(entries))
	}
	for i, e := range entries {
		body, err := MigrationsRead(e.Version)
		if err != nil {
			return "", err
		}
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString("---\n\n")
		b.WriteString(strings.TrimSpace(body))
		b.WriteString("\n")
	}
	return b.String(), nil
}

// migrationIndexRow is one parsed line from docs/migrations/README.md.
type migrationIndexRow struct {
	date    string
	summary string
}

// migrationsIndexRowPattern matches a markdown table row of the form
// `| [vX.Y.Z](anything) | YYYY-MM-DD | summary text |`.
var migrationsIndexRowPattern = regexp.MustCompile(`^\|\s*\[(v[^\]]+)\]\([^)]*\)\s*\|\s*([^|]*?)\s*\|\s*(.*?)\s*\|\s*$`)

// parseMigrationsIndex returns the parsed rows of
// content/migrations/README.md keyed by version. Missing or malformed
// index is non-fatal: callers fall back to the file glob.
func parseMigrationsIndex() map[string]migrationIndexRow {
	out := map[string]migrationIndexRow{}
	body, err := fs.ReadFile(allDocs, "content/migrations/README.md")
	if err != nil {
		return out
	}
	scanner := bufio.NewScanner(strings.NewReader(string(body)))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		m := migrationsIndexRowPattern.FindStringSubmatch(line)
		if len(m) != 4 {
			continue
		}
		version := m[1]
		if !semver.IsValid(version) {
			continue
		}
		out[version] = migrationIndexRow{date: m[2], summary: m[3]}
	}
	return out
}
