package store_test

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestFinishAtGenerationUsesTheRunsTeam(t *testing.T) {
	s := storetest.Open(t)
	ctx := t.Context()
	teamA := tenantFor(t, s, "team-a")
	teamB := tenantFor(t, s, "team-b")
	now := time.Now()
	for _, entry := range []struct {
		tenant *store.Tenant
		id     string
	}{
		{teamA, "run-a"},
		{teamB, "run-b"},
	} {
		if err := entry.tenant.CreateTriggerWithRun(ctx,
			store.Trigger{ID: entry.id, Pipeline: "build", CreatedAt: now},
			store.Run{ID: entry.id, Pipeline: "build", Status: "running", CreatedAt: now, StartedAt: now},
		); err != nil {
			t.Fatal(err)
		}
	}
	finished, err := s.FinishRunAtGeneration(ctx, "run-b", 0, "success", "")
	if err != nil || !finished {
		t.Fatalf("finish team B run = %v, %v", finished, err)
	}
	runB, err := teamB.GetRun(ctx, "run-b")
	if err != nil || runB.Status != "success" {
		t.Fatalf("team B run = %+v, %v", runB, err)
	}
	runA, err := teamA.GetRun(ctx, "run-a")
	if err != nil || runA.Status != "running" {
		t.Fatalf("team A run = %+v, %v", runA, err)
	}
}

func TestFinishAtGenerationCannotFinishAnotherTeamsRunWithACollidingTriggerID(t *testing.T) {
	s := storetest.Open(t)
	ctx := t.Context()
	other := tenantFor(t, s, "other-team")
	now := time.Now()
	if err := other.CreateRun(ctx, store.Run{
		ID: "shared-id", Pipeline: "private", Status: "running", CreatedAt: now, StartedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateTrigger(ctx, store.Trigger{
		ID: "shared-id", Pipeline: "operator", TriggerSource: "api", CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	claim, err := s.ClaimNextTrigger(ctx, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if finished, err := s.FinishRunAtGeneration(ctx, "shared-id", claim.ClaimSeq, "failed", "wrong team"); finished || err == nil {
		t.Fatalf("colliding trigger finished another team's run: finished=%v err=%v", finished, err)
	}
	run, err := other.GetRun(ctx, "shared-id")
	if err != nil || run.Status != "running" || run.FinishedAt != nil {
		t.Fatalf("other team's run changed: %+v, %v", run, err)
	}
}
