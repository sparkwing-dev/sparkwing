package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/profile"
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

// cacheOperatorFixture is a controller that announces a cache, and that cache,
// which answers every request with the 401 a caller without its operator token
// gets. It counts what reached the cache and the controller's gitcache routes.
func cacheOperatorFixture(t *testing.T, multiTeam bool) (*profile.Profile, *atomic.Int32) {
	t.Helper()
	var reached atomic.Int32
	cacheSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached.Add(1)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	t.Cleanup(cacheSrv.Close)
	ctrl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/services" {
			_ = json.NewEncoder(w).Encode(map[string]any{"cache_pod": cacheSrv.URL, "multi_team": multiTeam})
			return
		}
		reached.Add(1)
		http.Error(w, "admin scope required", http.StatusForbidden)
	}))
	t.Cleanup(ctrl.Close)
	return &profile.Profile{Controller: &profile.ControllerSpec{URL: ctrl.URL, Token: "swu_member"}}, &reached
}

func captureDebugLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	saved := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(saved) })
	return &buf
}

// On a multi-team controller a team member holds no cache operator token, so
// the CLI makes no refresh or seed call it would be refused, and says so once
// at debug level; a commit not on origin is refused naming the push, not the
// cache's 401.
func TestAMultiTeamDispatchSkipsTheCachesOperatorRoutes(t *testing.T) {
	t.Setenv("SPARKWING_CACHE_TOKEN", "")
	work, pushed, local := pushedAndLocalCommits(t)
	prof, reached := cacheOperatorFixture(t, true)
	logs := captureDebugLog(t)

	if err := offerTriggerSource(prof, work, "https://github.com/acme/app.git", pushed); err != nil {
		t.Fatalf("a pushed commit on a multi-team controller = %v, want nil", err)
	}
	err := offerTriggerSource(prof, work, "https://github.com/acme/app.git", local)
	if err == nil || !strings.Contains(err.Error(), "push your commit") || strings.Contains(err.Error(), "401") {
		t.Fatalf("an unpushed commit on a multi-team controller = %v, want a refusal naming the push and not the cache", err)
	}
	seedCronsSource(prof, work, "https://github.com/acme/app.git", local)
	if n := reached.Load(); n != 0 {
		t.Fatalf("%d requests reached the cache or the controller's gitcache routes, want none", n)
	}
	if n := strings.Count(logs.String(), "git cache refresh and seed skipped"); n != 3 {
		t.Fatalf("debug log = %q, want one skip line per call", logs.String())
	}
}

// A single-team controller, or a shell holding the cache's operator token,
// still refreshes the cache before a dispatch.
func TestADispatchRefreshesTheCacheWhenItsOperatorRoutesAreOpen(t *testing.T) {
	work, pushed, _ := pushedAndLocalCommits(t)
	for name, tc := range map[string]struct {
		multiTeam bool
		token     string
	}{
		"single-team controller":         {multiTeam: false},
		"multi-team with operator token": {multiTeam: true, token: "operator-cache-token"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("SPARKWING_CACHE_TOKEN", tc.token)
			prof, reached := cacheOperatorFixture(t, tc.multiTeam)
			if err := offerTriggerSource(prof, work, "https://github.com/acme/app.git", pushed); err != nil {
				t.Fatalf("offerTriggerSource = %v", err)
			}
			if reached.Load() == 0 {
				t.Fatal("no refresh reached the cache")
			}
		})
	}
}
