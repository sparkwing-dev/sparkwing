package orchestrator

import (
	"context"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// noopConcurrency satisfies ConcurrencyBackend with no cross-runner
// coordination. AcquireSlot always returns Granted; ReleaseSlot,
// HeartbeatSlot, ResolveWaiter, and ForceReleaseSuperseded are
// no-ops.
//
// This is the Mode 2 ("S3-only shared") backend: many runners can
// race on the same cache key and all of them will compute, because
// there is no shared CAS surface to elect a leader. Content-addressed
// reuse via ArtifactStore still works -- two runners producing the
// same key write identical bytes -- but the leader/waiter coalesce
// path that Mode 3 (Postgres) and Mode 4 (controller) provide is
// deliberately absent. See DESIGN-shared-state.md for the tradeoff.
type noopConcurrency struct{}

// NoopConcurrency returns a ConcurrencyBackend that always grants
// every slot acquire. Exposed for the S3Backends bundle and for
// tests that want to bypass coordination without standing up a
// store.
func NoopConcurrency() ConcurrencyBackend { return noopConcurrency{} }

func (noopConcurrency) AcquireSlot(_ context.Context, req store.AcquireSlotRequest) (store.AcquireSlotResponse, error) {
	holderID := req.HolderID
	if holderID == "" {
		holderID = req.RunID + "/" + req.NodeID
	}
	lease := req.Lease
	if lease <= 0 {
		lease = store.DefaultConcurrencyLease
	}
	return store.AcquireSlotResponse{
		Kind:           store.AcquireGranted,
		HolderID:       holderID,
		LeaseExpiresAt: time.Now().Add(lease),
	}, nil
}

func (noopConcurrency) HeartbeatSlot(_ context.Context, _, _ string, lease time.Duration) (time.Time, bool, error) {
	if lease <= 0 {
		lease = store.DefaultConcurrencyLease
	}
	return time.Now().Add(lease), false, nil
}

func (noopConcurrency) ReleaseSlot(_ context.Context, _, _, _, _, _ string, _ time.Duration) error {
	return nil
}

func (noopConcurrency) ResolveWaiter(_ context.Context, _, _, _, _, _, _ string) (store.WaiterResolution, error) {
	return store.WaiterResolution{Status: store.WaiterLeaderFinished}, nil
}

func (noopConcurrency) ForceReleaseSuperseded(_ context.Context, _ string) ([]store.ConcurrencyHolder, error) {
	return nil, nil
}
