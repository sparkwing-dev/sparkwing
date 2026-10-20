package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
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

	if _, err := tn.TestOnlySettlePaidCredit(ctx, store.CreditGrantRequest{
		Kind: store.CreditGrantPaid, AmountMicro: 10 * store.MicroCreditsPerCredit,
		Reference: "pi_1", CreatedBy: "checkout",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AsOperator().TestOnlyReversePayment(ctx, "pi_1", "re_1", "operator", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := tn.RecordCreditGrant(ctx, store.CreditGrantRequest{Kind: store.CreditGrantFree, AmountMicro: store.MicroCreditsPerCredit, Reference: "promo", CreatedBy: "operator"}); err != nil {
		t.Fatal(err)
	}
	if _, err := teststore.ForTeam(t, st, team).TestOnlyHoldForDispute(ctx, "dp_1", "pi_1", "card holder says fraud", now); err != nil {
		t.Fatal(err)
	}
	if n, err := teststore.ForTeam(t, st, team).ReleaseCreditFreezes(ctx, "dp_1", "", "", now); err != nil || n != 1 {
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
		(paid[0].Actor != "payment_provider" || paid[0].Attrs["reference"] != "payment:test:fixture-row:pi_1") {
		t.Errorf("paid event = %+v, want the verified provider and committed payment", paid[0])
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
	if _, err := st.CreateAccountTeam(ctx, storetest.AccountAuthority(t, st, owner.Account.ID), store.TeamInfo{Slug: "acme", DisplayName: ""}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DeleteAccount(ctx, leaver.Account.ID, now); err != nil {
		t.Fatal(err)
	}
	if n := countWhere(t, st, "business_events", "account = '"+leaver.Account.ID+"' OR actor = '"+leaver.Account.ID+"'"); n != 0 {
		t.Fatalf("events still naming the deleted account = %d", n)
	}
	if n := countWhere(t, st, "business_events", "account = '"+owner.Account.ID+"' AND team IS NULL"); n != 1 {
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
	tenant, finish := runCompletionFixture(t, st)
	run, err := tenant.ReadRun(ctx, finish.Unit.RunID)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := st.ResolveAuthority(ctx, finish.Transition.Actor.Authority)
	if err != nil || owner.Capability.Parent == nil {
		t.Fatalf("owner ancestry: %+v, %v", owner, err)
	}
	for i, status := range []store.RunState{store.RunCancelled, store.RunSuccess, store.RunSuccess} {
		if status == store.RunCancelled {
			cancel := store.RunTransition{RunID: finish.Unit.RunID, To: store.RunCancelled, Cause: store.RunCancel, Actor: store.Actor{Kind: store.ActorOperator, Authority: run.Request.AuthorityRef}}
			if _, err := tenant.TransitionRun(ctx, cancel); err != nil {
				t.Fatal(err)
			}
		}
		finish.Transition.To = status
		if _, err := tenant.FinishRunAndUnit(ctx, finish); err != nil {
			t.Fatal(err)
		}
		if i == 2 {
			break
		}
		admitted, err := tenant.AdmitRun(ctx, store.IntakeRequest{Authority: run.Request.AuthorityRef, Request: runspec.Request{Lineage: runspec.Lineage{RetryOf: finish.Unit.RunID}}})
		if err != nil {
			t.Fatal(err)
		}
		claim, err := st.ClaimUnit(ctx, store.UnitDemand{CapabilityPrefix: owner.Capability.Parent.ID, Target: &store.UnitRef{Kind: capability.UnitTrigger, RunID: admitted.RunID}, Available: match.Resources{Cores: 8}})
		if err != nil {
			t.Fatal(err)
		}
		actor := store.Actor{Kind: store.ActorRunHost, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix}, CapabilityPrefix: claim.Award.CapabilityPrefix, Owner: claim.Owner}
		if _, err := tenant.TransitionRun(ctx, store.RunTransition{RunID: admitted.RunID, Actor: actor, To: store.RunRunning, Cause: store.RunStart}); err != nil {
			t.Fatal(err)
		}
		finish = store.FinishRunRequest{Unit: claim.Owner, Transition: store.RunTransition{RunID: admitted.RunID, Actor: actor, Cause: store.RunFinish}}
	}
	events, err := tenant.BusinessEvents(ctx, store.BusinessEventFirstRunSucceeded)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("first-success events = %+v, want exactly one", events)
	}
	runID, ok := events[0].Attrs["run_id"].(string)
	if !ok {
		t.Fatalf("first-success run = %v", events[0].Attrs["run_id"])
	}
	run, err = tenant.ReadRun(ctx, runID)
	if err != nil || run.State != store.RunSuccess {
		t.Fatalf("first-success run = %+v, %v", run, err)
	}
}
