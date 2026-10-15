package controller

import (
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/web"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestCapacityReadUsesRequestTenantProperty(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	server := New(s, nil).WithDashboard(Dashboard{Local: true})
	random := rand.New(rand.NewPCG(37, 19))
	for _, team := range []store.Team{store.DefaultTeam, "alpha", "beta"} {
		if err := s.AsOperator().CreateTeam(t.Context(), team); err != nil {
			t.Fatal(err)
		}
		tenant, err := s.ForTeam(t.Context(), team)
		if err != nil {
			t.Fatal(err)
		}
		pipeline := string(team) + "-only"
		if err := tenant.RecordProfileObservation(t.Context(), pipeline, "", store.ProfileObservation{Duration: time.Second, PeakCores: 1, CPUMeasured: true}); err != nil {
			t.Fatal(err)
		}
	}
	handler := server.capacityRead(web.CapacityProfilesHandler)
	for i := range 96 {
		team := []store.Team{"alpha", "beta"}[random.IntN(2)]
		query := fmt.Sprintf("/api/v1/capacity/profiles?team=%s&pipeline=%s", []string{"default", "alpha", "beta", "_%", "' OR 1=1 --"}[random.IntN(5)], []string{"default-only", "alpha-only", "beta-only"}[random.IntN(3)])
		req := httptest.NewRequest(http.MethodGet, strings.ReplaceAll(query, " ", "%20"), nil)
		req = req.WithContext(contextWithPrincipal(req.Context(), &Principal{AccountID: "account", Team: team}))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), string(team)+"-only") {
			t.Fatalf("request %d read wrong team: %d %s", i, rec.Code, rec.Body)
		}
		for _, foreign := range []store.Team{store.DefaultTeam, "alpha", "beta"} {
			if foreign != team && strings.Contains(rec.Body.String(), string(foreign)+"-only") {
				t.Fatalf("request %d leaked %s to %s: %s", i, foreign, team, rec.Body)
			}
		}
	}
	for _, team := range []store.Team{"", "missing"} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/capacity/profiles", nil)
		req = req.WithContext(contextWithPrincipal(req.Context(), &Principal{AccountID: "account", Team: team}))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("unregistered team %q read profiles: %d %s", team, rec.Code, rec.Body)
		}
	}
}
