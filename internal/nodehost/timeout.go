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

var errWallTimeout = errors.New("node timeout")

// safety: each attempt gets its own budgets. A success stands even when a
// timer fired, since a firing during the body kills the session; a later
// firing lands in the attempt's cleanups, which the timeouts do not cover.
func timeoutLayer(cfg *Config, next Executor) Executor {
	return func(ctx context.Context, n *NodeRun) Result {
		m := n.Plan
		if value(m.TimeoutMS) <= 0 && value(m.NoProgressTimeoutMS) <= 0 {
			return next(ctx, n)
		}
		wall, idle := millis(value(m.TimeoutMS)), millis(value(m.NoProgressTimeoutMS))
		actx, cancel := context.WithCancelCause(ctx)
		defer cancel(nil)
		attempt := *n
		if wall > 0 {
			wt := &wallTimer{left: wall, since: time.Now()}
			wt.t = time.AfterFunc(wall, func() { cancel(errWallTimeout) })
			defer wt.t.Stop()
			attempt.Hold = wt.hold
		}
		if idle > 0 {
			idleCtx, activity, stop := executorProgressContext(actx, idle)
			defer stop()
			actx = idleCtx
			outer := n.Progress
			attempt.Progress = func() {
				activity.Progress()
				if outer != nil {
					outer()
				}
			}
			attempt.Pause = activity.Await
		}
		res := next(actx, &attempt)
		if res.Outcome == sparkwing.Success {
			return res
		}
		// safety: the attempt's own error is the kill the timer caused, so the
		// message names the timeout the way the in-process executor did.
		cause := context.Cause(actx)
		switch {
		case errors.Is(cause, errExecutorNoProgress):
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

// safety: holds nest like the idle count, and a resume restarts only the budget left, so time spent held is
// excluded rather than refunded.
type wallTimer struct {
	t     *time.Timer
	mu    sync.Mutex
	held  int
	left  time.Duration
	since time.Time
}

func (wt *wallTimer) hold() (resume func()) {
	wt.mu.Lock()
	if wt.held++; wt.held == 1 && wt.t.Stop() {
		wt.left -= time.Since(wt.since)
	}
	wt.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			wt.mu.Lock()
			defer wt.mu.Unlock()
			if wt.held--; wt.held == 0 {
				wt.since = time.Now()
				wt.t.Reset(max(wt.left, 0))
			}
		})
	}
}

// safety: clamped in milliseconds first, so a huge value cannot overflow the
// Duration multiply into a negative or short one.
func millis(ms int64) time.Duration {
	return time.Duration(min(ms, math.MaxInt64/int64(time.Millisecond))) * time.Millisecond
}
