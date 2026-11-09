package docs

import (
	"strings"
	"testing"
	"testing/fstest"
)

func TestCollectChangelogGroupsFragments(t *testing.T) {
	released := "## [v0.1.0] - 2026-01-01\n\n### Fixed\n\n- **old:** Keep\n"
	body := "# Changelog\n\n## [Unreleased]\n\n" + released
	sources := fstest.MapFS{
		"changes/z.md": {Data: []byte("### Fixed\n\n- **z:** Last\n")},
		"changes/a.md": {Data: []byte("### Fixed\n\n- **a:** First\n\n### Added\n\n- **new:** Added\n")},
	}
	got, err := CollectChangelog(body, sources)
	if err != nil {
		t.Fatal(err)
	}
	unreleased := strings.Split(got, "## [v0.1.0]")[0]
	if strings.Count(unreleased, "### Fixed") != 1 || strings.Index(unreleased, "**a:**") > strings.Index(unreleased, "**z:**") {
		t.Fatalf("category order or grouping: %s", got)
	}
	if !strings.HasSuffix(got, released) {
		t.Fatal("released notes changed")
	}
	for _, fragment := range []string{"", "### Fixed\n", "### Unknown\n\n- entry", "## Wrong\n", "prose"} {
		sources["changes/bad.md"] = &fstest.MapFile{Data: []byte(fragment)}
		if _, err := CollectChangelog(body, sources); err == nil {
			t.Errorf("accepted fragment %q", fragment)
		}
	}
}

func TestCollectMigrationRejectsInvalidFragments(t *testing.T) {
	sources := fstest.MapFS{
		"_unreleased.md":  {Data: []byte("# Unreleased migrations\n\nNo breaking changes so far.\n")},
		"unreleased/a.md": {Data: []byte("## First\n\nUpgrade first.\n")},
		"unreleased/b.md": {Data: []byte("## Second\n\nUpgrade second.\n")},
	}
	got, err := CollectMigration(sources)
	if err != nil || strings.Contains(string(got), "No breaking") || strings.Count(string(got), "## ") != 2 {
		t.Fatalf("collected = %s, %v", got, err)
	}
	for _, fragment := range []string{"", "# Wrong\n\nText", "## No prose", "## First\n\nDuplicate", "## `First`!\n\nSame anchor"} {
		sources["unreleased/b.md"] = &fstest.MapFile{Data: []byte(fragment)}
		if _, err := CollectMigration(sources); err == nil {
			t.Errorf("accepted fragment %q", fragment)
		}
	}
}

func TestFragmentTopicsAreComposedAndCatalogued(t *testing.T) {
	if changelogErr != nil {
		t.Fatal(changelogErr)
	}
	for _, entry := range List() {
		if strings.HasPrefix(entry.Slug, "changes/") || strings.HasPrefix(entry.Slug, "migrations/unreleased/") {
			t.Fatalf("fragment topic leaked: %s", entry.Slug)
		}
		if entry.Slug == "migrations/_unreleased" {
			body, err := ReadRaw(entry.Slug)
			if err != nil || entry.Bytes != len(body) {
				t.Fatalf("metadata bytes = %d, body = %d, %v", entry.Bytes, len(body), err)
			}
		}
	}
	if _, err := ReadRaw("changes/release-note-fragments"); err == nil {
		t.Fatal("individual fragment is a readable topic")
	}
}
