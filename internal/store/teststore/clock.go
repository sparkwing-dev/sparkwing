package teststore

import (
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storeclock"
)

// WithClock replaces the default pinned clock for fixture reads and transactions.
// Both dialects use it when deriving authority and transaction time.
func WithClock(clock func() time.Time) store.Option {
	return storeclock.Option(clock).(store.Option)
}

// Now returns the fixture's clock for timestamps in seeded records.
func Now(s *store.Store) time.Time { return storeclock.Now(s) }

func fixtureOptions(opts []store.Option) []store.Option {
	// Setup and host load must not consume fixture authority before the test uses it.
	started := time.Now()
	return append([]store.Option{WithClock(func() time.Time { return started })}, opts...)
}
