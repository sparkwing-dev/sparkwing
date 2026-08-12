package cache

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// These tests cover the two NAT-egress guards on the gitcache: the
// per-repo fetch freshness throttle and the recovery-reclone circuit
// breaker. Both are about how much traffic leaves the cluster, so the
// assertions count git operations rather than inspect refs.

// gitcacheFixture points the package's directory layout at a temp dir
// and leaves a bare mirror of a local upstream on disk, registered
// under a repo URL that passes sourceurl.ValidateCloneURL (handleArchive
// rejects filesystem paths, so the URL is a plausible-looking remote and
// the network operations are stubbed instead).
func gitcacheFixture(t *testing.T) (repoURL, bareRepo, upstream string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}

	tmp := t.TempDir()
	oldRepoDir, oldArchDir, oldProxyDir := repoDir, archDir, proxyDir
	repoDir = filepath.Join(tmp, "repos")
	archDir = filepath.Join(tmp, "archives")
	proxyDir = filepath.Join(tmp, "proxy")
	for _, d := range []string{repoDir, archDir, proxyDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		repoDir, archDir, proxyDir = oldRepoDir, oldArchDir, oldProxyDir
	})

	upstream = filepath.Join(tmp, "upstream.git")
	mustGit(t, "", "init", "--bare", upstream)
	work := filepath.Join(tmp, "work")
	mustGit(t, "", "clone", upstream, work)
	mustGit(t, work, "config", "user.email", "t@t")
	mustGit(t, work, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(work, "pipelines.yaml"), []byte("pipelines: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, work, "add", "pipelines.yaml")
	mustGit(t, work, "commit", "-m", "first")
	mustGit(t, work, "branch", "-M", "main")
	mustGit(t, work, "push", "origin", "main")

	repoURL = "https://git.example.com/acme/widgets.git"
	bareRepo = filepath.Join(repoDir, repoHash(repoURL)+".git")
	mustGit(t, "", "clone", "--bare", upstream, bareRepo)

	resetFetchState(t)
	return repoURL, bareRepo, upstream
}

// resetFetchState isolates a test from freshness/reclone bookkeeping
// left behind by other tests -- bgFetch is process-wide.
func resetFetchState(t *testing.T) {
	t.Helper()
	old := bgFetch
	bgFetch = &fetchState{repos: map[string]*repoFetchState{}}
	t.Cleanup(func() { bgFetch = old })
}

// countFetches replaces the mirror fetch with a counting stub that
// reports the given error (nil for success).
func countFetches(t *testing.T, err error) *atomic.Int32 {
	t.Helper()
	var n atomic.Int32
	old := mirrorFetch
	mirrorFetch = func(time.Duration, string) (string, error) {
		n.Add(1)
		if err != nil {
			return "fatal: " + err.Error() + "\n", err
		}
		return "", nil
	}
	t.Cleanup(func() { mirrorFetch = old })
	return &n
}

// setWindows overrides the throttle knobs for the duration of a test.
func setWindows(t *testing.T, fresh, cooldown time.Duration) {
	t.Helper()
	oldFresh, oldCooldown := fetchFreshWindow, recloneCooldown
	fetchFreshWindow, recloneCooldown = fresh, cooldown
	t.Cleanup(func() { fetchFreshWindow, recloneCooldown = oldFresh, oldCooldown })
}

func fileRequest(t *testing.T, repoURL string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet,
		"/file?repo="+repoURL+"&branch=main&path=pipelines.yaml", nil)
	w := httptest.NewRecorder()
	handleFile(w, req)
	return w
}

func archiveRequest(t *testing.T, repoURL string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/archive?repo="+repoURL+"&branch=main", nil)
	w := httptest.NewRecorder()
	handleArchive(w, req)
	return w
}

// TestFetchThrottle_SecondRequestInsideWindowDoesNotFetch is the
// egress claim: back-to-back reads of the same repo cost one fetch, not
// one per request. Before the throttle a webhook burst multiplied
// GitHub traffic by the number of requests for no extra freshness --
// the background loop had already fetched.
func TestFetchThrottle_SecondRequestInsideWindowDoesNotFetch(t *testing.T) {
	repoURL, _, _ := gitcacheFixture(t)
	setWindows(t, time.Minute, time.Hour)
	fetches := countFetches(t, nil)

	for i := 0; i < 3; i++ {
		if w := fileRequest(t, repoURL); w.Code != 200 {
			t.Fatalf("request %d: status %d, body=%s", i, w.Code, w.Body.String())
		}
	}

	if got := fetches.Load(); got != 1 {
		t.Errorf("fetches inside the freshness window: got %d, want 1", got)
	}
}

// TestFetchThrottle_ExpiredWindowFetchesAgain: the throttle bounds
// staleness, it does not freeze the mirror.
func TestFetchThrottle_ExpiredWindowFetchesAgain(t *testing.T) {
	repoURL, _, _ := gitcacheFixture(t)
	setWindows(t, 10*time.Millisecond, time.Hour)
	fetches := countFetches(t, nil)

	fileRequest(t, repoURL)
	time.Sleep(20 * time.Millisecond)
	fileRequest(t, repoURL)

	if got := fetches.Load(); got != 2 {
		t.Errorf("fetches across an expired window: got %d, want 2", got)
	}
}

// TestFetchThrottle_AppliesToEveryReadHandler pins the throttle to all
// the handlers that used to fetch unconditionally, so a later edit that
// reintroduces a raw per-request fetch on one of them fails here.
func TestFetchThrottle_AppliesToEveryReadHandler(t *testing.T) {
	repoURL, _, _ := gitcacheFixture(t)
	setWindows(t, time.Minute, time.Hour)
	fetches := countFetches(t, nil)

	fileRequest(t, repoURL)

	treeReq := httptest.NewRequest(http.MethodGet, "/tree-hash?repo="+repoURL+"&branch=main", nil)
	handleTreeHash(httptest.NewRecorder(), treeReq)

	head := strings.TrimSpace(string(mustGitOut(t, filepath.Join(repoDir, repoHash(repoURL)+".git"), "rev-parse", "main")))
	containsReq := httptest.NewRequest(http.MethodGet,
		"/branch-contains?repo="+repoURL+"&branch=main&commit="+head, nil)
	handleBranchContains(httptest.NewRecorder(), containsReq)

	negotiate := httptest.NewRequest(http.MethodPost, "/sync/negotiate",
		strings.NewReader(`{"repo":"`+repoURL+`","commits":["`+head+`"]}`))
	handleSyncNegotiate(httptest.NewRecorder(), negotiate)

	if got := archiveRequest(t, repoURL).Code; got != 200 {
		t.Fatalf("archive status %d", got)
	}

	if got := fetches.Load(); got != 1 {
		t.Errorf("fetches across five read handlers in one window: got %d, want 1", got)
	}
}

// TestGitRefresh_BypassesFreshnessThrottle: /git/refresh exists to
// close the push-then-trigger race, so throttling it would defeat the
// endpoint. A fresh repo must still be fetched on demand.
func TestGitRefresh_BypassesFreshnessThrottle(t *testing.T) {
	repoURL, _, _ := gitcacheFixture(t)
	setWindows(t, time.Hour, time.Hour)
	fetches := countFetches(t, nil)

	fileRequest(t, repoURL)
	if got := fetches.Load(); got != 1 {
		t.Fatalf("setup: expected 1 fetch, got %d", got)
	}

	req := httptest.NewRequest(http.MethodPost, "/git/refresh?repo="+repoURL, nil)
	w := httptest.NewRecorder()
	handleGitRefresh(w, req)
	if w.Code != 200 {
		t.Fatalf("refresh status %d: %s", w.Code, w.Body.String())
	}

	if got := fetches.Load(); got != 2 {
		t.Errorf("/git/refresh must bypass the throttle: fetches got %d, want 2", got)
	}
}

// countReclones stubs the recovery reclone with a counting version that
// really re-clones from a local upstream, so the handler continues down
// its normal path afterwards.
func countReclones(t *testing.T, upstream string) *atomic.Int32 {
	t.Helper()
	var n atomic.Int32
	old := recloneMirror
	recloneMirror = func(_, bareRepo string) (string, error) {
		n.Add(1)
		_ = os.RemoveAll(bareRepo)
		return gitCmd("clone", "--bare", upstream, bareRepo)
	}
	t.Cleanup(func() { recloneMirror = old })
	return &n
}

// TestRecloneBreaker_SecondFailureInsideCooldownDoesNotReclone is the
// terabyte bug: a fetch that fails persistently (a ref conflict after a
// branch rename, for instance) used to turn every archive request into a
// full re-download of the repository. The first failure still recovers;
// the second reports the git error instead.
func TestRecloneBreaker_SecondFailureInsideCooldownDoesNotReclone(t *testing.T) {
	repoURL, _, upstream := gitcacheFixture(t)
	setWindows(t, -1, time.Hour) // throttle off: every request reaches the fetch
	fetchErr := &gitError{"cannot lock ref 'refs/heads/foo': 'refs/heads/foo/bar' exists"}
	countFetches(t, fetchErr)
	reclones := countReclones(t, upstream)

	if w := archiveRequest(t, repoURL); w.Code != 200 {
		t.Fatalf("first archive should recover by recloning: status %d, body=%s", w.Code, w.Body.String())
	}
	if got := reclones.Load(); got != 1 {
		t.Fatalf("first failure should reclone once: got %d", got)
	}

	w := archiveRequest(t, repoURL)
	if w.Code == 200 {
		t.Fatalf("second failure inside the cooldown should fail the request, got 200")
	}
	if got := reclones.Load(); got != 1 {
		t.Errorf("reclones after the second failure: got %d, want 1 (cooldown)", got)
	}
	body := w.Body.String()
	for _, want := range []string{"cooldown", "refs/heads/foo/bar", "operator"} {
		if !strings.Contains(body, want) {
			t.Errorf("error body missing %q: %s", want, body)
		}
	}
}

// backdateReclone rewinds a repo's last-reclone stamp so cooldown
// expiry is testable without sleeping through a real one.
func backdateReclone(t *testing.T, hash string, d time.Duration) {
	t.Helper()
	bgFetch.mu.Lock()
	defer bgFetch.mu.Unlock()
	rs := bgFetch.repos[stateKey(hash)]
	if rs == nil {
		t.Fatalf("no fetch state recorded for %s", hash)
	}
	rs.lastReclone = rs.lastReclone.Add(-d)
}

// TestRecloneBreaker_CooldownExpiryPermitsOneReclone: the breaker is a
// rate limit, not a permanent lockout -- a repo that recovers on its own
// later still gets one attempt per cooldown.
func TestRecloneBreaker_CooldownExpiryPermitsOneReclone(t *testing.T) {
	repoURL, _, upstream := gitcacheFixture(t)
	setWindows(t, -1, time.Hour)
	countFetches(t, &gitError{"cannot lock ref 'refs/heads/foo'"})
	reclones := countReclones(t, upstream)

	archiveRequest(t, repoURL)
	if w := archiveRequest(t, repoURL); w.Code == 200 {
		t.Fatalf("second archive inside the cooldown should have failed")
	}

	backdateReclone(t, repoHash(repoURL), 2*time.Hour)
	if w := archiveRequest(t, repoURL); w.Code != 200 {
		t.Fatalf("archive after cooldown expiry should reclone and succeed: status %d, body=%s", w.Code, w.Body.String())
	}

	if got := reclones.Load(); got != 2 {
		t.Errorf("reclones across an expired cooldown: got %d, want 2", got)
	}
}

// TestHealth_RepeatedReclonesSurfaceProblem: two reclones of one repo
// inside 24h is an operator problem, not a self-healing cache, so
// /health has to say so.
func TestHealth_RepeatedReclonesSurfaceProblem(t *testing.T) {
	repoURL, _, upstream := gitcacheFixture(t)
	setWindows(t, -1, time.Hour)
	countFetches(t, &gitError{"cannot lock ref 'refs/heads/foo'"})
	countReclones(t, upstream)

	archiveRequest(t, repoURL)
	backdateReclone(t, repoHash(repoURL), 2*time.Hour)
	archiveRequest(t, repoURL)

	w := httptest.NewRecorder()
	handleHealthCombined(w, httptest.NewRequest(http.MethodGet, "/health", nil))
	var resp struct {
		Status   string   `json:"status"`
		Problems []string `json:"problems"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("health body not JSON: %v (%s)", err, w.Body.String())
	}
	if resp.Status != "degraded" {
		t.Errorf("status: got %q, want degraded (%v)", resp.Status, resp.Problems)
	}
	joined := strings.Join(resp.Problems, "\n")
	if !strings.Contains(joined, repoHash(repoURL)) ||
		!strings.Contains(joined, "persistent fetch failure") ||
		!strings.Contains(joined, "expensive") {
		t.Errorf("health problems should name the repo and the fix, got: %v", resp.Problems)
	}
}

// gitError stands in for a git failure whose message an operator needs
// to see -- the breaker's error path is judged on whether it surfaces
// this text.
type gitError struct{ msg string }

func (e *gitError) Error() string { return e.msg }
