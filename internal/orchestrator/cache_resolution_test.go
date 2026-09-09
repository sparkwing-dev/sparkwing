package orchestrator

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestCacheResolutionFailureStopsDispatch(t *testing.T) {
	for _, failure := range []string{"empty", "panic"} {
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
			var resolutions atomic.Int32
			var executions atomic.Int32
			plan := sparkwing.NewPlan()
			node := sparkwing.Job(plan, nodeID, func(context.Context) error {
				executions.Add(1)
				return nil
			}).Memoize(func(context.Context) sparkwing.CacheKey {
				if resolutions.Add(1) > 1 {
					return "sample-key"
				}
				if failure == "panic" {
					panic("sample resolver panic")
				}
				return ""
			})
			executor := NewNodeExecutor(Backends{State: localState{st: state}})
			result, handled := executor.runNodeWithCache(t.Context(), runner.Request{RunID: runID, Node: node})
			if !handled || result.Outcome != sparkwing.Failed || result.Err == nil {
				t.Fatalf("cache resolution = (%s, %v, handled=%t)", result.Outcome, result.Err, handled)
			}
			if !strings.Contains(result.Err.Error(), failure) {
				t.Errorf("resolution error = %v", result.Err)
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
