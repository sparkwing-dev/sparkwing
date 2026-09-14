package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
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

	// StorageChargeInterval is how much time must pass before a team is
	// billed again. The amount is pro rata on the exact interval, so a pass
	// that arrives late bills what it covers rather than a whole day.
	StorageChargeInterval = 24 * time.Hour

	// CloudStorageRateMicroPerGBDay prices a gibibyte-day at GitHub's $0.25
	// per GB-month over a thirty-day month, which is 25 credits a month and
	// 0.833333 credits a day. Nothing sets it on its own: an installation
	// bills storage only once an operator writes the rate.
	CloudStorageRateMicroPerGBDay = 833_333

	// MaxStorageRateMicroPerGBDay is the highest price an operator may put on
	// a gibibyte-day, a million credits, which is the ceiling that keeps the
	// charge arithmetic inside int64 for any byte count a database can hold.
	MaxStorageRateMicroPerGBDay = 1_000_000_000_000
)

const (
	metaKeyStorageRateMicroPerGBDay  = "storage_rate_micro_per_gb_day"
	metaKeyStorageFreeAllowanceBytes = "storage_free_allowance_bytes"
)

// safety: one pass expires a bounded number of runs so a first sweep on a team
// far above its allowance does not hold the write lock over its whole history.
const storageAllowanceSweepMaxRuns = 500

// StorageCreditsExhaustedError refuses a write that would grow a team's
// retained bytes while the ledger has nothing left to pay the rent on them. It
// reports [ErrInsufficientCredits], so a caller answers it the way it answers a
// claim the ledger refused.
type StorageCreditsExhaustedError struct {
	Principal      string
	BalanceMicro   int64
	RequestedBytes int64
}

func (e *StorageCreditsExhaustedError) Error() string {
	return fmt.Sprintf(
		"insufficient credits: team %s cannot store %d more bytes on a balance of %s credits; "+
			"add credits, and retention releases what is already stored",
		e.Principal, e.RequestedBytes, FormatCredits(e.BalanceMicro))
}

// Unwrap reports [ErrInsufficientCredits], so a caller matches the condition
// without knowing this type.
func (e *StorageCreditsExhaustedError) Unwrap() error { return ErrInsufficientCredits }

// StorageChargeSweep is what one storage-charge pass billed.
type StorageChargeSweep struct {
	// Charges holds one row per team billed, in the order they were billed.
	Charges []CreditCharge
	// ChargedMicro is what the pass took out of the ledger.
	ChargedMicro int64
}

// StorageAllowanceSweep is what one allowance pass expired.
type StorageAllowanceSweep struct {
	Runs   int64
	Bytes  int64
	Events int64
}

// StorageRateMicroPerGBDay returns what one gibibyte kept for one day costs in
// micro-credits. An installation that never set one reads zero and is never
// billed for storage.
func (s *Store) StorageRateMicroPerGBDay(ctx context.Context) (int64, error) {
	return s.creditSetting(ctx, metaKeyStorageRateMicroPerGBDay, 0)
}

// StorageFreeAllowanceBytes returns how many retained bytes a team keeps
// without being billed for them. An installation that never set one reads
// zero.
func (s *Store) StorageFreeAllowanceBytes(ctx context.Context) (int64, error) {
	return s.creditSetting(ctx, metaKeyStorageFreeAllowanceBytes, 0)
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

// DatabaseNow reads the instant the database keeps. Billing compares one pass
// against the next, so the interval a charge covers is measured off the clock
// every controller sharing the database shares, never off a process clock a
// second controller or an NTP step would disagree with.
func (s *Store) DatabaseNow(ctx context.Context) (time.Time, error) {
	var secs int64
	if err := s.queryRow(ctx, `SELECT `+s.nowSeconds()).Scan(&secs); err != nil {
		return time.Time{}, fmt.Errorf("storage: read the database clock: %w", err)
	}
	return time.Unix(secs, 0).UTC(), nil
}

// StorageRetainedBytes reports what a team still has stored: the bytes of
// every run whose rows the controller still holds. Retention and the allowance
// sweep both take bytes out of it, which is what makes it a measure of what is
// kept rather than of what was written.
func (s *Store) StorageRetainedBytes(ctx context.Context, principal string) (int64, error) {
	var bytes sql.NullInt64
	err := s.queryRow(ctx,
		`SELECT SUM(bytes) FROM storage_run_usage WHERE principal = ?`, principal).Scan(&bytes)
	if err != nil {
		return 0, err
	}
	return bytes.Int64, nil
}

// SetStorageAllowance records how many retained bytes a team asked to keep and
// answers the quota it is held to afterwards. Zero keeps everything. The
// allowance is the ceiling the sweep expires oldest-first down to, so raising
// it keeps more and costs more, and lowering it expires the oldest runs on the
// next pass.
func (s *Store) SetStorageAllowance(ctx context.Context, principal string, bytes int64) (StorageQuota, error) {
	if !ValidStoragePrincipal(principal) {
		return StorageQuota{}, fmt.Errorf("storage: %q is not a usable principal name", principal)
	}
	if bytes < 0 {
		return StorageQuota{}, errors.New("storage: the allowance must not be negative")
	}
	if _, err := s.exec(ctx, `
INSERT INTO storage_quotas (principal, tier, max_bytes_per_run,
        max_bytes_per_month, max_objects_per_run, storage_allowance_bytes, updated_at)
VALUES (?, '', 0, 0, 0, ?, ?)
ON CONFLICT (principal) DO UPDATE SET
        storage_allowance_bytes = excluded.storage_allowance_bytes,
        updated_at = excluded.updated_at`,
		principal, bytes, time.Now().UnixNano()); err != nil {
		return StorageQuota{}, err
	}
	return s.StorageQuotaFor(ctx, principal)
}

// safety: the balance and the write that grows the team's bytes share one
// transaction, so a grant landing beside the check cannot let an unpaid write
// through and a refused write is never recorded.
func refuseStorageGrowthOnEmptyBalanceTx(
	ctx context.Context, tx *storeTx, principal string, bytes int64,
) error {
	rate, err := creditSettingTx(ctx, tx, metaKeyStorageRateMicroPerGBDay, 0)
	if err != nil || rate <= 0 {
		return err
	}
	balance, err := creditBalanceTx(ctx, tx)
	if err != nil {
		return err
	}
	if balance > 0 {
		return nil
	}
	return &StorageCreditsExhaustedError{
		Principal: principal, BalanceMicro: balance, RequestedBytes: bytes,
	}
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

// ChargeRetainedStorage bills every team for the bytes it has kept above the
// free allowance since it was last billed, pro rata at the storage rate. A pass
// bills a team only once [StorageChargeInterval] has passed, and a team the
// ledger has never billed is stamped with now and billed from the next pass, so
// turning the rate on never bills for the past.
//
// An installation whose storage rate is zero writes nothing, which is what an
// install that set no rate reads.
func (s *Store) ChargeRetainedStorage(ctx context.Context, now time.Time) (StorageChargeSweep, error) {
	var out StorageChargeSweep
	rate, err := s.StorageRateMicroPerGBDay(ctx)
	if err != nil || rate <= 0 {
		return out, err
	}
	free, err := s.StorageFreeAllowanceBytes(ctx)
	if err != nil {
		return out, err
	}
	quotas, err := s.ListStorageQuotas(ctx)
	if err != nil {
		return out, err
	}
	for _, quota := range quotas {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		charge, err := s.chargeOneTeamStorage(ctx, quota.Principal, rate, free, now)
		if err != nil {
			return out, fmt.Errorf("storage: charge team %s: %w", quota.Principal, err)
		}
		if charge != nil {
			out.Charges = append(out.Charges, *charge)
			out.ChargedMicro += charge.AmountMicro
		}
	}
	return out, nil
}

func (s *Store) chargeOneTeamStorage(
	ctx context.Context, principal string, rate, free int64, now time.Time,
) (_ *CreditCharge, err error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer rollbackUnlessDone(tx, &err)
	if err := lockCreditLedgerTx(ctx, tx); err != nil {
		return nil, err
	}
	var through int64
	if err := tx.QueryRowContext(ctx,
		`SELECT storage_charged_through FROM storage_quotas WHERE principal = ?`,
		principal).Scan(&through); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, tx.Commit()
		}
		return nil, err
	}
	// safety: a team the rate has never covered is stamped and left, so
	// turning storage billing on does not bill every byte kept before it.
	if through == 0 {
		if err := stampStorageChargedThroughTx(ctx, tx, principal, now.UnixNano()); err != nil {
			return nil, err
		}
		return nil, tx.Commit()
	}
	seconds := (now.UnixNano() - through) / int64(time.Second)
	if seconds < int64(StorageChargeInterval/time.Second) {
		return nil, tx.Commit()
	}
	var retained sql.NullInt64
	if err := tx.QueryRowContext(ctx,
		`SELECT SUM(bytes) FROM storage_run_usage WHERE principal = ?`, principal).
		Scan(&retained); err != nil {
		return nil, err
	}
	amount, err := storageChargeMicro(retained.Int64-free, rate, seconds)
	if err != nil {
		return nil, err
	}
	if err := stampStorageChargedThroughTx(ctx, tx, principal,
		through+seconds*int64(time.Second)); err != nil {
		return nil, err
	}
	if amount <= 0 {
		return nil, tx.Commit()
	}
	charge, err := insertStorageChargeTx(ctx, tx, storageCharge{
		Principal: principal, Bytes: retained.Int64 - free,
		Seconds: seconds, AmountMicro: amount, NowNS: now.UnixNano(),
	})
	if err != nil {
		return nil, err
	}
	return charge, tx.Commit()
}

func stampStorageChargedThroughTx(ctx context.Context, tx *storeTx, principal string, at int64) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE storage_quotas SET storage_charged_through = ? WHERE principal = ?`, at, principal)
	return err
}

type storageCharge struct {
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
	if _, err := tx.ExecContext(ctx, `
        INSERT INTO credit_charges (id, run_id, node_id, token_prefix, principal, kind,
                seconds, amount_micro, storage_bytes, charged_at)
        VALUES (?, '', '', '', ?, ?, ?, ?, ?, ?)`,
		id, c.Principal, CreditChargeStorage, c.Seconds, c.AmountMicro, c.Bytes, c.NowNS); err != nil {
		return nil, fmt.Errorf("credits: insert storage charge: %w", err)
	}
	return &CreditCharge{
		ID: id, Principal: c.Principal, Kind: CreditChargeStorage,
		Seconds: c.Seconds, AmountMicro: c.AmountMicro, StorageBytes: c.Bytes,
		ChargedAt: time.Unix(0, c.NowNS).UTC(),
	}, nil
}

// SweepStorageAllowance expires the oldest finished runs of every team holding
// more than it asked to keep, until what it keeps is back inside its allowance.
// A team with no allowance keeps everything.
//
// A spent balance lowers the ceiling to the free allowance, and that drain
// takes only runs whose retention window has already elapsed, so non-payment
// never removes anything the window still covers. An installation with no
// retention window releases nothing, so nothing drains there.
func (s *Store) SweepStorageAllowance(ctx context.Context, now time.Time) (StorageAllowanceSweep, error) {
	var out StorageAllowanceSweep
	quotas, err := s.ListStorageQuotas(ctx)
	if err != nil {
		return out, err
	}
	drain, cutoff, err := s.storageDrainTarget(ctx, now)
	if err != nil {
		return out, err
	}
	for _, quota := range quotas {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		if quota.AllowanceBytes > 0 {
			if err := s.expireAboveCeiling(ctx, quota.Principal, quota.AllowanceBytes, time.Time{}, &out); err != nil {
				return out, fmt.Errorf("storage: expire above the allowance of team %s: %w", quota.Principal, err)
			}
		}
		if !drain {
			continue
		}
		if err := s.expireAboveCeiling(ctx, quota.Principal, cutoff.free, cutoff.before, &out); err != nil {
			return out, fmt.Errorf("storage: drain team %s to the free allowance: %w", quota.Principal, err)
		}
	}
	return out, nil
}

type storageDrain struct {
	free   int64
	before time.Time
}

// safety: the drain reaches below the ceiling the customer chose, so it runs
// only while the ledger cannot pay the rent, and only over runs the retention
// window has already released.
func (s *Store) storageDrainTarget(ctx context.Context, now time.Time) (bool, storageDrain, error) {
	rate, err := s.StorageRateMicroPerGBDay(ctx)
	if err != nil || rate <= 0 {
		return false, storageDrain{}, err
	}
	balance, err := s.CreditBalanceMicro(ctx)
	if err != nil || balance > 0 {
		return false, storageDrain{}, err
	}
	settings, err := s.StorageSettings(ctx)
	if err != nil || settings.EventRetentionDays <= 0 {
		return false, storageDrain{}, err
	}
	free, err := s.StorageFreeAllowanceBytes(ctx)
	if err != nil {
		return false, storageDrain{}, err
	}
	return true, storageDrain{free: free, before: retentionCutoff(now, settings.EventRetentionDays)}, nil
}

// safety: the caller decides whether a ceiling binds at all, because zero means
// opposite things on the two passes: a team named no allowance, or a spent
// balance leaves nothing free.
func (s *Store) expireAboveCeiling(
	ctx context.Context, principal string, ceiling int64, before time.Time, out *StorageAllowanceSweep,
) error {
	retained, err := s.StorageRetainedBytes(ctx, principal)
	if err != nil || retained <= ceiling {
		return err
	}
	runs, err := s.oldestRetainedRuns(ctx, principal, before)
	if err != nil {
		return err
	}
	for _, run := range runs {
		if retained <= ceiling {
			return nil
		}
		events, err := s.expireRunStorage(ctx, principal, run.id)
		if err != nil {
			return err
		}
		retained -= run.bytes
		out.Runs++
		out.Bytes += run.bytes
		out.Events += events
	}
	return nil
}

type retainedRun struct {
	id    string
	bytes int64
}

// safety: a run still writing its own history keeps every byte of it, and a
// zero cutoff means the customer's own ceiling rather than the drain, so the
// age bound applies only when one was given.
func (s *Store) oldestRetainedRuns(
	ctx context.Context, principal string, before time.Time,
) (_ []retainedRun, err error) {
	query := `
SELECT u.run_id, u.bytes
  FROM storage_run_usage u JOIN runs r ON r.id = u.run_id
 WHERE u.principal = ? AND r.` + runTerminalIn + ` AND r.created_at < ?
 ORDER BY r.created_at ASC, u.run_id ASC
 LIMIT ?`
	bound := int64(math.MaxInt64)
	if !before.IsZero() {
		bound = before.UnixNano()
	}
	rows, err := s.query(ctx, query, principal, bound, storageAllowanceSweepMaxRuns)
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	var out []retainedRun
	for rows.Next() {
		var run retainedRun
		if err := rows.Scan(&run.id, &run.bytes); err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, rows.Err()
}

// safety: the usage row and the rows whose bytes it counted go together, so
// what a team is billed for is always what the controller still holds.
func (s *Store) expireRunStorage(ctx context.Context, principal, runID string) (_ int64, err error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return 0, err
	}
	defer rollbackUnlessDone(tx, &err)
	res, err := tx.ExecContext(ctx, `DELETE FROM events WHERE run_id = ?`, runID)
	if err != nil {
		return 0, err
	}
	events, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM storage_run_usage WHERE principal = ? AND run_id = ?`,
		principal, runID); err != nil {
		return 0, err
	}
	return events, tx.Commit()
}
