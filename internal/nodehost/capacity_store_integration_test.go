package nodehost

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
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
	row, err := fixture.Tenant.ReadNode(t.Context(), controllerwire.NodeRef{RunID: fixture.Accepted.RunID, NodeID: id})
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
	admission := storeAdmission{Store: fixture.Tenant, Owner: fixture.Actor}
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

func TestStoreAdmissionSQLiteCancelOthersCancelsBoundWorkBeforeReplacement(t *testing.T) {
	first := newStoreHostFixture(t, capacityPlan("cancel_others", 0, "a"))
	second := newStoreHostFixtureOn(t, first.Store, capacityPlan("cancel_others", 0, "a"), runspec.Options{})
	for _, fixture := range []*storeHostFixture{first, second} {
		admission := storeAdmission{Store: fixture.Tenant, Owner: fixture.Actor}
		if err := admission.Node(t.Context(), fixture.Accepted, capacityRow(t, fixture, "a"), func(context.Context) error {
			if fixture == second {
				row := capacityRow(t, first, "a")
				if row.CancelRequestedAt == nil && row.Outcome != store.NodeCancelled {
					t.Fatal("replacement entered before bound work received cancellation")
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestStoreAdmissionSQLiteCancelOthersReachesRunningUnitRenewal(t *testing.T) {
	first := newStoreHostFixture(t, capacityPlan("cancel_others", 0, "a"))
	second := newStoreHostFixtureOn(t, first.Store, capacityPlan("cancel_others", 0, "a"), runspec.Options{})
	admission := storeAdmission{Store: first.Tenant, Owner: first.Actor}
	row := capacityRow(t, first, "a")
	if err := admission.Node(t.Context(), first.Accepted, row, func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := first.Tenant.TransitionNode(t.Context(), store.NodeTransition{Ref: row.Ref, To: store.NodeReady, Actor: first.Actor, Cause: store.CauseRelease}); err != nil {
		t.Fatal(err)
	}
	award, err := first.Tenant.ReconcileNode(t.Context(), store.ReconcileRequest{Ref: row.Ref, Actor: first.Actor, Policy: store.SourceNone})
	if err != nil {
		t.Fatal(err)
	}
	actor := store.Actor{Kind: store.ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: award.Node.CapabilityPrefix}, CapabilityPrefix: award.Node.CapabilityPrefix, Holder: award.Node.ClaimedBy, Generation: award.Node.Generation}
	if _, err := first.Tenant.TransitionNode(t.Context(), store.NodeTransition{Ref: row.Ref, To: store.NodeRunning, Actor: actor, Cause: store.CauseStart, SourceAttempt: &award.Node.Attempt}); err != nil {
		t.Fatal(err)
	}
	replacement := storeAdmission{Store: second.Tenant, Owner: second.Actor}
	if err := replacement.Node(t.Context(), second.Accepted, capacityRow(t, second, "a"), func(context.Context) error {
		beat, err := first.Store.AsOperator().RenewUnit(t.Context(), award.Node.CapabilityPrefix, true)
		if err != nil || !beat.CancelRequested {
			t.Fatalf("superseded running unit renewed without cancellation: %+v, %v", beat, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if row := capacityRow(t, first, "a"); row.Status != store.StatusRunning || row.CancelRequestedAt == nil {
		t.Fatalf("running work did not retain cooperative cancellation: %+v", row)
	}
}

func TestStoreAdmissionSQLiteCancelOthersStopsOwnerSelfClaimedExecution(t *testing.T) {
	first := newStoreHostFixture(t, capacityPlan("cancel_others", 0, "a"))
	second := newStoreHostFixtureOn(t, first.Store, capacityPlan("cancel_others", 0, "a"), runspec.Options{})
	admission := storeAdmission{Store: first.Tenant, Owner: first.Actor}
	first.Host.Admit = admission.Node
	started := make(chan context.Context, 1)
	observed := make(chan struct{}, 1)
	wait := first.Host.Wait
	first.Host.Wait = func(ctx context.Context, before store.RunSnapshot) error {
		for _, row := range before.Nodes {
			if row.CancelRequestedAt != nil && row.Status != store.StatusDone {
				select {
				case observed <- struct{}{}:
				default:
				}
			}
		}
		return wait(ctx, before)
	}
	first.Host.Executor = ownerNodeFunc(func(ctx context.Context, input NodeInput) error {
		ref := controllerwire.NodeRef{RunID: first.Accepted.RunID, NodeID: input.NodeID}
		actor := store.Actor{Kind: store.ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: input.Authority.CapabilityPrefix}, CapabilityPrefix: input.Authority.CapabilityPrefix, Holder: input.Authority.Holder, Generation: input.Generation}
		applied, err := first.State.TransitionNode(ctx, store.NodeTransition{Ref: ref, Actor: actor, To: store.NodeRunning, Cause: store.CauseStart, SourceAttempt: new(input.Attempt)})
		if err != nil {
			return err
		}
		started <- ctx
		<-ctx.Done()
		_, err = first.State.TransitionNode(context.WithoutCancel(ctx), store.NodeTransition{Ref: ref, Actor: actor, To: store.NodeCancelled, Cause: store.CauseReport, SourceAttempt: new(applied.Attempt), Extra: store.TransitionExtra{Result: &store.NodeResult{Outcome: store.NodeCancelled, Reason: "concurrency superseded"}}})
		return err
	})
	type result struct {
		completion RunCompletion
		err        error
	}
	finished := make(chan result, 1)
	joined := make(chan struct{})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	go func() {
		defer close(joined)
		completion, err := first.Host.Run(ctx, first.Input)
		finished <- result{completion, err}
	}()
	defer func() { cancel(); <-joined }()
	var execution context.Context
	select {
	case execution = <-started:
	case result := <-finished:
		t.Fatalf("owner exited before self-claimed execution: %+v, %v", result.completion, result.err)
	case <-ctx.Done():
		t.Fatal(context.Cause(ctx))
	}
	replacement := storeAdmission{Store: second.Tenant, Owner: second.Actor}
	if err := replacement.Node(ctx, second.Accepted, capacityRow(t, second, "a"), func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	first.State.notify()
	var completed result
	select {
	case <-observed:
		if execution.Err() == nil {
			t.Fatal("owner observed supersession but left its active self-claimed body running")
		}
		select {
		case completed = <-finished:
		case <-ctx.Done():
			t.Fatal(context.Cause(ctx))
		}
	case completed = <-finished:
	case <-ctx.Done():
		t.Fatal(context.Cause(ctx))
	}
	if completed.err != nil || completed.completion.Outcome != "failed" {
		t.Fatalf("owner did not settle after supersession: %+v, %v", completed.completion, completed.err)
	}
	row := capacityRow(t, first, "a")
	if row.Status != store.StatusDone || row.Outcome != store.NodeCancelled || row.CancelRequestedAt == nil {
		t.Fatalf("self-claimed cancellation was not persisted: %+v", row)
	}
}

func TestStoreAdmissionSQLiteCancelOthersReachesPlanUnitRenewal(t *testing.T) {
	first := newStoreHostFixture(t, planGrouped("global", "cancel_others", "a"))
	second := newStoreHostFixtureOn(t, first.Store, planGrouped("global", "cancel_others", "a"), runspec.Options{})
	for _, fixture := range []*storeHostFixture{first, second} {
		admission := storeAdmission{Store: fixture.Tenant, Owner: fixture.Actor}
		if _, err := admission.Run(t.Context(), fixture.Accepted, func(context.Context) (RunCompletion, error) {
			if fixture == second {
				beat, err := first.Store.AsOperator().RenewUnit(t.Context(), first.Actor.CapabilityPrefix, false)
				if err != nil || !beat.CancelRequested {
					t.Fatalf("superseded plan renewed without cancellation: %+v, %v", beat, err)
				}
			}
			return RunCompletion{}, nil
		}); err != nil {
			t.Fatal(err)
		}
	}
}

type capacityMutationBoundary struct {
	*store.Tenant
	beforeAcquire func()
}

func (s capacityMutationBoundary) AcquireCapacity(ctx context.Context, request store.AcquireSlotRequest, binding store.CapacityBinding, actor store.Actor) (store.AcquireSlotResponse, error) {
	s.beforeAcquire()
	return s.Tenant.AcquireCapacity(ctx, request, binding, actor)
}

func TestStoreAdmissionSQLiteFencesAcquisitionAfterAdoption(t *testing.T) {
	fixture := newStoreHostFixture(t, capacityPlan("queue", 0, "a"))
	boundary := capacityMutationBoundary{Tenant: fixture.Tenant, beforeAcquire: func() {
		if _, err := teststore.SQL(t, fixture.Store).ExecContext(t.Context(), `UPDATE triggers SET generation=generation+1 WHERE team=? AND id=?`, store.DefaultTeam, fixture.Accepted.RunID); err != nil {
			t.Fatal(err)
		}
	}}
	admission := storeAdmission{Store: boundary, Owner: fixture.Actor}
	err := admission.Node(t.Context(), fixture.Accepted, capacityRow(t, fixture, "a"), func(context.Context) error { t.Fatal("stale owner ran"); return nil })
	if err == nil {
		t.Fatal("stale owner acquired capacity")
	}
	state, stateErr := fixture.Tenant.GetConcurrencyState(t.Context(), "global:db")
	if !errors.Is(stateErr, store.ErrNotFound) && (stateErr != nil || len(state.Holders) != 0 || len(state.Waiters) != 0) {
		t.Fatalf("stale owner mutated capacity: %+v, %v", state, stateErr)
	}
}

func TestStoreAdmissionSQLitePromotesOnlyAfterTerminalRelease(t *testing.T) {
	fixture := newStoreHostFixture(t, capacityPlan("queue", 0, "a", "b"))
	admission := storeAdmission{Store: fixture.Tenant, Owner: fixture.Actor}
	first, second := capacityRow(t, fixture, "a"), capacityRow(t, fixture, "b")
	if err := admission.Node(t.Context(), fixture.Accepted, first, func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	admission.Queued = func(ctx context.Context, _ controllerwire.NodeRef, _ store.AcquireSlotResponse) error {
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
			admission := storeAdmission{Store: fixture.Tenant, Owner: fixture.Actor}
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
	admission := storeAdmission{Store: fixture.Tenant, Owner: fixture.Actor}
	first, second := capacityRow(t, fixture, "a"), capacityRow(t, fixture, "b")
	if err := admission.Node(t.Context(), fixture.Accepted, first, func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	admission.Queued = func(context.Context, controllerwire.NodeRef, store.AcquireSlotResponse) error { cancel(); return nil }
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
		admission := storeAdmission{Store: fixture.Tenant, Owner: actor}
		err := admission.Node(t.Context(), fixture.Accepted, capacityRow(t, fixture, "a"), func(context.Context) error { t.Error("forged owner ran"); return nil })
		if err == nil {
			t.Fatal("forged owner admitted")
		}
	}
}

func TestStoreAdmissionSQLitePlanCapacityRetainedUntilRunFinishes(t *testing.T) {
	doc := planGrouped("global", "queue", "a")
	fixture := newStoreHostFixture(t, doc)
	admission := storeAdmission{Store: fixture.Tenant, Owner: fixture.Actor}
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
	admission := storeAdmission{Owner: store.Actor{Kind: store.ActorRunHost, Owner: store.OwnerLeaseRef{Kind: store.OwnerTrigger, RunID: accepted.RunID, Holder: "owner", Generation: 1}}}
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
		admission := storeAdmission{Store: fixture.Tenant, Owner: actor}
		if err := admission.Node(t.Context(), fixture.Accepted, capacityRow(t, fixture, "a"), func(context.Context) error { t.Error("stale owner ran"); return nil }); err == nil {
			t.Fatal("stale owner admitted")
		}
	})
}

func TestStoreAdmissionSQLitePlanGroupBoundsTwoRuns(t *testing.T) {
	first := newStoreHostFixture(t, planGrouped("global", "queue", "a"))
	second := newStoreHostFixtureOn(t, first.Store, planGrouped("global", "queue", "a"), runspec.Options{})
	firstAdmission := storeAdmission{Store: first.Tenant, Owner: first.Actor}
	if _, err := firstAdmission.Run(t.Context(), first.Accepted, func(context.Context) (RunCompletion, error) { return RunCompletion{}, nil }); err != nil {
		t.Fatal(err)
	}
	secondAdmission := storeAdmission{Store: second.Tenant, Owner: second.Actor}
	secondAdmission.Queued = func(context.Context, controllerwire.NodeRef, store.AcquireSlotResponse) error {
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
