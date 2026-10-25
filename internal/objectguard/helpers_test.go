package objectguard

import "time"

// setClock replaces a limiter's clock so a test can roll a window
// without waiting for one.
func setClock(l *Limiter, now func() time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.now = now
}
