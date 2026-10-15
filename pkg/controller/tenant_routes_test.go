package controller

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apiroutes"
)

// The route table is the boundary's only source, so it must name exactly the
// routes server.go registers behind authentication: a route missing from it
// is refused at runtime, and an entry with no route hides a rename.
func TestTenantRoutes_EveryMuxRouteHasAnEntry(t *testing.T) {
	registered := routesRegisteredOn(t, "server.go", "mux")
	if len(registered) < 200 {
		t.Fatalf("found %d mux routes in server.go; the enumeration is broken", len(registered))
	}
	for pattern := range registered {
		if _, ok := tenantRoutes[pattern]; !ok {
			t.Errorf("server.go registers %s and tenantRoutes has no entry; declare the tenant resource its path names", pattern)
		}
	}
	for pattern := range tenantRoutes {
		if !registered[pattern] {
			t.Errorf("tenantRoutes lists %s, which server.go does not register on mux", pattern)
		}
	}
}

// A wildcard names a row, so a route carrying one cannot claim it names
// none, and every route under one run or trigger is checked for the run.
func TestTenantRoutes_WildcardsNameTheirResource(t *testing.T) {
	for pattern, res := range tenantRoutes {
		_, path, _ := strings.Cut(pattern, " ")
		underRun := strings.HasPrefix(path, "/api/v1/runs/{id}") || strings.HasPrefix(path, "/api/v1/triggers/{id}")
		if underRun != (res.owner == ownerRun) {
			t.Errorf("%s is declared %s; a route under /runs/{id} or /triggers/{id} is a run or trigger and nothing else is", pattern, res.name)
		}
		if res == resNone && strings.Contains(path, "{") {
			t.Errorf("%s names a path wildcard and is declared to name nothing", pattern)
		}
	}
}

// safety: the boundary lets a deployment-owned route through for any team's
// row, so the route itself has to demand authority that crosses teams.
func TestTenantRoutes_DeploymentRoutesNeedDeploymentAuthority(t *testing.T) {
	crossTeam := map[string]bool{
		ScopeAdmin: true, ScopeCreditsGrant: true, ScopeClaimsLaunch: true, ScopeStorageCount: true,
		apiroutes.Operator: true,
	}
	routes, err := apiroutes.Parse("server.go", map[string]string{
		"ScopeAdmin": ScopeAdmin, "ScopeCreditsGrant": ScopeCreditsGrant,
		"ScopeClaimsLaunch": ScopeClaimsLaunch, "ScopeStorageCount": ScopeStorageCount,
	})
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, route := range routes {
		pattern := route.Method + " " + route.Path
		if tenantRoutes[pattern].owner != ownerDeployment {
			continue
		}
		checked++
		for scope := range strings.SplitSeq(route.Scope, "` or `") {
			if !crossTeam[scope] {
				t.Errorf("%s names any team's %s and accepts %s, which is not deployment or service authority",
					pattern, tenantRoutes[pattern].name, scope)
			}
		}
	}
	if checked == 0 {
		t.Errorf("checked %d deployment routes; the parse is not reading server.go", checked)
	}
}

func TestTeamBoundary_RefusesARouteWithNoEntry(t *testing.T) {
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	mux := http.NewServeMux()
	served := false
	mux.HandleFunc("GET /api/v1/unlisted/{id}", func(http.ResponseWriter, *http.Request) { served = true })
	rec := httptest.NewRecorder()
	s.teamBoundary(mux, mux).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/unlisted/x", nil))
	if served || rec.Code != http.StatusInternalServerError {
		t.Fatalf("an unlisted route answered %d (served=%v); want 500 before the handler", rec.Code, served)
	}
}

// A name keyed inside a team reaches the handler only once the caller's
// team resolves, so an account that lost its team is refused at the boundary.
func TestTeamBoundary_TeamNamedRouteNeedsTheCallersTeam(t *testing.T) {
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	mux := http.NewServeMux()
	served := false
	mux.HandleFunc("GET /api/v1/secrets/{name}", func(http.ResponseWriter, *http.Request) { served = true })
	req := httptest.NewRequest(http.MethodGet, "/api/v1/secrets/s1", nil)
	req = req.WithContext(contextWithPrincipal(req.Context(), &Principal{Name: "a", AccountID: "acct-1"}))
	rec := httptest.NewRecorder()
	s.teamBoundary(mux, mux).ServeHTTP(rec, req)
	if served || rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "no_team") {
		t.Fatalf("a teamless account answered %d (served=%v): %s", rec.Code, served, rec.Body.String())
	}
}
