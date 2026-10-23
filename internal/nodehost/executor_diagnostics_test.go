package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func TestNodeExecutorDiagnosticsFollowCommittedSteps(t *testing.T) {
	input, st, deps, _ := executorFixture(t)
	job := input.Accepted.Plan.Jobs[0]
	job.Steps = []nodeproto.StepSpec{{ID: "body"}, {ID: "blocked", Needs: []string{"body"}}}
	executorChangeJob(t, &input, st, job)
	records := &executorStepStore{NodeTransitionStore: st}
	deps.Store = records
	start := deps.Start
	deps.Start = func(ctx context.Context, in NodeInput, activity ExecutorActivity) (ExecutorProcess, error) {
		p, err := start(ctx, in, activity)
		p.(*executorProcess).plan = input.Accepted.Plan
		p.(*executorProcess).result = nodeproto.RunResult{Outcome: "failed"}
		return p, err
	}
	var events []string
	deps.Log = func(_ context.Context, in NodeInput, log nodeproto.FrameLog) error {
		if in.Attempt != input.Attempt+1 || in.Generation != input.Generation || in.Authority != input.Authority {
			t.Fatal("diagnostic lost its committed attempt")
		}
		events = append(events, log.Event+":"+log.Message)
		if log.Event == "step_start" || log.Event == "step_end" {
			last := records.reports[len(records.reports)-1]
			if last.StepID != log.Step || log.Event == "step_start" && last.State != "running" || log.Event == "step_end" && last.State == "running" {
				t.Fatal("diagnostic preceded its committed step")
			}
			var attrs map[string]any
			if err := json.Unmarshal(log.Attrs, &attrs); err != nil {
				t.Fatal(err)
			}
			if log.Event == "step_end" && attrs["duration_ms"] == nil {
				t.Fatal("terminal diagnostic lacks duration")
			}
		}
		return nil
	}
	host, err := NewNodeExecutor(deps)
	if err != nil {
		t.Fatal(err)
	}
	if err := host.Execute(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	want := []string{"step_start:body", "step_end:body", "step_end:blocked", "work_fail_fast:body"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("diagnostics=%v, want=%v", events, want)
	}
}

func TestNodeExecutorDiagnosticFailureJoinsWork(t *testing.T) {
	for _, event := range []string{"step_start", "step_end", "work_fail_fast", "retry"} {
		t.Run(event, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			input, st, deps, processes := executorFixture(t)
			executorRetryBudget(t, &input, st, 2)
			refused := errors.New("diagnostic write refused")
			deps.Log = func(_ context.Context, _ NodeInput, log nodeproto.FrameLog) error {
				if log.Event == event {
					return refused
				}
				return nil
			}
			start := deps.Start
			deps.Start = func(ctx context.Context, in NodeInput, activity ExecutorActivity) (ExecutorProcess, error) {
				p, err := start(ctx, in, activity)
				p.(*executorProcess).result = nodeproto.RunResult{Outcome: "failed"}
				return p, err
			}
			host, err := NewNodeExecutor(deps)
			if err != nil {
				t.Fatal(err)
			}
			if err := host.Execute(ctx, input); !errors.Is(err, refused) {
				t.Fatalf("diagnostic failure disappeared: %v", err)
			}
			if len(*processes) != 1 || (*processes)[0].closed != 1 || (*processes)[0].cleaned != 1 || st.changes[len(st.changes)-1].To != runstate.NodeFailed {
				t.Fatal("diagnostic failure retried, succeeded or leaked its process")
			}
			if event == "step_start" {
				for _, request := range (*processes)[0].requests {
					if request.Op == nodeproto.OpRunStep {
						t.Fatal("body ran after its diagnostic failed")
					}
				}
			}
		})
	}
}

func TestNodeExecutorWriteFailureSurvivesAttemptTimeout(t *testing.T) {
	for _, sink := range []string{"step", "diagnostic"} {
		t.Run(sink, func(t *testing.T) {
			input, st, deps, processes := executorFixture(t)
			executorRetryBudget(t, &input, st, 2)
			job := input.Accepted.Plan.Jobs[0]
			job.Steps = append(job.Steps, nodeproto.StepSpec{ID: "cleanup", Finally: true, Needs: []string{"body"}})
			job.Closures.AfterRun = []string{"after"}
			executorChangeJob(t, &input, st, job)
			start := deps.Start
			deps.Start = func(ctx context.Context, in NodeInput, activity ExecutorActivity) (ExecutorProcess, error) {
				p, err := start(ctx, in, activity)
				p.(*executorProcess).plan = input.Accepted.Plan
				p.(*executorProcess).answer = func(_ context.Context, request nodeproto.Request) (any, error) {
					if request.Op == nodeproto.OpRunStep && request.Step == "cleanup" || request.Op == nodeproto.OpEval && request.Closure == "after" {
						t.Error("write refusal allowed further author code after timeout")
					}
					return nil, nil
				}
				return p, err
			}
			var expire context.CancelCauseFunc
			deps.ChildContext = func(parent context.Context, _, _ time.Duration) (context.Context, context.CancelFunc, error) {
				ctx, cancel := context.WithCancelCause(parent)
				expire = cancel
				return ctx, func() { cancel(nil) }, nil
			}
			refused := errors.New("write refused at timeout")
			if sink == "step" {
				deps.Store = &executorStepStore{NodeTransitionStore: st, write: func(_ context.Context, report store.StepReport) error {
					if report.State != "running" {
						expire(context.DeadlineExceeded)
						return refused
					}
					return nil
				}}
			} else {
				deps.Log = func(_ context.Context, _ NodeInput, frame nodeproto.FrameLog) error {
					if frame.Event == "step_end" {
						expire(context.DeadlineExceeded)
						return refused
					}
					return nil
				}
			}
			host, err := NewNodeExecutor(deps)
			if err != nil {
				t.Fatal(err)
			}
			if err := host.Execute(t.Context(), input); !errors.Is(err, refused) {
				t.Fatalf("timeout hid write refusal: %v", err)
			}
			if len(*processes) != 1 || (*processes)[0].closed != 1 || (*processes)[0].cleaned != 1 {
				t.Fatal("timeout converted a write refusal into a retry")
			}
		})
	}
}

func TestNodeExecutorRetryDiagnosticKeepsCommittedOrdinal(t *testing.T) {
	input, st, deps, _ := executorFixture(t)
	input.Attempt, st.row.Attempt = 10, 10
	st.row.ApplicationRetries = 1
	executorRetryBudget(t, &input, st, 2)
	start := deps.Start
	deps.Start = func(ctx context.Context, in NodeInput, activity ExecutorActivity) (ExecutorProcess, error) {
		p, err := start(ctx, in, activity)
		if in.Attempt == 11 {
			p.(*executorProcess).result = nodeproto.RunResult{Outcome: "failed"}
		}
		return p, err
	}
	retries := 0
	deps.Log = func(_ context.Context, in NodeInput, log nodeproto.FrameLog) error {
		if log.Event != "retry" {
			return nil
		}
		retries++
		var attrs map[string]int64
		if err := json.Unmarshal(log.Attrs, &attrs); err != nil {
			t.Fatal(err)
		}
		if in.Attempt != 12 || attrs["ordinal"] != 12 || attrs["attempt"] != 3 || attrs["total"] != 3 || log.Message != "retry attempt 3/3" || st.changes[len(st.changes)-1].Cause != runstate.CauseRetry {
			t.Fatalf("retry differs from committed ordinal or application budget: %+v, %+v", in, log)
		}
		return nil
	}
	host, err := NewNodeExecutor(deps)
	if err != nil {
		t.Fatal(err)
	}
	if err := host.Execute(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	if retries != 1 || st.changes[len(st.changes)-1].To != runstate.NodeSuccess {
		t.Fatalf("retries=%d changes=%+v", retries, st.changes)
	}
}
