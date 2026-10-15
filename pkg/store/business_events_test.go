package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestBusinessEventsRecordLifecycleSQLite(t *testing.T) {
	assertBusinessEventsRecordLifecycle(t, storetest.NewSQLite(t).Open(t))
}

func TestBusinessEventsRecordLifecyclePostgres(t *testing.T) {
	assertBusinessEventsRecordLifecycle(t, storetest.NewPostgres(t).Open(t))
}

func assertBusinessEventsRecordLifecycle(t *testing.T, st *store.Store) {
	t.Helper()
	ctx := context.Background()
	now := time.Now()
	owner := signIn(t, st, "ev-owner", "ev@example.com")
	team := owner.Account.ActiveTeam
	tn := tenant(t, st, team)

	if n := countWhere(t, st, "business_events", "kind = '"+store.BusinessEventAccountAdmitted+
		"' AND account = '"+owner.Account.ID+"'"); n != 1 {
		t.Fatalf("admission events = %d, want 1", n)
	}
	signIn(t, st, "ev-owner", "ev@example.com")

	if _, err := tn.RecordCreditGrant(ctx, store.CreditGrantRequest{
		Kind: store.CreditGrantPaid, AmountMicro: 10 * store.MicroCreditsPerCredit,
		Reference: "pi_1", CreatedBy: "checkout",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ReversePayment(ctx, "pi_1", "re_1", "operator", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := tn.GrantCredits(ctx, store.CreditGrantFree, store.MicroCreditsPerCredit, "promo", "operator"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.HoldTeamForDispute(ctx, team, "dp_1", "pi_1", "card holder says fraud", now); err != nil {
		t.Fatal(err)
	}
	if n, err := st.ReleaseCreditFreezes(ctx, team, "dp_1", "", "", now); err != nil || n != 1 {
		t.Fatalf("released = %d, %v", n, err)
	}

	got := map[string][]store.BusinessEvent{}
	events, err := tn.BusinessEvents(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range events {
		if ev.Team != team {
			t.Fatalf("tenant read another team's event: %+v", ev)
		}
		got[ev.Kind] = append(got[ev.Kind], ev)
	}
	for kind, want := range map[string]int{
		store.BusinessEventTeamCreated:  1,
		store.BusinessEventCheckoutPaid: 1, store.BusinessEventCreditReversed: 1,
		store.BusinessEventCreditGranted: 1, store.BusinessEventTeamFrozen: 1,
		store.BusinessEventTeamUnfrozen: 1,
	} {
		if len(got[kind]) != want {
			t.Errorf("%s events = %d, want %d (all: %+v)", kind, len(got[kind]), want, events)
		}
	}
	if paid := got[store.BusinessEventCheckoutPaid]; len(paid) == 1 &&
		(paid[0].Actor != "checkout" || paid[0].Attrs["reference"] != "pi_1") {
		t.Errorf("paid event = %+v, want actor checkout and its payment", paid[0])
	}
	if ev := got[store.BusinessEventTeamFrozen]; len(ev) == 1 && ev[0].Attrs["reason"] != nil {
		t.Errorf("frozen event copies the free-form reason: %+v", ev[0])
	}
}

// Deleting an account removes its admission and every trace of its id in
// other events; purging a team removes the team's events.
func TestBusinessEventsFollowAccountAndTeamDeletion(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	now := time.Now()
	owner := signIn(t, st, "o", "owner@example.com")
	leaver := signIn(t, st, "l", "leaver@example.com")
	if _, err := st.CreateTeam(ctx, owner.Account.ID, "acme", "", now); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DeleteAccount(ctx, leaver.Account.ID, now); err != nil {
		t.Fatal(err)
	}
	if n := countWhere(t, st, "business_events", "account = '"+leaver.Account.ID+"' OR actor = '"+leaver.Account.ID+"'"); n != 0 {
		t.Fatalf("events still naming the deleted account = %d", n)
	}
	if n := countWhere(t, st, "business_events", "account = '"+owner.Account.ID+"' AND team = ''"); n != 1 {
		t.Fatalf("the remaining account's admission = %d, want 1", n)
	}
	deleteAndPurge(t, st, "acme", now)
	if n := countWhere(t, st, "business_events", "team = 'acme'"); n != 0 {
		t.Fatalf("events of the purged team = %d", n)
	}
	if n := countWhere(t, st, "business_events", "team = '"+string(owner.PersonalTeam)+"'"); n == 0 {
		t.Fatal("the purge removed another team's events")
	}
}

func TestBusinessEventFirstRunSucceededOncePerTeamSQLite(t *testing.T) {
	assertFirstRunSucceededOncePerTeam(t, storetest.NewSQLite(t).Open(t))
}

func TestBusinessEventFirstRunSucceededOncePerTeamPostgres(t *testing.T) {
	assertFirstRunSucceededOncePerTeam(t, storetest.NewPostgres(t).Open(t))
}

func assertFirstRunSucceededOncePerTeam(t *testing.T, st *store.Store) {
	t.Helper()
	ctx := context.Background()
	for id, status := range map[string]string{"run-fail": "failed", "run-ok-1": "success", "run-ok-2": "success"} {
		if err := st.CreateRun(ctx, store.Run{ID: id, Pipeline: "demo", Status: "running"}); err != nil {
			t.Fatal(err)
		}
		if err := st.FinishRun(ctx, id, status, ""); err != nil {
			t.Fatal(err)
		}
	}
	events, err := tenant(t, st, store.DefaultTeam).BusinessEvents(ctx, store.BusinessEventFirstRunSucceeded)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("first-success events = %+v, want exactly one", events)
	}
	if run := events[0].Attrs["run_id"]; run != "run-ok-1" && run != "run-ok-2" {
		t.Fatalf("first-success names run %v, want a successful run", run)
	}
}
