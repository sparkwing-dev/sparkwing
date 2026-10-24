package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

type admissionAcquireFunc func(context.Context, wingwire.AdmissionRequest, func(wingwire.Queued)) (Lease, error)

func (f admissionAcquireFunc) Acquire(ctx context.Context, request wingwire.AdmissionRequest, queued func(wingwire.Queued)) (Lease, error) {
	return f(ctx, request, queued)
}

func TestApprovalSkipPolicyPreservesSkip(t *testing.T) {
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
	evaluator, err := newOwnerEvaluator(deps)
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
	change := store.BindNodeCommand(result.Transition, row.Ref, f.Actor)
	if _, err := f.State.TransitionNode(t.Context(), change); err != nil {
		t.Fatal(err)
	}
	row = capacityRow(t, f, in.NodeID)
	if row.Outcome != runstate.NodeSkipped || row.Attempt != 0 {
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
				if _, err := f.State.TransitionNode(t.Context(), store.ReadyNode{Ref: row.Ref, Actor: f.Actor}); err != nil {
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
			if row.Outcome != runstate.NodeCancelled || row.Attempt != 0 || row.CancelReason != "superseded" {
				t.Fatalf("pending cancellation=%s attempt=%d", row.Outcome, row.Attempt)
			}
		})
	}
}

func TestOwnerExecutionCancellationKeepsIndependentFailure(t *testing.T) {
	fixture := newStoreHostFixture(t, nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "job", Steps: []nodeproto.StepSpec{{ID: "body"}}}}})
	row := capacityRow(t, fixture, "job")
	if _, err := fixture.State.TransitionNode(t.Context(), store.ReadyNode{Ref: row.Ref, Actor: fixture.Actor}); err != nil {
		t.Fatal(err)
	}
	independent := errors.New("executor cleanup failed")
	ctx, cancel := context.WithCancelCause(t.Context())
	defer cancel(nil)
	fixture.Host.Executor = ownerNodeFunc(func(_ context.Context, input NodeInput) error {
		if err := fixture.report(t.Context(), input, runstate.NodeCancelled); err != nil {
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
	err = fixture.Host.execute(ctx, fixture.Input, fixture.Accepted, entry, store.ReconcileRequest{Ref: row.Ref, Actor: fixture.Actor, Policy: runstate.SourceNone})
	if !errors.Is(err, independent) {
		t.Fatalf("cancellation hid executor cleanup failure: %v", err)
	}
}

func TestOwnerCannotAssertAdmissionOutcomes(t *testing.T) {
	for _, kind := range []string{"admission_skip", "admission_fail", "admission_queue_timeout"} {
		for _, level := range []string{"job", "run"} {
			t.Run(kind+"/"+level, func(t *testing.T) {
				fixture := newStoreHostFixture(t, nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "job", Steps: []nodeproto.StepSpec{{ID: "body"}}}}})
				row := capacityRow(t, fixture, "job")
				before, err := fixture.Tenant.ReadSnapshot(t.Context(), fixture.Input.RunID)
				if err != nil {
					t.Fatal(err)
				}
				outcome, reason := runstate.NodeFailed, ""
				if kind == "admission_skip" {
					outcome = runstate.NodeSkipped
				}
				if kind == "admission_queue_timeout" {
					reason = store.FailureQueueTimeout
				}
				policy := &runstate.OwnerPolicyResult{Kind: runstate.OwnerPolicyKind(kind), Generation: row.Generation}
				if level == "job" {
					_, err = fixture.Tenant.TransitionNode(t.Context(), store.OwnerPolicyNode{Ref: row.Ref, Actor: fixture.Actor, Outcome: outcome, OwnerPolicy: policy, Result: &runstate.NodeResult{Outcome: outcome, Reason: reason}})
				} else {
					policy.Generation = fixture.Actor.Owner.Generation
					payload, marshalErr := json.Marshal(map[string]any{"OwnerPolicy": policy, "Cause": "policy_refused", "Reason": reason})
					if marshalErr != nil {
						t.Fatal(marshalErr)
					}
					var completion runstate.RunCompletion
					if err := json.Unmarshal(payload, &completion); err != nil {
						t.Fatal(err)
					}
					completion.RunID, completion.Actor = runspec.RunID(fixture.Input.RunID), fixture.Actor
					_, err = fixture.Tenant.FinishRunAndUnit(t.Context(), runstate.FinishRunRequest{Transition: completion, Unit: fixture.Actor.Owner})
				}
				if err == nil {
					t.Fatal("owner asserted an admission outcome")
				}
				after, err := fixture.Tenant.ReadSnapshot(t.Context(), fixture.Input.RunID)
				if err != nil || after.Run.State != before.Run.State || after.Run.FinishedAt != nil || after.Nodes[0].Status != row.Status || after.Nodes[0].Outcome != row.Outcome || after.Nodes[0].StateSeq != row.StateSeq {
					t.Fatalf("refused admission assertion changed pending work: %+v %v", after, err)
				}
			})
		}
	}
}

func FuzzOwnerPolicyAuthorityAndBinding(f *testing.F) {
	f.Add(int64(0), int64(0), "admission_fail", false)
	f.Add(int64(1), int64(0), "admission_skip", false)
	f.Add(int64(0), int64(1), "admission_fail", false)
	f.Add(int64(0), int64(0), string(runstate.PolicyApprovalBypass), false)
	f.Add(int64(0), int64(0), "admission_fail", true)
	f.Fuzz(func(t *testing.T, ownerDelta, generation int64, kind string, released bool) {
		fixture := newStoreHostFixture(t, nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "job", Steps: []nodeproto.StepSpec{{ID: "body"}}}}})
		row := capacityRow(t, fixture, "job")
		if released {
			if _, err := fixture.Tenant.TransitionNode(t.Context(), store.ReadyNode{Ref: row.Ref, Actor: fixture.Actor}); err != nil {
				t.Fatal(err)
			}
		}
		before, err := fixture.Tenant.ReadStateEvents(t.Context(), fixture.Input.RunID, 0, 100)
		if err != nil {
			t.Fatal(err)
		}
		actor := fixture.Actor
		actor.Owner.Generation += runspec.ClaimGeneration(ownerDelta)
		outcome, reason := runstate.NodeFailed, ""
		if kind == "admission_skip" {
			outcome = runstate.NodeSkipped
		}
		if kind == string(runstate.PolicyApprovalBypass) {
			outcome, reason = runstate.NodeSuccess, "skip_if"
		}
		if kind == "admission_queue_timeout" {
			reason = store.FailureQueueTimeout
		}
		_, err = fixture.Tenant.TransitionNode(t.Context(), store.OwnerPolicyNode{Ref: row.Ref, Actor: actor, OwnerPolicy: &runstate.OwnerPolicyResult{Kind: runstate.OwnerPolicyKind(kind), Generation: runspec.ClaimGeneration(generation)}, Result: &runstate.NodeResult{Outcome: outcome, Reason: reason}, Outcome: outcome})
		if err == nil {
			t.Fatalf("owner delta=%d generation=%d kind=%q released=%v error=%v", ownerDelta, generation, kind, released, err)
		}
		after, readErr := fixture.Tenant.ReadStateEvents(t.Context(), fixture.Input.RunID, 0, 100)
		if readErr != nil || len(after) != len(before) {
			t.Fatalf("refused policy appended events: %d -> %d %v", len(before), len(after), readErr)
		}
		current := capacityRow(t, fixture, "job")
		if current.Status != runstate.StatusPending || current.Outcome != "" {
			t.Fatalf("refused policy changed state: %s/%s", current.Status, current.Outcome)
		}
	})
}
