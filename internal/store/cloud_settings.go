package store

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

// CloudUpsizeEnabledSetting names the deployment switch for next-size warm nodes.
const CloudUpsizeEnabledSetting = "cloud_upsize_enabled"

// CloudUpsizeEnabled reads the operator switch; an absent setting enables upsizing.
func (o *Operator) CloudUpsizeEnabled(ctx context.Context) (_ bool, err error) {
	tx, err := o.s.beginSnapshotReadTx(ctx)
	if err != nil {
		return false, err
	}
	defer rollbackUnlessDone(tx, &err)
	enabled, err := cloudUpsizeEnabledTx(ctx, tx)
	if err != nil {
		return false, err
	}
	return enabled, tx.Commit()
}

func cloudUpsizeEnabledTx(ctx context.Context, tx *storeTx) (bool, error) {
	raw, err := creditSettingRawTx(ctx, tx, CloudUpsizeEnabledSetting)
	if err != nil {
		return false, err
	}
	if raw == "" {
		return true, nil
	}
	var enabled *bool
	if err := json.Unmarshal([]byte(raw), &enabled); err != nil || enabled == nil {
		return false, authorityError(apierr.IntegrityFailure, "cloud_upsize_enabled must hold a boolean")
	}
	return *enabled, nil
}

// WriteCloudUpsizeEnabled changes the deployment switch under current operator authority.
func (o *Operator) WriteCloudUpsizeEnabled(ctx context.Context, a capability.Authorized, enabled bool) error {
	return operatorSettingsWrite(ctx, o, a, capability.OperatorLimits, "WriteCloudUpsizeEnabled", func(tx *storeTx, _ capability.Principal) error {
		return setCreditSettingTx(ctx, tx, CloudUpsizeEnabledSetting, strconv.FormatBool(enabled))
	})
}
