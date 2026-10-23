package objectguard

import "time"

// setClock replaces a limiter's clock so a test can roll a window
// without waiting for one.
func setClock(l *Limiter, now func() time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.now = now
}

// setBackoffRandom makes a backoff's jitter deterministic.
func setBackoffRandom(b backoff, random func(int64) int64) backoff {
	b.random = random
	return b
}
