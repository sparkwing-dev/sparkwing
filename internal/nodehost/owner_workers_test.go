package nodehost

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func TestOwnerHostWorkersBoundLocalExecution(t *testing.T) {
	for _, limit := range []int{1, 2, 3} {
		t.Run(string(rune('0'+limit)), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				state, host, input := newOwnerFixture(t, true)
				state.snapshot.Run.Spec.Resolved.Settings.Options.Workers.Value = limit
				input.Accepted.Spec, _ = json.Marshal(state.snapshot.Run.Spec)
				for _, id := range []string{"one", "two", "three"} {
					state.snapshot.Nodes = append(state.snapshot.Nodes, runstate.NodeRow{Ref: runspec.NodeRef{RunID: "run", NodeID: runspec.NodeID(id)}, Spec: marshalEventFixture(t, nodeproto.JobSpec{ID: id}), Status: runstate.StatusPending, SpecHash: "job"})
				}
				host.Decide = store.Decide
				host.Local = func(runstate.NodeRow) bool { return true }
				setTestPolicies(&host, func(context.Context, testPolicyName, EvaluationInput, runstate.NodeRow, StorageBinding) (ownerLayerResult, error) {
					return ownerLayerResult{}, nil
				})
				release := make(chan struct{})
				var started atomic.Int64
				host.Executor = ownerNodeFunc(func(ctx context.Context, node NodeInput) error {
					started.Add(1)
					<-release
					_, err := state.TransitionNode(ctx, store.ReportNode{Ref: runspec.NodeRef{RunID: "run", NodeID: runspec.NodeID(node.NodeID)}, Outcome: runstate.NodeSuccess})
					return err
				})
				done := make(chan error, 1)
				go func() { _, err := host.Run(t.Context(), input); done <- err }()
				synctest.Wait()
				if got := started.Load(); got != int64(limit) {
					t.Errorf("started %d jobs before any finished with workers=%d", got, limit)
				}
				close(release)
				if err := <-done; err != nil {
					t.Fatal(err)
				}
				if got := started.Load(); got != 3 {
					t.Fatalf("finished after starting %d jobs", got)
				}
			})
		})
	}
}
