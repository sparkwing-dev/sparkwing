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

func countWhere(t *testing.T, st *store.Store, table, where string) int {
	t.Helper()
	var n int
	q := fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE %s`, table, where)
	if err := st.DB().QueryRowContext(context.Background(), q).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

// seedTeam writes a pending run with its trigger, a secret, an open
// invitation and a runner token into team, so a purge has rows in several
// tables to find.
func seedTeam(t *testing.T, st *store.Store, tn *store.Tenant, ownerID, runID string) string {
	t.Helper()
	ctx := context.Background()
	now := time.Now()
	if err := tn.CreateTriggerWithRun(ctx,
		store.Trigger{ID: runID, Pipeline: "build", CreatedAt: now},
		store.Run{ID: runID, Pipeline: "build", Status: "pending", StartedAt: now}); err != nil {
		t.Fatalf("seed run: %v", err)
	}
	if err := tn.CreateOrReplaceSecret(store.Secret{Name: "API_KEY", Value: "v-" + runID}, now); err != nil {
		t.Fatalf("seed secret: %v", err)
	}
	if _, err := tn.CreateInvitation(ctx, ownerID, "guest-"+runID+"@example.com", store.RoleReader, now); err != nil {
		t.Fatalf("seed invitation: %v", err)
	}
	_, tok, err := tn.CreateRunnerToken(ctx, "agent:"+runID, []string{"nodes.claim"}, ownerID, now)
	if err != nil {
		t.Fatalf("seed token: %v", err)
	}
	return tok.Prefix
}

func TestTeamDeletionClosesTheTeamAtOnceAndThePurgeLeavesNoRow(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	now := time.Now()
	owner := signIn(t, st, "o", "owner@example.com")
	member := signIn(t, st, "m", "member@example.com")
	if _, err := st.CreateTeam(ctx, owner.Account.ID, "acme", "Acme", now); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateTeam(ctx, owner.Account.ID, "keep", "Keep", now); err != nil {
		t.Fatal(err)
	}
	acme := tenant(t, st, "acme")
	inv, err := acme.CreateInvitation(ctx, owner.Account.ID, "member@example.com", store.RoleEditor, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AcceptInvitation(ctx, member.Account.ID, inv.ID, now); err != nil {
		t.Fatal(err)
	}
	memberSession, _, _, err := st.CreateAccountSession(ctx, member.Account, "acme", time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	acmeToken := seedTeam(t, st, acme, owner.Account.ID, "run-acme")
	keepToken := seedTeam(t, st, tenant(t, st, "keep"), owner.Account.ID, "run-keep")

	if _, _, err := acme.RequestDeletion(ctx, member.Account.ID, now); !errors.Is(err, store.ErrRoleAboveOwn) {
		t.Fatalf("editor deleting the team = %v, want ErrRoleAboveOwn", err)
	}
	del, revoked, err := acme.RequestDeletion(ctx, owner.Account.ID, now)
	if err != nil {
		t.Fatalf("RequestDeletion: %v", err)
	}
	if del.State != store.TeamDeletionPending || del.Team != "acme" {
		t.Fatalf("deletion = %+v", del)
	}
	if len(revoked) != 1 || revoked[0] != acmeToken {
		t.Fatalf("revoked = %v, want [%s]", revoked, acmeToken)
	}

	if _, err := st.ForTeam(ctx, "acme"); !errors.Is(err, store.ErrUnknownTeam) {
		t.Fatalf("ForTeam on a team being deleted = %v, want ErrUnknownTeam", err)
	}
	if n := countWhere(t, st, "memberships", "team = 'acme'"); n != 0 {
		t.Fatalf("%d memberships left in a team being deleted", n)
	}
	if n := countWhere(t, st, "tokens", "team = 'acme' AND revoked_at IS NULL"); n != 0 {
		t.Fatalf("%d live tokens left in a team being deleted", n)
	}
	if n := countWhere(t, st, "invitations", "team = 'acme' AND withdrawn_at IS NULL AND accepted_at IS NULL"); n != 0 {
		t.Fatalf("%d open invitations left in a team being deleted", n)
	}
	if n := countWhere(t, st, "runs", "team = 'acme' AND status = 'cancelled'"); n != 1 {
		t.Fatalf("queued run not cancelled: %d cancelled", n)
	}
	sess, err := st.LookupSession(memberSession, now)
	if err != nil || sess.Team != member.PersonalTeam {
		t.Fatalf("member session = %+v, %v; want moved to %s", sess, err, member.PersonalTeam)
	}
	// Control: the other team's token, run and member rows are untouched.
	if n := countWhere(t, st, "tokens", fmt.Sprintf("prefix = '%s' AND revoked_at IS NULL", keepToken)); n != 1 {
		t.Fatal("deleting acme revoked another team's token")
	}

	if again, _, err := st.AsOperator().RequestTeamDeletion(ctx, "acme", now); err != nil || again.State != store.TeamDeletionPending {
		t.Fatalf("repeated request = %+v, %v", again, err)
	}
	pending, err := st.AsOperator().PendingTeamDeletions(ctx)
	if err != nil || len(pending) != 1 || pending[0].Team != "acme" {
		t.Fatalf("pending = %+v, %v", pending, err)
	}
	ids, err := st.AsOperator().TeamRunIDs(ctx, "acme")
	if err != nil || len(ids) != 1 || ids[0] != "run-acme" {
		t.Fatalf("TeamRunIDs = %v, %v", ids, err)
	}

	if err := st.AsOperator().PurgeTeam(ctx, "acme", now); err != nil {
		t.Fatalf("PurgeTeam: %v", err)
	}
	for _, table := range store.AllTenantTablesForTest() {
		if n := countWhere(t, st, table, "team = 'acme'"); n != 0 {
			t.Errorf("%s still holds %d acme rows after the purge", table, n)
		}
	}
	if n := countWhere(t, st, "teams", "name = 'acme'"); n != 0 {
		t.Fatal("the purge left the team registered")
	}
	for _, table := range []string{"runs", "secrets", "tokens", "memberships", "invitations"} {
		if n := countWhere(t, st, table, "team = 'keep'"); n == 0 {
			t.Errorf("the purge removed keep's %s", table)
		}
	}
	mine, err := st.TeamDeletionsRequestedBy(ctx, owner.Account.ID)
	if err != nil || len(mine) != 1 || mine[0].State != store.TeamDeletionDone || mine[0].FinishedAt == nil {
		t.Fatalf("requester's view = %+v, %v", mine, err)
	}
	if err := st.AsOperator().PurgeTeam(ctx, "acme", now); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("purging a finished deletion = %v, want ErrNotFound", err)
	}
	if _, err := st.CreateTeam(ctx, member.Account.ID, "acme", "", now); err != nil {
		t.Fatalf("a deleted slug is free again: %v", err)
	}
}

func TestTeamPurgeRefusesATeamNobodyAskedToDelete(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	owner := signIn(t, st, "o", "owner@example.com")
	seedTeam(t, st, tenant(t, st, owner.PersonalTeam), owner.Account.ID, "run-1")
	if err := st.AsOperator().PurgeTeam(ctx, owner.PersonalTeam, time.Now()); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("PurgeTeam without a request = %v, want ErrNotFound", err)
	}
	if n := countWhere(t, st, "runs", fmt.Sprintf("team = '%s'", owner.PersonalTeam)); n != 1 {
		t.Fatalf("a refused purge removed runs: %d left", n)
	}
	if _, _, err := st.AsOperator().RequestTeamDeletion(ctx, store.DefaultTeam, time.Now()); !errors.Is(err, store.ErrInvalidInput) {
		t.Fatalf("deleting the default team = %v, want ErrInvalidInput", err)
	}
}

func TestTeamDeletionRefusesTheOwnersOnlyTeam(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	owner := signIn(t, st, "o", "owner@example.com")
	if _, _, err := tenant(t, st, owner.PersonalTeam).RequestDeletion(ctx, owner.Account.ID, time.Now()); !errors.Is(err, store.ErrOnlyTeam) {
		t.Fatalf("deleting the only team = %v, want ErrOnlyTeam", err)
	}
	if _, err := st.ForTeam(ctx, owner.PersonalTeam); err != nil {
		t.Fatalf("a refused deletion closed the team: %v", err)
	}
}

func TestAccountDeletionRefusesALastOwnerWithMembersAndNamesTheTeams(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	now := time.Now()
	owner := signIn(t, st, "o", "owner@example.com")
	member := signIn(t, st, "m", "member@example.com")
	inv, err := tenant(t, st, owner.PersonalTeam).CreateInvitation(ctx, owner.Account.ID, "member@example.com", store.RoleReader, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AcceptInvitation(ctx, member.Account.ID, inv.ID, now); err != nil {
		t.Fatal(err)
	}
	_, err = st.DeleteAccount(ctx, owner.Account.ID, now)
	var lastOwner *store.LastOwnerError
	if !errors.As(err, &lastOwner) || len(lastOwner.Teams) != 1 || lastOwner.Teams[0].Slug != owner.PersonalTeam {
		t.Fatalf("DeleteAccount = %v, want LastOwnerError naming %s", err, owner.PersonalTeam)
	}
	if _, err := st.Account(ctx, owner.Account.ID); err != nil {
		t.Fatalf("a refused deletion removed the account: %v", err)
	}
}

func TestAccountDeletionRemovesTheHumanAndKeepsTheTeamsRuns(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	now := time.Now()
	owner := signIn(t, st, "o", "owner@example.com")
	leaver := signIn(t, st, "l", "leaver@example.com")
	shared := tenant(t, st, owner.PersonalTeam)
	inv, err := shared.CreateInvitation(ctx, owner.Account.ID, "leaver@example.com", store.RoleEditor, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AcceptInvitation(ctx, leaver.Account.ID, inv.ID, now); err != nil {
		t.Fatal(err)
	}
	session, _, _, err := st.CreateAccountSession(ctx, leaver.Account, owner.PersonalTeam, time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	_, cliTok, err := shared.CreateCLIToken(ctx, "leaver@example.com", []string{"runs.read"}, leaver.Account.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	runCtx := store.WithCreatingPrincipal(ctx, "leaver@example.com")
	if err := shared.CreateTriggerWithRun(runCtx,
		store.Trigger{ID: "run-l", Pipeline: "build", CreatedAt: now, TriggerUser: "leaver@example.com"},
		store.Run{ID: "run-l", Pipeline: "build", Status: "pending", StartedAt: now}); err != nil {
		t.Fatal(err)
	}

	res, err := st.DeleteAccount(ctx, leaver.Account.ID, now)
	if err != nil {
		t.Fatalf("DeleteAccount: %v", err)
	}
	if len(res.DeletedTeams) != 1 || res.DeletedTeams[0] != leaver.PersonalTeam {
		t.Fatalf("deleted teams = %v, want the leaver's own space", res.DeletedTeams)
	}
	if !containsString(res.RevokedPrefixes, cliTok.Prefix) {
		t.Fatalf("revoked = %v, want the CLI token %s", res.RevokedPrefixes, cliTok.Prefix)
	}
	if _, err := st.Account(ctx, leaver.Account.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("account after deletion = %v, want ErrNotFound", err)
	}
	if _, err := st.LookupSession(session, now); err == nil {
		t.Fatal("the deleted account's session still authenticates")
	}
	for table, where := range map[string]string{
		"identities":  fmt.Sprintf("account_id = '%s'", leaver.Account.ID),
		"memberships": fmt.Sprintf("account_id = '%s'", leaver.Account.ID),
		"invitations": "email = 'leaver@example.com'",
		"runs":        "created_principal = 'leaver@example.com'",
		"triggers":    "trigger_user = 'leaver@example.com'",
		"tokens":      "principal = 'leaver@example.com'",
	} {
		if n := countWhere(t, st, table, where); n != 0 {
			t.Errorf("%s still names the deleted account (%d rows where %s)", table, n, where)
		}
	}
	if n := countWhere(t, st, "runs", "id = 'run-l' AND created_principal = '"+store.DeletedUserLabel+"'"); n != 1 {
		t.Fatal("the run the account started left the team or kept no label")
	}
	if _, err := st.ForTeam(ctx, leaver.PersonalTeam); !errors.Is(err, store.ErrUnknownTeam) {
		t.Fatalf("the deleted account's own space = %v, want it closed", err)
	}
	// Control: the owner and their team are untouched.
	if role, err := shared.MemberRole(ctx, owner.Account.ID); err != nil || role != store.RoleOwner {
		t.Fatalf("owner after another account's deletion = %s, %v", role, err)
	}
	again := signIn(t, st, "l", "leaver@example.com")
	if !again.NewAccount || again.Account.ID == leaver.Account.ID {
		t.Fatalf("signing in after deletion = %+v, want a fresh account", again)
	}
}

func TestInvitationEmailsToOneAddressAreCappedAcrossTeams(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	now := time.Now()
	var claimed int
	for i := range store.MaxInvitationEmailsPerDay + 1 {
		owner := signIn(t, st, fmt.Sprintf("o%d", i), fmt.Sprintf("owner%d@example.com", i))
		inv, err := tenant(t, st, owner.PersonalTeam).CreateInvitation(ctx, owner.Account.ID, "target@example.com", store.RoleReader, now)
		if err != nil {
			t.Fatal(err)
		}
		ok, err := st.ClaimInvitationEmail(ctx, inv.ID, now)
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			claimed++
		}
		if i == 0 {
			if twice, err := st.ClaimInvitationEmail(ctx, inv.ID, now); err != nil || twice {
				t.Fatalf("claiming one invitation twice = %v, %v; want false", twice, err)
			}
		}
	}
	if claimed != store.MaxInvitationEmailsPerDay {
		t.Fatalf("claimed %d emails to one address, want %d", claimed, store.MaxInvitationEmailsPerDay)
	}
	// Control: another address has its own allowance, and a day later the
	// first one does too.
	owner := signIn(t, st, "x", "other-owner@example.com")
	other, err := tenant(t, st, owner.PersonalTeam).CreateInvitation(ctx, owner.Account.ID, "someone@example.com", store.RoleReader, now)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := st.ClaimInvitationEmail(ctx, other.ID, now); err != nil || !ok {
		t.Fatalf("another address = %v, %v; want claimed", ok, err)
	}
	later, err := tenant(t, st, owner.PersonalTeam).CreateInvitation(ctx, owner.Account.ID, "target@example.com", store.RoleReader, now)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := st.ClaimInvitationEmail(ctx, later.ID, now.Add(25*time.Hour)); err != nil || !ok {
		t.Fatalf("the same address a day later = %v, %v; want claimed", ok, err)
	}
}

func TestAccountByEmailPrefersTheVerifiedHolder(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	acct := signIn(t, st, "a", "Pat@Example.com")
	got, err := st.AccountByEmail(ctx, "pat@example.com")
	if err != nil || got.ID != acct.Account.ID {
		t.Fatalf("AccountByEmail = %+v, %v", got, err)
	}
	if _, err := st.AccountByEmail(ctx, "nobody@example.com"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown address = %v, want ErrNotFound", err)
	}
}
