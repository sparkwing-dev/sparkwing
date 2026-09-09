package orchestrator

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestCacheResolutionFailureStopsDispatch(t *testing.T) {
	for _, failure := range []string{"empty", "panic", "error", "error-with-key"} {
		t.Run(failure, func(t *testing.T) {
			state := consumerTestStore(t, t.TempDir())
			const runID = "sample-run"
			const nodeID = "sample-node"
			if err := state.CreateRun(t.Context(), store.Run{ID: runID, Pipeline: "sample", Status: "running"}); err != nil {
				t.Fatal(err)
			}
			if err := state.CreateNode(t.Context(), store.Node{RunID: runID, NodeID: nodeID, Status: "pending"}); err != nil {
				t.Fatal(err)
			}
			cause := errors.New("sample resolver " + failure)
			var resolutions atomic.Int32
			var executions atomic.Int32
			plan := sparkwing.NewPlan()
			node := sparkwing.Job(plan, nodeID, func(context.Context) error {
				executions.Add(1)
				return nil
			}).Memoize(func(context.Context) (sparkwing.CacheKey, error) {
				if resolutions.Add(1) > 1 {
					return "sample-key", nil
				}
				if failure == "panic" {
					panic("sample resolver panic")
				}
				if failure == "error" {
					return "", cause
				}
				if failure == "error-with-key" {
					return "sample-key", cause
				}
				return "", nil
			})
			executor := NewNodeExecutor(Backends{State: localState{st: state}})
			result := executor.RunNode(t.Context(), runner.Request{RunID: runID, NodeID: nodeID, Node: node})
			if result.Outcome != sparkwing.Failed || result.Err == nil {
				t.Fatalf("cache resolution = (%s, %v)", result.Outcome, result.Err)
			}
			if !strings.Contains(result.Err.Error(), failure) {
				t.Errorf("resolution error = %v", result.Err)
			}
			if strings.HasPrefix(failure, "error") && !errors.Is(result.Err, cause) {
				t.Errorf("resolver cause lost: %v", result.Err)
			}
			persisted, err := state.GetNode(t.Context(), runID, nodeID)
			if err != nil {
				t.Fatal(err)
			}
			if persisted.Outcome != string(sparkwing.Failed) {
				t.Errorf("persisted outcome = %s", persisted.Outcome)
			}
			if resolutions.Load() != 1 || executions.Load() != 0 {
				t.Errorf("resolutions=%d executions=%d", resolutions.Load(), executions.Load())
			}
		})
	}
}

func TestCacheResolutionAllowsExplicitBypass(t *testing.T) {
	plan := sparkwing.NewPlan()
	node := sparkwing.Job(plan, "sample", func(context.Context) error { return nil }).Memoize(
		func(context.Context) (sparkwing.CacheKey, error) { return sparkwing.NoCache, nil },
	)
	result, handled := NewNodeExecutor(Backends{}).runNodeWithCache(t.Context(), runner.Request{Node: node})
	if handled || result.Err != nil {
		t.Fatalf("explicit bypass = (%v, handled=%t)", result.Err, handled)
	}
}

func TestCacheResolutionRejectsEndedContext(t *testing.T) {
	for _, expired := range []bool{false, true} {
		var resolverContext context.Context
		var cancel context.CancelFunc
		wanted := context.Canceled
		if expired {
			resolverContext, cancel = context.WithDeadline(t.Context(), time.Time{})
			wanted = context.DeadlineExceeded
		} else {
			resolverContext, cancel = context.WithCancel(t.Context())
		}
		cancel()
		var calls atomic.Int32
		key, err := resolveCacheKey(resolverContext, func(context.Context) (sparkwing.CacheKey, error) {
			calls.Add(1)
			return "sample-key", nil
		}, "sample")
		if key != "" || !errors.Is(err, wanted) || calls.Load() != 0 {
			t.Errorf("ended context = (%q, %v), calls=%d", key, err, calls.Load())
		}
	}
}

func TestCacheResolutionDeadlineAfterEntry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		key, err := resolveCacheKey(t.Context(), func(ctx context.Context) (sparkwing.CacheKey, error) {
			calls.Add(1)
			<-ctx.Done()
			return "sample-key", nil
		}, "sample")
		if key != "" || !errors.Is(err, context.DeadlineExceeded) || calls.Load() != 1 {
			t.Fatalf("expired resolver = (%q, %v), calls=%d", key, err, calls.Load())
		}
	})
}

func TestCacheResolutionCancellationLeavesRowForTeardown(t *testing.T) {
	state := consumerTestStore(t, t.TempDir())
	const runID = "sample-run"
	const nodeID = "sample-node"
	if err := state.CreateRun(t.Context(), store.Run{ID: runID, Pipeline: "sample", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	if err := state.CreateNode(t.Context(), store.Node{RunID: runID, NodeID: nodeID, Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	plan := sparkwing.NewPlan()
	node := sparkwing.Job(plan, nodeID, func(context.Context) error { return nil }).Memoize(
		func(context.Context) (sparkwing.CacheKey, error) { return "sample-key", nil },
	)
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	result := NewNodeExecutor(Backends{State: localState{st: state}}).RunNode(canceled, runner.Request{RunID: runID, NodeID: nodeID, Node: node})
	if result.Outcome != sparkwing.Failed || !errors.Is(result.Err, context.Canceled) {
		t.Fatalf("canceled node = (%s, %v)", result.Outcome, result.Err)
	}
	persisted, err := state.GetNode(t.Context(), runID, nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Outcome != "" {
		t.Fatalf("outcome before teardown = %q", persisted.Outcome)
	}
}
