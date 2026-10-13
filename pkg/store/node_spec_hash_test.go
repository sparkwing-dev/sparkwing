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

// safety: a node failed as agent_lost runs again only for its run's own
// trigger holder resuming the run, and never once an agent-loss retry run took
// the run over, so the node cannot run in both.
func TestResetNodeForAutoRetry_ReopensALostNodeOnlyForItsResumingTriggerHolder(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	identity := store.ClaimIdentity{Principal: "runner", TokenPrefix: "swr_resume"}
	if err := s.CreateTrigger(ctx, store.Trigger{ID: "run-lost", Pipeline: "p", Status: "pending", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	trigger, err := s.ClaimNextTriggerFor(ctx, identity, time.Minute, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	host := store.WithTriggerClaimFence(ctx, store.TriggerClaimFence{Claimant: identity, ClaimGeneration: trigger.ClaimSeq})
	if err := s.CreateRun(host, store.Run{ID: trigger.ID, Pipeline: "p", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"lost", "retried"} {
		if err := s.CreateNode(host, store.Node{RunID: trigger.ID, NodeID: id, Status: "pending"}); err != nil {
			t.Fatal(err)
		}
		if err := s.FinishNodeWithReason(host, trigger.ID, id, "failed", "holder lost", nil, store.FailureAgentLost, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.ResetNodeForAutoRetry(ctx, trigger.ID, "lost"); !errors.Is(err, store.ErrLockHeld) {
		t.Fatalf("reset without the trigger's claim = %v, want refused", err)
	}
	if err := s.ResetNodeForAutoRetry(host, trigger.ID, "lost"); err != nil {
		t.Fatalf("reset by the resuming trigger holder: %v", err)
	}
	if n, err := s.GetNode(ctx, trigger.ID, "lost"); err != nil || n.Status != "pending" || n.FailureReason != "" {
		t.Fatalf("lost after the reset = %+v %v, want pending", n, err)
	}
	if _, err := s.DB().ExecContext(ctx, `INSERT INTO agent_loss_retries (run_id, source_run_id, root_run_id, cause_nodes_json, available_at, deadline_at, retry_count)
VALUES (?, ?, ?, '["retried"]', 0, 0, 1)`, trigger.ID, trigger.ID, trigger.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.ResetNodeForAutoRetry(host, trigger.ID, "retried"); !errors.Is(err, store.ErrLockHeld) {
		t.Fatalf("reset after an agent-loss retry took the run = %v, want refused", err)
	}
}
