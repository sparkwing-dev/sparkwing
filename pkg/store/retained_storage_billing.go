package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"
)

// ChargeRetainedStorage bills unique committed blobs and persisted log bytes.
// The maintenance fence and each team's watermarks commit with its ledger debit.
// The initial sample starts billing without charging for earlier storage.
func (o *Operator) ChargeRetainedStorage(ctx context.Context, lease SingletonLease) (StorageChargeSweep, error) {
	var out StorageChargeSweep
	if err := o.s.inTx(ctx, func(tx *storeTx) error {
		return assertSingletonLeaseTx(ctx, tx, lease, "maintenance", tx.now())
	}); err != nil {
		return out, err
	}
	teams, err := o.retainedStorageTeams(ctx)
	if err != nil {
		return out, err
	}
	for _, team := range teams {
		charge, err := o.chargeRetainedTeam(ctx, lease, team)
		if err != nil {
			return out, fmt.Errorf("storage: bill team %s: %w", team, err)
		}
		if charge != nil {
			out.Charges = append(out.Charges, *charge)
			if out.ChargedMicro > math.MaxInt64-charge.AmountMicro {
				return out, errors.New("storage: billed total exceeds report range")
			}
			out.ChargedMicro += charge.AmountMicro
		}
	}
	return out, nil
}

func (o *Operator) retainedStorageTeams(ctx context.Context) (_ []Team, err error) {
	rows, err := o.s.query(ctx, `SELECT t.name FROM teams t
WHERE EXISTS(SELECT 1 FROM blobs b WHERE b.team=t.name)
   OR EXISTS(SELECT 1 FROM log_streams l WHERE l.team=t.name)
   OR EXISTS(SELECT 1 FROM team_storage s WHERE s.team=t.name)
ORDER BY t.name`)
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	var teams []Team
	for rows.Next() {
		var team Team
		if err := rows.Scan(&team); err != nil {
			return nil, err
		}
		teams = append(teams, team)
	}
	return teams, rows.Err()
}

func (o *Operator) chargeRetainedTeam(ctx context.Context, lease SingletonLease, team Team) (_ *CreditCharge, err error) {
	tx, err := o.s.beginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer rollbackUnlessDone(tx, &err)
	if err := assertSingletonLeaseTx(ctx, tx, lease, "maintenance", tx.now()); err != nil {
		return nil, err
	}
	if err := lockCreditLedgerTx(ctx, tx); err != nil {
		return nil, err
	}
	charge, err := chargeRetainedTeamTx(ctx, tx, team)
	if err != nil {
		return nil, err
	}
	if err := assertSingletonLeaseTx(ctx, tx, lease, "maintenance", tx.now()); err != nil {
		return nil, err
	}
	return charge, tx.Commit()
}

func chargeRetainedTeamTx(ctx context.Context, tx *storeTx, team Team) (*CreditCharge, error) {
	rate, err := creditSettingTx(ctx, tx, metaKeyStorageRateMicroPerGBDay, 0)
	if err != nil {
		return nil, err
	}
	if rate == 0 {
		_, err := tx.ExecContext(ctx, "UPDATE team_storage SET storage_charged_through=0 WHERE team=? AND store IN ('cache','logs') AND storage_charged_through<>0", team)
		if err != nil {
			return nil, err
		}
		return nil, nil
	}
	free, err := creditSettingTx(ctx, tx, metaKeyStorageFreeAllowanceBytes, 0)
	if err != nil {
		return nil, err
	}
	through, err := lockRetainedWatermarksTx(ctx, tx, team)
	if err != nil {
		return nil, err
	}
	now := tx.now()
	bytes, oldest, err := retainedStorageSampleTx(ctx, tx, team)
	if err != nil {
		return nil, err
	}
	if through == 0 || bytes == 0 {
		if err := stampRetainedWatermarksTx(ctx, tx, team, now.UnixNano()); err != nil {
			return nil, err
		}
		return nil, nil
	}
	gap := (now.UnixNano() - through) / int64(time.Second)
	if gap <= 0 {
		return nil, nil
	}
	if err := stampRetainedWatermarksTx(ctx, tx, team, through+gap*int64(time.Second)); err != nil {
		return nil, err
	}
	allowance, err := retainedAllowanceTx(ctx, tx, team)
	if err != nil {
		return nil, err
	}
	if allowance > 0 {
		bytes = min(bytes, allowance)
	}
	bytes = max(bytes-free, 0)
	// safety: after an idle gap, fresh bytes cannot be billed before they existed.
	seconds := min(gap, max((now.UnixNano()-oldest)/int64(time.Second), 0))
	amount, err := storageChargeMicro(bytes, rate, seconds)
	if err != nil {
		return nil, err
	}
	if amount == 0 {
		return nil, nil
	}
	return insertStorageChargeTx(ctx, tx, storageCharge{Team: team, Bytes: bytes, Seconds: seconds, AmountMicro: amount, NowNS: now.UnixNano()})
}

// safety: cache and log rows share one watermark because the free allowance applies once to their total.
func lockRetainedWatermarksTx(ctx context.Context, tx *storeTx, team Team) (int64, error) {
	var through int64
	for i, kind := range []StorageKind{StorageCache, StorageLogs} {
		if _, err := tx.ExecContext(ctx, "INSERT INTO team_storage(team,store,updated_at) VALUES (?,?,?) ON CONFLICT(team,store) DO NOTHING", team, kind, tx.now().UnixNano()); err != nil {
			return 0, err
		}
		var stamp int64
		if err := tx.QueryRowContext(ctx, "SELECT storage_charged_through FROM team_storage WHERE team=? AND store=?"+tx.forUpdate(), team, kind).Scan(&stamp); err != nil {
			return 0, err
		}
		if stamp < 0 || (i > 0 && stamp != through) {
			return 0, errors.New("storage: inconsistent billing watermarks")
		}
		through = stamp
	}
	return through, nil
}

func stampRetainedWatermarksTx(ctx context.Context, tx *storeTx, team Team, through int64) error {
	for _, kind := range []StorageKind{StorageCache, StorageLogs} {
		result, err := tx.ExecContext(ctx, "UPDATE team_storage SET storage_charged_through=? WHERE team=? AND store=?", through, team, kind)
		if err != nil {
			return err
		}
		if err := requireOneRow(result, "storage billing watermark"); err != nil {
			return err
		}
	}
	return nil
}

func retainedStorageSampleTx(ctx context.Context, tx *storeTx, team Team) (bytes, oldest int64, err error) {
	rows, err := tx.QueryContext(ctx, "SELECT size,created_at FROM blobs WHERE team=? UNION ALL SELECT stored_bytes,created_at FROM log_streams WHERE team=?", team, team)
	if err != nil {
		return 0, 0, err
	}
	defer closeRowsInto(rows, &err)
	oldest = tx.now().UnixNano()
	for rows.Next() {
		var size, created int64
		if err := rows.Scan(&size, &created); err != nil {
			return 0, 0, err
		}
		if size == 0 {
			continue
		}
		if size < 0 || bytes > math.MaxInt64-size || created <= 0 {
			return 0, 0, errors.New("storage: invalid retained byte inventory")
		}
		bytes += size
		oldest = min(oldest, created)
	}
	return bytes, oldest, rows.Err()
}

func retainedAllowanceTx(ctx context.Context, tx *storeTx, team Team) (int64, error) {
	var allowance int64
	err := tx.QueryRowContext(ctx, "SELECT storage_allowance_bytes FROM storage_quotas WHERE team=?", team).Scan(&allowance)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err == nil && allowance < 0 {
		return 0, errors.New("storage: negative retained allowance")
	}
	return allowance, err
}
