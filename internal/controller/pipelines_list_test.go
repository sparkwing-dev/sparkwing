package controller_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runstate"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

type pipelinesListBody struct {
	Pipelines []store.PipelineSummary `json:"pipelines"`
}

func listedNames(b pipelinesListBody) []string {
	out := make([]string, 0, len(b.Pipelines))
	for _, p := range b.Pipelines {
		out = append(out, p.Name)
	}
	return out
}

// A reader lists its own team's pipelines and never another team's, whatever
// the request says about teams.
func TestControllerPipelines_ListIsTheCallersTeamOnly(t *testing.T) {
	f := newCronsFixture(t)
	ctx := context.Background()
	tenantB := teamTenant(t, f.store, teamB)
	readerB := teamToken(t, f.store, tenantB, "runs.read")
	own, err := f.store.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		t.Fatalf("ForTeam default: %v", err)
	}
	started := time.Now().Add(-time.Minute)
	if err := teststore.SeedRun(f.store, ctx, own.Team(), store.Run{RunSummary: runstate.RunSummary{ID: "run-own", Pipeline: "own-build", Status: "success", StartedAt: started}}); err != nil {
		t.Fatalf("CreateRun own: %v", err)
	}
	if err := teststore.SeedRun(f.store, ctx, tenantB.Team(), store.Run{RunSummary: runstate.RunSummary{ID: "run-b", Pipeline: "b-deploy", Status: "failed", StartedAt: started}}); err != nil {
		t.Fatalf("CreateRun team B: %v", err)
	}

	var body pipelinesListBody
	f.call(http.MethodGet, "/api/v1/pipelines?team="+string(teamB), f.reader, nil, http.StatusOK, &body)
	if names := listedNames(body); len(names) != 1 || names[0] != "own-build" {
		t.Fatalf("default team lists %v, want [own-build]", names)
	}
	if body.Pipelines[0].LastRunID != "run-own" || body.Pipelines[0].LastStatus != "success" {
		t.Errorf("own-build latest = %+v, want run-own success", body.Pipelines[0])
	}

	f.call(http.MethodGet, "/api/v1/pipelines", readerB, nil, http.StatusOK, &body)
	if names := listedNames(body); len(names) != 1 || names[0] != "b-deploy" {
		t.Fatalf("team B lists %v, want [b-deploy]", names)
	}

	if got := f.status(http.MethodGet, "/api/v1/pipelines", f.none, nil); got != http.StatusForbidden {
		t.Errorf("a token without runs.read listed pipelines: status %d, want 403", got)
	}
	if got := f.status(http.MethodGet, "/api/v1/pipelines?limit=0", f.reader, nil); got != http.StatusBadRequest {
		t.Errorf("limit=0 status %d, want 400", got)
	}
}

func TestControllerLatestPipelineRepositoryFilterNarrowsTeamObservations(t *testing.T) {
	f := newCronsFixture(t)
	teststore.SeedTeam(t, f.store, teamB)
	started := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	for i, row := range []struct {
		id, repo, status string
		team             store.Team
	}{
		{"own", "acme/one", "failed", store.DefaultTeam},
		{"other", "acme/two", "success", store.DefaultTeam},
		{"foreign", "acme/one", "success", teamB},
	} {
		if err := teststore.SeedRun(f.store, t.Context(), row.team, store.Run{RunSummary: runstate.RunSummary{ID: row.id, Pipeline: "shared", Status: row.status, DeclaredRepo: row.repo, StartedAt: started.Add(time.Duration(i) * time.Minute)}}); err != nil {
			t.Fatal(err)
		}
	}
	var latest store.Run
	f.call(http.MethodGet, "/api/v1/pipelines/shared/latest?status=failed,success&from_any_run=true&repo=acme/one", f.reader, nil, http.StatusOK, &latest)
	if latest.ID != "own" || latest.Status != "failed" {
		t.Fatalf("latest observation: %+v", latest.RunSummary)
	}
	f.call(http.MethodGet, "/api/v1/pipelines/shared/latest?status=failed,success&from_any_run=true&repo=missing", f.reader, nil, http.StatusNotFound, nil)
}
