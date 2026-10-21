//go:build unix

package nodehost

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func TestNativeApprovalPredicateSkipsWithoutJobCapacityOrCompute(t *testing.T) {
	job := nodeproto.JobSpec{ID: "gate", Approval: &nodeproto.ApprovalSpec{Message: "ship?"}, Closures: nodeproto.ClosureSpec{SkipIf: []string{"guard"}}}
	job.Concurrency = []nodeproto.ConcurrencySpec{{Group: "approval", Scope: new("global"), Capacity: new(1), Cost: new(1), OnLimit: new("queue")}}
	plan := nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{job}}
	f := newStoreHostFixture(t, plan)
	ctx := t.Context()
	var spec runspec.Spec
	if err := json.Unmarshal(f.Accepted.Spec, &spec); err != nil {
		t.Fatal(err)
	}
	selected := engine.Resolved{Identity: spec.Resolved.Engine}
	daemon := newFakeDaemon()
	var process *executorProcess
	evaluator, err := NewOwnerEvaluator(EvaluationDependencies{
		Store: f.State, Mask: func(s string) string { return s },
		Validation: nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: noV2Secrets{}},
		EnsureEngine: func(context.Context, engine.Identity, releaseasset.Target) (engine.Resolved, error) {
			return selected, nil
		},
		Start: func(context.Context, EvaluationInput) (ExecutorProcess, error) {
			capacity, err := f.Tenant.GetConcurrencyState(ctx, "job:global:approval")
			if !errors.Is(err, store.ErrNotFound) && (err != nil || len(capacity.Holders) != 0 || capacity.UsedCost != 0) {
				t.Errorf("approval predicate held job groups: %+v, %v", capacity, err)
			}
			if live, granted, released := daemon.counts(); live != 0 || granted != 0 || released != 0 {
				t.Errorf("approval predicate acquired compute: %d/%d/%d", live, granted, released)
			}
			process = &executorProcess{plan: plan, answer: func(_ context.Context, request nodeproto.Request) (any, error) {
				if request.Op == nodeproto.OpEval {
					return nodeproto.EvalResult{Value: json.RawMessage("true")}, nil
				}
				return nil, nil
			}}
			return executorCleanupProcess{process, func(context.Context) error {
				if live, _, released := daemon.counts(); live != 0 || released != 0 {
					t.Error("approval released compute before its process joined")
				}
				return nil
			}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	f.Host.executionAdmission = ComposeStartGate(f.Tenant, &LocalAdmission{Daemon: daemon}, false)
	f.Host.SkipPolicy = func(ctx context.Context, in EvaluationInput, row store.NodeRow, _ StorageBinding) (OwnerLayerResult, error) {
		return evaluator.skipLayer(ctx, in, row, func(context.Context, string, string, nodeproto.FrameLog) error { return nil })
	}
	result, err := f.Host.Run(ctx, f.Input)
	if err != nil || result.Outcome != "success" {
		t.Fatalf("approval bypass: %+v, %v", result, err)
	}
	row, err := f.Tenant.ReadNode(ctx, controllerwire.NodeRef{RunID: runspec.RunID(f.Input.RunID), NodeID: runspec.NodeID(job.ID)})
	if err != nil || row.Status != store.StatusDone || row.Outcome != store.NodeSkipped || row.Attempt != 0 {
		t.Fatalf("approval bypass created execution: %+v, %v", row, err)
	}
	if process == nil || process.closed != 1 || process.cleaned != 1 {
		t.Fatal("approval predicate process was not joined")
	}
	if live, granted, released := daemon.counts(); live != 0 || granted != 0 || released != 0 {
		t.Fatalf("approval retained compute: %d/%d/%d", live, granted, released)
	}
	capacity, err := f.Tenant.GetConcurrencyState(ctx, "job:global:approval")
	if !errors.Is(err, store.ErrNotFound) && (err != nil || len(capacity.Holders) != 0 || len(capacity.Waiters) != 0) {
		t.Fatalf("approval bypass retained its group: %+v, %v", capacity, err)
	}
}

func TestControllerApprovalEvaluationRunsPredicateAndCommitsTypedResult(t *testing.T) {
	for _, mode := range []string{"open", "skip", "fail"} {
		t.Run(mode, func(t *testing.T) {
			job := nodeproto.JobSpec{ID: "gate", Approval: &nodeproto.ApprovalSpec{Message: "ship?"}, Closures: nodeproto.ClosureSpec{SkipIf: []string{"guard"}}, Concurrency: []nodeproto.ConcurrencySpec{{Group: "approval", Scope: new("global"), Capacity: new(1), Cost: new(1), OnLimit: new("queue")}}}
			f := newStoreHostFixture(t, nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{job}})
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			if _, err := teststore.SQL(t, f.Store).ExecContext(ctx, `UPDATE runs SET dispatch='controller' WHERE id=?`, f.Input.RunID); err != nil {
				t.Fatal(err)
			}
			controllerLease, err := f.Store.AsOperator().AcquireSingletonLease(ctx, "controller.owner", "controller", time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.Store.AsOperator().ReconcileControllerRuns(ctx, controllerLease, "", 100); err != nil {
				t.Fatal(err)
			}
			award, err := f.Store.AsOperator().ClaimUnit(ctx, store.UnitDemand{CapabilityPrefix: f.Control.CapabilityPrefix, Target: &store.UnitRef{Kind: capability.UnitEval, RunID: runspec.RunID(f.Input.RunID), NodeID: "gate", SourceGeneration: 0}, Available: match.Resources{Cores: 64, MemoryBytes: 64 << 30}})
			if err != nil {
				t.Fatal(err)
			}
			principal, err := f.Store.AsOperator().LookupCapability(ctx, sha256.Sum256(award.Bearer))
			if err != nil || principal.Capability.Binding.Expansion == nil {
				t.Fatalf("eval binding=%+v error=%v", principal.Capability.Binding, err)
			}
			input := f.Input
			input.Owner = store.Actor{Kind: store.ActorRunHost, Authority: principal.Authority, CapabilityPrefix: award.Award.CapabilityPrefix, Owner: award.Owner}
			var accepted runspec.Spec
			if err := json.Unmarshal(f.Accepted.Spec, &accepted); err != nil {
				t.Fatal(err)
			}
			selected := engine.Resolved{Identity: accepted.Resolved.Engine}
			var process *executorProcess
			evaluator, err := NewOwnerEvaluator(EvaluationDependencies{Store: f.State, Mask: func(s string) string { return s }, Validation: nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: noV2Secrets{}}, EnsureEngine: func(context.Context, engine.Identity, releaseasset.Target) (engine.Resolved, error) {
				return selected, nil
			}, Start: func(context.Context, EvaluationInput) (ExecutorProcess, error) {
				if state, err := f.Tenant.GetConcurrencyState(ctx, "job:global:approval"); !errors.Is(err, store.ErrNotFound) && (err != nil || len(state.Holders) != 0) {
					t.Fatalf("approval evaluator held job capacity: %+v error=%v", state, err)
				}
				process = &executorProcess{plan: f.Accepted.Plan, answer: func(_ context.Context, request nodeproto.Request) (any, error) {
					if request.Op != nodeproto.OpEval {
						return nil, nil
					}
					if mode == "fail" {
						return nodeproto.EvalResult{Error: &apierr.Error{Code: apierr.SDKError, Message: "predicate failed"}}, nil
					}
					return nodeproto.EvalResult{Value: json.RawMessage(map[bool]string{false: "false", true: "true"}[mode == "skip"])}, nil
				}}
				return process, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			f.Host.SkipPolicy = func(ctx context.Context, in EvaluationInput, row store.NodeRow, _ StorageBinding) (OwnerLayerResult, error) {
				return evaluator.skipLayer(ctx, in, row, func(context.Context, string, string, nodeproto.FrameLog) error { return nil })
			}
			f.Host.executionAdmission.compute = &LocalAdmission{Daemon: admissionAcquireFunc(func(context.Context, wingwire.AdmissionRequest, func(wingwire.Queued)) (Lease, error) {
				t.Fatal("approval evaluator acquired compute")
				return nil, nil
			})}
			if _, err := f.Store.AsOperator().RenewUnit(ctx, award.Award.CapabilityPrefix, true); err != nil { t.Fatalf("eval renewal: %v", err) }
 if err := f.Host.Evaluate(ctx, input, "gate", 0, *principal.Capability.Binding.Expansion); err != nil {
				t.Fatal(err)
			}
			row := capacityRow(t, f, "gate")
			if mode == "open" && row.Status != store.StatusApprovalPending || mode == "skip" && row.Outcome != store.NodeSkipped || mode == "fail" && row.Outcome != store.NodeFailed || row.Attempt != 0 {
				t.Fatalf("mode=%s result=%+v", mode, row)
			}
			var status string
			if err := teststore.SQL(t, f.Store).QueryRowContext(ctx, `SELECT status FROM eval_units WHERE run_id=? AND source_node_id='gate' AND source_generation=0`, f.Input.RunID).Scan(&status); err != nil || status != "done" {
				t.Fatalf("eval status=%s error=%v", status, err)
			}
			if process == nil || process.closed != 1 || process.cleaned != 1 {
				t.Fatal("approval evaluator process did not join")
			}
		})
	}
}
