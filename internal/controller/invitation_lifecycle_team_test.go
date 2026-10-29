package controller_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

// Every invitation requires the inviter's current team.admin grant at acceptance.
func TestTeamBoundary_InvitationsGoWithTheirInvitersAuthority(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 2.6s measured in the short class; the full suite runs it")
	}
	cases := []struct {
		change      string
		invitedRole string
		wantGranted bool
	}{
		{"retained", "owner", true},
		{"retained", "editor", true},
		{"retained", "reader", true},
		{"removed", "owner", false},
		{"removed", "reader", false},
		{"demoted to reader", "owner", false},
		{"demoted to reader", "editor", false},
		{"demoted to reader", "reader", false},
		{"demoted to editor", "owner", false},
		{"demoted to editor", "editor", false},
		{"deleted", "editor", false},
	}
	for _, c := range cases {
		t.Run(c.change+"/"+c.invitedRole, func(t *testing.T) {
			tenancyDialects(t, func(t *testing.T, f *tenancyFixture) {
				ctx := context.Background()
				alice := teststore.AccountByEmail(t, f.st, "alice@example.test")
				erin, _ := signUp(t, f.st, "erin")
				inv, err := fixtureInvitation(t, f.st, f.teamA, alice.ID, erin.Email, store.RoleOwner)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := f.st.AsOperator().AcceptAccountInvitation(ctx, fixtureAccountAuthority(t, f.st, erin), inv.ID); err != nil {
					t.Fatal(err)
				}
				erinSess := session(t, f.st, erin, f.teamA.Team())
				mallory, malloryTeam := signUp(t, f.st, "mallory")
				code, raw := f.do(http.MethodPost, "/api/v1/team/invitations", erinSess,
					map[string]string{"email": mallory.Email, "role": c.invitedRole})
				if code != http.StatusCreated {
					t.Fatalf("erin invites mallory as %s: %d %s", c.invitedRole, code, raw)
				}
				var created struct {
					ID string `json:"id"`
				}
				if err := json.Unmarshal([]byte(raw), &created); err != nil {
					t.Fatal(err)
				}

				switch c.change {
				case "retained":
					code = http.StatusNoContent
				case "removed":
					code, raw = f.do(http.MethodDelete, "/api/v1/team/members/"+erin.ID, f.ownerA, nil)
				case "demoted to reader":
					code, raw = f.do(http.MethodPatch, "/api/v1/team/members/"+erin.ID, f.ownerA, map[string]string{"role": "reader"})
				case "demoted to editor":
					code, raw = f.do(http.MethodPatch, "/api/v1/team/members/"+erin.ID, f.ownerA, map[string]string{"role": "editor"})
				case "deleted":
					code = http.StatusNoContent
					f.srv.WithOperatorAccounts([]string{"operator"})
					teststore.SeedTeam(t, f.st, "ops")
					console, operator := teststore.Console(t, f.st, "ops", "operator")
					operator.Target.AccountID = erin.ID
					if _, err := console.ConsoleDeleteAccount(ctx, operator); err != nil {
						t.Fatal(err)
					}
				}
				if code != http.StatusNoContent {
					t.Fatalf("alice: erin %s = %d %s", c.change, code, raw)
				}

				code, raw = f.do(http.MethodPost, "/api/v1/invitations/"+created.ID+"/accept",
					session(t, f.st, mallory, malloryTeam), nil)
				wantCode := http.StatusConflict
				if c.wantGranted {
					wantCode = http.StatusNoContent
				}
				if code != wantCode {
					t.Fatalf("accept invitation: %d %s, want %d", code, raw, wantCode)
				}
				role, err := f.teamA.MemberRole(ctx, mallory.ID)
				if !c.wantGranted && !errors.Is(err, store.ErrNotMember) {
					t.Fatalf("refused invitation changed membership: role=%q err=%v", role, err)
				}
				granted := err == nil && role == store.Role(c.invitedRole)
				if granted != c.wantGranted {
					t.Errorf("after erin was %s, mallory accepting a %s invitation = %d %s; member role %q (%v), want granted=%v",
						c.change, c.invitedRole, code, raw, role, err, c.wantGranted)
				}
			})
		})
	}
}
