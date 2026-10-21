package store

import (
	"context"
	"time"
)

// AcquireCapacity admits capacity under the live owner and captured work binding.
func (t *Tenant) AcquireCapacity(ctx context.Context, req acquireSlotRequest, binding CapacityBinding, actor Actor) (AcquireSlotResponse, error) {
	if req.RunID != binding.RunID || req.NodeID != binding.NodeID || req.CacheKeyHash != "" {
		return AcquireSlotResponse{}, holderRefusal()
	}
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return AcquireSlotResponse{}, err
	}
	defer rollbackOrLog(tx)
	fence := capacityFence{binding: binding, actor: actor}
	if err := prepareCapacityAcquisitionTx(ctx, tx, t.team, []string{req.Key}, fence); err != nil {
		return AcquireSlotResponse{}, err
	}
	result, err := t.acquireConcurrencySlotTx(ctx, tx, req, &fence, time.Time{})
	if err != nil {
		return AcquireSlotResponse{}, err
	}
	return result, txCommitChecked(ctx, tx, tx.now(ctx).UnixNano(), concurrencyKey{t.team, req.Key})
}

// ResolveCapacityWaiter adjudicates promotion and sealed queue expiry under the captured owner fence.
func (t *Tenant) ResolveCapacityWaiter(ctx context.Context, key string, binding CapacityBinding, actor Actor) (waiterResolution, error) {
	return t.resolveWaiter(ctx, key, binding.RunID, binding.NodeID, "", "", &capacityFence{binding: binding, actor: actor})
}
