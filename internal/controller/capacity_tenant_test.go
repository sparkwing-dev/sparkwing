package controller

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/routepolicy"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
	"github.com/sparkwing-dev/sparkwing/internal/web"
	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
)

func TestCapacityReadUsesRequestTenantProperty(t *testing.T) {
	s, err := teststore.OpenExisting(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	server := New(s, nil).WithDashboard(Dashboard{Local: true})
	random := rand.New(rand.NewPCG(37, 19))
	for _, team := range []store.Team{store.DefaultTeam, "alpha", "beta"} {
		teststore.SeedTeam(t, s, team)
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
		query := fmt.Sprintf("/api/v1/sizing?team=%s&pipeline=%s", []string{"default", "alpha", "beta", "_%", "' OR 1=1 --"}[random.IntN(5)], []string{"default-only", "alpha-only", "beta-only"}[random.IntN(3)])
		req := httptest.NewRequest(http.MethodGet, strings.ReplaceAll(query, " ", "%20"), nil)
		rec := httptest.NewRecorder()
		if err := handler(rec, req, capability.Authorized{Target: capability.Target{Team: string(team)}}); err != nil {
			routepolicy.WritePolicyError(rec, err)
		}
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), string(team)+"-only") {
			t.Fatalf("request %d read wrong team: %d %s", i, rec.Code, rec.Body)
		}
		for _, foreign := range []store.Team{store.DefaultTeam, "alpha", "beta"} {
			if foreign != team && strings.Contains(rec.Body.String(), string(foreign)+"-only") {
				t.Fatalf("request %d leaked %s to %s: %s", i, foreign, team, rec.Body)
			}
		}
	}
	for _, tc := range []struct {
		team   store.Team
		status int
		code   apierr.Code
	}{{"", http.StatusInternalServerError, apierr.PolicyMissing}, {"missing", http.StatusNotFound, apierr.NotFound}} {
		team := tc.team
		req := httptest.NewRequest(http.MethodGet, "/api/v1/sizing", nil)
		rec := httptest.NewRecorder()
		if err := handler(rec, req, capability.Authorized{Target: capability.Target{Team: string(team)}}); err != nil {
			routepolicy.WritePolicyError(rec, err)
		}
		var failure apierr.Envelope
		if rec.Code != tc.status || json.Unmarshal(rec.Body.Bytes(), &failure) != nil || failure.Error == nil || failure.Error.Code != tc.code {
			t.Fatalf("unregistered team %q read profiles: %d %s", team, rec.Code, rec.Body)
		}
	}
}
