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
func retryLayer(cfg *Config, next Executor) Executor {
	return func(ctx context.Context, n *NodeRun) Result {
		m := n.Plan.Modifiers
		if m == nil || m.Retry <= 0 {
			return next(ctx, n)
		}
		first, budget := max(n.Attempt, 1), m.Retry+1
		backoff := millis(m.RetryBackoffMS)
		// safety: one rule for both the loop and the attempt's Retried, so after_run
		// runs in exactly the attempt the loop ends on.
		retries := func(r Result) bool { return r.Outcome == sparkwing.Failed && !r.Final && ctx.Err() == nil }
		var res Result
		for i := range budget {
			if i > 0 && !retryAnnounce(ctx, cfg, n, first+i, budget, scaledBackoff(backoff, i)) {
				return res
			}
			attempt := *n
			attempt.Attempt = first + i
			attempt.Retried = func(r Result) bool { return i+1 < budget && retries(r) }
			res = next(ctx, &attempt)
			// safety: a cancelled node is never retried, nor a failure the run's cancel caused,
			// nor a Final one.
			if !retries(res) {
				return res
			}
		}
		return res
	}
}

// safety: the lines and events are the dispatcher's: a plain retry notes its
// attempt in the node log and records attempt_retry once the backoff ends, and
// retry_auto records node_auto_retry before it, as a re-dispatch does.
func retryAnnounce(ctx context.Context, cfg *Config, n *NodeRun, ordinal, budget int, wait time.Duration) bool {
	after := ""
	if wait > 0 {
		after = " after " + wait.String()
	}
	if n.Plan.Modifiers.RetryAuto {
		hostNodeLog(ctx, cfg, n, sparkwing.LogRecord{Level: "info", Msg: fmt.Sprintf("auto-retry dispatch %d/%d%s", ordinal, budget, after)})
		hostEvent(ctx, n.Run, n.Plan.ID, "node_auto_retry", fmt.Appendf(nil, "dispatch %d/%d", ordinal, budget))
		return waitFor(ctx, wait)
	}
	hostNodeLog(ctx, cfg, n, sparkwing.LogRecord{
		Level: "info", Event: "retry", Msg: fmt.Sprintf("retry attempt %d/%d%s", ordinal, budget, after),
		Attrs: map[string]any{"attempt": ordinal, "total": budget},
	})
	if !waitFor(ctx, wait) {
		return false
	}
	hostEvent(ctx, n.Run, n.Plan.ID, "attempt_retry", fmt.Appendf(nil, "attempt %d/%d", ordinal, budget))
	return true
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
