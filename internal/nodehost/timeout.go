package nodehost

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

var (
	errWallTimeout = errors.New("node timeout")
	errNoProgress  = errors.New("node made no progress")
)

// safety: each attempt gets its own budgets. A success stands even when a
// timer fired, since a firing during the body kills the session; a later
// firing lands in the attempt's cleanups, which the timeouts do not cover.
func timeoutLayer(cfg *Config, next Executor) Executor {
	return func(ctx context.Context, n *NodeRun) Result {
		m := n.Plan.Modifiers
		if m == nil || (m.TimeoutMS <= 0 && m.NoProgressTimeoutMS <= 0) {
			return next(ctx, n)
		}
		wall, idle := millis(m.TimeoutMS), millis(m.NoProgressTimeoutMS)
		actx, cancel := context.WithCancelCause(ctx)
		defer cancel(nil)
		attempt := *n
		if wall > 0 {
			t := time.AfterFunc(wall, func() { cancel(errWallTimeout) })
			defer t.Stop()
		}
		if idle > 0 {
			it := &idleTimer{d: idle}
			it.t = time.AfterFunc(idle, func() { cancel(errNoProgress) })
			defer it.t.Stop()
			outer := n.Progress
			attempt.Progress = func() {
				it.progress()
				if outer != nil {
					outer()
				}
			}
			attempt.Pause = it.pause
		}
		res := next(actx, &attempt)
		if res.Outcome == sparkwing.Success {
			return res
		}
		// safety: the attempt's own error is the kill the timer caused, so the
		// message names the timeout the way the in-process executor did.
		cause := context.Cause(actx)
		switch {
		case errors.Is(cause, errNoProgress):
			res = Result{
				Outcome: sparkwing.Failed, Reason: store.FailureNoProgressTimeout,
				Err: fmt.Errorf("no progress for %s: %w", idle, context.DeadlineExceeded),
			}
		case errors.Is(cause, errWallTimeout):
			res = Result{
				Outcome: sparkwing.Failed, Reason: store.FailureTimeout,
				Err: fmt.Errorf("timeout exceeded (%s): %w", wall, context.DeadlineExceeded),
			}
		default:
			return res
		}
		afterRunAfterTimeout(ctx, cfg, n, res)
		return res
	}
}

// safety: concurrent waits nest, so the count stays held until the last one
// resumes, and a line logged while held does not restart it early.
type idleTimer struct {
	d      time.Duration
	t      *time.Timer
	mu     sync.Mutex
	paused int
}

func (it *idleTimer) progress() {
	it.mu.Lock()
	defer it.mu.Unlock()
	if it.paused == 0 {
		it.t.Reset(it.d)
	}
}

func (it *idleTimer) pause() (resume func()) {
	it.mu.Lock()
	it.paused++
	it.t.Stop()
	it.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			it.mu.Lock()
			defer it.mu.Unlock()
			if it.paused--; it.paused == 0 {
				it.t.Reset(it.d)
			}
		})
	}
}

// safety: clamped in milliseconds first, so a huge value cannot overflow the
// Duration multiply into a negative or short one.
func millis(ms int64) time.Duration {
	return time.Duration(min(ms, math.MaxInt64/int64(time.Millisecond))) * time.Millisecond
}
