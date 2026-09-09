package store

import (
	"context"
	"fmt"
	"time"
)

const (
	metaKeyCronTickAt    = "crons.tick"
	metaKeyCronTickClaim = "crons.tick.claim"
)

// CronTickInterval is the shortest gap RunCronTickLeased allows between two
// leased ticks. It sits just under a minute on purpose: a caller waking on a
// one-minute timer stamps its tick a little after it started, so a full minute
// here would turn that caller away and skip the minute it woke for.
const CronTickInterval = 55 * time.Second

// RunCronTickLeased runs fn under a store-wide lease, so exactly one caller
// evaluates the schedules per minute however many processes share the store.
// It reports whether fn ran: false means another holder owns this minute, or
// the last leased tick is less than [CronTickInterval] old, and is the ordinary
// answer rather than a failure.
//
// holder names the caller in the claim, ttl is how long the claim survives a
// caller that dies mid-tick, and fn's own error is returned after the claim is
// released so the next minute is not blocked by a failed tick.
func (s *Store) RunCronTickLeased(ctx context.Context, holder string, ttl time.Duration, fn func(context.Context) error) (bool, error) {
	if fn == nil {
		return false, fmt.Errorf("RunCronTickLeased: a tick function is required")
	}
	if ttl <= 0 {
		ttl = CronTickInterval
	}
	claimed, token, err := s.claimSweepWindow(ctx, metaKeyCronTickAt, metaKeyCronTickClaim, CronTickInterval, ttl)
	if err != nil {
		return false, fmt.Errorf("claim the cron tick for %s: %w", holder, err)
	}
	if !claimed {
		return false, nil
	}
	runErr := fn(ctx)
	if runErr == nil {
		runErr = s.stampSweepWindow(ctx, metaKeyCronTickAt)
	}
	if cerr := s.clearSweepClaim(ctx, metaKeyCronTickClaim, token); runErr == nil {
		runErr = cerr
	}
	return true, runErr
}
