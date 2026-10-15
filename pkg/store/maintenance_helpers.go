package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

func assertSingletonLeaseTx(ctx context.Context, tx *storeTx, lease SingletonLease, name string, now time.Time) error {
	if lease.Name != name || lease.Holder == "" || lease.Generation < 1 {
		return ErrLockHeld
	}
	var holder string
	var generation, until int64
	err := tx.QueryRowContext(ctx, `SELECT holder,generation,until FROM leases WHERE name = ?`+tx.forUpdate(), name).Scan(&holder, &generation, &until)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrLockHeld
	}
	if err != nil {
		return err
	}
	// safety: a lock wait can cross expiry, so check the clock after obtaining the fence.
	if current := tx.now(); current.After(now) {
		now = current
	}
	if holder != lease.Holder || generation != lease.Generation || until <= now.UnixNano() {
		return ErrLockHeld
	}
	return nil
}
