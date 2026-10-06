package controller

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

// A caller whose team is missing or unregistered is refused with 403, never
// served the default team's rows. A signed-in account that lost its team is
// the case that must not fall back the way a pre-team token does.
func TestRequestTenantRefusesACallerWithNoRegisteredTeam(t *testing.T) {
	st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
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
	st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
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

// The boundary reads the run id itself rather than taking it from the
// handler, so for every run and trigger route and every spelling of a path,
// the id it checks must be the id the mux hands the handler.
func TestTeamBoundaryRunIDIsTheIDTheHandlerReads(t *testing.T) {
	type seen struct{ pattern, id string }
	var got *seen
	mux := http.NewServeMux()
	var patterns []string
	for pattern := range muxRoutes(t, "server.go") {
		_, path, _ := strings.Cut(pattern, " ")
		if !strings.HasPrefix(path, "/api/v1/runs/{id}") && !strings.HasPrefix(path, "/api/v1/triggers/{id}") {
			continue
		}
		patterns = append(patterns, pattern)
		mux.HandleFunc(pattern, func(_ http.ResponseWriter, r *http.Request) {
			got = &seen{r.Pattern, r.PathValue("id")}
		})
	}
	if len(patterns) < 60 {
		t.Fatalf("found %d run routes in server.go; the enumeration is broken", len(patterns))
	}
	ids := []string{"run-1", "run%2D1", "run%252D1", "run%2F1", "%2E%2E", "%EF%BD%81", "r%C3%A9n", "re%CC%81n"}
	spell := []func(string) string{
		func(p string) string { return p },
		func(p string) string { return strings.Replace(p, "/runs/", "/%72uns/", 1) },
		func(p string) string { return strings.Replace(p, "/triggers/", "/%74riggers/", 1) },
		func(p string) string { return strings.Replace(p, "/api/", "/%61pi/", 1) },
		func(p string) string { return strings.Replace(p, "/v1/", "/v%31/", 1) },
		func(p string) string { return p + "/" },
		func(p string) string { return strings.Replace(p, "/api/v1/", "/api/v1/./", 1) },
		func(p string) string { return strings.Replace(p, "/api/v1/", "/api/v1//", 1) },
	}
	served := 0
	for _, pattern := range patterns {
		method, path, _ := strings.Cut(pattern, " ")
		for _, id := range ids {
			base := strings.NewReplacer("{id}", id, "{nodeID}", "n1", "{childID}", "c1", "{path...}", "info/refs").Replace(path)
			for _, sp := range spell {
				target := sp(base)
				req := httptest.NewRequest(method, target, nil)
				_, matched := mux.Handler(req)
				boundaryID, scoped := teamBoundaryRunID(matched, req)
				got = nil
				mux.ServeHTTP(httptest.NewRecorder(), req)
				if got == nil {
					continue
				}
				served++
				if _, exempt := teamBoundaryExempt[got.pattern]; exempt {
					continue
				}
				if !scoped || boundaryID != got.id {
					t.Errorf("%s %s: boundary checks %q (scoped=%v), handler %s reads %q",
						method, target, boundaryID, scoped, got.pattern, got.id)
				}
			}
		}
	}
	if served < len(patterns)*len(ids) {
		t.Errorf("only %d requests reached a handler; the probe is not exercising the routes", served)
	}
}
