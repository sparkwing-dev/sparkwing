package nodehost

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
	"github.com/sparkwing-dev/sparkwing/pkg/nodeproto"
)

func TestNodeExecutorRecordsVerifyTimeoutStage(t *testing.T) {
	input, storage, deps, _ := executorFixture(t)
	job := input.Accepted.Plan.Jobs[0]
	job.Closures.Verify = "verify"
	executorChangeJob(t, &input, storage, job)
	var expire context.CancelCauseFunc
	deps.ChildContext = func(parent context.Context, _, _ time.Duration) (context.Context, context.CancelFunc, error) {
		ctx, cancel := context.WithCancelCause(parent)
		expire = cancel
		return ctx, func() { cancel(nil) }, nil
	}
	start := deps.Start
	deps.Start = func(ctx context.Context, in NodeInput, activity ExecutorActivity) (ExecutorProcess, error) {
		process, err := start(ctx, in, activity)
		process.(*executorProcess).plan = input.Accepted.Plan
		process.(*executorProcess).answer = func(ctx context.Context, request nodeproto.Request) (any, error) {
			if request.Op == nodeproto.OpEval && request.Closure == "verify" {
				expire(context.DeadlineExceeded)
				return nil, context.Cause(ctx)
			}
			return nil, nil
		}
		return process, err
	}
	host, err := NewNodeExecutor(deps)
	if err != nil {
		t.Fatal(err)
	}
	if err := host.Execute(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	result := storage.changes[len(storage.changes)-1].Extra.Result
	if result == nil || result.Failure == nil || result.Failure.Stage != "verify" || result.Failure.Job != input.NodeID || result.Failure.Error == nil || result.Failure.Error.Code != apierr.DeadlineExceeded {
		t.Fatalf("verify timeout lost its persisted stage: %+v", result)
	}
}
