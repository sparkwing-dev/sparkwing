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

func retryNode(m nodeproto.PlanModifiers) *NodeRun {
	run := &Run{ID: testRunID, State: retryState{}, Logger: slog.New(slog.DiscardHandler)}
	return &NodeRun{Run: run, Plan: nodeproto.PlanNode{ID: "flaky", Modifiers: &m}}
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
			n := retryNode(nodeproto.PlanModifiers{Retry: 2, RetryAuto: true})
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
		mods    nodeproto.PlanModifiers
		ok      int
		want    []int
		outcome sparkwing.Outcome
	}{
		{"no retry", nodeproto.PlanModifiers{}, 0, []int{1}, sparkwing.Failed},
		{"every attempt fails", nodeproto.PlanModifiers{Retry: 2}, 0, []int{1, 2, 3}, sparkwing.Failed},
		{"second attempt passes", nodeproto.PlanModifiers{Retry: 3}, 2, []int{1, 2}, sparkwing.Success},
		{"retry_auto shares the budget", nodeproto.PlanModifiers{Retry: 1, RetryAuto: true}, 0, []int{1, 2}, sparkwing.Failed},
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
			retryLayer(nil, f.run)(t.Context(), retryNode(nodeproto.PlanModifiers{Retry: 3, RetryBackoffMS: tc.backoffMS}))
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
			res = retryLayer(nil, f.run)(ctx, retryNode(nodeproto.PlanModifiers{Retry: 5, RetryBackoffMS: 60_000}))
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
		retryLayer(nil, counted)(t.Context(), retryNode(nodeproto.PlanModifiers{Retry: 3}))
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
	retry := &nodeproto.PlanModifiers{Retry: 1}
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
	doc := nodeproto.PlanDocOld{Nodes: []nodeproto.PlanNode{{ID: "flaky", Modifiers: retry}}}
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

func TestRetryLeavesTheRowRunningThroughItsBackoff(t *testing.T) {
	st := &stateWrites{Store: openStore(t)}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	failed := make(chan struct{})
	body := func(context.Context, *NodeRun) Result {
		close(failed)
		return Result{Outcome: sparkwing.Failed, Err: errors.New("flake")}
	}
	doc := nodeproto.PlanDocOld{Nodes: []nodeproto.PlanNode{{ID: "flaky", Modifiers: &nodeproto.PlanModifiers{Retry: 1, RetryBackoffMS: int64(time.Hour / time.Millisecond)}}}}
	done := make(chan RunResult)
	go func() { done <- hostDoc(t, ctx, st, doc, body) }()
	<-failed
	// safety: an hour of backoff with ctx live means the layer is still waiting, so this read is mid-backoff.
	if row := nodeRow(t, st.Store, "flaky"); row.Status != "running" || row.Outcome != "" {
		t.Errorf("row in backoff: status %q outcome %q, want running with no outcome", row.Status, row.Outcome)
	}
	cancel()
	<-done
	if fmt.Sprint(st.writes) != "[start finish cancelled]" {
		t.Errorf("writes %v, want the one start and a cancelled finish", st.writes)
	}
}

// safety: a dispatched node's retries and backoff run on its one host, so
// the host's wall-clock bound covers the last attempt's whole timeout.
func TestAttemptsBudgetCoversEveryAttemptAndBackoff(t *testing.T) {
	for _, tc := range []struct {
		name string
		m    *nodeproto.PlanModifiers
		want time.Duration
	}{
		{"no timeout", &nodeproto.PlanModifiers{Retry: 3}, 0},
		{"one attempt", &nodeproto.PlanModifiers{TimeoutMS: 5 * 60_000}, 5 * time.Minute},
		{"retry_auto with backoff", &nodeproto.PlanModifiers{TimeoutMS: 5 * 60_000, Retry: 1, RetryAuto: true, RetryBackoffMS: 6 * 60_000}, 16 * time.Minute},
		{"doubling backoff", &nodeproto.PlanModifiers{TimeoutMS: 60_000, Retry: 2, RetryBackoffMS: 1_000}, 3*time.Minute + 3*time.Second},
	} {
		if got := AttemptsBudget(nodeproto.PlanNode{ID: "n", Modifiers: tc.m}); got != tc.want {
			t.Errorf("%s: budget = %s, want %s", tc.name, got, tc.want)
		}
	}
}
