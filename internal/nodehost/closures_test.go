package nodehost

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestClosuresRunOnceAroundRetries(t *testing.T) {
	for _, mode := range []string{"retry-success", "retry-failure", "before-failure", "verify-failure"} {
		t.Run(mode, func(t *testing.T) {
			input, state, deps, processes := executorFixture(t)
			input.Attempt, state.row.Attempt = 0, 0
			executorRetryBudget(t, &input, state, 2)
			job := input.Accepted.Plan.Jobs[0]
			job.Closures = nodeproto.ClosureSpec{BeforeRun: []string{"before"}, Verify: "verify", AfterRun: []string{"after"}}
			executorChangeJob(t, &input, state, job)
			var trace []string
			start := deps.Start
			deps.Start = func(ctx context.Context, in NodeInput, activity ExecutorActivity) (ExecutorProcess, error) {
				process, err := start(ctx, in, activity)
				p := process.(*executorProcess)
				p.plan = input.Accepted.Plan
				ordinal := len(*processes)
				p.answer = func(_ context.Context, request nodeproto.Request) (any, error) {
					if request.Op == nodeproto.OpRunStep {
						trace = append(trace, "body")
						if mode == "retry-failure" || mode == "retry-success" && ordinal == 1 {
							return nodeproto.RunResult{Outcome: "failed", Error: &apierr.Error{Code: apierr.SDKError, Message: "body refused"}}, nil
						}
					}
					if request.Op != nodeproto.OpEval {
						return nil, nil
					}
					trace = append(trace, request.Closure)
					if mode == "before-failure" && request.Closure == "before" || mode == "verify-failure" && request.Closure == "verify" {
						return nodeproto.EvalResult{Error: &apierr.Error{Code: apierr.SDKError, Message: "hook refused"}}, nil
					}
					if request.Closure == "after" {
						var result nodeproto.RunResult
						if err := json.Unmarshal(request.Input, &result); err != nil {
							t.Fatal(err)
						}
						if mode == "retry-success" && result.Outcome != "success" || mode != "retry-success" && (result.Outcome != "failed" || result.Error == nil) {
							t.Errorf("after_run input=%s", request.Input)
						}
					}
					return nodeproto.EvalResult{Value: json.RawMessage("null")}, nil
				}
				return process, err
			}
			executor, err := NewNodeExecutor(deps)
			if err != nil {
				t.Fatal(err)
			}
			err = executor.Execute(t.Context(), input)
			want := map[string][]string{
				"retry-success":  {"before", "body", "body", "verify", "after"},
				"retry-failure":  {"before", "body", "body", "body", "after"},
				"before-failure": {"before"},
				"verify-failure": {"before", "body", "verify", "body", "verify", "body", "verify", "after"},
			}[mode]
			if !reflect.DeepEqual(trace, want) {
				t.Fatalf("trace=%v, want %v (error=%v)", trace, want, err)
			}
			if (err != nil) != (mode == "before-failure") {
				t.Fatalf("error=%v", err)
			}
			report := state.changes[len(state.changes)-1]
			wantOutcome := store.NodeFailed
			if mode == "retry-success" {
				wantOutcome = store.NodeSuccess
			}
			if report.To != wantOutcome {
				t.Fatalf("report=%+v", report)
			}
			if mode == "verify-failure" && (report.Extra.Result == nil || report.Extra.Result.Failure == nil || report.Extra.Result.Failure.Stage != "verify") {
				t.Fatalf("verify failure lost its stage: %+v", report.Extra.Result)
			}
			for _, p := range *processes {
				if p.closed != 1 || p.cleaned != 1 {
					t.Fatal("closure execution leaked a process")
				}
			}
		})
	}
}
