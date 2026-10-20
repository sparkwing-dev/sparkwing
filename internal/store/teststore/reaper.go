package teststore

import (
	"context"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// ReapUnits acquires the fixture's elected reaper lease and executes one bounded pass.
func ReapUnits(ctx context.Context, s *store.Store) ([]store.ReapedUnit, error) {
	lease, err := s.AsOperator().AcquireSingletonLease(ctx, "controller.reaper", "test-reaper", time.Minute)
	if err != nil {
		return nil, err
	}
	return s.ReapUnits(ctx, lease, 1000)
}
