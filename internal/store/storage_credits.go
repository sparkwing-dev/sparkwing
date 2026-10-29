package store

import (
	"context"
	"fmt"
	"math/big"
	"time"
)

const (
	// StorageBytesPerGB is the gigabyte the storage rate prices, which is the gibibyte every other byte figure in this store is written in.
	StorageBytesPerGB = 1 << 30

	// storageChargeDaySeconds is the day the storage rate prices.
	storageChargeDaySeconds = 86_400

	// CloudStorageRateMicroPerGBDay prices a gibibyte-day at the published 0.10 dollars a GB-month over a thirty-day month, which is 100 credits a month and about 3.33 credits a day.
	CloudStorageRateMicroPerGBDay = 10 * MicroCreditsPerCent / 30

	maxStorageRateMicroPerGBDay = 1_000_000_000_000
)

const (
	metaKeyStorageRateMicroPerGBDay  = "storage_rate_micro_per_gb_day"
	metaKeyStorageFreeAllowanceBytes = "storage_free_allowance_bytes"
)

// storageRateTx prices a stored gibibyte-day: the operator's setting, else the published rate on Sparkwing Cloud
// and nothing on a self-hosted deployment.
func storageRateTx(ctx context.Context, tx *storeTx) (int64, error) {
	fallback := int64(0)
	if tx.store.cloudStorage.Load() {
		fallback = CloudStorageRateMicroPerGBDay
	}
	return creditSettingTx(ctx, tx, metaKeyStorageRateMicroPerGBDay, fallback)
}

// storageFreeAllowanceTx is the retained bytes a team stores without paying, shared by billing, admission and the
// console.
func storageFreeAllowanceTx(ctx context.Context, tx *storeTx) (int64, error) {
	allowance, err := creditSettingTx(ctx, tx, metaKeyStorageFreeAllowanceBytes, defaultFreeAllowanceBytes)
	return max(allowance, 0), err
}

// StorageChargeSweep is what one storage-charge pass billed.
type StorageChargeSweep struct {
	// Charges holds one row per team billed, in the order they were billed.
	Charges []CreditCharge
	// ChargedMicro is what the pass took out of the ledger.
	ChargedMicro int64
}

func validStorageRate(micro int64) error {
	if micro < 0 || micro > maxStorageRateMicroPerGBDay {
		return fmt.Errorf(
			"%w: the storage rate must be between 0 and %d micro-credits per gibibyte-day, got %d",
			ErrInvalidCreditSetting, int64(maxStorageRateMicroPerGBDay), micro)
	}
	return nil
}

func validStorageFreeAllowance(bytes int64) error {
	if bytes < 0 {
		return fmt.Errorf("%w: the free storage allowance must not be negative, got %d",
			ErrInvalidCreditSetting, bytes)
	}
	return nil
}

// safety: the product of a byte count, a rate and an interval outgrows int64
// long before a database outgrows the bytes, so the arithmetic is exact and
// the division is the only rounding. It truncates toward zero, so a fraction
// of a micro-credit is never billed.
func storageChargeMicro(excessBytes, rateMicroPerGBDay, seconds int64) (int64, error) {
	if excessBytes <= 0 || rateMicroPerGBDay <= 0 || seconds <= 0 {
		return 0, nil
	}
	num := big.NewInt(excessBytes)
	num.Mul(num, big.NewInt(rateMicroPerGBDay))
	num.Mul(num, big.NewInt(seconds))
	num.Quo(num, big.NewInt(int64(StorageBytesPerGB)*storageChargeDaySeconds))
	if !num.IsInt64() {
		return 0, fmt.Errorf(
			"storage: %d bytes kept for %ds at %d micro-credits a gibibyte-day is more than the ledger can hold",
			excessBytes, seconds, rateMicroPerGBDay)
	}
	return num.Int64(), nil
}

type storageCharge struct {
	Team        Team
	Principal   string
	Bytes       int64
	Seconds     int64
	AmountMicro int64
	NowNS       int64
}

// safety: the row carries no cpu class and no per-second rate, because neither
// priced it; the bytes and the interval it covered are what explain the amount.
func insertStorageChargeTx(ctx context.Context, tx *storeTx, c storageCharge) (*CreditCharge, error) {
	id, err := newCreditID("charge")
	if err != nil {
		return nil, err
	}
	l, err := lockLedgerTx(ctx, tx, c.Team)
	if err != nil {
		return nil, err
	}
	charge := CreditCharge{
		ID: id, Team: c.Team, Principal: c.Principal, Kind: CreditChargeStorage,
		Seconds: c.Seconds, AmountMicro: c.AmountMicro, StorageBytes: c.Bytes,
		ChargedAt: time.Unix(0, c.NowNS).UTC(),
	}
	return l.debitFunded(ctx, charge)
}
