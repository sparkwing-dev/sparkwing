package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A team runner fetches the triggered commit from origin, so only a commit a
// remote-tracking branch of origin contains counts as fetchable.
func TestCommitOnOriginSeesOnlyPushedCommits(t *testing.T) {
	origin := filepath.Join(t.TempDir(), "origin.git")
	gitInRepo(t, filepath.Dir(origin), "init", "--quiet", "--bare", origin)
	work := t.TempDir()
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

	pushed := commit("pushed")
	gitInRepo(t, work, "push", "--quiet", "origin", "main")
	local := commit("local only")

	if !commitOnOrigin(work, pushed) {
		t.Errorf("commitOnOrigin(%s) = false for a pushed commit", pushed)
	}
	if commitOnOrigin(work, local) {
		t.Errorf("commitOnOrigin(%s) = true for a commit that was never pushed", local)
	}
	if commitOnOrigin(work, "") {
		t.Error("commitOnOrigin accepted an empty commit")
	}
	if commitOnOrigin(filepath.Join(os.TempDir(), "no-such-checkout-"+pushed[:8]), pushed) {
		t.Error("commitOnOrigin accepted a directory that is not a checkout")
	}
}
