package controller

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// A caller whose team is missing or unregistered is refused with 403, never
// served the default team's rows. A signed-in account that lost its team is
// the case that must not fall back the way a pre-team token does.
func TestRequestTenantRefusesACallerWithNoRegisteredTeam(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv := New(st, nil)

	cases := []struct {
		name string
		p    *Principal
		want store.Team
	}{
		{"pre-team token", &Principal{Name: "runner"}, store.DefaultTeam},
		{"account without a team", &Principal{Name: "ana", AccountID: "acct-ana"}, ""},
		{"unregistered team", &Principal{Name: "ana", AccountID: "acct-ana", Team: "ghost"}, ""},
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/runs", nil)
		req = req.WithContext(contextWithPrincipal(req.Context(), c.p))
		rec := httptest.NewRecorder()
		tn, ok := srv.requestTenant(rec, req)
		switch {
		case c.want != "" && (!ok || tn.Team() != c.want):
			t.Errorf("%s: tenant %v ok=%v (%d), want %s", c.name, tn, ok, rec.Code, c.want)
		case c.want == "" && (ok || rec.Code != http.StatusForbidden):
			t.Errorf("%s: ok=%v status %d, want refused with 403", c.name, ok, rec.Code)
		}
	}
}

// A team removed inside the window keeps its handle only until the entry
// expires.
func TestTenantCacheEntriesExpire(t *testing.T) {
	var c tenantCache
	now := time.Now()
	c.put("acme", &store.Tenant{}, now)
	if _, ok := c.get("acme", now.Add(tenantCacheTTL-time.Nanosecond)); !ok {
		t.Fatal("a fresh entry missed")
	}
	if _, ok := c.get("acme", now.Add(tenantCacheTTL)); ok {
		t.Fatal("an entry outlived its TTL")
	}
}

// The logs service validates any team's runner's log-write claim, so the
// claim-validate route stays outside the run boundary.
func TestTeamBoundaryLeavesClaimValidateToItsOwnGate(t *testing.T) {
	const pattern = "POST /api/v1/runs/{id}/nodes/{nodeID}/claim/validate"
	req := httptest.NewRequest(http.MethodPost, "/api/v1/runs/run-1/nodes/build/claim/validate", nil)
	if id, scoped := teamBoundaryRunID(pattern, req); scoped {
		t.Fatalf("claim validate is inside the boundary for run %q", id)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/v1/runs/run-1/nodes", nil)
	if id, scoped := teamBoundaryRunID("GET /api/v1/runs/{id}/nodes", req); !scoped || id != "run-1" {
		t.Fatalf("a run route is outside the boundary: %q %v", id, scoped)
	}
}

// runTeam feeds the cache grant, which opens a team's cache namespace, so it
// repeats the boundary rather than trusting the middleware in front of it.
func TestRunTeamRefusesAnotherTeamsRun(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := t.Context()
	if err := st.AsOperator().CreateTeam(ctx, "team-b"); err != nil {
		t.Fatal(err)
	}
	b, err := st.ForTeam(ctx, "team-b")
	if err != nil {
		t.Fatal(err)
	}
	if err := b.CreateRun(ctx, store.Run{ID: "run-b", Pipeline: "demo", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	srv := New(st, nil)

	ask := func(p *Principal) (store.Team, error) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/runs/run-b/cache-grant", nil)
		req.SetPathValue("id", "run-b")
		req = req.WithContext(contextWithPrincipal(req.Context(), p))
		return srv.runTeam(req)
	}
	if team, err := ask(&Principal{Name: "b", Team: "team-b"}); err != nil || team != "team-b" {
		t.Fatalf("team B's own run = %q, %v", team, err)
	}
	if team, err := ask(&Principal{Name: "a", Team: store.DefaultTeam}); err == nil {
		t.Errorf("the default team was handed team %q for another team's run", team)
	}
	if team, err := ask(&Principal{Name: "ana", AccountID: "acct-ana"}); err == nil {
		t.Errorf("an account with no team was handed team %q", team)
	}
}
