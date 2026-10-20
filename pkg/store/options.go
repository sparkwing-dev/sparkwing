package store

import (
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storeclock"
)

// Option configures a Store inside Open. Its function is unexported, so an
// option cannot be applied to a store that is already serving.
type Option struct{ apply func(*Store) }

func init() {
	storeclock.Option = func(clock func() time.Time) any {
		return Option{apply: func(s *Store) { s.clock = clock }}
	}
}
