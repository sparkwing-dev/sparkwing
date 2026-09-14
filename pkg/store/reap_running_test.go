package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestReapStaleRunningRuns_FlipsOrphanedRunsToFailed(t *testing.T) {
	s := storetest.Open(t)
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

	if _, err := s.DB().ExecContext(ctx, storetest.Rebind(s,
		`UPDATE runs SET last_heartbeat_at = ? WHERE id = ?`),
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

func TestReapStaleRunningRuns_IgnoresFreshHeartbeat(t *testing.T) {
	s := storetest.Open(t)
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

func TestReapStaleRunningRuns_IgnoresNullHeartbeat(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()

	const runID, nodeID = "run-noheartbeat", "node-a"
	seedRunAndNode(t, s, runID, nodeID)
	if _, err := s.DB().ExecContext(ctx, storetest.Rebind(s,
		`UPDATE runs SET last_heartbeat_at = NULL WHERE id = ?`), runID); err != nil {
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

func TestReapStaleRunningRuns_IgnoresTerminalRuns(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()

	const runID, nodeID = "run-already-done", "node-a"
	seedRunAndNode(t, s, runID, nodeID)
	if err := s.TouchRunHeartbeat(ctx, runID); err != nil {
		t.Fatalf("TouchRunHeartbeat: %v", err)
	}
	if err := s.FinishRun(ctx, runID, "success", ""); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}
	if _, err := s.DB().ExecContext(ctx, storetest.Rebind(s,
		`UPDATE runs SET last_heartbeat_at = ? WHERE id = ?`),
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

func requeueClaimedTrigger(t *testing.T, s *store.Store, id, pipeline string) {
	t.Helper()
	ctx := context.Background()
	if err := s.CreateTrigger(ctx, store.Trigger{
		ID:        id,
		Pipeline:  pipeline,
		CreatedAt: time.Now().Add(-10 * time.Minute),
	}); err != nil {
		t.Fatalf("CreateTrigger: %v", err)
	}
	if _, err := s.ClaimNextTrigger(ctx, time.Nanosecond); err != nil {
		t.Fatalf("ClaimNextTrigger: %v", err)
	}
	ids, err := store.Maintenance.ReapExpiredTriggers(s, ctx)
	if err != nil {
		t.Fatalf("ReapExpiredTriggers: %v", err)
	}
	if len(ids) != 1 || ids[0] != id {
		t.Fatalf("expected the expired claim to requeue [%s], got %v", id, ids)
	}
}

func TestReapStaleRunningRuns_LeavesRequeuedTriggerRunsAlone(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()

	const runID, nodeID = "run-requeued", "node-a"
	requeueClaimedTrigger(t, s, runID, "demo")
	seedRunAndNode(t, s, runID, nodeID)
	if err := s.TouchRunHeartbeat(ctx, runID); err != nil {
		t.Fatalf("TouchRunHeartbeat: %v", err)
	}
	if _, err := s.DB().ExecContext(ctx, storetest.Rebind(s,
		`UPDATE runs SET last_heartbeat_at = ? WHERE id = ?`),
		time.Now().Add(-10*time.Minute).UnixNano(), runID); err != nil {
		t.Fatalf("backdate heartbeat: %v", err)
	}

	ids, err := store.Maintenance.ReapStaleRunningRuns(s, ctx,
		1*time.Minute, "test reason")
	if err != nil {
		t.Fatalf("reap: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("a run whose trigger is back in the claim queue must survive; got %v", ids)
	}
	run, err := s.GetRun(ctx, runID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.Status != "running" {
		t.Errorf("expected status=running, got %q", run.Status)
	}
	node, err := s.GetNode(ctx, runID, nodeID)
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if node.Status != "pending" {
		t.Errorf("expected the node to stay pending, got %q", node.Status)
	}
}

func TestReapQueueExpiredRuns_FailsRunsPastTheQueueDeadline(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()

	const runID = "run-queue-expired"
	requeueClaimedTrigger(t, s, runID, "demo")
	if err := s.CreateRun(ctx, store.Run{
		ID:        runID,
		Pipeline:  "demo",
		Status:    "pending",
		StartedAt: time.Now().Add(-20 * time.Minute),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	ids, err := store.Maintenance.ReapQueueExpiredRuns(s, ctx,
		15*time.Minute, 3*time.Minute, "test deadline")
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
		t.Errorf("expected status=failed, got %q", run.Status)
	}
	if run.Error != "test deadline" {
		t.Errorf("expected error=%q, got %q", "test deadline", run.Error)
	}
	if run.FinishedAt == nil {
		t.Error("expected finished_at to be set")
	}
}

func TestReapQueueExpiredRuns_LeavesRunsInsideTheDeadlineAlone(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()

	const runID = "run-queue-waiting"
	requeueClaimedTrigger(t, s, runID, "demo")
	if err := s.CreateRun(ctx, store.Run{
		ID:        runID,
		Pipeline:  "demo",
		Status:    "pending",
		StartedAt: time.Now().Add(-2 * time.Minute),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	ids, err := store.Maintenance.ReapQueueExpiredRuns(s, ctx,
		15*time.Minute, 3*time.Minute, "test deadline")
	if err != nil {
		t.Fatalf("reap: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("a run inside the queue deadline must survive; got %v", ids)
	}
	run, err := s.GetRun(ctx, runID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.Status != "pending" {
		t.Errorf("expected status=pending, got %q", run.Status)
	}
}

func TestReapQueueExpiredRuns_LeavesUnclaimedTriggersAlone(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()

	const runID = "run-never-claimed"
	if err := s.CreateTrigger(ctx, store.Trigger{
		ID:        runID,
		Pipeline:  "demo",
		CreatedAt: time.Now().Add(-40 * time.Minute),
	}); err != nil {
		t.Fatalf("CreateTrigger: %v", err)
	}
	if err := s.CreateRun(ctx, store.Run{
		ID:        runID,
		Pipeline:  "demo",
		Status:    "pending",
		StartedAt: time.Now().Add(-40 * time.Minute),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	ids, err := store.Maintenance.ReapQueueExpiredRuns(s, ctx,
		15*time.Minute, 3*time.Minute, "test deadline")
	if err != nil {
		t.Fatalf("reap: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("a trigger no claimant ever held keeps its existing fate; got %v", ids)
	}
}

func TestReapQueueExpiredRuns_LeavesALiveHeartbeatingRunAlone(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()

	const runID = "run-live-long"
	requeueClaimedTrigger(t, s, runID, "demo")
	if err := s.CreateRun(ctx, store.Run{
		ID:        runID,
		Pipeline:  "demo",
		Status:    "running",
		StartedAt: time.Now().Add(-20 * time.Minute),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := s.TouchRunHeartbeat(ctx, runID); err != nil {
		t.Fatalf("TouchRunHeartbeat: %v", err)
	}

	ids, err := store.Maintenance.ReapQueueExpiredRuns(s, ctx,
		15*time.Minute, 3*time.Minute, "test deadline")
	if err != nil {
		t.Fatalf("reap: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("a run still heartbeating must outlive its claimant; got %v", ids)
	}
	run, err := s.GetRun(ctx, runID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.Status != "running" {
		t.Errorf("expected status=running, got %q", run.Status)
	}
}

func TestReapQueueExpiredRuns_FailsARunWhoseOrchestratorWentSilent(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()

	const runID = "run-silent"
	requeueClaimedTrigger(t, s, runID, "demo")
	if err := s.CreateRun(ctx, store.Run{
		ID:        runID,
		Pipeline:  "demo",
		Status:    "running",
		StartedAt: time.Now().Add(-20 * time.Minute),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if _, err := s.DB().ExecContext(ctx, storetest.Rebind(s,
		`UPDATE runs SET last_heartbeat_at = ? WHERE id = ?`),
		time.Now().Add(-10*time.Minute).UnixNano(), runID); err != nil {
		t.Fatalf("backdate heartbeat: %v", err)
	}

	ids, err := store.Maintenance.ReapQueueExpiredRuns(s, ctx,
		15*time.Minute, 3*time.Minute, "test deadline")
	if err != nil {
		t.Fatalf("reap: %v", err)
	}
	if len(ids) != 1 || ids[0] != runID {
		t.Fatalf("expected to reap [%s], got %v", runID, ids)
	}
}

func TestReapQueueExpiredRuns_FinishesTheTriggerItGaveUpOn(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()

	const runID = "run-queue-abandoned"
	requeueClaimedTrigger(t, s, runID, "demo")
	if err := s.CreateRun(ctx, store.Run{
		ID:        runID,
		Pipeline:  "demo",
		Status:    "pending",
		StartedAt: time.Now().Add(-20 * time.Minute),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	if _, err := store.Maintenance.ReapQueueExpiredRuns(s, ctx,
		15*time.Minute, 3*time.Minute, "test deadline"); err != nil {
		t.Fatalf("reap: %v", err)
	}

	trig, err := s.GetTrigger(ctx, runID)
	if err != nil {
		t.Fatalf("GetTrigger: %v", err)
	}
	if !trig.IsFinished() {
		t.Errorf("trigger left claimable after its run failed: status=%q", trig.Status)
	}
	claimed, err := s.ClaimNextTrigger(ctx, time.Minute)
	if err == nil {
		t.Fatalf("a late runner still claimed the abandoned trigger: %v", claimed)
	}
}
