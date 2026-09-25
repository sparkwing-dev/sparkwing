package controller_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
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
	readerB := teamToken(t, tenantB, controller.ScopeRunsRead)
	own, err := f.store.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		t.Fatalf("ForTeam default: %v", err)
	}
	started := time.Now().Add(-time.Minute)
	if err := own.CreateRun(ctx, store.Run{ID: "run-own", Pipeline: "own-build", Status: "success", StartedAt: started}); err != nil {
		t.Fatalf("CreateRun own: %v", err)
	}
	if err := tenantB.CreateRun(ctx, store.Run{ID: "run-b", Pipeline: "b-deploy", Status: "failed", StartedAt: started}); err != nil {
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

	// Negative control: team B's pipeline is there for team B's own reader,
	// so the default team's list omits it by scope.
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
