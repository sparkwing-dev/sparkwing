package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func pipelineNames(ps []store.PipelineSummary) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.Name)
	}
	return out
}

// A team's pipeline list holds what that team ran or queued, newest first,
// with each pipeline's latest run, and nothing another team did.
func TestTenantListPipelines_OneTeamsPipelinesNewestFirst(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	alpha := teamHandle(t, s, "alpha")
	bravo := teamHandle(t, s, "bravo")
	base := time.Now().Add(-time.Hour).Truncate(time.Second)

	for _, r := range []store.Run{
		{ID: "run-build-1", Pipeline: "build", Status: "success", StartedAt: base, FinishedAt: ptrTime(base.Add(time.Minute))},
		{ID: "run-build-2", Pipeline: "build", Status: "failed", StartedAt: base.Add(10 * time.Minute), FinishedAt: ptrTime(base.Add(11 * time.Minute))},
		{ID: "run-lint-1", Pipeline: "lint", Status: "success", StartedAt: base.Add(5 * time.Minute), FinishedAt: ptrTime(base.Add(6 * time.Minute))},
	} {
		if err := alpha.CreateRun(ctx, r); err != nil {
			t.Fatalf("CreateRun %s: %v", r.ID, err)
		}
	}
	if err := alpha.CreateTrigger(ctx, store.Trigger{ID: "trg-deploy", Pipeline: "deploy", CreatedAt: base.Add(20 * time.Minute)}); err != nil {
		t.Fatalf("CreateTrigger: %v", err)
	}
	if err := bravo.CreateRun(ctx, store.Run{ID: "run-bravo", Pipeline: "bravo-only", Status: "success", StartedAt: base.Add(30 * time.Minute)}); err != nil {
		t.Fatalf("CreateRun bravo: %v", err)
	}

	got, err := alpha.ListPipelines(ctx, 200)
	if err != nil {
		t.Fatalf("ListPipelines alpha: %v", err)
	}
	if names := pipelineNames(got); len(names) != 3 || names[0] != "deploy" || names[1] != "build" || names[2] != "lint" {
		t.Fatalf("alpha pipelines = %v, want [deploy build lint]", names)
	}
	if got[0].LastRunID != "" || got[0].LastStatus != "" {
		t.Errorf("deploy has never run but reports run %q status %q", got[0].LastRunID, got[0].LastStatus)
	}
	build := got[1]
	if build.LastRunID != "run-build-2" || build.LastStatus != "failed" || build.LastRunAt == nil || !build.LastRunAt.Equal(base.Add(10*time.Minute)) {
		t.Errorf("build latest = %+v, want run-build-2 failed at %s", build, base.Add(10*time.Minute))
	}

	// Negative control: the other team's pipeline is in the store and visible
	// to its own team, so its absence above is the scope and not an empty read.
	theirs, err := bravo.ListPipelines(ctx, 200)
	if err != nil {
		t.Fatalf("ListPipelines bravo: %v", err)
	}
	if names := pipelineNames(theirs); len(names) != 1 || names[0] != "bravo-only" {
		t.Fatalf("bravo pipelines = %v, want [bravo-only]", names)
	}

	limited, err := alpha.ListPipelines(ctx, 2)
	if err != nil {
		t.Fatalf("ListPipelines limit: %v", err)
	}
	if names := pipelineNames(limited); len(names) != 2 || names[0] != "deploy" || names[1] != "build" {
		t.Fatalf("alpha pipelines limit 2 = %v, want [deploy build]", names)
	}
}

func ptrTime(t time.Time) *time.Time { return &t }
