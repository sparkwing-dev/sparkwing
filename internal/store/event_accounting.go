package store

import "context"

// AppendEventCharged commits an event and its tenant usage under the controller's trusted replica identity.
func (t *Tenant) AppendEventCharged(ctx context.Context, replica, runID, nodeID, kind string, payload []byte) (int64, error) {
	if replica == "" {
		return 0, ErrInvalidInput
	}
	if err := ValidateEventKind(kind); err != nil {
		return 0, err
	}
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return 0, err
	}
	defer rollbackOrLog(tx)
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
	if err := chargeEventStorageTx(ctx, tx, t.team, replica, runID, size); err != nil {
		return 0, err
	}
	clockTime1, clockErr1 := tx.now(ctx)
	if clockErr1 != nil {
		return 0, clockErr1
	}
	seq, err := appendEventTx(ctx, tx, t.team, runID, nodeID, kind, payload, clockTime1)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return seq, nil
}

func admitEventStorageTx(ctx context.Context, tx *storeTx, team Team, size int64) error {
	if tx.store.creditMeteringOff.Load() {
		return nil
	}
	if _, err := lockLedgerTx(ctx, tx, team); err != nil {
		return err
	}
	if holdsFreeAllowance(team) {
		clockTime2, clockErr2 := tx.now(ctx)
		if clockErr2 != nil {
			return clockErr2
		}
		if err := admitFreeEventsTx(ctx, tx, team, string(team), size, clockTime2); err != nil {
			return err
		}
	}
	rate, err := creditSettingTx(ctx, tx, metaKeyStorageRateMicroPerGBDay, 0)
	if err != nil || rate == 0 {
		return err
	}
	return refuseStorageGrowthOnEmptyBalanceTx(ctx, tx, team, size)
}

func chargeEventStorageTx(ctx context.Context, tx *storeTx, team Team, replica, run string, size int64) error {
	policy, err := storageQuotaForTeamTx(ctx, tx, team)
	if err != nil {
		return err
	}
	clockTime3, clockErr3 := tx.now(ctx)
	if clockErr3 != nil {
		return clockErr3
	}
	dimensions := uploadQuotaDimensions("", run, StorageMonth(clockTime3), size, 0)[:2]
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
	return recordStorageUsageTx(ctx, tx, team, run, replica, period, size, 0)
}
