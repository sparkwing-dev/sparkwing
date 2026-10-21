package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

const testRunID = "run-sched"

func openStore(t *testing.T, opts ...store.Option) *store.Store {
	t.Helper()
	st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"), append([]store.Option{store.WithPolicies(func(*store.Store) (store.Policies, error) {
		cipher, err := secrets.NewCipher(make([]byte, secrets.KeySize))
		return store.Policies{Intake: hostIntakePolicy{}, ClaimProfiles: hostEnrollment{}, SessionKeys: cipher}, err
	})}, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func deps(ids ...string) []string { return ids }

func TestSchedulerRunsADiamondInOrderAndInParallel(t *testing.T) {
	f := newStoreHostFixture(t, nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{
		{ID: "d", Needs: deps("b", "c"), Steps: []nodeproto.StepSpec{{ID: "body"}}},
		{ID: "b", Needs: deps("a"), Steps: []nodeproto.StepSpec{{ID: "body"}}},
		{ID: "c", Needs: deps("a"), Steps: []nodeproto.StepSpec{{ID: "body"}}},
		{ID: "a", Steps: []nodeproto.StepSpec{{ID: "body"}}},
	}})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	b, c := make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	var order []string
	f.Host.Executor = ownerNodeFunc(func(ctx context.Context, input NodeInput) error {
		mu.Lock()
		order = append(order, input.NodeID)
		mu.Unlock()
		var mine, other chan struct{}
		if input.NodeID == "b" {
			mine, other = b, c
		}
		if input.NodeID == "c" {
			mine, other = c, b
		}
		if mine != nil {
			close(mine)
			select {
			case <-other:
			case <-ctx.Done():
				return context.Cause(ctx)
			}
		}
		return f.report(ctx, input, store.NodeSuccess)
	})
	completion, err := f.Host.Run(ctx, f.Input)
	if err != nil || completion.Outcome != "success" {
		t.Fatalf("completion=%+v error=%v", completion, err)
	}
	if len(order) != 4 || order[0] != "a" || order[3] != "d" {
		t.Fatalf("order=%v", order)
	}
	for _, id := range []string{"a", "b", "c", "d"} {
		row, err := f.Tenant.ReadNode(ctx, controllerwire.NodeRef{RunID: runspec.RunID(f.Accepted.RunID), NodeID: runspec.NodeID(id)})
		if err != nil || row.Status != store.StatusDone || row.Outcome != store.NodeSuccess {
			t.Errorf("%s row=%+v error=%v", id, row, err)
		}
	}
}

func TestSchedulerPropagatesFailureAsTheDispatcherDoes(t *testing.T) {
	f := newStoreHostFixture(t, nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{
		{ID: "build", Steps: []nodeproto.StepSpec{{ID: "body"}}},
		{ID: "test", Needs: deps("build"), Steps: []nodeproto.StepSpec{{ID: "body"}}},
		{ID: "deploy", Needs: deps("test"), Steps: []nodeproto.StepSpec{{ID: "body"}}},
		{ID: "build-recover", OnFailureOf: "build", Steps: []nodeproto.StepSpec{{ID: "body"}}},
		{ID: "docs", Steps: []nodeproto.StepSpec{{ID: "body"}}},
		{ID: "docs-recover", OnFailureOf: "docs", Steps: []nodeproto.StepSpec{{ID: "body"}}},
		{ID: "lint", ContinueOnError: true, Steps: []nodeproto.StepSpec{{ID: "body"}}},
		{ID: "after-lint", Needs: deps("lint"), Steps: []nodeproto.StepSpec{{ID: "body"}}},
		{ID: "flaky", Steps: []nodeproto.StepSpec{{ID: "body"}}},
		{ID: "after-flaky", NeedsIfPresent: deps("flaky", "absent"), Steps: []nodeproto.StepSpec{{ID: "body"}}},
		{ID: "maybe", Optional: true, Steps: []nodeproto.StepSpec{{ID: "body"}}},
	}})
	var mu sync.Mutex
	ran := map[string]bool{}
	f.Host.Executor = ownerNodeFunc(func(ctx context.Context, input NodeInput) error {
		mu.Lock()
		ran[input.NodeID] = true
		mu.Unlock()
		outcome := store.NodeSuccess
		if slices.Contains([]string{"build", "lint", "flaky", "maybe"}, input.NodeID) {
			outcome = store.NodeFailed
		}
		return f.report(ctx, input, outcome)
	})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	completion, err := f.Host.Run(ctx, f.Input)
	if err != nil || completion.Outcome != "failed" {
		t.Fatalf("completion=%+v error=%v", completion, err)
	}
	for id, want := range map[string]store.NodeState{"build": store.NodeFailed, "test": store.NodeCancelled, "deploy": store.NodeCancelled, "build-recover": store.NodeSuccess, "docs-recover": store.NodeSkipped, "after-lint": store.NodeSuccess, "after-flaky": store.NodeCancelled, "maybe": store.NodeFailed} {
		row, err := f.Tenant.ReadNode(ctx, controllerwire.NodeRef{RunID: runspec.RunID(f.Accepted.RunID), NodeID: runspec.NodeID(id)})
		if err != nil || row.Outcome != want || row.Status != store.StatusDone {
			t.Errorf("%s row=%+v error=%v, want %s", id, row, err, want)
		}
	}
	for _, id := range []string{"test", "deploy", "docs-recover", "after-flaky"} {
		if ran[id] {
			t.Errorf("%s ran", id)
		}
	}
}

func TestSchedulerOptionalFailureLeavesTheRunGreen(t *testing.T) {
	f := newStoreHostFixture(t, nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{
		{ID: "maybe", Optional: true, Steps: []nodeproto.StepSpec{{ID: "body"}}},
		{ID: "after", NeedsIfPresent: deps("absent"), Steps: []nodeproto.StepSpec{{ID: "body"}}},
	}})
	f.Host.Executor = ownerNodeFunc(func(ctx context.Context, input NodeInput) error {
		outcome := store.NodeSuccess
		if input.NodeID == "maybe" {
			outcome = store.NodeFailed
		}
		return f.report(ctx, input, outcome)
	})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	completion, err := f.Host.Run(ctx, f.Input)
	if err != nil || completion.Outcome != "success" {
		t.Fatalf("completion=%+v error=%v", completion, err)
	}
	row, err := f.Tenant.ReadNode(ctx, controllerwire.NodeRef{RunID: runspec.RunID(f.Accepted.RunID), NodeID: "after"})
	if err != nil || row.Outcome != store.NodeSuccess {
		t.Fatalf("row=%+v error=%v", row, err)
	}
	optional, err := f.Tenant.ReadNode(ctx, controllerwire.NodeRef{RunID: runspec.RunID(f.Accepted.RunID), NodeID: "maybe"})
	if err != nil || optional.Status != store.StatusDone || optional.Outcome != store.NodeFailed {
		t.Fatalf("optional row=%+v error=%v", optional, err)
	}
	run, err := f.Tenant.ReadRun(ctx, f.Accepted.RunID)
	if err != nil || run.State != store.RunSuccess {
		t.Fatalf("run=%+v error=%v", run, err)
	}
}

func TestSchedulerWritesPendingRowsFromThePlan(t *testing.T) {
	f := newStoreHostFixture(t, nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{
		{ID: "base", Requires: []string{"linux"}, Steps: []nodeproto.StepSpec{{ID: "body"}}},
		{ID: "gpu", Needs: deps("base"), NeedsIfPresent: deps("extra"), Requires: []string{"gpu", "linux", "big"}, Prefers: []string{"fast"}, Resources: &nodeproto.ResourceSpec{Cores: new(float64(4)), MemoryBytes: new(int64(1 << 30))}, Steps: []nodeproto.StepSpec{{ID: "body"}}},
	}})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	snapshot, err := f.Tenant.ReadSnapshot(ctx, f.Accepted.RunID)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range snapshot.Nodes {
		if row.Status != store.StatusPending || row.StartedAt != nil {
			t.Errorf("initial row=%+v", row)
		}
		if row.Ref.NodeID == "gpu" {
			var job nodeproto.JobSpec
			if err := json.Unmarshal(row.Spec, &job); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(job.Needs, []string{"base"}) || !slices.Equal(job.NeedsIfPresent, []string{"extra"}) || !slices.Equal(row.Execution.Requires.Value, []string{"gpu", "linux", "big"}) || !slices.Equal(row.Execution.Prefers.Value, []string{"fast"}) || row.Execution.Resources.Value.Cores.Value != 4 || row.Execution.Resources.Value.MemoryBytes.Value != 1<<30 {
				t.Fatalf("job=%+v execution=%+v", job, row.Execution)
			}
		}
	}
}

func TestSchedulerCancelMidRunFinishesEveryRow(t *testing.T) {
	f := newStoreHostFixture(t, nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{
		{ID: "slow", Steps: []nodeproto.StepSpec{{ID: "body"}}},
		{ID: "next", Needs: deps("slow"), Steps: []nodeproto.StepSpec{{ID: "body"}}},
		{ID: "later", Needs: deps("next"), Steps: []nodeproto.StepSpec{{ID: "body"}}},
	}})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	f.Host.Executor = ownerNodeFunc(func(ctx context.Context, input NodeInput) error {
		if input.NodeID != "slow" {
			return errors.New("downstream body started during cancellation")
		}
		ref := controllerwire.NodeRef{RunID: runspec.RunID(f.Accepted.RunID), NodeID: runspec.NodeID(input.NodeID)}
		actor := store.Actor{Kind: store.ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: input.Authority.CapabilityPrefix}, CapabilityPrefix: input.Authority.CapabilityPrefix, NodeFence: runspec.NodeFence{RunID: runspec.RunID(input.Accepted.RunID), NodeID: runspec.NodeID(input.NodeID), Holder: input.Authority.Holder, Generation: input.Generation}}
		started, err := f.State.TransitionNode(ctx, store.StartNode{Ref: ref, Actor: actor, SourceAttempt: new(input.Attempt)})
		if err != nil {
			return err
		}
		if _, err := f.Tenant.TransitionRun(ctx, store.RunTransition{RunID: runspec.RunID(f.Accepted.RunID), Actor: f.Control, To: store.RunCancelled, Cause: store.RunCancel, Reason: "interrupted by SIGINT"}); err != nil {
			return err
		}
		reportCtx, stopReport := context.WithTimeout(context.WithoutCancel(ctx), limits.HeartbeatTimeout)
		defer stopReport()
		_, err = f.State.TransitionNode(reportCtx, store.ReportNode{Ref: ref, Actor: actor, SourceAttempt: new(started.Attempt), Outcome: store.NodeCancelled, Result: &store.NodeResult{Outcome: store.NodeCancelled, Reason: "interrupted by SIGINT"}})
		return err
	})
	completion, err := f.Host.Run(ctx, f.Input)
	if err != nil || completion.Outcome != "cancelled" {
		t.Fatalf("completion=%+v error=%v", completion, err)
	}
	snapshot, err := f.Tenant.ReadSnapshot(ctx, f.Accepted.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Run.State != store.RunCancelled || snapshot.Run.CancelReason != "interrupted by SIGINT" || snapshot.Run.FinishedAt == nil {
		t.Fatalf("run=%+v", snapshot.Run)
	}
	for _, row := range snapshot.Nodes {
		if row.Status != store.StatusDone || row.Outcome != store.NodeCancelled || row.Ref.NodeID != "slow" && row.StartedAt != nil {
			t.Errorf("cancelled row=%+v", row)
		}
	}
}

func TestSchedulerCallerCancellationStopsDownstreamDispatch(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(cause.Error(), func(t *testing.T) {
			f := newStoreHostFixture(t, nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{
				{ID: "slow", Steps: []nodeproto.StepSpec{{ID: "body"}}},
				{ID: "next", Needs: deps("slow"), Steps: []nodeproto.StepSpec{{ID: "body"}}},
			}})
			bounded, stop := context.WithTimeout(t.Context(), 5*time.Second)
			defer stop()
			ctx, cancel := context.WithCancelCause(bounded)
			defer cancel(nil)
			f.Host.Executor = ownerNodeFunc(func(ctx context.Context, input NodeInput) error {
				if input.NodeID != "slow" {
					return errors.New("downstream body started after host cancellation")
				}
				cancel(cause)
				return context.Cause(ctx)
			})
			_, err := f.Host.Run(ctx, f.Input)
			if !errors.Is(err, cause) {
				t.Fatalf("error=%v, want %v", err, cause)
			}
			row, err := f.Tenant.ReadNode(bounded, controllerwire.NodeRef{RunID: runspec.RunID(f.Accepted.RunID), NodeID: "next"})
			if err != nil || row.StartedAt != nil || row.ClaimedBy != "" {
				t.Fatalf("downstream row=%+v error=%v", row, err)
			}
		})
	}
}

func TestSchedulerWithoutAnExecutorFailsLoudly(t *testing.T) {
	f := newStoreHostFixture(t, nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "a", Steps: []nodeproto.StepSpec{{ID: "body"}}}}})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	_, err := f.Host.Run(ctx, f.Input)
	if err == nil || !strings.Contains(err.Error(), "node host") {
		t.Fatalf("error=%v", err)
	}
	row, err := f.Tenant.ReadNode(ctx, controllerwire.NodeRef{RunID: runspec.RunID(f.Accepted.RunID), NodeID: "a"})
	if err != nil || row.StartedAt != nil {
		t.Fatalf("row=%+v error=%v", row, err)
	}
}

func TestSchedulerARefusedOutputFailsTheNodeAndDependants(t *testing.T) {
	for _, message := range []string{"output refused", strings.Repeat("upload refused\n", 200<<10)} {
		t.Run(fmt.Sprintf("%d-bytes", len(message)), func(t *testing.T) {
			f := newStoreHostFixture(t, nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{
				{ID: "big", ResultStep: "body", Steps: []nodeproto.StepSpec{{ID: "body"}}},
				{ID: "after", Needs: deps("big"), Steps: []nodeproto.StepSpec{{ID: "body"}}},
			}})
			_, _, dependencies, _ := executorFixture(t)
			dependencies.Store = f.State
			dependencies.Start = func(context.Context, NodeInput, ExecutorActivity) (ExecutorProcess, error) {
				return &executorProcess{plan: f.Accepted.Plan, result: nodeproto.RunResult{Outcome: "success", Output: json.RawMessage(`"huge"`)}}, nil
			}
			refused := errors.New(message)
			dependencies.PublishOutput = func(context.Context, NodeInput, json.RawMessage) (*blob.RecordKey, error) { return nil, refused }
			executor, err := NewNodeExecutor(dependencies)
			if err != nil {
				t.Fatal(err)
			}
			f.Host.Executor = executor
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			completion, err := f.Host.Run(ctx, f.Input)
			if err != nil || completion.Outcome != "failed" {
				t.Fatalf("completion=%+v error=%v", completion, err)
			}
			for id, outcome := range map[string]store.NodeState{"big": store.NodeFailed, "after": store.NodeCancelled} {
				row, err := f.Tenant.ReadNode(ctx, controllerwire.NodeRef{RunID: runspec.RunID(f.Accepted.RunID), NodeID: runspec.NodeID(id)})
				if err != nil || row.Outcome != outcome {
					t.Fatalf("%s row=%+v error=%v", id, row, err)
				}
			}
			events, err := f.Tenant.ReadStateEvents(ctx, f.Accepted.RunID, 0, 100)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, event := range events {
				if event.NodeID != "big" || event.Kind != "job_failed" {
					continue
				}
				var result store.NodeResult
				if err := json.Unmarshal(event.Payload, &result); err != nil {
					t.Fatal(err)
				}
				if len(result.MaskedError) > int(limits.ErrorBytes) || !strings.Contains(result.MaskedError, "refused") {
					t.Fatalf("failure has %d bytes: %q", len(result.MaskedError), result.MaskedError)
				}
				found = true
			}
			if !found {
				t.Fatal("output refusal has no committed failure")
			}
		})
	}
}
