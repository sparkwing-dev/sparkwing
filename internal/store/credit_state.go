package store

import (
	"context"
	"database/sql"
	"time"
)

// CreditBalanceMicro returns the tenant's grants minus its charges.
func (t *Tenant) CreditBalanceMicro(ctx context.Context) (int64, error) {
	return creditBalanceTx(ctx, storeRowQuerier{t.s}, t.team)
}

// CreditState reads tenant finances and deployment prices from one snapshot.
func (t *Tenant) CreditState(ctx context.Context, window time.Duration) (CreditState, error) {
	out := CreditState{BurnWindow: window}
	tx, err := t.s.beginSnapshotReadTx(ctx)
	if err != nil {
		return out, err
	}
	defer rollbackOrLog(tx)
	var granted, reversed, charged sql.NullInt64
	if err := tx.QueryRowContext(ctx, creditGrantSplitSQL,
		t.team, CreditGrantReversal, t.team, CreditGrantReversal,
		t.team).Scan(&granted, &reversed, &charged); err != nil {
		return out, err
	}
	out.GrantedMicro, out.ReversedMicro, out.ChargedMicro = granted.Int64, reversed.Int64, charged.Int64
	out.BalanceMicro = out.GrantedMicro - out.ReversedMicro - out.ChargedMicro
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(amount_micro),0) FROM credit_charges WHERE team=? AND kind=?`,
		t.team, CreditChargeStorage).Scan(&out.StorageChargedMicro); err != nil {
		return out, err
	}
	if window > 0 {
		if err := tx.QueryRowContext(ctx,
			`SELECT COALESCE(SUM(amount_micro),0) FROM credit_charges WHERE team=? AND charged_at>=?`,
			t.team, tx.now(ctx).Add(-window).UnixNano()).Scan(&out.BurnMicro); err != nil {
			return out, err
		}
	}
	settings, err := creditSettingsTx(ctx, tx)
	if err != nil {
		return out, err
	}
	out.RateMicroPerSecond, out.RateTable, out.RateTableSet = settings.RateMicroPerSecond, settings.RateTable, settings.RateTableSet
	out.WarmCPUClassCores = settings.WarmCPUClassCores
	out.StorageRateMicroPerGBDay, out.StorageFreeAllowanceBytes = settings.StorageRateMicroPerGBDay, settings.StorageFreeAllowanceBytes
	return out, tx.Commit()
}
