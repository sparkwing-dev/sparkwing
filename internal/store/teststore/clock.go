package teststore

import (
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storeclock"
)

// WithClock opens a SQLite fixture on clock instead of wall time.
// PostgreSQL keeps database time.
func WithClock(clock func() time.Time) store.Option {
	return storeclock.Option(clock).(store.Option)
}
