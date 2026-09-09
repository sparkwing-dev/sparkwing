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
		t.Fatal("second archive should fail: the mirror is gone and the clone fails")
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

func backdateClone(t *testing.T, hash string, d time.Duration) {
	t.Helper()
	bgFetch.mu.Lock()
	defer bgFetch.mu.Unlock()
	rs := bgFetch.repos[stateKey(hash)]
	if rs == nil || rs.lastClone.IsZero() {
		t.Fatalf("no clone attempt recorded for %s", hash)
	}
	rs.lastClone = rs.lastClone.Add(-d)
}

func TestCloneBreaker_CooldownExpiryPermitsOneClone(t *testing.T) {
	repoURL, _, upstream := gitcacheFixture(t)
	setWindows(t, -1, time.Hour)
	bareRepo := filepath.Join(repoDir, repoHash(repoURL)+".git")
	if err := os.RemoveAll(bareRepo); err != nil {
		t.Fatalf("remove mirror: %v", err)
	}
	clones := countClones(t, upstream, true)

	if w := archiveRequest(t, repoURL); w.Code == 200 {
		t.Fatal("setup: the failing clone should fail the request")
	}
	if w := archiveRequest(t, repoURL); w.Code == 200 {
		t.Fatal("setup: the second request should be refused")
	}
	if got := clones.Load(); got != 1 {
		t.Fatalf("setup: clones inside the cooldown: got %d, want 1", got)
	}

	backdateClone(t, repoHash(repoURL), 2*time.Hour)

	if w := archiveRequest(t, repoURL); w.Code == 200 {
		t.Fatal("the clone still fails, so the request still fails")
	}
	if got := clones.Load(); got != 2 {
		t.Errorf("clones after the cooldown expired: got %d, want 2", got)
	}
}

// The cooldown records an attempt, not a verdict: a mirror that is healthy
// again must not carry the refusal into a later legitimate clone.
func TestCloneBreaker_SuccessfulFetchClearsTheCooldown(t *testing.T) {
	repoURL, _, upstream := gitcacheFixture(t)
	setWindows(t, -1, time.Hour)
	bareRepo := filepath.Join(repoDir, repoHash(repoURL)+".git")
	if err := os.RemoveAll(bareRepo); err != nil {
		t.Fatalf("remove mirror: %v", err)
	}
	clones := countClones(t, upstream, false)

	if w := archiveRequest(t, repoURL); w.Code != 200 {
		t.Fatalf("first clone: status %d, body=%s", w.Code, w.Body.String())
	}
	if err := os.RemoveAll(bareRepo); err != nil {
		t.Fatalf("prune mirror: %v", err)
	}

	if w := archiveRequest(t, repoURL); w.Code != 200 {
		t.Fatalf("a prune after a healthy clone must be recloneable: status %d, body=%s", w.Code, w.Body.String())
	}
	if got := clones.Load(); got != 2 {
		t.Errorf("clones: got %d, want 2", got)
	}
}

func TestCloneBreaker_NegativeCooldownDisablesTheGuard(t *testing.T) {
	repoURL, _, upstream := gitcacheFixture(t)
	setWindows(t, -1, -1)
	bareRepo := filepath.Join(repoDir, repoHash(repoURL)+".git")
	if err := os.RemoveAll(bareRepo); err != nil {
		t.Fatalf("remove mirror: %v", err)
	}
	clones := countClones(t, upstream, true)

	for i := range 3 {
		if w := archiveRequest(t, repoURL); w.Code == 200 {
			t.Fatalf("request %d should fail", i)
		}
	}
	if got := clones.Load(); got != 3 {
		t.Errorf("a negative cooldown must not bound cloning: got %d clones, want 3", got)
	}
}

// handleGit reaches resolveGitRepo twice per runner clone, so an auto-clone
// that keeps failing is the hotter version of the same unbounded download.
func TestResolveGitRepo_AutoCloneIsBoundedByTheCooldown(t *testing.T) {
	root := t.TempDir()
	oldRepoDir := repoDir
	repoDir = filepath.Join(root, "cache")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	resetFetchState(t)
	setWindows(t, -1, time.Hour)
	t.Cleanup(func() {
		repoDir = oldRepoDir
		repoNamesMu.Lock()
		delete(repoNames, "cooldown-fixture")
		repoNamesMu.Unlock()
	})

	repoNamesMu.Lock()
	repoNames["cooldown-fixture"] = "/this/path/does/not/exist.git"
	repoNamesMu.Unlock()
	clones := countClones(t, "", true)

	if _, err := resolveGitRepo("cooldown-fixture"); err == nil {
		t.Fatal("expected the auto-clone of a bogus URL to fail")
	}
	_, err := resolveGitRepo("cooldown-fixture")
	if err == nil {
		t.Fatal("expected the second resolve to fail too")
	}
	if !strings.Contains(err.Error(), "cooldown") {
		t.Errorf("second resolve error %q does not name the cooldown", err)
	}
	if got := clones.Load(); got != 1 {
		t.Errorf("auto-clone attempts: got %d, want 1", got)
	}
}
