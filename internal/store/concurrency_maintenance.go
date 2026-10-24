package store

import (
	"context"
	"errors"
)

// ConcurrencyMaintenanceResult counts only committed maintenance work.
type ConcurrencyMaintenanceResult struct {
	StaleHolders []ConcurrencyHolder `json:"stale_holders,omitempty"`
	StaleWaiters []ConcurrencyWaiter `json:"stale_waiters,omitempty"`
}

// MaintainConcurrency removes dead reservations under the elected reaper fence.
func (o *Operator) MaintainConcurrency(ctx context.Context, lease SingletonLease) (ConcurrencyMaintenanceResult, error) {
	return inTxResult(ctx, o.s, func(tx *storeTx) (ConcurrencyMaintenanceResult, error) {
		if err := assertSingletonLeaseTx(ctx, tx, lease, "controller.reaper"); err != nil {
			return ConcurrencyMaintenanceResult{}, err
		}
		keys, err := tx.operator.lockMaintenanceCapacityTx(ctx, tx)
		if err != nil {
			return ConcurrencyMaintenanceResult{}, err
		}
		var result ConcurrencyMaintenanceResult
		for _, key := range keys {
			waiters, err := reapStaleConcurrencyWaitersTx(ctx, tx, key.team, key.key)
			if err != nil {
				return ConcurrencyMaintenanceResult{}, err
			}
			result.StaleWaiters = append(result.StaleWaiters, waiters...)
			holders, err := reapStaleConcurrencyHoldersTx(ctx, tx, key.team, key.key)
			if err != nil {
				return ConcurrencyMaintenanceResult{}, err
			}
			result.StaleHolders = append(result.StaleHolders, holders...)
		}
		for _, key := range keys {
			if err := txCheckConcurrencyInvariants(ctx, tx, key.team, key.key, tx.now(ctx).UnixNano()); err != nil {
				return ConcurrencyMaintenanceResult{}, err
			}
		}
		if err := assertSingletonLeaseTx(ctx, tx, lease, "controller.reaper"); err != nil {
			return ConcurrencyMaintenanceResult{}, err
		}
		return result, nil
	})
}

func (op *Operator) lockMaintenanceCapacityTx(ctx context.Context, tx *storeTx) ([]concurrencyKey, error) {
	rows, err := tx.queryContext(ctx, `SELECT team,key FROM concurrency_entries e
WHERE EXISTS (SELECT 1 FROM concurrency_waiters w WHERE w.team=e.team AND w.key=e.key)
OR EXISTS (SELECT 1 FROM concurrency_holders h WHERE h.team=e.team AND h.key=e.key)
ORDER BY team,key`)
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
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	var locked []concurrencyKey
	teams := map[Team]bool{}
	for _, key := range keys {
		if _, seen := teams[key.team]; !seen {
			err := lockAdmissionTeamTx(ctx, tx, key.team)
			if err != nil && !errors.Is(err, errCapacityAdmissionBusy) {
				return nil, err
			}
			teams[key.team] = err == nil
		}
	}
	for _, key := range keys {
		if !teams[key.team] {
			continue
		}
		if err := lockCapacityEntryTx(ctx, tx, key.team, key.key); err != nil {
			return nil, err
		}
		locked = append(locked, key)
	}
	return locked, nil
}
