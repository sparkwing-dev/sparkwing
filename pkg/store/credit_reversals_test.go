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

// A frozen team's metered claims are refused however much it holds, until
// every freeze on it is released; an unmetered claim is not affected.
func TestFrozenTeamsMeteredClaimsAreRefused(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	claimant, _ := fundedMeteredNode(t, s, "run-frozen")
	now := time.Now()
	if _, err := s.HoldTeamForDispute(ctx, store.DefaultTeam, "dp_1", "", "dispute opened", now); err != nil {
		t.Fatalf("hold: %v", err)
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

	if _, err := s.ReleaseCreditFreezes(ctx, store.DefaultTeam, "", now); err != nil {
		t.Fatalf("release: %v", err)
	}
	if n, err := s.ClaimNextReadyNode(ctx, claimant, "pod-1", time.Minute, nil); err != nil || n == nil {
		t.Fatalf("a claim after the release = %v, %v", n, err)
	}
	if _, err := s.HoldTeamForDispute(ctx, "nobody", "dp_x", "", "", now); !errors.Is(err, store.ErrUnknownTeam) {
		t.Fatalf("holding an unknown team = %v, want ErrUnknownTeam", err)
	}
}

// A freeze is one row per dispute and the team stays frozen while any is
// unreleased: releasing one dispute leaves the other's hold, and holding a
// dispute again, however often it is replayed, never undoes a release.
func TestATeamStaysFrozenWhileAnyDisputeHoldsIt(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	acme := teamHandle(t, s, "acme")
	now := time.Now()
	for _, dispute := range []string{"dp_1", "dp_2", "dp_1"} {
		if _, err := s.HoldTeamForDispute(ctx, "acme", dispute, "", "dispute opened", now); err != nil {
			t.Fatalf("hold %s: %v", dispute, err)
		}
	}
	frozen := func() store.TeamCreditFreeze {
		t.Helper()
		f, err := acme.CreditFreeze(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	if f := frozen(); !f.Frozen || len(f.Disputes) != 2 {
		t.Fatalf("freeze = %+v, want two disputes holding the team", f)
	}
	if n, err := s.ReleaseCreditFreezes(ctx, "acme", "dp_1", now); err != nil || n != 1 {
		t.Fatalf("release dp_1 = %d, %v", n, err)
	}
	if f := frozen(); !f.Frozen || len(f.Disputes) != 1 || f.Disputes[0] != "dp_2" {
		t.Fatalf("freeze after releasing one dispute = %+v, want dp_2 still holding it", f)
	}
	if created, err := s.HoldTeamForDispute(ctx, "acme", "dp_1", "", "replayed", now); err != nil || created {
		t.Fatalf("a replayed hold of a released dispute = %v, %v; want nothing written", created, err)
	}
	if f := frozen(); len(f.Disputes) != 1 {
		t.Fatalf("a replay re-held a released dispute: %+v", f)
	}
	team, found, err := s.DisputeTeam(ctx, "dp_2")
	if err != nil || !found || team != "acme" {
		t.Fatalf("dispute team = %q, %v, %v", team, found, err)
	}
	if n, err := s.ReleaseCreditFreezes(ctx, team, "dp_2", now); err != nil || n != 1 {
		t.Fatalf("release dp_2 = %d, %v", n, err)
	}
	if f := frozen(); f.Frozen {
		t.Fatalf("freeze after every release = %+v", f)
	}
}

// A dispute belongs to one payment and so one team: a hold or a lost-dispute
// reversal naming the same dispute with another payment is refused, and the
// first team's hold stands untouched.
func TestADisputeIsBoundToOnePayment(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	acme := teamHandle(t, s, "acme")
	globex := teamHandle(t, s, "globex")
	now := time.Now()
	for _, tc := range []struct {
		team *store.Tenant
		pi   string
	}{{acme, "pi_acme"}, {globex, "pi_globex"}} {
		if _, err := tc.team.RecordCreditGrant(ctx, store.CreditGrantRequest{
			Kind: store.CreditGrantPaid, AmountMicro: 100 * store.MicroCreditsPerCent, Reference: tc.pi, CreatedBy: "billing",
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.HoldTeamForDispute(ctx, "acme", "dp_1", "pi_acme", "opened", now); err != nil {
		t.Fatalf("hold: %v", err)
	}
	if created, err := s.HoldTeamForDispute(ctx, "acme", "dp_1", "pi_acme", "replayed", now); err != nil || created {
		t.Fatalf("a replay of the same hold = %v, %v; want nothing written", created, err)
	}
	if _, err := s.HoldTeamForDispute(ctx, "globex", "dp_1", "pi_globex", "opened", now); !errors.Is(err, store.ErrDisputeConflict) {
		t.Fatalf("the same dispute with another team's payment = %v, want ErrDisputeConflict", err)
	}
	if _, err := s.ReversePayment(ctx, "pi_globex", "dp_1", "billing"); !errors.Is(err, store.ErrDisputeConflict) {
		t.Fatalf("a lost-dispute reversal of another payment under the dispute = %v, want ErrDisputeConflict", err)
	}
	if f, err := acme.CreditFreeze(ctx); err != nil || !f.Frozen || len(f.Disputes) != 1 {
		t.Fatalf("acme's freeze = %+v, %v; want its hold untouched", f, err)
	}
	if f, err := globex.CreditFreeze(ctx); err != nil || f.Frozen {
		t.Fatalf("globex's freeze = %+v, %v; want no hold", f, err)
	}
	if got, err := globex.CreditBalanceMicro(ctx); err != nil || got != 100*store.MicroCreditsPerCent {
		t.Fatalf("globex balance = %d, %v; the refused reversal moved it", got, err)
	}
	if n, err := s.ReleaseCreditFreezes(ctx, "acme", "dp_1", now); err != nil || n != 1 {
		t.Fatalf("release by dispute = %d, %v; want exactly the one row", n, err)
	}
}
