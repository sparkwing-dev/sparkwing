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
	st := &stateWrites{Store: openStore(t)}
	retry := &nodeproto.JobSpec{Retry: &nodeproto.RetrySpec{Attempts: 1}, Steps: []nodeproto.StepSpec{{ID: "body"}}}
	var during *store.Node
	body := func(ctx context.Context, n *NodeRun) Result {
		if n.Attempt == 1 {
			return Result{Outcome: sparkwing.Failed, Err: errors.New("flake")}
		}
		row, err := st.GetNode(ctx, testRunID, "flaky")
		if err != nil {
			return Result{Outcome: sparkwing.Failed, Err: err}
		}
		during = row
		return Result{Outcome: sparkwing.Success}
	}
	doc := nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "flaky", Retry: retry.Retry, Steps: []nodeproto.StepSpec{{ID: "body"}}}}}
	if res := hostDoc(t, t.Context(), st, doc, body); res.Status != "success" {
		t.Fatalf("status %s err %v, want success", res.Status, res.Err)
	}
	if during.Status != "running" || during.Outcome != "" {
		t.Errorf("row before the retry: status %q outcome %q, want running with no outcome", during.Status, during.Outcome)
	}
	if fmt.Sprint(st.writes) != "[start start finish success]" {
		t.Errorf("writes %v, want a start per attempt and one finish", st.writes)
	}
	wantRow(t, st.Store, "flaky", sparkwing.Success, "")
}

// safety: a dispatched node's retries and backoff run on its one host, so
// the host's wall-clock bound covers the last attempt's whole timeout.
func TestAttemptsBudgetCoversEveryAttemptAndBackoff(t *testing.T) {
	for _, tc := range []struct {
		name string
		m    *nodeproto.JobSpec
		want time.Duration
	}{
		{"no timeout", &nodeproto.JobSpec{Retry: &nodeproto.RetrySpec{Attempts: 3}, Steps: []nodeproto.StepSpec{{ID: "body"}}}, 0},
		{"one attempt", &nodeproto.JobSpec{TimeoutMS: new(int64(5 * 60_000)), Steps: []nodeproto.StepSpec{{ID: "body"}}}, 5 * time.Minute},
		{"retry with backoff", &nodeproto.JobSpec{TimeoutMS: new(int64(5 * 60_000)), Retry: &nodeproto.RetrySpec{Attempts: 1, BackoffMS: new(int64(6 * 60_000))}, Steps: []nodeproto.StepSpec{{ID: "body"}}}, 16 * time.Minute},
		{"doubling backoff", &nodeproto.JobSpec{TimeoutMS: new(int64(60_000)), Retry: &nodeproto.RetrySpec{Attempts: 2, BackoffMS: new(int64(1_000))}, Steps: []nodeproto.StepSpec{{ID: "body"}}}, 3*time.Minute + 3*time.Second},
	} {
		if got := AttemptsBudget(*tc.m); got != tc.want {
			t.Errorf("%s: budget = %s, want %s", tc.name, got, tc.want)
		}
	}
}
