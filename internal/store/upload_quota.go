package store

import (
	"context"
	"math"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

type uploadQuotaDimension struct {
	id, quota, period, limitName, unit string
	amount, ceiling                    int64
}

func uploadQuotaDimensions(id, run, period string, bytes, objects int64) []uploadQuotaDimension {
	return []uploadQuotaDimension{
		{id: id, quota: "storage", period: period, amount: bytes, limitName: StorageLimitBytesPerMonth, unit: "bytes"},
		{id: "run-bytes:" + id, quota: "storage.run.bytes:" + run, amount: bytes, limitName: StorageLimitBytesPerRun, unit: "bytes"},
		{id: "run-objects:" + id, quota: "storage.run.objects:" + run, amount: objects, limitName: StorageLimitObjectsPerRun, unit: "objects"},
	}
}

func reserveBlobUploadQuotaTx(ctx context.Context, tx *storeTx, team Team, id, run string, size, objects int64, expires time.Time) error {
	policy, err := storageQuotaForTeamTx(ctx, tx, team)
	if err != nil {
		return err
	}
	clockTime1, clockErr1 := tx.now(ctx)
	if clockErr1 != nil {
		return clockErr1
	}
	dimensions := uploadQuotaDimensions(blobUploadQuotaID(id), run, clockTime1.Format("2006-01"), size, objects)
	dimensions[0].ceiling, dimensions[1].ceiling = policy.MaxBytesPerMonth, policy.MaxBytesPerRun
	dimensions[2].ceiling = policy.MaxObjectsPerRun
	for _, dimension := range dimensions {
		if err := reserveUploadQuotaDimensionTx(ctx, tx, team, dimension, expires); err != nil {
			return err
		}
	}
	return nil
}

// safety: dimensions always lock monthly bytes before run bytes and objects.
// Durable holds keep concurrent transfers from spending the same run capacity.
func reserveUploadQuotaDimensionTx(ctx context.Context, tx *storeTx, team Team, d uploadQuotaDimension, expires time.Time) error {
	period, err := applyStorageQuotaDimensionTx(ctx, tx, team, d, true)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO quota_reservations(team,id,quota,period,amount,expires_at,state) VALUES (?,?,?,?,?,?,'held')`, team, d.id, d.quota, period, d.amount, expires.UnixNano())
	return err
}

func applyStorageQuotaDimensionTx(ctx context.Context, tx *storeTx, team Team, d uploadQuotaDimension, reserve bool) (string, error) {
	clockTime2, clockErr2 := tx.now(ctx)
	if clockErr2 != nil {
		return "", clockErr2
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO quota_admission(team,quota,period,updated_at) VALUES (?,?,?,?) ON CONFLICT(team,quota) DO NOTHING`, team, d.quota, d.period, clockTime2.UnixNano()); err != nil {
		return "", err
	}
	var prior string
	var used, reserved int64
	if err := tx.QueryRowContext(ctx, "SELECT period,used FROM quota_admission WHERE team=? AND quota=?"+tx.forUpdate(), team, d.quota).Scan(&prior, &used); err != nil {
		return "", err
	}
	if d.quota == "storage" {
		clockTime3, clockErr3 := tx.now(ctx)
		if clockErr3 != nil {
			return "", clockErr3
		}
		d.period = clockTime3.Format("2006-01")
	}
	if prior != d.period {
		used = 0
	}
	clockTime4, clockErr4 := tx.now(ctx)
	if clockErr4 != nil {
		return "", clockErr4
	}
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(SUM(amount),0) FROM quota_reservations WHERE team=? AND quota=? AND period=? AND state='held' AND expires_at>?", team, d.quota, d.period, clockTime4.UnixNano()).Scan(&reserved); err != nil {
		return "", err
	}
	if used < 0 || reserved < 0 || used > math.MaxInt64-reserved || d.amount > math.MaxInt64-used-reserved {
		return "", authorityError(apierr.Conflict, "storage quota overflow")
	}
	if d.amount > 0 && d.ceiling > 0 && d.amount > d.ceiling-used-reserved {
		return "", &StorageQuotaError{Principal: string(team), Limit: d.limitName, Unit: d.unit, Used: used + reserved, Allowed: d.ceiling, Requested: d.amount}
	}
	clockTime5, clockErr5 := tx.now(ctx)
	if clockErr5 != nil {
		return "", clockErr5
	}
	if _, err := tx.ExecContext(ctx, "UPDATE quota_reservations SET state='released' WHERE team=? AND quota=? AND state='held' AND expires_at<=?", team, d.quota, clockTime5.UnixNano()); err != nil {
		return "", err
	}
	if reserve {
		reserved += d.amount
	} else {
		used += d.amount
	}
	clockTime6, clockErr6 := tx.now(ctx)
	if clockErr6 != nil {
		return "", clockErr6
	}
	_, err := tx.ExecContext(ctx, "UPDATE quota_admission SET period=?,used=?,reserved=?,updated_at=? WHERE team=? AND quota=?", d.period, used, reserved, clockTime6.UnixNano(), team, d.quota)
	return d.period, err
}
