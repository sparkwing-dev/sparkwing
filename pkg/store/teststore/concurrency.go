package teststore

import (
	"context"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// MaintainConcurrency executes a pass under the fixture's elected reaper lease.
func MaintainConcurrency(ctx context.Context, s *store.Store, opts store.ConcurrencyMaintenanceOptions) (store.ConcurrencyMaintenanceResult, error) {
	lease, err := s.AsOperator().AcquireSingletonLease(ctx, "controller.reaper", "test-reaper", time.Minute)
	if err != nil {
		return store.ConcurrencyMaintenanceResult{}, err
	}
	return s.AsOperator().MaintainConcurrency(ctx, lease, opts)
}
