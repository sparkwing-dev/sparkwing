package nodehost

import (
	"context"
	"fmt"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// safety: retry_auto shares the budget and backoff, because every attempt
// already runs in a fresh SDK process, which is the whole-node re-dispatch
// retry_auto asks for. The node row stays running until the last attempt
// ends, since only the scheduler finishes it.
func retryLayer(_ *Config, next Executor) Executor {
	return func(ctx context.Context, n *NodeRun) Result {
		m := n.Plan.Modifiers
		if m == nil || m.Retry <= 0 {
			return next(ctx, n)
		}
		first, budget := max(n.Attempt, 1), m.Retry+1
		backoff := millis(m.RetryBackoffMS)
		var res Result
		for i := range budget {
			if i > 0 {
				wait := scaledBackoff(backoff, i)
				msg := "retry attempt"
				if m.RetryAuto {
					msg = "auto-retry dispatch"
				}
				n.Run.Logger.Info("nodehost: "+msg, "run", n.Run.ID, "node", n.Plan.ID,
					"attempt", fmt.Sprintf("%d/%d", first+i, budget), "after", wait, "last_err", errText(res.Err))
				if !waitFor(ctx, wait) {
					return res
				}
			}
			attempt := *n
			attempt.Attempt = first + i
			res = next(ctx, &attempt)
			// safety: a cancelled node is never retried, nor a failure the run's cancel caused.
			if res.Outcome != sparkwing.Failed || ctx.Err() != nil {
				return res
			}
		}
		return res
	}
}

func scaledBackoff(initial time.Duration, attempt int) time.Duration {
	if initial <= 0 || attempt <= 0 {
		return 0
	}
	out := initial
	for i := 1; i < attempt; i++ {
		out *= 2
		if out > 5*time.Minute {
			return 5 * time.Minute
		}
	}
	return out
}

func waitFor(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}
