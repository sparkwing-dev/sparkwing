package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

// A reference a team's operator chose, such as a welcome grant, names a grant
// within that team: two teams each granted "welcome" get one grant each.
func TestCreditGrantReferencesAreKeyedPerTeam(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	acme, other := teamHandle(t, st, "acme"), teamHandle(t, st, "other")
	for _, team := range []*store.Tenant{acme, other} {
		if _, err := team.GrantCredits(ctx, store.CreditGrantFree, 5*store.MicroCreditsPerCent, "welcome", "operator"); err != nil {
			t.Fatalf("welcome grant: %v", err)
		}
		// A retry of the same grant stays one grant.
		if _, err := team.GrantCredits(ctx, store.CreditGrantFree, 5*store.MicroCreditsPerCent, "welcome", "operator"); err != nil {
			t.Fatalf("welcome grant retry: %v", err)
		}
	}
	for _, team := range []*store.Tenant{acme, other} {
		balance, err := team.CreditBalanceMicro(ctx)
		if err != nil || balance != 5*store.MicroCreditsPerCent {
			t.Fatalf("balance = %d, %v; want one welcome grant", balance, err)
		}
	}
	// A payment id is the deployment's: one seen under a second team is
	// still refused rather than paid twice.
	if _, err := acme.GrantCredits(ctx, store.CreditGrantPaid, store.MicroCreditsPerCent, "pay_1", "billing"); err != nil {
		t.Fatal(err)
	}
	if _, err := other.GrantCredits(ctx, store.CreditGrantPaid, store.MicroCreditsPerCent, "pay_1", "billing"); !errors.Is(err, store.ErrCreditGrantConflict) {
		t.Fatalf("a payment id reused by another team = %v, want ErrCreditGrantConflict", err)
	}
}
