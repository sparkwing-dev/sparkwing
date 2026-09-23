package store_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

// deleteAndPurge deletes a team its owner created and finishes the purge.
func deleteAndPurge(t *testing.T, st *store.Store, team store.Team, now time.Time) {
	t.Helper()
	ctx := context.Background()
	if _, _, err := st.AsOperator().RequestTeamDeletion(ctx, team, now); err != nil {
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
	if _, err := st.CreateTeam(ctx, owner.Account.ID, "acme", "", now); err != nil {
		t.Fatal(err)
	}
	deleteAndPurge(t, st, "acme", now)
	for _, at := range []time.Time{now.Add(time.Hour), now.Add(365 * 24 * time.Hour)} {
		if _, err := st.CreateTeam(ctx, other.Account.ID, "acme", "", at); !errors.Is(err, store.ErrSlugTaken) {
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
				if _, err := st.CreateTeam(ctx, a.Account.ID, "shared", "", now); err != nil {
					t.Fatal(err)
				}
				shared := tenant(t, st, "shared")
				inv, err := shared.CreateInvitation(ctx, a.Account.ID, "b@example.com", store.RoleOwner, now)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := st.AcceptInvitation(ctx, b.Account.ID, inv.ID, now); err != nil {
					t.Fatal(err)
				}
				if withEditor {
					e := signIn(t, st, fmt.Sprintf("e%d", i), "e@example.com")
					inv, err := shared.CreateInvitation(ctx, a.Account.ID, "e@example.com", store.RoleEditor, now)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := st.AcceptInvitation(ctx, e.Account.ID, inv.ID, now); err != nil {
						t.Fatal(err)
					}
				}
				var wg sync.WaitGroup
				for _, id := range []string{a.Account.ID, b.Account.ID} {
					wg.Add(1)
					go func() {
						defer wg.Done()
						_, _ = st.DeleteAccount(ctx, id, now)
					}()
				}
				wg.Wait()
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
		if _, err := st.CreateTeam(ctx, owner.Account.ID, slug, "", now); err != nil {
			t.Fatal(err)
		}
		inv, err := tenant(t, st, slug).CreateInvitation(ctx, owner.Account.ID, to, store.RoleReader, now)
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
	created := 1 // the personal space the sign-in created
	for i := range 30 {
		slug := store.Team(fmt.Sprintf("churn-%d", i))
		if _, err = st.CreateTeam(ctx, owner.Account.ID, slug, "", now); err != nil {
			break
		}
		created++
		deleteAndPurge(t, st, slug, now)
	}
	if !errors.Is(err, store.ErrTeamLimit) || created != store.MaxCreatedTeams {
		t.Fatalf("created %d teams before %v, want %d then ErrTeamLimit", created, err, store.MaxCreatedTeams)
	}
}

// A storage watermark names its team in its key, so the purge removes it
// with the team's rows.
func TestTeamPurgeRemovesTheTeamsStorageWatermarks(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	now := time.Now()
	owner := signIn(t, st, "o", "owner@example.com")
	if _, err := st.CreateTeam(ctx, owner.Account.ID, "acme", "", now); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"storage_charged_through/acme/ci", "storage_charged_through/keep/ci"} {
		if _, err := st.DB().ExecContext(ctx, storetest.Rebind(st, `INSERT INTO sparkwing_meta (key, value, updated_at) VALUES (?, '1', 1)`), key); err != nil {
			t.Fatal(err)
		}
	}
	deleteAndPurge(t, st, "acme", now)
	if n := countWhere(t, st, "sparkwing_meta", "key = 'storage_charged_through/acme/ci'"); n != 0 {
		t.Fatal("the purge left the team's storage watermark")
	}
	if n := countWhere(t, st, "sparkwing_meta", "key = 'storage_charged_through/keep/ci'"); n != 1 {
		t.Fatal("the purge removed another team's storage watermark")
	}
}

func purge(t *testing.T, st *store.Store, team store.Team, now time.Time) {
	t.Helper()
	ctx := context.Background()
	if ok, err := st.AsOperator().ClaimTeamDeletion(ctx, team, "purger", now, time.Minute); err != nil || !ok {
		t.Fatalf("claim %s = %v, %v", team, ok, err)
	}
	if err := st.AsOperator().PurgeTeam(ctx, team, "purger", now, time.Hour); err != nil {
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
	if _, err := st.CreateTeam(ctx, owner.Account.ID, "acme", "", now); err != nil {
		t.Fatal(err)
	}
	op := st.AsOperator()
	if _, _, err := op.RequestTeamDeletion(ctx, "acme", now); err != nil {
		t.Fatal(err)
	}
	if ok, err := op.ClaimTeamDeletion(ctx, "acme", "a", now, time.Minute); err != nil || !ok {
		t.Fatalf("first claim = %v, %v", ok, err)
	}
	if ok, err := op.ClaimTeamDeletion(ctx, "acme", "b", now.Add(30*time.Second), time.Minute); err != nil || ok {
		t.Fatalf("second holder during a live lease = %v, %v; want false", ok, err)
	}
	if err := op.PurgeTeam(ctx, "acme", "b", now.Add(30*time.Second), time.Hour); !errors.Is(err, store.ErrDeletionLeaseLost) {
		t.Fatalf("purge by a non-holder = %v, want ErrDeletionLeaseLost", err)
	}
	later := now.Add(2 * time.Minute)
	if ok, err := op.ClaimTeamDeletion(ctx, "acme", "b", later, time.Minute); err != nil || !ok {
		t.Fatalf("claim after the lease lapsed = %v, %v", ok, err)
	}
	if err := op.PurgeTeam(ctx, "acme", "a", later, time.Hour); !errors.Is(err, store.ErrDeletionLeaseLost) {
		t.Fatalf("purge by the holder that lost the lease = %v, want ErrDeletionLeaseLost", err)
	}
	if n := countWhere(t, st, "team_deletions", "slug = 'acme' AND state = 'pending'"); n != 1 {
		t.Fatal("a refused purge marked the deletion done")
	}
	if err := op.PurgeTeam(ctx, "acme", "b", later, time.Hour); err != nil {
		t.Fatalf("purge by the holder = %v", err)
	}
}

// Rows written under a deleted slug after the purge, by a credential that
// outlived the team, are removed by the recheck once it is due.
func TestTeamRecheckRemovesRowsWrittenAfterThePurge(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	now := time.Now()
	owner := signIn(t, st, "o", "owner@example.com")
	if _, err := st.CreateTeam(ctx, owner.Account.ID, "acme", "", now); err != nil {
		t.Fatal(err)
	}
	deleteAndPurge(t, st, "acme", now)
	if _, err := st.DB().ExecContext(ctx, storetest.Rebind(st,
		`INSERT INTO egress_usage (principal, month, bytes, updated_at, team) VALUES ('late', '2026-09', 1, 1, 'acme')`)); err != nil {
		t.Fatal(err)
	}
	op := st.AsOperator()
	if ok, err := op.ClaimTeamDeletion(ctx, "acme", "p", now.Add(30*time.Minute), time.Minute); err != nil || ok {
		t.Fatalf("claiming a recheck before it is due = %v, %v; want false", ok, err)
	}
	due := now.Add(2 * time.Hour)
	if ok, err := op.ClaimTeamDeletion(ctx, "acme", "p", due, time.Minute); err != nil || !ok {
		t.Fatalf("claiming a due recheck = %v, %v", ok, err)
	}
	if err := op.FinishTeamRecheck(ctx, "acme", "p", due); err != nil {
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
	inv, err := shared.CreateInvitation(ctx, owner.Account.ID, "leaver@example.com", store.RoleEditor, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AcceptInvitation(ctx, leaver.Account.ID, inv.ID, now); err != nil {
		t.Fatal(err)
	}
	if err := shared.CreateTriggerWithRun(ctx,
		store.Trigger{ID: "run-l", Pipeline: "build", CreatedAt: now},
		store.Run{ID: "run-l", Pipeline: "build", Status: "pending", StartedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := shared.CreateOrReplaceSecret(store.Secret{Name: "K", Value: "v", Principal: "leaver@example.com"}, now); err != nil {
		t.Fatal(err)
	}
	team := string(owner.PersonalTeam)
	for _, q := range []string{
		`INSERT INTO egress_usage (principal, month, bytes, updated_at, team) VALUES ('leaver@example.com', '2026-09', 700, 1, '` + team + `')`,
		`INSERT INTO credit_grants (id, kind, amount_micro, created_by, created_at, team) VALUES ('g1', 'free', 5, 'leaver@example.com', 1, '` + team + `')`,
		`INSERT INTO events (run_id, seq, ts, kind, payload, team) VALUES ('run-l', 1, 1, 'note', '{"by":"leaver@example.com"}', '` + team + `')`,
		`UPDATE triggers SET trigger_user = 'leaver@example.com' WHERE id = 'run-l'`,
		`UPDATE runs SET created_principal = 'leaver@example.com' WHERE id = 'run-l'`,
	} {
		if _, err := st.DB().ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}

	if _, err := st.DeleteAccount(ctx, leaver.Account.ID, now); err != nil {
		t.Fatal(err)
	}
	for table, col := range map[string]string{
		"egress_usage": "principal", "credit_grants": "created_by", "secrets": "principal",
		"triggers": "trigger_user", "runs": "created_principal",
	} {
		if n := countWhere(t, st, table, col+" = 'leaver@example.com'"); n != 0 {
			t.Errorf("%s.%s still names the deleted account", table, col)
		}
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
	if n := countWhere(t, st, "egress_usage", "principal = '"+store.DeletedUserLabel+"' AND bytes = 700"); n != 1 {
		t.Error("egress bytes were not kept under the deleted-user label")
	}
}
