package store

import (
	"context"
)

func admitEventStorageTx(ctx context.Context, tx *storeTx, team Team, size int64) error {
	if tx.store.creditMeteringOff.Load() {
		return nil
	}
	if _, err := lockLedgerTx(ctx, tx, team); err != nil {
		return err
	}
	if holdsFreeAllowance(team) {
		if err := admitFreeEventsTx(ctx, tx, team, string(team), size, tx.now(ctx)); err != nil {
			return err
		}
	}
	rate, err := creditSettingTx(ctx, tx, metaKeyStorageRateMicroPerGBDay, 0)
	if err != nil || rate == 0 {
		return err
	}
	return refuseStorageGrowthOnEmptyBalanceTx(ctx, tx, team, size)
}

func chargeEventStorageTx(ctx context.Context, tx *storeTx, team Team, run string, size int64) error {
	policy, err := storageQuotaForTeamTx(ctx, tx, team)
	if err != nil {
		return err
	}
	dimensions := uploadQuotaDimensions("", run, StorageMonth(tx.now(ctx)), size, 0)[:2]
	dimensions[0].ceiling, dimensions[1].ceiling = policy.MaxBytesPerMonth, policy.MaxBytesPerRun
	var period string
	for i, dimension := range dimensions {
		chargedPeriod, err := applyStorageQuotaDimensionTx(ctx, tx, team, dimension, false)
		if err != nil {
			return err
		}
		if i == 0 {
			period = chargedPeriod
		}
	}
	return recordStorageUsageTx(ctx, tx, team, run, period, size, 0)
}

// AppendEventCharged commits an event and its tenant usage together.
func (t *Tenant) AppendEventCharged(ctx context.Context, runID, nodeID, kind string, payload []byte) (int64, error) {
	if err := validateEventKind(kind); err != nil {
		return 0, err
	}
	return inTxResult(ctx, t.s, func(tx *storeTx) (int64, error) {
		if err := lockStateRunTx(ctx, tx, t.team, runID); err != nil {
			return 0, err
		}
		size := eventBytes(kind, payload)
		if err := refuseEventOverLimitsTx(ctx, tx, t.team, runID, size); err != nil {
			return 0, err
		}
		if err := admitEventStorageTx(ctx, tx, t.team, size); err != nil {
			return 0, err
		}
		if err := chargeEventStorageTx(ctx, tx, t.team, runID, size); err != nil {
			return 0, err
		}
		return appendEventTx(ctx, tx, t.team, runID, nodeID, kind, payload, tx.now(ctx))
	})
}
