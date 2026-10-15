package nodehost

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func capacityPlan(policy string, timeout int64, ids ...string) nodeproto.PlanDoc {
	doc := nodeproto.PlanDoc{}
	for _, id := range ids {
		doc.Jobs = append(doc.Jobs, nodeproto.JobSpec{ID: id, Steps: []nodeproto.StepSpec{{ID: "body"}}, Concurrency: []nodeproto.ConcurrencySpec{{Group: "db", Scope: new("global"), Capacity: new(1), Cost: new(1), OnLimit: new(policy), QueueTimeoutMS: new(timeout)}}})
	}
	return doc
}

func capacityRow(t *testing.T, fixture *storeHostFixture, id string) store.NodeRow {
	t.Helper()
	row, err := fixture.Tenant.ReadNode(t.Context(), store.NodeRef{RunID: fixture.Accepted.RunID, NodeID: id})
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func cancelCapacityNode(t *testing.T, fixture *storeHostFixture, row store.NodeRow) {
	t.Helper()
	if _, err := fixture.Tenant.TransitionNode(t.Context(), store.NodeTransition{Ref: row.Ref, To: store.NodeCancelled, Actor: fixture.Actor, Cause: store.CauseCancel}); err != nil {
		t.Fatal(err)
	}
}

func TestStoreAdmissionSQLiteRetainsUntilTerminalTransition(t *testing.T) {
	fixture := newStoreHostFixture(t, capacityPlan("queue", 0, "a"))
	admission := StoreAdmission{Store: fixture.Tenant, Owner: fixture.Actor}
	row := capacityRow(t, fixture, "a")
	interrupted := errors.New("owner interrupted")
	for range 2 {
		if err := admission.Node(t.Context(), fixture.Accepted, row, func(context.Context) error { return interrupted }); !errors.Is(err, interrupted) {
			t.Fatal(err)
		}
	}
	state, err := fixture.Tenant.GetConcurrencyState(t.Context(), "global:db")
	if err != nil || len(state.Holders) != 1 || state.UsedCost != 1 {
		t.Fatalf("retained state=%+v err=%v", state, err)
	}
	cancelCapacityNode(t, fixture, row)
	state, err = fixture.Tenant.GetConcurrencyState(t.Context(), "global:db")
	if err != nil || len(state.Holders) != 0 || len(state.Waiters) != 0 {
		t.Fatalf("terminal state=%+v err=%v", state, err)
	}
}

func TestStoreAdmissionSQLitePromotesOnlyAfterTerminalRelease(t *testing.T) {
	fixture := newStoreHostFixture(t, capacityPlan("queue", 0, "a", "b"))
	admission := StoreAdmission{Store: fixture.Tenant, Owner: fixture.Actor}
	first, second := capacityRow(t, fixture, "a"), capacityRow(t, fixture, "b")
	if err := admission.Node(t.Context(), fixture.Accepted, first, func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	admission.Queued = func(ctx context.Context, _ store.NodeRef, _ store.AcquireSlotResponse) error {
		state, err := fixture.Tenant.GetConcurrencyState(ctx, "global:db")
		if err != nil || len(state.Waiters) != 1 || len(state.Holders) != 1 {
			t.Errorf("queued state=%+v err=%v", state, err)
		}
		cancelCapacityNode(t, fixture, first)
		return nil
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	ran := false
	if err := admission.Node(ctx, fixture.Accepted, second, func(context.Context) error { ran = true; return nil }); err != nil || !ran {
		t.Fatalf("promoted ran=%v err=%v", ran, err)
	}
	state, err := fixture.Tenant.GetConcurrencyState(t.Context(), "global:db")
	if err != nil || len(state.Holders) != 1 || state.Holders[0].NodeID != "b" || len(state.Waiters) != 0 {
		t.Fatalf("promoted state=%+v err=%v", state, err)
	}
}

func TestStoreAdmissionSQLiteQueueTimeoutAndLimitPolicies(t *testing.T) {
	for _, test := range []struct {
		policy string
		want   store.NodeState
		reason string
	}{{"queue", store.NodeFailed, store.FailureQueueTimeout}, {"skip", store.NodeSkipped, ""}, {"fail", store.NodeFailed, ""}} {
		t.Run(test.policy, func(t *testing.T) {
			fixture := newStoreHostFixture(t, capacityPlan(test.policy, 10, "a", "b"))
			admission := StoreAdmission{Store: fixture.Tenant, Owner: fixture.Actor}
			if err := admission.Node(t.Context(), fixture.Accepted, capacityRow(t, fixture, "a"), func(context.Context) error { return nil }); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			err := admission.Node(ctx, fixture.Accepted, capacityRow(t, fixture, "b"), func(context.Context) error { t.Error("ran beyond full group"); return nil })
			var failure *AdmissionFailure
			if !errors.As(err, &failure) || failure.Outcome != test.want || failure.Reason != test.reason {
				t.Fatalf("refusal=%v", err)
			}
			state, err := fixture.Tenant.GetConcurrencyState(t.Context(), "global:db")
			if err != nil || len(state.Waiters) != 0 || len(state.Holders) != 1 {
				t.Fatalf("refused state=%+v err=%v", state, err)
			}
		})
	}
}

func TestStoreAdmissionSQLiteOwnerLossRetainsWaiterUntilWorkEnds(t *testing.T) {
	fixture := newStoreHostFixture(t, capacityPlan("queue", 0, "a", "b"))
	admission := StoreAdmission{Store: fixture.Tenant, Owner: fixture.Actor}
	first, second := capacityRow(t, fixture, "a"), capacityRow(t, fixture, "b")
	if err := admission.Node(t.Context(), fixture.Accepted, first, func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	admission.Queued = func(context.Context, store.NodeRef, store.AcquireSlotResponse) error { cancel(); return nil }
	err := admission.Node(ctx, fixture.Accepted, second, func(context.Context) error { t.Error("ran after owner loss"); return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	state, err := fixture.Tenant.GetConcurrencyState(t.Context(), "global:db")
	if err != nil || len(state.Waiters) != 1 {
		t.Fatalf("lost waiter state=%+v err=%v", state, err)
	}
	cancelCapacityNode(t, fixture, second)
	state, err = fixture.Tenant.GetConcurrencyState(t.Context(), "global:db")
	if err != nil || len(state.Waiters) != 0 || len(state.Holders) != 1 {
		t.Fatalf("cancelled state=%+v err=%v", state, err)
	}
}

func TestStoreAdmissionSQLiteRejectsForgedOwner(t *testing.T) {
	fixture := newStoreHostFixture(t, capacityPlan("queue", 0, "a"))
	for _, mutate := range []func(*store.Actor){func(actor *store.Actor) { actor.Owner.Generation++ }, func(actor *store.Actor) { actor.Owner.Holder += "forged" }, func(actor *store.Actor) { actor.CapabilityPrefix += "forged" }} {
		actor := fixture.Actor
		mutate(&actor)
		admission := StoreAdmission{Store: fixture.Tenant, Owner: actor}
		err := admission.Node(t.Context(), fixture.Accepted, capacityRow(t, fixture, "a"), func(context.Context) error { t.Error("forged owner ran"); return nil })
		if err == nil {
			t.Fatal("forged owner admitted")
		}
	}
}

func TestStoreAdmissionSQLitePlanCapacityRetainedUntilRunFinishes(t *testing.T) {
	doc := planGrouped("global", "queue", "a")
	fixture := newStoreHostFixture(t, doc)
	admission := StoreAdmission{Store: fixture.Tenant, Owner: fixture.Actor}
	interrupted := errors.New("owner interrupted")
	for range 2 {
		_, err := admission.Run(t.Context(), fixture.Accepted, func(context.Context) (RunCompletion, error) { return RunCompletion{}, interrupted })
		if !errors.Is(err, interrupted) {
			t.Fatal(err)
		}
	}
	state, err := fixture.Tenant.GetConcurrencyState(t.Context(), "global:deploys")
	if err != nil || len(state.Holders) != 1 || state.Holders[0].NodeID != "" {
		t.Fatalf("plan state=%+v err=%v", state, err)
	}
	cancelCapacityNode(t, fixture, capacityRow(t, fixture, "a"))
	if _, err := fixture.Tenant.FinishRunAndUnit(t.Context(), store.FinishRunRequest{Unit: fixture.Actor.Owner, Transition: store.RunTransition{RunID: fixture.Accepted.RunID, To: store.RunFailed, Actor: fixture.Actor, Cause: store.RunFinish}}); err != nil {
		t.Fatal(err)
	}
	state, err = fixture.Tenant.GetConcurrencyState(t.Context(), "global:deploys")
	if err != nil || len(state.Holders) != 0 || len(state.Waiters) != 0 {
		t.Fatalf("finished plan state=%+v err=%v", state, err)
	}
}

func TestStoreAdmissionRequiresCoordinationStore(t *testing.T) {
	accepted := localAdmissionSeal(t, localAdmissionOptions())
	row := localAdmissionRow("a")
	admission := StoreAdmission{Owner: store.Actor{Kind: store.ActorRunHost, Owner: store.OwnerLeaseRef{Kind: store.OwnerTrigger, RunID: accepted.RunID, Holder: "owner", Generation: 1}}}
	if err := admission.Node(t.Context(), accepted, row, func(context.Context) error { t.Error("job ran without store"); return nil }); err == nil {
		t.Fatal("missing store admitted work")
	}
	if _, err := admission.Run(t.Context(), accepted, func(context.Context) (RunCompletion, error) {
		t.Error("run started without store")
		return RunCompletion{}, nil
	}); err == nil {
		t.Fatal("missing store admitted run")
	}
}

func FuzzStoreAdmissionSQLiteRejectsStaleOwner(f *testing.F) {
	f.Add(uint16(1))
	f.Add(uint16(65535))
	f.Fuzz(func(t *testing.T, delta uint16) {
		fixture := newStoreHostFixture(t, capacityPlan("queue", 0, "a"))
		actor := fixture.Actor
		actor.Owner.Generation += int64(delta) + 1
		admission := StoreAdmission{Store: fixture.Tenant, Owner: actor}
		if err := admission.Node(t.Context(), fixture.Accepted, capacityRow(t, fixture, "a"), func(context.Context) error { t.Error("stale owner ran"); return nil }); err == nil {
			t.Fatal("stale owner admitted")
		}
	})
}

func TestStoreAdmissionSQLitePlanGroupBoundsTwoRuns(t *testing.T) {
	first := newStoreHostFixture(t, planGrouped("global", "queue", "a"))
	second := newStoreHostFixtureOn(t, first.Store, planGrouped("global", "queue", "a"), runspec.Options{})
	firstAdmission := StoreAdmission{Store: first.Tenant, Owner: first.Actor}
	if _, err := firstAdmission.Run(t.Context(), first.Accepted, func(context.Context) (RunCompletion, error) { return RunCompletion{}, nil }); err != nil {
		t.Fatal(err)
	}
	secondAdmission := StoreAdmission{Store: second.Tenant, Owner: second.Actor}
	secondAdmission.Queued = func(context.Context, store.NodeRef, store.AcquireSlotResponse) error {
		cancelCapacityNode(t, first, capacityRow(t, first, "a"))
		_, err := first.Tenant.FinishRunAndUnit(t.Context(), store.FinishRunRequest{Unit: first.Actor.Owner, Transition: store.RunTransition{RunID: first.Accepted.RunID, To: store.RunFailed, Actor: first.Actor, Cause: store.RunFinish}})
		return err
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	ran := false
	if _, err := secondAdmission.Run(ctx, second.Accepted, func(context.Context) (RunCompletion, error) { ran = true; return RunCompletion{}, nil }); err != nil || !ran {
		t.Fatalf("second ran=%v err=%v", ran, err)
	}
	state, err := second.Tenant.GetConcurrencyState(t.Context(), "global:deploys")
	if err != nil || len(state.Holders) != 1 || state.Holders[0].RunID != second.Accepted.RunID || len(state.Waiters) != 0 {
		t.Fatalf("second plan state=%+v err=%v", state, err)
	}
}
