package store_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func googleProfile(sub, email string) store.SignInProfile {
	return store.SignInProfile{
		Provider: store.ProviderGoogle, Subject: sub, Email: email, EmailVerified: true, GivenName: "Pat",
	}
}

func signIn(t *testing.T, st *store.Store, sub, email string) store.SignInResult {
	t.Helper()
	res, err := st.ResolveSignIn(context.Background(), googleProfile(sub, email), store.SignUpConditions{}, time.Now())
	if err != nil {
		t.Fatalf("ResolveSignIn(%s): %v", email, err)
	}
	return res
}

func tenant(t *testing.T, st *store.Store, team store.Team) *store.Tenant {
	t.Helper()
	tn, err := st.ForTeam(context.Background(), team)
	if err != nil {
		t.Fatalf("ForTeam(%s): %v", team, err)
	}
	return tn
}

func membershipActor(t *testing.T, st *store.Store, id string, team store.Team) store.Actor {
	t.Helper()
	cipher, err := secrets.NewCipher(make([]byte, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	st.SetSessionKeyCipher(cipher)
	account, err := st.Account(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	raw, _, _, err := st.CreateAccountSession(t.Context(), account, team, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(raw))
	return store.Actor{Kind: store.ActorOperator, Authority: capability.AuthorityRef{Kind: capability.AuthoritySession, ID: hex.EncodeToString(digest[:])}}
}

func rolePointer(role store.Role) *store.Role { return &role }

func TestIdentitySignInCreatesOnePersonalSpace(t *testing.T) {
	st := storetest.Open(t)
	res := signIn(t, st, "s1", "Pat.Lee@Example.com")
	if !res.NewAccount || res.PersonalTeam != "pat-lee" || res.Account.ActiveTeam != "pat-lee" {
		t.Fatalf("first sign-in = %+v", res)
	}
	info, err := st.TeamInfo(context.Background(), "pat-lee")
	if err != nil || info.DisplayName != "Pat's space" || info.CreatedBy != res.Account.ID {
		t.Fatalf("team = %+v, %v", info, err)
	}
	again := signIn(t, st, "s1", "pat.lee@example.com")
	if again.NewAccount || again.PersonalTeam != "" || again.Account.ID != res.Account.ID {
		t.Fatalf("returning sign-in = %+v", again)
	}
}

func TestIdentitySignInRefusesAnUnverifiedEmail(t *testing.T) {
	st := storetest.Open(t)
	p := googleProfile("s1", "u@example.com")
	p.EmailVerified = false
	if _, err := st.ResolveSignIn(context.Background(), p, store.SignUpConditions{}, time.Now()); !errors.Is(err, store.ErrUnverifiedEmail) {
		t.Fatalf("ResolveSignIn = %v, want ErrUnverifiedEmail", err)
	}
}

func TestIdentityLastOwnerCannotLeaveAndAMemberKeepsTheirTeams(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	owner := signIn(t, st, "o", "owner@example.com")
	joiner := signIn(t, st, "j", "joiner@example.com")
	own := tenant(t, st, owner.PersonalTeam)
	inv, err := own.InviteForTest(t, ctx, owner.Account.ID, "joiner@example.com", store.RoleOwner, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AcceptAccountInvitation(ctx, storetest.AccountAuthority(t, st, joiner.Account.ID), inv.ID); err != nil {
		t.Fatal(err)
	}
	if err := tenant(t, st, joiner.PersonalTeam).UpdateMembership(ctx, membershipActor(t, st, joiner.Account.ID, tenant(t, st, joiner.PersonalTeam).Team()), store.MembershipUpdate{AccountID: joiner.Account.ID}); !errors.Is(err, store.ErrLastOwner) {
		t.Fatalf("last owner leaving their space = %v, want ErrLastOwner", err)
	}
	if err := own.UpdateMembership(ctx, membershipActor(t, st, joiner.Account.ID, own.Team()), store.MembershipUpdate{AccountID: joiner.Account.ID}); err != nil {
		t.Fatalf("leave: %v", err)
	}
	again := signIn(t, st, "j", "joiner@example.com")
	if again.PersonalTeam != "" || again.Account.ActiveTeam != joiner.PersonalTeam {
		t.Fatalf("a user who still has a team got %+v", again)
	}
}

func TestIdentityInvitationNeedsTheInvitedVerifiedEmail(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	owner := signIn(t, st, "o", "owner@example.com")
	x := signIn(t, st, "x", "x@example.com")
	y := signIn(t, st, "y", "y@example.com")
	inv, err := tenant(t, st, owner.PersonalTeam).InviteForTest(t, ctx, owner.Account.ID, "X@Example.com", store.RoleEditor, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AcceptAccountInvitation(ctx, storetest.AccountAuthority(t, st, y.Account.ID), inv.ID); !errors.Is(err, store.ErrEmailMismatch) {
		t.Fatalf("y accepting x's invitation = %v, want ErrEmailMismatch", err)
	}
	acc, err := st.AcceptAccountInvitation(ctx, storetest.AccountAuthority(t, st, x.Account.ID), inv.ID)
	if err != nil || acc.Team != owner.PersonalTeam || acc.Admitted {
		t.Fatalf("x accepting = %+v, %v", acc, err)
	}
	if _, err := st.AcceptAccountInvitation(ctx, storetest.AccountAuthority(t, st, x.Account.ID), inv.ID); !errors.Is(err, store.ErrInvitationClosed) {
		t.Fatalf("second accept = %v, want ErrInvitationClosed", err)
	}
	role, err := tenant(t, st, owner.PersonalTeam).MemberRole(ctx, x.Account.ID)
	if err != nil || role != store.RoleEditor {
		t.Fatalf("x's role = %s, %v", role, err)
	}
}

func TestIdentityInvitationLifetime(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	owner := signIn(t, st, "o", "owner@example.com")
	inv, err := tenant(t, st, owner.PersonalTeam).InviteForTest(t, ctx, owner.Account.ID, "x@example.com", store.RoleReader, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got := inv.ExpiresAt.Sub(inv.CreatedAt); got != 7*24*time.Hour {
		t.Fatalf("invitation lives %s, want 7 days", got)
	}
}

func TestIdentityRolesCannotEscalateOrOrphanATeam(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	owner := signIn(t, st, "o", "owner@example.com")
	ed := signIn(t, st, "e", "ed@example.com")
	tn := tenant(t, st, owner.PersonalTeam)
	inv, err := tn.InviteForTest(t, ctx, owner.Account.ID, "ed@example.com", store.RoleEditor, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AcceptAccountInvitation(ctx, storetest.AccountAuthority(t, st, ed.Account.ID), inv.ID); err != nil {
		t.Fatal(err)
	}
	if err := tn.UpdateMembership(ctx, membershipActor(t, st, ed.Account.ID, tn.Team()), store.MembershipUpdate{AccountID: ed.Account.ID, Role: rolePointer(store.RoleOwner)}); err == nil {
		t.Fatal("editor promoted itself")
	}
	if _, err := tn.InviteForTest(t, ctx, ed.Account.ID, "z@example.com", store.RoleOwner, time.Now()); err == nil {
		t.Fatalf("editor inviting an owner = %v, want refusal", err)
	}
	if err := tn.UpdateMembership(ctx, membershipActor(t, st, owner.Account.ID, tn.Team()), store.MembershipUpdate{AccountID: owner.Account.ID, Role: rolePointer(store.RoleReader)}); !errors.Is(err, store.ErrLastOwner) {
		t.Fatalf("last owner demoting self = %v, want ErrLastOwner", err)
	}
	if err := tn.UpdateMembership(ctx, membershipActor(t, st, ed.Account.ID, tn.Team()), store.MembershipUpdate{AccountID: owner.Account.ID}); err == nil {
		t.Fatal("editor removed the owner")
	}
	if err := tn.UpdateMembership(ctx, membershipActor(t, st, ed.Account.ID, tn.Team()), store.MembershipUpdate{AccountID: owner.Account.ID, Role: rolePointer(store.RoleReader)}); err == nil {
		t.Fatal("editor demoted the owner")
	}
	if err := tn.UpdateMembership(ctx, membershipActor(t, st, owner.Account.ID, tn.Team()), store.MembershipUpdate{AccountID: ed.Account.ID, Role: rolePointer(store.Role("admin"))}); !errors.Is(err, store.ErrInvalidInput) {
		t.Fatalf("owner granting an unknown role = %v, want ErrInvalidInput", err)
	}
	if _, err := tn.InviteForTest(t, ctx, owner.Account.ID, "z@example.com", store.Role("admin"), time.Now()); !errors.Is(err, store.ErrInvalidInput) {
		t.Fatalf("owner inviting at an unknown role = %v, want ErrInvalidInput", err)
	}
}

// A row holding an unknown role grants nothing, even over another unknown role.
func TestIdentityUnknownRolesCarryNoAuthority(t *testing.T) {
	for _, pair := range [][2]store.Role{{"", ""}, {"admin", "admin"}, {"admin", ""}} {
		if pair[0].AtLeast(pair[1]) {
			t.Errorf("Role(%q).AtLeast(%q) = true", pair[0], pair[1])
		}
	}
}

func TestIdentityTeamScopedWritesMissAnotherTeam(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	a := signIn(t, st, "a", "a-user@example.com")
	b := signIn(t, st, "b", "b-user@example.com")
	ta, tb := tenant(t, st, a.PersonalTeam), tenant(t, st, b.PersonalTeam)
	inv, err := tb.InviteForTest(t, ctx, b.Account.ID, "someone@example.com", store.RoleReader, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := ta.WithdrawInvitationForTest(t, ctx, inv.ID, time.Now()); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("team a deleting team b's invitation = %v, want ErrNotFound", err)
	}
	if err := ta.UpdateMembership(ctx, membershipActor(t, st, a.Account.ID, ta.Team()), store.MembershipUpdate{AccountID: b.Account.ID, Role: rolePointer(store.RoleReader)}); !errors.Is(err, store.ErrNotMember) {
		t.Fatalf("team a re-roling team b's owner = %v, want ErrNotMember", err)
	}
}

func TestIdentitySessionSwitchNamesTheTeamItLeaves(t *testing.T) {
	st := storetest.Open(t)
	cipher, err := secrets.NewCipher(make([]byte, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	st.SetSessionKeyCipher(cipher)
	ctx := context.Background()
	a := signIn(t, st, "a", "sw@example.com")
	raw, _, _, err := st.CreateAccountSession(ctx, a.Account, a.PersonalTeam, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateAccountTeam(ctx, storetest.AccountAuthority(t, st, a.Account.ID), store.TeamInfo{Slug: "second", DisplayName: "Second"}); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(raw))
	principal, err := st.ResolveAuthority(ctx, capability.AuthorityRef{Kind: capability.AuthoritySession, ID: hex.EncodeToString(digest[:])})
	if err != nil {
		t.Fatal(err)
	}
	route, _ := capability.LookupRoute("POST", "/api/v1/me/active-team")
	authorized := capability.Authorized{Principal: principal, Access: route.Access[0]}
	authorized.Principal.Capability.Team = "not-its-team"
	if err := st.SelectAccountTeam(ctx, authorized, "second"); err == nil {
		t.Fatal("switch from the wrong team succeeded")
	}
	authorized.Principal = principal
	if err := st.SelectAccountTeam(ctx, authorized, "second"); err != nil {
		t.Fatal(err)
	}
	sess, err := st.LookupSession(t.Context(), raw)
	if err != nil || sess.Team != "second" || sess.AccountID != a.Account.ID {
		t.Fatalf("session = %+v, %v", sess, err)
	}
}

func TestIdentityUserWithNoTeamGetsAFreshSpace(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	first := signIn(t, st, "n", "nomad@example.com")
	if _, err := st.DB().ExecContext(ctx, storetest.Rebind(st,
		`DELETE FROM memberships WHERE account_id = ?`), first.Account.ID); err != nil {
		t.Fatal(err)
	}
	again := signIn(t, st, "n", "nomad@example.com")
	if again.PersonalTeam != "nomad-2" || again.Account.ActiveTeam != "nomad-2" {
		t.Fatalf("sign-in with no team = %+v", again)
	}
}

func TestIdentityRecycledEmailNeverLinksIntoTheOldAccount(t *testing.T) {
	st := storetest.Open(t)
	alice := signIn(t, st, "s-alice", "alice@corp.example")
	moved := signIn(t, st, "s-alice", "alice.smith@corp.example")
	if moved.Account.ID != alice.Account.ID || moved.Account.Email != "alice.smith@corp.example" {
		t.Fatalf("a returning identity's address did not follow the provider: %+v", moved.Account)
	}
	newcomer := signIn(t, st, "s-newcomer", "alice@corp.example")
	if newcomer.Account.ID == alice.Account.ID || !newcomer.NewAccount {
		t.Fatal("a new subject on Alice's old address linked into Alice's account")
	}
}

func TestIdentitySecondSubjectFromTheSameProviderGetsItsOwnAccount(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	first := signIn(t, st, "s-1", "shared@corp.example")
	second := signIn(t, st, "s-2", "shared@corp.example")
	if second.Account.ID == first.Account.ID || !second.NewAccount {
		t.Fatalf("second subject = %+v", second)
	}
	old, err := st.Account(ctx, first.Account.ID)
	if err != nil || old.EmailVerified {
		t.Fatalf("the first account still holds the address verified: %+v, %v", old, err)
	}
	owner := signIn(t, st, "s-o", "owner@corp.example")
	inv, err := tenant(t, st, owner.PersonalTeam).InviteForTest(t, ctx, owner.Account.ID, "shared@corp.example", store.RoleReader, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AcceptAccountInvitation(ctx, storetest.AccountAuthority(t, st, first.Account.ID), inv.ID); !errors.Is(err, store.ErrEmailMismatch) {
		t.Fatalf("the account that lost the address accepted its invitation: %v", err)
	}
}

func TestIdentityConcurrentFirstSignInsShareALocalPart(t *testing.T) {
	st := storetest.Open(t)
	const n = 8
	errs := make(chan error, n)
	slugs := make(chan store.Team, n)
	for i := range n {
		go func() {
			res, err := st.ResolveSignIn(context.Background(),
				googleProfile(fmt.Sprintf("s-%d", i), fmt.Sprintf("sam@host%d.example", i)), store.SignUpConditions{}, time.Now())
			errs <- err
			slugs <- res.PersonalTeam
		}()
	}
	seen := map[store.Team]bool{}
	for range n {
		if err := <-errs; err != nil {
			t.Errorf("concurrent sign-in: %v", err)
		}
		slug := <-slugs
		if seen[slug] {
			t.Errorf("slug %s handed out twice", slug)
		}
		seen[slug] = true
	}
}

func TestIdentityTeamCreationIsCapped(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	u := signIn(t, st, "s", "busy@example.com")
	for i := 2; i <= 3; i++ {
		if _, err := st.CreateAccountTeam(ctx, storetest.AccountAuthority(t, st, u.Account.ID), store.TeamInfo{Slug: store.Team(fmt.Sprintf("busy-team-%d", i)), DisplayName: ""}); err != nil {
			t.Fatalf("team %d: %v", i, err)
		}
	}
	_, err := st.CreateAccountTeam(ctx, storetest.AccountAuthority(t, st, u.Account.ID), store.TeamInfo{Slug: "one-too-many", DisplayName: ""})
	if !errors.Is(err, store.ErrTeamLimit) {
		t.Fatalf("fourth team = %v, want ErrTeamLimit", err)
	}
	if !strings.Contains(err.Error(), "at most 3 teams") {
		t.Fatalf("refusal = %q, want it to name the cap", err)
	}
}

func TestIdentityReservedSlugs(t *testing.T) {
	for _, slug := range []string{"app", "login", "auth", "sparkwing", "cache", "logs", "demo", "demo-acme", "default"} {
		if err := store.ValidateSlug(slug); !errors.Is(err, store.ErrInvalidSlug) {
			t.Errorf("ValidateSlug(%q) = %v, want ErrInvalidSlug", slug, err)
		}
	}
	st := storetest.Open(t)
	if res := signIn(t, st, "s", "demo-user@example.com"); store.ValidateSlug(string(res.PersonalTeam)) != nil {
		t.Fatalf("personal slug %q is itself reserved", res.PersonalTeam)
	}
}

func TestIdentityOpenInvitationsAreCapped(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	u := signIn(t, st, "s", "host@example.com")
	tn := tenant(t, st, u.PersonalTeam)
	for i := range store.MaxOpenInvitations {
		if _, err := tn.InviteForTest(t, ctx, u.Account.ID, fmt.Sprintf("guest%d@example.com", i), store.RoleReader, time.Now()); err != nil {
			t.Fatalf("invitation %d: %v", i, err)
		}
	}
	if _, err := tn.InviteForTest(t, ctx, u.Account.ID, "one-more@example.com", store.RoleReader, time.Now()); !errors.Is(err, store.ErrInvitationLimit) {
		t.Fatalf("invitation past the open cap = %v, want ErrInvitationLimit", err)
	}
}

// Withdrawing keeps the row, so create-and-withdraw cannot mail an
// unbounded number of invitations in a day.
func TestIdentityDailyInvitationsAreCappedThroughWithdrawals(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	u := signIn(t, st, "s", "host@example.com")
	tn := tenant(t, st, u.PersonalTeam)
	for i := range store.MaxInvitationsPerDay {
		inv, err := tn.InviteForTest(t, ctx, u.Account.ID, "victim@example.com", store.RoleReader, time.Now())
		if err != nil {
			t.Fatalf("invitation %d: %v", i, err)
		}
		if err := tn.WithdrawInvitationForTest(t, ctx, inv.ID, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tn.InviteForTest(t, ctx, u.Account.ID, "victim@example.com", store.RoleReader, time.Now()); !errors.Is(err, store.ErrInvitationLimit) {
		t.Fatalf("invitation past the daily cap = %v, want ErrInvitationLimit", err)
	}
	if _, err := tn.InviteForTest(t, ctx, u.Account.ID, "victim@example.com", store.RoleReader,
		time.Now().Add(25*time.Hour)); err != nil {
		t.Fatalf("invitation a day later: %v", err)
	}
}

func TestIdentityWithdrawnInvitationCannotBeAccepted(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	owner := signIn(t, st, "o", "owner@example.com")
	x := signIn(t, st, "x", "x@example.com")
	tn := tenant(t, st, owner.PersonalTeam)
	inv, err := tn.InviteForTest(t, ctx, owner.Account.ID, "x@example.com", store.RoleReader, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := tn.WithdrawInvitationForTest(t, ctx, inv.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AcceptAccountInvitation(ctx, storetest.AccountAuthority(t, st, x.Account.ID), inv.ID); !errors.Is(err, store.ErrInvitationClosed) {
		t.Fatalf("accepting a withdrawn invitation = %v, want ErrInvitationClosed", err)
	}
	if err := tn.WithdrawInvitationForTest(t, ctx, inv.ID, time.Now()); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("withdrawing twice = %v, want ErrNotFound", err)
	}
}
