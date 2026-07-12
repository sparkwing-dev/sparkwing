package orchestrator

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type watchdogDynamicSource struct {
	sparkwing.Base
	sparkwing.Produces[[]string]
}

func (watchdogDynamicSource) Work(work *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(work, "discover", func(ctx context.Context) ([]string, error) {
		return []string{"queued"}, nil
	}), nil
}

func TestDispatchContinuationRequiresEveryUnresolvedNodeCovered(t *testing.T) {
	plan := sparkwing.NewPlan()
	sparkwing.Job(plan, "progressing", func(ctx context.Context) error { return nil })
	sparkwing.Job(plan, "wedged", func(ctx context.Context) error { return nil })

	since := time.Now().Add(-time.Second)
	events := []store.Event{{
		RunID:  "run-watchdog-mixed",
		Seq:    1,
		NodeID: "progressing",
		Kind:   "node_started",
		TS:     time.Now(),
	}}
	continuation, err := unresolvedNodesBlockedByAdmission(
		context.Background(),
		pagedWatchdogState{events: events},
		"run-watchdog-mixed",
		plan,
		&dispatchState{outcomes: map[string]sparkwing.Outcome{}},
		since,
		time.Now(),
	)
	if err != nil {
		t.Fatalf("unresolvedNodesBlockedByAdmission: %v", err)
	}
	if continuation.Continue {
		t.Fatalf("continuation = %+v, want watchdog to fire when one unresolved node has no progress or bounded admission wait", continuation)
	}
}

func TestWaitForDispatchWithoutTimeoutAbortsOnContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	done := make(chan dispatchWaitResult, 1)
	go func() {
		done <- waitForDispatch(ctx, &wg, 0, nil)
	}()

	cancel()

	select {
	case got := <-done:
		if got != dispatchWaitAborted {
			t.Fatalf("waitForDispatch = %v, want dispatchWaitAborted", got)
		}
	case <-time.After(time.Second):
		t.Fatal("waitForDispatch did not abort after context cancellation")
	}
}

func TestDispatchContinuationExpiresBoundedAdmissionWait(t *testing.T) {
	plan := sparkwing.NewPlan()
	group := sparkwing.NewConcurrencyGroup("watchdog-expired-queue", sparkwing.ConcurrencyLimit{
		Capacity:     1,
		OnLimit:      sparkwing.Queue,
		QueueTimeout: 50 * time.Millisecond,
	})
	sparkwing.Job(plan, "queued", func(ctx context.Context) error { return nil }).Concurrency(group)

	now := time.Now()
	events := []store.Event{{
		RunID:   "run-watchdog-expired",
		Seq:     1,
		NodeID:  "queued",
		Kind:    "concurrency_wait",
		TS:      now.Add(-time.Second),
		Payload: []byte(`{"kind":"queued"}`),
	}}
	continuation, err := unresolvedNodesBlockedByAdmission(
		context.Background(),
		pagedWatchdogState{events: events},
		"run-watchdog-expired",
		plan,
		&dispatchState{outcomes: map[string]sparkwing.Outcome{}},
		now.Add(-2*time.Second),
		now,
	)
	if err != nil {
		t.Fatalf("unresolvedNodesBlockedByAdmission: %v", err)
	}
	if continuation.Continue {
		t.Fatalf("continuation = %+v, want expired bounded admission wait to stop extending watchdog", continuation)
	}
}

func TestDispatchContinuationFollowsReadyDynamicGroupMembers(t *testing.T) {
	plan := sparkwing.NewPlan()
	source := sparkwing.Job(plan, "source", &watchdogDynamicSource{})
	dynamicGroup := sparkwing.JobFanOutDynamic[string](plan, "dynamic", source, func(item string) (string, any) {
		return item, func(ctx context.Context) error { return nil }
	})
	concurrencyGroup := sparkwing.NewConcurrencyGroup("watchdog-dynamic-queue", sparkwing.ConcurrencyLimit{
		Capacity:     1,
		OnLimit:      sparkwing.Queue,
		QueueTimeout: time.Second,
	})
	queued := sparkwing.Job(plan, "queued", func(ctx context.Context) error { return nil }).Concurrency(concurrencyGroup)
	downstream := sparkwing.Job(plan, "downstream", func(ctx context.Context) error { return nil }).Needs(dynamicGroup)
	sparkwing.RuntimePlumbing.Fns.JobGroupFinalize(dynamicGroup, []*sparkwing.JobNode{queued}, nil)

	now := time.Now()
	events := []store.Event{{
		RunID:   "run-watchdog-dynamic",
		Seq:     1,
		NodeID:  queued.ID(),
		Kind:    "concurrency_wait",
		TS:      now,
		Payload: []byte(`{"kind":"queued"}`),
	}}
	state := &dispatchState{outcomes: map[string]sparkwing.Outcome{source.ID(): sparkwing.Success}}
	continuation, err := unresolvedNodesBlockedByAdmission(
		context.Background(),
		pagedWatchdogState{events: events},
		"run-watchdog-dynamic",
		plan,
		state,
		now.Add(-time.Millisecond),
		now,
	)
	if err != nil {
		t.Fatalf("unresolvedNodesBlockedByAdmission: %v", err)
	}
	if !continuation.Continue {
		t.Fatalf("continuation = %+v, want downstream %q covered by dynamic member %q admission wait", continuation, downstream.ID(), queued.ID())
	}
}

func TestStuckNodeIDsIncludesScheduledDynamicMembers(t *testing.T) {
	plan := sparkwing.NewPlan()
	source := sparkwing.Job(plan, "source", &watchdogDynamicSource{})
	dynamicGroup := sparkwing.JobFanOutDynamic[string](plan, "dynamic", source, func(item string) (string, any) {
		return item, func(ctx context.Context) error { return nil }
	})
	queued := sparkwing.Job(plan, "queued", func(ctx context.Context) error { return nil })
	sparkwing.Job(plan, "downstream", func(ctx context.Context) error { return nil }).Needs(dynamicGroup)
	sparkwing.RuntimePlumbing.Fns.JobGroupFinalize(dynamicGroup, []*sparkwing.JobNode{queued}, nil)

	state := &dispatchState{
		outcomes:  map[string]sparkwing.Outcome{source.ID(): sparkwing.Success},
		scheduled: map[string]*sparkwing.JobNode{queued.ID(): queued},
	}

	got := stuckNodeIDs(plan, state)
	if !slices.Contains(got, queued.ID()) {
		t.Fatalf("stuckNodeIDs = %v, want scheduled dynamic node %q", got, queued.ID())
	}
	if slices.Contains(got, source.ID()) {
		t.Fatalf("stuckNodeIDs = %v, completed source %q should not be reported", got, source.ID())
	}
}

func TestDispatchContinuationFollowsPendingDynamicGroupSource(t *testing.T) {
	plan := sparkwing.NewPlan()
	concurrencyGroup := sparkwing.NewConcurrencyGroup("watchdog-dynamic-source-queue", sparkwing.ConcurrencyLimit{
		Capacity:     1,
		OnLimit:      sparkwing.Queue,
		QueueTimeout: time.Second,
	})
	source := sparkwing.Job(plan, "source", &watchdogDynamicSource{}).Concurrency(concurrencyGroup)
	dynamicGroup := sparkwing.JobFanOutDynamic[string](plan, "dynamic", source, func(item string) (string, any) {
		return item, func(ctx context.Context) error { return nil }
	})
	downstream := sparkwing.Job(plan, "downstream", func(ctx context.Context) error { return nil }).Needs(dynamicGroup)

	now := time.Now()
	events := []store.Event{{
		RunID:   "run-watchdog-pending-dynamic",
		Seq:     1,
		NodeID:  source.ID(),
		Kind:    "concurrency_wait",
		TS:      now,
		Payload: []byte(`{"kind":"queued"}`),
	}}
	continuation, err := unresolvedNodesBlockedByAdmission(
		context.Background(),
		pagedWatchdogState{events: events},
		"run-watchdog-pending-dynamic",
		plan,
		&dispatchState{outcomes: map[string]sparkwing.Outcome{}},
		now.Add(-time.Millisecond),
		now,
	)
	if err != nil {
		t.Fatalf("unresolvedNodesBlockedByAdmission: %v", err)
	}
	if !continuation.Continue {
		t.Fatalf("continuation = %+v, want downstream %q covered by pending dynamic source %q admission wait", continuation, downstream.ID(), source.ID())
	}
}

func TestDispatchContinuationIncludesOnFailureRecoveryNodes(t *testing.T) {
	plan := sparkwing.NewPlan()
	recoveryGroup := sparkwing.NewConcurrencyGroup("watchdog-recovery-queue", sparkwing.ConcurrencyLimit{
		Capacity:     1,
		OnLimit:      sparkwing.Queue,
		QueueTimeout: time.Second,
	})
	parent := sparkwing.Job(plan, "parent", func(ctx context.Context) error { return errors.New("boom") })
	parent.OnFailure("recover", func(ctx context.Context) error { return nil })
	recovery := parent.OnFailureNode().Concurrency(recoveryGroup)

	now := time.Now()
	events := []store.Event{{
		RunID:   "run-watchdog-recovery",
		Seq:     1,
		NodeID:  recovery.ID(),
		Kind:    "concurrency_wait",
		TS:      now,
		Payload: []byte(`{"kind":"queued"}`),
	}}
	continuation, err := unresolvedNodesBlockedByAdmission(
		context.Background(),
		pagedWatchdogState{events: events},
		"run-watchdog-recovery",
		plan,
		&dispatchState{outcomes: map[string]sparkwing.Outcome{parent.ID(): sparkwing.Failed}},
		now.Add(-time.Millisecond),
		now,
	)
	if err != nil {
		t.Fatalf("unresolvedNodesBlockedByAdmission: %v", err)
	}
	if !continuation.Continue {
		t.Fatalf("continuation = %+v, want recovery node %q covered by its own admission wait", continuation, recovery.ID())
	}
}

func TestDispatchContinuationPropagatesEventReadError(t *testing.T) {
	plan := sparkwing.NewPlan()
	sparkwing.Job(plan, "queued", func(ctx context.Context) error { return nil })

	wantErr := errors.New("event read failed")
	_, err := unresolvedNodesBlockedByAdmission(
		context.Background(),
		pagedWatchdogState{err: wantErr},
		"run-watchdog-event-read",
		plan,
		&dispatchState{outcomes: map[string]sparkwing.Outcome{}},
		time.Now().Add(-time.Millisecond),
		time.Now(),
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
}

func TestDispatchWatchdogEventsReadsPastFirstPage(t *testing.T) {
	events := make([]store.Event, 0, dispatchWatchdogEventPageSize+1)
	for i := 1; i <= dispatchWatchdogEventPageSize; i++ {
		events = append(events, store.Event{
			RunID:  "run-watchdog-pages",
			Seq:    int64(i),
			NodeID: "filler",
			Kind:   "node_started",
			TS:     time.Now(),
		})
	}
	events = append(events, store.Event{
		RunID:   "run-watchdog-pages",
		Seq:     int64(dispatchWatchdogEventPageSize + 1),
		NodeID:  "queued",
		Kind:    "concurrency_wait",
		TS:      time.Now(),
		Payload: []byte(`{"kind":"queued","queue_timeout_ms":2000}`),
	})

	got, err := dispatchWatchdogEvents(context.Background(), pagedWatchdogState{events: events}, "run-watchdog-pages")
	if err != nil {
		t.Fatalf("dispatchWatchdogEvents: %v", err)
	}
	if len(got) != dispatchWatchdogEventPageSize+1 {
		t.Fatalf("events len = %d, want %d", len(got), dispatchWatchdogEventPageSize+1)
	}
	if last := got[len(got)-1]; last.Kind != "concurrency_wait" || last.NodeID != "queued" {
		t.Fatalf("last event = %+v, want queued concurrency_wait from page two", last)
	}
}

type pagedWatchdogState struct {
	StateBackend
	events []store.Event
	err    error
}

func (s pagedWatchdogState) ListEventsAfter(ctx context.Context, runID string, afterSeq int64, limit int) ([]store.Event, error) {
	if s.err != nil {
		return nil, s.err
	}
	if limit <= 0 {
		limit = dispatchWatchdogEventPageSize
	}
	page := make([]store.Event, 0, limit)
	for _, event := range s.events {
		if event.RunID == runID && event.Seq > afterSeq {
			page = append(page, event)
			if len(page) == limit {
				break
			}
		}
	}
	return page, nil
}
