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

func gitcacheFixture(t *testing.T) (repoURL, bareRepo, upstream string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}

	tmp := t.TempDir()
	oldRepoDir, oldProxyDir := repoDir, proxyDir
	repoDir = filepath.Join(tmp, "repos")
	proxyDir = filepath.Join(tmp, "proxy")
	for _, d := range []string{repoDir, proxyDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		repoDir, proxyDir = oldRepoDir, oldProxyDir
	})

	upstream = filepath.Join(tmp, "upstream.git")
	mustGit(t, "", "init", "--bare", "-b", "main", upstream)
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

	repoNamesMu.Lock()
	repoNames[fixtureRepoName] = repoURL
	repoNamesMu.Unlock()
	t.Cleanup(func() {
		repoNamesMu.Lock()
		delete(repoNames, fixtureRepoName)
		repoNamesMu.Unlock()
	})

	resetFetchState(t)
	return repoURL, bareRepo, upstream
}

const fixtureRepoName = "widgets"

func refsRequest(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/git/"+fixtureRepoName+"/info/refs?service=git-upload-pack", nil)
	w := httptest.NewRecorder()
	handleGit(w, req)
	return w
}

func resetFetchState(t *testing.T) {
	t.Helper()
	old := bgFetch
	bgFetch = &fetchState{repos: map[string]*repoFetchState{}}
	t.Cleanup(func() { bgFetch = old })
}

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

func setWindows(t *testing.T, fresh, cooldown time.Duration) {
	t.Helper()
	oldFresh, oldCooldown := fetchFreshWindow, recloneCooldown
	fetchFreshWindow, recloneCooldown = fresh, cooldown
	t.Cleanup(func() { fetchFreshWindow, recloneCooldown = oldFresh, oldCooldown })
}

func backdateFetch(t *testing.T, hash string, d time.Duration) {
	t.Helper()
	bgFetch.mu.Lock()
	defer bgFetch.mu.Unlock()
	rs := bgFetch.repos[stateKey(hash)]
	if rs == nil || rs.lastOK.IsZero() {
		t.Fatalf("no successful fetch recorded for %s", hash)
	}
	rs.lastOK = rs.lastOK.Add(-d)
}

func TestFetchThrottle_SecondRequestInsideWindowDoesNotFetch(t *testing.T) {
	gitcacheFixture(t)
	setWindows(t, time.Minute, time.Hour)
	fetches := countFetches(t, nil)

	for i := 0; i < 3; i++ {
		if w := refsRequest(t); w.Code != 200 {
			t.Fatalf("request %d: status %d, body=%s", i, w.Code, w.Body.String())
		}
	}

	if got := fetches.Load(); got != 1 {
		t.Errorf("fetches inside the freshness window: got %d, want 1", got)
	}
}

func TestFetchThrottle_ExpiredWindowFetchesAgain(t *testing.T) {
	repoURL, _, _ := gitcacheFixture(t)
	setWindows(t, 10*time.Millisecond, time.Hour)
	fetches := countFetches(t, nil)

	refsRequest(t)
	backdateFetch(t, repoHash(repoURL), 20*time.Millisecond)
	refsRequest(t)

	if got := fetches.Load(); got != 2 {
		t.Errorf("fetches across an expired window: got %d, want 2", got)
	}
}

func TestHealth_FailedCloneSurfacesProblem(t *testing.T) {
	repoURL, bareRepo, upstream := gitcacheFixture(t)
	setWindows(t, -1, time.Hour)
	if err := os.RemoveAll(bareRepo); err != nil {
		t.Fatal(err)
	}
	countClones(t, upstream, true)

	if w := refsRequest(t); w.Code == http.StatusOK {
		t.Fatalf("a failing clone should fail the request: %s", w.Body.String())
	}

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
	if strings.Contains(joined, repoHash(repoURL)) || strings.Contains(joined, "could not read") {
		t.Errorf("public health exposed repository details: %v", resp.Problems)
	}
	if !strings.Contains(joined, "gitcache: background fetch failing") {
		t.Errorf("health omitted the generic fetch alarm: %v", resp.Problems)
	}
}

type gitError struct{ msg string }

func (e *gitError) Error() string { return e.msg }

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
}

func mustGitOut(t *testing.T, dir string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return out
}
