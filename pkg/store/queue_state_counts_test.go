package store_test

import (
	"context"
	"strconv"
	"sync/atomic"
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
	wantReserved := unpinnedNodeRateMicro * store.MinBillableSeconds
	if totals.ReservedMicro != wantReserved {
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
	if settled.SettledSeconds < 0 || settled.SettledSeconds > store.MinBillableSeconds {
		t.Errorf("settled seconds = %d, want the net of a reservation the node barely used",
			settled.SettledSeconds)
	}
	if want := settled.SettledSeconds * unpinnedNodeRateMicro; want != 25_000_000-settled.BalanceMicro {
		t.Errorf("settled seconds %d price to %d micro, but the balance fell by %d: the seconds and the bill disagree",
			settled.SettledSeconds, want, 25_000_000-settled.BalanceMicro)
	}
}

// A counter that falls reads to Prometheus as a reset, so the settled-seconds
// figure must not dip anywhere in a node's life: not while a reservation is
// outstanding, not when usage bills past it, and not when the finish refunds
// the part the node never used.
func TestCreditLedgerTotals_SettledSecondsNeverFall(t *testing.T) {
	assertSettledSecondsNeverFall(t, storetest.Open(t))
}

// This is the guard for the isolation level beginSnapshotReadTx names. Only
// Postgres can disagree with itself: under its default isolation each
// statement takes its own snapshot, so a claim landing between the charge sum
// and the reservation sum is counted by one and not the other, and the figure
// drops by a reservation. A sequential run cannot see that, so this one samples
// while claims and finishes land. Reverting the isolation reds it. The run
// needs a server; without one the SQLite test above is the only coverage.
func TestCreditLedgerTotals_SettledSecondsNeverFallUnderConcurrentClaimsOnPostgres(t *testing.T) {
	s := storetest.OpenPostgres(t)
	ctx := context.Background()

	if _, err := s.GrantCredits(ctx, store.CreditGrantPaid, 100_000_000_000, "invoice", "admin"); err != nil {
		t.Fatalf("grant: %v", err)
	}
	claimant := meteredClaimant(t, s, "pool")
	if err := s.CreateRun(ctx, store.Run{
		ID: "run-conc", Pipeline: "demo", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}

	churn, stop := context.WithCancel(ctx)
	defer stop()
	var landed atomic.Int64
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; churn.Err() == nil; i++ {
			node := "node-" + strconv.Itoa(i)
			if err := s.CreateNode(churn, store.Node{
				RunID: "run-conc", NodeID: node, Status: "pending",
			}); err != nil {
				return
			}
			if err := s.MarkNodeReady(churn, "run-conc", node); err != nil {
				return
			}
			if _, err := s.ClaimNextReadyNode(churn, claimant, "holder-"+strconv.Itoa(i), time.Minute, nil); err != nil {
				return
			}
			if _, err := s.FinalizeNodeCredits(churn, "run-conc", node, claimant.TokenPrefix, time.Now()); err != nil {
				return
			}
			landed.Add(1)
		}
	}()

	const samples = 200
	high := int64(-1)
	for range samples {
		totals, err := s.CreditLedgerTotals(ctx)
		if err != nil {
			t.Fatalf("CreditLedgerTotals: %v", err)
		}
		if totals.SettledSeconds < high {
			t.Fatalf("settled seconds fell from %d to %d while claims were landing",
				high, totals.SettledSeconds)
		}
		if totals.SettledSeconds > high {
			high = totals.SettledSeconds
		}
	}
	stop()
	<-done

	if landed.Load() == 0 {
		t.Error("no claim finished while the ledger was sampled, so no sample raced a write")
	}
}

func assertSettledSecondsNeverFall(t *testing.T, s *store.Store) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.GrantCredits(ctx, store.CreditGrantPaid, 200_000_000, "invoice", "admin"); err != nil {
		t.Fatalf("grant: %v", err)
	}
	claimant := meteredClaimant(t, s, "pool")

	high := int64(-1)
	sample := func(stage string) int64 {
		t.Helper()
		totals, err := s.CreditLedgerTotals(ctx)
		if err != nil {
			t.Fatalf("CreditLedgerTotals at %s: %v", stage, err)
		}
		if totals.SettledSeconds < high {
			t.Errorf("settled seconds fell to %d at %s, having reached %d",
				totals.SettledSeconds, stage, high)
		}
		if totals.SettledSeconds > high {
			high = totals.SettledSeconds
		}
		return totals.SettledSeconds
	}

	sample("empty ledger")
	readyNode(t, s, "run-mono", "node-a")
	if _, err := s.ClaimNextReadyNode(ctx, claimant, "holder-mono", time.Minute, nil); err != nil {
		t.Fatalf("claim: %v", err)
	}
	atClaim := sample("claim")
	if atClaim > 1 {
		t.Errorf("settled seconds = %d right after the claim, want the unconsumed reservation held back", atClaim)
	}

	// safety: rewinding the charge window is how a test advances the clock the
	// ledger measures against without sleeping through a reservation.
	rewindChargeWindow(t, s, "run-mono", "node-a", time.Now().Add(-10*time.Second))
	afterWait := sample("reservation partly consumed")
	if afterWait < store.MinBillableSeconds-10 {
		t.Errorf("settled seconds = %d once the window had 10s left, want near the consumed minute", afterWait)
	}

	if _, err := s.ChargeNodeCredits(ctx, "run-mono", "node-a", claimant.TokenPrefix, time.Now()); err != nil {
		t.Fatalf("heartbeat charge: %v", err)
	}
	sample("heartbeat")

	if _, err := s.FinalizeNodeCredits(ctx, "run-mono", "node-a", claimant.TokenPrefix, time.Now()); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	atFinish := sample("finish and refund")

	totals, err := s.CreditLedgerTotals(ctx)
	if err != nil {
		t.Fatalf("CreditLedgerTotals: %v", err)
	}
	if want := atFinish * unpinnedNodeRateMicro; want != 200_000_000-totals.BalanceMicro {
		t.Errorf("settled seconds %d price to %d micro but the balance fell by %d",
			atFinish, want, 200_000_000-totals.BalanceMicro)
	}
}

func TestNodeSettlement_ReadsTheNodesOwnClaimCredential(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()

	if _, err := s.GrantCredits(ctx, store.CreditGrantPaid, 20_000_000, "invoice", "admin"); err != nil {
		t.Fatalf("grant: %v", err)
	}
	claimant := meteredClaimant(t, s, "pool")
	readyNode(t, s, "run-settled", "metered")
	if _, err := s.ClaimNextReadyNode(ctx, claimant, "holder-metered", time.Minute, nil); err != nil {
		t.Fatalf("metered claim: %v", err)
	}
	if err := s.StartNode(ctx, "run-settled", "metered"); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := s.FinishNode(ctx, "run-settled", "metered", "success", "", nil); err != nil {
		t.Fatalf("finish: %v", err)
	}

	settlement, err := s.NodeSettlement(ctx, "run-settled", "metered")
	if err != nil {
		t.Fatalf("NodeSettlement: %v", err)
	}
	if settlement.Metering != store.MeteringPaid {
		t.Errorf("metering = %v, want paid for a node a metered credential claimed", settlement.Metering)
	}
	if settlement.ClaimTokenPrefix != claimant.TokenPrefix {
		t.Errorf("claim prefix = %q, want the credential the node recorded", settlement.ClaimTokenPrefix)
	}
	if !settlement.ChargeWindowOpen {
		t.Error("a claimed metered node reports no open charge window")
	}

	readyNode(t, s, "run-settled-local", "plain")
	if _, err := s.ClaimNextReadyNode(ctx, store.ClaimIdentity{Principal: "laptop", TokenPrefix: "swr_plain"},
		"holder-plain", time.Minute, nil); err != nil {
		t.Fatalf("unmetered claim: %v", err)
	}
	if err := s.StartNode(ctx, "run-settled-local", "plain"); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := s.FinishNode(ctx, "run-settled-local", "plain", "success", "", nil); err != nil {
		t.Fatalf("finish: %v", err)
	}
	local, err := s.NodeSettlement(ctx, "run-settled-local", "plain")
	if err != nil {
		t.Fatalf("NodeSettlement: %v", err)
	}
	if local.Metering != store.MeteringUnknown {
		t.Errorf("metering = %v, want unknown for a node whose claim credential is not on file", local.Metering)
	}
	if local.ChargeWindowOpen {
		t.Error("an unmetered claim opened a charge window")
	}
	if local.Seconds < 0 {
		t.Errorf("seconds = %v, want a non-negative wall time", local.Seconds)
	}
}
