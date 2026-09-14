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

	// safety: the watermark is per team and a team need not hold a quota row
	// to hold bytes, so it lives here rather than beside a quota that may not
	// exist; a row here is created by the first pass that sees the team.
	metaKeyStorageChargedThroughPrefix = "storage_charged_through/"
)

// safety: one pass expires a bounded number of runs so a first sweep on a team
// far above its allowance does not hold the write lock over its whole history.
const storageAllowanceSweepMaxRuns = 500

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
// transaction, so a refused write is never recorded.
func refuseStorageGrowthOnEmptyBalanceTx(
	ctx context.Context, tx *storeTx, principal string, bytes int64,
) error {
	balance, err := creditBalanceTx(ctx, tx)
	if err != nil || balance > 0 {
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

// ChargeRetainedStorage bills every team for the bytes it retains, at the
// storage rate, for the interval since it was last billed. A team is billed on
// every pass, so the meter's error is one pass interval of held-then-released
// bytes rather than a whole day of them. Bytes above the team's allowance are
// not billed, because the allowance is the most a team agreed to pay for, and
// the free allowance comes off what is left.
//
// A team the ledger has never billed is stamped with now and billed from the
// next pass, so pricing storage never bills for the past. An installation
// whose storage rate is zero writes nothing, which is what an install that set
// no rate reads.
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
	teams, err := s.retainingPrincipals(ctx)
	if err != nil {
		return out, err
	}
	for _, principal := range teams {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		charge, err := s.chargeOneTeamStorage(ctx, principal, rate, free, now)
		if errors.Is(err, errStorageChargeRaced) {
			continue
		}
		if err != nil {
			return out, fmt.Errorf("storage: charge team %s: %w", principal, err)
		}
		if charge != nil {
			out.Charges = append(out.Charges, *charge)
			out.ChargedMicro += charge.AmountMicro
		}
	}
	return out, nil
}

// safety: the teams worth billing are the ones holding bytes, not the ones
// holding a quota row; a team with retained bytes and no quota row would
// otherwise store without ever paying for it.
func (s *Store) retainingPrincipals(ctx context.Context) (_ []string, err error) {
	rows, err := s.query(ctx,
		`SELECT principal FROM storage_run_usage GROUP BY principal ORDER BY principal ASC`)
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	var out []string
	for rows.Next() {
		var principal string
		if err := rows.Scan(&principal); err != nil {
			return nil, err
		}
		out = append(out, principal)
	}
	return out, rows.Err()
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
	stamped, err := creditSettingRawTx(ctx, tx, storageChargedThroughKey(principal))
	if err != nil {
		return nil, err
	}
	if stamped == "" {
		if err := stampStorageChargedThroughTx(ctx, tx, principal, "", now.UnixNano()); err != nil {
			return nil, err
		}
		return nil, tx.Commit()
	}
	through := parseCreditSetting(stamped, 0)
	seconds := (now.UnixNano() - through) / int64(time.Second)
	if seconds <= 0 {
		return nil, tx.Commit()
	}
	// safety: the watermark moves whether or not the truncated amount is worth
	// a micro-credit, so an interval is never billed twice; what truncation
	// forgives is at most one micro-credit per team per pass.
	if err := stampStorageChargedThroughTx(ctx, tx, principal,
		stamped, through+seconds*int64(time.Second)); err != nil {
		return nil, err
	}
	billable, err := billableRetainedBytesTx(ctx, tx, principal, free)
	if err != nil {
		return nil, err
	}
	amount, err := storageChargeMicro(billable, rate, seconds)
	if err != nil {
		return nil, err
	}
	if amount <= 0 {
		return nil, tx.Commit()
	}
	charge, err := insertStorageChargeTx(ctx, tx, storageCharge{
		Principal: principal, Bytes: billable,
		Seconds: seconds, AmountMicro: amount, NowNS: now.UnixNano(),
	})
	if err != nil {
		return nil, err
	}
	return charge, tx.Commit()
}

// safety: a team never pays for more than the allowance it named, because that
// is what every page describing the allowance promises, and the sweep has
// already expired what stands above it by the time this runs.
func billableRetainedBytesTx(ctx context.Context, tx *storeTx, principal string, free int64) (int64, error) {
	var retained sql.NullInt64
	if err := tx.QueryRowContext(ctx,
		`SELECT SUM(bytes) FROM storage_run_usage WHERE principal = ?`, principal).
		Scan(&retained); err != nil {
		return 0, err
	}
	quota, err := storageQuotaForTx(ctx, tx, principal)
	if err != nil {
		return 0, err
	}
	billable := retained.Int64
	if quota.AllowanceBytes > 0 && billable > quota.AllowanceBytes {
		billable = quota.AllowanceBytes
	}
	return max(billable-free, 0), nil
}

// safety: the watermark is a compare-and-set against the exact value this pass
// read, so two controllers passing at once bill one interval once whatever
// either one's clock says.
func stampStorageChargedThroughTx(
	ctx context.Context, tx *storeTx, principal, from string, to int64,
) error {
	key := storageChargedThroughKey(principal)
	if from == "" {
		res, err := tx.ExecContext(ctx, `
                INSERT INTO sparkwing_meta (key, value, updated_at) VALUES (?, ?, ?)
                ON CONFLICT (key) DO NOTHING`,
			key, formatCreditSetting(to), time.Now().UnixNano())
		if err != nil {
			return err
		}
		return storageWatermarkWon(res)
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE sparkwing_meta SET value = ?, updated_at = ? WHERE key = ? AND value = ?`,
		formatCreditSetting(to), time.Now().UnixNano(), key, from)
	if err != nil {
		return err
	}
	return storageWatermarkWon(res)
}

// safety: another pass moved the watermark first, so this one bills nothing
// rather than billing the interval a second time.
var errStorageChargeRaced = errors.New("storage: another pass already billed this interval")

func storageWatermarkWon(res sql.Result) error {
	moved, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if moved == 0 {
		return errStorageChargeRaced
	}
	return nil
}

func storageChargedThroughKey(principal string) string {
	return metaKeyStorageChargedThroughPrefix + principal
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
	teams, err := s.retainingPrincipals(ctx)
	if err != nil {
		return out, err
	}
	drain, cutoff, err := s.storageDrainTarget(ctx, now)
	if err != nil {
		return out, err
	}
	for _, principal := range teams {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		quota, err := s.StorageQuotaFor(ctx, principal)
		if err != nil {
			return out, err
		}
		if quota.AllowanceBytes > 0 {
			if err := s.expireAboveCeiling(ctx, principal, quota.AllowanceBytes, time.Time{}, &out); err != nil {
				return out, fmt.Errorf("storage: expire above the allowance of team %s: %w", principal, err)
			}
		}
		if !drain {
			continue
		}
		if err := s.expireAboveCeiling(ctx, principal, cutoff.free, cutoff.before, &out); err != nil {
			return out, fmt.Errorf("storage: drain team %s to the free allowance: %w", principal, err)
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
