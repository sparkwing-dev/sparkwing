package store

import "time"

// Option configures a Store before Open returns it, so no holder can swap a
// policy while the store serves requests.
type Option func(*Store)

// WithClock supplies the SQLite store clock. PostgreSQL keeps database time.
func WithClock(clock func() time.Time) Option {
	return func(s *Store) { s.clock = clock }
}
