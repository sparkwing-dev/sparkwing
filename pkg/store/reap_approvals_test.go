package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// TestReapTimedOutApprovals_ResolvesElapsedApprovals covers the bug
// the reaper was added for: an approval whose timeout_ms has elapsed
// but whose dispatching orchestrator went silent without resolving
// it. The reaper writes ApprovalResolutionTimedOut so a re-attached
// orchestrator can apply its on_timeout policy.
func TestReapTimedOutApprovals_ResolvesElapsedApprovals(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()
	seedRunAndNode(t, s, "run-stuck", "gate")

	// Approval requested 1 hour ago with a 2s timeout. Well past
	// the deadline.
	if err := s.CreateApproval(ctx, store.Approval{
		RunID:       "run-stuck",
		NodeID:      "gate",
		RequestedAt: time.Now().Add(-1 * time.Hour),
		TimeoutMS:   2000,
		OnTimeout:   store.ApprovalOnTimeoutApprove,
	}); err != nil {
		t.Fatalf("CreateApproval: %v", err)
	}

	pairs, err := store.Maintenance.ReapTimedOutApprovals(s, ctx)
	if err != nil {
		t.Fatalf("reap: %v", err)
	}
	if len(pairs) != 1 || pairs[0][0] != "run-stuck" || pairs[0][1] != "gate" {
		t.Fatalf("expected [[run-stuck,gate]]; got %v", pairs)
	}

	got, err := s.GetApproval(ctx, "run-stuck", "gate")
	if err != nil {
		t.Fatalf("GetApproval: %v", err)
	}
	if got.ResolvedAt == nil {
		t.Fatal("expected resolved_at set")
	}
	if got.Resolution != store.ApprovalResolutionTimedOut {
		t.Errorf("resolution = %q, want %q", got.Resolution, store.ApprovalResolutionTimedOut)
	}
	if got.Approver != "controller-reaper" {
		t.Errorf("approver = %q, want %q", got.Approver, "controller-reaper")
	}
}

// TestReapTimedOutApprovals_LeavesInsideWindow verifies the reaper
// doesn't touch approvals whose timeout hasn't elapsed yet.
func TestReapTimedOutApprovals_LeavesInsideWindow(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()
	seedRunAndNode(t, s, "run-active", "gate")

	if err := s.CreateApproval(ctx, store.Approval{
		RunID:       "run-active",
		NodeID:      "gate",
		RequestedAt: time.Now(),
		TimeoutMS:   60_000, // 60s, just started
		OnTimeout:   store.ApprovalOnTimeoutFail,
	}); err != nil {
		t.Fatalf("CreateApproval: %v", err)
	}

	pairs, err := store.Maintenance.ReapTimedOutApprovals(s, ctx)
	if err != nil {
		t.Fatalf("reap: %v", err)
	}
	if len(pairs) != 0 {
		t.Fatalf("expected no reaps; approval still inside its window. got %v", pairs)
	}
}

// TestReapTimedOutApprovals_IgnoresResolved verifies the reaper skips
// approvals that have already been resolved (human action, prior
// orchestrator action, idempotent re-runs).
func TestReapTimedOutApprovals_IgnoresResolved(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()
	seedRunAndNode(t, s, "run-done", "gate")

	if err := s.CreateApproval(ctx, store.Approval{
		RunID:       "run-done",
		NodeID:      "gate",
		RequestedAt: time.Now().Add(-1 * time.Hour),
		TimeoutMS:   2000,
		OnTimeout:   store.ApprovalOnTimeoutApprove,
	}); err != nil {
		t.Fatalf("CreateApproval: %v", err)
	}
	if _, err := s.ResolveApproval(ctx, "run-done", "gate",
		store.ApprovalResolutionApproved, "alice", "looks good"); err != nil {
		t.Fatalf("ResolveApproval: %v", err)
	}

	pairs, err := store.Maintenance.ReapTimedOutApprovals(s, ctx)
	if err != nil {
		t.Fatalf("reap: %v", err)
	}
	if len(pairs) != 0 {
		t.Fatalf("expected no reaps; approval already resolved. got %v", pairs)
	}

	got, err := s.GetApproval(ctx, "run-done", "gate")
	if err != nil {
		t.Fatalf("GetApproval: %v", err)
	}
	if got.Resolution != store.ApprovalResolutionApproved {
		t.Errorf("resolution overwritten: %q", got.Resolution)
	}
	if got.Approver != "alice" {
		t.Errorf("approver overwritten: %q", got.Approver)
	}
}
