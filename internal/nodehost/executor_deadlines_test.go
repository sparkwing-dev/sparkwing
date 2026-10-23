package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

type executorCleanupProcess struct {
	*executorProcess
	cleanup func(context.Context) error
}

func TestRetryLeavesTheRowRunningThroughItsBackoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		input, state, deps, processes := executorFixture(t)
		input.Execution.RetryBackoff.Value = time.Hour
		executorRetryBudget(t, &input, state, 1)
		start := deps.Start
		deps.Start = func(ctx context.Context, input NodeInput, activity ExecutorActivity) (ExecutorProcess, error) {
			process, err := start(ctx, input, activity)
			process.(*executorProcess).result.Outcome = "failed"
			return process, err
		}
		host, err := NewNodeExecutor(deps)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- host.Execute(ctx, input) }()
		synctest.Wait()
		select {
		case err := <-done:
			t.Fatal("executor returned before backoff:", err)
		default:
		}
		if len(state.changes) != 2 || state.changes[0].Cause != runstate.CauseStart || state.changes[1].Cause != runstate.CauseRetry || state.changes[1].To != runstate.NodeRunning || state.changes[1].Actor.Generation != input.Generation || len(*processes) != 1 {
			t.Fatalf("backoff changed lifecycle: transitions=%+v processes=%d", state.changes, len(*processes))
		}
		cancel()
		synctest.Wait()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatal("cancel during backoff:", err)
		}
		if len(state.changes) != 3 || state.changes[2].To != runstate.NodeCancelled || len(*processes) != 1 {
			t.Fatalf("backoff cancellation started work or lost its terminal write: %+v", state.changes)
		}
	})
}

func (p executorCleanupProcess) Cleanup(ctx context.Context) error {
	p.cleaned++
	return p.cleanup(ctx)
}

func TestNodeExecutorCleanupDoesNotConsumeAttemptBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		input, s, deps, _ := executorFixture(t)
		input.Execution.Timeout.Value = time.Second
		executorRetryBudget(t, &input, s, 0)
		start := deps.Start
		deps.Start = func(ctx context.Context, in NodeInput, progress ExecutorActivity) (ExecutorProcess, error) {
			process, err := start(ctx, in, progress)
			return executorCleanupProcess{process.(*executorProcess), func(cleanup context.Context) error {
				if cleanup.Err() != nil {
					t.Fatal("cleanup inherited the attempt cancellation")
				}
				<-time.NewTimer(2 * time.Second).C
				return nil
			}}, err
		}
		host, err := NewNodeExecutor(deps)
		if err != nil {
			t.Fatal(err)
		}
		if err = host.Execute(t.Context(), input); err != nil {
			t.Fatal(err)
		}
		if got := s.changes[len(s.changes)-1].To; got != runstate.NodeSuccess {
			t.Fatalf("cleanup changed successful attempt to %s", got)
		}
	})
}

func TestNodeExecutorTimeoutRunsFinallyAndAfterRun(t *testing.T) {
	input, s, deps, _ := executorFixture(t)
	job := input.Accepted.Plan.Jobs[0]
	job.Steps = append(job.Steps, nodeproto.StepSpec{ID: "finally", Needs: []string{"body"}, Finally: true})
	job.Closures.AfterRun = []string{"after"}
	executorChangeJob(t, &input, s, job)
	var expire context.CancelCauseFunc
	deps.ChildContext = func(parent context.Context, _, _ time.Duration) (context.Context, context.CancelFunc, error) {
		ctx, cancel := context.WithCancelCause(parent)
		expire = cancel
		return ctx, func() { cancel(nil) }, nil
	}
	finally, after := false, false
	deps.Start = func(context.Context, NodeInput, ExecutorActivity) (ExecutorProcess, error) {
		return &executorProcess{plan: input.Accepted.Plan, answer: func(ctx context.Context, request nodeproto.Request) (any, error) {
			switch {
			case request.Op == nodeproto.OpRunStep && request.Step == "body":
				expire(context.DeadlineExceeded)
				return nil, context.Cause(ctx)
			case request.Op == nodeproto.OpRunStep && request.Step == "finally":
				finally = true
			case request.Op == nodeproto.OpEval && request.Closure == "after":
				after = true
			default:
				return nil, nil
			}
			deadline, bounded := ctx.Deadline()
			if ctx.Err() != nil || !bounded {
				t.Errorf("cleanup callback has invalid context: error=%v deadline=%v", ctx.Err(), deadline)
			}
			if request.Op == nodeproto.OpEval {
				return nodeproto.EvalResult{Value: []byte("null")}, nil
			}
			return nodeproto.RunResult{Outcome: "success"}, nil
		}}, nil
	}
	host, err := NewNodeExecutor(deps)
	if err != nil {
		t.Fatal(err)
	}
	if err = host.Execute(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	if !finally || !after {
		t.Fatalf("timeout cleanup: finally=%v after_run=%v", finally, after)
	}
	result := s.changes[len(s.changes)-1].Extra.Result
	if result.Outcome != runstate.NodeFailed || result.Reason != string(apierr.DeadlineExceeded) {
		t.Fatalf("timeout result=%+v", result)
	}
}

func TestNodeExecutorTimeoutDoesNotHideCleanupFailure(t *testing.T) {
	input, s, deps, processes := executorFixture(t)
	executorRetryBudget(t, &input, s, 1)
	var expire context.CancelCauseFunc
	deps.ChildContext = func(parent context.Context, _, _ time.Duration) (context.Context, context.CancelFunc, error) {
		ctx, cancel := context.WithCancelCause(parent)
		expire = cancel
		return ctx, func() { cancel(nil) }, nil
	}
	cleanupErr := errors.New("registered cleanup failed")
	start := deps.Start
	deps.Start = func(ctx context.Context, in NodeInput, progress ExecutorActivity) (ExecutorProcess, error) {
		process, err := start(ctx, in, progress)
		p := process.(*executorProcess)
		p.onRun = func(context.Context) { expire(context.DeadlineExceeded) }
		p.cleanupErr = cleanupErr
		return p, err
	}
	host, err := NewNodeExecutor(deps)
	if err != nil {
		t.Fatal(err)
	}
	if err = host.Execute(t.Context(), input); !errors.Is(err, cleanupErr) {
		t.Fatalf("error=%v", err)
	}
	if len(*processes) != 1 || (*processes)[0].cleaned != 1 {
		t.Fatal("retried before cleanup succeeded")
	}
}

func TestNodeExecutorAwaitsHoldNoProgressUntilLastResume(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		input, s, deps, _ := executorFixture(t)
		input.Execution.NoProgressTimeout.Value = 2 * time.Second
		executorRetryBudget(t, &input, s, 0)
		start := time.Now()
		deps.Masker = secrets.NewMasker()
		deps.Start = func(_ context.Context, _ NodeInput, activity ExecutorActivity) (ExecutorProcess, error) {
			return &executorProcess{plan: input.Accepted.Plan, answer: func(ctx context.Context, request nodeproto.Request) (any, error) {
				if request.Op != nodeproto.OpRunStep {
					return nil, nil
				}
				first, second := activity.Await(), activity.Await()
				<-time.NewTimer(5 * time.Second).C
				first()
				activity.Progress()
				<-time.NewTimer(5 * time.Second).C
				if ctx.Err() != nil {
					t.Errorf("await expired: %v", context.Cause(ctx))
					return nil, context.Cause(ctx)
				}
				second()
				second()
				<-ctx.Done()
				return nil, context.Cause(ctx)
			}}, nil
		}
		host, err := NewNodeExecutor(deps)
		if err != nil {
			t.Fatal(err)
		}
		if err = host.Execute(t.Context(), input); err != nil {
			t.Fatal(err)
		}
		result := s.changes[len(s.changes)-1].Extra.Result
		if elapsed := time.Since(start); elapsed != 12*time.Second || result.Reason != string(apierr.DeadlineExceeded) || result.MaskedError != "job attempt exceeded its no-progress deadline" {
			t.Fatalf("elapsed=%s result=%+v", elapsed, result)
		}
	})
}

func TestNodeExecutorAwaitCannotHoldExecutionDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		input, s, deps, _ := executorFixture(t)
		input.Execution.Timeout.Value = 3 * time.Second
		input.Execution.NoProgressTimeout.Value = time.Second
		executorRetryBudget(t, &input, s, 0)
		started := time.Now()
		deps.Start = func(_ context.Context, _ NodeInput, activity ExecutorActivity) (ExecutorProcess, error) {
			return &executorProcess{plan: input.Accepted.Plan, answer: func(ctx context.Context, request nodeproto.Request) (any, error) {
				if request.Op != nodeproto.OpRunStep {
					return nil, nil
				}
				defer activity.Await()()
				<-ctx.Done()
				return nil, context.Cause(ctx)
			}}, nil
		}
		host, err := NewNodeExecutor(deps)
		if err != nil {
			t.Fatal(err)
		}
		if err = host.Execute(t.Context(), input); err != nil {
			t.Fatal(err)
		}
		if elapsed := time.Since(started); elapsed != 3*time.Second {
			t.Fatalf("execution deadline elapsed=%s", elapsed)
		}
	})
}

func TestNodeExecutorLeaseLossStopsAwaitAndSDKCleanup(t *testing.T) {
	input, s, deps, processes := executorFixture(t)
	job := input.Accepted.Plan.Jobs[0]
	job.Steps = append(job.Steps, nodeproto.StepSpec{ID: "finally", Needs: []string{"body"}, Finally: true})
	job.Closures.AfterRun = []string{"after"}
	executorChangeJob(t, &input, s, job)
	ctx, cancel := context.WithCancelCause(t.Context())
	defer cancel(nil)
	deps.Start = func(_ context.Context, _ NodeInput, activity ExecutorActivity) (ExecutorProcess, error) {
		process := &executorProcess{plan: input.Accepted.Plan, answer: func(ctx context.Context, request nodeproto.Request) (any, error) {
			if request.Op == nodeproto.OpRunStep && request.Step == "body" {
				defer activity.Await()()
				cancel(lease.ErrLeaseLost)
				<-ctx.Done()
				return nil, context.Cause(ctx)
			}
			if request.Op == nodeproto.OpRunStep || request.Op == nodeproto.OpEval {
				t.Errorf("SDK cleanup ran after loss of authority: %+v", request)
			}
			return nil, nil
		}}
		*processes = append(*processes, process)
		return process, nil
	}
	host, err := NewNodeExecutor(deps)
	if err != nil {
		t.Fatal(err)
	}
	if err = host.Execute(ctx, input); !errors.Is(err, lease.ErrLeaseLost) {
		t.Fatalf("error=%v", err)
	}
	if len(s.changes) != 1 || s.changes[0].Cause != runstate.CauseStart || (*processes)[0].cleaned != 1 {
		t.Fatalf("changes=%+v registered cleanup=%d", s.changes, (*processes)[0].cleaned)
	}
}

func TestNodeExecutorTimeoutDoesNotHideSDKCleanupFailure(t *testing.T) {
	for _, stage := range []string{"finally", "after_run"} {
		t.Run(stage, func(t *testing.T) {
			input, s, deps, processes := executorFixture(t)
			job := input.Accepted.Plan.Jobs[0]
			if stage == "finally" {
				job.Steps = append(job.Steps, nodeproto.StepSpec{ID: "finally", Needs: []string{"body"}, Finally: true})
				executorRetryBudget(t, &input, s, 1)
			} else {
				job.Closures.AfterRun = []string{"after"}
			}
			executorChangeJob(t, &input, s, job)
			var expire context.CancelCauseFunc
			deps.ChildContext = func(parent context.Context, _, _ time.Duration) (context.Context, context.CancelFunc, error) {
				ctx, cancel := context.WithCancelCause(parent)
				expire = cancel
				return ctx, func() { cancel(nil) }, nil
			}
			cleanupErr := errors.New("SDK cleanup failed")
			deps.Start = func(context.Context, NodeInput, ExecutorActivity) (ExecutorProcess, error) {
				process := &executorProcess{plan: input.Accepted.Plan, answer: func(ctx context.Context, request nodeproto.Request) (any, error) {
					if request.Op == nodeproto.OpRunStep && request.Step == "body" {
						expire(context.DeadlineExceeded)
						return nil, context.Cause(ctx)
					}
					if request.Op == nodeproto.OpRunStep || request.Op == nodeproto.OpEval {
						return nil, cleanupErr
					}
					return nil, nil
				}}
				*processes = append(*processes, process)
				return process, nil
			}
			host, err := NewNodeExecutor(deps)
			if err != nil {
				t.Fatal(err)
			}
			if err = host.Execute(t.Context(), input); !errors.Is(err, cleanupErr) {
				t.Fatalf("error=%v", err)
			}
			if len(*processes) != 1 || (*processes)[0].closed != 1 || (*processes)[0].cleaned != 1 {
				t.Fatal("retried with unfinished cleanup")
			}
		})
	}
}

func TestNodeExecutorTimeoutBoundsFinally(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		input, s, deps, _ := executorFixture(t)
		input.Execution.Timeout.Value = time.Second
		executorRetryBudget(t, &input, s, 0)
		job := input.Accepted.Plan.Jobs[0]
		job.Steps = append(job.Steps, nodeproto.StepSpec{ID: "finally", Needs: []string{"body"}, Finally: true})
		executorChangeJob(t, &input, s, job)
		deps.Start = func(context.Context, NodeInput, ExecutorActivity) (ExecutorProcess, error) {
			return &executorProcess{plan: input.Accepted.Plan, answer: func(ctx context.Context, request nodeproto.Request) (any, error) {
				if request.Op == nodeproto.OpRunStep {
					<-ctx.Done()
					return nil, context.Cause(ctx)
				}
				return nil, nil
			}}, nil
		}
		host, err := NewNodeExecutor(deps)
		if err != nil {
			t.Fatal(err)
		}
		started := time.Now()
		if err = host.Execute(t.Context(), input); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("error=%v", err)
		}
		if elapsed := time.Since(started); elapsed != time.Second+limits.CleanupTimeout {
			t.Fatalf("cleanup finished after %s", elapsed)
		}
	})
}

func TestNodeExecutorClosureTimeoutRunsAfterRun(t *testing.T) {
	for _, stage := range []string{"before_run", "verify"} {
		t.Run(stage, func(t *testing.T) {
			input, s, deps, _ := executorFixture(t)
			input.Attempt, s.row.Attempt = 0, 0
			job := input.Accepted.Plan.Jobs[0]
			job.Closures.AfterRun = []string{"after"}
			if stage == "before_run" {
				job.Closures.BeforeRun = []string{"timeout"}
			} else {
				job.Closures.Verify = "timeout"
			}
			executorChangeJob(t, &input, s, job)
			var expire context.CancelCauseFunc
			deps.ChildContext = func(parent context.Context, _, _ time.Duration) (context.Context, context.CancelFunc, error) {
				ctx, cancel := context.WithCancelCause(parent)
				expire = cancel
				return ctx, func() { cancel(nil) }, nil
			}
			afterCalls := 0
			deps.Start = func(context.Context, NodeInput, ExecutorActivity) (ExecutorProcess, error) {
				return &executorProcess{plan: input.Accepted.Plan, result: nodeproto.RunResult{Outcome: "success"}, answer: func(ctx context.Context, request nodeproto.Request) (any, error) {
					if request.Op != nodeproto.OpEval {
						return nil, nil
					}
					if request.Closure == "timeout" {
						expire(context.DeadlineExceeded)
						return nil, context.Cause(ctx)
					}
					afterCalls++
					var result nodeproto.RunResult
					if err := json.Unmarshal(request.Input, &result); err != nil || result.Outcome != "failed" || result.Error == nil || result.Error.Code != apierr.DeadlineExceeded {
						t.Errorf("after_run input=%s error=%v", request.Input, err)
					}
					if ctx.Err() != nil {
						t.Errorf("after_run context=%v", ctx.Err())
					}
					return nodeproto.EvalResult{Value: []byte("null")}, nil
				}}, nil
			}
			host, err := NewNodeExecutor(deps)
			if err != nil {
				t.Fatal(err)
			}
			if err = host.Execute(t.Context(), input); err != nil {
				t.Fatal(err)
			}
			if afterCalls != 1 || s.changes[len(s.changes)-1].To != runstate.NodeFailed {
				t.Fatalf("after_run calls=%d changes=%+v", afterCalls, s.changes)
			}
		})
	}
}
