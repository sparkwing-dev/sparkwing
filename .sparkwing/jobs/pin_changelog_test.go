package jobs

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPinBumpWritesOnlyItsFragment(t *testing.T) {
	repo := t.TempDir()
	writeReleaseFixture(t, filepath.Join(repo, "CHANGELOG.md"), fixtureChangelog)
	writeReleaseFixture(t, filepath.Join(repo, "docs/changes/other.md"), "### Fixed\n\n- **other:** Keep\n")
	for range 2 {
		if err := recordPinBumpInChangelog(repo, "v0.19.0"); err != nil {
			t.Fatal(err)
		}
	}
	body, err := readChangelog(repo)
	// safety: this fixture has aggregate notes to prove the updater leaves them untouched.
	if err == nil || !strings.Contains(err.Error(), "move CHANGELOG") {
		t.Fatalf("aggregate validation = %s, %v", body, err)
	}
	raw, err := os.ReadFile(filepath.Join(repo, "CHANGELOG.md"))
	if err != nil || string(raw) != fixtureChangelog {
		t.Fatal("aggregate changed")
	}
	fragment, err := os.ReadFile(filepath.Join(repo, pinBumpFragmentPaths("v0.19.0")[0]))
	if err != nil || strings.Count(string(fragment), "pins v0.19.0") != 1 {
		t.Fatalf("fragment = %s, %v", fragment, err)
	}
	if err := recordPinBumpInChangelog(repo, "../bad"); err == nil {
		t.Fatal("accepted unsafe version")
	}
}

func TestPinBumpRollbackPreservesOtherFragments(t *testing.T) {
	repo := seedReleaseRepo(t)
	writeReleaseFixture(t, filepath.Join(repo, "docs/changes/other.md"), "### Fixed\n\n- **other:** Keep\n")
	if err := requireCleanSparkwingPinArtifacts(t.Context(), repo, "v0.2.0"); err != nil {
		t.Fatal(err)
	}
	for _, path := range pinBumpFragmentPaths("v0.2.0") {
		writeReleaseFixture(t, filepath.Join(repo, path), "owned")
	}
	gitRun(t, repo, "add", "--", pinBumpFragmentPaths("v0.2.0")[0])
	if err := restoreSparkwingPinArtifacts(context.Background(), repo, "v0.2.0"); err != nil {
		t.Fatal(err)
	}
	for _, path := range pinBumpFragmentPaths("v0.2.0") {
		if _, err := os.Stat(filepath.Join(repo, path)); !os.IsNotExist(err) {
			t.Fatalf("owned fragment survived: %v", err)
		}
	}
	if got := readRepoFile(t, repo, "docs/changes/other.md"); !strings.Contains(got, "**other:** Keep") {
		t.Fatal("other fragment changed")
	}
	if staged := gitRun(t, repo, "diff", "--cached", "--name-only"); staged != "" {
		t.Fatalf("rollback left staged paths: %s", staged)
	}
}
