package jobs

import (
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
