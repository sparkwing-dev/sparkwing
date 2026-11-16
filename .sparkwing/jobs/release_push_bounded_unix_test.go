//go:build unix

package jobs

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
	"github.com/sparkwing-dev/sparkwing/sparkwingtest"
)

type releasePushFixture struct {
	repo, origin, detached, source, commit string
}

// safety: the release commit for v0.99.0 is detached and on no remote, as commit-changelog leaves it.
func newReleasePushFixture(t *testing.T) releasePushFixture {
	t.Helper()
	repo := atomicReleaseRepo(t, fixtureChangelog)
	source := strings.TrimSpace(gitRun(t, repo, "rev-parse", "HEAD"))
	origin := t.TempDir()
	gitRun(t, origin, "init", "--bare", "-b", "main")
	gitRun(t, repo, "remote", "add", "origin", origin)
	gitRun(t, repo, "push", "origin", "main")
	detached := filepath.Join(t.TempDir(), "release")
	gitRun(t, repo, "worktree", "add", "--detach", detached, source)
	collected, err := readChangelog(detached)
	if err != nil {
		t.Fatal(err)
	}
	body, err := rewriteUnreleasedToVersion(string(collected), "v0.99.0", "2026-01-01")
	if err != nil {
		t.Fatal(err)
	}
	if err := writeChangelogPair(detached, body); err != nil {
		t.Fatal(err)
	}
	gitRun(t, detached, "add", "--", "CHANGELOG.md", embeddedChangelogRel)
	gitRun(t, detached, "commit", "--no-verify", "-m", "release notes")
	commit := strings.TrimSpace(gitRun(t, detached, "rev-parse", "HEAD"))
	return releasePushFixture{repo: repo, origin: origin, detached: detached, source: source, commit: commit}
}

func (f releasePushFixture) run(t *testing.T, host sparkwingtest.Host) error {
	t.Helper()
	return f.runWith(t, host, newReleaseGitRunner())
}

func (f releasePushFixture) runWith(t *testing.T, host sparkwingtest.Host, git releaseGitRunner) error {
	t.Helper()
	job := pushTagJob{RepoDir: f.detached, Source: f.source, Version: sparkwing.Ref[string]{JobID: "version"}, Commit: sparkwing.Ref[string]{JobID: "commit"}, git: git}
	return job.run(host.Context(t.Context()))
}

func releasePushHost(t *testing.T, commit string) sparkwingtest.Host {
	t.Helper()
	return newTestHost(t, sparkwingtest.Options{Outputs: map[string]json.RawMessage{
		"version": json.RawMessage(`"v0.99.0"`), "commit": json.RawMessage(`"` + commit + `"`),
	}})
}

// safety: a FIFO with no writer blocks its reader with no clock; cleanup opens and closes it to release a leaked one.
func blockingFIFO(t *testing.T) string {
	t.Helper()
	fifo := filepath.Join(t.TempDir(), "block")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if f, err := os.OpenFile(fifo, os.O_RDWR, 0); err == nil {
			_ = f.Close()
		}
	})
	return fifo
}

func TestReleasePushSkipsThePrePushHook(t *testing.T) {
	f := newReleasePushFixture(t)
	marker := filepath.Join(t.TempDir(), "hook-ran")
	fifo := blockingFIFO(t)
	// safety: the fixture disables hooks; the release checkout points
	// core.hooksPath at its own hooks, where sparkwing installs pre-push.
	gitRun(t, f.repo, "config", "core.hooksPath", filepath.Join(f.repo, ".git", "hooks"))
	writeDeliveryTool(t, filepath.Join(f.repo, ".git", "hooks", "pre-push"),
		"#!/bin/sh\ntouch '"+marker+"'\nexec cat '"+fifo+"'\n")
	host := releasePushHost(t, f.commit)
	if err := f.run(t, host); err != nil {
		t.Fatalf("publication: %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("the release push ran the repository's pre-push hook (stat %v)", err)
	}
	if got := strings.TrimSpace(gitRun(t, f.origin, "rev-parse", "v0.99.0^{commit}")); got != f.commit {
		t.Fatalf("origin tag = %s, want %s", got, f.commit)
	}
	if !releaseLogged(host, "pre-push hook") {
		t.Fatalf("publication did not say why it skips the hook: %+v", host.Records())
	}
}

func TestReleasePushNamesAHungGitCommand(t *testing.T) {
	f := newReleasePushFixture(t)
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	block := blockingFIFO(t)
	started := blockingFIFO(t)
	bin := t.TempDir()
	writeDeliveryTool(t, filepath.Join(bin, "git"), `#!/bin/sh
for arg in "$@"; do
 if [ "$arg" = push ]; then
  printf started > '`+started+`'
  exec cat '`+block+`'
 fi
done
exec '`+realGit+`' "$@"
`)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	// safety: the deadline fires once the push is known to be running, so the
	// test waits on no clock and still proves a hung push is killed and named.
	var mu sync.Mutex
	var expire context.CancelCauseFunc
	git := newReleaseGitRunner()
	git.deadline = func(ctx context.Context, _ time.Duration) (context.Context, context.CancelFunc) {
		bounded, cancel := context.WithCancelCause(ctx)
		mu.Lock()
		expire = cancel
		mu.Unlock()
		return bounded, func() { cancel(nil) }
	}
	go func() {
		ready, err := os.ReadFile(started)
		if err != nil || string(ready) != "started" {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if expire != nil {
			expire(context.DeadlineExceeded)
		}
	}()

	host := releasePushHost(t, f.commit)
	err = f.runWith(t, host, git)
	if err == nil || !strings.Contains(err.Error(), "git push --no-verify --atomic") || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("hung push error = %v, want it to name the push and the timeout", err)
	}
	if !releaseLogged(host, "git push --no-verify --atomic") {
		t.Fatalf("no progress line named the push before it ran: %+v", host.Records())
	}
	if strings.TrimSpace(gitRun(t, f.origin, "tag", "--list")) != "" {
		t.Fatal("a timed-out push published a tag")
	}
}

func TestReleasePushStaleLocalTag(t *testing.T) {
	t.Run("unpushed tag from an earlier attempt is replaced", func(t *testing.T) {
		f := newReleasePushFixture(t)
		gitRun(t, f.detached, "commit", "--allow-empty", "--no-verify", "-m", "abandoned attempt")
		abandoned := strings.TrimSpace(gitRun(t, f.detached, "rev-parse", "HEAD"))
		gitRun(t, f.repo, "tag", "-a", "v0.99.0", abandoned, "-m", "Release v0.99.0")
		if err := f.run(t, releasePushHost(t, f.commit)); err != nil {
			t.Fatalf("retry over an unpushed tag: %v", err)
		}
		if got := strings.TrimSpace(gitRun(t, f.origin, "rev-parse", "v0.99.0^{commit}")); got != f.commit {
			t.Fatalf("origin tag = %s, want the retry's commit %s", got, f.commit)
		}
	})
	t.Run("tag on a commit a remote has is refused", func(t *testing.T) {
		f := newReleasePushFixture(t)
		gitRun(t, f.repo, "tag", "-a", "v0.99.0", f.source, "-m", "Release v0.99.0")
		before := strings.TrimSpace(gitRun(t, f.repo, "rev-parse", "v0.99.0"))
		err := f.run(t, releasePushHost(t, f.commit))
		if err == nil || !strings.Contains(err.Error(), "refs/remotes/origin/main") {
			t.Fatalf("tag on a published commit = %v, want a refusal naming the remote ref", err)
		}
		if got := strings.TrimSpace(gitRun(t, f.repo, "rev-parse", "v0.99.0")); got != before {
			t.Fatal("refusal changed the local tag")
		}
		if strings.TrimSpace(gitRun(t, f.origin, "tag", "--list")) != "" {
			t.Fatal("refusal published a tag")
		}
	})
	t.Run("tag on a commit an unfetched origin branch has is refused", func(t *testing.T) {
		f := newReleasePushFixture(t)
		gitRun(t, f.repo, "tag", "-a", "v0.99.0", f.commit, "-m", "old release")
		// safety: another clone published the commit; this clone has no tracking ref for that branch.
		gitRun(t, f.origin, "fetch", f.detached, f.commit)
		gitRun(t, f.origin, "update-ref", "refs/heads/published", f.commit)
		before := strings.TrimSpace(gitRun(t, f.repo, "rev-parse", "v0.99.0"))
		err := f.run(t, releasePushHost(t, f.commit))
		if err == nil || !strings.Contains(err.Error(), "refs/remotes/origin/published") {
			t.Fatalf("tag on a commit origin publishes on an unfetched branch = %v, want a refusal naming it", err)
		}
		if got := strings.TrimSpace(gitRun(t, f.repo, "rev-parse", "v0.99.0")); got != before {
			t.Fatal("refusal changed the local tag")
		}
	})
	t.Run("unreachable origin refuses the replacement", func(t *testing.T) {
		f := newReleasePushFixture(t)
		gitRun(t, f.detached, "commit", "--allow-empty", "--no-verify", "-m", "abandoned attempt")
		gitRun(t, f.repo, "tag", "-a", "v0.99.0", strings.TrimSpace(gitRun(t, f.detached, "rev-parse", "HEAD")), "-m", "Release v0.99.0")
		before := strings.TrimSpace(gitRun(t, f.repo, "rev-parse", "v0.99.0"))
		realGit, err := exec.LookPath("git")
		if err != nil {
			t.Fatal(err)
		}
		bin := t.TempDir()
		writeDeliveryTool(t, filepath.Join(bin, "git"), "#!/bin/sh\nfor arg in \"$@\"; do [ \"$arg\" = fetch ] && { echo 'fetch refused' >&2; exit 1; }; done\nexec '"+realGit+"' \"$@\"\n")
		t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
		err = f.run(t, releasePushHost(t, f.commit))
		if err == nil || !strings.Contains(err.Error(), "fetch") {
			t.Fatalf("replacement without a fresh view of origin = %v, want a refusal naming the fetch", err)
		}
		if got := strings.TrimSpace(gitRun(t, f.repo, "rev-parse", "v0.99.0")); got != before {
			t.Fatal("refusal changed the local tag")
		}
		if strings.TrimSpace(gitRun(t, f.origin, "tag", "--list")) != "" {
			t.Fatal("refusal published a tag")
		}
	})
	t.Run("tag on origin is never touched", func(t *testing.T) {
		f := newReleasePushFixture(t)
		gitRun(t, f.repo, "tag", "-a", "v0.99.0", f.source, "-m", "Release v0.99.0")
		gitRun(t, f.repo, "push", "origin", "refs/tags/v0.99.0")
		remote := strings.TrimSpace(gitRun(t, f.origin, "rev-parse", "v0.99.0"))
		if err := f.run(t, releasePushHost(t, f.commit)); err == nil {
			t.Fatal("publication over a tag on origin succeeded")
		}
		if got := strings.TrimSpace(gitRun(t, f.origin, "rev-parse", "v0.99.0")); got != remote {
			t.Fatal("publication changed the tag on origin")
		}
		if got := strings.TrimSpace(gitRun(t, f.repo, "rev-parse", "v0.99.0")); got != remote {
			t.Fatal("publication changed the local copy of the origin tag")
		}
	})
}

func releaseLogged(host sparkwingtest.Host, fragment string) bool {
	for _, record := range host.Records() {
		if strings.Contains(record.Msg, fragment) {
			return true
		}
	}
	return false
}
