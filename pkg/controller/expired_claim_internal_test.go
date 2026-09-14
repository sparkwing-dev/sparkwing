package controller

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func requeuedClaimServer(t *testing.T, runID, runStatus string) (*Server, *store.Store) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	if err := st.CreateTrigger(ctx, store.Trigger{
		ID: runID, Pipeline: "demo", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("CreateTrigger: %v", err)
	}
	if _, err := st.ClaimNextTrigger(ctx, time.Nanosecond); err != nil {
		t.Fatalf("ClaimNextTrigger: %v", err)
	}
	if err := st.CreateRun(ctx, store.Run{
		ID: runID, Pipeline: "demo", Status: runStatus, StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	ids, err := store.Maintenance.ReapExpiredTriggers(st, ctx)
	if err != nil {
		t.Fatalf("ReapExpiredTriggers: %v", err)
	}
	if len(ids) != 1 {
		t.Fatalf("expected one expired claim, got %v", ids)
	}
	return New(st, nil), st
}

func TestSettleExpiredTriggerClaim_KeepsDispatchedRunQueued(t *testing.T) {
	ctx := context.Background()
	srv, st := requeuedClaimServer(t, "run-warming", "pending")

	srv.settleExpiredTriggerClaim(ctx, "run-warming")

	run, err := st.GetRun(ctx, "run-warming")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.Status != "pending" || run.FinishedAt != nil {
		t.Fatalf("run = %s finished=%v, want it still queued for the next claimant",
			run.Status, run.FinishedAt)
	}
	claimed, err := st.ClaimNextTrigger(ctx, time.Minute)
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if claimed == nil || claimed.ID != "run-warming" {
		t.Fatalf("second claim = %v, want the requeued trigger", claimed)
	}
}

func TestSettleExpiredTriggerClaim_FailsRunTheDeadClaimantStarted(t *testing.T) {
	ctx := context.Background()
	srv, st := requeuedClaimServer(t, "run-started", "running")

	srv.settleExpiredTriggerClaim(ctx, "run-started")

	run, err := st.GetRun(ctx, "run-started")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.Status != "failed" {
		t.Fatalf("run status = %q, want failed", run.Status)
	}
	if run.Error != "runner lease expired" {
		t.Errorf("run error = %q, want the lease-expiry reason", run.Error)
	}
}
