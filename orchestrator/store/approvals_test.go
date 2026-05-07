package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
)

func TestCreateApproval_FlipsNodeStatus(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()
	seedRunAndNode(t, s, "run-1", "gate")

	if err := s.CreateApproval(ctx, store.Approval{
		RunID:       "run-1",
		NodeID:      "gate",
		RequestedAt: time.Now(),
		Message:     "promote?",
		TimeoutMS:   60000,
		OnTimeout:   store.ApprovalOnTimeoutFail,
	}); err != nil {
		t.Fatalf("CreateApproval: %v", err)
	}

	n, err := s.GetNode(ctx, "run-1", "gate")
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if n.Status != store.NodeStatusApprovalPending {
		t.Fatalf("node status: %q", n.Status)
	}

	got, err := s.GetApproval(ctx, "run-1", "gate")
	if err != nil {
		t.Fatalf("GetApproval: %v", err)
	}
	if got.Message != "promote?" {
		t.Fatalf("message: %q", got.Message)
	}
	if got.ResolvedAt != nil {
		t.Fatalf("ResolvedAt should be nil on create")
	}
}

func TestResolveApproval_Approve(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()
	seedRunAndNode(t, s, "run-1", "gate")
	if err := s.CreateApproval(ctx, store.Approval{
		RunID: "run-1", NodeID: "gate", RequestedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	got, err := s.ResolveApproval(ctx, "run-1", "gate",
		store.ApprovalResolutionApproved, "alice", "lgtm")
	if err != nil {
		t.Fatalf("ResolveApproval: %v", err)
	}
	if got.Resolution != store.ApprovalResolutionApproved {
		t.Fatalf("resolution: %q", got.Resolution)
	}
	if got.Approver != "alice" || got.Comment != "lgtm" {
		t.Fatalf("approver/comment: %q %q", got.Approver, got.Comment)
	}
	if got.ResolvedAt == nil {
		t.Fatalf("ResolvedAt should be set")
	}
}

func TestResolveApproval_SecondResolveIsConflict(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()
	seedRunAndNode(t, s, "run-1", "gate")
	if err := s.CreateApproval(ctx, store.Approval{
		RunID: "run-1", NodeID: "gate", RequestedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResolveApproval(ctx, "run-1", "gate",
		store.ApprovalResolutionApproved, "alice", ""); err != nil {
		t.Fatal(err)
	}
	_, err := s.ResolveApproval(ctx, "run-1", "gate",
		store.ApprovalResolutionDenied, "bob", "")
	if !errors.Is(err, store.ErrLockHeld) {
		t.Fatalf("expected ErrLockHeld, got %v", err)
	}
}

func TestResolveApproval_NotFound(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()
	_, err := s.ResolveApproval(ctx, "nope", "nope",
		store.ApprovalResolutionApproved, "alice", "")
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestListPendingApprovals_OrdersByRequestedAsc(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()
	if err := s.CreateRun(ctx, store.Run{
		ID: "run-1", Pipeline: "demo", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b", "c"} {
		if err := s.CreateNode(ctx, store.Node{RunID: "run-1", NodeID: id, Status: "pending"}); err != nil {
			t.Fatal(err)
		}
	}

	t0 := time.Now().Add(-3 * time.Second)
	t1 := time.Now().Add(-2 * time.Second)
	t2 := time.Now().Add(-1 * time.Second)
	_ = s.CreateApproval(ctx, store.Approval{RunID: "run-1", NodeID: "b", RequestedAt: t1})
	_ = s.CreateApproval(ctx, store.Approval{RunID: "run-1", NodeID: "a", RequestedAt: t0})
	_ = s.CreateApproval(ctx, store.Approval{RunID: "run-1", NodeID: "c", RequestedAt: t2})

	// Resolve "a" so it should no longer appear in pending.
	if _, err := s.ResolveApproval(ctx, "run-1", "a",
		store.ApprovalResolutionApproved, "alice", ""); err != nil {
		t.Fatal(err)
	}

	pend, err := s.ListPendingApprovals(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pend) != 2 {
		t.Fatalf("expected 2 pending, got %d", len(pend))
	}
	if pend[0].NodeID != "b" || pend[1].NodeID != "c" {
		t.Fatalf("bad order: %q %q", pend[0].NodeID, pend[1].NodeID)
	}
}

func TestListApprovalsForRun_IncludesResolved(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()
	seedRunAndNode(t, s, "run-1", "gate")
	if err := s.CreateApproval(ctx, store.Approval{
		RunID: "run-1", NodeID: "gate", RequestedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResolveApproval(ctx, "run-1", "gate",
		store.ApprovalResolutionDenied, "alice", "nope"); err != nil {
		t.Fatal(err)
	}
	rows, err := s.ListApprovalsForRun(ctx, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Resolution != store.ApprovalResolutionDenied {
		t.Fatalf("unexpected rows: %+v", rows)
	}
}

func TestCreateApproval_OverwritesPreviousRow(t *testing.T) {
	// A retry of a gated node asks for approval again; CreateApproval
	// must reset resolution so the waiter isn't handed stale state.
	s := newStoreT(t)
	ctx := context.Background()
	seedRunAndNode(t, s, "run-1", "gate")
	_ = s.CreateApproval(ctx, store.Approval{RunID: "run-1", NodeID: "gate", RequestedAt: time.Now()})
	_, _ = s.ResolveApproval(ctx, "run-1", "gate", store.ApprovalResolutionDenied, "alice", "")

	if err := s.CreateApproval(ctx, store.Approval{
		RunID: "run-1", NodeID: "gate", RequestedAt: time.Now(), Message: "try again?",
	}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetApproval(ctx, "run-1", "gate")
	if got.Resolution != "" || got.ResolvedAt != nil {
		t.Fatalf("expected reset: %+v", got)
	}
	if got.Message != "try again?" {
		t.Fatalf("message: %q", got.Message)
	}
}
