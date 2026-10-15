package store

import (
	"context"
	"fmt"
	"math/big"
	"time"
)

// Retained storage is billed from the same ledger as cloud runner time. A team
// keeps bytes after its runs end, so the charge is a rent on what is still
// stored rather than a price on work: every pass bills the bytes above the free
// allowance for the interval since the previous pass, pro rata.
const (
	// StorageBytesPerGB is the gigabyte the storage rate prices, which is the
	// gibibyte every other byte figure in this store is written in.
	StorageBytesPerGB = 1 << 30

	// StorageChargeDaySeconds is the day the storage rate prices.
	StorageChargeDaySeconds = 86_400

	// CloudStorageRateMicroPerGBDay prices a gibibyte-day at the published
	// 0.10 dollars a GB-month over a thirty-day month, which is 100 credits
	// a month and about 3.33 credits a day. Nothing sets it on its own: an
	// installation bills storage only once an operator writes the rate.
	CloudStorageRateMicroPerGBDay = 10 * MicroCreditsPerCent / 30

	// MaxStorageRateMicroPerGBDay is the highest price an operator may put on
	// a gibibyte-day, ten thousand dollars, which is the ceiling that keeps the
	// charge arithmetic inside int64 for any byte count a database can hold.
	MaxStorageRateMicroPerGBDay = 1_000_000_000_000
)

const (
	metaKeyStorageRateMicroPerGBDay  = "storage_rate_micro_per_gb_day"
	metaKeyStorageFreeAllowanceBytes = "storage_free_allowance_bytes"
)

// StorageChargeSweep is what one storage-charge pass billed.
type StorageChargeSweep struct {
	// Charges holds one row per team billed, in the order they were billed.
	Charges []CreditCharge
	// ChargedMicro is what the pass took out of the ledger.
	ChargedMicro int64
}

func validStorageRate(micro int64) error {
	if micro < 0 || micro > MaxStorageRateMicroPerGBDay {
		return fmt.Errorf(
			"%w: the storage rate must be between 0 and %d micro-credits per gibibyte-day, got %d",
			ErrInvalidCreditSetting, int64(MaxStorageRateMicroPerGBDay), micro)
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

// safety: the balance and the write that grows the team's bytes share one
// transaction, so a refused write is never recorded.
func refuseStorageGrowthOnEmptyBalanceTx(
	ctx context.Context, tx *storeTx, team Team, principal string, bytes int64,
) error {
	room, err := headroomTx(ctx, tx, team, time.Now())
	if err != nil || room > 0 {
		return err
	}
	balance, err := creditBalanceTx(ctx, tx, team)
	if err != nil {
		return err
	}
	return fmt.Errorf(
		"%w: team %s cannot store %d more bytes on a balance of %s credits; "+
			"add credits, and retention releases what is already stored",
		ErrInsufficientCredits, principal, bytes, FormatCredits(balance))
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
	num.Quo(num, big.NewInt(int64(StorageBytesPerGB)*StorageChargeDaySeconds))
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
	card, err := teamCardTx(ctx, tx, c.Team)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `
        INSERT INTO credit_charges (team, id, run_id, node_id, capability_prefix, principal, kind,
                seconds, amount_micro, storage_bytes, charged_at, card_fingerprint)
        VALUES (?, ?, '', '', '', ?, ?, ?, ?, ?, ?, ?)`,
		string(c.Team), id, c.Principal, CreditChargeStorage,
		c.Seconds, c.AmountMicro, c.Bytes, c.NowNS, card); err != nil {
		return nil, fmt.Errorf("credits: insert storage charge: %w", err)
	}
	if err := addSpendTx(ctx, tx, c.Team, card, c.NowNS, c.AmountMicro); err != nil {
		return nil, err
	}
	return &CreditCharge{
		ID: id, Team: c.Team, Principal: c.Principal, Kind: CreditChargeStorage,
		Seconds: c.Seconds, AmountMicro: c.AmountMicro, StorageBytes: c.Bytes,
		ChargedAt: time.Unix(0, c.NowNS).UTC(),
	}, nil
}
