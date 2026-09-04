package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// A trigger returned to the queue and taken by another consumer must
// stop answering to the previous claimant's token, because that claim
// is what authorizes the run's writes and its secret reads.
func TestTriggerClaim_ReclaimDropsPreviousClaimant(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()
	runnerA := store.ClaimIdentity{Principal: "runner-a", TokenPrefix: "swr_aaaaaaaa"}

	seedPending(t, s, "t1")
	if _, err := s.ClaimNextTriggerFor(ctx, runnerA, 10*time.Millisecond, nil, nil); err != nil {
		t.Fatalf("ClaimNextTriggerFor: %v", err)
	}
	time.Sleep(20 * time.Millisecond)

	ids, err := store.Maintenance.ReapExpiredTriggers(s, ctx)
	if err != nil {
		t.Fatalf("ReapExpiredTriggers: %v", err)
	}
	if len(ids) != 1 || ids[0] != "t1" {
		t.Fatalf("reaped = %v, want [t1]", ids)
	}
	if _, err := s.ClaimSpecificTrigger(ctx, "t1", time.Hour); err != nil {
		t.Fatalf("ClaimSpecificTrigger: %v", err)
	}

	held, err := s.PrincipalHoldsTriggerClaim(ctx, "t1", runnerA, time.Now())
	if err != nil {
		t.Fatalf("PrincipalHoldsTriggerClaim: %v", err)
	}
	if held {
		t.Fatal("runner-a still holds the claim after another consumer re-claimed the trigger")
	}
}
