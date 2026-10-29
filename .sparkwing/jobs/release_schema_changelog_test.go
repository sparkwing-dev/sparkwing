package jobs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseStoreSchemaVersion(t *testing.T) {
	cases := []struct {
		name    string
		src     string
		want    int
		wantErr bool
	}{
		{name: "canonical", src: "package store\n\nconst expectedSchemaVersion = 4\n", want: 4},
		{name: "extra spacing", src: "const expectedSchemaVersion   =   12\n", want: 12},
		{name: "with trailing comment", src: "const expectedSchemaVersion = 7 // bumped\n", want: 7},
		{name: "missing", src: "package store\n\nconst other = 3\n", wantErr: true},
		{name: "not a top-level const line", src: "// const expectedSchemaVersion = 9\n", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseStoreSchemaVersion(tc.src)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got version %d", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("version: got %d want %d", got, tc.want)
			}
		})
	}
}

func TestLintSchemaBreak_UnchangedSchemaPasses(t *testing.T) {
	body := `## [v0.10.0] - 2026-06-14

### Changed

- **cli:** something
`
	if issues := LintSchemaBreak(body, "v0.10.0", 4, 4); len(issues) != 0 {
		t.Fatalf("unchanged schema should pass, got %v", formatAllIssues(issues))
	}
}

func TestLintSchemaBreak_MarkedInVersionSectionPasses(t *testing.T) {
	body := `## [v0.10.0] - 2026-06-14

### Changed

- **runs-store (Breaking):** schema 3 -> 4, runs persist a new column. See [migration](docs/migrations/v0.10.0.md#schema).
`
	if issues := LintSchemaBreak(body, "v0.10.0", 3, 4); len(issues) != 0 {
		t.Fatalf("marked schema break should pass, got %v", formatAllIssues(issues))
	}
}

func TestLintSchemaBreak_MarkedInUnreleasedPasses(t *testing.T) {
	body := `## [Unreleased]

### Changed

- **store (Breaking):** runs-store schema bumped for the new index.
`
	if issues := LintSchemaBreak(body, "v0.10.0", 3, 4); len(issues) != 0 {
		t.Fatalf("schema break marked in [Unreleased] should pass before the rewrite, got %v", formatAllIssues(issues))
	}
}

func TestLintSchemaBreak_ChangedButUnmarkedFails(t *testing.T) {
	body := `## [v0.10.0] - 2026-06-14

### Changed

- **cli:** unrelated change, no schema mention.
`
	issues := LintSchemaBreak(body, "v0.10.0", 3, 4)
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %d: %v", len(issues), formatAllIssues(issues))
	}
	i := issues[0]
	if i.Category != schemaBreakCategory {
		t.Errorf("category: got %q want %q", i.Category, schemaBreakCategory)
	}
	if !strings.Contains(i.Message, "3 -> 4") {
		t.Errorf("message should name the version delta: %q", i.Message)
	}
	if !strings.Contains(i.Message, "v0.10.0") {
		t.Errorf("message should name the migration file: %q", i.Message)
	}
}

func TestLintSchemaBreak_BreakingEntryNotAboutSchemaFails(t *testing.T) {
	body := `## [v0.10.0] - 2026-06-14

### Changed

- **config (Breaking):** renamed a YAML field, unrelated to the store.
`
	issues := LintSchemaBreak(body, "v0.10.0", 3, 4)
	if len(issues) != 1 {
		t.Fatalf("a (Breaking) entry that never names the schema should not satisfy the gate, got %d: %v",
			len(issues), formatAllIssues(issues))
	}
}

func TestLintSchemaBreak_SchemaMentionWithoutBreakingMarkerFails(t *testing.T) {
	body := `## [v0.10.0] - 2026-06-14

### Changed

- **store:** internal runs-store schema tweak, no breaking marker.
`
	issues := LintSchemaBreak(body, "v0.10.0", 3, 4)
	if len(issues) != 1 {
		t.Fatalf("a schema mention without a (Breaking) marker should fail, got %d: %v",
			len(issues), formatAllIssues(issues))
	}
}

func TestSchemaSQLDrift(t *testing.T) {
	same := map[string]string{
		"internal/store/schema_sqlite.sql":   "CREATE TABLE runs (id TEXT);\n",
		"internal/store/schema_postgres.sql": "CREATE TABLE runs (id text);\n",
	}
	if err := schemaSQLDrift("v0.70.0", 1, same, same); err != nil {
		t.Fatalf("identical baselines = %v, want none", err)
	}
	edited := map[string]string{
		"internal/store/schema_sqlite.sql":   "CREATE TABLE runs (run_id TEXT);\n",
		"internal/store/schema_postgres.sql": same["internal/store/schema_postgres.sql"],
	}
	err := schemaSQLDrift("v0.70.0", 1, same, edited)
	if err == nil || !strings.Contains(err.Error(), "schema SQL changed without bumping the schema version") ||
		!strings.Contains(err.Error(), "schema_sqlite.sql") || strings.Contains(err.Error(), "schema_postgres.sql") {
		t.Fatalf("an edited SQLite baseline = %v, want the drift refusal naming only that file", err)
	}
	if err := schemaSQLDrift("v0.70.0", 1, map[string]string{}, same); err == nil {
		t.Fatal("a tag without the baseline files passed the drift check")
	}
}

func TestSchemaGateCountsAStoreThePreviousTagLacksAsAChange(t *testing.T) {
	base := t.TempDir()
	origin := filepath.Join(base, "origin.git")
	repo := filepath.Join(base, "repo")
	gitRun(t, base, "init", "--bare", "-b", "main", origin)
	gitRun(t, base, "init", "-b", "main", repo)
	gitRun(t, repo, "config", "user.email", "test@example.invalid")
	gitRun(t, repo, "config", "user.name", "test")
	gitRun(t, repo, "remote", "add", "origin", origin)
	writeFile(t, filepath.Join(repo, "CHANGELOG.md"), "# Changelog\n\n## [Unreleased]\n")
	gitRun(t, repo, "add", "CHANGELOG.md")
	gitRun(t, repo, "commit", "-m", "before the store moved")
	gitRun(t, repo, "tag", "v0.1.0")
	if err := os.MkdirAll(filepath.Join(repo, "internal", "store"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(repo, filepath.FromSlash(storeSchemaSourcePath)), "package store\n\nconst expectedSchemaVersion = 3\n")
	writeFile(t, filepath.Join(repo, "CHANGELOG.md"), "# Changelog\n\n## [Unreleased]\n\n### Changed\n\n- **cli:** Unrelated.\n")
	gitRun(t, repo, "add", ".")
	gitRun(t, repo, "commit", "-m", "the store moves")
	gitRun(t, repo, "push", "origin", "main", "--tags")

	err := checkSchemaBreak(t.Context(), repo, "v0.2.0")
	if err == nil || !strings.Contains(err.Error(), "undescribed runs-store schema change") {
		t.Fatalf("unmarked new store = %v, want the schema refusal", err)
	}
	writeFile(t, filepath.Join(repo, "CHANGELOG.md"), "# Changelog\n\n## [Unreleased]\n\n### Changed\n\n- **store (Breaking):** A new schema baseline.\n")
	if err := checkSchemaBreak(t.Context(), repo, "v0.2.0"); err != nil {
		t.Fatalf("marked new store refused: %v", err)
	}
}
