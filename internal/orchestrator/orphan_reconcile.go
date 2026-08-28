package orchestrator

import (
	"context"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const localOrphanThreshold = 60 * time.Second

func ReconcileOrphanedLocalRuns(ctx context.Context, st *store.Store, threshold time.Duration) (int, error) {
	if threshold <= 0 {
		threshold = localOrphanThreshold
	}
	return store.Maintenance.ReconcileOrphanedLocalRuns(st, ctx, threshold)
}
