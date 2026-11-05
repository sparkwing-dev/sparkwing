package nodehost

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

var errExecutorNoProgress = errors.New("job made no progress")

func executorAttemptTimeout(ctx context.Context) error {
	switch cause := context.Cause(ctx); {
	case errors.Is(cause, errExecutorNoProgress):
		return executorError(apierr.DeadlineExceeded, "job attempt exceeded its no-progress deadline")
	case errors.Is(cause, context.DeadlineExceeded):
		return executorError(apierr.DeadlineExceeded, "job attempt exceeded its execution deadline")
	default:
		return nil
	}
}

type awaitNotifyKey struct{}

// WithAwaitNotify reports when a job hosted under ctx starts and stops waiting on
// child runs, so a runner can stop counting it against its job slots. Nested
// waits report once. notify must not block.
func WithAwaitNotify(ctx context.Context, notify func(awaiting bool)) context.Context {
	return context.WithValue(ctx, awaitNotifyKey{}, notify)
}

// AwaitNotify returns the hook WithAwaitNotify installed, or a no-op.
func AwaitNotify(ctx context.Context) func(awaiting bool) {
	if notify, ok := ctx.Value(awaitNotifyKey{}).(func(bool)); ok {
		return notify
	}
	return func(bool) {}
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
	notify := AwaitNotify(parent)
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
			if paused == 1 {
				notify(true)
			}
			reset()
			mu.Unlock()
			var once sync.Once
			return func() {
				once.Do(func() {
					mu.Lock()
					defer mu.Unlock()
					paused--
					if paused == 0 {
						notify(false)
					}
					reset()
				})
			}
		},
	}
	stop := func() {
		mu.Lock()
		defer mu.Unlock()
		stopped = true
		reset()
		cancel(nil)
	}
	return ctx, activity, stop
}
