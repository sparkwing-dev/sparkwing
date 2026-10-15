package nodehost

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type retryState struct {
	storage.StateStoreOld
	consumed int
}

func (retryState) AppendEvent(context.Context, string, string, string, []byte) error { return nil }

func (s retryState) GetNode(context.Context, string, string) (*store.Node, error) {
	return &store.Node{AttemptsConsumed: s.consumed}, nil
}

func retryNode(m nodeproto.JobSpec) *NodeRun {
	m.ID = "flaky"
	run := &Run{ID: testRunID, State: retryState{}, Logger: slog.New(slog.DiscardHandler), validation: nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: noV2Secrets{}}}
	return &NodeRun{Run: run, Plan: m}
}

func TestRetryStartsPastTheAttemptsTheNodeAlreadySpent(t *testing.T) {
	for _, tc := range []struct {
		name     string
		consumed int
		want     []int
	}{
		{"one spent", 1, []int{2, 3}},
		{"every attempt spent", 3, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &failUntil{}
			n := retryNode(nodeproto.JobSpec{Retry: &nodeproto.RetrySpec{Attempts: 2}, Steps: []nodeproto.StepSpec{{ID: "body"}}})
			n.Run.State = retryState{consumed: tc.consumed}
			res := retryLayer(nil, f.run)(t.Context(), n)
			if fmt.Sprint(f.attempts) != fmt.Sprint(tc.want) || res.Outcome != sparkwing.Failed {
				t.Fatalf("attempts %v outcome %s, want %v failed", f.attempts, res.Outcome, tc.want)
			}
			if tc.want == nil && !strings.Contains(errText(res.Err), "exhausted its 3 execution-attempt budget") {
				t.Fatalf("err = %v, want the exhausted budget", res.Err)
			}
		})
	}
}

type failUntil struct {
	ok       int
	attempts []int
	at       []time.Time
}

func (f *failUntil) run(_ context.Context, n *NodeRun) Result {
	attempt := max(n.Attempt, 1)
	f.attempts = append(f.attempts, attempt)
	f.at = append(f.at, time.Now())
	if attempt == f.ok {
		return Result{Outcome: sparkwing.Success}
	}
	return Result{Outcome: sparkwing.Failed, Err: fmt.Errorf("attempt %d broke", attempt), Reason: store.FailureVerify}
}

func TestRetryRunsTheBudgetAndKeepsTheLastFailure(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mods    nodeproto.JobSpec
		ok      int
		want    []int
		outcome sparkwing.Outcome
	}{
		{"no retry", nodeproto.JobSpec{Steps: []nodeproto.StepSpec{{ID: "body"}}}, 0, []int{1}, sparkwing.Failed},
		{"every attempt fails", nodeproto.JobSpec{Retry: &nodeproto.RetrySpec{Attempts: 2}, Steps: []nodeproto.StepSpec{{ID: "body"}}}, 0, []int{1, 2, 3}, sparkwing.Failed},
		{"second attempt passes", nodeproto.JobSpec{Retry: &nodeproto.RetrySpec{Attempts: 3}, Steps: []nodeproto.StepSpec{{ID: "body"}}}, 2, []int{1, 2}, sparkwing.Success},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &failUntil{ok: tc.ok}
			res := retryLayer(nil, f.run)(t.Context(), retryNode(tc.mods))
			if fmt.Sprint(f.attempts) != fmt.Sprint(tc.want) || res.Outcome != tc.outcome {
				t.Fatalf("attempts %v outcome %s, want %v %s", f.attempts, res.Outcome, tc.want, tc.outcome)
			}
			if tc.outcome == sparkwing.Failed {
				last := tc.want[len(tc.want)-1]
				if res.Err.Error() != fmt.Sprintf("attempt %d broke", last) || res.Reason != store.FailureVerify {
					t.Errorf("err %v reason %q, want the last attempt's", res.Err, res.Reason)
				}
			}
		})
	}
}

func TestRetryBackoffDoublesUpToFiveMinutes(t *testing.T) {
	for _, tc := range []struct {
		backoffMS int64
		want      []time.Duration
	}{
		{0, []time.Duration{0, 0, 0}},
		{1000, []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}},
		{int64(2 * time.Minute / time.Millisecond), []time.Duration{2 * time.Minute, 4 * time.Minute, 5 * time.Minute}},
	} {
		synctest.Test(t, func(t *testing.T) {
			f := &failUntil{}
			retryLayer(nil, f.run)(t.Context(), retryNode(nodeproto.JobSpec{Retry: &nodeproto.RetrySpec{Attempts: 3, BackoffMS: new(int64(tc.backoffMS))}, Steps: []nodeproto.StepSpec{{ID: "body"}}}))
			var gaps []time.Duration
			for i := 1; i < len(f.at); i++ {
				gaps = append(gaps, f.at[i].Sub(f.at[i-1]))
			}
			if fmt.Sprint(gaps) != fmt.Sprint(tc.want) {
				t.Errorf("backoff %dms: gaps %v, want %v", tc.backoffMS, gaps, tc.want)
			}
		})
	}
}

func TestRetryStopsWhenTheRunIsCancelled(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		f := &failUntil{}
		var res Result
		done := make(chan struct{})
		go func() {
			defer close(done)
			res = retryLayer(nil, f.run)(ctx, retryNode(nodeproto.JobSpec{Retry: &nodeproto.RetrySpec{Attempts: 5, BackoffMS: new(int64(60_000))}, Steps: []nodeproto.StepSpec{{ID: "body"}}}))
		}()
		synctest.Wait()
		cancel()
		<-done
		if len(f.attempts) != 1 || res.Outcome != sparkwing.Failed {
			t.Errorf("attempts %v outcome %s, want one failed attempt", f.attempts, res.Outcome)
		}

		cancelled := func(context.Context, *NodeRun) Result { return Result{Outcome: sparkwing.Cancelled} }
		calls := 0
		counted := func(ctx context.Context, n *NodeRun) Result { calls++; return cancelled(ctx, n) }
		retryLayer(nil, counted)(t.Context(), retryNode(nodeproto.JobSpec{Retry: &nodeproto.RetrySpec{Attempts: 3}, Steps: []nodeproto.StepSpec{{ID: "body"}}}))
		if calls != 1 {
			t.Errorf("a cancelled attempt ran %d times, want 1", calls)
		}
	})
}

type stateWrites struct {
	*store.Store
	mu     sync.Mutex
	writes []string
}

func (s *stateWrites) note(w string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writes = append(s.writes, w)
}

func (s *stateWrites) StartNode(ctx context.Context, runID, nodeID string) error {
	s.note("start")
	return s.Store.StartNode(ctx, runID, nodeID)
}

func (s *stateWrites) FinishNodeWithReason(ctx context.Context, runID, nodeID, outcome, errMsg string, output []byte, reason string, exitCode *int) error {
	s.note("finish " + outcome)
	return s.Store.FinishNodeWithReason(ctx, runID, nodeID, outcome, errMsg, output, reason, exitCode)
}

func TestRetryLeavesTheRowRunningBetweenAttempts(t *testing.T) {
	fixture := newStoreHostFixture(t, nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "flaky", Retry: &nodeproto.RetrySpec{Attempts: 1, BackoffMS: new(int64(0))}, Steps: []nodeproto.StepSpec{{ID: "body"}}}}})
	_, _, dependencies, _ := executorFixture(t)
	dependencies.Store = fixture.State
	attempts := 0
	var during store.NodeRow
	dependencies.Start = func(ctx context.Context, input NodeInput, _ ExecutorActivity) (ExecutorProcess, error) {
		attempts++
		process := &executorProcess{plan: fixture.Accepted.Plan, result: nodeproto.RunResult{Outcome: "success"}}
		if attempts == 1 {
			process.result = nodeproto.RunResult{Outcome: "failed", Error: &apierr.Error{Code: apierr.SDKError, Message: "flake"}}
		}
		if attempts == 2 {
			var err error
			during, err = fixture.Tenant.ReadNode(ctx, store.NodeRef{RunID: fixture.Accepted.RunID, NodeID: input.NodeID})
			if err != nil {
				return nil, err
			}
			if during.Generation != input.Generation || during.Attempt != input.Attempt {
				return nil, errors.New("retry changed its claim generation or attempt")
			}
		}
		return process, nil
	}
	executor, err := NewNodeExecutor(dependencies)
	if err != nil {
		t.Fatal(err)
	}
	fixture.Host.Executor = executor
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	completion, err := fixture.Host.Run(ctx, fixture.Input)
	if err != nil || completion.Outcome != "success" {
		t.Fatalf("completion=%+v error=%v", completion, err)
	}
	if attempts != 2 || during.Status != store.StatusRunning || during.Outcome != "" || during.StartedAt == nil || during.Attempt != 2 {
		t.Fatalf("attempts=%d retry row=%+v", attempts, during)
	}
	row, err := fixture.Tenant.ReadNode(ctx, during.Ref)
	if err != nil || row.Outcome != store.NodeSuccess || row.Attempt != 2 || row.Generation != during.Generation || row.FinishedAt == nil {
		t.Fatalf("final row=%+v error=%v", row, err)
	}
	events, err := fixture.Tenant.ReadStateEvents(ctx, fixture.Accepted.RunID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	successes, failures := 0, 0
	for _, event := range events {
		if event.NodeID != "flaky" {
			continue
		}
		if event.Kind == "job_succeeded" {
			successes++
		}
		if event.Kind == "job_failed" {
			failures++
		}
	}
	if successes != 1 || failures != 0 {
		t.Fatalf("terminal events success=%d failure=%d", successes, failures)
	}
}
