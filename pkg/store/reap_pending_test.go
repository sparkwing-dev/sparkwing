package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestReapStalePendingRuns_FlipsDoneTriggerPendingRunsToFailed(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()

	stuckID := "run-stuck"
	if err := s.CreateTrigger(ctx, store.Trigger{
		ID:        stuckID,
		Pipeline:  "weather-report",
		CreatedAt: time.Now().Add(-10 * time.Minute),
	}); err != nil {
		t.Fatalf("CreateTrigger: %v", err)
	}
	if err := s.CreateRun(ctx, store.Run{
		ID:        stuckID,
		Pipeline:  "weather-report",
		Status:    "pending",
		StartedAt: time.Now().Add(-10 * time.Minute),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := s.FinishTrigger(ctx, stuckID); err != nil {
		t.Fatalf("FinishTrigger: %v", err)
	}

	ids, err := store.Maintenance.ReapStalePendingRuns(s, ctx,
		1*time.Minute, "test reason")
	if err != nil {
		t.Fatalf("reap: %v", err)
	}
	if len(ids) != 1 || ids[0] != stuckID {
		t.Fatalf("expected to reap [%s], got %v", stuckID, ids)
	}

	run, err := s.GetRun(ctx, stuckID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.Status != "failed" {
		t.Errorf("expected status=failed, got %q", run.Status)
	}
	if run.Error != "test reason" {
		t.Errorf("expected error=%q, got %q", "test reason", run.Error)
	}
	if run.FinishedAt == nil {
		t.Error("expected finished_at to be set")
	}
}

func TestReapStalePendingRuns_LeavesRunsWithLiveTriggerAlone(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()

	pendingTriggerID := "run-pending-trigger"
	if err := s.CreateTrigger(ctx, store.Trigger{
		ID:        pendingTriggerID,
		Pipeline:  "weather-report",
		CreatedAt: time.Now().Add(-10 * time.Minute),
	}); err != nil {
		t.Fatalf("CreateTrigger: %v", err)
	}
	if err := s.CreateRun(ctx, store.Run{
		ID:        pendingTriggerID,
		Pipeline:  "weather-report",
		Status:    "pending",
		StartedAt: time.Now().Add(-10 * time.Minute),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	ids, err := store.Maintenance.ReapStalePendingRuns(s, ctx,
		1*time.Minute, "test reason")
	if err != nil {
		t.Fatalf("reap: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("expected no reaps; trigger still pending. got %v", ids)
	}
	run, err := s.GetRun(ctx, pendingTriggerID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.Status != "pending" {
		t.Errorf("expected status=pending, got %q", run.Status)
	}
}

func TestReapStalePendingRuns_RespectsGracePeriod(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()

	freshID := "run-fresh"
	if err := s.CreateTrigger(ctx, store.Trigger{
		ID:        freshID,
		Pipeline:  "weather-report",
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("CreateTrigger: %v", err)
	}
	if err := s.CreateRun(ctx, store.Run{
		ID:        freshID,
		Pipeline:  "weather-report",
		Status:    "pending",
		StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := s.FinishTrigger(ctx, freshID); err != nil {
		t.Fatalf("FinishTrigger: %v", err)
	}

	ids, err := store.Maintenance.ReapStalePendingRuns(s, ctx,
		1*time.Minute, "test reason")
	if err != nil {
		t.Fatalf("reap: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("fresh stuck run should be inside grace window; got reaps %v", ids)
	}
}
