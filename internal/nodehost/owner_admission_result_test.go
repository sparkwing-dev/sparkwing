package nodehost

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

type admissionAcquireFunc func(context.Context, wingwire.AdmissionRequest, func(wingwire.Queued)) (Lease, error)

func (f admissionAcquireFunc) Acquire(ctx context.Context, request wingwire.AdmissionRequest, queued func(wingwire.Queued)) (Lease, error) {
	return f(ctx, request, queued)
}

func TestOwnerPreservesIndependentLocalAcquisitionFailures(t *testing.T) {
	for _, policy := range []string{"skip", "queue"} {
		t.Run(policy, func(t *testing.T) {
			fixture := newStoreHostFixture(t, capacityPlan(policy, 1, "refused"))
			independent := errors.New("admission transport cleanup failed")
			admission := &LocalAdmission{Daemon: admissionAcquireFunc(func(_ context.Context, request wingwire.AdmissionRequest, queued func(wingwire.Queued)) (Lease, error) {
				if policy == "queue" {
					queued(wingwire.Queued{RunID: string(request.RunID), Key: "host"})
					return nil, independent
				}
				return nil, errors.Join(&wingdclient.AdmissionError{Policy: wingwire.PolicySkip, Key: "db"}, independent)
			})}
			configureNativeAdmission(&fixture.Host, admission, &storeAdmission{Store: fixture.Tenant, Owner: fixture.Actor}, false)
			executed := false
			fixture.Host.Executor = ownerNodeFunc(func(context.Context, NodeInput) error {
				executed = true
				return nil
			})
			completion, err := fixture.Host.Run(t.Context(), fixture.Input)
			if err != nil {
				t.Fatalf("setup failure did not finish: completion=%+v error=%v", completion, err)
			}
			persisted, err := fixture.Tenant.GetNode(t.Context(), fixture.Input.RunID, "refused")
			if err != nil || !strings.Contains(persisted.Error, independent.Error()) {
				t.Fatalf("independent failure was not recorded: node=%+v error=%v", persisted, err)
			}
			row := capacityRow(t, fixture, "refused")
			if executed || row.Attempt != 0 {
				t.Errorf("acquisition failure started execution: executed=%v attempt=%d", executed, row.Attempt)
			}
			if row.Outcome != store.NodeFailed || completion.Outcome != "failed" {
				t.Errorf("independent failure became a policy result: row=%+v completion=%+v", row, completion)
			}
		})
	}
}

func TestResumedPlanRefusalPreservesCompletedWork(t *testing.T) {
	for _, generator := range []bool{false, true} {
		t.Run(fmt.Sprint(generator), func(t *testing.T) {
			plan := planGrouped("global", "fail", "finished", "pending")
			if generator {
				plan.Jobs[0].Closures.Generate = []string{"children"}
				plan.Jobs[1].NeedsGenerated = []string{"children"}
			}
			fixture := newStoreHostFixture(t, plan)
			admission := &storeAdmission{Store: fixture.Tenant, Owner: fixture.Actor}
			if _, err := admission.Run(t.Context(), fixture.Accepted, func(context.Context) (RunCompletion, error) { return RunCompletion{}, nil }); err != nil {
				t.Fatal(err)
			}
			entry, err := fixture.Host.Acquire(t.Context(), fixture.Input)
			if err != nil {
				t.Fatal(err)
			}
			fixture.Host.Executor = ownerNodeFunc(func(ctx context.Context, input NodeInput) error { return fixture.report(ctx, input, store.NodeSuccess) })
			err = fixture.Host.release(t.Context(), fixture.Input, fixture.Accepted, entry, capacityRow(t, fixture, "finished"), store.DecisionReady, func(ctx context.Context, request store.ReconcileRequest) error {
				return fixture.Host.execute(ctx, fixture.Input, fixture.Accepted, entry, request)
			})
			if err != nil {
				t.Fatal(err)
			}
			before := capacityRow(t, fixture, "finished")
			expired := time.Now().UnixNano()
			if _, err := teststore.SQL(t, fixture.Store).ExecContext(t.Context(), `UPDATE triggers SET lease_expires_at=? WHERE id=?`, expired, fixture.Input.RunID); err != nil {
				t.Fatal(err)
			}
			if _, err := teststore.SQL(t, fixture.Store).ExecContext(t.Context(), `UPDATE concurrency_holders SET lease_expires_at=? WHERE run_id=?`, expired, fixture.Input.RunID); err != nil {
				t.Fatal(err)
			}
			if reaped, err := teststore.ReapUnits(t.Context(), fixture.Store); err != nil || len(reaped) != 1 || !reaped[0].Requeued {
				t.Fatalf("recovery=%+v error=%v", reaped, err)
			}
			competitor := newStoreHostFixtureOn(t, fixture.Store, plan, runspec.Options{})
			competing := &storeAdmission{Store: competitor.Tenant, Owner: competitor.Actor}
			if _, err := competing.Run(t.Context(), competitor.Accepted, func(context.Context) (RunCompletion, error) { return RunCompletion{}, nil }); err != nil {
				t.Fatal(err)
			}
			claim, err := fixture.Store.AsOperator().ClaimUnit(t.Context(), store.UnitDemand{CapabilityPrefix: fixture.Control.CapabilityPrefix, Target: &store.UnitRef{Kind: capability.UnitTrigger, RunID: fixture.Input.RunID}, Available: match.Resources{Cores: 64, MemoryBytes: 64 << 30}})
			if err != nil {
				t.Fatal(err)
			}
			fixture.Actor = store.Actor{Kind: store.ActorRunHost, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix}, CapabilityPrefix: claim.Award.CapabilityPrefix, Owner: claim.Owner}
			fixture.Input.Owner = fixture.Actor
			fixture.Host.Around = (&storeAdmission{Store: fixture.Tenant, Owner: fixture.Actor}).Run
			completion, err := fixture.Host.Run(t.Context(), fixture.Input)
			if err != nil || completion.Outcome != "failed" {
				t.Errorf("resumed refusal=%+v error=%v", completion, err)
			}
			finished, pending := capacityRow(t, fixture, "finished"), capacityRow(t, fixture, "pending")
			if finished.Outcome != before.Outcome || finished.Attempt != before.Attempt || finished.Generation != before.Generation || !finished.FinishedAt.Equal(*before.FinishedAt) {
				t.Errorf("completed result changed: before=%s/%d/%d after=%s/%d/%d", before.Outcome, before.Attempt, before.Generation, finished.Outcome, finished.Attempt, finished.Generation)
			}
			if generator && finished.Expansion == store.ExpansionExpected {
				t.Error("refused plan retained expected generator expansion")
			}
			if pending.Outcome != store.NodeFailed || pending.Attempt != 0 {
				t.Errorf("pending result=%s attempt=%d", pending.Outcome, pending.Attempt)
			}
			run, err := fixture.Tenant.ReadRun(t.Context(), fixture.Input.RunID)
			if err != nil || run.State != store.RunFailed || run.FinishedAt == nil {
				t.Errorf("run remains active: state=%s finished=%v error=%v", run.State, run.FinishedAt, err)
			}
			snapshot, err := fixture.Tenant.ReadSnapshot(t.Context(), fixture.Input.RunID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := snapshot.Scheduling(); err != nil {
				t.Errorf("refused plan has invalid scheduling projection: %v", err)
			}
		})
	}
}

func TestOwnerPersistsStoreAdmissionRefusals(t *testing.T) {
	for _, policy := range []string{"skip", "fail", "queue"} {
		t.Run(policy, func(t *testing.T) {
			f := newStoreHostFixture(t, capacityPlan(policy, 1, "held", "refused"))
			admission := &storeAdmission{Store: f.Tenant, Owner: f.Actor}
			if err := admission.Node(t.Context(), f.Accepted, capacityRow(t, f, "held"), func(context.Context) error { return nil }); err != nil {
				t.Fatal(err)
			}
			f.Host.Admit = admission.Node
			assertOwnerRefusal(t, f, "refused", policy)
		})
	}
}

func assertOwnerRefusal(t *testing.T, f *storeHostFixture, id, policy string) {
	t.Helper()
	entry, err := f.Host.Acquire(t.Context(), f.Input)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	err = f.Host.release(ctx, f.Input, f.Accepted, entry, capacityRow(t, f, id), store.DecisionReady, func(context.Context, store.ReconcileRequest) error {
		t.Error("refused job executed")
		return nil
	})
	if err != nil {
		t.Errorf("policy escaped scheduling: %v", err)
	}
	row := capacityRow(t, f, id)
	want := store.NodeFailed
	if policy == "skip" {
		want = store.NodeSkipped
	}
	if row.Status != store.StatusDone || row.Outcome != want || row.ReadyAt != nil || row.Attempt != 0 {
		t.Errorf("refusal not persisted: status=%s outcome=%s attempt=%d, want %s", row.Status, row.Outcome, row.Attempt, want)
	}
	if policy == "queue" {
		var reason string
		if err := teststore.SQL(t, f.Store).QueryRowContext(t.Context(), `SELECT failure_reason FROM nodes WHERE run_id=? AND node_id=?`, f.Input.RunID, id).Scan(&reason); err != nil || reason != store.FailureQueueTimeout {
			t.Errorf("queue reason=%q err=%v", reason, err)
		}
	}
}

func TestOwnerPersistsPlanAdmissionRefusalAndEndsUnit(t *testing.T) {
	for _, policy := range []string{"skip", "fail", "queue"} {
		t.Run(policy, func(t *testing.T) {
			plan := planGrouped("global", policy, "body")
			plan.Concurrency[0].QueueTimeoutMS = new(int64(1))
			plan.Jobs[0].ContinueOnError = true
			plan.Jobs[0].Optional = true
			first := newStoreHostFixture(t, plan)
			admission := &storeAdmission{Store: first.Tenant, Owner: first.Actor}
			if _, err := admission.Run(t.Context(), first.Accepted, func(context.Context) (RunCompletion, error) { return RunCompletion{}, nil }); err != nil {
				t.Fatal(err)
			}
			second := newStoreHostFixtureOn(t, first.Store, plan, runspec.Options{})
			admission = &storeAdmission{Store: second.Tenant, Owner: second.Actor}
			second.Host.Around = admission.Run
			completion, err := second.Host.Run(t.Context(), second.Input)
			want := "failed"
			if policy == "skip" {
				want = "success"
			}
			if err != nil || completion.Outcome != want {
				t.Errorf("plan refusal escaped: %+v %v", completion, err)
			}
			row, err := second.Tenant.ReadRun(t.Context(), second.Input.RunID)
			if err != nil || string(row.State) != want || row.FinishedAt == nil {
				t.Errorf("plan remains active: state=%s finished=%v %v", row.State, row.FinishedAt, err)
			}
			if _, err := second.Store.AsOperator().RenewUnit(t.Context(), second.Actor.CapabilityPrefix, false); err == nil {
				t.Error("refused plan left live unit")
			}
		})
	}
}

func TestOwnerRecognizesWrappedAdmissionFailure(t *testing.T) {
	f := newStoreHostFixture(t, capacityPlan("skip", 0, "refused"))
	f.Host.Admit = func(context.Context, AcceptedRun, store.NodeRow, func(context.Context) error) error {
		return fmt.Errorf("admission transport: %w", &AdmissionFailure{Outcome: store.NodeSkipped, Err: errors.New("full")})
	}
	assertOwnerRefusal(t, f, "refused", "skip")
}

func TestApprovalSkipPolicyProducesSuccess(t *testing.T) {
	in, _, deps, _ := policyFixture(t, func(job *nodeproto.JobSpec) {
		job.Approval = &nodeproto.ApprovalSpec{}
		job.Steps = nil
		job.Closures.SkipIf = []string{"bypass"}
	})
	start := deps.Start
	deps.Start = func(ctx context.Context, input EvaluationInput) (ExecutorProcess, error) {
		p, err := start(ctx, input)
		p.(*executorProcess).answer = func(_ context.Context, request nodeproto.Request) (any, error) {
			if request.Op != nodeproto.OpEval {
				return nil, nil
			}
			return nodeproto.EvalResult{Value: []byte("true")}, nil
		}
		return p, err
	}
	evaluator, err := NewOwnerEvaluator(deps)
	if err != nil {
		t.Fatal(err)
	}
	plan := in.Accepted.Plan
	plan.Jobs = append(plan.Jobs, nodeproto.JobSpec{ID: "dependent", Needs: []string{in.NodeID}, Steps: []nodeproto.StepSpec{{ID: "body"}}})
	f := newStoreHostFixture(t, plan)
	row := capacityRow(t, f, in.NodeID)
	result, err := evaluator.skipLayer(t.Context(), in, row, func(context.Context, string, string, nodeproto.FrameLog) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if result.Transition == nil {
		t.Fatal("skip policy returned no durable decision")
	}
	change := *result.Transition
	change.Ref, change.Actor = row.Ref, f.Actor
	if _, err := f.State.TransitionNode(t.Context(), change); err != nil {
		t.Fatal(err)
	}
	row = capacityRow(t, f, in.NodeID)
	if row.Outcome != store.NodeSuccess || row.Attempt != 0 {
		t.Fatalf("approval bypass=%+v", row)
	}
	snapshot, err := f.Tenant.ReadSnapshot(t.Context(), f.Input.RunID)
	if err != nil {
		t.Fatal(err)
	}
	graph, err := snapshot.Scheduling()
	if err != nil {
		t.Fatal(err)
	}
	for _, decision := range store.Decide(graph).Nodes {
		if decision.NodeID == "dependent" && decision.Kind == store.DecisionReady {
			return
		}
	}
	t.Fatal("approval bypass did not release dependent")
}

func TestOwnerDrainsPendingSupersessionIntent(t *testing.T) {
	for _, ready := range []bool{false, true} {
		t.Run(fmt.Sprint(ready), func(t *testing.T) {
			f := newStoreHostFixture(t, nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "old", Steps: []nodeproto.StepSpec{{ID: "body"}}}}})
			if ready {
				row := capacityRow(t, f, "old")
				if _, err := f.State.TransitionNode(t.Context(), store.NodeTransition{Ref: row.Ref, Actor: f.Actor, To: store.NodeReady, Cause: store.CauseRelease}); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := teststore.SQL(t, f.Store).ExecContext(t.Context(), `UPDATE nodes SET cancel_requested_at=?,cancel_reason='superseded' WHERE run_id=? AND node_id='old'`, time.Now().UnixNano(), f.Input.RunID); err != nil {
				t.Fatal(err)
			}
			f.Host.Executor = ownerNodeFunc(func(context.Context, NodeInput) error {
				t.Error("superseded pending work executed")
				return errors.New("executed")
			})
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			completion, err := f.Host.schedule(ctx, f.Input, f.Accepted, &source.Entry{})
			if err != nil || completion.Outcome != "failed" {
				t.Fatalf("pending cancellation result=%+v err=%v", completion, err)
			}
			row := capacityRow(t, f, "old")
			if row.Outcome != store.NodeCancelled || row.Attempt != 0 || row.CancelReason != "superseded" {
				t.Fatalf("pending cancellation=%s attempt=%d", row.Outcome, row.Attempt)
			}
		})
	}
}

func TestOwnerExecutionCancellationKeepsIndependentFailure(t *testing.T) {
	fixture := newStoreHostFixture(t, nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "job", Steps: []nodeproto.StepSpec{{ID: "body"}}}}})
	row := capacityRow(t, fixture, "job")
	if _, err := fixture.State.TransitionNode(t.Context(), store.NodeTransition{Ref: row.Ref, Actor: fixture.Actor, To: store.NodeReady, Cause: store.CauseRelease}); err != nil {
		t.Fatal(err)
	}
	independent := errors.New("executor cleanup failed")
	ctx, cancel := context.WithCancelCause(t.Context())
	defer cancel(nil)
	fixture.Host.Executor = ownerNodeFunc(func(_ context.Context, input NodeInput) error {
		if err := fixture.report(t.Context(), input, store.NodeCancelled); err != nil {
			return err
		}
		cause := &lease.CancelError{Reason: "superseded"}
		cancel(cause)
		return errors.Join(cause, independent)
	})
	entry, err := fixture.Host.Acquire(ctx, fixture.Input)
	if err != nil {
		t.Fatal(err)
	}
	err = fixture.Host.execute(ctx, fixture.Input, fixture.Accepted, entry, store.ReconcileRequest{Ref: row.Ref, Actor: fixture.Actor, Policy: store.SourceNone})
	if !errors.Is(err, independent) {
		t.Fatalf("cancellation hid executor cleanup failure: %v", err)
	}
}

func FuzzOwnerPolicyAuthorityAndBinding(f *testing.F) {
	f.Add(int64(0), int64(0), string(store.PolicyAdmissionFail), false)
	f.Add(int64(1), int64(0), string(store.PolicyAdmissionSkip), false)
	f.Add(int64(0), int64(1), string(store.PolicyAdmissionFail), false)
	f.Add(int64(0), int64(0), string(store.PolicyApprovalBypass), false)
	f.Add(int64(0), int64(0), string(store.PolicyAdmissionFail), true)
	f.Fuzz(func(t *testing.T, ownerDelta, generation int64, kind string, released bool) {
		fixture := newStoreHostFixture(t, nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "job", Steps: []nodeproto.StepSpec{{ID: "body"}}}}})
		row := capacityRow(t, fixture, "job")
		if released {
			if _, err := fixture.Tenant.TransitionNode(t.Context(), store.NodeTransition{Ref: row.Ref, Actor: fixture.Actor, To: store.NodeReady, Cause: store.CauseRelease}); err != nil {
				t.Fatal(err)
			}
		}
		before, err := fixture.Tenant.ReadStateEvents(t.Context(), fixture.Input.RunID, 0, 100)
		if err != nil {
			t.Fatal(err)
		}
		actor := fixture.Actor
		actor.Owner.Generation += ownerDelta
		outcome, reason := store.NodeFailed, ""
		if kind == string(store.PolicyAdmissionSkip) {
			outcome = store.NodeSkipped
		}
		if kind == string(store.PolicyApprovalBypass) {
			outcome, reason = store.NodeSuccess, "skip_if"
		}
		if kind == string(store.PolicyQueueTimeout) {
			reason = store.FailureQueueTimeout
		}
		_, err = fixture.Tenant.TransitionNode(t.Context(), store.NodeTransition{
			Ref: row.Ref, Actor: actor, To: outcome, Cause: store.CauseOwnerPolicy,
			Extra: store.TransitionExtra{OwnerPolicy: &store.OwnerPolicyResult{Kind: store.OwnerPolicyKind(kind), Generation: generation}, Result: &store.NodeResult{Outcome: outcome, Reason: reason}},
		})
		valid := ownerDelta == 0 && generation == row.Generation && !released && (kind == string(store.PolicyAdmissionFail) || kind == string(store.PolicyAdmissionSkip) || kind == string(store.PolicyQueueTimeout))
		if (err == nil) != valid {
			t.Fatalf("owner delta=%d generation=%d kind=%q released=%v error=%v", ownerDelta, generation, kind, released, err)
		}
		if !valid {
			after, readErr := fixture.Tenant.ReadStateEvents(t.Context(), fixture.Input.RunID, 0, 100)
			if readErr != nil || len(after) != len(before) {
				t.Fatalf("refused policy appended events: %d -> %d %v", len(before), len(after), readErr)
			}
			current := capacityRow(t, fixture, "job")
			if current.Status != store.StatusPending || current.Outcome != "" {
				t.Fatalf("refused policy changed state: %s/%s", current.Status, current.Outcome)
			}
		}
	})
}
