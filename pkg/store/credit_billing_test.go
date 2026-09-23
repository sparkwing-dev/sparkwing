package store_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

// The cap bounds the balance, not the grant: a purchase that fits alone is
// refused when the team already holds enough that the two together pass it.
func TestGrantAboveTheBalanceCapIsRefused(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	acme := teamHandle(t, s, "acme")
	cent := int64(store.MicroCreditsPerCent)

	if _, err := acme.RecordCreditGrant(ctx, store.CreditGrantRequest{
		Kind: store.CreditGrantPaid, AmountMicro: store.MaxTeamBalanceMicro - cent, Reference: "pi_1", CreatedBy: "billing",
	}); err != nil {
		t.Fatalf("a grant one cent under the cap: %v", err)
	}
	for _, kind := range []string{store.CreditGrantPaid, store.CreditGrantFree} {
		_, err := acme.RecordCreditGrant(ctx, store.CreditGrantRequest{
			Kind: kind, AmountMicro: 2 * cent, Reference: "over_" + kind, CreatedBy: "billing",
		})
		var capErr *store.CreditBalanceCapError
		if !errors.As(err, &capErr) || !errors.Is(err, store.ErrCreditBalanceCap) {
			t.Fatalf("a %s grant past the cap = %v, want a CreditBalanceCapError", kind, err)
		}
		if capErr.BalanceMicro != store.MaxTeamBalanceMicro-cent || capErr.AmountMicro != 2*cent ||
			capErr.CapMicro != store.MaxTeamBalanceMicro {
			t.Fatalf("refusal figures = %+v", capErr)
		}
	}
	if got, err := acme.CreditBalanceMicro(ctx); err != nil || got != store.MaxTeamBalanceMicro-cent {
		t.Fatalf("balance after refusals = %d, %v; a refused grant moved it", got, err)
	}
	if _, err := acme.RecordCreditGrant(ctx, store.CreditGrantRequest{
		Kind: store.CreditGrantPaid, AmountMicro: cent, Reference: "pi_2", CreatedBy: "billing",
	}); err != nil {
		t.Fatalf("a grant landing exactly on the cap: %v", err)
	}
	replay, err := acme.RecordCreditGrant(ctx, store.CreditGrantRequest{
		Kind: store.CreditGrantPaid, AmountMicro: store.MaxTeamBalanceMicro - cent, Reference: "pi_1", CreatedBy: "billing",
	})
	if err != nil || replay.Created {
		t.Fatalf("a replayed payment at the cap = %+v, %v; want the stored grant", replay, err)
	}
	if _, err := acme.RecordCreditGrant(ctx, store.CreditGrantRequest{
		Kind: store.CreditGrantReversal, AmountMicro: -cent, Reference: "re_1", Reverses: "pi_2", CreatedBy: "billing",
	}); err != nil {
		t.Fatalf("a reversal at the cap: %v", err)
	}
	if err := acme.CheckCreditPurchase(ctx, cent); err != nil {
		t.Fatalf("a purchase that fits after the reversal: %v", err)
	}
	if err := acme.CheckCreditPurchase(ctx, 2*cent); !errors.Is(err, store.ErrCreditBalanceCap) {
		t.Fatalf("a purchase past the cap = %v, want ErrCreditBalanceCap", err)
	}

	globex := teamHandle(t, s, "globex")
	if _, err := globex.RecordCreditGrant(ctx, store.CreditGrantRequest{
		Kind: store.CreditGrantPaid, AmountMicro: store.MaxTeamBalanceMicro, Reference: "pi_globex", CreatedBy: "billing",
	}); err != nil {
		t.Fatalf("another team's full balance was refused by acme's: %v", err)
	}
}

// Two grants that each fit but together pass the cap race under the ledger
// lock, so exactly one lands.
func TestConcurrentGrantsCannotBothPassTheCap(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	acme := teamHandle(t, s, "acme")
	half := int64(store.MaxTeamBalanceMicro/2 + store.MicroCreditsPerCent)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = acme.RecordCreditGrant(ctx, store.CreditGrantRequest{
				Kind: store.CreditGrantPaid, AmountMicro: half,
				Reference: []string{"pi_a", "pi_b"}[i], CreatedBy: "billing",
			})
		}(i)
	}
	wg.Wait()
	landed := 0
	for _, err := range errs {
		switch {
		case err == nil:
			landed++
		case errors.Is(err, store.ErrCreditBalanceCap):
		default:
			t.Fatalf("grant: %v", err)
		}
	}
	if landed != 1 {
		t.Fatalf("%d grants landed, want exactly one under the cap", landed)
	}
	if got, err := acme.CreditBalanceMicro(ctx); err != nil || got > store.MaxTeamBalanceMicro {
		t.Fatalf("balance = %d, %v; the race passed the cap", got, err)
	}
}

// A run's usage nets its reservation, usage and refunds, includes its
// trigger step, and leaves storage and other teams out.
func TestCreditUsageByRunGroupsATeamsRunnerSpend(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	acme := teamHandle(t, s, "acme")
	globex := teamHandle(t, s, "globex")
	if _, err := acme.RecordCreditGrant(ctx, store.CreditGrantRequest{
		Kind: store.CreditGrantPaid, AmountMicro: 100 * store.MicroCreditsPerCent, Reference: "pi_acme", CreatedBy: "billing",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := globex.RecordCreditGrant(ctx, store.CreditGrantRequest{
		Kind: store.CreditGrantPaid, AmountMicro: 100 * store.MicroCreditsPerCent, Reference: "pi_globex", CreatedBy: "billing",
	}); err != nil {
		t.Fatal(err)
	}
	acmeRunner := meteredTeamClaimant(t, acme, "agent:acme")
	globexRunner := meteredTeamClaimant(t, globex, "agent:globex")
	readyTeamNode(t, s, acme, "run-a1", "build")
	readyTeamNode(t, s, acme, "run-a2", "build")
	readyTeamNode(t, s, globex, "run-g1", "build")
	for _, c := range []struct {
		runner store.ClaimIdentity
		runID  string
	}{{acmeRunner, "run-a1"}, {acmeRunner, "run-a2"}, {globexRunner, "run-g1"}} {
		n, err := s.ClaimNextReadyNode(ctx, c.runner, "pod-"+c.runID, time.Minute, nil)
		if err != nil || n == nil {
			t.Fatalf("claim %s: %v", c.runID, err)
		}
		if _, err := s.FinalizeNodeCredits(ctx, n.RunID, n.NodeID, c.runner.TokenPrefix, time.Now()); err != nil {
			t.Fatalf("finalize %s: %v", c.runID, err)
		}
	}

	usage, err := acme.CreditUsageByRun(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(usage) != 2 {
		t.Fatalf("acme usage = %+v, want its two runs and not globex's", usage)
	}
	for _, u := range usage {
		if u.RunID != "run-a1" && u.RunID != "run-a2" {
			t.Fatalf("acme usage lists %s", u.RunID)
		}
		if u.Seconds != store.MinBillableSeconds || u.AmountMicro != store.MinBillableSeconds*unpinnedNodeRateMicro {
			t.Fatalf("usage of %s = %+v, want the minimum", u.RunID, u)
		}
	}
	if limited, err := acme.CreditUsageByRun(ctx, 1); err != nil || len(limited) != 1 {
		t.Fatalf("a limit of one returned %d rows, %v", len(limited), err)
	}
}
