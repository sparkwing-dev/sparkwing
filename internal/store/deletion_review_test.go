package store_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runstate"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func deleteAndPurge(t *testing.T, st *store.Store, team store.Team, now time.Time) {
	t.Helper()
	ctx := context.Background()
	st.SetTestClock(t, func() time.Time { return now })
	if _, _, err := st.AsOperator().RequestTeamDeletion(ctx, team); err != nil {
		t.Fatalf("request deletion of %s: %v", team, err)
	}
	purge(t, st, team, now)
}

// A team slug is never registered twice, so a new team cannot inherit what
// a credential of the deleted one writes under it later.
func TestDeletedSlugIsNeverRegisteredAgain(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	now := time.Now()
	owner := signIn(t, st, "o", "owner@example.com")
	other := signIn(t, st, "x", "other@example.com")
	if _, err := st.AsOperator().CreateAccountTeam(ctx, storetest.AccountAuthority(t, st, owner.Account.ID), store.TeamInfo{Slug: "acme", DisplayName: ""}); err != nil {
		t.Fatal(err)
	}
	deleteAndPurge(t, st, "acme", now)
	for _, at := range []time.Time{now.Add(time.Hour), now.Add(365 * 24 * time.Hour)} {
		if _, err := st.AsOperator().CreateAccountTeam(ctx, storetest.AccountAuthority(t, st, other.Account.ID), store.TeamInfo{Slug: "acme", DisplayName: ""}); !errors.Is(err, store.ErrSlugTaken) {
			t.Fatalf("registering a deleted slug at %s = %v, want ErrSlugTaken", at, err)
		}
	}
}

// Two co-owners deleting their accounts at once must not both count the
// other as the owner who stays.
func TestConcurrentCoOwnerDeletionsLeaveTheTeamAnOwner(t *testing.T) {
	for _, withEditor := range []bool{true, false} {
		t.Run(fmt.Sprintf("editor=%v", withEditor), func(t *testing.T) {
			for i := range 20 {
				st := storetest.Open(t)
				ctx := context.Background()
				now := time.Now()
				a := signIn(t, st, fmt.Sprintf("a%d", i), "a@example.com")
				b := signIn(t, st, fmt.Sprintf("b%d", i), "b@example.com")
				if _, err := st.AsOperator().CreateAccountTeam(ctx, storetest.AccountAuthority(t, st, a.Account.ID), store.TeamInfo{Slug: "shared", DisplayName: ""}); err != nil {
					t.Fatal(err)
				}
				shared := tenant(t, st, "shared")
				inv, err := shared.InviteForTest(t, ctx, a.Account.ID, "b@example.com", store.RoleOwner, now)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := st.AsOperator().AcceptAccountInvitation(ctx, storetest.AccountAuthority(t, st, b.Account.ID), inv.ID); err != nil {
					t.Fatal(err)
				}
				if withEditor {
					e := signIn(t, st, fmt.Sprintf("e%d", i), "e@example.com")
					inv, err := shared.InviteForTest(t, ctx, a.Account.ID, "e@example.com", store.RoleEditor, now)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := st.AsOperator().AcceptAccountInvitation(ctx, storetest.AccountAuthority(t, st, e.Account.ID), inv.ID); err != nil {
						t.Fatal(err)
					}
				}
				st.SetTestClock(t, func() time.Time { return now })
				var wg sync.WaitGroup
				ids := []string{a.Account.ID, b.Account.ID}
				errs := make([]error, len(ids))
				for n, id := range ids {
					wg.Add(1)
					go func() {
						defer wg.Done()
						_, errs[n] = st.DeleteAccount(ctx, id)
					}()
				}
				wg.Wait()
				deleted, lastOwner := 0, 0
				for n, err := range errs {
					switch {
					case err == nil:
						deleted++
						if left := countWhere(t, st, "accounts", "id = '"+ids[n]+"'"); left != 0 {
							t.Fatalf("iteration %d: deletion of %s succeeded but the account remains", i, ids[n])
						}
					case errors.Is(err, store.ErrLastOwner):
						lastOwner++
					default:
						t.Fatalf("iteration %d: deleting %s: %v", i, ids[n], err)
					}
				}
				// safety: with an editor the second deletion is the last owner of a team with members; without one it
				// empties the team, which is queued for deletion.
				if withEditor && (deleted != 1 || lastOwner != 1) || !withEditor && deleted != 2 {
					t.Fatalf("iteration %d editor=%v: %d deletions succeeded and %d were refused as last owner", i, withEditor, deleted, lastOwner)
				}
				owners := countWhere(t, st, "memberships", "team = 'shared' AND role = 'owner'")
				members := countWhere(t, st, "memberships", "team = 'shared'")
				deleting := countWhere(t, st, "team_deletions", "slug = 'shared' AND state = 'pending'")
				if members > 0 && owners == 0 {
					t.Fatalf("iteration %d: the team kept %d members and no owner", i, members)
				}
				if members == 0 && deleting == 0 {
					t.Fatalf("iteration %d: the team lost every member and was not queued for deletion", i)
				}
			}
		})
	}
}

// The daily cap on invitation emails protects an inbox, so deleting the
// teams that sent them must not reset it, and an address counts the same
// whatever its case.
func TestInvitationEmailCapSurvivesTeamDeletion(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	now := time.Now()
	spellings := []string{
		"target@example.com", "Target@example.com", "TARGET@example.com",
		"target@Example.com", "target@EXAMPLE.COM", "tArget@example.com",
	}
	claimed := 0
	for i, to := range spellings {
		owner := signIn(t, st, fmt.Sprintf("o%d", i), fmt.Sprintf("owner%d@example.com", i))
		slug := store.Team(fmt.Sprintf("churn-%d", i))
		if _, err := st.AsOperator().CreateAccountTeam(ctx, storetest.AccountAuthority(t, st, owner.Account.ID), store.TeamInfo{Slug: slug, DisplayName: ""}); err != nil {
			t.Fatal(err)
		}
		inv, err := tenant(t, st, slug).InviteForTest(t, ctx, owner.Account.ID, to, store.RoleReader, now)
		if err != nil {
			t.Fatal(err)
		}
		st.SetTestClock(t, func() time.Time { return now })
		ok, err := st.AsOperator().ClaimInvitationEmail(ctx, inv.ID)
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			claimed++
		}
		deleteAndPurge(t, st, slug, now)
	}
	if claimed != store.MaxInvitationEmailsPerDay {
		t.Fatalf("mailed one inbox %d times in a day, want %d", claimed, store.MaxInvitationEmailsPerDay)
	}
}

// Deleting a team frees nothing on the count of teams an account has
// created, so creating and deleting in a loop stops at the lifetime cap.
func TestTeamCreationIsCappedOverTheAccountsLifetime(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	now := time.Now()
	owner := signIn(t, st, "o", "owner@example.com")
	var err error
	created := 1
	for i := range 30 {
		slug := store.Team(fmt.Sprintf("churn-%d", i))
		if _, err = st.AsOperator().CreateAccountTeam(ctx, storetest.AccountAuthority(t, st, owner.Account.ID), store.TeamInfo{Slug: slug, DisplayName: ""}); err != nil {
			break
		}
		created++
		deleteAndPurge(t, st, slug, now)
	}
	if !errors.Is(err, store.ErrTeamLimit) || created != store.MaxCreatedTeams {
		t.Fatalf("created %d teams before %v, want %d then ErrTeamLimit", created, err, store.MaxCreatedTeams)
	}
}

func TestTeamPurgeRemovesOnlyItsStorageUsage(t *testing.T) {
	st := storetest.Open(t)
	now := time.Now()
	owner := signIn(t, st, "o", "owner@example.com")
	for _, team := range []store.Team{"acme", "keep"} {
		if _, err := st.AsOperator().CreateAccountTeam(t.Context(), storetest.AccountAuthority(t, st, owner.Account.ID), store.TeamInfo{Slug: team, DisplayName: ""}); err != nil {
			t.Fatal(err)
		}
		for range 2 {
			if err := tenant(t, st, team).AddUsage(t.Context(), store.UsageDelta{Period: "period", Counter: "storage_bytes", Amount: 1}); err != nil {
				t.Fatal(err)
			}
		}
	}
	deleteAndPurge(t, st, "acme", now)
	if n := countWhere(t, st, "storage_month_usage", "team = 'acme'"); n != 0 {
		t.Fatalf("purge left %d storage rows", n)
	}
	if n := countWhere(t, st, "storage_month_usage", "team = 'keep' AND bytes = 2"); n != 1 {
		t.Fatalf("purge changed another tenant's usage rows: %d", n)
	}
}

func purge(t *testing.T, st *store.Store, team store.Team, now time.Time) {
	t.Helper()
	ctx := context.Background()
	st.SetTestClock(t, func() time.Time { return now })
	if ok, err := st.AsOperator().ClaimTeamDeletion(ctx, team, "purger", time.Minute); err != nil || !ok {
		t.Fatalf("claim %s = %v, %v", team, ok, err)
	}
	st.SetTestClock(t, func() time.Time { return now })
	if err := st.AsOperator().PurgeTeam(ctx, team, "purger", time.Hour); err != nil {
		t.Fatalf("purge %s: %v", team, err)
	}
}

// One replica purges a team at a time: a second holder is refused while the
// first's lease is live, and a purge by a holder that lost the lease fails
// without marking the deletion done.
func TestTeamDeletionLeaseKeepsOnePurger(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	now := time.Now()
	owner := signIn(t, st, "o", "owner@example.com")
	if _, err := st.AsOperator().CreateAccountTeam(ctx, storetest.AccountAuthority(t, st, owner.Account.ID), store.TeamInfo{Slug: "acme", DisplayName: ""}); err != nil {
		t.Fatal(err)
	}
	op := st.AsOperator()
	st.SetTestClock(t, func() time.Time { return now })
	if _, _, err := op.RequestTeamDeletion(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	if ok, err := op.ClaimTeamDeletion(ctx, "acme", "a", time.Minute); err != nil || !ok {
		t.Fatalf("first claim = %v, %v", ok, err)
	}
	st.SetTestClock(t, func() time.Time { return now.Add(30 * time.Second) })
	if ok, err := op.ClaimTeamDeletion(ctx, "acme", "b", time.Minute); err != nil || ok {
		t.Fatalf("second holder during a live lease = %v, %v; want false", ok, err)
	}
	st.SetTestClock(t, func() time.Time { return now.Add(30 * time.Second) })
	if err := op.PurgeTeam(ctx, "acme", "b", time.Hour); !errors.Is(err, store.ErrDeletionLeaseLost) {
		t.Fatalf("purge by a non-holder = %v, want ErrDeletionLeaseLost", err)
	}
	later := now.Add(2 * time.Minute)
	st.SetTestClock(t, func() time.Time { return later })
	if ok, err := op.ClaimTeamDeletion(ctx, "acme", "b", time.Minute); err != nil || !ok {
		t.Fatalf("claim after the lease lapsed = %v, %v", ok, err)
	}
	st.SetTestClock(t, func() time.Time { return later })
	if err := op.PurgeTeam(ctx, "acme", "a", time.Hour); !errors.Is(err, store.ErrDeletionLeaseLost) {
		t.Fatalf("purge by the holder that lost the lease = %v, want ErrDeletionLeaseLost", err)
	}
	if n := countWhere(t, st, "team_deletions", "slug = 'acme' AND state = 'pending'"); n != 1 {
		t.Fatal("a refused purge marked the deletion done")
	}
	st.SetTestClock(t, func() time.Time { return later })
	if err := op.PurgeTeam(ctx, "acme", "b", time.Hour); err != nil {
		t.Fatalf("purge by the holder = %v", err)
	}
}

func TestTeamRecheckRemovesOperationalRowsFromTombstone(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	now := time.Now()
	owner := signIn(t, st, "o", "owner@example.com")
	if _, err := st.AsOperator().CreateAccountTeam(ctx, storetest.AccountAuthority(t, st, owner.Account.ID), store.TeamInfo{Slug: "acme", DisplayName: ""}); err != nil {
		t.Fatal(err)
	}
	deleteAndPurge(t, st, "acme", now)
	if _, err := st.DB().ExecContext(ctx, storetest.Rebind(st,
		`INSERT INTO egress_usage (period, bytes, updated_at, team) VALUES ('2026-09', 1, 1, 'acme')`)); err != nil {
		t.Fatal(err)
	}
	op := st.AsOperator()
	st.SetTestClock(t, func() time.Time { return now.Add(30 * time.Minute) })
	if ok, err := op.ClaimTeamDeletion(ctx, "acme", "p", time.Minute); err != nil || ok {
		t.Fatalf("claiming a recheck before it is due = %v, %v; want false", ok, err)
	}
	due := now.Add(2 * time.Hour)
	st.SetTestClock(t, func() time.Time { return due })
	if ok, err := op.ClaimTeamDeletion(ctx, "acme", "p", time.Minute); err != nil || !ok {
		t.Fatalf("claiming a due recheck = %v, %v", ok, err)
	}
	st.SetTestClock(t, func() time.Time { return due })
	if err := op.FinishTeamRecheck(ctx, "acme", "p"); err != nil {
		t.Fatal(err)
	}
	if n := countWhere(t, st, "egress_usage", "team = 'acme'"); n != 0 {
		t.Fatal("the recheck left a row written after the purge")
	}
	pending, err := op.PendingTeamDeletions(ctx)
	if err != nil || len(pending) != 0 {
		t.Fatalf("work left after the recheck = %+v, %v", pending, err)
	}
}

// Every row a deleted account leaves in a team, and every usage row, names
// "deleted user" instead of the account's address; billing amounts stay.
func TestAccountDeletionRelabelsEveryRowThatNamedTheAccount(t *testing.T) {
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
	if _, err := st.AsOperator().AcceptAccountInvitation(ctx, storetest.AccountAuthority(t, st, leaver.Account.ID), inv.ID); err != nil {
		t.Fatal(err)
	}
	if err := shared.TestOnlySeedTriggerWithRun(ctx,
		store.Trigger{ID: "run-l", Pipeline: "build", CreatedAt: now},
		store.Run{RunSummary: runstate.RunSummary{ID: "run-l", Pipeline: "build", Status: "pending", StartedAt: now}}); err != nil {
		t.Fatal(err)
	}
	if err := teststore.SeedSecret(st, shared.Team(), store.Secret{Name: "K", Value: "v", Principal: "leaver@example.com"}, now); err != nil {
		t.Fatal(err)
	}
	shared.MemberCapabilityForTest(t, leaver.Account.ID, now)
	if err := teststore.SeedNode(st, ctx, store.Node{RunID: "run-l", NodeID: "n", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	team := string(owner.PersonalTeam)
	bystander := string(leaver.PersonalTeam)
	for _, q := range []string{
		`INSERT INTO egress_usage (period, bytes, updated_at, team) VALUES ('2026-09', 700, 1, '` + team + `')`,
		`INSERT INTO debug_pauses (run_id, node_id, reason, paused_at, expires_at, released_by, team) VALUES ('run-l', 'n', 'r', 1, 2, 'leaver@example.com', '` + team + `')`,
		`INSERT INTO approvals (run_id, node_id, requested_at, approver, team) VALUES ('run-l', 'n', 1, '` + leaver.Account.ID + `', '` + team + `')`,
		`INSERT INTO cron_schedules (id, repo_path, pipeline, cron, tz, overlap, catch_up_ns, armed_at, armed_by, updated_at, cursor_at, team, repo_key)
		 VALUES ('c1', '/r', 'build', '* * * * *', 'UTC', 'skip', 0, 1, 'leaver@example.com', 1, 1, '` + team + `', 'gh:1')`,
		`UPDATE nodes SET claim_principal = '` + leaver.Account.ID + `' WHERE run_id = 'run-l' AND node_id = 'n'`,
		`UPDATE triggers SET claim_principal = 'leaver@example.com' WHERE id = 'run-l'`,
		`INSERT INTO credit_grants (id, kind, amount_micro, created_by, created_at, team) VALUES ('g2', 'free', 5, 'deploy-bot', 1, '` + bystander + `')`,
		`INSERT INTO credit_grants (id, kind, amount_micro, created_by, created_at, team) VALUES ('g1', 'free', 5, 'leaver@example.com', 1, '` + team + `')`,
		`UPDATE runs SET created_principal = 'leaver@example.com' WHERE id = 'run-l'`,
	} {
		if _, err := st.DB().ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if _, err := st.DB().ExecContext(ctx, storetest.Rebind(st, `INSERT INTO events (run_id, seq, ts, kind, payload, team) VALUES ('run-l', 1, 1, 'note', ?, ?)`), []byte(`{"by":"leaver@example.com"}`), team); err != nil {
		t.Fatal(err)
	}

	st.SetTestClock(t, func() time.Time { return now })
	if _, err := st.DeleteAccount(ctx, leaver.Account.ID); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ table, col string }{
		{"credit_grants", "created_by"},
		{"secrets", "principal"},
		{"triggers", "claim_principal"},
		{"runs", "created_principal"},
		{"debug_pauses", "released_by"},
		{"cron_schedules", "armed_by"},
		{"approvals", "approver"},
		{"nodes", "claim_principal"},
	} {
		if n := countWhere(t, st, c.table, c.col+" IN ('leaver@example.com', '"+leaver.Account.ID+"') AND team = '"+team+"'"); n != 0 {
			t.Errorf("%s.%s still names the deleted account", c.table, c.col)
		}
	}
	if n := countWhere(t, st, "credit_grants", "id = 'g2' AND created_by = 'deploy-bot'"); n != 1 {
		t.Error("the relabel reached a row in a team the deleted account's token was never in")
	}
	if n := countWhere(t, st, "events", "run_id = 'run-l' AND payload IS NOT NULL"); n != 1 {
		t.Fatal("the event went missing")
	}
	var payload string
	readPayload := `SELECT CAST(payload AS TEXT) FROM events WHERE run_id = 'run-l'`
	if st.Dialect() == store.DialectPostgres {
		readPayload = `SELECT convert_from(payload, 'UTF8') FROM events WHERE run_id = 'run-l'`
	}
	if err := st.DB().QueryRowContext(ctx, readPayload).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if payload != `{"by":"deleted user"}` {
		t.Errorf("event payload = %s", payload)
	}
	if n := countWhere(t, st, "egress_usage", "team = '"+team+"' AND bytes = 700"); n != 1 {
		t.Error("tenant egress bytes changed during account deletion")
	}
}

// The invitation email log holds an unsalted digest of every invited address,
// so the retention pass drops each row once it no longer counts toward the
// daily cap, not only when the same address is invited again.
func TestRetentionPrunesTheInvitationEmailLogAfterADay(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	now := time.Now()
	if _, err := st.DB().ExecContext(ctx, storetest.Rebind(st, `INSERT INTO invitation_email_log(address,sent_at) VALUES ('old',?),('recent',?)`), now.Add(-25*time.Hour).UnixNano(), now.Add(-time.Hour).UnixNano()); err != nil {
		t.Fatal(err)
	}
	st.SetTestClock(t, func() time.Time { return now })
	if _, err := st.AsOperator().MaintainRetention(ctx, store.RetentionRequest{Limit: 100}); err != nil {
		t.Fatal(err)
	}
	if old, recent := countWhere(t, st, "invitation_email_log", "address = 'old'"), countWhere(t, st, "invitation_email_log", "address = 'recent'"); old != 0 || recent != 1 {
		t.Fatalf("after retention the log keeps %d day-old and %d recent rows, want 0 and 1", old, recent)
	}
}
