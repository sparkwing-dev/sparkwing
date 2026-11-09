package jobs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
	"github.com/sparkwing-dev/sparkwing/sparkwing/sparkwingtest"
)

func TestReleaseCollectsAndConsumesFragments(t *testing.T) {
	old := twoBreakingChangelog[strings.Index(twoBreakingChangelog, "## [v0.8.0]"):]
	repo := atomicReleaseRepo(t, "# Changelog\n\n## [Unreleased]\n\n"+old)
	notes := twoBreakingChangelog[strings.Index(twoBreakingChangelog, "### Changed"):strings.Index(twoBreakingChangelog, "## [v0.8.0]")]
	guide := twoBreakingGuide[strings.Index(twoBreakingGuide, "## Typed"):]
	for _, base := range []string{migrationsDirRel, mirrorMigrationsDirRel} {
		writeReleaseFixture(t, filepath.Join(repo, base, unreleasedGuideName), freshUnreleasedGuide)
	}
	fragmentDirs := []string{
		"docs/changes", "pkg/docs/mirror/changes",
		"docs/migrations/unreleased", "pkg/docs/mirror/migrations/unreleased",
	}
	for _, dir := range fragmentDirs {
		body := notes
		if strings.Contains(dir, "migrations") {
			body = guide
		}
		writeReleaseFixture(t, filepath.Join(repo, dir, "change.md"), body)
	}
	gate, err := os.ReadFile(filepath.Join("..", "..", "bin", "check-changelog.sh"))
	if err != nil {
		t.Fatal(err)
	}
	writeReleaseFixture(t, filepath.Join(repo, "bin", "check-changelog.sh"), string(gate))
	gitRun(t, repo, "add", ".")
	gitRun(t, repo, "commit", "--no-verify", "-m", "independent fragments")
	before := gitRun(t, repo, "rev-parse", "HEAD")
	ctx := newTestHost(t, sparkwingtest.Options{Outputs: map[string]json.RawMessage{"version": json.RawMessage(`"v0.9.0"`)}}).Context(t.Context())
	version := sparkwing.Ref[string]{JobID: "version"}
	prepare := prepareChangelogJob{RepoDir: repo, Version: version}
	if err := prepare.run(ctx); err != nil {
		t.Fatal(err)
	}
	if before != gitRun(t, repo, "rev-parse", "HEAD") {
		t.Fatal("preparation committed before gates")
	}
	for _, dir := range fragmentDirs {
		if _, err := os.Stat(filepath.Join(repo, dir, "change.md")); !os.IsNotExist(err) {
			t.Fatalf("fragment survived: %s, %v", dir, err)
		}
		if diff := gitRun(t, repo, "diff", "--cached", "--no-renames", "--diff-filter=D", "--name-only"); !strings.Contains(diff, dir+"/change.md") {
			t.Fatalf("fragment deletion not staged: %s", diff)
		}
	}
	body := readRepoFile(t, repo, "CHANGELOG.md")
	if strings.Contains(body, "_unreleased.md#") || strings.Count(body, "**sdk (Breaking):**") != 1 || !strings.HasSuffix(body, old) {
		t.Fatalf("collected release or historical notes changed: %s", body)
	}
	if err := checkMigrationGuide(ctx, repo, "v0.9.0"); err != nil {
		t.Fatal(err)
	}
	if err := checkChangelogSection(ctx, repo, "v0.9.0"); err != nil {
		t.Fatal(err)
	}
	staged := gitRun(t, repo, "diff", "--cached")
	if err := prepare.run(ctx); err != nil {
		t.Fatal(err)
	}
	if got := gitRun(t, repo, "diff", "--cached"); got != staged {
		t.Fatal("retry changed the prepared release")
	}
	writeReleaseFixture(t, filepath.Join(repo, scaffoldFallbackRel), "package scaffold\nconst FallbackSDKVersion = \"v0.9.0\"\n")
	t.Setenv("BASE_REF", strings.TrimSpace(before))
	for _, invalid := range []string{
		"# Changelog\n\n## [Unreleased]\n\n" + old,
		"# Changelog\n\n## [Unreleased]\n\n## [v0.9.0] - 2026-03-04\n\n" + old,
	} {
		if err := writeChangelogPair(repo, invalid); err != nil {
			t.Fatal(err)
		}
		if _, err := captureCmd(t.Context(), repo, "bash", "bin/check-changelog.sh"); err == nil {
			t.Fatal("consumed fragments exempted a release without versioned entries")
		}
	}
	if err := writeChangelogPair(repo, body); err != nil {
		t.Fatal(err)
	}
	if out, err := captureCmd(t.Context(), repo, "bash", "bin/check-changelog.sh"); err != nil {
		t.Fatalf("fragment gate refuses the staged release: %v\n%s", err, out)
	}
	if err := (&commitChangelogJob{RepoDir: repo, Version: version, SDKVersion: version}).run(ctx); err != nil {
		t.Fatal(err)
	}
	if gitRun(t, repo, "status", "--porcelain") != "" {
		t.Fatal("release left uncommitted fragments or notes")
	}
	if out, err := captureCmd(t.Context(), repo, "bash", "bin/check-changelog.sh"); err != nil {
		t.Fatalf("fragment gate refuses the committed release: %v\n%s", err, out)
	}
	t.Setenv("BASE_REF", strings.TrimSpace(gitRun(t, repo, "rev-parse", "HEAD")))
	writeReleaseFixture(t, filepath.Join(repo, scaffoldFallbackRel), "package scaffold\nconst FallbackSDKVersion = \"v0.9.1\"\n")
	if _, err := captureCmd(t.Context(), repo, "bash", "bin/check-changelog.sh"); err == nil {
		t.Fatal("a previous release exempted a new covered change without a fragment")
	}
}

func writeReleaseFixture(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, body)
}

func TestReleasePreservesUnclaimedMigrationFragments(t *testing.T) {
	repo := atomicReleaseRepo(t, fixtureChangelog)
	path := filepath.Join(repo, migrationsDirRel, "unreleased", "orphan.md")
	writeReleaseFixture(t, path, "## Keep this prose\n\nUpgrade instructions.\n")
	ctx := newTestHost(t, sparkwingtest.Options{Outputs: map[string]json.RawMessage{"version": json.RawMessage(`"v0.9.0"`)}}).Context(t.Context())
	before := readRepoFile(t, repo, "CHANGELOG.md")
	err := (&prepareChangelogJob{RepoDir: repo, Version: sparkwing.Ref[string]{JobID: "version"}}).run(ctx)
	if err == nil || !strings.Contains(err.Error(), "migration fragments need") {
		t.Fatalf("release = %v", err)
	}
	if _, err := os.Stat(path); err != nil || readRepoFile(t, repo, "CHANGELOG.md") != before {
		t.Fatal("refused release changed notes")
	}
}
