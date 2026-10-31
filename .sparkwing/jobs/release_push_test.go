package jobs

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
	"github.com/sparkwing-dev/sparkwing/sparkwing/sparkwingtest"
)

func TestReleasePushFromDetachedHEADIsAtomic(t *testing.T) {
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	for _, failure := range []string{"", "tag", "push", "missing commit"} {
		t.Run("failure="+failure, func(t *testing.T) {
			repo := atomicReleaseRepo(t, fixtureChangelog)
			snapshot := filepath.Join(t.TempDir(), "snapshot")
			gitRun(t, repo, "worktree", "add", "--detach", snapshot, "HEAD")
			gitRun(t, repo, "-c", "user.name=Test", "-c", "user.email=test@example.invalid",
				"-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null", "commit", "--allow-empty", "-m", "release commit")
			commit := strings.TrimSpace(gitRun(t, repo, "rev-parse", "HEAD"))
			t.Setenv("RELEASE_TEST_COMMIT", commit)
			t.Setenv("RELEASE_TEST_REAL_GIT", realGit)
			t.Chdir(snapshot)
			logPath := filepath.Join(repo, "git-calls")
			t.Setenv("RELEASE_TEST_GIT_LOG", logPath)
			t.Setenv("RELEASE_TEST_FAILURE", failure)
			bin := filepath.Join(repo, "bin")
			writeGoFile(t, filepath.Join(bin, "git"), `#!/bin/sh
printf '%s\n' "$*" >> "$RELEASE_TEST_GIT_LOG"
case "$*" in
  'ls-remote --tags origin refs/tags/v0.99.0') exit 0 ;;
  'rev-parse --abbrev-ref HEAD') exec "$RELEASE_TEST_REAL_GIT" "$@" ;;
  "tag -a v0.99.0 $RELEASE_TEST_COMMIT -m Release v0.99.0")
    test "$RELEASE_TEST_FAILURE" != tag ;;
  "push --atomic origin $RELEASE_TEST_COMMIT:refs/heads/main refs/tags/v0.99.0")
    test "$RELEASE_TEST_FAILURE" != push ;;
  *) echo "unexpected git call: $*" >&2; exit 1 ;;
esac
`)
			if err := os.Chmod(filepath.Join(bin, "git"), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			output := commit
			if failure == "missing commit" {
				output = ""
			}
			ctx := newTestHost(t, sparkwingtest.Options{Outputs: map[string]json.RawMessage{
				"version": json.RawMessage(`"v0.99.0"`),
				"commit":  json.RawMessage(`"` + output + `"`),
			}}).Context(t.Context())
			job := pushTagJob{
				RepoDir: snapshot,
				Version: sparkwing.Ref[string]{JobID: "version"},
				Commit:  sparkwing.Ref[string]{JobID: "commit"},
			}
			err := job.run(ctx)
			if (err != nil) != (failure != "") {
				t.Fatalf("push failure %q: %v", failure, err)
			}
			calls, readErr := os.ReadFile(logPath)
			if readErr != nil {
				t.Fatal(readErr)
			}
			wantPushes := 1
			if failure == "tag" || failure == "missing commit" {
				wantPushes = 0
			}
			if failure == "missing commit" && strings.Contains(string(calls), "tag -a") {
				t.Fatalf("missing release commit fell back to HEAD:\n%s", calls)
			}
			if pushes := strings.Count(string(calls), "push "); pushes != wantPushes {
				t.Fatalf("got %d pushes, want %d after %q failure:\n%s", pushes, wantPushes, failure, calls)
			}
			if strings.Contains(string(calls), "rev-parse --abbrev-ref HEAD") {
				t.Fatalf("push still depends on the detached HEAD:\n%s", calls)
			}
		})
	}
}

func TestReleasePushRunsOnTheDispatcherHost(t *testing.T) {
	if !mustNode(t, releasePlan(t), "push-tag").Inline {
		t.Fatal("push-tag would run in a detached job snapshot instead of the release checkout")
	}
}
