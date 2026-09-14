package store_test

import (
	"context"
	"errors"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

// safety: a node whose plan pins no cpu resolves to one core, which the
// smallest class of the default rate table covers, so that is the price these
// fixtures are billed at.
var unpinnedNodeRateMicro = store.DefaultCreditRateTable(store.DefaultCreditRateMicro).RateFor(1)

func seedClaimedNode(t *testing.T, s *store.Store, runID, nodeID string) {
	t.Helper()
	ctx := context.Background()
	if err := s.CreateRun(ctx, store.Run{
		ID: runID, Pipeline: "demo", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := s.CreateNode(ctx, store.Node{RunID: runID, NodeID: nodeID, Status: "pending"}); err != nil {
		t.Fatalf("create node: %v", err)
	}
}

// safety: the ledger prices work against the claim identity, so a test that
// needs charging must claim under a token the operator marked metered.
func meteredClaimant(t *testing.T, s *store.Store, principal string) store.ClaimIdentity {
	t.Helper()
	_, tok, err := s.CreateTokenWith(context.Background(), principal, store.TokenKindRunner,
		[]string{"nodes.claim"}, 0, time.Now(), store.TokenOptions{Metered: true})
	if err != nil {
		t.Fatalf("mint a metered token: %v", err)
	}
	return store.ClaimIdentity{Principal: principal, TokenPrefix: tok.Prefix}
}

func readyNode(t *testing.T, s *store.Store, runID, nodeID string) {
	t.Helper()
	seedClaimedNode(t, s, runID, nodeID)
	if err := s.MarkNodeReady(context.Background(), runID, nodeID); err != nil {
		t.Fatalf("mark ready: %v", err)
	}
}

// safety: values are inlined because the two dialects spell bound parameters
// differently and every one here is a test-owned integer or identifier.
func rewindChargeWindow(t *testing.T, s *store.Store, runID, nodeID string, at time.Time) {
	t.Helper()
	res, err := s.DB().Exec(fmt.Sprintf(
		`UPDATE nodes SET credit_charged_through = %d WHERE run_id = '%s' AND node_id = '%s'`,
		at.UnixNano(), runID, nodeID))
	if err != nil {
		t.Fatalf("rewind the charge window: %v", err)
	}
	changed, err := res.RowsAffected()
	if err != nil || changed != 1 {
		t.Fatalf("rewind changed %d rows (%v)", changed, err)
	}
}

func expireNodeLease(t *testing.T, s *store.Store, runID, nodeID string, at time.Time) {
	t.Helper()
	if _, err := s.DB().Exec(fmt.Sprintf(
		`UPDATE nodes SET lease_expires_at = %d WHERE run_id = '%s' AND node_id = '%s'`,
		at.UnixNano(), runID, nodeID)); err != nil {
		t.Fatalf("expire the lease: %v", err)
	}
}

func chargeWindowAnchor(t *testing.T, s *store.Store, runID, nodeID string) int64 {
	t.Helper()
	var anchor int64
	if err := s.DB().QueryRow(fmt.Sprintf(
		`SELECT credit_charged_through FROM nodes WHERE run_id = '%s' AND node_id = '%s'`,
		runID, nodeID)).Scan(&anchor); err != nil {
		t.Fatalf("read the charge window: %v", err)
	}
	return anchor
}

func TestCreditsBalanceIsGrantsMinusCharges(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()

	balance, err := s.CreditBalanceMicro(ctx)
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	if balance != 0 {
		t.Fatalf("fresh balance = %d, want 0", balance)
	}

	if _, err := s.GrantCredits(ctx, store.CreditGrantPaid, 1000*store.MicroCreditsPerCredit, "pay_123", "admin"); err != nil {
		t.Fatalf("paid grant: %v", err)
	}
	if _, err := s.GrantCredits(ctx, store.CreditGrantFree, 50*store.MicroCreditsPerCredit, "", "admin"); err != nil {
		t.Fatalf("free grant: %v", err)
	}
	balance, err = s.CreditBalanceMicro(ctx)
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	if want := int64(1050 * store.MicroCreditsPerCredit); balance != want {
		t.Fatalf("balance = %d, want %d", balance, want)
	}

	grants, err := s.ListCreditGrants(ctx, 10)
	if err != nil {
		t.Fatalf("list grants: %v", err)
	}
	if len(grants) != 2 {
		t.Fatalf("grants = %d, want 2", len(grants))
	}
	if grants[0].Kind != store.CreditGrantFree {
		t.Fatalf("newest grant kind = %q, want free", grants[0].Kind)
	}
}

func TestCreditsGrantRejectsUnknownKindAndZero(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	if _, err := s.GrantCredits(ctx, "gift", 10, "", "admin"); err == nil {
		t.Fatal("expected an unknown grant kind to be refused")
	}
	if _, err := s.GrantCredits(ctx, store.CreditGrantFree, 0, "", "admin"); err == nil {
		t.Fatal("expected a zero grant to be refused")
	}
}

func TestMeteredClaimReservesItsFirstMinute(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	claimant := meteredClaimant(t, s, "agent:cloud")
	readyNode(t, s, "run-reserve", "build")
	if _, err := s.GrantCredits(ctx, store.CreditGrantPaid, 100*store.MicroCreditsPerCredit, "pay_1", "admin"); err != nil {
		t.Fatalf("grant: %v", err)
	}

	n, err := s.ClaimNextReadyNode(ctx, claimant, "pod-1", time.Minute, nil)
	if err != nil || n == nil {
		t.Fatalf("claim: %v", err)
	}
	charges, err := s.ListCreditCharges(ctx, 10)
	if err != nil {
		t.Fatalf("list charges: %v", err)
	}
	if len(charges) != 1 || charges[0].Kind != store.CreditChargeReservation {
		t.Fatalf("charges after a claim = %+v, want one reservation", charges)
	}
	if want := int64(store.CreditClaimFloorSeconds) * unpinnedNodeRateMicro; charges[0].AmountMicro != want {
		t.Fatalf("reservation = %d, want %d", charges[0].AmountMicro, want)
	}
	if anchor := chargeWindowAnchor(t, s, "run-reserve", "build"); anchor <= time.Now().UnixNano() {
		t.Fatal("the reservation did not anchor the charge window past now")
	}
}

func TestMeteredClaimIsRefusedWhenTheBalanceCannotCoverTheReservation(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	claimant := meteredClaimant(t, s, "agent:cloud")
	readyNode(t, s, "run-broke", "build")

	_, err := s.ClaimNextReadyNode(ctx, claimant, "pod-1", time.Minute, nil)
	if !errors.Is(err, store.ErrInsufficientCredits) {
		t.Fatalf("claim on an empty ledger = %v, want ErrInsufficientCredits", err)
	}
	var shortfall *store.InsufficientCreditsError
	if !errors.As(err, &shortfall) {
		t.Fatalf("claim error %v does not name the shortfall", err)
	}
	if shortfall.RequiredMicro != int64(store.CreditClaimFloorSeconds)*unpinnedNodeRateMicro {
		t.Fatalf("required = %d", shortfall.RequiredMicro)
	}

	node, err := s.GetNode(ctx, "run-broke", "build")
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	if node.Claimed {
		t.Fatal("a refused claim left the node claimed")
	}
	charges, err := s.ListCreditCharges(ctx, 10)
	if err != nil {
		t.Fatalf("list charges: %v", err)
	}
	if len(charges) != 0 {
		t.Fatalf("a refused claim wrote %+v", charges)
	}
}

// Two metered runners looking at a balance that covers exactly one
// reservation must not both claim: the reservation is taken inside the claim
// transaction, so the second read sees the first one's spend.
func TestClaimReservationLetsOnlyOneRunnerClaimTheLastMinute(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	claimantA := meteredClaimant(t, s, "agent:cloud-a")
	claimantB := meteredClaimant(t, s, "agent:cloud-b")
	readyNode(t, s, "run-a", "build")
	readyNode(t, s, "run-b", "build")

	floor := unpinnedNodeRateMicro * store.CreditClaimFloorSeconds
	if _, err := s.GrantCredits(ctx, store.CreditGrantPaid, floor, "pay_exact", "admin"); err != nil {
		t.Fatalf("grant: %v", err)
	}

	first, err := s.ClaimNextReadyNode(ctx, claimantA, "pod-a", time.Minute, nil)
	if err != nil || first == nil {
		t.Fatalf("the first claim must succeed: %v", err)
	}
	_, err = s.ClaimNextReadyNode(ctx, claimantB, "pod-b", time.Minute, nil)
	if !errors.Is(err, store.ErrInsufficientCredits) {
		t.Fatalf("the second claim = %v, want ErrInsufficientCredits", err)
	}
	balance, err := s.CreditBalanceMicro(ctx)
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	if balance != 0 {
		t.Fatalf("balance = %d, want the reservation to have taken it all", balance)
	}
}

func TestChargeNodeCreditsBillsElapsedSecondsIdempotently(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	claimant := meteredClaimant(t, s, "agent:cloud")
	readyNode(t, s, "run-charge", "build")
	if _, err := s.GrantCredits(ctx, store.CreditGrantPaid, 1000*store.MicroCreditsPerCredit, "pay_1", "admin"); err != nil {
		t.Fatalf("grant: %v", err)
	}
	if _, err := s.ClaimNextReadyNode(ctx, claimant, "pod-1", time.Minute, nil); err != nil {
		t.Fatalf("claim: %v", err)
	}

	start := time.Now()
	rewindChargeWindow(t, s, "run-charge", "build", start)

	res, err := s.ChargeNodeCredits(ctx, "run-charge", "build", claimant.TokenPrefix, start.Add(10*time.Second))
	if err != nil {
		t.Fatalf("charge: %v", err)
	}
	if res.Charge == nil || res.Charge.Seconds != 10 || res.Charge.Kind != store.CreditChargeUsage {
		t.Fatalf("first charge = %+v, want 10 seconds of usage", res.Charge)
	}
	if want := int64(10) * unpinnedNodeRateMicro; res.Charge.AmountMicro != want {
		t.Fatalf("charge amount = %d, want %d", res.Charge.AmountMicro, want)
	}
	if res.Cancel {
		t.Fatal("a funded ledger must not ask for a cancellation")
	}

	res, err = s.ChargeNodeCredits(ctx, "run-charge", "build", claimant.TokenPrefix, start.Add(10*time.Second))
	if err != nil {
		t.Fatalf("repeat charge: %v", err)
	}
	if res.Charge != nil {
		t.Fatalf("repeat charge wrote %+v, want nothing", res.Charge)
	}

	res, err = s.ChargeNodeCredits(ctx, "run-charge", "build", claimant.TokenPrefix, start.Add(25*time.Second))
	if err != nil {
		t.Fatalf("third charge: %v", err)
	}
	if res.Charge == nil || res.Charge.Seconds != 15 {
		t.Fatalf("third charge = %+v, want 15 seconds", res.Charge)
	}

	state, err := s.CreditState(ctx, 24*time.Hour)
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if state.BalanceMicro != state.GrantedMicro-state.ChargedMicro {
		t.Fatalf("balance %d does not equal granted %d minus charged %d",
			state.BalanceMicro, state.GrantedMicro, state.ChargedMicro)
	}
	if state.RateMicroPerSecond != store.DefaultCreditRateMicro {
		t.Fatalf("rate = %d, want the default %d", state.RateMicroPerSecond, store.DefaultCreditRateMicro)
	}
	if state.MaxChargeSeconds != store.DefaultCreditMaxChargeSeconds {
		t.Fatalf("charge cap = %d, want the default %d", state.MaxChargeSeconds, store.DefaultCreditMaxChargeSeconds)
	}
}

// A node still inside the minute its claim reserved is charged nothing more,
// because that minute is already paid for.
func TestChargeNodeCreditsDoesNotDoubleChargeTheReservedMinute(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	claimant := meteredClaimant(t, s, "agent:cloud")
	readyNode(t, s, "run-reserved", "build")
	if _, err := s.GrantCredits(ctx, store.CreditGrantPaid, 1000*store.MicroCreditsPerCredit, "pay_1", "admin"); err != nil {
		t.Fatalf("grant: %v", err)
	}
	if _, err := s.ClaimNextReadyNode(ctx, claimant, "pod-1", time.Minute, nil); err != nil {
		t.Fatalf("claim: %v", err)
	}
	for _, after := range []time.Duration{time.Second, 5 * time.Second, 30 * time.Second} {
		res, err := s.ChargeNodeCredits(ctx, "run-reserved", "build", claimant.TokenPrefix, time.Now().Add(after))
		if err != nil {
			t.Fatalf("charge at %s: %v", after, err)
		}
		if res.Charge != nil {
			t.Fatalf("charge at %s wrote %+v inside the reserved minute", after, res.Charge)
		}
	}
	charges, err := s.ListCreditCharges(ctx, 10)
	if err != nil {
		t.Fatalf("list charges: %v", err)
	}
	if len(charges) != 1 {
		t.Fatalf("charges = %d, want the reservation alone", len(charges))
	}
}

// A node reaped from its first attempt and claimed again must pay for the two
// attempts, never for the idle gap between them.
func TestReclaimedNodeIsNotBilledForItsIdleGap(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	claimant := meteredClaimant(t, s, "agent:cloud")
	readyNode(t, s, "run-gap", "build")
	if _, err := s.GrantCredits(ctx, store.CreditGrantPaid, 1000*store.MicroCreditsPerCredit, "pay_1", "admin"); err != nil {
		t.Fatalf("grant: %v", err)
	}
	if _, err := s.ClaimNextReadyNode(ctx, claimant, "pod-1", time.Minute, nil); err != nil {
		t.Fatalf("first claim: %v", err)
	}

	// safety: the first attempt's lease expired an hour ago, so the reaper
	// requeues it and the second attempt must not inherit its charge window.
	expireNodeLease(t, s, "run-gap", "build", time.Now().Add(-time.Hour))
	if _, err := s.ReapExpiredNodeClaims(ctx); err != nil {
		t.Fatalf("reap expired claims: %v", err)
	}
	if anchor := chargeWindowAnchor(t, s, "run-gap", "build"); anchor != 0 {
		t.Fatalf("the requeue left a charge window of %d, want it released", anchor)
	}

	if err := s.MarkNodeReady(ctx, "run-gap", "build"); err != nil {
		t.Fatalf("mark ready again: %v", err)
	}
	if _, err := s.ClaimNextReadyNode(ctx, claimant, "pod-2", time.Minute, nil); err != nil {
		t.Fatalf("second claim: %v", err)
	}

	// safety: five seconds of second-attempt work, an hour after the first.
	rewindChargeWindow(t, s, "run-gap", "build", time.Now().Add(-5*time.Second))
	res, err := s.FinalizeNodeCredits(ctx, "run-gap", "build", claimant.TokenPrefix, time.Now())
	if err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if res.Charge == nil || res.Charge.Seconds > 6 {
		t.Fatalf("second attempt billed %+v, want about five seconds", res.Charge)
	}

	charges, err := s.ListCreditCharges(ctx, 20)
	if err != nil {
		t.Fatalf("list charges: %v", err)
	}
	var total int64
	for _, c := range charges {
		total += c.AmountMicro
		if c.Seconds > int64(store.CreditClaimFloorSeconds) {
			t.Fatalf("a charge billed %d seconds, which is the idle gap: %+v", c.Seconds, c)
		}
	}
	// safety: two reservations of a minute each plus five seconds of work,
	// never the 3600-second gap.
	if want := int64(2*store.CreditClaimFloorSeconds+6) * store.DefaultCreditRateMicro; total > want {
		t.Fatalf("total charged %d exceeds %d, so the idle gap was billed", total, want)
	}
}

// A node that finishes between heartbeats pays for the seconds it ran, not
// for the minute its claim reserved.
func TestFinalizeNodeCreditsRefundsTheUnusedReservation(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	claimant := meteredClaimant(t, s, "agent:cloud")
	readyNode(t, s, "run-short", "build")
	if _, err := s.GrantCredits(ctx, store.CreditGrantPaid, 1000*store.MicroCreditsPerCredit, "pay_1", "admin"); err != nil {
		t.Fatalf("grant: %v", err)
	}
	before, err := s.CreditBalanceMicro(ctx)
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	claimedAt := time.Now()
	if _, err := s.ClaimNextReadyNode(ctx, claimant, "pod-1", time.Minute, nil); err != nil {
		t.Fatalf("claim: %v", err)
	}

	// safety: the node finishes five seconds in, before any heartbeat charged.
	finishedAt := claimedAt.Add(5 * time.Second)
	res, err := s.FinalizeNodeCredits(ctx, "run-short", "build", claimant.TokenPrefix, finishedAt)
	if err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if res.Charge == nil || res.Charge.Kind != store.CreditChargeRefund {
		t.Fatalf("finalize wrote %+v, want a refund", res.Charge)
	}
	after, err := s.CreditBalanceMicro(ctx)
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	spent := before - after
	want := int64(5) * unpinnedNodeRateMicro
	if diff := spent - want; diff > unpinnedNodeRateMicro || diff < -unpinnedNodeRateMicro {
		t.Fatalf("spent %d for five seconds of work, want about %d", spent, want)
	}
	if anchor := chargeWindowAnchor(t, s, "run-short", "build"); anchor != 0 {
		t.Fatalf("finalize left a charge window of %d, want it released", anchor)
	}

	// safety: settling twice must not move the ledger again.
	res, err = s.FinalizeNodeCredits(ctx, "run-short", "build", claimant.TokenPrefix, finishedAt)
	if err != nil {
		t.Fatalf("second finalize: %v", err)
	}
	if res.Charge != nil {
		t.Fatalf("a second finalize wrote %+v", res.Charge)
	}
}

// A node that ran past its reservation pays the tail its heartbeats missed.
func TestFinalizeNodeCreditsBillsTheTailPastTheLastHeartbeat(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	claimant := meteredClaimant(t, s, "agent:cloud")
	readyNode(t, s, "run-tail", "build")
	if _, err := s.GrantCredits(ctx, store.CreditGrantPaid, 1000*store.MicroCreditsPerCredit, "pay_1", "admin"); err != nil {
		t.Fatalf("grant: %v", err)
	}
	if _, err := s.ClaimNextReadyNode(ctx, claimant, "pod-1", time.Minute, nil); err != nil {
		t.Fatalf("claim: %v", err)
	}
	rewindChargeWindow(t, s, "run-tail", "build", time.Now().Add(-8*time.Second))
	res, err := s.FinalizeNodeCredits(ctx, "run-tail", "build", claimant.TokenPrefix, time.Now())
	if err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if res.Charge == nil || res.Charge.Kind != store.CreditChargeUsage {
		t.Fatalf("finalize wrote %+v, want a usage charge", res.Charge)
	}
	if res.Charge.Seconds < 7 || res.Charge.Seconds > 9 {
		t.Fatalf("tail billed %d seconds, want about 8", res.Charge.Seconds)
	}
}

// A controller outage or a stalled heartbeat loop must not bill the gap it
// left behind.
func TestChargeCapForgivesAStalledGap(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	claimant := meteredClaimant(t, s, "agent:cloud")
	readyNode(t, s, "run-stall", "build")
	if _, err := s.GrantCredits(ctx, store.CreditGrantPaid, 1000*store.MicroCreditsPerCredit, "pay_1", "admin"); err != nil {
		t.Fatalf("grant: %v", err)
	}
	if _, err := s.ClaimNextReadyNode(ctx, claimant, "pod-1", time.Minute, nil); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := s.SetCreditMaxChargeSeconds(ctx, 10); err != nil {
		t.Fatalf("set the charge cap: %v", err)
	}
	rewindChargeWindow(t, s, "run-stall", "build", time.Now().Add(-time.Hour))

	res, err := s.ChargeNodeCredits(ctx, "run-stall", "build", claimant.TokenPrefix, time.Now())
	if err != nil {
		t.Fatalf("charge after a stall: %v", err)
	}
	if res.Charge == nil || res.Charge.Seconds != 10 {
		t.Fatalf("charge after a stall = %+v, want the cap of 10 seconds", res.Charge)
	}
	if res.ForgivenSeconds < 3500 {
		t.Fatalf("forgiven = %d, want the rest of the hour", res.ForgivenSeconds)
	}

	// safety: the anchor caught up, so the next charge bills from now rather
	// than re-billing the forgiven gap.
	res, err = s.ChargeNodeCredits(ctx, "run-stall", "build", claimant.TokenPrefix, time.Now().Add(2*time.Second))
	if err != nil {
		t.Fatalf("charge after the cap: %v", err)
	}
	if res.Charge == nil || res.Charge.Seconds != 2 {
		t.Fatalf("charge after the cap = %+v, want 2 seconds", res.Charge)
	}
}

func TestChargeNodeCreditsCancelsAfterGrace(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	claimant := meteredClaimant(t, s, "agent:cloud")
	readyNode(t, s, "run-empty", "build")
	floor := unpinnedNodeRateMicro * store.CreditClaimFloorSeconds
	if _, err := s.GrantCredits(ctx, store.CreditGrantFree, floor, "", "admin"); err != nil {
		t.Fatalf("grant: %v", err)
	}
	if err := s.SetCreditGraceSeconds(ctx, 30); err != nil {
		t.Fatalf("set grace: %v", err)
	}
	if _, err := s.ClaimNextReadyNode(ctx, claimant, "pod-1", time.Minute, nil); err != nil {
		t.Fatalf("claim: %v", err)
	}

	start := time.Now()
	rewindChargeWindow(t, s, "run-empty", "build", start)
	res, err := s.ChargeNodeCredits(ctx, "run-empty", "build", claimant.TokenPrefix, start.Add(5*time.Second))
	if err != nil {
		t.Fatalf("charge: %v", err)
	}
	if res.BalanceMicro > 0 {
		t.Fatalf("balance = %d, want it spent", res.BalanceMicro)
	}
	if res.Cancel {
		t.Fatal("the grace period had not elapsed yet")
	}

	rewindChargeWindow(t, s, "run-empty", "build", start.Add(5*time.Second))
	res, err = s.ChargeNodeCredits(ctx, "run-empty", "build", claimant.TokenPrefix, start.Add(40*time.Second))
	if err != nil {
		t.Fatalf("charge after grace: %v", err)
	}
	if !res.Cancel {
		t.Fatalf("expected a cancellation after the grace period, got %+v", res)
	}

	if _, err := s.GrantCredits(ctx, store.CreditGrantPaid, 100*store.MicroCreditsPerCredit, "pay_2", "admin"); err != nil {
		t.Fatalf("top-up: %v", err)
	}
	rewindChargeWindow(t, s, "run-empty", "build", start.Add(40*time.Second))
	res, err = s.ChargeNodeCredits(ctx, "run-empty", "build", claimant.TokenPrefix, start.Add(45*time.Second))
	if err != nil {
		t.Fatalf("charge after top-up: %v", err)
	}
	if res.Cancel {
		t.Fatal("a top-up must clear the exhaustion stamp")
	}
}

func TestCancelNodeForExhaustedCreditsFailsAndReleasesTheNode(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	claimant := meteredClaimant(t, s, "agent:cloud")
	readyNode(t, s, "run-cancel", "build")
	if _, err := s.GrantCredits(ctx, store.CreditGrantPaid, 100*store.MicroCreditsPerCredit, "pay_1", "admin"); err != nil {
		t.Fatalf("grant: %v", err)
	}
	if _, err := s.ClaimNextReadyNode(ctx, claimant, "pod-1", time.Minute, nil); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := s.CancelNodeForExhaustedCredits(ctx, "run-cancel", "build", claimant.TokenPrefix, time.Now()); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	node, err := s.GetNode(ctx, "run-cancel", "build")
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	if node.Status != "done" || node.Outcome != "failed" {
		t.Fatalf("node = %s/%s, want done/failed", node.Status, node.Outcome)
	}
	if node.FailureReason != store.FailureCreditsExhausted {
		t.Fatalf("failure reason = %q, want %q", node.FailureReason, store.FailureCreditsExhausted)
	}
	if node.Claimed {
		t.Fatal("the cancelled node is still claimed")
	}
	if anchor := chargeWindowAnchor(t, s, "run-cancel", "build"); anchor != 0 {
		t.Fatalf("cancel left a charge window of %d", anchor)
	}
}

func TestTokenMeteredMarkerIsOperatorSet(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()

	_, plain, err := s.CreateToken("agent:local", store.TokenKindRunner, []string{"nodes.claim"}, 0, time.Now())
	if err != nil {
		t.Fatalf("create plain token: %v", err)
	}
	if plain.Metered {
		t.Fatal("a plain mint must not be metered")
	}
	metered, err := s.TokenMetered(ctx, plain.Prefix)
	if err != nil {
		t.Fatalf("token metered: %v", err)
	}
	if metered {
		t.Fatal("TokenMetered reported a plain token as metered")
	}

	_, cloud, err := s.CreateTokenWith(ctx, "agent:cloud", store.TokenKindRunner, []string{"nodes.claim"}, 0, time.Now(),
		store.TokenOptions{Metered: true})
	if err != nil {
		t.Fatalf("create metered token: %v", err)
	}
	if !cloud.Metered {
		t.Fatal("a metered mint must carry the marker")
	}
	metered, err = s.TokenMetered(ctx, cloud.Prefix)
	if err != nil {
		t.Fatalf("token metered: %v", err)
	}
	if !metered {
		t.Fatal("TokenMetered lost the marker")
	}

	if err := s.SetTokenMetered(ctx, plain.Prefix, true); err != nil {
		t.Fatalf("set metered: %v", err)
	}
	metered, err = s.TokenMetered(ctx, plain.Prefix)
	if err != nil {
		t.Fatalf("token metered: %v", err)
	}
	if !metered {
		t.Fatal("set-metered did not take")
	}
	if err := s.SetTokenMetered(ctx, plain.Prefix, false); err != nil {
		t.Fatalf("unset metered: %v", err)
	}
	metered, err = s.TokenMetered(ctx, plain.Prefix)
	if err != nil {
		t.Fatalf("token metered: %v", err)
	}
	if metered {
		t.Fatal("unset-metered did not take")
	}

	if err := s.SetTokenMetered(ctx, "swu_nosuchtok", true); err == nil {
		t.Fatal("expected an unknown prefix to be refused")
	}
	metered, err = s.TokenMetered(ctx, "swu_nosuchtok")
	if err != nil {
		t.Fatalf("unknown prefix: %v", err)
	}
	if metered {
		t.Fatal("an unknown prefix must not read as metered")
	}
}

// An unmetered token claims and runs without touching the ledger, which is
// what keeps an install that marks nothing behaving as it did before.
func TestUnmeteredClaimNeitherReservesNorCharges(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	_, tok, err := s.CreateToken("agent:local", store.TokenKindRunner, []string{"nodes.claim"}, 0, time.Now())
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	claimant := store.ClaimIdentity{Principal: "agent:local", TokenPrefix: tok.Prefix}
	readyNode(t, s, "run-local", "build")

	n, err := s.ClaimNextReadyNode(ctx, claimant, "pod-1", time.Minute, nil)
	if err != nil || n == nil {
		t.Fatalf("an unmetered claim on an empty ledger must succeed: %v", err)
	}
	if anchor := chargeWindowAnchor(t, s, "run-local", "build"); anchor != 0 {
		t.Fatalf("an unmetered claim anchored a charge window at %d", anchor)
	}
	charges, err := s.ListCreditCharges(ctx, 10)
	if err != nil {
		t.Fatalf("list charges: %v", err)
	}
	if len(charges) != 0 {
		t.Fatalf("an unmetered claim wrote %+v", charges)
	}
	res, err := s.FinalizeNodeCredits(ctx, "run-local", "build", tok.Prefix, time.Now())
	if err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if res.Charge != nil || res.Cancel {
		t.Fatalf("finalizing an unmetered node did %+v", res)
	}
}

func TestTokenRotationCarriesTheMeteredMarker(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	_, cloud, err := s.CreateTokenWith(ctx, "agent:cloud", store.TokenKindRunner, []string{"nodes.claim"}, 0, time.Now(),
		store.TokenOptions{Metered: true})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_, newTok, _, err := s.RotateToken(cloud.Prefix, time.Hour, 0, time.Now())
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if !newTok.Metered {
		t.Fatal("a rotation dropped the metering marker")
	}
	metered, err := s.TokenMetered(ctx, newTok.Prefix)
	if err != nil {
		t.Fatalf("token metered: %v", err)
	}
	if !metered {
		t.Fatal("the rotated token is not metered in the database")
	}
}

func TestAppendEventOnceCollapsesRepeats(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	seedClaimedNode(t, s, "run-once", "build")

	wrote, err := s.AppendEventOnce(ctx, "run-once", "build", store.EventKindCreditsBlocked, []byte(`{"balance_micro":0}`))
	if err != nil {
		t.Fatalf("append once: %v", err)
	}
	if !wrote {
		t.Fatal("the first append must write")
	}
	wrote, err = s.AppendEventOnce(ctx, "run-once", "build", store.EventKindCreditsBlocked, []byte(`{"balance_micro":0}`))
	if err != nil {
		t.Fatalf("append once again: %v", err)
	}
	if wrote {
		t.Fatal("the second append must collapse into the first")
	}
	events, err := s.ListEventsAfter(ctx, "run-once", 0, 10)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	seen := 0
	for _, e := range events {
		if e.Kind == store.EventKindCreditsBlocked {
			seen++
		}
	}
	if seen != 1 {
		t.Fatalf("credits_blocked events = %d, want 1", seen)
	}
}

func TestOldestWaitingReadyNodeNamesTheWaitingRun(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	runID, nodeID, err := s.OldestWaitingReadyNode(ctx)
	if err != nil {
		t.Fatalf("oldest waiting: %v", err)
	}
	if runID != "" || nodeID != "" {
		t.Fatalf("empty queue named %s/%s", runID, nodeID)
	}
	readyNode(t, s, "run-wait", "build")
	runID, nodeID, err = s.OldestWaitingReadyNode(ctx)
	if err != nil {
		t.Fatalf("oldest waiting: %v", err)
	}
	if runID != "run-wait" || nodeID != "build" {
		t.Fatalf("oldest waiting = %s/%s, want run-wait/build", runID, nodeID)
	}
}

func TestCreditHistoryLimitIsClampedToItsMaximum(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	for i := range 3 {
		if _, err := s.GrantCredits(ctx, store.CreditGrantFree, int64(i+1)*store.MicroCreditsPerCredit, "", "admin"); err != nil {
			t.Fatalf("grant %d: %v", i, err)
		}
	}
	for _, limit := range []int{0, 1, store.CreditHistoryMaxLimit, store.CreditHistoryMaxLimit + 5000} {
		grants, err := s.ListCreditGrants(ctx, limit)
		if err != nil {
			t.Fatalf("list with limit %d: %v", limit, err)
		}
		want := 3
		if limit == 1 {
			want = 1
		}
		if len(grants) != want {
			t.Fatalf("limit %d returned %d grants, want %d", limit, len(grants), want)
		}
	}
}

func TestFormatCreditsRendersTwoPlaces(t *testing.T) {
	cases := []struct {
		micro int64
		want  string
	}{
		{0, "0.00"},
		{store.MicroCreditsPerCredit, "1.00"},
		{1000 * store.MicroCreditsPerCredit, "1000.00"},
		{store.MicroCreditsPerCredit / 2, "0.50"},
		{-3 * store.MicroCreditsPerCredit / 2, "-1.50"},
	}
	for _, c := range cases {
		if got := store.FormatCredits(c.micro); got != c.want {
			t.Errorf("FormatCredits(%d) = %q, want %q", c.micro, got, c.want)
		}
	}
}

func TestCreditSettingsBoundTheRateSoTheLedgerCannotOverflow(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()

	if err := s.SetCreditRateMicroPerSecond(ctx, store.MaxCreditRateMicro); err != nil {
		t.Fatalf("the highest allowed rate was refused: %v", err)
	}
	floor, err := s.CreditClaimFloorMicro(ctx)
	if err != nil {
		t.Fatalf("floor: %v", err)
	}
	if floor <= 0 {
		t.Fatalf("claim floor = %d at the highest allowed rate; it must stay positive", floor)
	}

	for name, rate := range map[string]int64{
		"one past the ceiling": store.MaxCreditRateMicro + 1,
		"the int64 maximum":    math.MaxInt64,
		"zero":                 0,
		"negative":             -1,
	} {
		if err := s.SetCreditRateMicroPerSecond(ctx, rate); !errors.Is(err, store.ErrInvalidCreditSetting) {
			t.Errorf("%s was accepted as a rate: %v", name, err)
		}
	}
	rate, err := s.CreditRateMicroPerSecond(ctx)
	if err != nil {
		t.Fatalf("read rate: %v", err)
	}
	if rate != store.MaxCreditRateMicro {
		t.Fatalf("rate = %d; a refused write moved it", rate)
	}
}

func TestCreditClaimIsRefusedAtTheHighestAllowedRate(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	claimant := meteredClaimant(t, s, "agent:cloud")
	readyNode(t, s, "run-rate", "build")

	table := store.DefaultCreditRateTable(store.DefaultCreditRateMicro)
	for i := range table {
		table[i].MicroPerSecond = store.MaxCreditRateMicro
	}
	if err := s.SetCreditRateTable(ctx, table); err != nil {
		t.Fatalf("set rate: %v", err)
	}
	if _, err := s.GrantCredits(ctx, store.CreditGrantPaid,
		10*store.MicroCreditsPerCredit, "pay_1", "admin"); err != nil {
		t.Fatalf("grant: %v", err)
	}
	_, err := s.ClaimNextReadyNode(ctx, claimant, "pod-1", time.Minute, nil)
	if !errors.Is(err, store.ErrInsufficientCredits) {
		t.Fatalf("a claim for ten credits at the highest rate = %v, want a refusal", err)
	}
}

func TestCreditSettingsBoundTheChargeCap(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()

	if err := s.SetCreditMaxChargeSeconds(ctx, store.MinCreditMaxChargeSeconds); err != nil {
		t.Fatalf("the lowest allowed cap was refused: %v", err)
	}
	if err := s.SetCreditMaxChargeSeconds(ctx, store.MaxCreditMaxChargeSeconds); err != nil {
		t.Fatalf("the highest allowed cap was refused: %v", err)
	}
	for name, cap := range map[string]int64{
		"one under the floor":  store.MinCreditMaxChargeSeconds - 1,
		"one past the ceiling": store.MaxCreditMaxChargeSeconds + 1,
		"zero":                 0,
		"the int64 maximum":    math.MaxInt64,
	} {
		if err := s.SetCreditMaxChargeSeconds(ctx, cap); !errors.Is(err, store.ErrInvalidCreditSetting) {
			t.Errorf("%s was accepted as a charge cap: %v", name, err)
		}
	}
}

func TestCreditMaxChargeFloorClearsTheLongestHeartbeat(t *testing.T) {
	longest := int64(store.MaxNodeHeartbeatInterval / time.Second)
	if store.MinCreditMaxChargeSeconds != longest+1 {
		t.Fatalf("charge cap floor = %d, want one past the %ds heartbeat cadence",
			store.MinCreditMaxChargeSeconds, longest)
	}
	if store.PoolHeartbeatInterval > store.MaxNodeHeartbeatInterval ||
		store.DispatchedHeartbeatInterval > store.MaxNodeHeartbeatInterval {
		t.Fatal("a declared heartbeat cadence is longer than the one the cap is judged against")
	}
}

func TestSetCreditSettingsWritesEveryNamedValueOrNone(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()

	rate, grace, maxCharge := int64(30_000), int64(0), int64(45)
	got, err := s.SetCreditSettings(ctx, store.CreditSettingsUpdate{
		RateMicroPerSecond: &rate, GraceSeconds: &grace, MaxChargeSeconds: &maxCharge,
	})
	if err != nil {
		t.Fatalf("set all three: %v", err)
	}
	if got.RateMicroPerSecond != rate || got.GraceSeconds != grace || got.MaxChargeSeconds != maxCharge {
		t.Fatalf("settings = %+v", got)
	}

	bad := int64(-5)
	keep := int64(20)
	if _, err := s.SetCreditSettings(ctx, store.CreditSettingsUpdate{
		MaxChargeSeconds: &keep, RateMicroPerSecond: &bad,
	}); !errors.Is(err, store.ErrInvalidCreditSetting) {
		t.Fatalf("a mixed update = %v, want a refusal", err)
	}
	got, err = s.CreditSettings(ctx)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	if got.MaxChargeSeconds != maxCharge {
		t.Fatalf("charge cap = %d; the good half of a refused update was written", got.MaxChargeSeconds)
	}

	if _, err := s.SetCreditSettings(ctx, store.CreditSettingsUpdate{}); !errors.Is(
		err, store.ErrInvalidCreditSetting) {
		t.Fatalf("an update naming nothing = %v, want a refusal", err)
	}
}

// safety: the claim consumes the whole balance, so every later heartbeat reads
// a spent ledger and only the grace period decides when the node stops.
func exhaustedReservedNode(t *testing.T, s *store.Store, runID string, grace int64) (
	store.ClaimIdentity, time.Time,
) {
	t.Helper()
	ctx := context.Background()
	claimant := meteredClaimant(t, s, "agent:"+runID)
	readyNode(t, s, runID, "build")
	floor := unpinnedNodeRateMicro * store.CreditClaimFloorSeconds
	if _, err := s.GrantCredits(ctx, store.CreditGrantFree, floor, "", "admin"); err != nil {
		t.Fatalf("grant: %v", err)
	}
	if _, err := s.SetCreditSettings(ctx, store.CreditSettingsUpdate{GraceSeconds: &grace}); err != nil {
		t.Fatalf("set grace: %v", err)
	}
	start := time.Now()
	if _, err := s.ClaimNextReadyNode(ctx, claimant, "pod-1", time.Minute, nil); err != nil {
		t.Fatalf("claim: %v", err)
	}
	return claimant, start
}

func chargeAt(
	t *testing.T, s *store.Store, runID string, claimant store.ClaimIdentity, at time.Time,
) store.CreditChargeResult {
	t.Helper()
	res, err := s.ChargeNodeCredits(context.Background(), runID, "build", claimant.TokenPrefix, at)
	if err != nil {
		t.Fatalf("charge at %s: %v", at, err)
	}
	return res
}

func TestChargeNodeCreditsCountsGraceFromTheReservationEnd(t *testing.T) {
	reserved := time.Duration(store.CreditClaimFloorSeconds) * time.Second
	for _, tc := range []struct {
		name              string
		grace             int64
		survives, cancels time.Duration
	}{
		{
			"grace zero stops at the first heartbeat past the reservation",
			0, reserved, reserved + 5*time.Second,
		},
		{
			"grace sixty stops a minute past the reservation",
			60, reserved + 55*time.Second, reserved + 65*time.Second,
		},
		{
			"grace one twenty stops two minutes past the reservation",
			120, reserved + 115*time.Second, reserved + 125*time.Second,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := storetest.Open(t)
			runID := fmt.Sprintf("run-grace-%d", tc.grace)
			claimant, start := exhaustedReservedNode(t, s, runID, tc.grace)

			res := chargeAt(t, s, runID, claimant, start.Add(tc.survives))
			if res.BalanceMicro > 0 {
				t.Fatalf("balance at %s = %d, want it spent", tc.survives, res.BalanceMicro)
			}
			if res.Cancel {
				t.Fatalf("cancelled at %s, inside the %ds grace period past the reservation",
					tc.survives, tc.grace)
			}

			rewindChargeWindow(t, s, runID, "build", start.Add(tc.survives))
			if res := chargeAt(t, s, runID, claimant, start.Add(tc.cancels)); !res.Cancel {
				t.Fatalf("still running at %s, past the reservation and a %ds grace period",
					tc.cancels, tc.grace)
			}
		})
	}
}

func TestChargeNodeCreditsGivesEachNodeItsOwnGraceClock(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	reserved := time.Duration(store.CreditClaimFloorSeconds) * time.Second
	claimant := meteredClaimant(t, s, "agent:cloud")
	floor := unpinnedNodeRateMicro * store.CreditClaimFloorSeconds
	// safety: two reservations' worth, so both nodes claim before the ledger
	// empties and each carries a reservation of its own.
	if _, err := s.GrantCredits(ctx, store.CreditGrantFree, 2*floor, "", "admin"); err != nil {
		t.Fatalf("grant: %v", err)
	}
	grace := int64(0)
	if _, err := s.SetCreditSettings(ctx, store.CreditSettingsUpdate{GraceSeconds: &grace}); err != nil {
		t.Fatalf("set grace: %v", err)
	}

	start := time.Now()
	for _, runID := range []string{"run-early", "run-late"} {
		readyNode(t, s, runID, "build")
		if _, err := s.ClaimNextReadyNode(ctx, claimant, "pod-"+runID, time.Minute, nil); err != nil {
			t.Fatalf("claim %s: %v", runID, err)
		}
	}
	// safety: the early node is paid through an instant already past, so its
	// runway is spent while the late node sits inside its own.
	rewindChargeWindow(t, s, "run-early", "build", start.Add(-30*time.Second))

	at := start.Add(reserved / 2)
	if res := chargeAt(t, s, "run-early", claimant, at); !res.Cancel {
		t.Fatal("the early node outran the runway it was paid for and was not cancelled")
	}
	if res := chargeAt(t, s, "run-late", claimant, at); res.Cancel {
		t.Fatal("the late node was cancelled inside its own reservation, on the early node's clock")
	}
}

func TestChargeNodeCreditsNeverCancelsInsideTheClaimReservation(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	claimant := meteredClaimant(t, s, "agent:cloud")
	readyNode(t, s, "run-reserved", "build")
	floor := unpinnedNodeRateMicro * store.CreditClaimFloorSeconds
	if _, err := s.GrantCredits(ctx, store.CreditGrantFree, floor, "", "admin"); err != nil {
		t.Fatalf("grant: %v", err)
	}
	grace := int64(0)
	if _, err := s.SetCreditSettings(ctx, store.CreditSettingsUpdate{GraceSeconds: &grace}); err != nil {
		t.Fatalf("set grace: %v", err)
	}
	start := time.Now()
	if _, err := s.ClaimNextReadyNode(ctx, claimant, "pod-1", time.Minute, nil); err != nil {
		t.Fatalf("claim: %v", err)
	}

	// safety: the claim consumed the whole balance, so every heartbeat inside
	// the reserved minute reads a spent ledger and must still let it run.
	for _, at := range []time.Duration{3 * time.Second, 30 * time.Second, store.CreditClaimFloorSeconds * time.Second} {
		res, err := s.ChargeNodeCredits(ctx, "run-reserved", "build", claimant.TokenPrefix, start.Add(at))
		if err != nil {
			t.Fatalf("charge at %s: %v", at, err)
		}
		if res.BalanceMicro > 0 {
			t.Fatalf("balance at %s = %d, want it spent", at, res.BalanceMicro)
		}
		if res.Cancel {
			t.Fatalf("cancelled at %s, inside the minute the claim reserved and paid for", at)
		}
	}

	res, err := s.ChargeNodeCredits(ctx, "run-reserved", "build", claimant.TokenPrefix,
		start.Add((store.CreditClaimFloorSeconds+store.PoolHeartbeatInterval/time.Second)*time.Second))
	if err != nil {
		t.Fatalf("charge past the reservation: %v", err)
	}
	if !res.Cancel {
		t.Fatalf("grace zero must cancel at the first heartbeat past the reservation, got %+v", res)
	}
}

func TestChargeNodeCreditsKeepsTheGracePeriodPastTheReservation(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	claimant := meteredClaimant(t, s, "agent:cloud")
	readyNode(t, s, "run-graced", "build")
	floor := unpinnedNodeRateMicro * store.CreditClaimFloorSeconds
	if _, err := s.GrantCredits(ctx, store.CreditGrantFree, floor, "", "admin"); err != nil {
		t.Fatalf("grant: %v", err)
	}
	grace := int64(store.DefaultCreditGraceSeconds)
	if _, err := s.SetCreditSettings(ctx, store.CreditSettingsUpdate{GraceSeconds: &grace}); err != nil {
		t.Fatalf("set grace: %v", err)
	}
	start := time.Now()
	if _, err := s.ClaimNextReadyNode(ctx, claimant, "pod-1", time.Minute, nil); err != nil {
		t.Fatalf("claim: %v", err)
	}

	reservationEnd := start.Add(store.CreditClaimFloorSeconds * time.Second)
	res, err := s.ChargeNodeCredits(ctx, "run-graced", "build", claimant.TokenPrefix,
		reservationEnd.Add(5*time.Second))
	if err != nil {
		t.Fatalf("charge past the reservation: %v", err)
	}
	if res.Cancel {
		t.Fatalf("cancelled five seconds past the reservation with a %ds grace period", grace)
	}

	rewindChargeWindow(t, s, "run-graced", "build", reservationEnd)
	res, err = s.ChargeNodeCredits(ctx, "run-graced", "build", claimant.TokenPrefix,
		reservationEnd.Add(time.Duration(grace+5)*time.Second))
	if err != nil {
		t.Fatalf("charge after the grace period: %v", err)
	}
	if !res.Cancel {
		t.Fatalf("a node past its reservation and past the grace period must be cancelled, got %+v", res)
	}
}
