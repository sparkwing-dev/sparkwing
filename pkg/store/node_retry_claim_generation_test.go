package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

// An automatic retry is a fresh attempt at the node, so its claim reports the
// wait since the retry made the node claimable rather than carrying the failed
// attempt's claim generation forward.
func TestResetNodeForAutoRetry_MakesTheNextClaimAFirstClaim(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	identity := store.ClaimIdentity{Principal: "runner", TokenPrefix: "swr_retry"}

	readyNode(t, s, "run-retry", "node-a")
	first, err := s.ClaimNextReadyNode(ctx, identity, "holder-1", time.Minute, nil)
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if first.ClaimGeneration != 1 {
		t.Fatalf("first claim generation = %d, want 1", first.ClaimGeneration)
	}
	if first.PlacementHoldFrom == nil {
		t.Fatal("first claim carries no hold instant")
	}

	fenced := store.WithNodeClaimFence(ctx, store.NodeClaimFence{
		Claimant: identity, HolderID: first.ClaimedBy, MembershipID: first.ClaimMembershipID,
		ReservationID: first.ReservationID, ClaimGeneration: first.ClaimGeneration,
	})
	if err := s.FinishNodeWithReason(fenced, "run-retry", "node-a", "failed",
		"ordinary failure", nil, store.FailureUnknown, nil); err != nil {
		t.Fatalf("finish: %v", err)
	}
	if err := s.ResetNodeForAutoRetry(ctx, "run-retry", "node-a"); err != nil {
		t.Fatalf("ResetNodeForAutoRetry: %v", err)
	}
	if err := s.MarkNodeReady(ctx, "run-retry", "node-a"); err != nil {
		t.Fatalf("mark ready after the retry: %v", err)
	}

	second, err := s.ClaimNextReadyNode(ctx, identity, "holder-2", time.Minute, nil)
	if err != nil {
		t.Fatalf("retry claim: %v", err)
	}
	if second.ClaimGeneration != 1 {
		t.Errorf("retry claim generation = %d, want 1 so the wait is observed", second.ClaimGeneration)
	}
	if second.PlacementHoldFrom == nil {
		t.Fatal("the retry claim carries no hold instant")
	}
	if !second.PlacementHoldFrom.After(*first.PlacementHoldFrom) {
		t.Errorf("retry hold instant %v is not later than the first attempt's %v",
			second.PlacementHoldFrom, first.PlacementHoldFrom)
	}
}

// A lease-expiry requeue is the same attempt continuing, so its claim keeps the
// generation that says the wait already elapsed.
func TestReapExpiredNodeClaims_KeepsTheClaimGeneration(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	identity := store.ClaimIdentity{Principal: "runner", TokenPrefix: "swr_requeue"}

	readyNode(t, s, "run-requeue", "node-a")
	if _, err := s.ClaimNextReadyNode(ctx, identity, "holder-1", time.Minute, nil); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if _, err := s.DB().ExecContext(ctx, storetest.Rebind(s,
		`UPDATE nodes SET lease_expires_at = ? WHERE run_id = ? AND node_id = ?`),
		time.Now().Add(-time.Second).UnixNano(), "run-requeue", "node-a"); err != nil {
		t.Fatalf("expire the lease: %v", err)
	}
	if _, err := s.ReapExpiredNodeClaims(ctx); err != nil {
		t.Fatalf("ReapExpiredNodeClaims: %v", err)
	}

	second, err := s.ClaimNextReadyNode(ctx, identity, "holder-2", time.Minute, nil)
	if err != nil {
		t.Fatalf("requeue claim: %v", err)
	}
	if second.ClaimGeneration < 2 {
		t.Errorf("requeue claim generation = %d, want the attempt to carry on past 1", second.ClaimGeneration)
	}
}
