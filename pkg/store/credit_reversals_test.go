package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

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

	if _, err := s.ReleaseCreditFreezes(ctx, store.DefaultTeam, "dp_1", "", "", now); err != nil {
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
	if n, err := s.ReleaseCreditFreezes(ctx, "acme", "dp_1", "", "", now); err != nil || n != 1 {
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
	if n, err := s.ReleaseCreditFreezes(ctx, "acme", "dp_2", "", "", now); err != nil || n != 1 {
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
	if _, err := s.ReversePayment(ctx, "pi_globex", "dp_1", "billing", 0); !errors.Is(err, store.ErrDisputeConflict) {
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
	if n, err := s.ReleaseCreditFreezes(ctx, "acme", "dp_1", "", "", now); err != nil || n != 1 {
		t.Fatalf("release by dispute = %d, %v; want exactly the one row", n, err)
	}
}

// A caller's reference may not carry the prefix that marks a warned
// repayment's unapplied rest, so a refund under such a name cannot pass for
// that rest and keep later refunds from reversing.
func TestAReversalMayNotUseTheUnappliedPrefix(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	acme := teamHandle(t, s, "acme")
	dollars := func(n int64) int64 { return n * 100 * store.MicroCreditsPerCent }
	if _, err := acme.RecordCreditGrant(ctx, store.CreditGrantRequest{
		Kind: store.CreditGrantPaid, AmountMicro: dollars(100), Reference: "pi_1", CreatedBy: "billing",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReversePayment(ctx, "pi_1", "unapplied:refund-1", "ops", dollars(30)); !errors.Is(err, store.ErrInvalidInput) {
		t.Fatalf("reverse under the reserved prefix = %v, want ErrInvalidInput", err)
	}
	if _, err := acme.RecordCreditGrant(ctx, store.CreditGrantRequest{
		Kind: store.CreditGrantReversal, AmountMicro: -dollars(30), Reference: "unapplied:refund-1", Reverses: "pi_1",
		CreatedBy: "ops",
	}); !errors.Is(err, store.ErrInvalidInput) {
		t.Fatalf("grant under the reserved prefix = %v, want ErrInvalidInput", err)
	}
	if _, err := s.ReversePayment(ctx, "pi_1", "refund-2", "ops", dollars(20)); err != nil {
		t.Fatal(err)
	}
	if bal, err := acme.CreditBalanceMicro(ctx); err != nil || bal != dollars(80) {
		t.Fatalf("balance = %d, %v; want $80 after the one $20 refund", bal, err)
	}
}

// A release names its hold: an empty or blank id is refused and releases
// nothing, so it can never stand for every hold on the team.
func TestReleaseCreditFreezesRefusesABlankHold(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	acme := teamHandle(t, s, "acme")
	now := time.Now()
	if _, err := s.HoldTeamForDispute(ctx, "acme", "dp_1", "", "dispute opened", now); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"", "   "} {
		if n, err := s.ReleaseCreditFreezes(ctx, "acme", id, "op", "resolved", now); !errors.Is(err, store.ErrInvalidInput) || n != 0 {
			t.Errorf("release %q = %d, %v; want an invalid-input refusal", id, n, err)
		}
	}
	if f, err := acme.CreditFreeze(ctx); err != nil || !f.Frozen || len(f.Disputes) != 1 {
		t.Fatalf("freeze after the refusals = %+v, %v; want dp_1 still holding", f, err)
	}
}
