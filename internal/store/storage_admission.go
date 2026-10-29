package store

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// storageGrowth is one write that adds bytes or objects to a team's stored data.
type storageGrowth struct {
	// reservation names an upload ticket's quota hold; empty charges the growth now and records it as usage.
	reservation string
	// expires ends a reservation's hold.
	expires time.Time
	// run attributes the growth to a run's per-run limits; empty charges only the monthly limit.
	run            string
	bytes, objects int64
	// lifecycle marks a run's own state record, which is counted but never refused so a run can always finish.
	lifecycle bool
}

// admitStorageGrowthTx is the one storage rule. Every write that grows a team's stored data calls it in the
// write's transaction: run events, blob and source upload reservations and tagged log segments. It refuses a held
// team, growth past the free allowance on an empty balance while storage is priced, and growth past the team's
// tier quota, then counts the growth against the quota so a refused write is never recorded.
func admitStorageGrowthTx(ctx context.Context, tx *storeTx, team Team, g storageGrowth) error {
	if g.bytes < 0 || g.objects < 0 {
		return fmt.Errorf("%w: negative storage growth", ErrInvalidInput)
	}
	if !g.lifecycle && g.bytes > 0 {
		if err := refuseUnpaidStorageGrowthTx(ctx, tx, team, g.bytes); err != nil {
			return err
		}
	}
	policy, err := storageQuotaForTeamTx(ctx, tx, team)
	if err != nil {
		return err
	}
	dimensions := uploadQuotaDimensions(g.reservation, g.run, StorageMonth(tx.now(ctx)), g.bytes, g.objects)
	if !g.lifecycle {
		dimensions[0].ceiling, dimensions[1].ceiling, dimensions[2].ceiling = policy.MaxBytesPerMonth, policy.MaxBytesPerRun, policy.MaxObjectsPerRun
	}
	if g.run == "" {
		dimensions = dimensions[:1]
	}
	if g.reservation != "" {
		for _, d := range dimensions {
			if err := reserveUploadQuotaDimensionTx(ctx, tx, team, d, g.expires); err != nil {
				return err
			}
		}
		return nil
	}
	var period string
	for i, d := range dimensions {
		charged, err := applyStorageQuotaDimensionTx(ctx, tx, team, d, false)
		if err != nil {
			return err
		}
		if i == 0 {
			period = charged
		}
	}
	return recordStorageUsageTx(ctx, tx, team, g.run, period, g.bytes, g.objects)
}

// safety: storage past the free allowance is billed daily, so a team that cannot pay may not grow it; a held team,
// revoked standing included, may not grow it at all.
func refuseUnpaidStorageGrowthTx(ctx context.Context, tx *storeTx, team Team, bytes int64) error {
	if !tx.store.holdsFreeAllowance(team) {
		return nil
	}
	if cause, err := teamHoldTx(ctx, tx, team); err != nil {
		return err
	} else if cause != "" {
		return &InsufficientCreditsError{Hold: cause}
	}
	rate, err := storageRateTx(ctx, tx)
	if err != nil || rate == 0 {
		return err
	}
	balance, err := creditBalanceTx(ctx, tx, team)
	if err != nil || balance > 0 {
		return err
	}
	free, err := storageFreeAllowanceTx(ctx, tx)
	if err != nil {
		return err
	}
	retained, err := retainedStorageBytesTx(ctx, tx, team)
	if err != nil {
		return err
	}
	if retained <= free-bytes {
		return nil
	}
	return fmt.Errorf(
		"%w: team %s stores %d bytes and cannot add %d more past its free %d on a balance of %s credits; "+
			"add credits, and retention releases what is already stored",
		ErrInsufficientCredits, team, retained, bytes, free, FormatCredits(balance))
}

// retainedStorageBytesTx is what a team stores now: run events, blobs, every signed log slot until its object is
// deleted, and live upload holds.
func retainedStorageBytesTx(ctx context.Context, tx *storeTx, team Team) (int64, error) {
	events, err := retainedEventBytesTx(ctx, tx, team)
	if err != nil {
		return 0, err
	}
	inventory, err := retainedStorageSampleTx(ctx, tx, team)
	if err != nil {
		return 0, err
	}
	var held int64
	now := tx.now(ctx).UnixNano()
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(amount),0) FROM quota_reservations WHERE team=? AND quota='storage' AND state='held' AND expires_at>?`, team, now).Scan(&held); err != nil {
		return 0, err
	}
	total := events + inventory.blobs + inventory.logs + held
	if held < 0 || total < events {
		return 0, errors.New("storage: invalid retained inventory")
	}
	return total, nil
}
