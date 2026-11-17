package launcher

import (
	"context"
	"time"
)

type claimResult struct {
	award     *CloudJobAward
	pollAfter time.Duration
	immediate bool
	err       error
	received  time.Time
}

type claimWaiter interface {
	waitClaim(context.Context, string, []string, time.Duration) claimResult
}

type claimWait struct {
	context     context.Context
	launcher    *Launcher
	pending     <-chan claimResult
	result      *claimResult
	completedAt time.Time
}

func (c *claimWait) stop(cancel context.CancelFunc) {
	cancel()
	if c.pending != nil {
		result := <-c.pending
		c.result = &result
	}
	if c.result != nil && shutdownFault(c.context, c.result.err) != nil {
		c.launcher.logger().Warn("launcher claim failed", failureAttrs(c.result.err)...)
	}
}

func (c *claimWait) step(ctx context.Context, poll time.Duration) (launched bool, pollAfter time.Duration, immediate, completed bool, err error) {
	waiter, ok := c.launcher.Control.(claimWaiter)
	if !ok {
		launched, pollAfter, err = c.launcher.claimReady(ctx)
		c.completedAt = time.Now()
		return launched, pollAfter, false, true, err
	}
	if c.result != nil {
		result := *c.result
		c.result = nil
		c.completedAt = result.received
		if result.award != nil {
			launched, err = c.launcher.launch(ctx, result.award)
			c.completedAt = time.Now()
		} else {
			err = result.err
		}
		return launched, result.pollAfter, result.immediate, true, err
	}
	if c.pending == nil {
		excluded := make([]string, 0, len(c.launcher.Config.StoppedTeams))
		for _, team := range c.launcher.Config.StoppedTeams {
			excluded = append(excluded, TeamNodeValue(c.launcher.Config.Namespace, team))
		}
		done := make(chan claimResult, 1)
		c.pending = done
		// safety: only delivery waits concurrently; all placement and teardown stay in the reconciliation loop.
		go func() {
			result := waiter.waitClaim(ctx, c.launcher.Config.Namespace, excluded, poll)
			result.received = time.Now()
			done <- result
		}()
	}
	return false, 0, false, false, nil
}
