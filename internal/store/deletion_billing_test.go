package store_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

// A handle taken before the team's deletion began cannot open a checkout
// after it: the checkout would take a payment for a team about to be purged.
func TestACheckoutCannotOpenOnceTheTeamIsBeingDeleted(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	now := time.Now()
	owner := signIn(t, st, "o", "owner@example.com")
	if _, err := st.AsOperator().CreateAccountTeam(ctx, storetest.AccountAuthority(t, st, owner.Account.ID), store.TeamInfo{Slug: "acme", DisplayName: "Acme"}); err != nil {
		t.Fatal(err)
	}
	stale := tenant(t, st, "acme")
	if _, err := stale.TestOnlyOpenCreditCheckout(ctx, 50*store.MicroCreditsPerCent, now.Add(-2*time.Hour), time.Hour); err != nil {
		t.Fatalf("a checkout before the deletion = %v", err)
	}
	if _, _, err := stale.DeleteTeamForTest(t, ctx, owner.Account.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := stale.TestOnlyOpenCreditCheckout(ctx, 50*store.MicroCreditsPerCent, now, time.Hour); !errors.Is(err, store.ErrTeamBeingDeleted) {
		t.Fatalf("a checkout on a team being deleted = %v, want ErrTeamBeingDeleted", err)
	}
	if n := countWhere(t, st, "credit_checkouts", fmt.Sprintf("team = 'acme' AND opened_at >= %d", now.UnixNano())); n != 0 {
		t.Fatalf("%d checkouts left for a team being deleted", n)
	}
}
