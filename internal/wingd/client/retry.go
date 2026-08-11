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
}

func newRetry(op string, limit int) *retry {
	return &retry{op: op, limit: limit, delay: retryBaseDelay}
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
	if r.delay < retryMaxDelay {
		r.delay *= 2
		if r.delay > retryMaxDelay {
			r.delay = retryMaxDelay
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

// jitter spreads a delay over [d/2, d] so many runs reconnecting to the
// same restarted daemon do not arrive in one burst.
func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	half := int64(d / 2)
	return d/2 + time.Duration(rand.Int64N(half+1))
}
