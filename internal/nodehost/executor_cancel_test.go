package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

func TestNodeExecutorUserCancelRunsAfterRunAndKeepsCancelledOutcome(t *testing.T) {
	for _, cleanupFailure := range []bool{false, true} {
		name := "cleanup_success"
		if cleanupFailure {
			name = "cleanup_failure"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				input, state, deps, _ := executorFixture(t)
				executorRetryBudget(t, &input, state, 2)
				job := input.Accepted.Plan.Jobs[0]
				job.Closures.AfterRun = []string{"cleanup"}
				executorChangeJob(t, &input, state, job)
				directory := t.TempDir()
				ctx, cancel := context.WithCancelCause(t.Context())
				defer cancel(nil)
				deps.Start = func(context.Context, NodeInput, ExecutorActivity) (ExecutorProcess, error) {
					return &executorProcess{plan: input.Accepted.Plan, answer: func(call context.Context, request nodewire.Request) (any, error) {
						switch {
						case request.Op == nodewire.OpRunStep:
							if err := os.WriteFile(filepath.Join(directory, "started"), []byte("started"), 0600); err != nil {
								return nil, err
							}
							cancel(&lease.CancelError{Reason: "cancel_requested"})
							return nil, context.Cause(call)
						case request.Op == nodewire.OpEval && request.Closure == "cleanup":
							deadline, bounded := call.Deadline()
							if call.Err() != nil || !bounded || time.Until(deadline) <= 0 || time.Until(deadline) > limits.CleanupTimeout {
								t.Errorf("cleanup context is not within the existing budget: err=%v deadline=%v", call.Err(), deadline)
							}
							var result nodewire.RunResult
							if err := json.Unmarshal(request.Input, &result); err != nil {
								return nil, err
							}
							if result.Outcome != "failed" || result.Error == nil || (!strings.HasPrefix(result.Error.Message, "run cancelled:") || !strings.Contains(result.Error.Message, "cancel_requested")) {
								t.Errorf("cleanup did not receive cancellation as its final body error: %+v", result)
							}
							marker, err := os.OpenFile(filepath.Join(directory, "cleanup"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
							if err != nil {
								return nil, err
							}
							_, writeErr := marker.WriteString("cleanup\n")
							resultValue := nodewire.EvalResult{Value: json.RawMessage("null")}
							if cleanupFailure {
								resultValue.Error = &apierr.Error{Code: apierr.SDKError, Message: "cleanup failed"}
							}
							return resultValue, errors.Join(writeErr, marker.Close())
						}
						return nil, nil
					}}, nil
				}
				host, err := NewNodeExecutor(deps)
				if err != nil {
					t.Fatal(err)
				}
				err = host.Execute(ctx, input)
				var requested *lease.CancelError
				if !errors.As(err, &requested) {
					t.Fatalf("requested cancellation lost: %v", err)
				}
				for name, want := range map[string]string{"started": "started", "cleanup": "cleanup\n"} {
					data, readErr := os.ReadFile(filepath.Join(directory, name))
					if readErr != nil || string(data) != want {
						t.Errorf("%s marker=%q, error=%v; want %q", name, data, readErr, want)
					}
				}
				if len(state.changes) != 2 || state.changes[1].To != runstate.NodeCancelled || state.changes[1].Extra.Result.Outcome != runstate.NodeCancelled {
					t.Fatalf("cleanup changed canceled outcome or retried work: %+v", state.changes)
				}
			})
		})
	}
}

func TestNodeExecutorLostAuthorityDoesNotRunAfterRun(t *testing.T) {
	input, state, deps, _ := executorFixture(t)
	job := input.Accepted.Plan.Jobs[0]
	job.Closures.AfterRun = []string{"cleanup"}
	executorChangeJob(t, &input, state, job)
	ctx, cancel := context.WithCancelCause(t.Context())
	defer cancel(nil)
	cleaned := false
	deps.Start = func(context.Context, NodeInput, ExecutorActivity) (ExecutorProcess, error) {
		return &executorProcess{plan: input.Accepted.Plan, answer: func(call context.Context, request nodewire.Request) (any, error) {
			if request.Op == nodewire.OpRunStep {
				cancel(lease.ErrLeaseLost)
				return nil, context.Cause(call)
			}
			if request.Op == nodewire.OpEval && request.Closure == "cleanup" {
				cleaned = true
			}
			return nil, nil
		}}, nil
	}
	host, err := NewNodeExecutor(deps)
	if err != nil {
		t.Fatal(err)
	}
	if err = host.Execute(ctx, input); !errors.Is(err, lease.ErrLeaseLost) {
		t.Fatalf("lost authority cause=%v", err)
	}
	if cleaned || len(state.changes) != 1 {
		t.Fatalf("lost authority ran cleanup or reported a result: cleanup=%v changes=%+v", cleaned, state.changes)
	}
}
