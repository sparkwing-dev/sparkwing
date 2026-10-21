package teststore

import (
	"context"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storeclock"
)

// WithClock opens a SQLite fixture on clock instead of wall time.
// PostgreSQL keeps database time.
func WithClock(clock func() time.Time) store.Option {
	return storeclock.Option(clock).(store.Option)
}

// Now reads the store's transaction clock for raw authority and lease fixtures.
func Now(ctx context.Context, s *store.Store) (time.Time, error) {
	return storeclock.Now(ctx, s)
}

func monotonicClock() store.Option {
	start := time.Now()
	// Wall-clock corrections must not put a freshly issued fixture in the future.
	return WithClock(func() time.Time { return start.Add(time.Since(start)) })
}
