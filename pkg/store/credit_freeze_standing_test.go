package store_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
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
		_, err := st.AcceptInvitation(context.Background(), acct.Account.ID, invite(t, st, owner, email).ID, time.Now())
		return err
	}
	fund(t, tenant(t, st, team))
	if got := standing(t, st, team); got.Tier != store.TeamTierFunded {
		t.Fatalf("a team with credits = %+v, want funded", got)
	}
	if err := join("m0", "m0@example.com"); err != nil {
		t.Fatalf("a member of a paid team = %v", err)
	}

	if _, err := st.HoldTeamForDispute(ctx, team, "dp_frozen", "", "dispute opened", time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := standing(t, st, team); got.Tier == store.TeamTierFunded {
		t.Fatalf("a frozen team with a positive balance = %+v, want not funded", got)
	}
	if err := join("m1", "m1@example.com"); !errors.Is(err, store.ErrTeamFull) {
		t.Fatalf("a member past the free limit of a frozen paid team = %v, want ErrTeamFull", err)
	}

	if _, err := st.ReleaseCreditFreezes(ctx, team, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := standing(t, st, team); got.Tier != store.TeamTierFunded {
		t.Fatalf("after the hold is released = %+v, want funded again", got)
	}
	if err := join(fmt.Sprintf("m%d", 2), "m2@example.com"); err != nil {
		t.Fatalf("a member after the hold is released = %v", err)
	}
}
