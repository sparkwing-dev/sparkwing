package controller_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// Only the deployment operator reads usage metrics, and the operator's own
// team never counts toward them.
func TestControllerUsageMetrics_OperatorOnlyAndExcludesOwnTeam(t *testing.T) {
	f := newCronsFixture(t)
	ctx := context.Background()
	admin, _, err := f.store.CreateToken("operator", store.TokenKindUser, []string{controller.ScopeAdmin}, 0, time.Now().UTC())
	if err != nil {
		t.Fatalf("CreateToken admin: %v", err)
	}
	tenantB := teamTenant(t, f.store, teamB)
	owner := teamToken(t, tenantB, controller.ScopeRunsRead, controller.ScopeTeamAdmin)
	own, err := f.store.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		t.Fatalf("ForTeam default: %v", err)
	}
	now := time.Now()
	for _, r := range []struct {
		tenant *store.Tenant
		id     string
	}{{own, "run-op-1"}, {own, "run-op-2"}, {tenantB, "run-b-1"}} {
		if err := r.tenant.CreateRun(ctx, store.Run{ID: r.id, Pipeline: "build", Status: "success", StartedAt: now}); err != nil {
			t.Fatalf("CreateRun %s: %v", r.id, err)
		}
	}

	for _, token := range []string{f.reader, f.writer, owner} {
		if got := f.status(http.MethodGet, "/api/v1/admin/usage-metrics", token, nil); got != http.StatusForbidden {
			t.Errorf("a non-operator token read usage metrics: status %d, want 403", got)
		}
	}
	if got := f.status(http.MethodGet, "/api/v1/admin/usage-metrics?weeks=0", admin, nil); got != http.StatusBadRequest {
		t.Errorf("weeks=0 status %d, want 400", got)
	}

	var body struct {
		ExcludedTeams []string          `json:"excluded_teams"`
		Weeks         []store.UsageWeek `json:"weeks"`
		FirstGreen    struct {
			TeamsCreated int `json:"teams_created"`
			TeamsGreen   int `json:"teams_green"`
		} `json:"time_to_first_green"`
	}
	f.call(http.MethodGet, "/api/v1/admin/usage-metrics?weeks=4", admin, nil, http.StatusOK, &body)
	if len(body.Weeks) != 4 {
		t.Fatalf("weeks = %d, want 4", len(body.Weeks))
	}
	cur := body.Weeks[3]
	if cur.ActiveTeams != 1 || cur.Runs != 1 || cur.NewTeams != 1 {
		t.Errorf("this week = %+v, want only team B: 1 active team, 1 run, 1 new team", cur)
	}
	if body.FirstGreen.TeamsCreated != 1 || body.FirstGreen.TeamsGreen != 1 {
		t.Errorf("first green = %+v, want team B created and green", body.FirstGreen)
	}

	f.call(http.MethodGet, "/api/v1/admin/usage-metrics?weeks=1&exclude_team="+string(teamB), admin, nil, http.StatusOK, &body)
	if len(body.Weeks) != 1 || body.Weeks[0].ActiveTeams != 0 || body.Weeks[0].Runs != 0 {
		t.Errorf("excluding team B left %+v, want nothing counted", body.Weeks)
	}
	if len(body.ExcludedTeams) != 2 {
		t.Errorf("excluded teams = %v, want default and team B", body.ExcludedTeams)
	}
}
