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
