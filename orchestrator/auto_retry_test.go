package orchestrator_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/orchestrator"
	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// TOD-073 / SDK-019: Retry(n, RetryAuto(), ...) re-dispatches the
// whole runner up to n additional times on Failed outcomes. Without
// RetryAuto() the same Retry(n) loops the step body in a single
// runner invocation. The two modes share one verb but exercise
// different code paths in the orchestrator.

var autoRetryCount atomic.Int32

type autoRetryFlakyJob struct {
	sparkwing.Base
	failUntilDispatch int32
}

func (j *autoRetryFlakyJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	w.Step("run", func(ctx context.Context) error {
		dispatch := autoRetryCount.Add(1)
		if dispatch <= j.failUntilDispatch {
			return errors.New("infra flake (simulated)")
		}
		return nil
	})
	return nil, nil
}

type autoRetrySuccessAfterTwoFailsPipe struct{ sparkwing.Base }

func (autoRetrySuccessAfterTwoFailsPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "flaky", &autoRetryFlakyJob{failUntilDispatch: 2}).
		Retry(2, sparkwing.RetryBackoff(10*time.Millisecond), sparkwing.RetryAuto())
	return nil
}

type autoRetryExhaustsAttemptsPipe struct{ sparkwing.Base }

func (autoRetryExhaustsAttemptsPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "always-fails", &autoRetryFlakyJob{failUntilDispatch: 100}).
		Retry(2, sparkwing.RetryAuto())
	return nil
}

func init() {
	register("auto-retry-recovers", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &autoRetrySuccessAfterTwoFailsPipe{} })
	register("auto-retry-exhausts", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &autoRetryExhaustsAttemptsPipe{} })
}

// TestAutoRetry_RecoversAfterTransientFailures verifies the happy
// path: a node that fails twice then succeeds on the third dispatch
// completes with the run reported as success. AutoRetry(2) means up
// to 2 re-dispatches beyond the initial = 3 total attempts.
func TestAutoRetry_RecoversAfterTransientFailures(t *testing.T) {
	autoRetryCount.Store(0)
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "auto-retry-recovers"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("status = %q (err=%v); auto-retry should have recovered after 2 transient failures", res.Status, res.Error)
	}
	if got := autoRetryCount.Load(); got != 3 {
		t.Fatalf("dispatch count = %d, want 3 (initial + 2 auto-retries)", got)
	}

	// Confirm the orchestrator emitted node_auto_retry events (one per
	// re-dispatch beyond the first) so the dashboard can surface the
	// retry count.
	st, _ := store.Open(p.StateDB())
	defer st.Close()
	events, err := st.ListEventsAfter(context.Background(), res.RunID, 0, 1000)
	if err != nil {
		t.Fatalf("ListEventsAfter: %v", err)
	}
	var autoRetryEvents int
	for _, e := range events {
		if e.NodeID == "flaky" && e.Kind == "node_auto_retry" {
			autoRetryEvents++
		}
	}
	if autoRetryEvents != 2 {
		t.Fatalf("node_auto_retry events = %d, want 2 (one per re-dispatch beyond the first)", autoRetryEvents)
	}
}

// TestAutoRetry_FailsAfterExhaustingAttempts verifies the budget is
// enforced: a node that always fails completes with the run failed
// after AutoRetry(2)+1 = 3 total dispatches.
func TestAutoRetry_FailsAfterExhaustingAttempts(t *testing.T) {
	autoRetryCount.Store(0)
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "auto-retry-exhausts"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != "failed" {
		t.Fatalf("status = %q, want failed (auto-retry should not paper over a node that always fails)", res.Status)
	}
	if got := autoRetryCount.Load(); got != 3 {
		t.Fatalf("dispatch count = %d, want 3 (initial + 2 auto-retries before giving up)", got)
	}

	// Final node row should be Failed with the final attempt's error
	// preserved (not buried under the auto-retry plumbing).
	st, _ := store.Open(p.StateDB())
	defer st.Close()
	nodes, _ := st.ListNodes(context.Background(), res.RunID)
	if len(nodes) != 1 || nodes[0].NodeID != "always-fails" {
		t.Fatalf("expected one always-fails node, got %+v", nodes)
	}
	if nodes[0].Outcome != string(sparkwing.Failed) {
		t.Fatalf("node outcome = %q, want failed", nodes[0].Outcome)
	}
	if !strings.Contains(nodes[0].Error, "infra flake") {
		t.Fatalf("node error should cite the underlying error, got %q", nodes[0].Error)
	}
}
