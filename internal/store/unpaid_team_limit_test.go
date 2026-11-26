package store_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

func createOwnedTeam(t *testing.T, st *store.Store, accountID string, slug store.Team) error {
	t.Helper()
	_, err := st.AsOperator().CreateAccountTeam(context.Background(), storetest.AccountAuthority(t, st, accountID), store.TeamInfo{Slug: slug})
	return err
}

func requireUnpaidTeamLimit(t *testing.T, err error) {
	t.Helper()
	requireUnpaidTeamUsage(t, err, 3)
}

func unpaidTeamRefusal(t *testing.T, err error) string {
	t.Helper()
	var failure *apierr.Error
	if !errors.As(err, &failure) || failure.Code != hosterr.LimitExceeded || failure.Details["reason"] != "unpaid_team_limit" {
		t.Fatalf("grant past the unpaid limit = %v, want limit_exceeded with reason unpaid_team_limit", err)
	}
	return failure.Message
}

func requireUnpaidTeamUsage(t *testing.T, err error, owned int) {
	t.Helper()
	message := unpaidTeamRefusal(t, err)
	for _, want := range []string{"unpaid team limit is 3 per account", fmt.Sprintf("you own %d teams that are unpaid in the current payment mode", owned), "adding funds to one of them", "deleting or leaving one", "support@sparkwing.dev"} {
		if !strings.Contains(message, want) {
			t.Fatalf("refusal %q omits %q", message, want)
		}
	}
}

func payTeam(t *testing.T, st *store.Store, team store.Team, reference string) {
	t.Helper()
	if _, err := tenant(t, st, team).TestOnlySettlePaidCredit(context.Background(), store.CreditGrantRequest{Kind: store.CreditGrantPaid, AmountMicro: 500 * store.MicroCreditsPerCent, Reference: reference, CreatedBy: "billing"}); err != nil {
		t.Fatal(err)
	}
}

func TestAccountOwnsAtMostThreeUnpaidTeams(t *testing.T) {
	st := storetest.Open(t)
	u := signIn(t, st, "s", "busy@example.com")
	for _, slug := range []store.Team{"busy-two", "busy-three"} {
		if err := createOwnedTeam(t, st, u.Account.ID, slug); err != nil {
			t.Fatalf("team %s: %v", slug, err)
		}
	}
	requireUnpaidTeamLimit(t, createOwnedTeam(t, st, u.Account.ID, "busy-four"))
	if _, err := st.TeamInfo(context.Background(), "busy-four"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("refused team was registered: %v", err)
	}
	other := signIn(t, st, "o", "other@example.com")
	if err := createOwnedTeam(t, st, other.Account.ID, "other-two"); err != nil {
		t.Fatalf("another account's team: %v", err)
	}
}

func TestPaidTeamsDoNotCountTowardTheUnpaidTeamLimit(t *testing.T) {
	st := storetest.Open(t)
	u := signIn(t, st, "s", "payer@example.com")
	payTeam(t, st, u.PersonalTeam, "pay-personal")
	for _, slug := range []store.Team{"payer-two", "payer-three", "payer-four"} {
		if err := createOwnedTeam(t, st, u.Account.ID, slug); err != nil {
			t.Fatalf("team %s beside a paid team: %v", slug, err)
		}
	}
	requireUnpaidTeamLimit(t, createOwnedTeam(t, st, u.Account.ID, "payer-five"))
}

func TestFullyRefundedTeamCountsAsUnpaid(t *testing.T) {
	st := storetest.Open(t)
	u := signIn(t, st, "s", "refund@example.com")
	payTeam(t, st, u.PersonalTeam, "pay-refunded")
	for _, slug := range []store.Team{"refund-two", "refund-three", "refund-four"} {
		if err := createOwnedTeam(t, st, u.Account.ID, slug); err != nil {
			t.Fatalf("team %s: %v", slug, err)
		}
	}
	if _, err := st.AsOperator().TestOnlyReversePayment(context.Background(), "pay-refunded", "refund-pay-refunded", "billing", 0); err != nil {
		t.Fatal(err)
	}
	memberships, err := st.AsOperator().AccountMemberships(context.Background(), u.Account.ID)
	if err != nil || len(memberships) != 4 {
		t.Fatalf("refund left %d memberships, want all 4 owned teams kept: %v", len(memberships), err)
	}
	for _, m := range memberships {
		if m.Role != store.RoleOwner {
			t.Fatalf("refund changed %s to %s, want owner", m.Team, m.Role)
		}
	}
	requireUnpaidTeamUsage(t, createOwnedTeam(t, st, u.Account.ID, "refund-five"), 4)
}

func TestConcurrentTeamCreationKeepsTheUnpaidTeamLimit(t *testing.T) {
	for name, open := range map[string]func(*testing.T) *store.Store{
		"sqlite":   func(t *testing.T) *store.Store { return storetest.OpenSQLite(t) },
		"postgres": storetest.OpenPostgres,
	} {
		t.Run(name, func(t *testing.T) {
			st := open(t)
			u := signIn(t, st, "s", "racer@example.com")
			const attempts = 8
			results := make(chan error, attempts)
			for i := range attempts {
				go func() {
					_, err := st.AsOperator().CreateAccountTeam(context.Background(), storetest.AccountAuthority(t, st, u.Account.ID), store.TeamInfo{Slug: store.Team(fmt.Sprintf("racer-%d", i))})
					results <- err
				}()
			}
			created := 0
			for range attempts {
				err := <-results
				if err == nil {
					created++
					continue
				}
				var failure *apierr.Error
				if !errors.As(err, &failure) || failure.Code != hosterr.LimitExceeded {
					t.Fatalf("concurrent creation: %v", err)
				}
			}
			if created != 2 {
				t.Fatalf("created %d teams beside the personal team, want 2", created)
			}
			memberships, err := st.AsOperator().AccountMemberships(context.Background(), u.Account.ID)
			if err != nil || len(memberships) != 3 {
				t.Fatalf("account owns %d teams, want 3: %v", len(memberships), err)
			}
		})
	}
}

func inviteAs(t *testing.T, st *store.Store, host store.SignInResult, team store.Team, email string, role store.Role) store.Invitation {
	t.Helper()
	inv, err := tenant(t, st, team).InviteForTest(t, context.Background(), host.Account.ID, email, role, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return inv
}

func acceptInvitation(t *testing.T, st *store.Store, accountID string, inv store.Invitation) error {
	t.Helper()
	_, err := st.AsOperator().AcceptAccountInvitation(context.Background(), storetest.AccountAuthority(t, st, accountID), inv.ID, store.SignUpConditions{})
	return err
}

func ownsThreeUnpaidTeams(t *testing.T, st *store.Store, sub, email string) store.SignInResult {
	t.Helper()
	u := signIn(t, st, sub, email)
	for _, slug := range []string{"two", "three"} {
		if err := createOwnedTeam(t, st, u.Account.ID, store.Team(sub+"-"+slug)); err != nil {
			t.Fatalf("team %s-%s: %v", sub, slug, err)
		}
	}
	return u
}

func TestOwnerInvitationRespectsTheUnpaidTeamLimit(t *testing.T) {
	st := storetest.Open(t)
	full := ownsThreeUnpaidTeams(t, st, "full", "full@example.com")
	host := signIn(t, st, "host", "host@example.com")
	owner := inviteAs(t, st, host, host.PersonalTeam, full.Account.Email, store.RoleOwner)
	requireUnpaidTeamLimit(t, acceptInvitation(t, st, full.Account.ID, owner))
	if _, err := tenant(t, st, host.PersonalTeam).MemberRole(context.Background(), full.Account.ID); !errors.Is(err, store.ErrNotMember) {
		t.Fatalf("refused owner invitation left a membership: %v", err)
	}
	if err := createOwnedTeam(t, st, host.Account.ID, "host-two"); err != nil {
		t.Fatal(err)
	}
	if err := acceptInvitation(t, st, full.Account.ID, inviteAs(t, st, host, "host-two", full.Account.Email, store.RoleEditor)); err != nil {
		t.Fatalf("editor invitation at the limit: %v", err)
	}
	payTeam(t, st, host.PersonalTeam, "pay-host")
	if err := acceptInvitation(t, st, full.Account.ID, owner); err != nil {
		t.Fatalf("owner invitation to a team that has since paid: %v", err)
	}
}

func TestPromotionToOwnerRespectsTheUnpaidTeamLimit(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	full := ownsThreeUnpaidTeams(t, st, "full", "full@example.com")
	host := signIn(t, st, "host", "host@example.com")
	if err := acceptInvitation(t, st, full.Account.ID, inviteAs(t, st, host, host.PersonalTeam, full.Account.Email, store.RoleReader)); err != nil {
		t.Fatal(err)
	}
	team := tenant(t, st, host.PersonalTeam)
	actor := membershipActor(t, st, host.Account.ID, host.PersonalTeam)
	if err := team.UpdateMembership(ctx, actor, store.MembershipUpdate{AccountID: full.Account.ID, Role: rolePointer(store.RoleEditor)}); err != nil {
		t.Fatalf("promotion to editor at the limit: %v", err)
	}
	refusal := unpaidTeamRefusal(t, team.UpdateMembership(ctx, actor, store.MembershipUpdate{AccountID: full.Account.ID, Role: rolePointer(store.RoleOwner)}))
	if !strings.Contains(refusal, "this member has reached it") || !strings.Contains(refusal, "they must own fewer than 3 unpaid teams") ||
		strings.Contains(refusal, "you own") || strings.Contains(refusal, "3 teams") {
		t.Fatalf("promotion refusal %q must name the member's limit without their usage", refusal)
	}
	if role, err := team.MemberRole(ctx, full.Account.ID); err != nil || role != store.RoleEditor {
		t.Fatalf("refused promotion left role %q: %v", role, err)
	}
	payTeam(t, st, host.PersonalTeam, "pay-host")
	if err := team.UpdateMembership(ctx, actor, store.MembershipUpdate{AccountID: full.Account.ID, Role: rolePointer(store.RoleOwner)}); err != nil {
		t.Fatalf("promotion to owner of a paid team at the limit: %v", err)
	}
}

func TestOwnerInvitationAndTeamCreationShareTheLastUnpaidSlot(t *testing.T) {
	for name, open := range map[string]func(*testing.T) *store.Store{
		"sqlite":   func(t *testing.T) *store.Store { return storetest.OpenSQLite(t) },
		"postgres": storetest.OpenPostgres,
	} {
		t.Run(name, func(t *testing.T) {
			st := open(t)
			u := signIn(t, st, "s", "racer@example.com")
			if err := createOwnedTeam(t, st, u.Account.ID, "racer-two"); err != nil {
				t.Fatal(err)
			}
			host := signIn(t, st, "host", "host@example.com")
			inv := inviteAs(t, st, host, host.PersonalTeam, u.Account.Email, store.RoleOwner)
			creator := storetest.AccountAuthority(t, st, u.Account.ID)
			acceptor := storetest.AccountAuthority(t, st, u.Account.ID)
			results := make(chan error, 2)
			go func() {
				_, err := st.AsOperator().CreateAccountTeam(context.Background(), creator, store.TeamInfo{Slug: "racer-three"})
				results <- err
			}()
			go func() {
				_, err := st.AsOperator().AcceptAccountInvitation(context.Background(), acceptor, inv.ID, store.SignUpConditions{})
				results <- err
			}()
			var refused int
			for range 2 {
				var failure *apierr.Error
				if err := <-results; errors.As(err, &failure) && failure.Code == hosterr.LimitExceeded {
					refused++
				} else if err != nil {
					t.Fatalf("racing for the last unpaid slot: %v", err)
				}
			}
			memberships, err := st.AsOperator().AccountMemberships(context.Background(), u.Account.ID)
			if err != nil || refused != 1 || len(memberships) != 3 {
				t.Fatalf("%d refused and account owns %d teams, want 1 refused and 3 teams: %v", refused, len(memberships), err)
			}
		})
	}
}

func TestOwnerInvitationToAMemberAtTheLimitReportsTheMembership(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	full := ownsThreeUnpaidTeams(t, st, "full", "full@example.com")
	host := signIn(t, st, "host", "host@example.com")
	if err := acceptInvitation(t, st, full.Account.ID, inviteAs(t, st, host, host.PersonalTeam, full.Account.Email, store.RoleEditor)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().ExecContext(ctx, storetest.Rebind(st, `INSERT INTO email_owners(email,account_id,updated_at) VALUES (?,?,1)`), "full-next@example.com", full.Account.ID); err != nil {
		t.Fatal(err)
	}
	owner := inviteAs(t, st, host, host.PersonalTeam, "full-next@example.com", store.RoleOwner)
	if err := acceptInvitation(t, st, full.Account.ID, owner); !errors.Is(err, store.ErrAlreadyMember) {
		t.Fatalf("owner invitation to an existing member at the limit = %v, want ErrAlreadyMember", err)
	}
}
