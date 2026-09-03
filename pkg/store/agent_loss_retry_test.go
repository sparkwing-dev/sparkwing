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
	if err := s.AcknowledgeNodeExecutionStart(ctx, n.RunID, n.NodeID, claimant, store.ExecutionStart{HolderID: "agent:box-a:1", ClaimGeneration: n.ClaimGeneration, AttemptOrdinal: 1}); err != nil {
		t.Fatalf("acknowledge: %v", err)
	}
	got, err := s.GetNode(ctx, n.RunID, n.NodeID)
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	if got.ExecutionStartedAt == nil {
		t.Fatal("execution start was not persisted")
	}
	if got.CoordinatorID == "" || got.ExecutorKind != "" || got.ExecutorID != "" || got.ReservationID != "" || got.ExecutorLocation != "unknown" {
		t.Fatalf("attempt identity = coordinator:%q kind:%q executor:%q reservation:%q",
			got.CoordinatorID, got.ExecutorKind, got.ExecutorID, got.ReservationID)
	}
	if err := s.AcknowledgeNodeExecutionStart(ctx, n.RunID, n.NodeID, claimant, store.ExecutionStart{HolderID: "agent:box-b:1", ClaimGeneration: n.ClaimGeneration, AttemptOrdinal: 1}); !errors.Is(err, store.ErrLockHeld) {
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
	heartbeatCtx := store.WithNodeClaimFence(ctx, store.NodeClaimFence{
		Claimant: claimant, HolderID: n.ClaimedBy, MembershipID: n.ClaimMembershipID,
		ReservationID: n.ReservationID, ClaimGeneration: n.ClaimGeneration,
	})
	if err := s.HeartbeatNodeClaim(heartbeatCtx, n.RunID, n.NodeID, claimant, n.ClaimedBy, time.Minute); !errors.Is(err, store.ErrLockHeld) {
		t.Fatalf("late heartbeat = %v, want ErrLockHeld", err)
	}
	if err := s.AcknowledgeNodeExecutionStart(ctx, n.RunID, n.NodeID, claimant, store.ExecutionStart{HolderID: n.ClaimedBy, ClaimGeneration: n.ClaimGeneration, AttemptOrdinal: 1}); !errors.Is(err, store.ErrLockHeld) {
		t.Fatalf("late execution start = %v, want ErrLockHeld", err)
	}
}

func TestLegacyClaimCannotSelfReportExecutorIdentity(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	coordinatorID, err := s.CoordinatorID(ctx)
	if err != nil {
		t.Fatalf("coordinator id: %v", err)
	}
	if err := s.CreateRun(ctx, store.Run{
		ID: "run-retry", Pipeline: "p", Status: "running", StartedAt: time.Now(),
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
	n, err := s.ClaimNextReadyNodeAs(ctx, claimant, "agent:box-a:2", time.Minute, nil,
		store.ExecutorIdentity{Kind: "agent", ID: "box-a", ReservationID: "untrusted"})
	if err != nil {
		t.Fatalf("alternate executor claim: %v", err)
	}
	if n.ExecutorID != "" || n.ExecutorKind != "" || n.ReservationID != "" || n.CoordinatorID != coordinatorID {
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
