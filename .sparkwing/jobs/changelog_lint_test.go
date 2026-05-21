package jobs

import (
	"strings"
	"testing"
	"testing/fstest"
)

func TestLintChangelog_Dedupe_Clean(t *testing.T) {
	body := `# Changelog

## [Unreleased]

### Added

- **sdk:** Foo

### Changed

- **cli:** Bar

### Fixed

- **runner:** Baz
`
	issues := LintChangelog(body, fstest.MapFS{})
	if len(issues) != 0 {
		t.Fatalf("expected 0 issues, got %v", formatAllIssues(issues))
	}
}

func TestLintChangelog_Dedupe_DuplicateAdded(t *testing.T) {
	body := `# Changelog

## [Unreleased]

### Added

- **sdk:** First

### Changed

- **cli:** Mid

### Added

- **runner:** Second
`
	issues := LintChangelog(body, fstest.MapFS{})
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %d: %v", len(issues), formatAllIssues(issues))
	}
	i := issues[0]
	if i.Category != "duplicate-heading" {
		t.Errorf("category: got %q want duplicate-heading", i.Category)
	}
	if i.Line != 13 {
		t.Errorf("line: got %d want 13 (the second ### Added)", i.Line)
	}
	if !strings.Contains(i.Message, "appears more than once in [Unreleased]") {
		t.Errorf("message missing section context: %q", i.Message)
	}
	if !strings.Contains(i.Message, "first at line 5") {
		t.Errorf("message missing first-occurrence line: %q", i.Message)
	}
}

func TestLintChangelog_Dedupe_MultipleCategoriesDuplicated(t *testing.T) {
	body := `## [Unreleased]

### Added

- **a:** x

### Changed

- **b:** y

### Added

- **c:** z

### Changed

- **d:** w
`
	issues := LintChangelog(body, fstest.MapFS{})
	if len(issues) != 2 {
		t.Fatalf("expected 2 dedupe issues, got %d: %v", len(issues), formatAllIssues(issues))
	}
	if issues[0].Category != "duplicate-heading" || issues[1].Category != "duplicate-heading" {
		t.Errorf("both should be duplicate-heading, got %v", formatAllIssues(issues))
	}
}

func TestLintChangelog_Dedupe_SameNameAcrossSectionsIsFine(t *testing.T) {
	body := `## [Unreleased]

### Added

- **a:** new thing

## [v0.3.0] - 2026-05-20

### Added

- **b:** old thing
`
	issues := LintChangelog(body, fstest.MapFS{})
	if len(issues) != 0 {
		t.Fatalf("expected 0 issues; ### Added in two different sections is legitimate. Got %v", formatAllIssues(issues))
	}
}

func TestLintChangelog_Dedupe_UnknownCategoryNotFlagged(t *testing.T) {
	body := `## [Unreleased]

### Notes

Some prose.

### Notes

More prose.
`
	// Both "Notes" headings would dedup-flag because we treat
	// any repeated ### within a section as a duplicate. This is
	// stricter than the spec, which only mentions the known
	// categories — but flagging here is the conservative behavior.
	issues := LintChangelog(body, fstest.MapFS{})
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(issues))
	}
	if issues[0].Category != "duplicate-heading" {
		t.Errorf("category: got %q", issues[0].Category)
	}
}

func TestLintChangelog_Dedupe_CaseSensitivity(t *testing.T) {
	body := `## [Unreleased]

### Added

- **a:** x

### added

- **b:** y
`
	issues := LintChangelog(body, fstest.MapFS{})
	if len(issues) != 0 {
		t.Fatalf("### Added vs ### added are distinct; expected 0 issues, got %v", formatAllIssues(issues))
	}
}

// ---------------------------------------------------------------
// Breaking-entry / migration-link tests
// ---------------------------------------------------------------

func TestLintChangelog_Breaking_VersionedSection_LinkResolves(t *testing.T) {
	body := `## [v0.4.0] - 2026-05-20

### Changed

- **sdk (Breaking):** ` + "`Needs(...any)`" + ` replaced with ` + "`Needs(...Dep)`" + `.
  See [migration guide](docs/migrations/v0.4.0.md#typed-dep-interface).
`
	migrations := fstest.MapFS{
		"v0.4.0.md": &fstest.MapFile{Data: []byte(`# Migrating to v0.4.0

## Typed Dep interface

Before/after content.
`)},
	}
	issues := LintChangelog(body, migrations)
	if len(issues) != 0 {
		t.Fatalf("expected 0 issues, got %v", formatAllIssues(issues))
	}
}

func TestLintChangelog_Breaking_VersionedSection_MissingLink(t *testing.T) {
	body := `## [v0.4.0] - 2026-05-20

### Changed

- **sdk (Breaking):** Needs(...any) replaced with Needs(...Dep).
`
	issues := LintChangelog(body, fstest.MapFS{})
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %d: %v", len(issues), formatAllIssues(issues))
	}
	if issues[0].Category != "missing-migration-link" {
		t.Errorf("category: got %q", issues[0].Category)
	}
}

func TestLintChangelog_Breaking_VersionMismatch(t *testing.T) {
	body := `## [v0.4.0] - 2026-05-20

### Changed

- **sdk (Breaking):** Foo.
  See [migration guide](docs/migrations/v0.5.0.md#foo).
`
	migrations := fstest.MapFS{
		"v0.5.0.md": &fstest.MapFile{Data: []byte("# Migrating to v0.5.0\n\n## Foo\n")},
	}
	issues := LintChangelog(body, migrations)
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %d: %v", len(issues), formatAllIssues(issues))
	}
	if issues[0].Category != "version-mismatch" {
		t.Errorf("category: got %q", issues[0].Category)
	}
	if !strings.Contains(issues[0].Message, "v0.5.0") || !strings.Contains(issues[0].Message, "v0.4.0") {
		t.Errorf("message should name both versions, got %q", issues[0].Message)
	}
}

func TestLintChangelog_Breaking_MissingAnchor_ListsAvailable(t *testing.T) {
	body := `## [v0.4.0] - 2026-05-20

### Changed

- **sdk (Breaking):** Foo.
  See [migration guide](docs/migrations/v0.4.0.md#nonexistent).
`
	migrations := fstest.MapFS{
		"v0.4.0.md": &fstest.MapFile{Data: []byte(`# Migrating to v0.4.0

## Typed Dep interface

stuff

## CacheOptions rename

more stuff
`)},
	}
	issues := LintChangelog(body, migrations)
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %d: %v", len(issues), formatAllIssues(issues))
	}
	if issues[0].Category != "missing-migration-anchor" {
		t.Errorf("category: got %q", issues[0].Category)
	}
	// Message should surface the available anchors so the author
	// can fix a typo without re-opening the file.
	if !strings.Contains(issues[0].Message, "#typed-dep-interface") {
		t.Errorf("message missing #typed-dep-interface in available list: %q", issues[0].Message)
	}
	if !strings.Contains(issues[0].Message, "#cacheoptions-rename") {
		t.Errorf("message missing #cacheoptions-rename in available list: %q", issues[0].Message)
	}
}

func TestLintChangelog_Breaking_MissingFile(t *testing.T) {
	body := `## [v0.4.0] - 2026-05-20

### Changed

- **sdk (Breaking):** Foo.
  See [migration guide](docs/migrations/v0.4.0.md#foo).
`
	issues := LintChangelog(body, fstest.MapFS{})
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %d: %v", len(issues), formatAllIssues(issues))
	}
	if issues[0].Category != "missing-migration-file" {
		t.Errorf("category: got %q", issues[0].Category)
	}
}

func TestLintChangelog_Breaking_Unreleased_NoLinkIsOK(t *testing.T) {
	body := `## [Unreleased]

### Changed

- **sdk (Breaking):** Foo. The migration guide gets generated at release time.
`
	issues := LintChangelog(body, fstest.MapFS{})
	if len(issues) != 0 {
		t.Fatalf("expected 0 issues (Unreleased breaking-without-link is allowed), got %v", formatAllIssues(issues))
	}
}

func TestLintChangelog_Breaking_Unreleased_UnreleasedPlaceholderIsOK(t *testing.T) {
	body := `## [Unreleased]

### Changed

- **sdk (Breaking):** Foo.
  See [migration guide](docs/migrations/_unreleased.md#foo).
`
	issues := LintChangelog(body, fstest.MapFS{})
	if len(issues) != 0 {
		t.Fatalf("expected 0 issues (_unreleased.md placeholder is allowed), got %v", formatAllIssues(issues))
	}
}

func TestLintChangelog_NonBreaking_LinkAllowedNotRequired(t *testing.T) {
	body := `## [v0.4.0] - 2026-05-20

### Added

- **sdk:** New feature. See [docs](docs/migrations/v0.4.0.md#some-anchor) for context.

### Changed

- **cli:** Tweaked help text.
`
	migrations := fstest.MapFS{
		"v0.4.0.md": &fstest.MapFile{Data: []byte("# v0.4.0\n\n## Some anchor\n")},
	}
	issues := LintChangelog(body, migrations)
	if len(issues) != 0 {
		t.Fatalf("non-breaking entries shouldn't require links and links shouldn't fail; got %v", formatAllIssues(issues))
	}
}

func TestLintChangelog_BreakingInProseNotInTitleLine_Ignored(t *testing.T) {
	body := `## [v0.4.0] - 2026-05-20

### Changed

- **sdk:** Some non-breaking change.
  The follow-up sentence mentions (Breaking) just as prose.
`
	migrations := fstest.MapFS{
		"v0.4.0.md": &fstest.MapFile{Data: []byte("# v0.4.0\n")},
	}
	issues := LintChangelog(body, migrations)
	if len(issues) != 0 {
		t.Fatalf("`(Breaking)` only counts in the title-line scope prefix; got %v", formatAllIssues(issues))
	}
}

// ---------------------------------------------------------------
// Slugifier coverage
// ---------------------------------------------------------------

func TestSlugifyHeading(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"Typed Dep interface", "typed-dep-interface"},
		{"CacheOptions rename", "cacheoptions-rename"},
		{"Two   Spaces", "two---spaces"},
		{"Drops Punctuation, Like Commas.", "drops-punctuation-like-commas"},
		{"underscores_are_kept_and-hyphens-too", "underscores_are_kept_and-hyphens-too"},
		{"Already-Lower 123", "already-lower-123"},
		// Code-like heading: GitHub strips backticks, parens, dots,
		// arrows. Our slugifier matches that behavior for the common
		// shapes.
		{"`Needs(...any)`→`Needs(...Dep)`", "needsanyneedsdep"},
	}
	for _, c := range cases {
		got := slugifyHeading(c.in)
		if got != c.want {
			t.Errorf("slugifyHeading(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func formatAllIssues(issues []ChangelogIssue) []string {
	out := make([]string, len(issues))
	for i, x := range issues {
		out[i] = x.Format()
	}
	return out
}
