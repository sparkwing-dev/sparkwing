package store_test

import (
	"context"
	"errors"
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

	var admitted int
	if err := st.DB().QueryRowContext(ctx, storetest.Rebind(st,
		`SELECT COUNT(*) FROM business_events WHERE kind = ? AND account = ?`),
		store.BusinessEventAccountAdmitted, owner.Account.ID).Scan(&admitted); err != nil || admitted != 1 {
		t.Fatalf("admission events = %d, %v; want 1", admitted, err)
	}
	signIn(t, st, "ev-owner", "ev@example.com")

	checkout, err := tn.OpenCreditCheckout(ctx, 10*store.MicroCreditsPerCredit, now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := tn.AttachCreditCheckout(ctx, checkout, "cs_paid", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := tn.RecordCreditGrant(ctx, store.CreditGrantRequest{
		Kind: store.CreditGrantPaid, AmountMicro: 10 * store.MicroCreditsPerCredit,
		Reference: "pi_1", CreatedBy: "checkout", Checkout: "cs_paid",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ReversePayment(ctx, "pi_1", "re_1", "operator"); err != nil {
		t.Fatal(err)
	}
	if _, err := tn.GrantCredits(ctx, store.CreditGrantFree, store.MicroCreditsPerCredit, "promo", "operator"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := tn.CloseCreditCheckout(ctx, "cs_gone", store.BusinessEventCheckoutExpired, now); err != nil {
			t.Fatal(err)
		}
	}
	if err := tn.CloseCreditCheckout(ctx, "cs_declined", store.BusinessEventCheckoutFailed, now); err != nil {
		t.Fatal(err)
	}
	if err := tn.CloseCreditCheckout(ctx, "cs_x", "checkout.lost", now); !errors.Is(err, store.ErrInvalidInput) {
		t.Fatalf("an unknown checkout outcome = %v, want ErrInvalidInput", err)
	}
	if _, err := st.HoldTeamForDispute(ctx, team, "dp_1", "pi_1", "fraud", now); err != nil {
		t.Fatal(err)
	}
	if n, err := st.ReleaseCreditFreezes(ctx, team, "", now); err != nil || n != 1 {
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
		store.BusinessEventTeamCreated: 1, store.BusinessEventCheckoutOpened: 1,
		store.BusinessEventCheckoutPaid: 1, store.BusinessEventCreditReversed: 1,
		store.BusinessEventCreditGranted: 1, store.BusinessEventCheckoutExpired: 1,
		store.BusinessEventCheckoutFailed: 1, store.BusinessEventTeamFrozen: 1,
		store.BusinessEventTeamUnfrozen: 1,
	} {
		if len(got[kind]) != want {
			t.Errorf("%s events = %d, want %d (all: %+v)", kind, len(got[kind]), want, events)
		}
	}
	if paid := got[store.BusinessEventCheckoutPaid]; len(paid) == 1 &&
		(paid[0].Actor != "checkout" || paid[0].Attrs["session_id"] != "cs_paid") {
		t.Errorf("paid event = %+v, want actor checkout and its session", paid[0])
	}
	if opened := got[store.BusinessEventCheckoutOpened]; len(opened) == 1 && opened[0].SubjectID != "cs_paid" {
		t.Errorf("opened event = %+v, want subject cs_paid", opened[0])
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
