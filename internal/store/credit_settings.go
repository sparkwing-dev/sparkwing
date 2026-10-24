package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

func (s *Store) creditSetting(ctx context.Context, key string, fallback int64) (int64, error) {
	raw, err := s.creditSettingRaw(ctx, key)
	if err != nil {
		return 0, err
	}
	return integerCreditSetting(key, raw, fallback)
}

func creditSettingTx(ctx context.Context, tx *storeTx, key string, fallback int64) (int64, error) {
	raw, err := creditSettingRawTx(ctx, tx, key)
	if err != nil {
		return 0, err
	}
	return integerCreditSetting(key, raw, fallback)
}

func integerCreditSetting(key, raw string, fallback int64) (int64, error) {
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: %s must hold an integer", ErrInvalidCreditSetting, key)
	}
	validate := func(v int64) error {
		if v < 0 {
			return ErrInvalidCreditSetting
		}
		return nil
	}
	switch key {
	case metaKeyCreditRateMicro:
		validate = validCreditRate
	case metaKeyWarmCPUClass:
		validate = validWarmCPUClass
	case metaKeyStorageRateMicroPerGBDay:
		validate = validStorageRate
	case metaKeyStorageFreeAllowanceBytes:
		validate = validStorageFreeAllowance
	}
	if err := validate(value); err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return value, nil
}

func setCreditSettingTx(ctx context.Context, tx *storeTx, key, value string) error {
	if !json.Valid([]byte(value)) {
		return fmt.Errorf("%w: %s must hold JSON", ErrInvalidCreditSetting, key)
	}
	result, err := tx.execContext(ctx, `INSERT INTO settings(key,value) VALUES (?,?)
ON CONFLICT(key) DO UPDATE SET value=excluded.value WHERE settings.sealed=0`, key, []byte(value))
	if err != nil {
		return err
	}
	return requireOneRow(result, "credit setting")
}

func (s *Store) creditSettingRaw(ctx context.Context, key string) (string, error) {
	return scanCreditSetting(s.queryRow(ctx, "SELECT value,sealed FROM settings WHERE key=?", key), key)
}

func creditSettingRawTx(ctx context.Context, tx *storeTx, key string) (string, error) {
	return scanCreditSetting(tx.QueryRowContext(ctx, "SELECT value,sealed FROM settings WHERE key=?", key), key)
}

// safety: only an absent setting selects defaults; corrupt or sealed bytes cannot silently change a price.
func scanCreditSetting(row rowScanner, key string) (string, error) {
	var raw []byte
	var sealed int
	err := row.Scan(&raw, &sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if sealed != 0 || !json.Valid(raw) {
		return "", fmt.Errorf("%w: %s must hold unsealed JSON", ErrInvalidCreditSetting, key)
	}
	return string(raw), nil
}
