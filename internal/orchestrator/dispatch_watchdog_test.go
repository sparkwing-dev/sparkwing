package orchestrator_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// wedgeRelease lets the test unblock the deliberately-stuck node body
// after the watchdog has fired, so the leaked goroutine drains during
// teardown instead of affecting later tests in the same process.
var wedgeRelease = make(chan struct{})

type wedgedNodePipe struct{ sparkwing.Base }

func (wedgedNodePipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, "wedged", func(ctx context.Context) error {
		<-wedgeRelease
		return nil
	})
	return nil
}

func init() {
	register("wedged-node", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &wedgedNodePipe{} })
	register("watchdog-queue-holder", func() sparkwing.Pipeline[sparkwing.NoInputs] {
		return &watchdogQueueHolderPipe{}
	})
	register("watchdog-queue-waiter", func() sparkwing.Pipeline[sparkwing.NoInputs] {
		return &watchdogQueueWaiterPipe{}
	})
	register("watchdog-unbounded-queue-waiter", func() sparkwing.Pipeline[sparkwing.NoInputs] {
		return &watchdogUnboundedQueueWaiterPipe{}
	})
}

var watchdogQueueRelease chan struct{}

func watchdogQueueStep(hold time.Duration) func(context.Context) error {
	return func(ctx context.Context) error {
		if hold < 0 {
			select {
			case <-watchdogQueueRelease:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		select {
		case <-time.After(hold):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

type watchdogQueueHolderPipe struct{ sparkwing.Base }

func (watchdogQueueHolderPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	group := sparkwing.NewConcurrencyGroup("dispatch-watchdog-queued-wait", sparkwing.ConcurrencyLimit{
		Capacity: 1,
		OnLimit:  sparkwing.Queue,
	})
	sparkwing.Job(plan, "hold", watchdogQueueStep(-1)).Concurrency(group)
	return nil
}

type watchdogQueueWaiterPipe struct{ sparkwing.Base }

func (watchdogQueueWaiterPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	group := sparkwing.NewConcurrencyGroup("dispatch-watchdog-queued-wait", sparkwing.ConcurrencyLimit{
		Capacity:     1,
		OnLimit:      sparkwing.Queue,
		QueueTimeout: 2 * time.Second,
	})
	queued := sparkwing.Job(plan, "queued", watchdogQueueStep(10*time.Millisecond)).Concurrency(group)
	sparkwing.Job(plan, "after-queued", watchdogQueueStep(10*time.Millisecond)).Needs(queued)
	return nil
}

type watchdogUnboundedQueueWaiterPipe struct{ sparkwing.Base }

func (watchdogUnboundedQueueWaiterPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	group := sparkwing.NewConcurrencyGroup("dispatch-watchdog-queued-wait", sparkwing.ConcurrencyLimit{
		Capacity: 1,
		OnLimit:  sparkwing.Queue,
	})
	queued := sparkwing.Job(plan, "queued", watchdogQueueStep(10*time.Millisecond)).Concurrency(group)
	sparkwing.Job(plan, "after-queued", watchdogQueueStep(10*time.Millisecond)).Needs(queued)
	return nil
}

// TestDispatchWatchdog_FiresOnStuckNode: a node whose body ignores ctx
// and never returns leaves a wg slot incremented forever. With a
// short DispatchWaitTimeout the dispatcher must (a) return with an
// error mentioning dispatch_wait_timeout, (b) emit the watchdog event
// into both the envelope and the state events table, (c) name the
// stuck node, and (d) do all of this within a small multiple of the
// timeout (no hidden additional wait).
func TestDispatchWatchdog_FiresOnStuckNode(t *testing.T) {
	t.Cleanup(func() {
		select {
		case <-wedgeRelease:
		default:
			close(wedgeRelease)
		}
	})
	p := newPaths(t)

	start := time.Now()
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{
		Pipeline:            "wedged-node",
		DispatchWaitTimeout: 300 * time.Millisecond,
	})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("RunLocal returned an unexpected error: %v", err)
	}
	if res == nil || res.Status != "failed" || res.Error == nil {
		t.Fatalf("res = %+v, want status=failed with non-nil Error", res)
	}
	runErr := res.Error.Error()
	if !strings.Contains(runErr, "dispatch_wait_timeout") {
		t.Errorf("res.Error %q must mention dispatch_wait_timeout", runErr)
	}
	if !strings.Contains(runErr, "wedged") {
		t.Errorf("res.Error %q must name the stuck node", runErr)
	}
	if elapsed > 5*time.Second {
		t.Errorf("dispatcher returned after %s; watchdog should have fired near 300ms", elapsed)
	}

	st, _ := store.Open(p.StateDB())
	defer func() { _ = st.Close() }()
	events, _ := st.ListEventsAfter(context.Background(), res.RunID, 0, 500)
	found := false
	for _, e := range events {
		if e.Kind == "dispatch_wait_timeout" {
			found = true
			payload := string(e.Payload)
			for _, want := range []string{"wedged", "timeout", "stuck_nodes"} {
				if !strings.Contains(payload, want) {
					t.Errorf("dispatch_wait_timeout payload missing %q: %s", want, payload)
				}
			}
			break
		}
	}
	if !found {
		t.Errorf("no dispatch_wait_timeout event in run stream")
	}
}

// TestDispatchWatchdog_NegativeDisables: a negative timeout opts out
// of the watchdog (the historical wait-forever behavior). We assert
// the opt-out is reachable without actually hanging the test: a node
// that respects ctx cancellation finishes normally, so a -1 timeout
// must produce a clean success.
func TestDispatchWatchdog_NegativeDisables(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(1)
	p := newPaths(t)

	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{
		Pipeline:            "spawn-single",
		DispatchWaitTimeout: -1,
	})
	wg.Done()
	if err != nil {
		t.Fatalf("normal run with disabled watchdog: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("status = %q, want success", res.Status)
	}
}

func TestDispatchWatchdog_DoesNotFireWhileNodeWaitsInConcurrencyQueue(t *testing.T) {
	watchdogQueueRelease = make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-watchdogQueueRelease:
		default:
			close(watchdogQueueRelease)
		}
	})
	paths := newPaths(t)
	state, err := store.Open(paths.StateDB())
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	defer func() { _ = state.Close() }()

	holderLog := newWatchdogEventLog()
	holderDone := make(chan *orchestrator.Result, 1)
	go func() {
		res, _ := runWithSharedStore(t, paths, state, orchestrator.Options{
			RunID:               "dispatch-watchdog-holder",
			Pipeline:            "watchdog-queue-holder",
			DispatchWaitTimeout: time.Second,
			Delegate:            holderLog,
		})
		holderDone <- res
	}()

	holderLog.wait(t, "node_start")

	const waiterRunID = "dispatch-watchdog-waiter"
	waiterLog := newWatchdogEventLog()
	waiterDone := make(chan watchdogRunResult, 1)
	go func() {
		res, runErr := runWithSharedStore(t, paths, state, orchestrator.Options{
			RunID:               waiterRunID,
			Pipeline:            "watchdog-queue-waiter",
			DispatchWaitTimeout: 500 * time.Millisecond,
			Delegate:            waiterLog,
		})
		waiterDone <- watchdogRunResult{res: res, err: runErr}
	}()

	waiterLog.wait(t, "concurrency_wait")
	waitForContinuationBeforeResult(t, waiterLog, waiterDone, 5*time.Second)
	close(watchdogQueueRelease)

	var waiter *orchestrator.Result
	select {
	case got := <-waiterDone:
		waiter = got.res
		err = got.err
	case <-time.After(time.Second):
		t.Fatal("queued waiter did not finish after holder released")
	}
	if err != nil {
		t.Fatalf("queued waiter run returned error: %v", err)
	}
	if waiter == nil || waiter.Status != "success" {
		t.Fatalf("queued waiter result = %+v, want success after admission wait", waiter)
	}
	if waiter.Error != nil && strings.Contains(waiter.Error.Error(), "dispatch_wait_timeout") {
		t.Fatalf("queued admission wait was misreported as dispatch timeout: %v", waiter.Error)
	}

	select {
	case holder := <-holderDone:
		if holder == nil || holder.Status != "success" {
			t.Fatalf("holder result = %+v, want success", holder)
		}
	case <-time.After(time.Second):
		t.Fatal("holder did not finish")
	}
}

func TestDispatchWatchdog_FiresForUnboundedConcurrencyQueueWait(t *testing.T) {
	watchdogQueueRelease = make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-watchdogQueueRelease:
		default:
			close(watchdogQueueRelease)
		}
	})
	paths := newPaths(t)
	state, err := store.Open(paths.StateDB())
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	defer func() { _ = state.Close() }()

	holderLog := newWatchdogEventLog()
	holderDone := make(chan *orchestrator.Result, 1)
	go func() {
		res, _ := runWithSharedStore(t, paths, state, orchestrator.Options{
			RunID:               "dispatch-watchdog-unbounded-holder",
			Pipeline:            "watchdog-queue-holder",
			DispatchWaitTimeout: time.Second,
			Delegate:            holderLog,
		})
		holderDone <- res
	}()
	holderLog.wait(t, "node_start")

	waiterLog := newWatchdogEventLog()
	waiterDone := make(chan watchdogRunResult, 1)
	go func() {
		res, runErr := runWithSharedStore(t, paths, state, orchestrator.Options{
			RunID:               "dispatch-watchdog-unbounded-waiter",
			Pipeline:            "watchdog-unbounded-queue-waiter",
			DispatchWaitTimeout: 100 * time.Millisecond,
			Delegate:            waiterLog,
		})
		waiterDone <- watchdogRunResult{res: res, err: runErr}
	}()

	waiterLog.wait(t, "concurrency_wait")
	result := waitForRunResult(t, waiterDone)
	if result.err != nil {
		t.Fatalf("unbounded queued waiter returned error: %v", result.err)
	}
	if result.res == nil || result.res.Status != "failed" || result.res.Error == nil ||
		!strings.Contains(result.res.Error.Error(), "dispatch_wait_timeout") {
		t.Fatalf("unbounded queued waiter result = %+v, want dispatch_wait_timeout failure", result.res)
	}

	close(watchdogQueueRelease)
	holder := waitForHolderResult(t, holderDone)
	if holder == nil || holder.Status != "success" {
		t.Fatalf("holder result = %+v, want success", holder)
	}
}

type watchdogRunResult struct {
	res *orchestrator.Result
	err error
}

func waitForContinuationBeforeResult(t *testing.T, log *watchdogEventLog, done <-chan watchdogRunResult, timeout time.Duration) {
	t.Helper()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		select {
		case got := <-log.events:
			if got == "dispatch_watchdog_continued" {
				return
			}
		case result := <-done:
			if result.res != nil && result.res.Error != nil && strings.Contains(result.res.Error.Error(), "dispatch_wait_timeout") {
				t.Fatalf("queued admission wait was killed by dispatch watchdog before admission released: %v", result.res.Error)
			}
			t.Fatalf("queued waiter finished before dispatch watchdog continuation: res=%+v err=%v", result.res, result.err)
		case <-deadline.C:
			t.Fatal("dispatch watchdog continuation did not appear before deadline")
		}
	}
}

func waitForRunResult(t *testing.T, done <-chan watchdogRunResult) watchdogRunResult {
	t.Helper()
	select {
	case result := <-done:
		return result
	case <-time.After(time.Second):
		t.Fatal("run did not finish before deadline")
		return watchdogRunResult{}
	}
}

func waitForHolderResult(t *testing.T, done <-chan *orchestrator.Result) *orchestrator.Result {
	t.Helper()
	select {
	case result := <-done:
		return result
	case <-time.After(time.Second):
		t.Fatal("holder did not finish")
		return nil
	}
}

type watchdogEventLog struct {
	events chan string
}

func newWatchdogEventLog() *watchdogEventLog {
	return &watchdogEventLog{events: make(chan string, 16)}
}

func (l *watchdogEventLog) Log(level, msg string) {
	l.Emit(sparkwing.LogRecord{Level: level, Msg: msg})
}

func (l *watchdogEventLog) Emit(rec sparkwing.LogRecord) {
	if rec.Event == "" {
		return
	}
	select {
	case l.events <- rec.Event:
	default:
	}
}

func (l *watchdogEventLog) wait(t *testing.T, event string) {
	t.Helper()
	for {
		select {
		case got := <-l.events:
			if got == event {
				return
			}
		case <-time.After(time.Second):
			t.Fatalf("event %s did not appear before deadline", event)
		}
	}
}
