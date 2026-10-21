package store

import (
	"context"
	"errors"
	"math"
)

// StorageStanding reads this team's tier, retained inventory and live upload holds in one snapshot.
func (t *Tenant) StorageStanding(ctx context.Context) (_ StorageStanding, err error) {
	tx, err := t.s.beginSnapshotReadTx(ctx)
	if err != nil {
		return StorageStanding{}, err
	}
	defer rollbackUnlessDone(tx, &err)
	out, err := storageStandingTx(ctx, tx, t.team)
	if err != nil {
		return StorageStanding{}, err
	}
	inventory, err := retainedStorageSampleTx(ctx, tx, t.team)
	if err != nil {
		return StorageStanding{}, err
	}
	out.BlobBytes, out.LogBytes = inventory.blobs, inventory.logs
	clockTime1, clockErr1 := tx.now(ctx)
	if clockErr1 != nil {
		return StorageStanding{}, clockErr1
	}
	now := clockTime1.UnixNano()
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(q.amount),0)
FROM uploads u JOIN quota_reservations q ON q.team=u.team AND q.id='upload:' || u.id
WHERE u.team=? AND u.store='blob' AND u.committed_at=0 AND u.expires_at>?
AND q.quota='storage' AND q.state='held' AND q.expires_at>?`, t.team, now, now).Scan(&out.ReservedBlobBytes)
	if err != nil {
		return StorageStanding{}, err
	}
	if out.ReservedBlobBytes < 0 || out.BlobBytes > math.MaxInt64-out.ReservedBlobBytes {
		return StorageStanding{}, errors.New("storage: invalid retained upload inventory")
	}
	if err := tx.Commit(); err != nil {
		return StorageStanding{}, err
	}
	return out, nil
}

func retainedEventBytesTx(ctx context.Context, tx *storeTx, team Team) (int64, error) {
	var bytes, invalid int64
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(event_bytes),0),COALESCE(SUM(CASE WHEN event_bytes<0 THEN 1 ELSE 0 END),0) FROM runs WHERE team=?`, team).Scan(&bytes, &invalid)
	if err != nil {
		return 0, err
	}
	if bytes < 0 || invalid != 0 {
		return 0, errors.New("storage: invalid retained event inventory")
	}
	return bytes, nil
}
