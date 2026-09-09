package cache

import (
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func countClones(t *testing.T, upstream string, fail bool) *atomic.Int32 {
	t.Helper()
	var n atomic.Int32
	old := cloneMirror
	cloneMirror = func(_, bareRepo string) (string, error) {
		n.Add(1)
		if fail {
			return "fatal: could not read from remote repository\n", &gitError{"clone failed"}
		}
		return gitCmd("clone", "--bare", upstream, bareRepo)
	}
	t.Cleanup(func() { cloneMirror = old })
	return &n
}

// After a recovery reclone deletes the mirror and its own clone fails, the
// mirror is gone: every later /archive request lands on the clone-if-missing
// path, which re-downloaded the whole repository with nothing bounding it.
func TestCloneBreaker_SecondMissingMirrorInsideCooldownDoesNotClone(t *testing.T) {
	repoURL, _, upstream := gitcacheFixture(t)
	setWindows(t, -1, time.Hour)
	countFetches(t, &gitError{"cannot lock ref 'refs/heads/foo'"})

	old := recloneMirror
	recloneMirror = func(_, bareRepo string) (string, error) {
		_ = os.RemoveAll(bareRepo)
		return "fatal: could not read from remote repository\n", &gitError{"reclone failed"}
	}
	t.Cleanup(func() { recloneMirror = old })

	if w := archiveRequest(t, repoURL); w.Code == 200 {
		t.Fatal("setup: the failing recovery reclone should fail the request")
	}

	clones := countClones(t, upstream, true)

	if w := archiveRequest(t, repoURL); w.Code == 200 {
		t.Fatalf("second archive should fail: the mirror is gone and the clone fails")
	}
	if got := clones.Load(); got != 1 {
		t.Fatalf("clones on the first missing-mirror request: got %d, want 1", got)
	}

	w := archiveRequest(t, repoURL)
	if w.Code == 200 {
		t.Fatal("third archive should fail")
	}
	if got := clones.Load(); got != 1 {
		t.Errorf("clones after the cooldown should have stopped it: got %d, want 1", got)
	}
	body := w.Body.String()
	for _, want := range []string{"cooldown", "operator"} {
		if !strings.Contains(body, want) {
			t.Errorf("body %q does not mention %q", body, want)
		}
	}
}

// A repository whose mirror is simply absent still gets its first clone.
func TestCloneBreaker_FirstCloneIsNotBlocked(t *testing.T) {
	repoURL, _, upstream := gitcacheFixture(t)
	setWindows(t, -1, time.Hour)
	bareRepo := filepath.Join(repoDir, repoHash(repoURL)+".git")
	if err := os.RemoveAll(bareRepo); err != nil {
		t.Fatalf("remove mirror: %v", err)
	}
	clones := countClones(t, upstream, false)

	if w := archiveRequest(t, repoURL); w.Code != 200 {
		t.Fatalf("first clone must proceed: status %d, body=%s", w.Code, w.Body.String())
	}
	if got := clones.Load(); got != 1 {
		t.Errorf("clones: got %d, want 1", got)
	}
}
