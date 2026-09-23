package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

// A reversal takes back what a payment still has on the ledger, in the team
// it funded, once: a repeat under the same reference returns the first, and a
// second reference for a payment already taken back writes nothing.
func TestReversePaymentTakesBackWhatThePaymentStillHas(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	acme := teamHandle(t, s, "acme")
	dollars := func(n int64) int64 { return n * 100 * store.MicroCreditsPerCent }
	if _, err := acme.RecordCreditGrant(ctx, store.CreditGrantRequest{
		Kind: store.CreditGrantPaid, AmountMicro: dollars(100), Reference: "pi_1", CreatedBy: "billing",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := acme.RecordCreditGrant(ctx, store.CreditGrantRequest{
		Kind: store.CreditGrantReversal, AmountMicro: -dollars(30), Reference: "re_part", Reverses: "pi_1",
		CreatedBy: "ops",
	}); err != nil {
		t.Fatal(err)
	}

	first, err := s.ReversePayment(ctx, "pi_1", "refund:pi_1", "ops")
	if err != nil {
		t.Fatalf("reverse: %v", err)
	}
	if !first.Created || first.Team != "acme" || first.Grant.AmountMicro != -dollars(70) ||
		first.PaidMicro != dollars(100) || first.ReversedMicro != dollars(100) || first.BalanceMicro != 0 {
		t.Fatalf("reversal = %+v, grant %+v; want the remaining $70 taken back from acme", first, first.Grant)
	}
	again, err := s.ReversePayment(ctx, "pi_1", "refund:pi_1", "ops")
	if err != nil || again.Created || again.Grant == nil || again.Grant.ID != first.Grant.ID {
		t.Fatalf("repeat = %+v, %v; want the first reversal back", again, err)
	}
	other, err := s.ReversePayment(ctx, "pi_1", "dp_1", "billing")
	if err != nil || other.Created || other.Grant != nil || other.ReversedMicro != dollars(100) {
		t.Fatalf("a second reference for a payment already reversed = %+v, %v; want nothing written", other, err)
	}
	if got, err := acme.CreditBalanceMicro(ctx); err != nil || got != 0 {
		t.Fatalf("balance = %d, %v; want 0", got, err)
	}
	if _, err := s.ReversePayment(ctx, "pi_unknown", "refund:pi_unknown", "ops"); !errors.Is(err, store.ErrUnknownPayment) {
		t.Fatalf("an unknown payment = %v, want ErrUnknownPayment", err)
	}
}

// A payment already spent is still reversed whole, so the balance goes below
// zero and the claim path stops the team's new metered work.
func TestReversePaymentMayTakeTheBalanceBelowZero(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	claimant, granted := fundedMeteredNode(t, s, "run-spent")
	if _, err := s.ClaimNextReadyNode(ctx, claimant, "pod-1", time.Minute, nil); err != nil {
		t.Fatalf("claim: %v", err)
	}
	res, err := s.ReversePayment(ctx, "pay_run-spent", "refund:pay_run-spent", "ops")
	if err != nil || res.Grant.AmountMicro != -granted || res.BalanceMicro >= 0 {
		t.Fatalf("reversal = %+v, %v; want the whole payment back and a negative balance", res, err)
	}
}

// A frozen team's metered claims are refused however much it holds, until the
// freeze is released; an unmetered claim is not affected.
func TestFrozenTeamsMeteredClaimsAreRefused(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	claimant, _ := fundedMeteredNode(t, s, "run-frozen")
	if err := s.SetTeamCreditFreeze(ctx, store.DefaultTeam, true, "dispute dp_1 opened", time.Now()); err != nil {
		t.Fatalf("freeze: %v", err)
	}
	_, err := s.ClaimNextReadyNode(ctx, claimant, "pod-1", time.Minute, nil)
	var refused *store.InsufficientCreditsError
	if !errors.As(err, &refused) || !refused.Frozen || !errors.Is(err, store.ErrInsufficientCredits) {
		t.Fatalf("a frozen team's claim = %v, want a frozen refusal", err)
	}

	pool := meteredClaimant(t, s, "agent:pool")
	pendingTrigger(t, s, "run-trig")
	if _, err := s.ClaimSpecificTriggerFor(ctx, "run-trig", pool, 0); !errors.As(err, &refused) || !refused.Frozen {
		t.Fatalf("a frozen team's trigger claim = %v, want a frozen refusal", err)
	}
	if _, err := s.ClaimSpecificTriggerFor(ctx, "run-trig", unmeteredClaimant(t, s, "agent:laptop"), 0); err != nil {
		t.Fatalf("an unmetered claim on a frozen team: %v", err)
	}

	if err := s.SetTeamCreditFreeze(ctx, store.DefaultTeam, false, "", time.Now()); err != nil {
		t.Fatalf("release: %v", err)
	}
	if n, err := s.ClaimNextReadyNode(ctx, claimant, "pod-1", time.Minute, nil); err != nil || n == nil {
		t.Fatalf("a claim after the release = %v, %v", n, err)
	}
	if err := s.SetTeamCreditFreeze(ctx, "nobody", true, "x", time.Now()); !errors.Is(err, store.ErrUnknownTeam) {
		t.Fatalf("freezing an unknown team = %v, want ErrUnknownTeam", err)
	}
}
