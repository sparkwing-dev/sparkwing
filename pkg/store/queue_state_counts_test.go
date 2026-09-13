package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestCountNodesByQueueState_SeparatesEveryOutstandingState(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()

	if err := s.CreateRun(ctx, store.Run{
		ID: "run-q", Pipeline: "demo", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	for _, id := range []string{"waiting", "ready", "claimed", "running", "gate", "done"} {
		if err := s.CreateNode(ctx, store.Node{RunID: "run-q", NodeID: id, Status: "pending"}); err != nil {
			t.Fatalf("create node %s: %v", id, err)
		}
	}
	for _, id := range []string{"ready", "claimed"} {
		if err := s.MarkNodeReady(ctx, "run-q", id); err != nil {
			t.Fatalf("mark %s ready: %v", id, err)
		}
	}
	if _, err := s.ClaimNextReadyNode(ctx, store.ClaimIdentity{}, "holder-q", time.Minute, nil); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := s.StartNode(ctx, "run-q", "running"); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := s.FinishNode(ctx, "run-q", "done", "success", "", nil); err != nil {
		t.Fatalf("finish: %v", err)
	}

	counts, err := s.CountNodesByQueueState(ctx)
	if err != nil {
		t.Fatalf("CountNodesByQueueState: %v", err)
	}
	for _, state := range store.QueueStates() {
		if _, ok := counts[state]; !ok {
			t.Errorf("state %q absent from the answer", state)
		}
	}
	if counts[store.QueueStateWaiting] != 2 {
		t.Errorf("waiting = %d, want the nodes still pending with no ready_at", counts[store.QueueStateWaiting])
	}
	if counts[store.QueueStateClaimed] != 1 {
		t.Errorf("claimed = %d, want the one node a runner holds", counts[store.QueueStateClaimed])
	}
	if counts[store.QueueStateRunning] != 1 {
		t.Errorf("running = %d, want the one started node", counts[store.QueueStateRunning])
	}
	if counts[store.QueueStateReady]+counts[store.QueueStateClaimed] != 2 {
		t.Errorf("ready+claimed = %d, want the two nodes marked ready",
			counts[store.QueueStateReady]+counts[store.QueueStateClaimed])
	}
}

func TestCountNodesByQueueState_EmptyTableAnswersZeros(t *testing.T) {
	counts, err := storetest.Open(t).CountNodesByQueueState(context.Background())
	if err != nil {
		t.Fatalf("CountNodesByQueueState: %v", err)
	}
	for _, state := range store.QueueStates() {
		if counts[state] != 0 {
			t.Errorf("%s = %d on an empty store, want 0", state, counts[state])
		}
	}
}

func TestCreditLedgerTotals_SplitsGrantsAndCharges(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()

	if _, err := s.GrantCredits(ctx, store.CreditGrantFree, 5_000_000, "trial", "admin"); err != nil {
		t.Fatalf("free grant: %v", err)
	}
	if _, err := s.GrantCredits(ctx, store.CreditGrantPaid, 20_000_000, "invoice-1", "admin"); err != nil {
		t.Fatalf("paid grant: %v", err)
	}

	claimant := meteredClaimant(t, s, "pool")
	readyNode(t, s, "run-ledger", "node-a")
	if _, err := s.ClaimNextReadyNode(ctx, claimant, "holder-ledger", time.Minute, nil); err != nil {
		t.Fatalf("claim: %v", err)
	}

	totals, err := s.CreditLedgerTotals(ctx)
	if err != nil {
		t.Fatalf("CreditLedgerTotals: %v", err)
	}
	if totals.GrantedFreeMicro != 5_000_000 || totals.GrantedPaidMicro != 20_000_000 {
		t.Errorf("grants = free %d / paid %d, want 5000000 / 20000000",
			totals.GrantedFreeMicro, totals.GrantedPaidMicro)
	}
	wantReserved := store.DefaultCreditRateMicro * store.CreditClaimFloorSeconds
	if totals.ReservedMicro != int64(wantReserved) {
		t.Errorf("reserved = %d, want the claim floor %d", totals.ReservedMicro, wantReserved)
	}
	if totals.ChargedMicro != 0 {
		t.Errorf("charged = %d, want nothing billed before the node runs", totals.ChargedMicro)
	}
	if want := int64(25_000_000) - totals.ReservedMicro; totals.BalanceMicro != want {
		t.Errorf("balance = %d, want %d", totals.BalanceMicro, want)
	}

	if _, err := s.FinalizeNodeCredits(ctx, "run-ledger", "node-a", claimant.TokenPrefix, time.Now()); err != nil {
		t.Fatalf("FinalizeNodeCredits: %v", err)
	}
	settled, err := s.CreditLedgerTotals(ctx)
	if err != nil {
		t.Fatalf("CreditLedgerTotals after settle: %v", err)
	}
	if settled.RefundedMicro <= 0 {
		t.Errorf("refunded = %d, want the unused reservation tail returned", settled.RefundedMicro)
	}
	if settled.BalanceMicro <= totals.BalanceMicro {
		t.Errorf("balance = %d, want the refund to raise it above %d",
			settled.BalanceMicro, totals.BalanceMicro)
	}
}
