//go:build unix

package nodehost

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func TestNativeApprovalPredicateRetainsAdmissionUntilJoined(t *testing.T) {
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
			if err != nil || len(capacity.Holders) != 1 || capacity.UsedCost != 1 {
				t.Errorf("approval predicate entered without its group: %+v, %v", capacity, err)
			}
			if live, granted, released := daemon.counts(); live != 1 || granted != 1 || released != 0 {
				t.Errorf("approval predicate entered without compute: %d/%d/%d", live, granted, released)
			}
			process = &executorProcess{plan: plan, answer: func(_ context.Context, request nodeproto.Request) (any, error) {
				if request.Op == nodeproto.OpEval {
					return nodeproto.EvalResult{Value: json.RawMessage("true")}, nil
				}
				return nil, nil
			}}
			return executorCleanupProcess{process, func(context.Context) error {
				if live, _, released := daemon.counts(); live != 1 || released != 0 {
					t.Error("approval released compute before its process joined")
				}
				return nil
			}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	configureNativeAdmission(&f.Host, &LocalAdmission{Daemon: daemon}, &storeAdmission{Store: f.Tenant, Owner: f.Actor}, false)
	f.Host.SkipPolicy = func(ctx context.Context, in EvaluationInput, row store.NodeRow, _ StorageBinding) (OwnerLayerResult, error) {
		return evaluator.skipLayer(ctx, in, row, func(context.Context, string, string, nodeproto.FrameLog) error { return nil })
	}
	result, err := f.Host.Run(ctx, f.Input)
	if err != nil || result.Outcome != "success" {
		t.Fatalf("approval bypass: %+v, %v", result, err)
	}
	row, err := f.Tenant.ReadNode(ctx, controllerwire.NodeRef{RunID: f.Input.RunID, NodeID: job.ID})
	if err != nil || row.Status != store.StatusDone || row.Outcome != store.NodeSuccess || row.Attempt != 0 {
		t.Fatalf("approval bypass created execution: %+v, %v", row, err)
	}
	if process == nil || process.closed != 1 || process.cleaned != 1 {
		t.Fatal("approval predicate process was not joined")
	}
	if live, granted, released := daemon.counts(); live != 0 || granted != 1 || released != 1 {
		t.Fatalf("approval retained compute: %d/%d/%d", live, granted, released)
	}
	capacity, err := f.Tenant.GetConcurrencyState(ctx, "job:global:approval")
	if err != nil || len(capacity.Holders) != 0 || len(capacity.Waiters) != 0 {
		t.Fatalf("approval bypass retained its group: %+v, %v", capacity, err)
	}
}
