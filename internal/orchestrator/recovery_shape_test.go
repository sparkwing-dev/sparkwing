package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type recordingRunner struct{ req runner.Request }

func (r *recordingRunner) RunNode(_ context.Context, req runner.Request) runner.Result {
	r.req = req
	return runner.Result{Outcome: sparkwing.Success}
}

// safety: a Kubernetes runner sizes and bounds a recovery Job from the
// request's fields alone, so a recovery keeps its declared timeout and pin.
func TestRecoveryDispatchCarriesTheNodesTimeoutAndPin(t *testing.T) {
	plan := sparkwing.NewPlan()
	node := sparkwing.Job(plan, "rollback", func(context.Context) error { return nil }).
		Timeout(time.Minute).Resources(sparkwing.Cores(4), sparkwing.MemoryGB(8))
	r := &recordingRunner{}
	s := &dispatchState{runner: r, plan: plan, runID: "run", pipeline: "pipeline"}
	s.invokeRecoveryRunner(t.Context(), node, sparkwing.Failure{})
	if r.req.Timeout != time.Minute || r.req.Cores != 4 || r.req.MemoryBytes != node.ResourceHints().MemoryBytes {
		t.Fatalf("recovery request timeout=%s cores=%v memory=%d, want the node's 1m, 4 cores and 8 GB",
			r.req.Timeout, r.req.Cores, r.req.MemoryBytes)
	}
}
