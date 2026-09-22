package controller_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/crons"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const teamB store.Team = "team-b"

func teamTenant(t *testing.T, st *store.Store, team store.Team) *store.Tenant {
	t.Helper()
	ctx := context.Background()
	if err := st.AsOperator().CreateTeam(ctx, team); err != nil {
		t.Fatalf("CreateTeam %s: %v", team, err)
	}
	tenant, err := st.ForTeam(ctx, team)
	if err != nil {
		t.Fatalf("ForTeam %s: %v", team, err)
	}
	return tenant
}

func teamToken(t *testing.T, tenant *store.Tenant, scopes ...string) string {
	t.Helper()
	raw, _, err := tenant.CreateToken(context.Background(), "member-of-"+string(tenant.Team()),
		store.TokenKindUser, scopes, 0, time.Now().UTC())
	if err != nil {
		t.Fatalf("CreateToken for %s: %v", tenant.Team(), err)
	}
	return raw
}

// A schedule answers only to the team that armed it: another team's list
// omits it, its id reads as missing on every route, a repository delete
// leaves it armed, and arming the same repository from another team is
// refused rather than taking the row over.
func TestControllerCrons_ASchedulesTeamIsTheOnlyOneThatSeesIt(t *testing.T) {
	f := newCronsFixture(t)
	tenantB := teamTenant(t, f.store, teamB)
	writerB := teamToken(t, tenantB, controller.ScopeRunsRead, controller.ScopeRunsControl)

	var pushed map[string]any
	f.call(http.MethodPut, "/api/v1/crons/repos", writerB, cronPushBody(), http.StatusOK, &pushed)

	ctx := context.Background()
	stored, err := tenantB.ListCronSchedules(ctx)
	if err != nil {
		t.Fatalf("ListCronSchedules team B: %v", err)
	}
	if len(stored) != 2 {
		t.Fatalf("team B holds %d schedules, want the 2 it pushed", len(stored))
	}
	for _, sched := range stored {
		if sched.Team != teamB {
			t.Errorf("schedule %s team = %q, want %q", sched.ID, sched.Team, teamB)
		}
	}
	if own, err := f.store.ListCronSchedules(ctx); err != nil || len(own) != 0 {
		t.Fatalf("default team holds %d schedules (err %v), want none", len(own), err)
	}

	var overview crons.OverviewView
	f.call(http.MethodGet, "/api/v1/crons", f.writer, nil, http.StatusOK, &overview)
	if len(overview.Schedules) != 0 {
		t.Errorf("the default team lists %d of team B's schedules", len(overview.Schedules))
	}
	f.call(http.MethodGet, "/api/v1/crons", writerB, nil, http.StatusOK, &overview)
	if len(overview.Schedules) != 2 {
		t.Errorf("team B lists %d schedules, want 2", len(overview.Schedules))
	}

	id := stored[0].ID
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/crons/" + id},
		{http.MethodGet, "/api/v1/crons/acme%2Fwidgets%2Fnightly"},
		{http.MethodPost, "/api/v1/crons/" + id + "/pause"},
		{http.MethodPost, "/api/v1/crons/" + id + "/resume"},
		{http.MethodPost, "/api/v1/crons/" + id + "/run"},
		{http.MethodPost, "/api/v1/crons/" + id + "/disarm"},
		{http.MethodDelete, "/api/v1/crons/" + id + "/override"},
	} {
		if got := f.status(route.method, route.path, f.writer, nil); got != http.StatusNotFound {
			t.Errorf("default team %s %s = %d, want 404", route.method, route.path, got)
		}
	}
	if got := f.status(http.MethodPut, "/api/v1/crons/"+id+"/override", f.writer,
		map[string]any{"cron": "0 4 * * *"}); got != http.StatusNotFound {
		t.Errorf("default team override of team B's schedule = %d, want 404", got)
	}

	var removed struct {
		Removed int `json:"removed"`
	}
	f.call(http.MethodDelete, "/api/v1/crons/repos?repo_url="+cronTestRepoURL, f.writer, nil, http.StatusOK, &removed)
	if removed.Removed != 0 {
		t.Errorf("the default team's repository delete removed %d of team B's schedules", removed.Removed)
	}

	if got := f.status(http.MethodPut, "/api/v1/crons/repos", f.writer, cronPushBody()); got != http.StatusConflict {
		t.Errorf("the default team arming team B's repository = %d, want 409", got)
	}
	after, err := tenantB.GetCronSchedule(ctx, id)
	if err != nil {
		t.Fatalf("team B's schedule after the default team's attempts: %v", err)
	}
	if after.Paused || after.Team != teamB || after.ArmedBy != stored[0].ArmedBy {
		t.Errorf("team B's schedule changed under the default team: %+v", after)
	}

	f.call(http.MethodPost, "/api/v1/crons/"+id+"/pause", writerB, nil, http.StatusOK, nil)
	if got, err := tenantB.GetCronSchedule(ctx, id); err != nil || !got.Paused {
		t.Errorf("team B could not pause its own schedule: paused=%v err=%v", got.Paused, err)
	}
}

// A run a team B schedule launches is team B's, and the default team cannot
// read it.
func TestControllerCrons_RunNowLaunchesInTheSchedulesTeam(t *testing.T) {
	f := newCronsFixture(t)
	tenantB := teamTenant(t, f.store, teamB)
	writerB := teamToken(t, tenantB, controller.ScopeRunsRead, controller.ScopeRunsControl)
	f.call(http.MethodPut, "/api/v1/crons/repos", writerB, cronPushBody(), http.StatusOK, nil)

	var launched crons.RunEnvelope
	f.call(http.MethodPost, "/api/v1/crons/acme%2Fwidgets%2Fnightly/run", writerB, nil, http.StatusOK, &launched)

	ctx := context.Background()
	if _, err := tenantB.GetRun(ctx, launched.RunID); err != nil {
		t.Fatalf("team B cannot read the run its schedule launched: %v", err)
	}
	tenantDefault, err := f.store.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		t.Fatalf("ForTeam default: %v", err)
	}
	if _, err := tenantDefault.GetRun(ctx, launched.RunID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("default team GetRun of team B's scheduled run = %v, want ErrNotFound", err)
	}
}
