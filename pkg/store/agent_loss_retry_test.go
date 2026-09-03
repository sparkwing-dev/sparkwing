package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestNodeExecutionStartAcknowledgementIsClaimBound(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	createRunAndReadyNode(t, s, "run-ack", "build")
	claimant := store.ClaimIdentity{Principal: "runner", TokenPrefix: "swr_runner"}
	n, err := s.ClaimNextReadyNodeAs(ctx, claimant, "agent:box-a:1", time.Minute, nil, store.ExecutorIdentity{
		Kind:          "agent",
		ID:            "box-a",
		ReservationID: "reserve-1",
	})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := s.AcknowledgeNodeExecutionStart(ctx, n.RunID, n.NodeID, claimant, "agent:box-a:1"); err != nil {
		t.Fatalf("acknowledge: %v", err)
	}
	got, err := s.GetNode(ctx, n.RunID, n.NodeID)
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	if got.ExecutionStartedAt == nil {
		t.Fatal("execution start was not persisted")
	}
	if got.CoordinatorID == "" || got.ExecutorKind != "agent" || got.ExecutorID != "box-a" || got.ReservationID != "reserve-1" {
		t.Fatalf("attempt identity = coordinator:%q kind:%q executor:%q reservation:%q",
			got.CoordinatorID, got.ExecutorKind, got.ExecutorID, got.ReservationID)
	}
	if err := s.AcknowledgeNodeExecutionStart(ctx, n.RunID, n.NodeID, claimant, "agent:box-b:1"); !errors.Is(err, store.ErrLockHeld) {
		t.Fatalf("wrong-holder acknowledgement = %v, want ErrLockHeld", err)
	}
}

func TestExpiredNodeClaimCannotBeRevivedOrStarted(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	createRunAndReadyNode(t, s, "run-expired", "build")
	claimant := store.ClaimIdentity{Principal: "runner", TokenPrefix: "swr_runner"}
	n, err := s.ClaimNextReadyNode(ctx, claimant, "agent:box-a:1", time.Millisecond, nil)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	time.Sleep(5 * time.Millisecond)
	if err := s.HeartbeatNodeClaim(ctx, n.RunID, n.NodeID, claimant, n.ClaimedBy, time.Minute); !errors.Is(err, store.ErrLockHeld) {
		t.Fatalf("late heartbeat = %v, want ErrLockHeld", err)
	}
	if err := s.AcknowledgeNodeExecutionStart(ctx, n.RunID, n.NodeID, claimant, n.ClaimedBy); !errors.Is(err, store.ErrLockHeld) {
		t.Fatalf("late execution start = %v, want ErrLockHeld", err)
	}
}

func TestRetryAvoidanceIsScopedToCoordinatorAndExecutor(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	coordinatorID, err := s.CoordinatorID(ctx)
	if err != nil {
		t.Fatalf("coordinator id: %v", err)
	}
	until := time.Now().Add(time.Minute)
	if err := s.CreateRun(ctx, store.Run{
		ID: "run-retry", Pipeline: "p", Status: "running", StartedAt: time.Now(),
		RetryAvoidCoordinatorID: coordinatorID,
		RetryAvoidExecutorKind:  "agent",
		RetryAvoidExecutorID:    "box-a",
		RetryAvoidUntil:         &until,
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := s.CreateNode(ctx, store.Node{RunID: "run-retry", NodeID: "build", Status: "pending"}); err != nil {
		t.Fatalf("create node: %v", err)
	}
	if err := s.MarkNodeReady(ctx, "run-retry", "build"); err != nil {
		t.Fatalf("mark ready: %v", err)
	}
	claimant := store.ClaimIdentity{Principal: "runner", TokenPrefix: "swr_runner"}
	if _, err := s.ClaimNextReadyNodeAs(ctx, claimant, "agent:box-a:2", time.Minute, nil,
		store.ExecutorIdentity{Kind: "agent", ID: "box-a"}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("failed executor claim = %v, want ErrNotFound", err)
	}
	n, err := s.ClaimNextReadyNodeAs(ctx, claimant, "agent:box-b:1", time.Minute, nil,
		store.ExecutorIdentity{Kind: "agent", ID: "box-b"})
	if err != nil {
		t.Fatalf("alternate executor claim: %v", err)
	}
	if n.ExecutorID != "box-b" || n.CoordinatorID != coordinatorID {
		t.Fatalf("claim identity = coordinator:%q executor:%q", n.CoordinatorID, n.ExecutorID)
	}
}

func createRunAndReadyNode(t *testing.T, s *store.Store, runID, nodeID string) {
	t.Helper()
	ctx := context.Background()
	if err := s.CreateRun(ctx, store.Run{ID: runID, Pipeline: "p", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := s.CreateNode(ctx, store.Node{RunID: runID, NodeID: nodeID, Status: "pending"}); err != nil {
		t.Fatalf("create node: %v", err)
	}
	if err := s.MarkNodeReady(ctx, runID, nodeID); err != nil {
		t.Fatalf("mark ready: %v", err)
	}
}
