package store

import (
	"context"
	"fmt"
)

// CreditSettings reads deployment billing settings from one snapshot.
func (o *Operator) CreditSettings(ctx context.Context) (_ CreditSettings, err error) {
	var out CreditSettings
	tx, err := o.s.beginSnapshotReadTx(ctx)
	if err != nil {
		return out, err
	}
	defer rollbackUnlessDone(tx, &err)
	out, err = creditSettingsTx(ctx, tx)
	if err != nil {
		return out, err
	}
	return out, tx.Commit()
}

// SetCreditSettings validates and atomically applies a deployment billing update.
func (o *Operator) SetCreditSettings(ctx context.Context, up CreditSettingsUpdate) (_ CreditSettings, err error) {
	writes, err := creditSettingsWrites(up)
	if err != nil {
		return CreditSettings{}, err
	}
	tx, err := o.s.beginCreditSettingsTx(ctx)
	if err != nil {
		return CreditSettings{}, err
	}
	defer rollbackUnlessDone(tx, &err)
	out, err := writeCreditSettingsTx(ctx, tx, writes)
	if err != nil {
		return CreditSettings{}, err
	}
	return out, tx.Commit()
}

// SetOperatorCreditSettings keeps an installed rate table authoritative during an update.
func (o *Operator) SetOperatorCreditSettings(
	ctx context.Context, up CreditSettingsUpdate,
) (_ CreditSettings, err error) {
	tx, err := o.s.beginCreditSettingsTx(ctx)
	if err != nil {
		return CreditSettings{}, err
	}
	defer rollbackOrLog(tx)
	out, err := setOperatorCreditSettingsTx(ctx, tx, up)
	if err != nil {
		return CreditSettings{}, err
	}
	return out, tx.Commit()
}

// WarmCPUClassCores returns the configured warm runner CPU class.
func (o *Operator) WarmCPUClassCores(ctx context.Context) (int64, error) {
	return o.s.creditSetting(ctx, metaKeyWarmCPUClass, DefaultWarmCPUClassCores)
}

// CreditRateTable returns the deployment price ladder.
func (o *Operator) CreditRateTable(ctx context.Context) (CreditRateTable, error) {
	settings, err := o.CreditSettings(ctx)
	return settings.RateTable, err
}

func setOperatorCreditSettingsTx(ctx context.Context, tx *storeTx, up CreditSettingsUpdate) (_ CreditSettings, err error) {
	writes, err := creditSettingsWrites(up)
	if err != nil {
		return CreditSettings{}, err
	}

	if up.RateMicroPerSecond != nil && up.RateTable == nil {
		raw, err := creditSettingRawTx(ctx, tx, metaKeyCreditRateTable)
		if err != nil {
			return CreditSettings{}, err
		}
		if raw != "" {
			return CreditSettings{}, fmt.Errorf(
				"%w: rate_micro_per_second is the four-core entry of the rate table; write rate_table instead",
				ErrInvalidCreditSetting)
		}
	}
	out, err := writeCreditSettingsTx(ctx, tx, writes)
	if err != nil {
		return CreditSettings{}, err
	}
	return out, nil
}
