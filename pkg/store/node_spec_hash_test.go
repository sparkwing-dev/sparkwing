package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

// The spec_hash a run host records on a node is what the node's claim reads
// back, so a remote host checks its own plan against the accepted one.
func TestCreateNode_KeepsTheAcceptedSpecHashForTheClaim(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	const hash = "sha256:9426d2b1671061a6b509ce60d3d811ec20e983a666acdc3fefc057eec4c3cf32"
	if err := s.CreateRun(ctx, store.Run{ID: "r", Pipeline: "p", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateNode(ctx, store.Node{RunID: "r", NodeID: "build", Status: "pending", SpecHash: hash}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetNode(ctx, "r", "build")
	if err != nil || got.SpecHash != hash {
		t.Fatalf("node = %+v, %v; want spec_hash %s", got, err, hash)
	}
	claimed, err := s.ClaimNamedNode(ctx, store.ClaimIdentity{}, "r", "build", "k8s-job:x", time.Minute, store.NamedClaimOptions{})
	if err != nil || claimed.SpecHash != hash {
		t.Fatalf("claimed node = %+v, %v; want spec_hash %s", claimed, err, hash)
	}
}

// hack: one fixture per guard, each a claimed trigger's run with node "lost" failed as agent_lost, so a guard's own
// test fails when that guard alone is removed.
func lostNodeRun(t *testing.T, runID string) (*store.Store, context.Context) {
	t.Helper()
	s := storetest.Open(t)
	ctx := context.Background()
	identity := store.ClaimIdentity{Principal: "runner", TokenPrefix: "swr_resume"}
	if err := s.CreateTrigger(ctx, store.Trigger{ID: runID, Pipeline: "p", Status: "pending", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	trigger, err := s.ClaimNextTriggerFor(ctx, identity, time.Minute, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	host := store.WithTriggerClaimFence(ctx, store.TriggerClaimFence{Claimant: identity, ClaimGeneration: trigger.ClaimSeq})
	if err := s.CreateRun(host, store.Run{ID: runID, Pipeline: "p", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateNode(host, store.Node{RunID: runID, NodeID: "lost", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishNodeWithReason(host, runID, "lost", "failed", "holder lost", nil, store.FailureAgentLost, nil); err != nil {
		t.Fatal(err)
	}
	return s, host
}

// safety: a node failed as agent_lost runs again only for its run's own
// trigger holder resuming the run, so the node cannot run in two places.
func TestResetNodeForAutoRetry_ReopensALostNodeForItsResumingTriggerHolder(t *testing.T) {
	s, host := lostNodeRun(t, "run-lost")
	if err := s.ResetNodeForAutoRetry(context.Background(), "run-lost", "lost"); !errors.Is(err, store.ErrLockHeld) {
		t.Fatalf("reset without the trigger's claim = %v, want refused", err)
	}
	if err := s.ResetNodeForAutoRetry(host, "run-lost", "lost"); err != nil {
		t.Fatalf("reset by the resuming trigger holder: %v", err)
	}
	if n, err := s.GetNode(host, "run-lost", "lost"); err != nil || n.Status != "pending" || n.FailureReason != "" {
		t.Fatalf("lost after the reset = %+v %v, want pending", n, err)
	}
}

// safety: a cancel requested on the run keeps its lost node failed.
func TestResetNodeForAutoRetry_KeepsALostNodeOnceACancelIsRequested(t *testing.T) {
	s, host := lostNodeRun(t, "run-cancel")
	if err := s.RequestCancel(context.Background(), "run-cancel"); err != nil {
		t.Fatal(err)
	}
	if err := s.ResetNodeForAutoRetry(host, "run-cancel", "lost"); !errors.Is(err, store.ErrLockHeld) {
		t.Fatalf("reset after a cancel was requested = %v, want refused", err)
	}
}

// safety: a finished run keeps its lost node failed.
func TestResetNodeForAutoRetry_KeepsALostNodeOfAFinishedRun(t *testing.T) {
	s, host := lostNodeRun(t, "run-finished")
	if err := s.FinishRun(host, "run-finished", "failed", "nodes failed"); err != nil {
		t.Fatal(err)
	}
	if err := s.ResetNodeForAutoRetry(host, "run-finished", "lost"); !errors.Is(err, store.ErrLockHeld) {
		t.Fatalf("reset of a finished run's node = %v, want refused", err)
	}
}

// safety: once an agent-loss retry run took the run over, the lost node runs
// there, so the source run keeps it failed.
func TestResetNodeForAutoRetry_KeepsALostNodeAnAgentLossRetryTookOver(t *testing.T) {
	s, host := lostNodeRun(t, "run-retried")
	if _, err := s.DB().ExecContext(context.Background(), storetest.Rebind(s, `INSERT INTO agent_loss_retries (run_id, source_run_id, root_run_id, cause_nodes_json, available_at, deadline_at, retry_count)
VALUES (?, ?, ?, '["lost"]', 0, 0, 1)`), "run-retried", "run-retried", "run-retried"); err != nil {
		t.Fatal(err)
	}
	if err := s.ResetNodeForAutoRetry(host, "run-retried", "lost"); !errors.Is(err, store.ErrLockHeld) {
		t.Fatalf("reset after an agent-loss retry took the run = %v, want refused", err)
	}
}
