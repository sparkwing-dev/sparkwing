package jobs

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
	"github.com/sparkwing-dev/sparkwing/sparkwing/sparkwingtest"
)

func runReleaseStateCheck(t *testing.T, repo, name string) error {
	t.Helper()
	t.Chdir(repo)
	host, plan, doc := prepareWork(t, &releaseCutChecksJob{})
	if findStep(doc, name) == nil {
		t.Fatalf("release class has no %s check", name)
	}
	_, err := host.RunStep(t.Context(), plan, "job", name)
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
			ctx := newTestHost(t, sparkwingtest.Options{Outputs: map[string]json.RawMessage{"version": json.RawMessage(`"v0.2.0"`)}}).Context(t.Context())
			job := prepareChangelogJob{RepoDir: repo, Version: sparkwing.Ref[string]{JobID: "version"}}
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
