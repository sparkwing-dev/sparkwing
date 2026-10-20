package store

import (
	"context"
	"errors"
	"time"
)

// ConcurrencyMaintenanceResult counts only committed maintenance work.
type ConcurrencyMaintenanceResult struct {
	StaleHolders []ConcurrencyHolder `json:"stale_holders,omitempty"`
	Promoted     int                 `json:"promoted"`
	StaleWaiters []ConcurrencyWaiter `json:"stale_waiters,omitempty"`
}

// ConcurrencyMaintenanceOptions preserves promotion and abandoned-waiter policy.
type ConcurrencyMaintenanceOptions struct {
	Lease        time.Duration
	WaiterMaxAge time.Duration
}

func (o *ConcurrencyMaintenanceOptions) withDefaults() {
	if o.Lease <= 0 {
		o.Lease = DefaultConcurrencyLease
	}
	if o.WaiterMaxAge <= 0 {
		o.WaiterMaxAge = 2 * DefaultConcurrencyLease
	}
}

// MaintainConcurrency sweeps capacity atomically under the elected reaper fence.
func (o *Operator) MaintainConcurrency(ctx context.Context, lease SingletonLease, opts ConcurrencyMaintenanceOptions) (ConcurrencyMaintenanceResult, error) {
	opts.withDefaults()
	tx, err := o.s.beginTx(ctx)
	if err != nil {
		return ConcurrencyMaintenanceResult{}, err
	}
	defer rollbackOrLog(tx)
	if err := assertSingletonLeaseTx(ctx, tx, lease, "controller.reaper", tx.now()); err != nil {
		return ConcurrencyMaintenanceResult{}, err
	}
	keys, err := lockMaintenanceCapacityTx(ctx, tx)
	if err != nil {
		return ConcurrencyMaintenanceResult{}, err
	}
	var result ConcurrencyMaintenanceResult
	for _, key := range keys {
		waiters, err := reapStaleConcurrencyWaitersTx(ctx, tx, key.team, key.key, opts.WaiterMaxAge)
		if err != nil {
			return ConcurrencyMaintenanceResult{}, err
		}
		result.StaleWaiters = append(result.StaleWaiters, waiters...)
		holders, err := reapStaleConcurrencyHoldersTx(ctx, tx, key.team, key.key)
		if err != nil {
			return ConcurrencyMaintenanceResult{}, err
		}
		result.StaleHolders = append(result.StaleHolders, holders...)
		now := tx.now()
		promoted, err := txPromoteWaiters(ctx, tx, key.team, key.key, now.UnixNano(), now.Add(opts.Lease).UnixNano())
		if err != nil {
			return ConcurrencyMaintenanceResult{}, err
		}
		result.Promoted += len(promoted)
	}
	for _, key := range keys {
		if err := txCheckConcurrencyInvariants(ctx, tx, key.team, key.key, tx.now().UnixNano()); err != nil {
			return ConcurrencyMaintenanceResult{}, err
		}
	}
	if err := assertSingletonLeaseTx(ctx, tx, lease, "controller.reaper", tx.now()); err != nil {
		return ConcurrencyMaintenanceResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return ConcurrencyMaintenanceResult{}, err
	}
	return result, nil
}

func lockMaintenanceCapacityTx(ctx context.Context, tx *storeTx) ([]concurrencyKey, error) {
	rows, err := tx.QueryContext(ctx, `SELECT team,key FROM concurrency_entries e
WHERE EXISTS (SELECT 1 FROM concurrency_waiters w WHERE w.team=e.team AND w.key=e.key)
OR EXISTS (SELECT 1 FROM concurrency_holders h WHERE h.team=e.team AND h.key=e.key AND (h.lease_expires_at<=? OR EXISTS (SELECT 1 FROM runs r WHERE r.team=h.team AND r.id=h.run_id AND r.finished_at IS NOT NULL)))
ORDER BY team,key`+tx.forUpdate(), tx.now().UnixNano())
	if err != nil {
		return nil, err
	}
	var keys []concurrencyKey
	for rows.Next() {
		var key concurrencyKey
		if err := rows.Scan(&key.team, &key.key); err != nil {
			return nil, errors.Join(err, rows.Close())
		}
		keys = append(keys, key)
	}
	return keys, errors.Join(rows.Err(), rows.Close())
}
