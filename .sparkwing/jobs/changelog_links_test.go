package jobs

import (
	"strings"
	"testing"
	"testing/fstest"
)

func linkFS() fstest.MapFS {
	return fstest.MapFS{
		"docs/caching.md": &fstest.MapFile{Data: []byte("# Caching\n\n## Shared cache reads\n\ntext\n")},
	}
}

func categories(issues []ChangelogIssue) []string {
	var out []string
	for _, i := range issues {
		out = append(out, i.Category)
	}
	return out
}

func TestLintChangelogDocLinks_AcceptsALiveFileAndAnchor(t *testing.T) {
	body := "- **cache:** see [the guide](docs/caching.md#shared-cache-reads).\n"
	if got := LintChangelogDocLinks(body, linkFS()); len(got) != 0 {
		t.Errorf("issues = %v, want none", categories(got))
	}
}

func TestLintChangelogDocLinks_ReportsAMissingFile(t *testing.T) {
	body := "- **run:** see [the contract](docs/box-slot-lockfile-contract.md).\n"
	got := LintChangelogDocLinks(body, linkFS())
	if len(got) != 1 || got[0].Category != deadDocLinkCategory {
		t.Fatalf("issues = %v, want one %s", categories(got), deadDocLinkCategory)
	}
	if !strings.Contains(got[0].Message, "box-slot-lockfile-contract.md") {
		t.Errorf("message = %q, want it to name the missing file", got[0].Message)
	}
}

func TestLintChangelogDocLinks_ReportsAnAnchorNoHeadingMatches(t *testing.T) {
	body := "- **cache:** see [the guide](docs/caching.md#reservations-that-never-existed).\n"
	got := LintChangelogDocLinks(body, linkFS())
	if len(got) != 1 {
		t.Fatalf("issues = %v, want exactly one", categories(got))
	}
}

func TestLintChangelogDocLinks_LeavesMigrationLinksToTheBreakingEntryCheck(t *testing.T) {
	body := "- **cli (Breaking):** see [migration](docs/migrations/v9.9.9.md#gone).\n"
	if got := LintChangelogDocLinks(body, linkFS()); len(got) != 0 {
		t.Errorf("issues = %v, want none: migration links belong to the breaking-entry check", categories(got))
	}
}

func TestPinnedMigrationLink_SatisfiesTheBreakingEntryRequirement(t *testing.T) {
	body := "## [v0.40.0] - 2026-01-01\n### Changed\n\n" +
		"- **cli (Breaking):** scopes split. See [the migration\n" +
		"  guide](https://github.com/sparkwing-dev/sparkwing/blob/v0.40.0/docs/migrations/_unreleased.md#breaking-runner-scopes-split-out-of-admin).\n"
	if got := LintChangelog(body, fstest.MapFS{}); len(got) != 0 {
		t.Errorf("issues = %v, want none: a permalink pinned to the release tag still resolves", categories(got))
	}
}

func TestUnguidedBreakingEntries_ForgivesOnlyWhatItNames(t *testing.T) {
	unlisted := "## [v0.40.0] - 2026-01-01\n### Changed\n\n" +
		"- **controller (Breaking):** something else entirely changed.\n"
	got := LintChangelog(unlisted, fstest.MapFS{})
	if len(got) != 1 || got[0].Category != "missing-migration-link" {
		t.Fatalf("issues = %v, want one missing-migration-link: only the named entry is forgiven", categories(got))
	}

	listed := "## [v0.40.0] - 2026-01-01\n### Changed\n\n" +
		"- **controller (Breaking):** Revoking a token, rotating one, or deleting a user takes effect at once.\n"
	if got := LintChangelog(listed, fstest.MapFS{}); len(got) != 0 {
		t.Errorf("issues = %v, want none for the entry the exception names", categories(got))
	}
}

func TestPinnedMigrationLink_RefusesAPermalinkToAnotherRelease(t *testing.T) {
	body := "## [v0.40.0] - 2026-01-01\n### Changed\n\n" +
		"- **cli (Breaking):** scopes split. See [the migration\n" +
		"  guide](https://github.com/sparkwing-dev/sparkwing/blob/v0.39.0/docs/migrations/_unreleased.md#pipeline-name-charset).\n"
	got := LintChangelog(body, fstest.MapFS{})
	if len(got) != 1 || got[0].Category != "missing-migration-link" {
		t.Fatalf("issues = %v, want one missing-migration-link: the pin names a different release", categories(got))
	}
}
