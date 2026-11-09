package nodehost

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

func TestNodeExecutorSendsCurrentAttemptSettlement(t *testing.T) {
	for _, mode := range []string{"success", "skipped", "failed", "retry"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			input, st, deps, processes := executorFixture(t)
			input.Attempt, st.row.Attempt = 0, 0
			job := input.Accepted.Plan.Jobs[0]
			job.Closures.BeforeRun = []string{"before"}
			job.Steps = []nodewire.StepSpec{
				{ID: "first", Closures: nodewire.ClosureSpec{SkipIf: []string{"guard"}}},
				{ID: "second", Needs: []string{"first"}, Finally: true, Closures: nodewire.ClosureSpec{SkipIf: []string{"second-guard"}}},
				{ID: "last", Needs: []string{"second"}, Finally: true},
			}
			executorChangeJob(t, &input, st, job)
			if mode == "retry" {
				executorRetryBudget(t, &input, st, 1)
			}
			start := deps.Start
			deps.Start = func(ctx context.Context, in NodeInput, activity ExecutorActivity) (ExecutorProcess, error) {
				process, err := start(ctx, in, activity)
				p := process.(*executorProcess)
				p.plan = input.Accepted.Plan
				p.answer = func(_ context.Context, r nodewire.Request) (any, error) {
					if r.Op != nodewire.OpRunStep && r.Op != nodewire.OpEval {
						return nil, nil
					}
					if err := r.Validate(); err != nil {
						return nil, fmt.Errorf("invalid execution request: %w", err)
					}
					if r.Closure == "before" {
						if r.Attempt != 0 || r.Settled != nil {
							t.Error("job hook received step state")
						}
						return nodewire.EvalResult{Value: json.RawMessage(`null`)}, nil
					}
					step := r.Step
					if r.Closure == "guard" {
						step = "first"
					} else if r.Closure == "second-guard" {
						step = "second"
					}
					want := map[string][]string{"first": {}, "second": {"first"}, "last": {"first", "second"}}[step]
					if !reflect.DeepEqual(r.Settled, want) || runspec.AttemptOrdinal(int64(r.Attempt)) != in.Attempt {
						t.Errorf("%s attempt=%d settled=%v, want attempt=%d settled=%v", step, r.Attempt, r.Settled, in.Attempt, want)
					}
					if r.Op == nodewire.OpEval {
						value := `false`
						if r.Closure == "guard" && mode == "skipped" {
							value = `true`
						}
						return nodewire.EvalResult{Value: json.RawMessage(value)}, nil
					}
					if mode == "skipped" && step == "first" {
						t.Error("skipped body ran")
					}
					if step == "first" && (mode == "failed" || mode == "retry" && len(*processes) == 1) {
						return nodewire.RunResult{Outcome: "failed", Error: &apierr.Error{Code: apierr.SDKError, Message: "body failed"}}, nil
					}
					return nodewire.RunResult{Outcome: "success"}, nil
				}
				return process, err
			}
			host, err := NewNodeExecutor(deps)
			if err != nil {
				t.Fatal(err)
			}
			err = host.Execute(ctx, input)
			if err != nil {
				t.Fatalf("mode=%s error=%v", mode, err)
			}
			wantOutcome := runstate.NodeSuccess
			if mode == "failed" {
				wantOutcome = runstate.NodeFailed
			}
			if got := st.changes[len(st.changes)-1].To; got != wantOutcome {
				t.Fatalf("outcome=%s, want %s", got, wantOutcome)
			}
			wantAttempts := 1
			if mode == "retry" {
				wantAttempts = 2
			}
			if len(*processes) != wantAttempts {
				t.Fatalf("attempts=%d, want %d", len(*processes), wantAttempts)
			}
			for _, process := range *processes {
				last := false
				for _, request := range process.requests {
					last = last || request.Op == nodewire.OpRunStep && request.Step == "last"
				}
				if !last {
					t.Fatal("terminal predecessors did not release the final step")
				}
			}
		})
	}
}
