package nodehost

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
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

func executorProgressContext(parent context.Context, idle time.Duration) (context.Context, ExecutorActivity, func()) {
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
			reset()
			mu.Unlock()
			var once sync.Once
			return func() {
				once.Do(func() {
					mu.Lock()
					defer mu.Unlock()
					paused--
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
