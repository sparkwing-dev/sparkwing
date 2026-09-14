package cache

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func registerMirrorName(t *testing.T, name, repoURL string) {
	t.Helper()
	repoNamesMu.Lock()
	previous, had := repoNames[name]
	repoNames[name] = repoURL
	repoNamesMu.Unlock()
	t.Cleanup(func() {
		repoNamesMu.Lock()
		if had {
			repoNames[name] = previous
		} else {
			delete(repoNames, name)
		}
		repoNamesMu.Unlock()
	})
}

// safety: git needs a URL, so the incident shape can only be reproduced against
// a server whose mirror the test can age and whose origin it can push to.
func gitcacheServerFixture(t *testing.T) (srv *httptest.Server, bareRepo, upstream string, fetches *atomic.Int32) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	// safety: the counter is installed before the server so its cleanup runs
	// after the server closes, and no handler reads it as it is restored.
	fetches = countRealFetches(t)
	cfg := DefaultConfig()
	cfg.AllowUnauthenticated = true
	// safety: the window is the bound under test, not the default; a minute
	// keeps these counts independent of how long the box takes to run them.
	cfg.FetchFreshWindow = time.Minute
	srv = newTestServerConfig(t, cfg)
	resetFetchState(t)

	tmp := t.TempDir()
	upstream = filepath.Join(tmp, "upstream.git")
	mustGit(t, "", "init", "--bare", upstream)
	work := filepath.Join(tmp, "work")
	mustGit(t, "", "clone", "--quiet", upstream, work)
	mustGit(t, work, "config", "user.email", "t@t")
	mustGit(t, work, "config", "user.name", "t")
	mustGit(t, work, "commit", "--allow-empty", "-m", "first")
	mustGit(t, work, "branch", "-M", "main")
	mustGit(t, work, "push", "--quiet", "origin", "main")

	const repoURL = "https://git.example.com/acme/widgets.git"
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	bareRepo = filepath.Join(repoDir, repoHash(repoURL)+".git")
	mustGit(t, "", "clone", "--bare", "--quiet", upstream, bareRepo)
	// safety: every path that creates a mirror enables this, and without it git
	// refuses a commit fetch client-side and never reaches the cache at all.
	enableSHAFetch(bareRepo)
	registerMirrorName(t, "widgets", repoURL)
	return srv, bareRepo, upstream, fetches
}

// safety: the mirror is cloned before this commit exists, so the push is what
// makes it lag the way a trigger firing seconds after a push does.
func pushCommit(t *testing.T, upstream string) string {
	t.Helper()
	work := filepath.Join(t.TempDir(), "push")
	mustGit(t, "", "clone", "--quiet", upstream, work)
	mustGit(t, work, "config", "user.email", "t@t")
	mustGit(t, work, "config", "user.name", "t")
	mustGit(t, work, "commit", "--allow-empty", "-m", "pushed after the mirror was cloned")
	mustGit(t, work, "push", "--quiet", "origin", "HEAD:main")
	return strings.TrimSpace(string(mustGitOut(t, work, "rev-parse", "HEAD")))
}

// safety: this is the runner's own command, so a test that built the request by
// hand would prove nothing about what a real checkout costs.
func gitFetchSHA(t *testing.T, srv *httptest.Server, name, sha string) (string, error) {
	t.Helper()
	dir := t.TempDir()
	mustGit(t, dir, "init", "--quiet")
	cmd := exec.Command("git", "-C", dir, "fetch", "--depth=1", srv.URL+"/git/"+name, sha)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func mirrorHasCommit(t *testing.T, bareRepo, sha string) bool {
	t.Helper()
	return exec.Command("git", "-C", bareRepo, "cat-file", "-e", sha+"^{commit}").Run() == nil
}

func TestGitFetchBySHA_FindsACommitPushedAfterTheMirrorWasCloned(t *testing.T) {
	srv, bareRepo, upstream, fetches := gitcacheServerFixture(t)

	sha := pushCommit(t, upstream)
	if mirrorHasCommit(t, bareRepo, sha) {
		t.Fatalf("setup wrong: the mirror already had %s", sha)
	}

	if out, err := gitFetchSHA(t, srv, "widgets", sha); err != nil {
		t.Fatalf("git fetch of a freshly pushed commit: %v: %s", err, out)
	}
	if got := fetches.Load(); got != 1 {
		t.Errorf("origin fetches for one checkout: got %d, want 1", got)
	}
}

func TestGitFetchBySHA_BurstOnOnePushCostsOneOriginFetch(t *testing.T) {
	srv, _, upstream, fetches := gitcacheServerFixture(t)

	sha := pushCommit(t, upstream)

	const triggers = 10
	failures := make([]string, triggers)
	var wg sync.WaitGroup
	for i := range failures {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if out, err := gitFetchSHA(t, srv, "widgets", sha); err != nil {
				failures[i] = fmt.Sprintf("%v: %s", err, out)
			}
		}()
	}
	wg.Wait()

	for i, failure := range failures {
		if failure != "" {
			t.Errorf("trigger %d: %s", i, failure)
		}
	}
	if got := fetches.Load(); got != 1 {
		t.Errorf("origin fetches for %d concurrent triggers on one push: got %d, want 1", triggers, got)
	}
}

func TestGitFetchBySHA_CommitOriginDoesNotHaveRefusesTheSameWay(t *testing.T) {
	srv, _, _, fetches := gitcacheServerFixture(t)

	const absent = "d4c0dc18d4c0dc18d4c0dc18d4c0dc18d4c0dc18"
	out, err := gitFetchSHA(t, srv, "widgets", absent)

	if err == nil {
		t.Fatalf("git fetch of a commit origin does not have succeeded: %s", out)
	}
	if !strings.Contains(out, notOurRef) {
		t.Errorf("refusal changed: got %q, want it to carry %q", out, notOurRef)
	}
	if got := fetches.Load(); got != 1 {
		t.Errorf("origin fetches before refusing: got %d, want 1", got)
	}
}

// safety: the trigger loop retries on this string, so the cache and the loop
// agree on it or the retry silently stops covering the push race.
const notOurRef = "not our ref"

func TestGitFetchBySHA_RetryAfterTheFreshnessWindowFindsThePush(t *testing.T) {
	srv, _, upstream, fetches := gitcacheServerFixture(t)

	// safety: the mirror was refreshed a moment ago, which is what makes the
	// first attempt miss: the trigger fires inside the window it bought.
	if out, err := gitFetchSHA(t, srv, "widgets", strings.TrimSpace(string(mustGitOut(t, upstream, "rev-parse", "main")))); err != nil {
		t.Fatalf("warm-up checkout: %v: %s", err, out)
	}
	settled := fetches.Load()

	sha := pushCommit(t, upstream)

	if out, err := gitFetchSHA(t, srv, "widgets", sha); err == nil {
		t.Logf("the first attempt already saw the push: %s", out)
	} else if !strings.Contains(out, notOurRef) {
		t.Fatalf("first attempt failed for another reason: %v: %s", err, out)
	}

	// safety: this stands in for the trigger loop's retry delay, which has to
	// outlast the window or the retry reads the refs the first attempt saw.
	backdateFetch(t, repoHash("https://git.example.com/acme/widgets.git"), 2*time.Minute)

	if out, err := gitFetchSHA(t, srv, "widgets", sha); err != nil {
		t.Fatalf("the retry still could not fetch %s: %v: %s", sha, err, out)
	}

	if spent := fetches.Load() - settled; spent < 1 || spent > 2 {
		t.Errorf("origin fetches for the push and the retry: got %d, want 1 or 2", spent)
	}
}

func TestInfoRefs_ReceivePackSpendsNoOriginFetch(t *testing.T) {
	srv, _, _, fetches := gitcacheServerFixture(t)

	resp, err := srv.Client().Get(srv.URL + "/git/widgets/info/refs?service=git-receive-pack")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if got := fetches.Load(); got != 0 {
		t.Errorf("origin fetches for a push advertisement the cache refuses anyway: got %d, want 0", got)
	}
}

func backdateRequest(t *testing.T, hash string, d time.Duration) {
	t.Helper()
	bgFetch.mu.Lock()
	defer bgFetch.mu.Unlock()
	rs := bgFetch.repos[stateKey(hash)]
	if rs == nil || rs.lastRequest.IsZero() {
		t.Fatalf("no request recorded for %s", hash)
	}
	rs.lastRequest = rs.lastRequest.Add(-d)
}

func expireRetry(t *testing.T, hash string, d time.Duration) {
	t.Helper()
	bgFetch.mu.Lock()
	defer bgFetch.mu.Unlock()
	rs := bgFetch.repos[stateKey(hash)]
	if rs == nil || rs.nextRetry.IsZero() {
		t.Fatalf("no retry scheduled for %s", hash)
	}
	rs.nextRetry = rs.nextRetry.Add(-d)
}

func TestKeepWarm_SkipsAMirrorNoRequestAskedFor(t *testing.T) {
	gitcacheFixture(t)
	fetches := countFetches(t, nil)

	if fetched, failed := keepWarmPass(context.Background(), 30*time.Second); fetched != 0 || failed != 0 {
		t.Errorf("idle mirror: got %d fetched, %d failed; want 0, 0", fetched, failed)
	}
	if got := fetches.Load(); got != 0 {
		t.Errorf("origin fetches for a mirror nobody asked for: got %d, want 0", got)
	}
}

func TestKeepWarm_RefreshesAMirrorARequestAskedForAndStopsWhenItGoesIdle(t *testing.T) {
	repoURL, _, _ := gitcacheFixture(t)
	setWindows(t, time.Minute, time.Hour)
	fetches := countFetches(t, nil)

	if w := fileRequest(t, repoURL); w.Code != http.StatusOK {
		t.Fatalf("file request: status %d, body=%s", w.Code, w.Body.String())
	}
	if got := fetches.Load(); got != 1 {
		t.Fatalf("setup: the request itself fetched %d times, want 1", got)
	}

	if fetched, _ := keepWarmPass(context.Background(), 30*time.Second); fetched != 1 {
		t.Errorf("mirror inside the keep-warm window: got %d fetched, want 1", fetched)
	}

	backdateRequest(t, repoHash(repoURL), keepWarmWindow+time.Minute)

	if fetched, _ := keepWarmPass(context.Background(), 30*time.Second); fetched != 0 {
		t.Errorf("mirror that went idle: got %d fetched, want 0", fetched)
	}
}

func TestKeepWarm_BacksOffWhileOriginKeepsFailing(t *testing.T) {
	repoURL, _, _ := gitcacheFixture(t)
	hash := repoHash(repoURL)
	bgFetch.markRequested(stateKey(hash))
	fetches := countFetches(t, errors.New("ssh: connect refused"))

	const interval = 30 * time.Second
	pass := func() int {
		t.Helper()
		fetched, failed := keepWarmPass(context.Background(), interval)
		if fetched != failed {
			t.Fatalf("a failing origin reported %d fetched and %d failed", fetched, failed)
		}
		return fetched
	}

	if pass() != 1 {
		t.Fatal("the first round never reached origin")
	}
	if pass() != 0 {
		t.Error("the second round retried immediately after a failure")
	}

	expireRetry(t, hash, interval)
	if pass() != 1 {
		t.Errorf("the first backoff is longer than the %s interval", interval)
	}

	expireRetry(t, hash, interval)
	if pass() != 0 {
		t.Error("the second backoff did not double: one interval was enough to retry")
	}
	expireRetry(t, hash, interval)
	if pass() != 1 {
		t.Error("the second backoff is longer than two intervals")
	}

	if got := fetches.Load(); got != 3 {
		t.Errorf("origin attempts across four rounds: got %d, want 3", got)
	}
}

func TestBackgroundFetchLoop_ZeroIntervalPollsNothing(t *testing.T) {
	gitcacheFixture(t)
	fetches := countFetches(t, nil)

	backgroundFetchLoop(context.Background(), 0)

	if got := fetches.Load(); got != 0 {
		t.Errorf("origin fetches with the keep-warm pass off: got %d, want 0", got)
	}
	if DefaultConfig().FetchInterval != 0 {
		t.Errorf("default FetchInterval: got %s, want the pass off", DefaultConfig().FetchInterval)
	}
}

func countRealFetches(t *testing.T) *atomic.Int32 {
	t.Helper()
	var n atomic.Int32
	old := mirrorFetch
	mirrorFetch = func(timeout time.Duration, bareRepo string) (string, error) {
		n.Add(1)
		return old(timeout, bareRepo)
	}
	t.Cleanup(func() { mirrorFetch = old })
	return &n
}
