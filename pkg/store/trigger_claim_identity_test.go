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

func TestTriggerClaim_RequeueUnstartedClaimDropsClaimant(t *testing.T) {
	s := newStoreT(t)
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
	s := newStoreT(t)
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
// or retry a close; a requeue clears it, and an unbound caller and a
// different consumer never match it.
func TestTriggerClaim_IsClaimantOutlivesLeaseAndCompletion(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()
	runnerA := store.ClaimIdentity{Principal: "runner-a", TokenPrefix: "swr_aaaaaaaa"}
	runnerB := store.ClaimIdentity{Principal: "runner-b", TokenPrefix: "swr_bbbbbbbb"}

	seedPending(t, s, "t1")
	if _, err := s.ClaimNextTriggerFor(ctx, runnerA, time.Hour, nil, nil); err != nil {
		t.Fatalf("ClaimNextTriggerFor: %v", err)
	}

	for _, tc := range []struct {
		name     string
		claimant store.ClaimIdentity
		want     bool
	}{
		{"the holder", runnerA, true},
		{"another consumer", runnerB, false},
		{"an unbound caller", store.ClaimIdentity{}, false},
	} {
		got, err := s.PrincipalIsTriggerClaimant(ctx, "t1", tc.claimant)
		if err != nil {
			t.Fatalf("PrincipalIsTriggerClaimant %s: %v", tc.name, err)
		}
		if got != tc.want {
			t.Errorf("PrincipalIsTriggerClaimant %s = %v, want %v", tc.name, got, tc.want)
		}
	}

	if err := s.FinishTrigger(ctx, "t1"); err != nil {
		t.Fatalf("FinishTrigger: %v", err)
	}
	got, err := s.PrincipalIsTriggerClaimant(ctx, "t1", runnerA)
	if err != nil {
		t.Fatalf("PrincipalIsTriggerClaimant after finish: %v", err)
	}
	if !got {
		t.Error("the holder stopped matching its own finished trigger")
	}
}

func TestTriggerClaim_IsClaimantClearedByRequeue(t *testing.T) {
	s := newStoreT(t)
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
	got, err := s.PrincipalIsTriggerClaimant(ctx, "t1", runnerA)
	if err != nil {
		t.Fatalf("PrincipalIsTriggerClaimant: %v", err)
	}
	if got {
		t.Error("runner-a still matches a trigger whose claim was requeued")
	}
}
