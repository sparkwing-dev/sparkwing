package nodehost

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func TestNodeExecutorThrowingStepPredicateCannotRunBody(t *testing.T) {
	input, st, deps, _ := executorFixture(t)
	job := &input.Accepted.Plan.Jobs[0]
	job.Steps[0].Closures.SkipIf = []string{"guard"}
	planHash, _ := runspec.CanonicalHash(input.Accepted.Plan)
	jobHash, _ := runspec.CanonicalHash(job)
	st.row.Spec, _ = json.Marshal(job)
	st.row.SpecHash, input.SpecHash, input.Accepted.PlanHash = string(jobHash), string(jobHash), string(planHash)
	var spec runspec.Spec
	if err := json.Unmarshal(input.Accepted.Spec, &spec); err != nil {
		t.Fatal(err)
	}
	spec.PlanHash = planHash
	input.Accepted.Spec, _ = json.Marshal(spec)
	st.accepted = &input.Accepted
	start := deps.Start
	bodies := 0
	deps.Start = func(ctx context.Context, in NodeInput, activity ExecutorActivity) (ExecutorProcess, error) {
		process, err := start(ctx, in, activity)
		proc := process.(*executorProcess)
		proc.plan = input.Accepted.Plan
		proc.onRun = func(context.Context) { bodies++ }
		proc.answer = func(_ context.Context, request nodeproto.Request) (any, error) {
			if request.Op == nodeproto.OpEval {
				return nodeproto.EvalResult{Value: json.RawMessage(`null`), Error: &apierr.Error{Code: apierr.SDKError, Message: "throwing predicate"}}, nil
			}
			return nil, nil
		}
		return process, err
	}
	host, err := NewNodeExecutor(deps)
	if err != nil {
		t.Fatal(err)
	}
	if err := host.Execute(t.Context(), input); err == nil || bodies != 0 {
		t.Fatalf("throwing predicate ran a body or became success: bodies=%d error=%v", bodies, err)
	}
	final := st.changes[len(st.changes)-1]
	if final.Cause != runstate.CauseReport || final.To != runstate.NodeFailed {
		t.Fatalf("predicate failure did not fail its job: %+v", final)
	}
}
