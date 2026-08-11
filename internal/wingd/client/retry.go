package client

import (
	"context"
	"fmt"
	"math/rand/v2"
	"time"
)

const (
	// retryBaseDelay is the first wait between two attempts at the same
	// daemon exchange. It is short enough that an ordinary daemon blink
	// costs the run nothing an operator would notice.
	retryBaseDelay = 25 * time.Millisecond
	// retryMaxDelay caps the wait. A client that cannot complete an
	// exchange retries about once a second forever rather than as fast as
	// the kernel will let it: every dropped connection makes the daemon
	// fsync its state, so an unpaced retry loop burns a core on each side.
	retryMaxDelay = time.Second
	// readOnlyRetryLimit bounds the exchanges that answer a question
	// rather than hold a claim. Nothing depends on their eventual
	// success, so a daemon that keeps failing them is reported to the
	// caller instead of retried for the life of the process.
	readOnlyRetryLimit = 10
)

// retry paces re-driving one daemon exchange after a transport failure.
// A limit of zero retries until ctx ends, for the exchanges whose callers
// depend on eventual success.
type retry struct {
	op       string
	limit    int
	attempts int
	delay    time.Duration
	max      time.Duration
}

func newRetry(op string, limit int) *retry {
	return newRetryCapped(op, limit, retryMaxDelay)
}

// newRetryCapped is [newRetry] with a tighter ceiling, for waits whose
// own budget is measured in wall-clock time: pacing them to a full second
// would spend that budget on sleeping rather than on looking.
func newRetryCapped(op string, limit int, max time.Duration) *retry {
	if max < retryBaseDelay {
		max = retryBaseDelay
	}
	return &retry{op: op, limit: limit, delay: retryBaseDelay, max: max}
}

// wait records one failed attempt and sleeps before the next. It returns
// an error when the caller must stop: the attempt budget is spent, or ctx
// ended. cause is the transport failure that ended the attempt, and is
// what an exhausted retry reports.
func (r *retry) wait(ctx context.Context, cause error) error {
	r.attempts++
	if r.limit > 0 && r.attempts >= r.limit {
		return fmt.Errorf("wingd/client: %s failed after %d attempts: %w", r.op, r.attempts, cause)
	}
	delay := jitter(r.delay)
	if r.delay < r.max {
		r.delay *= 2
		if r.delay > r.max {
			r.delay = r.max
		}
	}
	return sleep(ctx, delay)
}

// reset returns the pacing to its base after the exchange made progress,
// so a later unrelated blink is recovered from as quickly as the first.
func (r *retry) reset() {
	r.attempts = 0
	r.delay = retryBaseDelay
}

// dialPaceMax caps the wait for a spawned daemon's socket to appear. It
// is deliberately tighter than the other connect waits: this one sits in
// front of every cold run, where the pause between the daemon binding and
// the next probe is latency the user pays before anything starts.
const dialPaceMax = 100 * time.Millisecond

// electionPaceMax caps the wait for a predecessor daemon to release the
// election lock. That wait is genuinely long -- it lasts as long as the
// old daemon takes to go -- and each cycle re-attempts a file lock, so
// pacing it further is worth the quarter second it can add.
const electionPaceMax = 250 * time.Millisecond

// jitter spreads a delay over [d/2, d] so many runs reconnecting to the
// same restarted daemon do not arrive in one burst.
func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	half := int64(d / 2)
	return d/2 + time.Duration(rand.Int64N(half+1))
}
