package controller_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// Accepting an invitation grants the role it names, so an invitation outlives
// its inviter's authority only up to what the inviter still holds: removal
// and account deletion withdraw everything they issued, and a demotion
// withdraws what now ranks above them.
func TestTeamBoundary_InvitationsGoWithTheirInvitersAuthority(t *testing.T) {
	cases := []struct {
		change      string
		invitedRole string
		wantGranted bool
	}{
		{"removed", "owner", false},
		{"removed", "reader", false},
		{"demoted to reader", "owner", false},
		{"demoted to reader", "editor", false},
		{"demoted to reader", "reader", true},
		{"demoted to editor", "owner", false},
		{"demoted to editor", "editor", true},
		{"deleted", "editor", false},
	}
	for _, c := range cases {
		t.Run(c.change+"/"+c.invitedRole, func(t *testing.T) {
			tenancyDialects(t, func(t *testing.T, f *tenancyFixture) {
				ctx := context.Background()
				alice, err := f.st.AccountByEmail(ctx, "alice@example.test")
				if err != nil {
					t.Fatal(err)
				}
				erin, _ := signUp(t, f.st, "erin")
				inv, err := f.teamA.CreateInvitation(ctx, alice.ID, erin.Email, store.RoleOwner, time.Now().UTC())
				if err != nil {
					t.Fatal(err)
				}
				if _, err := f.st.AcceptInvitation(ctx, erin.ID, inv.ID, time.Now().UTC()); err != nil {
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
				case "removed":
					code, raw = f.do(http.MethodDelete, "/api/v1/team/members/"+erin.ID, f.ownerA, nil)
				case "demoted to reader":
					code, raw = f.do(http.MethodPatch, "/api/v1/team/members/"+erin.ID, f.ownerA, map[string]string{"role": "reader"})
				case "demoted to editor":
					code, raw = f.do(http.MethodPatch, "/api/v1/team/members/"+erin.ID, f.ownerA, map[string]string{"role": "editor"})
				case "deleted":
					code = http.StatusNoContent
					if _, err := f.st.DeleteAccount(ctx, erin.ID, time.Now().UTC()); err != nil {
						t.Fatal(err)
					}
				}
				if code != http.StatusNoContent {
					t.Fatalf("alice: erin %s = %d %s", c.change, code, raw)
				}

				code, raw = f.do(http.MethodPost, "/api/v1/invitations/"+created.ID+"/accept",
					session(t, f.st, mallory, malloryTeam), nil)
				role, err := f.teamA.MemberRole(ctx, mallory.ID)
				granted := err == nil && role == store.Role(c.invitedRole)
				if granted != c.wantGranted {
					t.Errorf("after erin was %s, mallory accepting a %s invitation = %d %s; member role %q (%v), want granted=%v",
						c.change, c.invitedRole, code, raw, role, err, c.wantGranted)
				}
			})
		})
	}
}
