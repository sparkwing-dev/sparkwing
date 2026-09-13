package objectguard

import "time"

// SetClock replaces a limiter's clock so a test can roll a window
// without waiting for one.
func SetClock(l *Limiter, now func() time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.now = now
}

// SetBackoffRandom makes a Backoff's jitter deterministic.
func SetBackoffRandom(b Backoff, random func(int64) int64) Backoff {
	b.random = random
	return b
}
