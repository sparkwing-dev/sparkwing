package store_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestAFrozenTeamIsUnfundedAndCanStillAddMembers(t *testing.T) {
	ctx := context.Background()
	st := storetest.Open(t)
	owner := signIn(t, st, "o", "o@example.com")
	team := owner.Account.ActiveTeam
	join := func(sub, email string) error {
		acct := signIn(t, st, sub, email)
		_, err := st.AsOperator().AcceptAccountInvitation(context.Background(), storetest.AccountAuthority(t, st, acct.Account.ID), invite(t, st, owner, email).ID)
		return err
	}
	fund(t, tenant(t, st, team))
	if got := standing(t, st, team); got.Tier != "funded" {
		t.Fatalf("a team with credits = %+v, want funded", got)
	}
	if err := join("m0", "m0@example.com"); err != nil {
		t.Fatalf("a member of a paid team = %v", err)
	}

	if _, err := teststore.ForTeam(t, st, team).TestOnlyHoldForDispute(ctx, "dp_frozen", "", "dispute opened", teststore.Now(st)); err != nil {
		t.Fatal(err)
	}
	if got := standing(t, st, team); got.Tier == "funded" {
		t.Fatalf("a frozen team with a positive balance = %+v, want not funded", got)
	}
	for i := range 12 {
		if err := join(fmt.Sprintf("held%d", i), fmt.Sprintf("held%d@example.com", i)); err != nil {
			t.Fatalf("member %d while payment is frozen = %v", i+3, err)
		}
	}

	if _, err := teststore.ForTeam(t, st, team).ReleaseCreditFreezes(ctx, "dp_frozen", "operator", ""); err != nil {
		t.Fatal(err)
	}
	if got := standing(t, st, team); got.Tier != "funded" {
		t.Fatalf("after the hold is released = %+v, want funded again", got)
	}
	if err := join("m2", "m2@example.com"); err != nil {
		t.Fatalf("a member after the hold is released = %v", err)
	}
}

// A frozen team's events are refused while the hold lasts: the admission of an append and the tier lookup read one
// funded predicate, so the credits the disputed payment bought fund neither.
func TestAFrozenTeamsEventsAreRefused(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	acme := teamHandle(t, st, "acme")
	teamRun(t, acme, "r1")
	fund(t, acme)
	if err := appendBytes(acme, "r1", 500); err != nil {
		t.Fatalf("a funded team's write: %v", err)
	}
	if _, err := teststore.ForTeam(t, st, "acme").TestOnlyHoldForDispute(ctx, "dp_ev", "", "dispute opened", teststore.Now(st)); err != nil {
		t.Fatal(err)
	}
	if got := standing(t, st, "acme"); got.Tier == "funded" {
		t.Fatalf("a frozen team's tier = %+v, want not funded", got)
	}
	err := appendBytes(acme, "r1", 200)
	var held *store.InsufficientCreditsError
	if !errors.As(err, &held) || held.Hold == "" {
		t.Fatalf("a frozen team's write = %v, want a hold refusal", err)
	}
}
