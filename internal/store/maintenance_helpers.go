package store

import (
	"context"
	"database/sql"
	"errors"
)

func assertSingletonLeaseTx(ctx context.Context, tx *storeTx, lease SingletonLease, name string) error {
	if lease.Name != name || lease.Holder == "" || lease.Generation < 1 {
		return ErrLockHeld
	}
	var holder string
	var generation, until int64
	err := tx.QueryRowContext(ctx, `SELECT holder,generation,until FROM leases WHERE name = ?`+tx.forKeyShare(), name).Scan(&holder, &generation, &until)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrLockHeld
	}
	if err != nil {
		return err
	}
	// safety: a lock wait can cross expiry, so check the clock after obtaining the fence.
	now := tx.now(ctx)
	if holder != lease.Holder || generation != lease.Generation || until <= now.UnixNano() {
		return ErrLockHeld
	}
	return nil
}

// ReleaseSingletonLease releases only the caller's current lease generation.
func (o *Operator) ReleaseSingletonLease(ctx context.Context, lease SingletonLease) error {
	if !validSingletonWork(lease.Name) || lease.Holder == "" || lease.Generation <= 0 {
		return ErrLockHeld
	}
	return o.s.inTx(ctx, func(tx *storeTx) error {
		if _, err := tx.ExecContext(ctx, `SELECT holder FROM leases WHERE name=?`+tx.forUpdate(), lease.Name); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `UPDATE leases SET holder='',until=0 WHERE name=? AND holder=? AND generation=?`, lease.Name, lease.Holder, lease.Generation)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return ErrLockHeld
		}
		return nil
	})
}
