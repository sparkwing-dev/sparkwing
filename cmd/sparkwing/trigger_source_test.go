package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// pushedAndLocalCommits makes a checkout whose origin holds one commit and
// whose HEAD is a second commit never pushed.
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
func TestCommitOnOriginSeesOnlyPushedCommits(t *testing.T) {
	work, pushed, local := pushedAndLocalCommits(t)
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

func TestOfferTriggerSourceRequiresPushedCommit(t *testing.T) {
	work, pushed, local := pushedAndLocalCommits(t)
	if err := offerTriggerSource(nil, work, "https://github.com/acme/app.git", pushed); err != nil {
		t.Fatalf("pushed commit = %v", err)
	}
	err := offerTriggerSource(nil, work, "https://github.com/acme/app.git", local)
	if err == nil || !strings.Contains(err.Error(), "--working-tree") {
		t.Fatalf("unpushed commit = %v, want working-tree guidance", err)
	}
}

func TestCronPinRejectsCommitOnlyOnAnotherRemote(t *testing.T) {
	work, _, local := pushedAndLocalCommits(t)
	gitInRepo(t, work, "update-ref", "refs/remotes/upstream/main", local)
	if err := checkPushableHead(work, false); err == nil || !strings.Contains(err.Error(), "Push the branch first") {
		t.Fatalf("cron pinned an unpushed origin commit: %v", err)
	}
}
