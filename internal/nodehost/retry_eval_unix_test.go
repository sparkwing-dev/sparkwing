//go:build unix

package nodehost

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func retryGeneratedJobs() []nodeproto.JobSpec {
	return []nodeproto.JobSpec{
		{ID: "good", Groups: []string{testGenerator}, Needs: []string{"targets"}, Steps: []nodeproto.StepSpec{{ID: "body"}}},
		{ID: "bad", Groups: []string{testGenerator}, Needs: []string{"targets"}, Steps: []nodeproto.StepSpec{{ID: "body"}}},
	}
}

func retryWithRecordedExpansion(t *testing.T) (*storeHostFixture, NodeInput) {
	t.Helper()
	plan := fanOutDoc()
	plan.Jobs, plan.Jobs[1].Needs = plan.Jobs[:2], nil
	f := newStoreHostFixture(t, plan)
	source := claimedFixtureNode(t, f, "targets")
	if err := f.report(t.Context(), source, store.NodeSuccess); err != nil {
		t.Fatal(err)
	}
	acceptedFixtureExpansion(t, f, source, retryGeneratedJobs()...)
	if err := f.report(t.Context(), claimedFixtureNode(t, f, "good"), store.NodeSuccess); err != nil {
		t.Fatal(err)
	}
	return f, source
}

func TestRetryTargetSchedulesOnlyUnfinishedGeneratedWork(t *testing.T) {
	f, _ := retryWithRecordedExpansion(t)
	var bodies []string
	var mu sync.Mutex
	f.Host.Executor = ownerNodeFunc(func(ctx context.Context, input NodeInput) error {
		mu.Lock()
		bodies = append(bodies, input.NodeID)
		mu.Unlock()
		return f.report(ctx, input, store.NodeSuccess)
	})
	f.Host.Expand = func(context.Context, EvaluationInput, store.NodeRow, StorageBinding) (json.RawMessage, *store.ExpansionFailure, error) {
		t.Fatal("recorded expansion evaluated again")
		return nil, nil, nil
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	completion, err := f.Host.Run(ctx, f.Input)
	if err != nil || completion.Outcome != "success" || !slices.Equal(bodies, []string{"bad", "summary"}) {
		t.Fatalf("bodies=%v completion=%+v error=%v", bodies, completion, err)
	}
}

type retryMissingOutput struct{ OwnerStore }

func (s retryMissingOutput) ReadSnapshot(ctx context.Context, runID string) (store.RunSnapshot, error) {
	snapshot, err := s.OwnerStore.ReadSnapshot(ctx, runID)
	snapshot.Run.Retry = &store.RetrySelection{Mode: "failed", Results: map[string]store.MaterializedResultSource{"targets": {Ref: controllerwire.NodeRef{RunID: "previous", NodeID: "targets"}, StateSeq: 8}}}
	return snapshot, err
}

func (s retryMissingOutput) ReconcileNode(ctx context.Context, request store.ReconcileRequest) (store.ReconciledNode, error) {
	if request.Policy != store.SourceManualRetry {
		return s.OwnerStore.ReconcileNode(ctx, request)
	}
	snapshot, err := s.OwnerStore.ReadSnapshot(ctx, request.Ref.RunID)
	row, ok := ownerNode(snapshot, request.Ref.NodeID)
	if !request.MaterializeOnly || !ok || request.Source == nil {
		return store.ReconciledNode{}, executorError(apierr.BadRequest, "retry miss requires a materialization probe")
	}
	return store.ReconciledNode{Kind: store.Dispatch, Node: row}, err
}

func TestRetryReuseMissExecutesGeneratorAndChildren(t *testing.T) {
	plan := fanOutDoc()
	plan.Jobs, plan.Jobs[1].Needs = plan.Jobs[:2], nil
	f := newStoreHostFixture(t, plan)
	f.Host.Store = retryMissingOutput{f.State}

	var bodies []string
	var mu sync.Mutex
	f.Host.Executor = ownerNodeFunc(func(ctx context.Context, input NodeInput) error {
		mu.Lock()
		bodies = append(bodies, input.NodeID)
		mu.Unlock()
		return f.report(ctx, input, store.NodeSuccess)
	})
	f.Host.Expand = func(context.Context, EvaluationInput, store.NodeRow, StorageBinding) (json.RawMessage, *store.ExpansionFailure, error) {
		body, err := json.Marshal(nodeproto.PlanDoc{Jobs: retryGeneratedJobs()})
		return body, nil, err
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	completion, err := f.Host.Run(ctx, f.Input)
	slices.Sort(bodies)
	if err != nil || completion.Outcome != "success" || !slices.Equal(bodies, []string{"bad", "good", "summary", "targets"}) {
		t.Fatalf("bodies=%v completion=%+v error=%v", bodies, completion, err)
	}
}

func TestMaterializedGeneratorEvaluationKeepsTheJobTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		input, state, deps, process := evaluationFixture(t)
		state.row.Outcome = store.NodeMemoized
		state.row.Execution.Timeout.Value = 400 * time.Millisecond
		deps.ChildContext = nil
		process.answer = func(ctx context.Context, request nodeproto.Request) (any, error) {
			if request.Op != nodeproto.OpEval {
				return nil, nil
			}
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) != 400*time.Millisecond {
				t.Fatal("generator did not receive the source job timeout")
			}
			<-ctx.Done()
			return nil, ctx.Err()
		}
		host, err := NewOwnerEvaluator(deps)
		if err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		body, failure, err := host.Expand(t.Context(), input)
		if err != nil || len(body) != 0 || failure == nil || failure.Code != apierr.DeadlineExceeded || !strings.Contains(failure.MaskedError, "timeout") || time.Since(start) != 400*time.Millisecond {
			t.Fatalf("body=%s failure=%+v elapsed=%s error=%v", body, failure, time.Since(start), err)
		}
		if process.closed != 1 || process.cleaned != 1 || len(state.changes) != 0 {
			t.Fatal("expired evaluation did not join without writing topology")
		}
	})
}

func TestGeneratedRetryExecutionRefusesChangedRecordedDeclarations(t *testing.T) {
	f, sourceInput := retryWithRecordedExpansion(t)
	input := claimedFixtureNode(t, f, "bad")
	input.Entry = &source.Entry{Engine: input.Engine}
	before, err := f.Tenant.ReadSnapshot(t.Context(), f.Accepted.RunID)
	if err != nil {
		t.Fatal(err)
	}
	process := &executorProcess{plan: f.Accepted.Plan}
	process.answer = func(_ context.Context, request nodeproto.Request) (any, error) {
		if request.Op != nodeproto.OpEval {
			return nil, nil
		}
		jobs := retryGeneratedJobs()
		jobs[0].Steps[0].ID = "changed"
		raw, err := json.Marshal(jobs)
		return nodeproto.EvalResult{Value: raw}, err
	}
	host, err := NewNodeExecutor(ExecutorDependencies{
		Store: f.State,
		Start: func(context.Context, NodeInput, ExecutorActivity) (ExecutorProcess, error) { return process, nil },
		EnsureEngine: func(context.Context, engine.Identity, releaseasset.Target) (engine.Resolved, error) {
			return input.Engine, nil
		},
		Mask:       func(text string) string { return text },
		Validation: nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: noV2Secrets{}},
		ReadOutput: func(_ context.Context, ref controllerwire.NodeRef, generation int64) (json.RawMessage, error) {
			if ref.RunID != f.Accepted.RunID || ref.NodeID != "targets" || generation != sourceInput.Generation {
				t.Fatal("reconstruction read outside its accepted source")
			}
			return json.RawMessage(`["good","bad"]`), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	err = host.Execute(ctx, input)
	if hostCode(err) != apierr.PlanDrift {
		t.Fatalf("changed generator error=%v, want plan_drift", err)
	}
	for _, request := range process.requests {
		if request.Op == nodeproto.OpRunStep {
			t.Fatal("changed generated code executed a body")
		}
	}
	after, err := f.Tenant.ReadSnapshot(t.Context(), f.Accepted.RunID)
	if err != nil || !reflect.DeepEqual(before.Expansions, after.Expansions) {
		t.Fatalf("recorded expansion changed: %v", err)
	}
	good, ok := ownerNode(after, "good")
	bad, failed := ownerNode(after, "bad")
	if !ok || good.Outcome != store.NodeSuccess || !failed || bad.Outcome != store.NodeFailed || process.closed != 1 || process.cleaned != 1 {
		t.Fatalf("good=%+v bad=%+v process=%+v", good, bad, process)
	}
}
