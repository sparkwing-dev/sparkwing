package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/storetest"
)

// A trigger returned to the queue and taken by another consumer must
// stop answering to the previous claimant's token, because that claim
// is what authorizes the run's writes and its secret reads.
func TestTriggerClaim_ReclaimDropsPreviousClaimant(t *testing.T) {
	s := storetest.Open(t)
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

func TestTriggerClaim_RequeueUnstartedClaimDropsClaimant(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	runnerA := store.ClaimIdentity{Principal: "runner-a", TokenPrefix: "swr_aaaaaaaa"}

	seedPending(t, s, "t1")
	if _, err := s.ClaimNextTriggerFor(ctx, runnerA, time.Hour, nil, nil); err != nil {
		t.Fatalf("ClaimNextTriggerFor: %v", err)
	}
	requeued, err := s.RequeueUnstartedClaim(ctx, "t1")
	if err != nil {
		t.Fatalf("RequeueUnstartedClaim: %v", err)
	}
	if !requeued {
		t.Fatal("RequeueUnstartedClaim returned false for an unstarted claim")
	}
	if _, err := s.ClaimSpecificTrigger(ctx, "t1", time.Hour); err != nil {
		t.Fatalf("ClaimSpecificTrigger: %v", err)
	}

	held, err := s.PrincipalHoldsTriggerClaim(ctx, "t1", runnerA, time.Now())
	if err != nil {
		t.Fatalf("PrincipalHoldsTriggerClaim: %v", err)
	}
	if held {
		t.Fatal("runner-a still holds the claim after its unstarted claim was requeued and taken")
	}
}

func TestTriggerClaim_ReleaseAtGenerationDropsClaimant(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	runnerA := store.ClaimIdentity{Principal: "runner-a", TokenPrefix: "swr_aaaaaaaa"}

	seedPending(t, s, "t1")
	if _, err := s.ClaimNextTriggerFor(ctx, runnerA, time.Hour, nil, nil); err != nil {
		t.Fatalf("ClaimNextTriggerFor: %v", err)
	}
	seq, err := s.TriggerClaimGeneration(ctx, "t1")
	if err != nil {
		t.Fatalf("TriggerClaimGeneration: %v", err)
	}
	released, err := s.ReleaseClaimAtGeneration(ctx, "t1", seq)
	if err != nil {
		t.Fatalf("ReleaseClaimAtGeneration: %v", err)
	}
	if !released {
		t.Fatal("ReleaseClaimAtGeneration returned false at the current generation")
	}
	if _, err := s.ClaimSpecificTrigger(ctx, "t1", time.Hour); err != nil {
		t.Fatalf("ClaimSpecificTrigger: %v", err)
	}

	held, err := s.PrincipalHoldsTriggerClaim(ctx, "t1", runnerA, time.Now())
	if err != nil {
		t.Fatalf("PrincipalHoldsTriggerClaim: %v", err)
	}
	if held {
		t.Fatal("runner-a still holds the claim after releasing it at its generation")
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
	if _, err := s.ClaimNextTriggerFor(ctx, runnerA, time.Hour, nil, nil); err != nil {
		t.Fatalf("ClaimNextTriggerFor: %v", err)
	}
	got, err := s.TriggerClaimant(ctx, "t1")
	if err != nil {
		t.Fatalf("TriggerClaimant: %v", err)
	}
	if got != runnerA {
		t.Fatalf("TriggerClaimant = %+v, want %+v", got, runnerA)
	}

	if err := s.FinishTrigger(ctx, "t1"); err != nil {
		t.Fatalf("FinishTrigger: %v", err)
	}
	got, err = s.TriggerClaimant(ctx, "t1")
	if err != nil {
		t.Fatalf("TriggerClaimant after finish: %v", err)
	}
	if got != runnerA {
		t.Errorf("TriggerClaimant after finish = %+v, want the holder %+v", got, runnerA)
	}
}

// A trigger nobody holds and a trigger that does not exist are different
// answers: the first is the zero identity, the second is ErrNotFound.
func TestTriggerClaim_ClaimantClearedByRequeue(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	runnerA := store.ClaimIdentity{Principal: "runner-a", TokenPrefix: "swr_aaaaaaaa"}

	seedPending(t, s, "t1")
	if _, err := s.ClaimNextTriggerFor(ctx, runnerA, time.Hour, nil, nil); err != nil {
		t.Fatalf("ClaimNextTriggerFor: %v", err)
	}
	requeued, err := s.RequeueUnstartedClaim(ctx, "t1")
	if err != nil || !requeued {
		t.Fatalf("RequeueUnstartedClaim = (%v, %v), want (true, nil)", requeued, err)
	}
	got, err := s.TriggerClaimant(ctx, "t1")
	if err != nil {
		t.Fatalf("TriggerClaimant: %v", err)
	}
	if got != (store.ClaimIdentity{}) {
		t.Errorf("TriggerClaimant = %+v, want the zero identity after a requeue", got)
	}

	if _, err := s.TriggerClaimant(ctx, "no-such-trigger"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("TriggerClaimant for a missing trigger = %v, want ErrNotFound", err)
	}
}
