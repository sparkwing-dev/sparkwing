package jobs

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func runReleaseStateCheck(t *testing.T, repo, name string) error {
	t.Helper()
	previous := sparkwing.CurrentRuntime().WorkDir
	sparkwing.SetWorkDir(repo)
	defer sparkwing.SetWorkDir(previous)
	w := sparkwing.NewWork()
	if _, err := (&releaseCutChecksJob{}).Work(w); err != nil {
		t.Fatal(err)
	}
	if w.StepByID(name) == nil {
		t.Fatalf("release class has no %s check", name)
	}
	for _, step := range w.Steps() {
		if step.ID() != name && step.ID() != budgetStepID {
			step.SkipIf(func(context.Context) bool { return true })
		}
	}
	_, err := sparkwing.RunWork(t.Context(), w)
	return err
}

func TestReleaseStateChecksRefuseStalePublishedVersions(t *testing.T) {
	repo := seedProxyPinnedRepo(t, false)
	origin := t.TempDir()
	gitRun(t, origin, "init", "--bare", "-b", "main")
	gitRun(t, repo, "remote", "add", "origin", origin)
	gitRun(t, repo, "push", "origin", "main", "--tags")
	err := runReleaseStateCheck(t, repo, "version-freshness")
	if err == nil || !strings.Contains(err.Error(), fixturePhantomVersion) {
		t.Fatalf("stale published pin refusal = %v, want %s", err, fixturePhantomVersion)
	}
}

func TestReleaseStateChecksRefuseIncoherentPins(t *testing.T) {
	repo := seedReleaseRepo(t)
	if err := runReleaseStateCheck(t, repo, "sdk-pins"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(repo, filepath.FromSlash(kubernetesE2EPipelineModuleRel), "go.mod")
	writeFile(t, path, strings.ReplaceAll(fixtureKubernetesE2EPipelinesGoMod, "v0.1.0", "v0.0.9"))
	err := runReleaseStateCheck(t, repo, "sdk-pins")
	if err == nil || !strings.Contains(err.Error(), kubernetesE2EPipelineModuleRel) {
		t.Fatalf("fixture-only pin drift refusal = %v", err)
	}
}

func TestReleaseStateChecksRefuseBrokenChangelogLinks(t *testing.T) {
	repo := seedReleaseRepo(t)
	writeFile(t, filepath.Join(repo, "CHANGELOG.md"), "# Changelog\n\n## [Unreleased]\n\n### Changed\n\n- **checks:** Read [the guide](docs/missing.md).\n")
	err := runReleaseStateCheck(t, repo, "changelog-links")
	if err == nil || !strings.Contains(err.Error(), "docs/missing.md") {
		t.Fatalf("broken changelog link refusal = %v", err)
	}
}

func TestReleaseFreshnessDoesNotRequireTheLatestMain(t *testing.T) {
	repo := seedReleaseRepo(t)
	old := strings.TrimSpace(gitRun(t, repo, "rev-parse", "HEAD"))
	gitRun(t, repo, "tag", "v0.1.0")
	writeFile(t, filepath.Join(repo, "doc.go"), "package sparkwing\n\nconst NewerMain = true\n")
	gitRun(t, repo, "add", "doc.go")
	gitRun(t, repo, "commit", "-m", "newer main")
	gitRun(t, repo, "push", "origin", "main", "--tags")
	gitRun(t, repo, "checkout", "--detach", old)
	if err := runReleaseStateCheck(t, repo, "version-freshness"); err != nil {
		t.Fatalf("release refused an older checkout with current published pins: %v", err)
	}
	if err := CheckVersionsFreshness(t.Context(), repo); err == nil || !strings.Contains(err.Error(), "behind origin/main") {
		t.Fatalf("development freshness lost its local replacement check: %v", err)
	}
}

func TestReleasePreparationRefusesMissingNotesBeforeCommit(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"empty changelog", "# Changelog\n\n## [Unreleased]\n", "[Unreleased] is empty"},
		{"missing migration", "# Changelog\n\n## [Unreleased]\n\n### Changed\n\n- **sdk (Breaking):** The interface changed.\n", "docs/migrations/_unreleased.md"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := seedReleaseRepo(t)
			writeFile(t, filepath.Join(repo, "CHANGELOG.md"), tc.body)
			before := gitRun(t, repo, "rev-parse", "HEAD")
			ctx := context.WithValue(t.Context(), sparkwing.RuntimePlumbing.Keys.JSONRefResolver,
				func(id string) ([]byte, bool) { return []byte(`"v0.2.0"`), id == "version" })
			job := prepareChangelogJob{RepoDir: repo, Version: sparkwing.Ref[string]{NodeID: "version"}}
			err := job.run(ctx)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("release preparation refusal = %v, want %q", err, tc.want)
			}
			if got := gitRun(t, repo, "rev-parse", "HEAD"); got != before {
				t.Fatal("refused release preparation committed a change")
			}
		})
	}
}
