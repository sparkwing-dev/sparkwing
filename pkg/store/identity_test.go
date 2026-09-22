package store_test

import (
	"context"
	"errors"
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
	if err := tenant(t, st, joiner.PersonalTeam).RemoveMember(ctx, joiner.Account.ID, joiner.Account.ID); !errors.Is(err, store.ErrLastOwner) {
		t.Fatalf("last owner leaving their space = %v, want ErrLastOwner", err)
	}
	if err := own.RemoveMember(ctx, joiner.Account.ID, joiner.Account.ID); err != nil {
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
	if err := tn.SetMemberRole(ctx, ed.Account.ID, ed.Account.ID, store.RoleOwner); !errors.Is(err, store.ErrRoleAboveOwn) {
		t.Fatalf("editor promoting self = %v, want ErrRoleAboveOwn", err)
	}
	if _, err := tn.CreateInvitation(ctx, ed.Account.ID, "z@example.com", store.RoleOwner, time.Now()); !errors.Is(err, store.ErrRoleAboveOwn) {
		t.Fatalf("editor inviting an owner = %v, want ErrRoleAboveOwn", err)
	}
	if err := tn.SetMemberRole(ctx, owner.Account.ID, owner.Account.ID, store.RoleReader); !errors.Is(err, store.ErrLastOwner) {
		t.Fatalf("last owner demoting self = %v, want ErrLastOwner", err)
	}
	if err := tn.RemoveMember(ctx, ed.Account.ID, owner.Account.ID); !errors.Is(err, store.ErrRoleAboveOwn) {
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
	if err := ta.DeleteInvitation(ctx, inv.ID); !errors.Is(err, store.ErrNotFound) {
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
	if err := ta.SetMemberRole(ctx, a.Account.ID, b.Account.ID, store.RoleReader); !errors.Is(err, store.ErrNotMember) {
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
