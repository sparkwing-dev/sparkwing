package orchestrator

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type noopConcurrency struct {
	warnedKeys sync.Map
}

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

func (*noopConcurrency) State(_ context.Context, key string) (*store.ConcurrencyState, error) {
	return &store.ConcurrencyState{Key: key, Capacity: 1, EffectiveCapacity: 1}, nil
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

func isCoordinatingPolicy(p string) bool {
	return p != ""
}
