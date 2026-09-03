package orchestrator

import (
	"context"
	"errors"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const localOrphanThreshold = 60 * time.Second

// ErrOrphanThresholdRequired reports an orphan sweep asked for with no
// idle age. The store reads a zero threshold as "every running run is
// orphaned", so a caller that cannot name one is refused rather than
// served a default it did not choose.
var ErrOrphanThresholdRequired = errors.New("orphan reconcile: threshold must be > 0")

func ReconcileOrphanedLocalRuns(ctx context.Context, st *store.Store, threshold time.Duration) (int, error) {
	if threshold <= 0 {
		threshold = localOrphanThreshold
	}
	return store.Maintenance.ReconcileOrphanedLocalRuns(st, ctx, threshold)
}
