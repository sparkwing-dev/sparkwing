package logs

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type claimController struct {
	url         string
	mu          sync.Mutex
	status      int
	team        string
	noAttempt   bool
	validations int
}

func (c *claimController) set(status int, team string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.status, c.team = status, team
}

func (c *claimController) asked() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.validations
}

func newClaimController(t *testing.T) *claimController {
	t.Helper()
	c := &claimController{status: http.StatusNoContent}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/claim/validate") {
			http.Error(w, "unexpected", http.StatusTeapot)
			return
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		c.validations++
		if c.status != http.StatusNoContent {
			http.Error(w, "revoked", c.status)
			return
		}
		if c.team != "" {
			w.Header().Set(store.ClaimTeamHeader, c.team)
		}
		generation := "1"
		if strings.HasSuffix(r.Header.Get("Authorization"), "2") {
			generation = "2"
		}
		if !c.noAttempt {
			w.Header().Set(store.ClaimGenerationHeader, generation)
			w.Header().Set(store.AttemptOrdinalHeader, "1")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(ts.Close)
	c.url = ts.URL
	return c
}

func claimAppend(t *testing.T, h http.Handler, token, path string) int {
	t.Helper()
	return claimAppendLine(t, h, token, path, "x")
}

func claimAppendLine(t *testing.T, h http.Handler, token, path, msg string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader("{\"msg\":\""+msg+"\"}\n"))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

// A claim token's append is refused when the controller's validation names
// no team. A confirmed claim is trusted for at most MaxClaimTokenCacheTTL,
// under its whole token, run and node, and a write served from that entry
// still carries the claim's team.
func TestClaimTokenAppend_IsCachedBrieflyAndAlwaysCarriesATeam(t *testing.T) {
	ctrl := newClaimController(t)
	s, err := New(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	h := s.WithControllerAuth(ctrl.url, MaxClaimCacheTTL).Handler()
	for range 2 {
		if code := claimAppend(t, h, "swc_claim", "/api/v1/logs/run-1/a"); code != http.StatusBadGateway {
			t.Fatalf("an append the controller named no team for = %d, want 502", code)
		}
	}
	if n := ctrl.asked(); n != 2 {
		t.Fatalf("two teamless appends asked the controller %d times, want 2", n)
	}
	ctrl.set(http.StatusNoContent, "acme")
	start := ctrl.asked()
	for range 2 {
		if code := claimAppend(t, h, "swc_claim", "/api/v1/logs/run-1/a"); code != http.StatusNoContent {
			t.Fatalf("a claim token's append = %d, want 204", code)
		}
	}
	if n := ctrl.asked() - start; n != 1 {
		t.Fatalf("two appends inside the cache window asked the controller %d times, want 1", n)
	}
	for what, c := range map[string]struct{ token, path string }{
		"another token": {"swc_claim2", "/api/v1/logs/run-1/a"},
		"another node":  {"swc_claim", "/api/v1/logs/run-1/b"},
		"another run":   {"swc_claim", "/api/v1/logs/run-2/a"},
	} {
		before := ctrl.asked()
		if code := claimAppend(t, h, c.token, c.path); code != http.StatusNoContent || ctrl.asked() != before+1 {
			t.Errorf("an append under %s = %d after %d validations, want 204 after its own", what, code, ctrl.asked()-before)
		}
	}
	for _, e := range s.claims.entries {
		if e.until.After(time.Now().Add(MaxClaimTokenCacheTTL)) || e.team != "acme" {
			t.Fatalf("a claim token's entry runs to %v for team %q, want at most %v and acme", e.until, e.team, MaxClaimTokenCacheTTL)
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/logs/run-1/a", nil)
	req.Header.Set("Authorization", "Bearer swc_claim")
	p := &logsPrincipal{Name: "claim", Kind: "claim", Scopes: []string{scopeLogsClaim}}
	before := ctrl.asked()
	if _, _, err := s.validateAppendClaim(req.WithContext(contextWithLogsPrincipal(req.Context(), p)), "run-1", "a"); err != nil || ctrl.asked() != before || p.Team != "acme" {
		t.Fatalf("a cached claim's write = %v after %d validations, team %q, want served from the cache as acme", err, ctrl.asked()-before, p.Team)
	}
}

// A revoked claim keeps writing only until its cached confirmation lapses,
// which the test above holds to MaxClaimTokenCacheTTL, and is refused once it
// has.
func TestClaimTokenAppend_ARevokedClaimIsRefusedOnceItsEntryLapses(t *testing.T) {
	ctrl := newClaimController(t)
	ctrl.set(http.StatusNoContent, "acme")
	s, err := New(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	h := s.WithControllerAuth(ctrl.url, MaxClaimCacheTTL).Handler()
	if code := claimAppend(t, h, "swc_claim", "/api/v1/logs/run-1/a"); code != http.StatusNoContent {
		t.Fatalf("a live claim's append = %d, want 204", code)
	}
	ctrl.set(http.StatusUnauthorized, "acme")
	if code := claimAppend(t, h, "swc_claim", "/api/v1/logs/run-1/a"); code != http.StatusNoContent {
		t.Fatalf("a revoked claim's append inside its cache window = %d, want 204", code)
	}
	s.claims.mu.Lock()
	for k, e := range s.claims.entries {
		e.until = e.until.Add(-MaxClaimTokenCacheTTL)
		s.claims.entries[k] = e
	}
	s.claims.mu.Unlock()
	if code := claimAppend(t, h, "swc_claim", "/api/v1/logs/run-1/a"); code != http.StatusUnauthorized {
		t.Fatalf("a revoked claim's append %v after it was cached = %d, want 401", MaxClaimTokenCacheTTL, code)
	}
}

// A claim's write lands in the stream of the attempt the controller named for
// it, so a revoked claim still inside its cached confirmation appends only to
// its own attempt's log, never to its successor's or to the shared node log.
func TestClaimTokenAppend_WritesOnlyItsOwnAttemptStream(t *testing.T) {
	ctrl := newClaimController(t)
	ctrl.set(http.StatusNoContent, "acme")
	root := t.TempDir()
	s, err := New(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	h := s.WithControllerAuth(ctrl.url, MaxClaimCacheTTL).Handler()
	for _, w := range []struct{ token, msg string }{{"swc_claim", "first"}, {"swc_claim2", "successor"}} {
		if code := claimAppendLine(t, h, w.token, "/api/v1/logs/run-1/a", w.msg); code != http.StatusNoContent {
			t.Fatalf("%s's append = %d", w.token, code)
		}
	}
	ctrl.set(http.StatusUnauthorized, "acme")
	if code := claimAppendLine(t, h, "swc_claim", "/api/v1/logs/run-1/a", "late"); code != http.StatusNoContent {
		t.Fatalf("the revoked claim's cached append = %d, want 204 inside its window", code)
	}
	ctrl.mu.Lock()
	ctrl.status, ctrl.noAttempt = http.StatusNoContent, true
	ctrl.mu.Unlock()
	if code := claimAppendLine(t, h, "swc_claim3", "/api/v1/logs/run-1/a", "unplaced"); code != http.StatusBadGateway {
		t.Fatalf("an append the controller named no attempt for = %d, want 502", code)
	}
	seal := httptest.NewRequest(http.MethodPost, "/api/v1/logs/run-1/a/seal", strings.NewReader(`{"stream":"main","final_seq":0,"lines":1,"bytes":1}`))
	seal.Header.Set("Authorization", "Bearer swc_claim2")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, seal)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("the successor's seal = %d", rec.Code)
	}
	logs := map[string]string{}
	runs := filepath.Join(root, "runs")
	if err := filepath.WalkDir(runs, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		body, err := os.ReadFile(path)
		rel, _ := filepath.Rel(runs, path)
		logs[rel] = string(body)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if want := sealFileRel("run-1", nodeAttemptPath("run-1", "a", 2, 1)); !strings.Contains(logs[filepath.Join("run-1", ".seals", "a.log")], `"file":"`+want+`"`) {
		t.Errorf("the successor's seal = %q, want it to name its attempt stream %s", logs[filepath.Join("run-1", ".seals", "a.log")], want)
	}
	first, successor := logs[nodeAttemptPath("run-1", "a", 1, 1)], logs[nodeAttemptPath("run-1", "a", 2, 1)]
	if !strings.Contains(first, "first") || !strings.Contains(first, "late") || strings.Contains(first, "successor") {
		t.Errorf("the first claim's attempt log = %q, want its own two lines", first)
	}
	if !strings.Contains(successor, "successor") || strings.Contains(successor, "late") {
		t.Errorf("the successor's attempt log = %q, want only its own line", successor)
	}
	if node, ok := logs[nodePath("run-1", "a")]; ok {
		t.Errorf("a claim wrote the shared node log: %q", node)
	}
}
