package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// TestReapStaleRunningRuns_FlipsOrphanedRunsToFailed covers the
// Mode 4 orphan case: a dispatching orchestrator stops pinging
// last_heartbeat_at (laptop closed, network gone, process killed)
// while a run sits at status='running' without any active node
// claim. After the grace window, the controller-side reaper should
// flip the run to 'failed' and cascade non-done nodes.
func TestReapStaleRunningRuns_FlipsOrphanedRunsToFailed(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()

	const runID, nodeRunning, nodePending = "run-orphan", "node-running", "node-pending"
	seedRunAndNode(t, s, runID, nodeRunning)
	if err := s.CreateNode(ctx, store.Node{
		RunID:  runID,
		NodeID: nodePending,
		Status: "pending",
	}); err != nil {
		t.Fatalf("CreateNode pending: %v", err)
	}
	if err := s.StartNode(ctx, runID, nodeRunning); err != nil {
		t.Fatalf("StartNode: %v", err)
	}
	if err := s.TouchRunHeartbeat(ctx, runID); err != nil {
		t.Fatalf("TouchRunHeartbeat: %v", err)
	}

	if _, err := s.DB().ExecContext(ctx,
		`UPDATE runs SET last_heartbeat_at = ? WHERE id = ?`,
		time.Now().Add(-10*time.Minute).UnixNano(), runID); err != nil {
		t.Fatalf("backdate heartbeat: %v", err)
	}

	ids, err := store.Maintenance.ReapStaleRunningRuns(s, ctx,
		1*time.Minute, "test reason")
	if err != nil {
		t.Fatalf("reap: %v", err)
	}
	if len(ids) != 1 || ids[0] != runID {
		t.Fatalf("expected to reap [%s], got %v", runID, ids)
	}

	run, err := s.GetRun(ctx, runID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.Status != "failed" {
		t.Errorf("expected run status=failed, got %q", run.Status)
	}
	if run.Error != "test reason" {
		t.Errorf("expected run error=%q, got %q", "test reason", run.Error)
	}
	if run.FinishedAt == nil {
		t.Error("expected finished_at set on reaped run")
	}

	runningNode, err := s.GetNode(ctx, runID, nodeRunning)
	if err != nil {
		t.Fatalf("GetNode running: %v", err)
	}
	if runningNode.Status != "done" || runningNode.Outcome != "failed" {
		t.Errorf("running node: want status=done outcome=failed; got status=%q outcome=%q",
			runningNode.Status, runningNode.Outcome)
	}
	if runningNode.FailureReason != "orphaned" {
		t.Errorf("running node failure_reason: want orphaned, got %q", runningNode.FailureReason)
	}

	pendingNode, err := s.GetNode(ctx, runID, nodePending)
	if err != nil {
		t.Fatalf("GetNode pending: %v", err)
	}
	if pendingNode.Status != "done" || pendingNode.Outcome != "cancelled" {
		t.Errorf("pending node: want status=done outcome=cancelled; got status=%q outcome=%q",
			pendingNode.Status, pendingNode.Outcome)
	}
	if pendingNode.FailureReason != "orphaned" {
		t.Errorf("pending node failure_reason: want orphaned, got %q", pendingNode.FailureReason)
	}
}

// TestReapStaleRunningRuns_IgnoresFreshHeartbeat verifies that a run
// whose orchestrator is still pinging within the grace window is left
// alone.
func TestReapStaleRunningRuns_IgnoresFreshHeartbeat(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()

	const runID, nodeID = "run-fresh", "node-a"
	seedRunAndNode(t, s, runID, nodeID)
	if err := s.TouchRunHeartbeat(ctx, runID); err != nil {
		t.Fatalf("TouchRunHeartbeat: %v", err)
	}

	ids, err := store.Maintenance.ReapStaleRunningRuns(s, ctx,
		1*time.Minute, "test reason")
	if err != nil {
		t.Fatalf("reap: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("fresh heartbeat should be inside grace window; got %v", ids)
	}
	run, err := s.GetRun(ctx, runID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.Status != "running" {
		t.Errorf("expected status=running, got %q", run.Status)
	}
}

// TestReapStaleRunningRuns_IgnoresNullHeartbeat verifies that rows
// with last_heartbeat_at IS NULL are skipped. Those predate the
// feature or come from local/S3 modes which have their own orphan
// reconciliation path, and shouldn't get reaped by this sweep.
func TestReapStaleRunningRuns_IgnoresNullHeartbeat(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()

	const runID, nodeID = "run-noheartbeat", "node-a"
	seedRunAndNode(t, s, runID, nodeID)
	if _, err := s.DB().ExecContext(ctx,
		`UPDATE runs SET last_heartbeat_at = NULL WHERE id = ?`, runID); err != nil {
		t.Fatalf("clear heartbeat: %v", err)
	}
	ids, err := store.Maintenance.ReapStaleRunningRuns(s, ctx,
		1*time.Nanosecond, "test reason")
	if err != nil {
		t.Fatalf("reap: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("NULL heartbeat must be ignored; got %v", ids)
	}
	run, err := s.GetRun(ctx, runID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.Status != "running" {
		t.Errorf("expected status=running, got %q", run.Status)
	}
}

// TestReapStaleRunningRuns_IgnoresTerminalRuns verifies that runs
// already in a terminal status aren't re-touched by the sweep, even
// if their last_heartbeat_at predates the grace window.
func TestReapStaleRunningRuns_IgnoresTerminalRuns(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()

	const runID, nodeID = "run-already-done", "node-a"
	seedRunAndNode(t, s, runID, nodeID)
	if err := s.TouchRunHeartbeat(ctx, runID); err != nil {
		t.Fatalf("TouchRunHeartbeat: %v", err)
	}
	if err := s.FinishRun(ctx, runID, "success", ""); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}
	if _, err := s.DB().ExecContext(ctx,
		`UPDATE runs SET last_heartbeat_at = ? WHERE id = ?`,
		time.Now().Add(-10*time.Minute).UnixNano(), runID); err != nil {
		t.Fatalf("backdate heartbeat: %v", err)
	}

	ids, err := store.Maintenance.ReapStaleRunningRuns(s, ctx,
		1*time.Minute, "test reason")
	if err != nil {
		t.Fatalf("reap: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("terminal run must not be reaped; got %v", ids)
	}
	run, err := s.GetRun(ctx, runID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.Status != "success" {
		t.Errorf("expected status=success preserved, got %q", run.Status)
	}
}
