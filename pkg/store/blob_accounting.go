package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

// BlobAccounting identifies the signing reservation and the committing replica.
// The controller restores these fields from its signed upload ticket, never commit input.
type BlobAccounting struct {
	ReservationID string
	ReplicaID     string
}

type blobQuota struct {
	period, currentPeriod, state string
	amount                       int64
}

func lockBlobQuotaTx(ctx context.Context, tx *storeTx, team Team, accounting BlobAccounting, size int64) (blobQuota, error) {
	var q blobQuota
	if accounting.ReservationID == "" || accounting.ReplicaID == "" {
		return q, authorityError(apierr.BadRequest, "upload accounting is required")
	}
	err := tx.QueryRowContext(ctx, "SELECT period FROM quota_admission WHERE team=? AND quota='storage'"+tx.forUpdate(), team).Scan(&q.currentPeriod)
	if errors.Is(err, sql.ErrNoRows) {
		return q, ErrNotFound
	}
	if err != nil {
		return q, err
	}
	var expires int64
	err = tx.QueryRowContext(ctx, "SELECT period,state,amount,expires_at FROM quota_reservations WHERE team=? AND id=? AND quota='storage'"+tx.forUpdate(), team, accounting.ReservationID).Scan(&q.period, &q.state, &q.amount, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return q, ErrNotFound
	}
	if err != nil {
		return q, err
	}
	if q.amount < size {
		return q, authorityError(apierr.Conflict, "upload exceeds its reservation")
	}
	if q.state == "released" || (q.state == "held" && expires <= tx.now().UnixNano()) {
		return q, ErrLockHeld
	}
	return q, nil
}

// safety: the blob insert, receipt, reservation and replica counters share one commit.
// Even deduplicated uploads commit their reservation so an abort cannot be replayed as a PUT.
func settleBlobQuotaTx(ctx context.Context, tx *storeTx, team Team, run string, accounting BlobAccounting, q blobQuota, size, objects int64) error {
	if q.period == q.currentPeriod {
		result, err := tx.ExecContext(ctx, `UPDATE quota_admission SET used=used+?,reserved=reserved-?,updated_at=? WHERE team=? AND quota='storage' AND reserved>=? AND used<=9223372036854775807-?`, size, q.amount, tx.now().UnixNano(), team, q.amount, size)
		if err != nil {
			return err
		}
		if err := requireOneRow(result, "upload quota accounting"); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, "UPDATE quota_reservations SET state='committed',used=? WHERE team=? AND id=? AND state='held'", size, team, accounting.ReservationID); err != nil {
		return err
	}
	if objects == 0 {
		return nil
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO storage_month_usage(team,period,replica_id,bytes,objects,updated_at) VALUES (?,?,?,?,?,?)
ON CONFLICT(team,period,replica_id) DO UPDATE SET bytes=storage_month_usage.bytes+excluded.bytes,objects=storage_month_usage.objects+excluded.objects,updated_at=excluded.updated_at
WHERE storage_month_usage.bytes<=9223372036854775807-excluded.bytes AND storage_month_usage.objects<=9223372036854775807-excluded.objects`, team, q.period, accounting.ReplicaID, size, objects, tx.now().UnixNano())
	if err != nil {
		return err
	}
	if err := requireOneRow(result, "upload monthly usage"); err != nil {
		return err
	}
	result, err = tx.ExecContext(ctx, `INSERT INTO storage_run_usage(team,period,replica_id,run_id,bytes,objects,updated_at) VALUES (?,?,?,?,?,?,?)
ON CONFLICT(team,period,replica_id,run_id) DO UPDATE SET bytes=storage_run_usage.bytes+excluded.bytes,objects=storage_run_usage.objects+excluded.objects,updated_at=excluded.updated_at
WHERE storage_run_usage.bytes<=9223372036854775807-excluded.bytes AND storage_run_usage.objects<=9223372036854775807-excluded.objects`, team, q.period, accounting.ReplicaID, run, size, objects, tx.now().UnixNano())
	if err != nil {
		return err
	}
	return requireOneRow(result, "upload run usage")
}

func requireOneRow(result sql.Result, operation string) error {
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return authorityError(apierr.Conflict, operation+" did not update one row")
	}
	return nil
}
