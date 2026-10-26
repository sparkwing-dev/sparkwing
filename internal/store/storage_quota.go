package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

const (
	storageQuotaColumns = "team,tier,max_bytes_per_run,max_bytes_per_month,max_objects_per_run,storage_allowance_bytes"
	storageUsageTotals  = "COALESCE(SUM(bytes),0),COALESCE(SUM(objects),0),COALESCE(SUM(CASE WHEN bytes<0 OR objects<0 THEN 1 ELSE 0 END),0)"
)

func validateStorageQuota(q StorageQuota) error {
	if q.Tier != "" {
		if _, ok := TierQuota(q.Tier); !ok {
			return fmt.Errorf("storage: unknown tier %q", q.Tier)
		}
	}
	if q.MaxBytesPerRun < 0 || q.MaxBytesPerMonth < 0 || q.MaxObjectsPerRun < 0 || q.AllowanceBytes < 0 {
		return errors.New("storage: quota limits must not be negative")
	}
	return nil
}

// SetStorageQuota replaces this team's write limits and preserves its retained allowance.
// A named tier with zero write limits selects that tier's limits.
func (t *Tenant) SetStorageQuota(ctx context.Context, q StorageQuota) error {
	return t.s.inTx(ctx, func(tx *storeTx) error { return setStorageQuotaTx(ctx, tx, t.team, q) })
}

func setStorageQuotaTx(ctx context.Context, tx *storeTx, team Team, q StorageQuota) error {
	if q.Team != "" && q.Team != team {
		return fmt.Errorf("%w: quota belongs to another team", ErrInvalidInput)
	}
	if err := validateStorageQuota(q); err != nil {
		return err
	}
	if q.Tier != "" && q.unlimited() {
		q, _ = TierQuota(q.Tier)
	}
	_, err := tx.execContext(ctx, `INSERT INTO storage_quotas(team,tier,max_bytes_per_run,max_bytes_per_month,max_objects_per_run,limits_set,updated_at)
VALUES (?,?,?,?,?,1,?) ON CONFLICT(team) DO UPDATE SET tier=excluded.tier,max_bytes_per_run=excluded.max_bytes_per_run,max_bytes_per_month=excluded.max_bytes_per_month,max_objects_per_run=excluded.max_objects_per_run,limits_set=1,updated_at=excluded.updated_at`,
		team, q.Tier, q.MaxBytesPerRun, q.MaxBytesPerMonth, q.MaxObjectsPerRun, tx.now(ctx).UnixNano())
	return err
}

// StorageQuota returns this team's explicit quota, else the deployment's default tier, else on Sparkwing Cloud
// the team's automatic tier. Without any of them the team's writes are unlimited.
func (t *Tenant) StorageQuota(ctx context.Context) (StorageQuota, error) {
	var q StorageQuota
	err := t.s.inTx(ctx, func(tx *storeTx) error {
		var err error
		q, err = storageQuotaForTeamTx(ctx, tx, t.team)
		return err
	})
	return q, err
}

func storageQuotaForTeamTx(ctx context.Context, tx *storeTx, team Team) (StorageQuota, error) {
	var limitsSet bool
	row := tx.QueryRowContext(ctx, "SELECT "+storageQuotaColumns+",limits_set FROM storage_quotas WHERE team=?", team)
	q, err := scanStorageQuota(row, &limitsSet)
	if err == nil && limitsSet {
		return q, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return StorageQuota{}, err
	}
	tier, err := storageDefaultTierTx(ctx, tx)
	if err != nil {
		return StorageQuota{}, err
	}
	if tier == "" && tx.store.cloudStorage.Load() && holdsFreeAllowance(team) {
		// safety: a team keeps the paid tier once it has paid, even after a refund or while its balance is empty.
		var paid bool
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM credit_grants WHERE team=? AND kind=?)", team, CreditGrantPaid).Scan(&paid); err != nil {
			return StorageQuota{}, err
		}
		tier = storageTierFree
		if paid {
			tier = storageTierPaid
		}
	}
	allowance := q.AllowanceBytes
	q = StorageQuota{}
	if tier != "" {
		q, _ = TierQuota(tier)
	}
	q.Team, q.AllowanceBytes = team, allowance
	return q, nil
}

func scanStorageQuota(row rowScanner, extra ...any) (StorageQuota, error) {
	var q StorageQuota
	if err := row.Scan(append([]any{&q.Team, &q.Tier, &q.MaxBytesPerRun, &q.MaxBytesPerMonth, &q.MaxObjectsPerRun, &q.AllowanceBytes}, extra...)...); err != nil {
		return StorageQuota{}, err
	}
	return q, validateStorageQuota(q)
}

// SetStorageAllowance changes retained bytes without replacing this team's write limits.
// Zero leaves retained bytes uncapped. The returned quota is from the write transaction.
func (t *Tenant) SetStorageAllowance(ctx context.Context, bytes int64) (StorageQuota, error) {
	var q StorageQuota
	err := t.s.inTx(ctx, func(tx *storeTx) error {
		var err error
		q, err = setStorageAllowanceTx(ctx, tx, t.team, bytes)
		return err
	})
	return q, err
}

func setStorageAllowanceTx(ctx context.Context, tx *storeTx, team Team, bytes int64) (StorageQuota, error) {
	if bytes < 0 {
		return StorageQuota{}, errors.New("storage: the allowance must not be negative")
	}
	_, err := tx.execContext(ctx, `INSERT INTO storage_quotas(team,storage_allowance_bytes,limits_set,updated_at) VALUES (?,?,0,?)
ON CONFLICT(team) DO UPDATE SET storage_allowance_bytes=excluded.storage_allowance_bytes,updated_at=excluded.updated_at`, team, bytes, tx.now(ctx).UnixNano())
	if err != nil {
		return StorageQuota{}, err
	}
	return storageQuotaForTeamTx(ctx, tx, team)
}

// ListStorageQuotas returns every explicit team quota in team order.
func (o *Operator) ListStorageQuotas(ctx context.Context) (_ []StorageQuota, err error) {
	rows, err := o.s.query(ctx, "SELECT "+storageQuotaColumns+" FROM storage_quotas WHERE limits_set=1 ORDER BY team")
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	out := []StorageQuota{}
	for rows.Next() {
		q, err := scanStorageQuota(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, q)
	}
	return out, rows.Err()
}

// StorageUsageFor reads this team's usage counters. Run totals span periods;
// month totals select the requested period. Retention never restores these counters.
func (t *Tenant) StorageUsageFor(ctx context.Context, runID, month string) (StorageUsage, error) {
	out := StorageUsage{Team: t.team, RunID: runID, Month: month}
	err := t.s.inTx(ctx, func(tx *storeTx) error {
		var err error
		if runID != "" {
			out.RunBytes, out.RunObjects, err = scanStorageTotals(tx.QueryRowContext(ctx, "SELECT "+storageUsageTotals+" FROM storage_run_usage WHERE team=? AND run_id=?", t.team, runID))
			if err != nil {
				return err
			}
		}
		out.MonthBytes, out.MonthObjects, err = scanStorageTotals(tx.QueryRowContext(ctx, "SELECT "+storageUsageTotals+" FROM storage_month_usage WHERE team=? AND period=?", t.team, month))
		return err
	})
	return out, err
}

func scanStorageTotals(row rowScanner) (bytes, objects int64, err error) {
	var invalid int64
	if err := row.Scan(&bytes, &objects, &invalid); err != nil {
		return 0, 0, err
	}
	if invalid != 0 || bytes < 0 || objects < 0 {
		return 0, 0, errors.New("storage: invalid usage counters")
	}
	return bytes, objects, nil
}

// StorageRetainedBytes reports unique committed blob and log bytes, using the billing inventory.
func (t *Tenant) StorageRetainedBytes(ctx context.Context) (int64, error) {
	var bytes int64
	err := t.s.inTx(ctx, func(tx *storeTx) error {
		inventory, err := retainedStorageSampleTx(ctx, tx, t.team)
		bytes = inventory.blobs + inventory.logs
		return err
	})
	return bytes, err
}

// TopStorageTeams ranks usage for one period, breaking ties by team.
func (o *Operator) TopStorageTeams(ctx context.Context, month string, limit int) (_ []StorageTeamUsage, err error) {
	if limit <= 0 {
		limit = 10
	}
	rows, err := o.s.query(ctx, "SELECT team,"+storageUsageTotals+" FROM storage_month_usage WHERE period=? GROUP BY team ORDER BY SUM(bytes) DESC,team LIMIT ?", month, limit)
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	out := []StorageTeamUsage{}
	for rows.Next() {
		u := StorageTeamUsage{Month: month}
		var invalid int64
		if err := rows.Scan(&u.Team, &u.Bytes, &u.Objects, &invalid); err != nil {
			return nil, err
		}
		if invalid != 0 || u.Bytes < 0 || u.Objects < 0 {
			return nil, errors.New("storage: invalid usage counters")
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
