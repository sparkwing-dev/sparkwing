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

// The cap bounds the balance an operator's free grant may reach: a grant that
// fits alone is refused when the team already holds enough that the two
// together pass it.
func TestFreeGrantAboveTheBalanceCapIsRefused(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	acme := teamHandle(t, s, "acme")
	cent := int64(store.MicroCreditsPerCent)

	if _, err := acme.RecordCreditGrant(ctx, store.CreditGrantRequest{
		Kind: store.CreditGrantFree, AmountMicro: store.MaxTeamBalanceMicro - cent, Reference: "gift_1", CreatedBy: "ops",
	}); err != nil {
		t.Fatalf("a grant one cent under the cap: %v", err)
	}
	_, err := acme.RecordCreditGrant(ctx, store.CreditGrantRequest{
		Kind: store.CreditGrantFree, AmountMicro: 2 * cent, Reference: "gift_2", CreatedBy: "ops",
	})
	var capErr *store.CreditBalanceCapError
	if !errors.As(err, &capErr) || !errors.Is(err, store.ErrCreditBalanceCap) {
		t.Fatalf("a free grant past the cap = %v, want a CreditBalanceCapError", err)
	}
	if capErr.BalanceMicro != store.MaxTeamBalanceMicro-cent || capErr.AmountMicro != 2*cent ||
		capErr.CapMicro != store.MaxTeamBalanceMicro {
		t.Fatalf("refusal figures = %+v", capErr)
	}
	if got, err := acme.CreditBalanceMicro(ctx); err != nil || got != store.MaxTeamBalanceMicro-cent {
		t.Fatalf("balance after the refusal = %d, %v; a refused grant moved it", got, err)
	}
	replay, err := acme.RecordCreditGrant(ctx, store.CreditGrantRequest{
		Kind: store.CreditGrantFree, AmountMicro: store.MaxTeamBalanceMicro - cent, Reference: "gift_1", CreatedBy: "ops",
	})
	if err != nil || replay.Created {
		t.Fatalf("a replayed grant at the cap = %+v, %v; want the stored grant", replay, err)
	}

	globex := teamHandle(t, s, "globex")
	if _, err := globex.RecordCreditGrant(ctx, store.CreditGrantRequest{
		Kind: store.CreditGrantFree, AmountMicro: store.MaxTeamBalanceMicro, Reference: "gift_globex", CreatedBy: "ops",
	}); err != nil {
		t.Fatalf("another team's full balance was refused by acme's: %v", err)
	}
}

// Money has already moved when a verified payment is granted, so the cap
// never refuses it: two payments that each fit but together pass the cap both
// land, and the balance passes the cap by at most what was open at checkout.
func TestPaidGrantsForVerifiedPaymentsAreNeverRefusedByTheCap(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	acme := teamHandle(t, s, "acme")
	held := int64(4_600 * 100 * store.MicroCreditsPerCent)
	if _, err := acme.RecordCreditGrant(ctx, store.CreditGrantRequest{
		Kind: store.CreditGrantFree, AmountMicro: held, Reference: "gift", CreatedBy: "ops",
	}); err != nil {
		t.Fatal(err)
	}
	payment := int64(300 * 100 * store.MicroCreditsPerCent)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = acme.RecordCreditGrant(ctx, store.CreditGrantRequest{
				Kind: store.CreditGrantPaid, AmountMicro: payment,
				Reference: []string{"pi_a", "pi_b"}[i], CreatedBy: "billing",
			})
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatalf("a paid grant was refused: %v", err)
		}
	}
	if got, err := acme.CreditBalanceMicro(ctx); err != nil || got != held+2*payment {
		t.Fatalf("balance = %d, %v; want both payments, %d", got, err, held+2*payment)
	}
}

// The cap is held when a checkout opens: the balance plus every checkout
// still open plus the new one must fit. A checkout stops counting once its
// payment is granted or its session expires.
func TestOpenCheckoutsCountAgainstTheCap(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	acme := teamHandle(t, s, "acme")
	dollars := func(n int64) int64 { return n * 100 * store.MicroCreditsPerCent }
	now := time.Now()
	hold := 31 * time.Minute

	if _, err := acme.RecordCreditGrant(ctx, store.CreditGrantRequest{
		Kind: store.CreditGrantFree, AmountMicro: dollars(4000), Reference: "gift", CreatedBy: "ops",
	}); err != nil {
		t.Fatal(err)
	}
	first, err := acme.OpenCreditCheckout(ctx, dollars(600), now, hold)
	if err != nil {
		t.Fatalf("a checkout that fits: %v", err)
	}
	if err := acme.AttachCreditCheckout(ctx, first, "cs_first", now.Add(hold)); err != nil {
		t.Fatal(err)
	}
	_, err = acme.OpenCreditCheckout(ctx, dollars(600), now, hold)
	var capErr *store.CreditBalanceCapError
	if !errors.As(err, &capErr) || capErr.BalanceMicro != dollars(4000) || capErr.OpenMicro != dollars(600) {
		t.Fatalf("a second checkout past the cap with the first open = %v, want a refusal naming both", err)
	}
	if _, err := acme.OpenCreditCheckout(ctx, dollars(400), now, hold); err != nil {
		t.Fatalf("a checkout that fits beside the open one: %v", err)
	}

	later := now.Add(hold + time.Second)
	if _, err := acme.OpenCreditCheckout(ctx, dollars(1000), later, hold); err != nil {
		t.Fatalf("a checkout once the others expired: %v", err)
	}

	globex := teamHandle(t, s, "globex")
	if _, err := globex.RecordCreditGrant(ctx, store.CreditGrantRequest{
		Kind: store.CreditGrantFree, AmountMicro: dollars(4000), Reference: "gift", CreatedBy: "ops",
	}); err != nil {
		t.Fatal(err)
	}
	id, err := globex.OpenCreditCheckout(ctx, dollars(500), now, hold)
	if err != nil {
		t.Fatal(err)
	}
	if err := globex.AttachCreditCheckout(ctx, id, "cs_globex", now.Add(hold)); err != nil {
		t.Fatal(err)
	}
	if _, err := globex.RecordCreditGrant(ctx, store.CreditGrantRequest{
		Kind: store.CreditGrantPaid, AmountMicro: dollars(500), Reference: "pi_globex", Checkout: "cs_globex",
		CreatedBy: "billing",
	}); err != nil {
		t.Fatalf("the paid grant: %v", err)
	}
	if _, err := globex.OpenCreditCheckout(ctx, dollars(500), now, hold); err != nil {
		t.Fatalf("a checkout after the first was paid counts it once, in the balance: %v", err)
	}
	if _, err := globex.OpenCreditCheckout(ctx, dollars(5), now, hold); !errors.Is(err, store.ErrCreditBalanceCap) {
		t.Fatalf("a checkout past the cap = %v, want ErrCreditBalanceCap", err)
	}
}

// Two checkouts that each fit but together pass the cap race under the
// ledger lock, so exactly one opens.
func TestConcurrentCheckoutsCannotBothPassTheCap(t *testing.T) {
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
			_, errs[i] = acme.OpenCreditCheckout(ctx, half, time.Now(), 31*time.Minute)
		}(i)
	}
	wg.Wait()
	opened := 0
	for _, err := range errs {
		switch {
		case err == nil:
			opened++
		case errors.Is(err, store.ErrCreditBalanceCap):
		default:
			t.Fatalf("checkout: %v", err)
		}
	}
	if opened != 1 {
		t.Fatalf("%d checkouts opened, want exactly one under the cap", opened)
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
