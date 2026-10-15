package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Storage tiers a team's quota can be drawn from.
const (
	StorageTierFree = "free"
	StorageTierPaid = "paid"
)

// Quota limits, named so a refusal says which one it hit.
const (
	StorageLimitBytesPerRun   = "bytes_per_run"
	StorageLimitBytesPerMonth = "bytes_per_month"
	StorageLimitObjectsPerRun = "objects_per_run"
)

// FreeTierQuota is the free tier's storage allowance: ten megabytes per run,
// which keeps an active five-person team under a gigabyte without a lifetime
// cap.
var FreeTierQuota = StorageQuota{
	Tier:             StorageTierFree,
	MaxBytesPerRun:   10 << 20,
	MaxBytesPerMonth: 1 << 30,
	MaxObjectsPerRun: 1000,
}

// PaidTierQuota is the paid tier's storage allowance.
var PaidTierQuota = StorageQuota{
	Tier:             StorageTierPaid,
	MaxBytesPerRun:   1 << 30,
	MaxBytesPerMonth: 100 << 30,
	MaxObjectsPerRun: 100_000,
}

// ErrStorageQuota is returned when a write would take a team past one of its
// storage limits. The enforcing call returns a [StorageQuotaError], which
// wraps it and names the limit, so a caller matches the condition with
// errors.Is and prints the reason with Error.
var ErrStorageQuota = errors.New("storage quota exceeded")

// StorageQuotaError refuses a write and names the limit it hit, what the team
// has already stored against that limit, and what the write asked for.
type StorageQuotaError struct {
	Principal string
	Limit     string
	Unit      string
	Used      int64
	Allowed   int64
	Requested int64
	// Remedy, when set, tells the caller what lifts the limit.
	Remedy string
}

func (e *StorageQuotaError) Error() string {
	msg := fmt.Sprintf("storage quota exceeded: %s for team %s: %d of %d %s used and this write adds %d",
		e.Limit, e.Principal, e.Used, e.Allowed, e.Unit, e.Requested)
	if e.Remedy != "" {
		msg += "; " + e.Remedy
	}
	return msg
}

// Unwrap reports [ErrStorageQuota], so a caller matches the condition without
// knowing this type.
func (e *StorageQuotaError) Unwrap() error { return ErrStorageQuota }

// StorageSettings configures leased retention, database alarms and the default team quota.
type StorageSettings struct {
	RunRetentionDays        int64
	NodeMetricRetentionDays int64
	DatabaseAlarmBytes      int64

	// DefaultTier applies only to teams without an explicit quota row.
	DefaultTier string
}

// RetentionOn reports whether run or metric expiration is enabled.
func (s StorageSettings) RetentionOn() bool {
	return s.RunRetentionDays > 0 || s.NodeMetricRetentionDays > 0
}

// StorageQuota is one team's allowance. A zero limit is unbounded, so a quota
// may cap bytes without capping objects.
type StorageQuota struct {
	Team             Team
	Tier             string
	MaxBytesPerRun   int64
	MaxBytesPerMonth int64
	MaxObjectsPerRun int64
	// AllowanceBytes caps the retained bytes priced by storage billing. Zero is uncapped.
	// [Tenant.SetStorageAllowance] writes it independently of the write limits.
	AllowanceBytes int64
}

// Unlimited reports whether this quota refuses nothing.
func (q StorageQuota) Unlimited() bool {
	return q.MaxBytesPerRun <= 0 && q.MaxBytesPerMonth <= 0 && q.MaxObjectsPerRun <= 0
}

// StorageUsage is what one team has stored: the totals for one run, and the
// maintained totals for one calendar month.
type StorageUsage struct {
	Team         Team
	RunID        string
	Month        string
	RunBytes     int64
	RunObjects   int64
	MonthBytes   int64
	MonthObjects int64
}

// StorageTeamUsage is one row of the largest-teams report: what a team stored
// over a month.
type StorageTeamUsage struct {
	Team    Team
	Month   string
	Bytes   int64
	Objects int64
}

// TableSize is one table's total size on disk, indexes and toast included.
type TableSize struct {
	Name  string `json:"name"`
	Bytes int64  `json:"bytes"`
}

// DatabaseSize is a size sample taken off the database itself. It counts the
// pages a database actually holds, never the free pages a delete left behind,
// so an alarm clears once retention has removed the rows.
type DatabaseSize struct {
	TotalBytes int64       `json:"total_bytes"`
	Tables     []TableSize `json:"tables,omitempty"`
	SampledAt  time.Time   `json:"sampled_at"`
}

// DatabaseSize samples how much room the metadata database occupies. SQLite
// answers with the pages it holds less the pages on its free list, so a sweep
// shrinks the sample even though the file keeps its size until an operator
// runs VACUUM; Postgres answers per relation and the total is their sum.
func (o *Operator) DatabaseSize(ctx context.Context) (DatabaseSize, error) {
	if o.s.dialect == DialectPostgres {
		return o.s.postgresDatabaseSize(ctx)
	}
	return o.s.sqliteDatabaseSize(ctx)
}

func (s *Store) sqliteDatabaseSize(ctx context.Context) (DatabaseSize, error) {
	var pages, freelist, pageSize int64
	for _, probe := range []struct {
		pragma string
		dst    *int64
	}{
		{`PRAGMA page_count`, &pages},
		{`PRAGMA freelist_count`, &freelist},
		{`PRAGMA page_size`, &pageSize},
	} {
		if err := s.queryRow(ctx, probe.pragma).Scan(probe.dst); err != nil {
			return DatabaseSize{}, fmt.Errorf("storage: %s: %w", probe.pragma, err)
		}
	}
	return DatabaseSize{
		TotalBytes: max(pages-freelist, 0) * pageSize,
		SampledAt:  time.Now().UTC(),
	}, nil
}

func (s *Store) postgresDatabaseSize(ctx context.Context) (_ DatabaseSize, err error) {
	rows, err := s.query(ctx, `
SELECT c.relname, pg_total_relation_size(c.oid)
  FROM pg_class c
  JOIN pg_namespace n ON n.oid = c.relnamespace
 WHERE c.relkind = 'r'
   AND n.nspname = ANY (current_schemas(false))
 ORDER BY 2 DESC, 1 ASC`)
	if err != nil {
		return DatabaseSize{}, fmt.Errorf("storage: relation sizes: %w", err)
	}
	defer closeRowsInto(rows, &err)
	out := DatabaseSize{SampledAt: time.Now().UTC()}
	for rows.Next() {
		var t TableSize
		if err := rows.Scan(&t.Name, &t.Bytes); err != nil {
			return DatabaseSize{}, err
		}
		out.Tables = append(out.Tables, t)
		out.TotalBytes += t.Bytes
	}
	if err := rows.Err(); err != nil {
		return DatabaseSize{}, err
	}
	return out, nil
}

// TierQuota returns the allowance a named tier grants, and false for a tier
// this build does not know.
func TierQuota(tier string) (StorageQuota, bool) {
	switch tier {
	case StorageTierFree:
		return FreeTierQuota, true
	case StorageTierPaid:
		return PaidTierQuota, true
	default:
		return StorageQuota{}, false
	}
}

// StorageMonth is the calendar month a charge is counted against. A charge
// belongs to the month it is written in, never to the month its run started,
// so a long-lived run does not spend one month's budget forever.
func StorageMonth(t time.Time) string { return t.UTC().Format("2006-01") }

// safety: the charge and the durable write it pays for share one transaction,
// so a write that fails rolls the charge back with it and a retry pays once.
func (s *Store) chargeStorageTx(
	ctx context.Context, tx *storeTx, principal, runID string, bytes, objects int64, now time.Time,
) error {
	if principal == "" || runID == "" || (bytes <= 0 && objects <= 0) {
		return nil
	}
	team, err := creditTeamForRunTx(ctx, tx, runID)
	if err != nil {
		return err
	}
	var rate int64
	var free bool
	if !creditMeteringDisabled(ctx) {
		var err error
		rate, err = creditSettingTx(ctx, tx, metaKeyStorageRateMicroPerGBDay, 0)
		if err != nil {
			return err
		}
		free = holdsFreeAllowance(team)
	}
	if free {
		if err := admitFreeEventsTx(ctx, tx, team, principal, bytes, now); err != nil {
			return err
		}
	}
	if rate > 0 && bytes > 0 {
		if err := refuseStorageGrowthOnEmptyBalanceTx(ctx, tx, team, principal, bytes); err != nil {
			return err
		}
	}
	quota, err := storageQuotaForTeamTx(ctx, tx, team)
	if err != nil {
		return err
	}
	// safety: priced storage bills every team's bytes and a signed-up team's
	// event share is counted from them, so the total is kept for all of
	// those; otherwise it is kept only where a limit reads it.
	if !free && rate <= 0 && quota.Unlimited() && quota.AllowanceBytes <= 0 {
		return nil
	}
	if err := lockStorageUsageTx(ctx, tx, principal); err != nil {
		return err
	}
	var runBytes, runObjects int64
	err = tx.QueryRowContext(ctx,
		`SELECT bytes, objects FROM storage_run_usage WHERE principal = ? AND run_id = ?`,
		principal, runID).Scan(&runBytes, &runObjects)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	month := StorageMonth(now)
	var monthBytes int64
	err = tx.QueryRowContext(ctx,
		`SELECT bytes FROM storage_month_usage WHERE principal = ? AND month = ?`,
		principal, month).Scan(&monthBytes)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if refusal := quotaRefusal(quota, runBytes, runObjects, monthBytes, bytes, objects); refusal != nil {
		return refusal
	}
	bytes, objects = max(bytes, 0), max(objects, 0)
	if _, err := tx.ExecContext(ctx, `
INSERT INTO storage_run_usage (principal, run_id, bytes, objects, updated_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT (principal, run_id) DO UPDATE SET
        bytes = storage_run_usage.bytes + excluded.bytes,
        objects = storage_run_usage.objects + excluded.objects,
        updated_at = excluded.updated_at`,
		principal, runID, bytes, objects, now.UnixNano()); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO storage_month_usage (principal, month, bytes, objects, updated_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT (principal, month) DO UPDATE SET
        bytes = storage_month_usage.bytes + excluded.bytes,
        objects = storage_month_usage.objects + excluded.objects,
        updated_at = excluded.updated_at`,
		principal, month, bytes, objects, now.UnixNano()); err != nil {
		return err
	}
	return nil
}

func quotaRefusal(quota StorageQuota, runBytes, runObjects, monthBytes, bytes, objects int64) error {
	if quota.MaxBytesPerRun > 0 && runBytes+bytes > quota.MaxBytesPerRun {
		return &StorageQuotaError{
			Principal: string(quota.Team), Limit: StorageLimitBytesPerRun, Unit: "bytes",
			Used: runBytes, Allowed: quota.MaxBytesPerRun, Requested: bytes,
		}
	}
	if quota.MaxBytesPerMonth > 0 && monthBytes+bytes > quota.MaxBytesPerMonth {
		return &StorageQuotaError{
			Principal: string(quota.Team), Limit: StorageLimitBytesPerMonth, Unit: "bytes",
			Used: monthBytes, Allowed: quota.MaxBytesPerMonth, Requested: bytes,
		}
	}
	if quota.MaxObjectsPerRun > 0 && runObjects+objects > quota.MaxObjectsPerRun {
		return &StorageQuotaError{
			Principal: string(quota.Team), Limit: StorageLimitObjectsPerRun, Unit: "objects",
			Used: runObjects, Allowed: quota.MaxObjectsPerRun, Requested: objects,
		}
	}
	return nil
}

// safety: Postgres runs concurrent writes in their own transactions, so the
// read of a team's totals and the write that grows them have to serialize on
// something; SQLite allows one writing connection and is already serial.
func lockStorageUsageTx(ctx context.Context, tx *storeTx, principal string) error {
	if tx.dialect != DialectPostgres {
		return nil
	}
	_, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext(?))`,
		"sparkwing/storage-usage/"+principal)
	return err
}

// AppendEventCharged appends an event and charges its kind and payload to
// principal's storage quota in the same transaction, so a refused event is
// never written and a written event is always paid for. A kind that fails
// [ValidateEventKind] is refused before anything is read. An empty principal charges nothing
// and appends exactly as [Store.AppendEvent] does.
func (s *Store) AppendEventCharged(
	ctx context.Context, principal, runID, nodeID, kind string, payload []byte,
) (_ int64, err error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return 0, err
	}
	defer rollbackUnlessDone(tx, &err)
	if nodeID != "" {
		if err := s.assertNodeMutationFenceInRunsTeamTx(ctx, tx, runID, nodeID); err != nil {
			return 0, err
		}
	} else if err := s.assertRunMutationFenceInRunsTeamTx(ctx, tx, runID); err != nil {
		return 0, err
	}
	if err := ValidateEventKind(kind); err != nil {
		return 0, err
	}
	size := eventBytes(kind, payload)
	if err := refuseEventOverLimitsTx(ctx, tx, principal, runID, size); err != nil {
		return 0, err
	}
	if err := s.chargeStorageTx(ctx, tx, principal, runID, size, 0, time.Now().UTC()); err != nil {
		return 0, err
	}
	seq, err := appendRunEventTx(ctx, tx, runID, nodeID, kind, payload, time.Now())
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return seq, nil
}
