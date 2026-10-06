package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func seedMinter(t *testing.T, st *store.Store, team store.Team, accountID string, role store.Role) {
	t.Helper()
	if _, err := st.DB().ExecContext(context.Background(), storetest.Rebind(st,
		`INSERT INTO memberships (team, account_id, role, created_at) VALUES (?, ?, ?, ?)`),
		string(team), accountID, string(role), time.Now().Unix()); err != nil {
		t.Fatalf("seed %s as %s of %s: %v", accountID, role, team, err)
	}
}

// The controller checks the minter's role before the mint transaction opens,
// and a removal or demotion that commits in between has already swept the
// tokens the minter holds, so the mint itself reads the role again.
func TestRunnerTokenMintRechecksTheMintersRole(t *testing.T) {
	for _, change := range []string{"removed", "demoted"} {
		t.Run(change, func(t *testing.T) {
			st := storetest.Open(t)
			ctx := context.Background()
			owner := signIn(t, st, "owner-"+change, "owner-"+change+"@example.com")
			member := signIn(t, st, "member-"+change, "member-"+change+"@example.com")
			tn := tenant(t, st, owner.PersonalTeam)
			inv, err := tn.CreateInvitation(ctx, owner.Account.ID, member.Account.Email, store.RoleEditor, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := st.AcceptInvitation(ctx, member.Account.ID, inv.ID, time.Now()); err != nil {
				t.Fatal(err)
			}
			if _, _, err := tn.CreateRunnerToken(ctx, "agent:before-"+change, []string{"nodes.claim"}, member.Account.ID, time.Now()); err != nil {
				t.Fatalf("an editor cannot mint a runner token: %v", err)
			}
			switch change {
			case "removed":
				_, err = tn.RemoveMember(ctx, owner.Account.ID, member.Account.ID, time.Now())
			case "demoted":
				_, err = tn.SetMemberRole(ctx, owner.Account.ID, member.Account.ID, store.RoleReader, time.Now())
			}
			if err != nil {
				t.Fatal(err)
			}
			raw, _, err := tn.CreateRunnerToken(ctx, "agent:after-"+change, []string{"nodes.claim"}, member.Account.ID, time.Now())
			if !errors.Is(err, store.ErrNotMember) && !errors.Is(err, store.ErrRoleAboveOwn) {
				t.Fatalf("minting for a member who was %s = %v, want a refusal", change, err)
			}
			if raw != "" {
				if _, lerr := st.LookupToken(raw, time.Now()); lerr == nil {
					t.Fatalf("a runner token minted after the member was %s is live", change)
				}
			}
		})
	}
}
