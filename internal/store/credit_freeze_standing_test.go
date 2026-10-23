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

// A team held over a disputed payment keeps nothing that payment bought: it
// stores as an unfunded team and is held to the free member limit, until the
// operator releases the hold.
func TestAFrozenTeamIsNeitherFundedNorExemptFromTheMemberLimit(t *testing.T) {
	ctx := context.Background()
	st := storetest.Open(t)
	setLimits(t, st, store.SignUpLimits{FreeTeamMembers: 2})
	owner := signIn(t, st, "o", "o@example.com")
	team := owner.Account.ActiveTeam
	join := func(sub, email string) error {
		acct := signIn(t, st, sub, email)
		_, err := st.AsOperator().AcceptAccountInvitation(context.Background(), storetest.AccountAuthority(t, st, acct.Account.ID), invite(t, st, owner, email).ID)
		return err
	}
	fund(t, tenant(t, st, team))
	if got := standing(t, st, team); got.Tier != store.TeamTierFunded {
		t.Fatalf("a team with credits = %+v, want funded", got)
	}
	if err := join("m0", "m0@example.com"); err != nil {
		t.Fatalf("a member of a paid team = %v", err)
	}

	if _, err := teststore.ForTeam(t, st, team).TestOnlyHoldForDispute(ctx, "dp_frozen", "", "dispute opened", teststore.Now(st)); err != nil {
		t.Fatal(err)
	}
	if got := standing(t, st, team); got.Tier == store.TeamTierFunded {
		t.Fatalf("a frozen team with a positive balance = %+v, want not funded", got)
	}
	if err := join("m1", "m1@example.com"); !errors.Is(err, store.ErrTeamFull) {
		t.Fatalf("a member past the free limit of a frozen paid team = %v, want ErrTeamFull", err)
	}

	if _, err := teststore.ForTeam(t, st, team).ReleaseCreditFreezes(ctx, "dp_frozen", "", ""); err != nil {
		t.Fatal(err)
	}
	if got := standing(t, st, team); got.Tier != store.TeamTierFunded {
		t.Fatalf("after the hold is released = %+v, want funded again", got)
	}
	if err := join(fmt.Sprintf("m%d", 2), "m2@example.com"); err != nil {
		t.Fatalf("a member after the hold is released = %v", err)
	}
}

// A frozen team's events are held to the free share as its tier says: the
// admission of an append and the tier lookup read one funded predicate.
func TestAFrozenTeamsEventsAreHeldToTheFreeShare(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	setFreeAllowance(t, st, 1600)
	acme := teamHandle(t, st, "acme")
	teamRun(t, acme, "r1")
	fund(t, acme)
	if err := appendBytes(acme, "r1", 500); err != nil {
		t.Fatalf("a funded team's write past the share: %v", err)
	}
	if _, err := teststore.ForTeam(t, st, "acme").TestOnlyHoldForDispute(ctx, "dp_ev", "", "dispute opened", teststore.Now(st)); err != nil {
		t.Fatal(err)
	}
	if got := standing(t, st, "acme"); got.Tier == store.TeamTierFunded {
		t.Fatalf("a frozen team's tier = %+v, want not funded", got)
	}
	err := appendBytes(acme, "r1", 200)
	var quota *store.StorageQuotaError
	if !errors.As(err, &quota) || quota.Limit != store.StorageLimitFreeEvents {
		t.Fatalf("a frozen team's write past the share = %v, want a free event share refusal", err)
	}
}
