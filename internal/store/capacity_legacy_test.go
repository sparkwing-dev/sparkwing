package store

import (
	"context"
	"fmt"
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
	return result, txCommitChecked(ctx, tx, tx.now().UnixNano(), concurrencyKey{t.team, req.Key})
}

// ResolveCapacityWaiter adjudicates promotion and sealed queue expiry under the captured owner fence.
func (t *Tenant) ResolveCapacityWaiter(ctx context.Context, key string, binding CapacityBinding, actor Actor) (waiterResolution, error) {
	return t.resolveWaiter(ctx, key, binding.RunID, binding.NodeID, "", "", &capacityFence{binding: binding, actor: actor})
}

// AdoptCapacity checks continuity under the entry lock; expired holders and waiters are removed before a fresh request.
func (t *Tenant) AdoptCapacity(ctx context.Context, key, id string, binding CapacityBinding, actor Actor) (adoptionKind, error) {
	if key == "" || id == "" || binding.RunID == "" || binding.Generation < 0 {
		return "", fmt.Errorf("invalid capacity binding")
	}
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return "", err
	}
	defer rollbackOrLog(tx)
	fence := capacityFence{binding: binding, actor: actor}
	if err := checkCapacityFenceTx(ctx, tx, t.team, key, fence); err != nil {
		return "", err
	}
	if err := lockCapacityEntryTx(ctx, tx, t.team, key); err != nil {
		return "", err
	}
	if err := checkCapacityFenceTx(ctx, tx, t.team, key, fence); err != nil {
		return "", err
	}
	adopted, err := adoptCapacityTx(ctx, tx, t.team, key, id, binding)
	if err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return adopted, nil
}
