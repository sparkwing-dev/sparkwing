package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

// A trigger returned to the queue and taken by another consumer must
// stop answering to the previous claimant's token, because that claim
// is what authorizes the run's writes and its secret reads.
func TestTriggerClaim_ReclaimDropsPreviousClaimant(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	runnerA := store.ClaimIdentity{Principal: "runner-a", TokenPrefix: "swr_aaaaaaaa"}

	seedPending(t, s, "t1")
	if _, err := s.ClaimSpecificTriggerFor(ctx, "t1", runnerA, time.Minute); err != nil {
		t.Fatalf("ClaimSpecificTriggerFor: %v", err)
	}
	expireTriggerClaim(t, s, "t1")

	ids, err := teststore.ReapUnits(ctx, s)
	if err != nil {
		t.Fatalf("ReapUnits: %v", err)
	}
	if len(ids) != 1 || ids[0].RunID != "t1" {
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

// The claimant recorded on a trigger row outlives the claim's lease and
// the trigger's own completion, which is what lets the holder close out
// or retry a close.
func TestTriggerClaim_ClaimantOutlivesLeaseAndCompletion(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	runnerA := store.ClaimIdentity{Principal: "runner-a", TokenPrefix: "swr_aaaaaaaa"}

	seedPending(t, s, "t1")
	if _, err := s.ClaimSpecificTriggerFor(ctx, "t1", runnerA, time.Hour); err != nil {
		t.Fatalf("ClaimSpecificTriggerFor: %v", err)
	}
	got, err := tenant(t, s, store.DefaultTeam).TriggerClaimant(ctx, "t1")
	if err != nil {
		t.Fatalf("TriggerClaimant: %v", err)
	}
	if got != runnerA {
		t.Fatalf("TriggerClaimant = %+v, want %+v", got, runnerA)
	}

	if err := teststore.ForTeam(t, s, store.DefaultTeam).FinishTrigger(ctx, "t1"); err != nil {
		t.Fatalf("FinishTrigger: %v", err)
	}
	got, err = tenant(t, s, store.DefaultTeam).TriggerClaimant(ctx, "t1")
	if err != nil {
		t.Fatalf("TriggerClaimant after finish: %v", err)
	}
	if got != runnerA {
		t.Errorf("TriggerClaimant after finish = %+v, want the holder %+v", got, runnerA)
	}
}
