package objectguard

import (
	"context"
	"math/rand/v2"
	"time"
)

// Backoff spaces retry attempts as exponential growth with full jitter:
// attempt n waits a uniform random duration in [0, min(Max, Base<<n)].
// Full jitter, rather than a fixed or decorrelated delay, keeps many
// processes that fail at the same moment from retrying in lockstep.
type Backoff struct {
	// Base is the first attempt's ceiling.
	Base time.Duration
	// Max caps every later attempt's ceiling.
	Max time.Duration

	// hack: an injected source in [0, n) so a test can pin the jitter.
	random func(n int64) int64
}

// DefaultBackoff spaces object-store retries. The base is short enough
// that a transient error costs no visible latency and the ceiling is
// low enough that a caller waiting on a write does not hang.
var DefaultBackoff = Backoff{Base: 100 * time.Millisecond, Max: 5 * time.Second}

// Delay returns the wait before attempt n, counting the first attempt
// as zero.
func (b Backoff) Delay(attempt int) time.Duration {
	base := b.Base
	if base <= 0 {
		base = 100 * time.Millisecond
	}
	limit := b.Max
	if limit <= 0 {
		limit = 30 * time.Second
	}
	if attempt < 0 {
		attempt = 0
	}
	ceiling := base
	for range attempt {
		if ceiling >= limit {
			break
		}
		ceiling *= 2
	}
	if ceiling > limit || ceiling <= 0 {
		ceiling = limit
	}
	return time.Duration(b.rand(int64(ceiling)))
}

func (b Backoff) rand(n int64) int64 {
	if n <= 0 {
		return 0
	}
	if b.random != nil {
		return b.random(n)
	}
	return rand.Int64N(n)
}

// Wait sleeps for Delay(attempt) or returns the context's error when it
// ends first.
func (b Backoff) Wait(ctx context.Context, attempt int) error {
	d := b.Delay(attempt)
	if d <= 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
