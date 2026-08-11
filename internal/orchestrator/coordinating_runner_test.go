package orchestrator

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type downstreamCoordinatingRunner struct{ calls atomic.Int32 }

func (r *downstreamCoordinatingRunner) RunNode(context.Context, runner.Request) runner.Result {
	r.calls.Add(1)
	return runner.Result{Outcome: sparkwing.Success}
}

func (*downstreamCoordinatingRunner) CoordinatesDownstream() {}

func TestNewCoordinatingRunnerPreservesDownstreamCoordinator(t *testing.T) {
	downstream := &downstreamCoordinatingRunner{}
	if got := newCoordinatingRunner(Backends{}, downstream); got != downstream {
		t.Fatal("runner that coordinates in its execution process was wrapped by the upstream coordinator")
	}
}

var errRejectedRunnerPlan = errors.New("runner rejected plan")

type rejectingPlanRunner struct{ calls atomic.Int32 }

func (*rejectingPlanRunner) ValidatePlan(*sparkwing.Plan) error { return errRejectedRunnerPlan }

func (r *rejectingPlanRunner) RunNode(context.Context, runner.Request) runner.Result {
	r.calls.Add(1)
	return runner.Result{Outcome: sparkwing.Success}
}

type guardedPlanPipeline struct{ sparkwing.Base }

func (guardedPlanPipeline) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "protected", func(context.Context) error { return nil }).
		Inline().
		Cache(func(context.Context) sparkwing.CacheKey { return "candidate-key" })
	return nil
}

func TestRunValidatesRunnerPlanBeforeInlineOrCacheDispatch(t *testing.T) {
	const pipeline = "runner-plan-guard"
	sparkwing.Register[sparkwing.NoInputs](pipeline, func() sparkwing.Pipeline[sparkwing.NoInputs] {
		return guardedPlanPipeline{}
	})
	downstream := &rejectingPlanRunner{}
	result, err := RunLocal(context.Background(), PathsAt(t.TempDir()), Options{
		Pipeline: pipeline,
		Runner:   downstream,
	})
	if err != nil {
		t.Fatalf("RunLocal setup: %v", err)
	}
	if !errors.Is(result.Error, errRejectedRunnerPlan) {
		t.Fatalf("run error = %v, want runner plan rejection", result.Error)
	}
	if calls := downstream.calls.Load(); calls != 0 {
		t.Fatalf("downstream runner calls = %d, want zero after plan rejection", calls)
	}
}
