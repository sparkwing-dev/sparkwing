package teststore

import (
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storeclock"
)

// WithClock opens a SQLite fixture on clock instead of wall time.
// PostgreSQL keeps database time.
func WithClock(clock func() time.Time) store.Option {
	return storeclock.Option(clock).(store.Option)
}
