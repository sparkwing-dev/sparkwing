package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestClaimNamedNodeAwardsAnUnqueuedNode(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	seedClaimedNode(t, s, "run-named", "build")

	n, err := s.ClaimNamedNode(ctx, store.ClaimIdentity{}, "run-named", "build", "k8s-job:sw-1", time.Minute, store.NamedClaimOptions{})
	if err != nil {
		t.Fatalf("ClaimNamedNode: %v", err)
	}
	if n.ClaimedBy != "k8s-job:sw-1" {
		t.Fatalf("claimed_by = %q, want the named holder", n.ClaimedBy)
	}
	if n.ClaimGeneration < 1 {
		t.Fatalf("claim generation = %d, want a fence the pod can send", n.ClaimGeneration)
	}
	stored, err := s.GetNode(ctx, "run-named", "build")
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if !stored.Claimed {
		t.Fatal("the awarded node is not claimed in the store")
	}
	live, err := s.NodeClaimFenceIsLive(ctx, "run-named", "build", store.NodeClaimFence{
		HolderID: n.ClaimedBy, MembershipID: n.ClaimMembershipID,
		ReservationID: n.ReservationID, ClaimGeneration: n.ClaimGeneration,
	}, time.Now())
	if err != nil {
		t.Fatalf("NodeClaimFenceIsLive: %v", err)
	}
	if !live {
		t.Fatal("the fence the award returned does not admit the holder's writes")
	}
}

func TestClaimNamedNodeRefusesANodeAnotherHolderHas(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	readyNode(t, s, "run-taken", "build")
	if _, err := s.ClaimNextReadyNode(ctx, store.ClaimIdentity{}, "agent:box-a", time.Minute, nil); err != nil {
		t.Fatalf("agent claim: %v", err)
	}

	_, err := s.ClaimNamedNode(ctx, store.ClaimIdentity{}, "run-taken", "build", "k8s-job:sw-1", time.Minute, store.NamedClaimOptions{})
	if !errors.Is(err, store.ErrLockHeld) {
		t.Fatalf("ClaimNamedNode on a held node = %v, want ErrLockHeld", err)
	}
	n, err := s.GetNode(ctx, "run-taken", "build")
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if n.ClaimedBy != "agent:box-a" {
		t.Fatalf("claimed_by = %q, want the agent's claim untouched", n.ClaimedBy)
	}
}

func TestClaimNamedNodeRefusesAFinishedNode(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	seedClaimedNode(t, s, "run-done", "build")
	if err := s.FinishNode(ctx, "run-done", "build", "success", "", nil); err != nil {
		t.Fatalf("FinishNode: %v", err)
	}

	_, err := s.ClaimNamedNode(ctx, store.ClaimIdentity{}, "run-done", "build", "k8s-job:sw-1", time.Minute, store.NamedClaimOptions{})
	if !errors.Is(err, store.ErrLockHeld) {
		t.Fatalf("ClaimNamedNode on a finished node = %v, want ErrLockHeld", err)
	}
}

func TestClaimNamedNodeReportsANodeThatDoesNotExist(t *testing.T) {
	s := storetest.Open(t)
	_, err := s.ClaimNamedNode(context.Background(), store.ClaimIdentity{},
		"run-missing", "build", "k8s-job:sw-1", time.Minute, store.NamedClaimOptions{})
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("ClaimNamedNode on an absent node = %v, want ErrNotFound", err)
	}
}

func TestClaimNamedNodeReservesCreditsForAMeteredToken(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	claimant := meteredClaimant(t, s, "agent:cloud")
	seedClaimedNode(t, s, "run-billed", "build")
	if _, err := s.GrantCredits(ctx, store.CreditGrantPaid, 100*store.MicroCreditsPerCent, "pay_1", "admin"); err != nil {
		t.Fatalf("grant: %v", err)
	}

	if _, err := s.ClaimNamedNode(ctx, claimant, "run-billed", "build", "k8s-job:sw-1", time.Minute, store.NamedClaimOptions{}); err != nil {
		t.Fatalf("ClaimNamedNode: %v", err)
	}
	charges, err := s.ListCreditCharges(ctx, 10)
	if err != nil {
		t.Fatalf("ListCreditCharges: %v", err)
	}
	if len(charges) != 1 || charges[0].Kind != store.CreditChargeReservation {
		t.Fatalf("charges after a named claim = %+v, want one reservation", charges)
	}
	if want := int64(store.CreditClaimFloorSeconds) * unpinnedNodeRateMicro; charges[0].AmountMicro != want {
		t.Fatalf("reservation = %d, want %d", charges[0].AmountMicro, want)
	}

	rewindChargeWindow(t, s, "run-billed", "build", time.Now().Add(-90*time.Second))
	res, err := s.FinalizeNodeCredits(ctx, "run-billed", "build", claimant.TokenPrefix, time.Now())
	if err != nil {
		t.Fatalf("FinalizeNodeCredits: %v", err)
	}
	if res.Charge == nil || res.Charge.Seconds < 1 {
		t.Fatalf("final charge = %+v, want the minute this node ran", res.Charge)
	}
}

func TestClaimNamedNodeIsRefusedWhenTheBalanceCannotCoverTheReservation(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	claimant := meteredClaimant(t, s, "agent:cloud")
	seedClaimedNode(t, s, "run-broke-named", "build")

	_, err := s.ClaimNamedNode(ctx, claimant, "run-broke-named", "build", "k8s-job:sw-1", time.Minute, store.NamedClaimOptions{})
	if !errors.Is(err, store.ErrInsufficientCredits) {
		t.Fatalf("named claim on an empty ledger = %v, want ErrInsufficientCredits", err)
	}
	n, err := s.GetNode(ctx, "run-broke-named", "build")
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if n.Claimed {
		t.Fatal("a refused claim left the node claimed")
	}
}

func TestClaimNamedNodeRefusesANodeOfAFinishedRun(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	seedClaimedNode(t, s, "run-over", "build")
	if err := s.FinishRun(ctx, "run-over", "success", ""); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}

	_, err := s.ClaimNamedNode(ctx, store.ClaimIdentity{}, "run-over", "build", "k8s-job:sw-1", time.Minute, store.NamedClaimOptions{})
	if !errors.Is(err, store.ErrLockHeld) {
		t.Fatalf("ClaimNamedNode on a node of a finished run = %v, want ErrLockHeld", err)
	}
}

// The named claim and the queue claim share one award, and only the named one
// may take a node the queue has not opened.
func TestClaimNextReadyNodeStillRefusesANodeTheQueueHasNotOpened(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	seedClaimedNode(t, s, "run-unready", "build")

	_, err := s.ClaimNextReadyNode(ctx, store.ClaimIdentity{}, "agent:box-a", time.Minute, nil)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("ClaimNextReadyNode saw an unready node = %v, want ErrNotFound", err)
	}
	n, err := s.GetNode(ctx, "run-unready", "build")
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if n.Claimed {
		t.Fatalf("the queue claim took an unready node for %q", n.ClaimedBy)
	}
}
