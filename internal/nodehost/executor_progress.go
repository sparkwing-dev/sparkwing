package nodehost

import hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"

import (
	"context"
	"errors"
	"sync"
	"time"
)

var errExecutorNoProgress = errors.New("job made no progress")

func executorAttemptTimeout(ctx context.Context) error {
	switch cause := context.Cause(ctx); {
	case errors.Is(cause, errExecutorNoProgress):
		return executorError(hosterr.DeadlineExceeded, "job attempt exceeded its no-progress deadline")
	case errors.Is(cause, context.DeadlineExceeded):
		return executorError(hosterr.DeadlineExceeded, "job attempt exceeded its execution deadline")
	default:
		return nil
	}
}

type awaitSlotKey struct{}

// WithAwaitSlot installs a slot suspension hook. Suspending releases capacity;
// its returned resume function must reacquire it or wait for context cancellation.
func WithAwaitSlot(ctx context.Context, suspend func() func(context.Context)) context.Context {
	return context.WithValue(ctx, awaitSlotKey{}, suspend)
}

// AwaitSlot returns the suspension hook installed by WithAwaitSlot, or a no-op.
func AwaitSlot(ctx context.Context) func() func(context.Context) {
	if suspend, ok := ctx.Value(awaitSlotKey{}).(func() func(context.Context)); ok {
		return suspend
	}
	return func() func(context.Context) { return func(context.Context) {} }
}

type outcomeNotifyKey struct{}

// WorkOutcome is the terminal state a host recorded for a run, or for one of its jobs when JobID is set.
type WorkOutcome struct{ RunID, JobID, State string }

// WithOutcomeNotify reports each terminal state a host under ctx records, so a
// one-shot runner can fail when the work it executed failed. notify must not block.
func WithOutcomeNotify(ctx context.Context, notify func(WorkOutcome)) context.Context {
	return context.WithValue(ctx, outcomeNotifyKey{}, notify)
}

// OutcomeNotify returns the hook WithOutcomeNotify installed, or a no-op.
func OutcomeNotify(ctx context.Context) func(WorkOutcome) {
	if notify, ok := ctx.Value(outcomeNotifyKey{}).(func(WorkOutcome)); ok {
		return notify
	}
	return func(WorkOutcome) {}
}

func executorProgressContext(parent context.Context, idle time.Duration) (context.Context, ExecutorActivity, func()) {
	suspend := AwaitSlot(parent)
	var resume func(context.Context)
	ctx, cancel := context.WithCancelCause(parent)
	var mu sync.Mutex
	var timer *time.Timer
	var revision uint64
	paused := 0
	stopped := false
	reset := func() {
		revision++
		if timer != nil {
			timer.Stop()
		}
		if idle <= 0 || stopped || paused > 0 {
			return
		}
		current := revision
		timer = time.AfterFunc(idle, func() {
			mu.Lock()
			defer mu.Unlock()
			// safety: a stopped callback may already be queued when a wait or log resets the budget.
			if current == revision && !stopped && paused == 0 {
				cancel(errExecutorNoProgress)
			}
		})
	}
	mu.Lock()
	reset()
	mu.Unlock()
	activity := ExecutorActivity{
		Progress: func() {
			mu.Lock()
			defer mu.Unlock()
			reset()
		},
		Await: func() func() {
			mu.Lock()
			paused++
			if resume == nil {
				resume = suspend()
			}
			reset()
			mu.Unlock()
			var once sync.Once
			return func() {
				once.Do(func() {
					mu.Lock()
					defer mu.Unlock()
					paused--
					if resume != nil {
						resume(ctx)
						resume = nil
					}
					reset()
				})
			}
		},
	}
	stop := func() {
		cancel(nil)
		mu.Lock()
		defer mu.Unlock()
		stopped = true
		reset()
	}
	return ctx, activity, stop
}
