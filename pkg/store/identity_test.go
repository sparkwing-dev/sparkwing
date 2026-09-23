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

func googleProfile(sub, email string) store.SignInProfile {
	return store.SignInProfile{
		Provider: store.ProviderGoogle, Subject: sub, Email: email, EmailVerified: true, GivenName: "Pat",
	}
}

func signIn(t *testing.T, st *store.Store, sub, email string) store.SignInResult {
	t.Helper()
	res, err := st.ResolveSignIn(context.Background(), googleProfile(sub, email), time.Now())
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
	if _, err := st.ResolveSignIn(context.Background(), p, time.Now()); !errors.Is(err, store.ErrUnverifiedEmail) {
		t.Fatalf("ResolveSignIn = %v, want ErrUnverifiedEmail", err)
	}
}

func TestIdentityLastOwnerCannotLeaveAndAMemberKeepsTheirTeams(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	owner := signIn(t, st, "o", "owner@example.com")
	joiner := signIn(t, st, "j", "joiner@example.com")
	own := tenant(t, st, owner.PersonalTeam)
	inv, err := own.CreateInvitation(ctx, owner.Account.ID, "joiner@example.com", store.RoleOwner, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AcceptInvitation(ctx, joiner.Account.ID, inv.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := tenant(t, st, joiner.PersonalTeam).RemoveMember(ctx, joiner.Account.ID, joiner.Account.ID, time.Now()); !errors.Is(err, store.ErrLastOwner) {
		t.Fatalf("last owner leaving their space = %v, want ErrLastOwner", err)
	}
	if _, err := own.RemoveMember(ctx, joiner.Account.ID, joiner.Account.ID, time.Now()); err != nil {
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
	inv, err := tenant(t, st, owner.PersonalTeam).CreateInvitation(ctx, owner.Account.ID, "X@Example.com", store.RoleEditor, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AcceptInvitation(ctx, y.Account.ID, inv.ID, time.Now()); !errors.Is(err, store.ErrEmailMismatch) {
		t.Fatalf("y accepting x's invitation = %v, want ErrEmailMismatch", err)
	}
	team, err := st.AcceptInvitation(ctx, x.Account.ID, inv.ID, time.Now())
	if err != nil || team != owner.PersonalTeam {
		t.Fatalf("x accepting = %s, %v", team, err)
	}
	if _, err := st.AcceptInvitation(ctx, x.Account.ID, inv.ID, time.Now()); !errors.Is(err, store.ErrInvitationClosed) {
		t.Fatalf("second accept = %v, want ErrInvitationClosed", err)
	}
	role, err := tenant(t, st, owner.PersonalTeam).MemberRole(ctx, x.Account.ID)
	if err != nil || role != store.RoleEditor {
		t.Fatalf("x's role = %s, %v", role, err)
	}
}

func TestIdentityInvitationExpires(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	owner := signIn(t, st, "o", "owner@example.com")
	x := signIn(t, st, "x", "x@example.com")
	inv, err := tenant(t, st, owner.PersonalTeam).CreateInvitation(ctx, owner.Account.ID, "x@example.com", store.RoleReader, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got := inv.ExpiresAt.Sub(inv.CreatedAt); got != 7*24*time.Hour {
		t.Fatalf("invitation lives %s, want 7 days", got)
	}
	if _, err := st.AcceptInvitation(ctx, x.Account.ID, inv.ID, inv.ExpiresAt.Add(time.Second)); !errors.Is(err, store.ErrInvitationClosed) {
		t.Fatalf("accepting after expiry = %v, want ErrInvitationClosed", err)
	}
}

func TestIdentityRolesCannotEscalateOrOrphanATeam(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	owner := signIn(t, st, "o", "owner@example.com")
	ed := signIn(t, st, "e", "ed@example.com")
	tn := tenant(t, st, owner.PersonalTeam)
	inv, err := tn.CreateInvitation(ctx, owner.Account.ID, "ed@example.com", store.RoleEditor, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AcceptInvitation(ctx, ed.Account.ID, inv.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := tn.SetMemberRole(ctx, ed.Account.ID, ed.Account.ID, store.RoleOwner, time.Now()); !errors.Is(err, store.ErrRoleAboveOwn) {
		t.Fatalf("editor promoting self = %v, want ErrRoleAboveOwn", err)
	}
	if _, err := tn.CreateInvitation(ctx, ed.Account.ID, "z@example.com", store.RoleOwner, time.Now()); !errors.Is(err, store.ErrRoleAboveOwn) {
		t.Fatalf("editor inviting an owner = %v, want ErrRoleAboveOwn", err)
	}
	if _, err := tn.SetMemberRole(ctx, owner.Account.ID, owner.Account.ID, store.RoleReader, time.Now()); !errors.Is(err, store.ErrLastOwner) {
		t.Fatalf("last owner demoting self = %v, want ErrLastOwner", err)
	}
	if _, err := tn.RemoveMember(ctx, ed.Account.ID, owner.Account.ID, time.Now()); !errors.Is(err, store.ErrRoleAboveOwn) {
		t.Fatalf("editor removing the owner = %v, want ErrRoleAboveOwn", err)
	}
}

func TestIdentityTeamScopedWritesMissAnotherTeam(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	a := signIn(t, st, "a", "a-user@example.com")
	b := signIn(t, st, "b", "b-user@example.com")
	ta, tb := tenant(t, st, a.PersonalTeam), tenant(t, st, b.PersonalTeam)
	inv, err := tb.CreateInvitation(ctx, b.Account.ID, "someone@example.com", store.RoleReader, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := ta.DeleteInvitation(ctx, inv.ID, time.Now()); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("team a deleting team b's invitation = %v, want ErrNotFound", err)
	}
	_, tok, err := tb.CreateTokenWith(ctx, "agent:b", store.TokenKindRunner, []string{"nodes.claim"}, 0, time.Now(),
		store.TokenOptions{CreatedBy: b.Account.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := ta.RevokeRunnerToken(ctx, tok.Prefix, time.Now()); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("team a revoking team b's token = %v, want ErrNotFound", err)
	}
	if toks, err := ta.RunnerTokens(ctx, time.Now()); err != nil || len(toks) != 0 {
		t.Fatalf("team a lists %d runner tokens (%v), want 0", len(toks), err)
	}
	if toks, err := tb.RunnerTokens(ctx, time.Now()); err != nil || len(toks) != 1 || toks[0].CreatedBy != b.Account.ID {
		t.Fatalf("team b lists %+v (%v)", toks, err)
	}
	if _, err := ta.SetMemberRole(ctx, a.Account.ID, b.Account.ID, store.RoleReader, time.Now()); !errors.Is(err, store.ErrNotMember) {
		t.Fatalf("team a re-roling team b's owner = %v, want ErrNotMember", err)
	}
}

func TestIdentitySessionSwitchNamesTheTeamItLeaves(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	a := signIn(t, st, "a", "sw@example.com")
	raw, _, _, err := st.CreateAccountSession(ctx, a.Account, a.PersonalTeam, time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateTeam(ctx, a.Account.ID, "second", "Second", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := st.SwitchSessionTeam(ctx, raw, a.Account.ID, "not-its-team", "second"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("switch from the wrong team = %v, want ErrNotFound", err)
	}
	if err := st.SwitchSessionTeam(ctx, raw, a.Account.ID, a.PersonalTeam, "second"); err != nil {
		t.Fatal(err)
	}
	sess, err := st.LookupSession(raw, time.Now())
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
	if newcomer.Account.ID == alice.Account.ID || newcomer.Linked {
		t.Fatal("a new subject on Alice's old address linked into Alice's account")
	}
}

func TestIdentitySecondSubjectFromTheSameProviderGetsItsOwnAccount(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	first := signIn(t, st, "s-1", "shared@corp.example")
	second := signIn(t, st, "s-2", "shared@corp.example")
	if second.Account.ID == first.Account.ID || second.Linked || !second.NewAccount {
		t.Fatalf("second subject = %+v", second)
	}
	old, err := st.Account(ctx, first.Account.ID)
	if err != nil || old.EmailVerified {
		t.Fatalf("the first account still holds the address verified: %+v, %v", old, err)
	}
	owner := signIn(t, st, "s-o", "owner@corp.example")
	inv, err := tenant(t, st, owner.PersonalTeam).CreateInvitation(ctx, owner.Account.ID, "shared@corp.example", store.RoleReader, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AcceptInvitation(ctx, first.Account.ID, inv.ID, time.Now()); !errors.Is(err, store.ErrEmailMismatch) {
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
				googleProfile(fmt.Sprintf("s-%d", i), fmt.Sprintf("sam@host%d.example", i)), time.Now())
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
	for i := 2; i <= store.MaxCreatedTeams; i++ {
		if _, err := st.CreateTeam(ctx, u.Account.ID, store.Team(fmt.Sprintf("busy-team-%d", i)), "", time.Now()); err != nil {
			t.Fatalf("team %d: %v", i, err)
		}
	}
	if _, err := st.CreateTeam(ctx, u.Account.ID, "one-too-many", "", time.Now()); !errors.Is(err, store.ErrTeamLimit) {
		t.Fatalf("team past the cap = %v, want ErrTeamLimit", err)
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

func TestIdentityRunnerTokenCapHoldsUnderConcurrentMints(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	u := signIn(t, st, "s", "mint@example.com")
	tn := tenant(t, st, u.PersonalTeam)
	const tries = 3 * store.MaxRunnerTokensPerTeam
	errs := make(chan error, tries)
	for i := range tries {
		go func() {
			_, _, err := tn.CreateRunnerToken(ctx, fmt.Sprintf("agent:m%d", i), []string{"nodes.claim"}, u.Account.ID, time.Now())
			errs <- err
		}()
	}
	minted := 0
	for range tries {
		switch err := <-errs; {
		case err == nil:
			minted++
		case !errors.Is(err, store.ErrRunnerTokenLimit):
			t.Errorf("mint: %v", err)
		}
	}
	if minted != store.MaxRunnerTokensPerTeam {
		t.Fatalf("minted %d runner tokens, want exactly %d", minted, store.MaxRunnerTokensPerTeam)
	}
	toks, err := tn.RunnerTokens(ctx, time.Now())
	if err != nil || len(toks) != store.MaxRunnerTokensPerTeam {
		t.Fatalf("team holds %d tokens (%v)", len(toks), err)
	}
	if err := tn.RevokeRunnerToken(ctx, toks[0].Prefix, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := tn.CreateRunnerToken(ctx, "agent:again", []string{"nodes.claim"}, u.Account.ID, time.Now()); err != nil {
		t.Fatalf("mint after a revoke freed a slot: %v", err)
	}
}

func TestIdentityTeamTokensNeverCarryAdmin(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	u := signIn(t, st, "s", "adm@example.com")
	tn := tenant(t, st, u.PersonalTeam)
	if _, _, err := tn.CreateTokenWith(ctx, "ops", store.TokenKindUser, []string{"runs.read", "admin"}, 0, time.Now(),
		store.TokenOptions{}); !errors.Is(err, store.ErrAdminScopeOnTeamToken) {
		t.Fatalf("team mint with admin = %v, want ErrAdminScopeOnTeamToken", err)
	}
	if _, _, err := tn.CreateToken(ctx, "ops", store.TokenKindUser, []string{" admin "}, 0, time.Now()); !errors.Is(err, store.ErrAdminScopeOnTeamToken) {
		t.Fatalf("team mint with padded admin = %v, want ErrAdminScopeOnTeamToken", err)
	}
	if _, _, err := tn.CreateRunnerToken(ctx, "agent:x", []string{"admin"}, u.Account.ID, time.Now()); !errors.Is(err, store.ErrAdminScopeOnTeamToken) {
		t.Fatalf("runner mint with admin = %v, want ErrAdminScopeOnTeamToken", err)
	}
}

func TestIdentityOpenInvitationsAreCapped(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	u := signIn(t, st, "s", "host@example.com")
	tn := tenant(t, st, u.PersonalTeam)
	for i := range store.MaxOpenInvitations {
		if _, err := tn.CreateInvitation(ctx, u.Account.ID, fmt.Sprintf("guest%d@example.com", i), store.RoleReader, time.Now()); err != nil {
			t.Fatalf("invitation %d: %v", i, err)
		}
	}
	if _, err := tn.CreateInvitation(ctx, u.Account.ID, "one-more@example.com", store.RoleReader, time.Now()); !errors.Is(err, store.ErrInvitationLimit) {
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
		inv, err := tn.CreateInvitation(ctx, u.Account.ID, "victim@example.com", store.RoleReader, time.Now())
		if err != nil {
			t.Fatalf("invitation %d: %v", i, err)
		}
		if err := tn.DeleteInvitation(ctx, inv.ID, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tn.CreateInvitation(ctx, u.Account.ID, "victim@example.com", store.RoleReader, time.Now()); !errors.Is(err, store.ErrInvitationLimit) {
		t.Fatalf("invitation past the daily cap = %v, want ErrInvitationLimit", err)
	}
	if _, err := tn.CreateInvitation(ctx, u.Account.ID, "victim@example.com", store.RoleReader,
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
	inv, err := tn.CreateInvitation(ctx, owner.Account.ID, "x@example.com", store.RoleReader, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := tn.DeleteInvitation(ctx, inv.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AcceptInvitation(ctx, x.Account.ID, inv.ID, time.Now()); !errors.Is(err, store.ErrInvitationClosed) {
		t.Fatalf("accepting a withdrawn invitation = %v, want ErrInvitationClosed", err)
	}
	if err := tn.DeleteInvitation(ctx, inv.ID, time.Now()); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("withdrawing twice = %v, want ErrNotFound", err)
	}
}

func TestIdentityLeavingOrLosingEditorRevokesTheRunnerTokensAMemberMinted(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	owner := signIn(t, st, "o", "owner@example.com")
	ed := signIn(t, st, "e", "ed@example.com")
	tn := tenant(t, st, owner.PersonalTeam)
	inv, err := tn.CreateInvitation(ctx, owner.Account.ID, "ed@example.com", store.RoleEditor, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AcceptInvitation(ctx, ed.Account.ID, inv.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	mint := func(name, by string) string {
		t.Helper()
		_, tok, err := tn.CreateRunnerToken(ctx, name, []string{"nodes.claim"}, by, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		return tok.Prefix
	}
	live := func() map[string]bool {
		t.Helper()
		toks, err := tn.RunnerTokens(ctx, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]bool{}
		for _, tok := range toks {
			out[tok.Prefix] = true
		}
		return out
	}
	kept := mint("agent:owner", owner.Account.ID)
	first := mint("agent:ed-1", ed.Account.ID)

	if _, err := tn.SetMemberRole(ctx, owner.Account.ID, ed.Account.ID, store.RoleOwner, time.Now()); err != nil {
		t.Fatal(err)
	}
	if !live()[first] {
		t.Fatal("a promotion revoked the member's runner token")
	}
	if _, err := tn.SetMemberRole(ctx, owner.Account.ID, ed.Account.ID, store.RoleEditor, time.Now()); err != nil {
		t.Fatal(err)
	}
	revoked, err := tn.SetMemberRole(ctx, owner.Account.ID, ed.Account.ID, store.RoleReader, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(revoked) != 1 || revoked[0] != first || live()[first] {
		t.Fatalf("demotion to reader revoked %v, live %v; want only %s", revoked, live(), first)
	}

	if _, err := tn.SetMemberRole(ctx, owner.Account.ID, ed.Account.ID, store.RoleEditor, time.Now()); err != nil {
		t.Fatal(err)
	}
	second := mint("agent:ed-2", ed.Account.ID)
	revoked, err = tn.RemoveMember(ctx, owner.Account.ID, ed.Account.ID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(revoked) != 1 || revoked[0] != second || live()[second] {
		t.Fatalf("removal revoked %v, live %v; want only %s", revoked, live(), second)
	}
	if !live()[kept] {
		t.Fatal("removing a member revoked another member's runner token")
	}
}
