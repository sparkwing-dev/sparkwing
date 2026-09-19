package jobs

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const twoBreakingChangelog = `# Changelog

## [Unreleased]

## [v0.9.0] - 2026-03-04

### Changed

- **sdk (Breaking):** ` + "`Needs(...any)`" + ` becomes ` + "`Needs(...Dep)`" + `. Call sites
  that passed a node by name no longer compile. See [migration
  guide](docs/migrations/_unreleased.md#typed-dep-interface).

- **cache (Breaking):** ` + "`CacheOptions.Namespace`" + ` splits into ` + "`Cache`" + ` and
  ` + "`Concurrency`" + `. Throttled nodes no longer replay each other's results. See
  [migration guide](docs/migrations/_unreleased.md#cacheoptions-splits-in-two).

- **cli:** ` + "`sparkwing runs list`" + ` prints the profile column.

## [v0.8.0] - 2026-02-01

- **store:** an additive column.
`

const twoBreakingGuide = `# Migrating to the next release

Two breaking changes, both mechanical.

## Typed Dep interface

**Before:** ` + "`Needs(\"build\")`" + `.

**After:** ` + "`Needs(build)`" + `.

## CacheOptions splits in two

**Before:** one namespace meant both memoization and throttling.

**After:** two options.
`

const migrationIndexFixture = `# Migration guides

One guide per release that contains a breaking change.

## Releases

| Version | Date | Summary |
|---|---|---|
| [v0.8.0](v0.8.0.md) | 2026-02-01 | An older break. |
`

func migrationRepo(t *testing.T, changelog, guide, index string) string {
	t.Helper()
	dir := t.TempDir()
	for _, sub := range []string{migrationsDirRel, mirrorMigrationsDirRel, "pkg/docs"} {
		if err := os.MkdirAll(filepath.Join(dir, filepath.FromSlash(sub)), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}
	write := func(rel, body string) {
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(rel)), []byte(body), 0o644); err != nil {
			t.Fatalf("seed %s: %v", rel, err)
		}
	}
	write("CHANGELOG.md", changelog)
	write("pkg/docs/changelog.md", changelog)
	for _, base := range []string{migrationsDirRel, mirrorMigrationsDirRel} {
		write(base+"/"+unreleasedGuideName, guide)
		write(base+"/"+migrationIndexName, index)
		write(base+"/v0.8.0.md", "# Migrating to v0.8.0\n\n## An older break\n")
	}
	return dir
}

func readRepoFile(t *testing.T, dir, rel string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(body)
}

func TestReleaseCutRollsTheGuideForTwoBreakingEntries(t *testing.T) {
	dir := migrationRepo(t, twoBreakingChangelog, twoBreakingGuide, migrationIndexFixture)

	roll, err := planMigrationRollIn(dir, twoBreakingChangelog, "v0.9.0", "2026-03-04")
	if err != nil {
		t.Fatalf("planMigrationRollIn: %v", err)
	}
	if roll.guideOnDisk {
		t.Fatalf("a fixture with no v0.9.0.md reported the guide already written")
	}
	if !roll.needed || roll.breaking != 2 {
		t.Fatalf("roll = %+v, want needed with 2 breaking entries", roll)
	}
	if roll.repointed != 2 {
		t.Errorf("repointed %d links, want 2", roll.repointed)
	}
	if _, err := writeMigrationRoll(dir, roll); err != nil {
		t.Fatalf("writeMigrationRoll: %v", err)
	}
	if err := writeChangelogPair(dir, roll.changelogBody); err != nil {
		t.Fatalf("writeChangelogPair: %v", err)
	}

	guide := readRepoFile(t, dir, migrationsDirRel+"/v0.9.0.md")
	if !strings.HasPrefix(guide, "# Migrating to v0.9.0\n") {
		t.Errorf("guide title = %q, want the version title", strings.SplitN(guide, "\n", 2)[0])
	}
	for _, heading := range []string{"## Typed Dep interface", "## CacheOptions splits in two"} {
		if !strings.Contains(guide, heading) {
			t.Errorf("guide lost %q", heading)
		}
	}

	if got := readRepoFile(t, dir, migrationsDirRel+"/"+unreleasedGuideName); got != freshUnreleasedGuide {
		t.Errorf("_unreleased.md = %q, want the fresh placeholder", got)
	}

	index := readRepoFile(t, dir, migrationsDirRel+"/"+migrationIndexName)
	if !indexHasVersionRow(index, "v0.9.0") {
		t.Fatalf("index carries no v0.9.0 row:\n%s", index)
	}
	row := indexRow(t, index, "v0.9.0")
	if !strings.Contains(row, "2026-03-04") {
		t.Errorf("index row %q carries no release date", row)
	}
	for _, want := range []string{"`Needs(...any)` becomes `Needs(...Dep)`", "`CacheOptions.Namespace` splits into"} {
		if !strings.Contains(row, want) {
			t.Errorf("index row %q does not summarize %q", row, want)
		}
	}
	if !strings.Contains(index, "[v0.8.0](v0.8.0.md)") {
		t.Errorf("index dropped the older row:\n%s", index)
	}

	changelog := readRepoFile(t, dir, "CHANGELOG.md")
	if strings.Contains(changelog, "docs/migrations/_unreleased.md") {
		t.Errorf("a released section still links the unreleased guide:\n%s", changelog)
	}
	if got := strings.Count(changelog, "docs/migrations/v0.9.0.md#"); got != 2 {
		t.Errorf("changelog carries %d links to the rolled guide, want 2", got)
	}

	for _, name := range []string{"v0.9.0.md", unreleasedGuideName, migrationIndexName} {
		source := readRepoFile(t, dir, migrationsDirRel+"/"+name)
		mirror := readRepoFile(t, dir, mirrorMigrationsDirRel+"/"+name)
		if source != mirror {
			t.Errorf("%s drifted from its mirror copy", name)
		}
	}

	if err := CheckChangelogLint(grantedCtx(context.Background()), dir); err != nil {
		t.Fatalf("the rolled tree still fails the changelog check that refuses commits:\n%v", err)
	}
	if err := checkMigrationGuide(grantedCtx(context.Background()), dir, "v0.9.0"); err != nil {
		t.Fatalf("release-verify refuses the tree the cut just rolled: %v", err)
	}
}

func indexRow(t *testing.T, index, version string) string {
	t.Helper()
	for _, line := range strings.Split(index, "\n") {
		if strings.HasPrefix(line, "| ["+version+"]") {
			return line
		}
	}
	t.Fatalf("index carries no row for %s", version)
	return ""
}

func TestReleaseCutRefusesABreakingEntryWithNoMigrationLink(t *testing.T) {
	changelog := `# Changelog

## [v0.9.0] - 2026-03-04

### Changed

- **cache (Breaking):** ` + "`FETCH_INTERVAL`" + ` now defaults to ` + "`0`" + `, so a cache
  started with no flags stops polling.
`
	dir := migrationRepo(t, changelog, freshUnreleasedGuide, migrationIndexFixture)

	_, err := planMigrationRollIn(dir, changelog, "v0.9.0", "2026-03-04")
	if err == nil {
		t.Fatalf("the cut accepted a (Breaking) entry with no migration link")
	}
	for _, want := range []string{"CHANGELOG.md:7", "missing-migration-link", "written by a person"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not name %q:\n%v", want, err)
		}
	}
	if _, statErr := os.Stat(filepath.Join(dir, filepath.FromSlash(migrationsDirRel), "v0.9.0.md")); statErr == nil {
		t.Errorf("the refused cut wrote a guide anyway")
	}
}

func TestReleaseCutRefusesAnAnchorTheGuideDoesNotCarry(t *testing.T) {
	changelog := `# Changelog

## [v0.9.0] - 2026-03-04

- **sdk (Breaking):** the shape changed. See [migration
  guide](docs/migrations/_unreleased.md#no-such-section).
`
	dir := migrationRepo(t, changelog, twoBreakingGuide, migrationIndexFixture)

	_, err := planMigrationRollIn(dir, changelog, "v0.9.0", "2026-03-04")
	if err == nil {
		t.Fatalf("the cut accepted a link to an anchor the guide does not carry")
	}
	if !strings.Contains(err.Error(), "#no-such-section") || !strings.Contains(err.Error(), "#typed-dep-interface") {
		t.Errorf("refusal names neither the dead anchor nor the ones the guide carries:\n%v", err)
	}
}

func TestReleaseCutRefusesALinkToAnotherReleasesGuide(t *testing.T) {
	changelog := `# Changelog

## [v0.9.0] - 2026-03-04

- **sdk (Breaking):** the shape changed. See [migration
  guide](docs/migrations/v0.8.0.md#an-older-break).
`
	dir := migrationRepo(t, changelog, twoBreakingGuide, migrationIndexFixture)

	_, err := planMigrationRollIn(dir, changelog, "v0.9.0", "2026-03-04")
	if err == nil {
		t.Fatalf("the cut accepted a link to a guide this release does not roll")
	}
	if !strings.Contains(err.Error(), "version-mismatch") || !strings.Contains(err.Error(), "docs/migrations/v0.9.0.md") {
		t.Errorf("refusal does not name the guide the release rolls:\n%v", err)
	}
}

func TestReleaseCutRefusesALinkWithNoAnchor(t *testing.T) {
	changelog := `# Changelog

## [v0.9.0] - 2026-03-04

- **sdk (Breaking):** the shape changed. See [migration
  guide](docs/migrations/_unreleased.md).
`
	dir := migrationRepo(t, changelog, twoBreakingGuide, migrationIndexFixture)

	_, err := planMigrationRollIn(dir, changelog, "v0.9.0", "2026-03-04")
	if err == nil {
		t.Fatalf("the cut accepted a migration link with no anchor")
	}
	if !strings.Contains(err.Error(), "missing-migration-anchor") {
		t.Errorf("refusal does not name the missing anchor:\n%v", err)
	}
}

func TestReleaseCutRollsNoGuideWithoutABreakingEntry(t *testing.T) {
	changelog := `# Changelog

## [v0.9.0] - 2026-03-04

- **cli:** a new column.
`
	dir := migrationRepo(t, changelog, freshUnreleasedGuide, migrationIndexFixture)

	roll, err := planMigrationRollIn(dir, changelog, "v0.9.0", "2026-03-04")
	if err != nil {
		t.Fatalf("planMigrationRollIn: %v", err)
	}
	if roll.needed {
		t.Fatalf("roll = %+v, want no guide for a release with no breaking entry", roll)
	}
	if indexHasVersionRow(readRepoFile(t, dir, migrationsDirRel+"/"+migrationIndexName), "v0.9.0") {
		t.Errorf("a release with no breaking entry took an index row")
	}
}

func TestReleaseCutStillRepointsAndIndexesAPreRolledGuide(t *testing.T) {
	dir := migrationRepo(t, twoBreakingChangelog, twoBreakingGuide, migrationIndexFixture)
	handRolled := twoBreakingGuide
	for _, base := range []string{migrationsDirRel, mirrorMigrationsDirRel} {
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(base), "v0.9.0.md"), []byte(handRolled), 0o644); err != nil {
			t.Fatalf("seed the hand-rolled guide: %v", err)
		}
	}

	roll, err := planMigrationRollIn(dir, twoBreakingChangelog, "v0.9.0", "2026-03-04")
	if err != nil {
		t.Fatalf("planMigrationRollIn: %v", err)
	}
	if !roll.guideOnDisk || !roll.needed {
		t.Fatalf("roll = %+v, want the remaining work planned against the guide on disk", roll)
	}
	if roll.repointed != 2 {
		t.Errorf("repointed %d links, want 2 even though the guide was already written", roll.repointed)
	}
	if _, err := writeMigrationRoll(dir, roll); err != nil {
		t.Fatalf("writeMigrationRoll: %v", err)
	}
	if err := writeChangelogPair(dir, roll.changelogBody); err != nil {
		t.Fatalf("writeChangelogPair: %v", err)
	}

	guide := readRepoFile(t, dir, migrationsDirRel+"/v0.9.0.md")
	if !strings.HasPrefix(guide, "# Migrating to v0.9.0\n") {
		t.Errorf("the cut left the placeholder title on the rolled guide: %q", strings.SplitN(guide, "\n", 2)[0])
	}
	for _, heading := range []string{"## Typed Dep interface", "## CacheOptions splits in two"} {
		if !strings.Contains(guide, heading) {
			t.Errorf("the cut lost %q from a guide a person had written", heading)
		}
	}
	if strings.Contains(guide, "Two breaking changes, both mechanical.") == false {
		t.Errorf("the cut rewrote the prose a person had written:\n%s", guide)
	}
	changelog := readRepoFile(t, dir, "CHANGELOG.md")
	if strings.Contains(changelog, "docs/migrations/_unreleased.md") {
		t.Errorf("the released section still links the unreleased guide:\n%s", changelog)
	}
	index := readRepoFile(t, dir, migrationsDirRel+"/"+migrationIndexName)
	if !indexHasVersionRow(index, "v0.9.0") {
		t.Errorf("the pre-rolled release took no index row:\n%s", index)
	}
	if got := readRepoFile(t, dir, migrationsDirRel+"/"+unreleasedGuideName); got != freshUnreleasedGuide {
		t.Errorf("_unreleased.md still holds the rolled sections: %q", got)
	}
	if err := CheckChangelogLint(grantedCtx(context.Background()), dir); err != nil {
		t.Fatalf("a pre-rolled cut leaves a tree that cannot be committed:\n%v", err)
	}
	if err := checkMigrationGuide(grantedCtx(context.Background()), dir, "v0.9.0"); err != nil {
		t.Fatalf("release-verify refuses the pre-rolled tree: %v", err)
	}
}

func TestReleaseVerifyRefusesATagStillLinkingTheUnreleasedGuide(t *testing.T) {
	dir := migrationRepo(t, twoBreakingChangelog, twoBreakingGuide, migrationIndexFixture)

	err := checkMigrationGuide(grantedCtx(context.Background()), dir, "v0.9.0")
	if err == nil {
		t.Fatalf("release-verify accepted a tag whose breaking entries link _unreleased.md")
	}
	if !strings.Contains(err.Error(), "version-mismatch") {
		t.Errorf("refusal does not name the mismatched link:\n%v", err)
	}
}

func TestReleaseVerifyRefusesAGuideMissingFromTheIndex(t *testing.T) {
	dir := migrationRepo(t, twoBreakingChangelog, twoBreakingGuide, migrationIndexFixture)
	roll, err := planMigrationRollIn(dir, twoBreakingChangelog, "v0.9.0", "2026-03-04")
	if err != nil {
		t.Fatalf("planMigrationRollIn: %v", err)
	}
	roll.indexBody = migrationIndexFixture
	if _, err := writeMigrationRoll(dir, roll); err != nil {
		t.Fatalf("writeMigrationRoll: %v", err)
	}
	if err := writeChangelogPair(dir, roll.changelogBody); err != nil {
		t.Fatalf("writeChangelogPair: %v", err)
	}

	err = checkMigrationGuide(grantedCtx(context.Background()), dir, "v0.9.0")
	if err == nil {
		t.Fatalf("release-verify accepted a guide the index does not list")
	}
	if !strings.Contains(err.Error(), "has no row for v0.9.0") {
		t.Errorf("refusal does not name the missing index row:\n%v", err)
	}
}

func TestBreakingSummaryTakesOneSentencePerEntry(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "cuts at the sentence a capital follows",
			body: "- **cli (Breaking):** The flag is gone. Use the other one.",
			want: "The flag is gone.",
		},
		{
			name: "a version number is not a sentence end",
			body: "- **sdk (Breaking):** Pins below v0.50.4 stop resolving. Bump them.",
			want: "Pins below v0.50.4 stop resolving.",
		},
		{
			name: "a period inside a code span is not a sentence end",
			body: "- **cli (Breaking):** A `.pem` file is refused now. Rename it.",
			want: "A `.pem` file is refused now.",
		},
		{
			name: "an abbreviation a lowercase word follows is not a sentence end",
			body: "- **sdk (Breaking):** Keyed literals are required, e.g. the holder struct. Rewrite them.",
			want: "Keyed literals are required, e.g. the holder struct.",
		},
		{
			name: "a digit before the period still ends a sentence",
			body: "- **store (Breaking):** Schema moves 13 -> 14. Upgrade every binary.",
			want: "Schema moves 13 -> 14.",
		},
		{
			name: "a version at the end of a sentence still ends it",
			body: "- **release (Breaking):** Ryan cut v0.50.4. The fix landed later.",
			want: "Ryan cut v0.50.4.",
		},
		{
			name: "a wrapped entry with no period keeps its whole body",
			body: "- **cli (Breaking):** the column\n  moved right",
			want: "the column moved right.",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := breakingSummary([]changelogEntry{{titleLine: 1, body: c.body}})
			if got != c.want {
				t.Fatalf("summary = %q, want %q", got, c.want)
			}
		})
	}
}

func TestBreakingSummaryJoinsEveryBreakingEntry(t *testing.T) {
	got := breakingSummary([]changelogEntry{
		{titleLine: 1, body: "- **cli (Breaking):** The flag is gone. More prose."},
		{titleLine: 5, body: "- **sdk (Breaking):** The signature changed. More prose."},
	})
	want := "The flag is gone; The signature changed."
	if got != want {
		t.Fatalf("summary = %q, want %q", got, want)
	}
}

func TestReleaseSectionDateReadsTheHeadingTheCutWrote(t *testing.T) {
	if got := releaseSectionDate(twoBreakingChangelog, "v0.9.0"); got != "2026-03-04" {
		t.Errorf("date = %q, want the heading's date", got)
	}
	if got := releaseSectionDate(twoBreakingChangelog, "v0.7.0"); got != "" {
		t.Errorf("date = %q, want empty for a section that is not there", got)
	}
}
