package orchestrator

import (
	"context"
	"log/slog"
	"sync"
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
type noopConcurrency struct {
	warnedKeys sync.Map // map[string]struct{} - emit each (key, policy) warning once
}

// NoopConcurrency returns a ConcurrencyBackend that always grants
// every slot acquire. Exposed for the S3Backends bundle and for
// tests that want to bypass coordination without standing up a
// store.
func NoopConcurrency() ConcurrencyBackend { return &noopConcurrency{} }

func (n *noopConcurrency) AcquireSlot(_ context.Context, req store.AcquireSlotRequest) (store.AcquireSlotResponse, error) {
	if isCoordinatingPolicy(req.Policy) {
		warnKey := req.Key + "|" + req.Policy
		if _, loaded := n.warnedKeys.LoadOrStore(warnKey, struct{}{}); !loaded {
			slog.Warn("cache concurrency policy is a no-op in this state backend; "+
				"cross-runner reservation requires Mode 3 (postgres) or Mode 4 (controller). "+
				"Multiple runners may run this node concurrently.",
				"key", req.Key,
				"policy", req.Policy,
				"capacity", req.Capacity,
				"run_id", req.RunID,
				"node_id", req.NodeID,
			)
		}
	}
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

func (*noopConcurrency) HeartbeatSlot(_ context.Context, _, _ string, lease time.Duration) (time.Time, bool, error) {
	if lease <= 0 {
		lease = store.DefaultConcurrencyLease
	}
	return time.Now().Add(lease), false, nil
}

func (*noopConcurrency) ObserveSlot(_ context.Context, key, holderID string) (*store.ConcurrencyHolder, error) {
	runID, nodeID := splitHolderID(holderID)
	return &store.ConcurrencyHolder{
		Key:            key,
		HolderID:       holderID,
		RunID:          runID,
		NodeID:         nodeID,
		LeaseExpiresAt: time.Now().Add(store.DefaultConcurrencyLease),
	}, nil
}

func (*noopConcurrency) ReleaseSlot(_ context.Context, _, _, _, _, _ string, _ time.Duration) error {
	return nil
}

func (*noopConcurrency) ResolveWaiter(_ context.Context, _, _, _, _, _, _ string, _ bool) (store.WaiterResolution, error) {
	return store.WaiterResolution{Status: store.WaiterLeaderFinished}, nil
}

func (*noopConcurrency) ForceReleaseSuperseded(_ context.Context, _ string) ([]store.ConcurrencyHolder, error) {
	return nil, nil
}

func (*noopConcurrency) CancelWaiter(_ context.Context, _, _, _ string) (bool, error) {
	return false, nil
}

// isCoordinatingPolicy reports whether the requested OnLimit policy
// implies a coordination contract this backend cannot honor. Empty
// policy means "no Cache() declared, no coordination expected";
// everything else expects a leader/waiter/reject decision the noop
// backend silently skips.
func isCoordinatingPolicy(p string) bool {
	return p != ""
}
