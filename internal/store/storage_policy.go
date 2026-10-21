package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
)

const (
	storageAuditRetentionKey  = "operator.audit_retention_days"
	storageRunRetentionKey    = "operator.run_retention_days"
	storageMetricRetentionKey = "operator.node_metric_retention_days"
	metaKeyDatabaseAlarmBytes = "storage_alarm_database_bytes"
	metaKeyDefaultStorageTier = "storage_default_tier"
)

// StorageSettings reads one committed snapshot of unsealed deployment policy.
func (o *Operator) StorageSettings(ctx context.Context) (StorageSettings, error) {
	var out StorageSettings
	err := o.s.inTx(ctx, func(tx *storeTx) error {
		var err error
		out, err = storageSettingsTx(ctx, tx)
		return err
	})
	return out, err
}

func storageSettingsTx(ctx context.Context, tx *storeTx) (StorageSettings, error) {
	var out StorageSettings
	for _, field := range []struct {
		key   string
		value *int64
	}{
		{storageAuditRetentionKey, &out.AuditRetentionDays},
		{storageRunRetentionKey, &out.RunRetentionDays},
		{storageMetricRetentionKey, &out.NodeMetricRetentionDays},
		{metaKeyDatabaseAlarmBytes, &out.DatabaseAlarmBytes},
	} {
		raw, err := creditSettingRawTx(ctx, tx, field.key)
		if err != nil {
			return StorageSettings{}, err
		}
		value, err := integerCreditSetting(field.key, raw, 0)
		if err != nil {
			return StorageSettings{}, err
		}
		*field.value = value
	}
	if out.AuditRetentionDays == 0 {
		out.AuditRetentionDays = defaultAuditRetentionDays
	}
	var err error
	out.DefaultTier, err = storageDefaultTierTx(ctx, tx)
	if err != nil {
		return StorageSettings{}, err
	}

	return out, validateStorageSettings(out)
}

func storageDefaultTierTx(ctx context.Context, tx *storeTx) (string, error) {
	raw, err := creditSettingRawTx(ctx, tx, metaKeyDefaultStorageTier)
	if err != nil || raw == "" {
		return "", err
	}
	var tier *string
	if json.Unmarshal([]byte(raw), &tier) != nil || tier == nil {
		return "", errors.New("storage: default tier must be a JSON string")
	}
	if *tier != "" {
		if _, ok := TierQuota(*tier); !ok {
			return "", fmt.Errorf("storage: unknown default tier %q", *tier)
		}
	}
	return *tier, nil
}

func validateStorageSettings(in StorageSettings) error {
	if in.AuditRetentionDays < 0 || in.RunRetentionDays < 0 || in.NodeMetricRetentionDays < 0 || in.DatabaseAlarmBytes < 0 {
		return errors.New("storage: settings must not be negative")
	}
	if in.AuditRetentionDays > maxRetentionDays || in.RunRetentionDays > maxRetentionDays || in.NodeMetricRetentionDays > maxRetentionDays {
		return errors.New("storage: retention duration overflows")
	}
	if in.DefaultTier != "" {
		if _, ok := TierQuota(in.DefaultTier); !ok {
			return fmt.Errorf("storage: unknown default tier %q", in.DefaultTier)
		}
	}
	return nil
}

// SetStorageSettings replaces the policy atomically. Invalid or sealed settings refuse the batch.
func (o *Operator) SetStorageSettings(ctx context.Context, in StorageSettings) error {
	return o.s.inTx(ctx, func(tx *storeTx) error { return setStorageSettingsTx(ctx, tx, in) })
}

func setStorageSettingsTx(ctx context.Context, tx *storeTx, in StorageSettings) error {
	if err := validateStorageSettings(in); err != nil {
		return err
	}
	if in.AuditRetentionDays == 0 {
		in.AuditRetentionDays = defaultAuditRetentionDays
	}
	tier, err := json.Marshal(in.DefaultTier)
	if err != nil {
		return err
	}
	for _, field := range []struct{ key, value string }{
		{storageAuditRetentionKey, strconv.FormatInt(in.AuditRetentionDays, 10)},
		{storageRunRetentionKey, strconv.FormatInt(in.RunRetentionDays, 10)},
		{storageMetricRetentionKey, strconv.FormatInt(in.NodeMetricRetentionDays, 10)},
		{metaKeyDatabaseAlarmBytes, strconv.FormatInt(in.DatabaseAlarmBytes, 10)},
		{metaKeyDefaultStorageTier, string(tier)},
	} {
		if err := setCreditSettingTx(ctx, tx, field.key, field.value); err != nil {
			return err
		}
	}
	return nil
}

// SeedRunRetention supplies a deployment default only when no value exists.
// Explicit zero survives restarts, and metric retention keeps its independent policy.
func (o *Operator) SeedRunRetention(ctx context.Context, days int64) (bool, error) {
	if err := validateStorageSettings(StorageSettings{RunRetentionDays: days}); err != nil {
		return false, err
	}
	result, err := o.s.exec(ctx, "INSERT INTO settings(key,value) VALUES (?,?) ON CONFLICT(key) DO NOTHING", storageRunRetentionKey, []byte(strconv.FormatInt(days, 10)))
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}
