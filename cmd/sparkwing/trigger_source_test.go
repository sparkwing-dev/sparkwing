package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func pushedAndLocalCommits(t *testing.T) (work, pushed, local string) {
	t.Helper()
	origin := filepath.Join(t.TempDir(), "origin.git")
	gitInRepo(t, filepath.Dir(origin), "init", "--quiet", "--bare", origin)
	work = t.TempDir()
	gitInRepo(t, work, "init", "--quiet", "--initial-branch=main")
	gitInRepo(t, work, "remote", "add", "origin", origin)
	commit := func(msg string) string {
		gitInRepo(t, work, "-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgSign=false",
			"commit", "--quiet", "--allow-empty", "-m", msg)
		out, err := exec.Command("git", "-C", work, "rev-parse", "HEAD").Output()
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(out))
	}
	pushed = commit("pushed")
	gitInRepo(t, work, "push", "--quiet", "origin", "main")
	local = commit("local only")
	return work, pushed, local
}

// A team runner fetches the triggered commit from origin, so only a commit a
// remote-tracking branch of origin contains counts as fetchable.
func TestTriggerPreflightSeesOnlyPushedCommits(t *testing.T) {
	work, pushed, local := pushedAndLocalCommits(t)
	check := func(directory, sha string) error {
		selected := preparedTriggerSource{git: runspec.Git{SHA: sha}, source: runspec.Source{Ref: sha}, checkout: triggerCheckout{directory: directory, environment: os.Environ()}}
		return selected.checkReachable(t.Context())
	}
	if err := check(work, pushed); err != nil {
		t.Fatalf("pushed source refused: %v", err)
	}
	for _, source := range []struct{ directory, sha string }{{work, local}, {work, ""}, {filepath.Join(os.TempDir(), "no-such-checkout-"+pushed[:8]), pushed}} {
		if err := check(source.directory, source.sha); err == nil {
			t.Fatal("unreachable source accepted")
		}
	}
}

func TestTriggerPreflightRejectsCommitOnlyOnAnotherRemote(t *testing.T) {
	work, _, local := pushedAndLocalCommits(t)
	gitInRepo(t, work, "update-ref", "refs/remotes/upstream/main", local)
	selection := preparedTriggerSource{git: runspec.Git{SHA: local}, source: runspec.Source{Ref: local}, checkout: triggerCheckout{directory: work, environment: os.Environ()}}
	if err := selection.checkReachable(t.Context()); err == nil || !strings.Contains(err.Error(), "not on origin") {
		t.Fatalf("preflight accepted an unpushed origin commit: %v", err)
	}
}
