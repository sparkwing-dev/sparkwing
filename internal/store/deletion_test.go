package store_test

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
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

func seedTeam(t *testing.T, st *store.Store, tn *store.Tenant, ownerID, runID string) string {
	t.Helper()
	ctx := context.Background()
	now := time.Now()
	if err := tn.TestOnlySeedTriggerWithRun(ctx,
		store.Trigger{ID: runID, Pipeline: "build", CreatedAt: now},
		store.Run{ID: runID, Pipeline: "build", Status: "pending", StartedAt: now}); err != nil {
		t.Fatalf("seed run: %v", err)
	}
	if err := teststore.SeedSecret(st, tn.Team(), store.Secret{Name: "API_KEY", Value: "v-" + runID}, now); err != nil {
		t.Fatalf("seed secret: %v", err)
	}
	if _, err := tn.InviteForTest(t, ctx, ownerID, "guest-"+runID+"@example.com", store.RoleReader, now); err != nil {
		t.Fatalf("seed invitation: %v", err)
	}
	return tn.MemberCapabilityForTest(t, ownerID, now).Capability.Prefix
}

func TestTeamDeletionClosesTheTeamAtOnceAndThePurgeLeavesNoRow(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	now := time.Now()
	owner := signIn(t, st, "o", "owner@example.com")
	member := signIn(t, st, "m", "member@example.com")
	if _, err := st.CreateAccountTeam(ctx, storetest.AccountAuthority(t, st, owner.Account.ID), store.TeamInfo{Slug: "acme", DisplayName: "Acme"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateAccountTeam(ctx, storetest.AccountAuthority(t, st, owner.Account.ID), store.TeamInfo{Slug: "keep", DisplayName: "Keep"}); err != nil {
		t.Fatal(err)
	}
	acme := tenant(t, st, "acme")
	inv, err := acme.InviteForTest(t, ctx, owner.Account.ID, "member@example.com", store.RoleEditor, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AcceptAccountInvitation(ctx, storetest.AccountAuthority(t, st, member.Account.ID), inv.ID); err != nil {
		t.Fatal(err)
	}
	memberSession, _, _, err := st.CreateAccountSession(ctx, member.Account, "acme", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	acmeToken := seedTeam(t, st, acme, owner.Account.ID, "run-acme")
	keepToken := seedTeam(t, st, tenant(t, st, "keep"), owner.Account.ID, "run-keep")
	// safety: billing rows are tenant-owned, so an expired checkout and a
	// released dispute hold go with the team rather than outliving it; open
	// ones refuse the deletion, see TestTeamDeletionWaitsForOpenCheckoutsAndDisputeHolds.
	if _, err := acme.TestOnlyOpenCreditCheckout(ctx, 50*store.MicroCreditsPerCent, now.Add(-2*time.Hour), time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := teststore.ForTeam(t, st, "acme").TestOnlyHoldForDispute(ctx, "dp_acme", "", "dispute opened", now); err != nil {
		t.Fatal(err)
	}
	if _, err := teststore.ForTeam(t, st, "acme").ReleaseCreditFreezes(ctx, "dp_acme", "", "", now); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"credit_checkouts", "credit_freezes"} {
		if n := countWhere(t, st, table, "team = 'acme'"); n == 0 {
			t.Fatalf("seeding left no %s row to purge", table)
		}
	}

	if _, _, err := acme.DeleteTeamForTest(t, ctx, member.Account.ID, now); err == nil {
		t.Fatalf("editor deleting the team = %v, want refusal", err)
	}
	del, revoked, err := acme.DeleteTeamForTest(t, ctx, owner.Account.ID, now)
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
	if n := countWhere(t, st, "capabilities", "team = 'acme' AND revoked_at IS NULL"); n != 0 {
		t.Fatalf("%d live tokens left in a team being deleted", n)
	}
	if n := countWhere(t, st, "invitations", "team = 'acme' AND withdrawn_at IS NULL AND accepted_at IS NULL"); n != 0 {
		t.Fatalf("%d open invitations left in a team being deleted", n)
	}
	if n := countWhere(t, st, "runs", "team = 'acme' AND cancel_requested_at IS NOT NULL"); n != 1 {
		t.Fatalf("queued run has no cancellation request: %d requested", n)
	}
	sess, err := st.LookupSession(t.Context(), memberSession)
	if err != nil || sess.Team != member.PersonalTeam {
		t.Fatalf("member session = %+v, %v; want moved to %s", sess, err, member.PersonalTeam)
	}
	if n := countWhere(t, st, "capabilities", fmt.Sprintf("prefix = '%s' AND revoked_at IS NULL", keepToken)); n != 1 {
		t.Fatal("deleting acme revoked another team's token")
	}

	if again, _, err := st.AsOperator().RequestTeamDeletion(ctx, "acme", now); err != nil || again.State != store.TeamDeletionPending {
		t.Fatalf("repeated request = %+v, %v", again, err)
	}
	pending, err := st.AsOperator().PendingTeamDeletions(ctx)
	if err != nil || len(pending) != 1 || pending[0].Team != "acme" {
		t.Fatalf("pending = %+v, %v", pending, err)
	}
	purge(t, st, "acme", now)
	for _, table := range store.AllTenantTablesForTest() {
		if n := countWhere(t, st, table, "team = 'acme'"); n != 0 {
			t.Errorf("%s still holds %d acme rows after the purge", table, n)
		}
	}
	if n := countWhere(t, st, "teams", "name = 'acme'"); n != 0 {
		t.Fatal("the purge left the team registered")
	}
	for _, table := range []string{"runs", "secrets", "capabilities", "memberships", "invitations"} {
		if n := countWhere(t, st, table, "team = 'keep'"); n == 0 {
			t.Errorf("the purge removed keep's %s", table)
		}
	}
	mine, err := st.TeamDeletionsRequestedBy(ctx, owner.Account.ID)
	if err != nil || len(mine) != 1 || mine[0].State != store.TeamDeletionDone || mine[0].FinishedAt == nil {
		t.Fatalf("requester's view = %+v, %v", mine, err)
	}
	if err := st.AsOperator().PurgeTeam(ctx, "acme", "p1", now, time.Hour); !errors.Is(err, store.ErrDeletionLeaseLost) {
		t.Fatalf("purging a finished deletion = %v, want ErrDeletionLeaseLost", err)
	}
}

func TestTeamPurgeRefusesATeamNobodyAskedToDelete(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	owner := signIn(t, st, "o", "owner@example.com")
	seedTeam(t, st, tenant(t, st, owner.PersonalTeam), owner.Account.ID, "run-1")
	if ok, err := st.AsOperator().ClaimTeamDeletion(ctx, owner.PersonalTeam, "p1", time.Now(), time.Minute); err != nil || ok {
		t.Fatalf("claiming a team nobody asked to delete = %v, %v; want false", ok, err)
	}
	if err := st.AsOperator().PurgeTeam(ctx, owner.PersonalTeam, "p1", time.Now(), time.Hour); !errors.Is(err, store.ErrDeletionLeaseLost) {
		t.Fatalf("PurgeTeam without a request = %v, want ErrDeletionLeaseLost", err)
	}
	if n := countWhere(t, st, "runs", fmt.Sprintf("team = '%s'", owner.PersonalTeam)); n != 1 {
		t.Fatalf("a refused purge removed runs: %d left", n)
	}
	if _, _, err := st.AsOperator().RequestTeamDeletion(ctx, store.DefaultTeam, time.Now()); !errors.Is(err, store.ErrInvalidInput) {
		t.Fatalf("deleting the default team = %v, want ErrInvalidInput", err)
	}
}

func TestTeamDeletionNanosecondTimestampsAndLeaseBoundariesProperty(t *testing.T) {
	random := rand.New(rand.NewPCG(71, 24))
	for sample := range 8 {
		t.Run(fmt.Sprint(sample), func(t *testing.T) {
			st := storetest.Open(t)
			ctx := t.Context()
			owner := signIn(t, st, "owner", "owner@example.com")
			team := owner.PersonalTeam
			now := time.Date(2040, 2, 3, 4, 5, 6, 1+random.IntN(999999000), time.FixedZone("offset", 7200))
			op := st.AsOperator()
			del, _, err := op.RequestTeamDeletion(ctx, team, now)
			if err != nil || !del.RequestedAt.Equal(now) {
				t.Fatalf("request time=%s, want %s: %v", del.RequestedAt, now, err)
			}
			if ok, err := op.ClaimTeamDeletion(ctx, team, "first", now, 7*time.Nanosecond); err != nil || !ok {
				t.Fatalf("initial lease=%v: %v", ok, err)
			}
			var requested, until int64
			if err := st.DB().QueryRowContext(ctx, storetest.Rebind(st, "SELECT requested_at,lease_until FROM team_deletions WHERE slug=?"), team).Scan(&requested, &until); err != nil || requested != now.UnixNano() || until != now.Add(7*time.Nanosecond).UnixNano() {
				t.Fatalf("stored request=%d until=%d: %v", requested, until, err)
			}
			if ok, err := op.ClaimTeamDeletion(ctx, team, "second", now.Add(6*time.Nanosecond), time.Nanosecond); err != nil || ok {
				t.Fatalf("unexpired lease stolen=%v: %v", ok, err)
			}
			if err := op.PurgeTeam(ctx, team, "first", now.Add(8*time.Nanosecond), time.Hour); !errors.Is(err, store.ErrDeletionLeaseLost) {
				t.Fatalf("expired lease purge: %v", err)
			}
			if ok, err := op.ClaimTeamDeletion(ctx, team, "second", now.Add(8*time.Nanosecond), time.Minute); err != nil || !ok {
				t.Fatalf("expired lease takeover=%v: %v", ok, err)
			}
			finished := now.Add(20 * time.Nanosecond)
			due := finished.Add(137 * time.Nanosecond)
			if _, err := st.DB().ExecContext(ctx, storetest.Rebind(st, "UPDATE accounts SET active_team=? WHERE id=?"), team, owner.Account.ID); err != nil {
				t.Fatal(err)
			}
			if err := op.PurgeTeam(ctx, team, "second", finished, 137*time.Nanosecond); err != nil {
				t.Fatal(err)
			}
			deletions, err := op.PendingTeamDeletions(ctx)
			if err != nil || len(deletions) != 1 || deletions[0].FinishedAt == nil || !deletions[0].FinishedAt.Equal(finished) || deletions[0].RecheckAt == nil || !deletions[0].RecheckAt.Equal(due) {
				t.Fatalf("purged timestamps=%+v: %v", deletions, err)
			}
			var finishedNS, recheckNS, accountNS int64
			if err := st.DB().QueryRowContext(ctx, storetest.Rebind(st, "SELECT finished_at,recheck_at FROM team_deletions WHERE slug=?"), team).Scan(&finishedNS, &recheckNS); err != nil || finishedNS != finished.UnixNano() || recheckNS != due.UnixNano() {
				t.Fatalf("stored finish=%d recheck=%d: %v", finishedNS, recheckNS, err)
			}
			if err := st.DB().QueryRowContext(ctx, storetest.Rebind(st, "SELECT updated_at FROM accounts WHERE id=?"), owner.Account.ID).Scan(&accountNS); err != nil || accountNS != finished.UnixNano() {
				t.Fatalf("stored account update=%d: %v", accountNS, err)
			}
			if ok, err := op.ClaimTeamDeletion(ctx, team, "recheck", due.Add(-time.Nanosecond), time.Minute); err != nil || ok {
				t.Fatalf("early recheck=%v: %v", ok, err)
			}
			if ok, err := op.ClaimTeamDeletion(ctx, team, "recheck", due, time.Minute); err != nil || !ok {
				t.Fatalf("due recheck=%v: %v", ok, err)
			}
			if err := op.FinishTeamRecheck(ctx, team, "recheck", due); err != nil {
				t.Fatal(err)
			}
			if pending, err := op.PendingTeamDeletions(ctx); err != nil || len(pending) != 0 {
				t.Fatalf("finished recheck pending=%+v: %v", pending, err)
			}
		})
	}
}

func TestTeamDeletionRefusesTheOwnersOnlyTeam(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	owner := signIn(t, st, "o", "owner@example.com")
	if _, _, err := tenant(t, st, owner.PersonalTeam).DeleteTeamForTest(t, ctx, owner.Account.ID, time.Now()); !errors.Is(err, store.ErrOnlyTeam) {
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
	inv, err := tenant(t, st, owner.PersonalTeam).InviteForTest(t, ctx, owner.Account.ID, "member@example.com", store.RoleReader, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AcceptAccountInvitation(ctx, storetest.AccountAuthority(t, st, member.Account.ID), inv.ID); err != nil {
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
	inv, err := shared.InviteForTest(t, ctx, owner.Account.ID, "leaver@example.com", store.RoleEditor, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AcceptAccountInvitation(ctx, storetest.AccountAuthority(t, st, leaver.Account.ID), inv.ID); err != nil {
		t.Fatal(err)
	}
	session, _, _, err := st.CreateAccountSession(ctx, leaver.Account, owner.PersonalTeam, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	cliTok := shared.MemberCapabilityForTest(t, leaver.Account.ID, now).Capability
	runCtx := store.FixtureWithCreatingPrincipal(ctx, "leaver@example.com")
	if err := shared.TestOnlySeedTriggerWithRun(runCtx,
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
	if _, err := st.LookupSession(t.Context(), session); err == nil {
		t.Fatal("the deleted account's session still authenticates")
	}
	for table, where := range map[string]string{
		"identities":   fmt.Sprintf("account_id = '%s'", leaver.Account.ID),
		"memberships":  fmt.Sprintf("account_id = '%s'", leaver.Account.ID),
		"invitations":  "email = 'leaver@example.com'",
		"runs":         "created_principal = 'leaver@example.com'",
		"capabilities": "subject_kind = 'account' AND subject_id = '" + leaver.Account.ID + "' AND revoked_at IS NULL",
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
		inv, err := tenant(t, st, owner.PersonalTeam).InviteForTest(t, ctx, owner.Account.ID, "target@example.com", store.RoleReader, now)
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
	owner := signIn(t, st, "x", "other-owner@example.com")
	other, err := tenant(t, st, owner.PersonalTeam).InviteForTest(t, ctx, owner.Account.ID, "someone@example.com", store.RoleReader, now)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := st.ClaimInvitationEmail(ctx, other.ID, now); err != nil || !ok {
		t.Fatalf("another address = %v, %v; want claimed", ok, err)
	}
	later, err := tenant(t, st, owner.PersonalTeam).InviteForTest(t, ctx, owner.Account.ID, "target@example.com", store.RoleReader, now)
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

func TestTeamDeletionWithdrawsSchedulesAndEffectsBeforePurge(t *testing.T) {
	st := storetest.Open(t)
	tn := tenantFor(t, st, "acme")
	seedTenantRun(t, tn, "withdraw-run", "build")
	for _, query := range []string{
		`INSERT INTO cron_schedules(id,repo_path,pipeline,cron,tz,overlap,catch_up_ns,armed_at,updated_at,cursor_at,team,repo_key) VALUES ('cron','repo','build','* * * * *','UTC','skip',0,1,1,1,'acme','repo')`,
		`INSERT INTO run_effects(team,run_id,effect,desired_state,desired_rev,next_at) VALUES ('acme','withdraw-run','github_check','running',1,1) ON CONFLICT(team,run_id,effect) DO NOTHING`,
	} {
		if _, err := st.DB().ExecContext(t.Context(), query); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := st.AsOperator().RequestTeamDeletion(t.Context(), "acme", time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"cron_schedules", "run_effects"} {
		if n := countWhere(t, st, table, `team='acme'`); n != 0 {
			t.Fatalf("%s retains %d pending rows", table, n)
		}
	}
}
