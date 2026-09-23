package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

// A team cannot be deleted while money is in flight for it: an unexpired
// checkout may still be paid, and a dispute hold is the record the dispute
// is about. Either refuses the request with its remedy until it clears.
func TestTeamDeletionWaitsForOpenCheckoutsAndDisputeHolds(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	now := time.Now()
	owner := signIn(t, st, "o", "owner@example.com")
	for _, slug := range []store.Team{"acme", "held"} {
		if _, err := st.CreateTeam(ctx, owner.Account.ID, slug, string(slug), now); err != nil {
			t.Fatal(err)
		}
	}

	acme := tenant(t, st, "acme")
	if _, err := acme.OpenCreditCheckout(ctx, store.MicroCreditsPerCredit, now, time.Hour); err != nil {
		t.Fatal(err)
	}
	_, _, err := acme.RequestDeletion(ctx, owner.Account.ID, now)
	if !errors.Is(err, store.ErrOpenCheckout) || !strings.Contains(err.Error(), "wait for the checkout to expire or complete") {
		t.Fatalf("deleting a team with an open checkout = %v, want ErrOpenCheckout naming the remedy", err)
	}
	if _, _, err := st.AsOperator().RequestTeamDeletion(ctx, "acme", now); !errors.Is(err, store.ErrOpenCheckout) {
		t.Fatalf("the operator deleting it = %v, want ErrOpenCheckout", err)
	}
	if _, err := st.ForTeam(ctx, "acme"); err != nil {
		t.Fatalf("a refused deletion closed the team: %v", err)
	}
	if _, _, err := acme.RequestDeletion(ctx, owner.Account.ID, now.Add(2*time.Hour)); err != nil {
		t.Fatalf("deleting it once the checkout expired = %v", err)
	}

	held := tenant(t, st, "held")
	if _, err := st.HoldTeamForDispute(ctx, "held", "dp_held", "", "dispute opened", now); err != nil {
		t.Fatal(err)
	}
	_, _, err = held.RequestDeletion(ctx, owner.Account.ID, now)
	if !errors.Is(err, store.ErrTeamFrozen) || !strings.Contains(err.Error(), "contact support") {
		t.Fatalf("deleting a held team = %v, want ErrTeamFrozen naming the remedy", err)
	}
	if _, err := st.ReleaseCreditFreezes(ctx, "held", "", now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := held.RequestDeletion(ctx, owner.Account.ID, now); err != nil {
		t.Fatalf("deleting it once the hold was released = %v", err)
	}
}
