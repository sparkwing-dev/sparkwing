package client

import (
	"context"
	"fmt"
	"math/rand/v2"
	"time"
)

const (
	retryBaseDelay = 25 * time.Millisecond

	retryMaxDelay = time.Second

	readOnlyRetryLimit = 10
)

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

func newRetryCapped(op string, limit int, max time.Duration) *retry {
	if max < retryBaseDelay {
		max = retryBaseDelay
	}
	return &retry{op: op, limit: limit, delay: retryBaseDelay, max: max}
}

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

func (r *retry) reset() {
	r.attempts = 0
	r.delay = retryBaseDelay
}

const dialPaceMax = 100 * time.Millisecond

const electionPaceMax = 250 * time.Millisecond

func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	half := int64(d / 2)
	// #nosec G404 -- retry jitter, not a security decision
	return d/2 + time.Duration(rand.Int64N(half+1))
}
