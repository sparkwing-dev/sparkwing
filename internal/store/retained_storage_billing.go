package store

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"time"
)

// ChargeRetainedStorage bills unique committed blobs and persisted log bytes.
// Each team's watermarks commit with its ledger debit.
// The initial sample starts billing without charging for earlier storage.
func (o *Operator) ChargeRetainedStorage(ctx context.Context) (StorageChargeSweep, error) {
	var out StorageChargeSweep
	var rate, free int64
	if err := o.s.inTx(ctx, func(tx *storeTx) error {
		var err error
		if rate, err = storageRateTx(ctx, tx); err != nil {
			return err
		}
		free, err = storageFreeAllowanceTx(ctx, tx)
		return err
	}); err != nil {
		return out, err
	}
	teams, err := o.retainedStorageTeams(ctx)
	if err != nil {
		return out, err
	}
	err = o.EachTeam(ctx, teams, func(t *Tenant) error {
		charge, err := o.chargeRetainedTeam(ctx, t.team, rate, free)
		if err != nil {
			return err
		}
		if charge != nil {
			out.Charges = append(out.Charges, *charge)
		}
		return nil
	})
	for _, charge := range out.Charges {
		if out.ChargedMicro > math.MaxInt64-charge.AmountMicro {
			return out, errors.Join(err, errors.New("storage: billed total exceeds report range"))
		}
		out.ChargedMicro += charge.AmountMicro
	}
	return out, err
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

func (o *Operator) chargeRetainedTeam(ctx context.Context, team Team, rate, free int64) (*CreditCharge, error) {
	return inTxResult(ctx, o.s, func(tx *storeTx) (*CreditCharge, error) {
		charge, err := chargeRetainedTeamTx(ctx, tx, team, rate, free)
		if err != nil {
			return nil, err
		}

		return charge, nil
	})
}

func chargeRetainedTeamTx(ctx context.Context, tx *storeTx, team Team, rate, free int64) (*CreditCharge, error) {
	if _, err := lockLedgerTx(ctx, tx, team); err != nil {
		return nil, err
	}
	if rate == 0 {
		_, err := tx.execContext(ctx, "UPDATE team_storage SET storage_charged_through=0 WHERE team=? AND store IN ('cache','logs') AND storage_charged_through<>0", team)
		if err != nil {
			return nil, err
		}
		return nil, nil
	}
	through, err := lockRetainedWatermarksTx(ctx, tx, team)
	if err != nil {
		return nil, err
	}
	now := tx.now(ctx)
	inventory, err := retainedStorageSampleTx(ctx, tx, team)
	if err != nil {
		return nil, err
	}
	bytes := inventory.blobs + inventory.logs
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
	// safety: an idle gap bills no longer than the team's oldest stored byte has existed. Mixed-age inventory is
	// billed for the whole interval at its current size, so bytes added just before a pass are charged for time
	// before they existed; normal passes are minutes apart, and an exceptional gap is reconciled by hand.
	seconds := min(gap, max((now.UnixNano()-inventory.oldest)/int64(time.Second), 0))
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
		if _, err := tx.execContext(ctx, "INSERT INTO team_storage(team,store,updated_at) VALUES (?,?,?) ON CONFLICT(team,store) DO NOTHING", team, kind, tx.now(ctx).UnixNano()); err != nil {
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
		result, err := tx.execContext(ctx, "UPDATE team_storage SET storage_charged_through=? WHERE team=? AND store=?", through, team, kind)
		if err != nil {
			return err
		}
		if err := requireOneRow(result, "storage billing watermark"); err != nil {
			return err
		}
	}
	return nil
}

type retainedStorageInventory struct {
	blobs, logs, oldest int64
}

func retainedStorageSampleTx(ctx context.Context, tx *storeTx, team Team) (out retainedStorageInventory, err error) {
	out.oldest = tx.now(ctx).UnixNano()
	rows, err := tx.queryContext(ctx, "SELECT size,created_at,0 FROM blobs WHERE team=? UNION ALL SELECT stored_bytes,created_at,1 FROM log_streams WHERE team=?", team, team)
	if err != nil {
		return out, err
	}
	defer closeRowsInto(rows, &err)
	for rows.Next() {
		var size, created, log int64
		if err := rows.Scan(&size, &created, &log); err != nil {
			return retainedStorageInventory{}, err
		}
		if size == 0 {
			continue
		}
		if size < 0 || out.blobs+out.logs > math.MaxInt64-size || created <= 0 {
			return retainedStorageInventory{}, errors.New("storage: invalid retained byte inventory")
		}
		if log == 1 {
			out.logs += size
		} else {
			out.blobs += size
		}
		out.oldest = min(out.oldest, created)
	}
	return out, rows.Err()
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
