package teststore

import (
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storeclock"
)

// WithClock pins fixture reads and transactions to clock in either dialect.
func WithClock(clock func() time.Time) store.Option {
	return storeclock.Option(clock).(store.Option)
}

// Now returns the fixture's clock for timestamps in seeded records.
func Now(s *store.Store) time.Time { return storeclock.Now(s) }

func fixtureOptions(opts []store.Option) []store.Option {
	// Fixture authority must outlive scheduling delays; expiry tests advance their clock.
	now := time.Now().UTC()
	return append([]store.Option{WithClock(func() time.Time { return now })}, opts...)
}
