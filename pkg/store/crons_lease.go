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

// CronTickWindow is the shortest gap RunCronTickLeased allows between two
// leased ticks. It sits just under a minute on purpose: a caller waking on a
// one-minute timer stamps its tick a little after it started, so a full minute
// here would turn that caller away and skip the minute it woke for.
const CronTickWindow = 55 * time.Second

// RunCronTickLeased runs fn under a store-wide lease, so ordinarily one caller
// evaluates the schedules per minute however many processes share the store.
// It reports whether fn ran: false means another holder owns this minute, or
// the last leased tick is less than [CronTickWindow] old, and is the ordinary
// answer rather than a failure.
//
// The lease is advisory. It is a cheap way to keep several evaluators off the
// same minute, not the thing that makes a due instant fire once: a holder whose
// claim expires mid-tick, or a clock that disagrees with its peer's, can leave
// two ticks overlapping. Correctness rests on the launch's idempotency key and
// on the schedule's monotone cursor, both of which turn a second evaluation of
// one instant into a no-op.
//
// holder names the caller in the claim, ttl is how long the claim survives a
// caller that dies mid-tick, and fn's own error is returned after the claim is
// released so the next minute is not blocked by a failed tick. The claim is
// released even when fn panics or when ctx is already done, so a cancelled tick
// does not hold the minute for the whole ttl.
func (s *Store) RunCronTickLeased(ctx context.Context, holder string, ttl time.Duration, fn func(context.Context) error) (ran bool, err error) {
	if fn == nil {
		return false, fmt.Errorf("RunCronTickLeased: a tick function is required")
	}
	if ttl <= 0 {
		ttl = CronTickWindow
	}
	claimed, token, err := s.claimSweepWindow(ctx, metaKeyCronTickAt, metaKeyCronTickClaim, CronTickWindow, ttl)
	if err != nil {
		return false, fmt.Errorf("claim the cron tick for %s: %w", holder, err)
	}
	if !claimed {
		return false, nil
	}
	ran = true
	// safety: the release outlives ctx, because a tick cut short by its own
	// deadline still has to hand the minute back rather than sit on the claim
	// until the ttl runs out.
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("cron tick for %s panicked: %v", holder, p)
		}
		if cerr := s.clearSweepClaim(context.WithoutCancel(ctx), metaKeyCronTickClaim, token); err == nil {
			err = cerr
		}
	}()
	if err = fn(ctx); err == nil {
		err = s.stampSweepWindow(ctx, metaKeyCronTickAt)
	}
	return ran, err
}
