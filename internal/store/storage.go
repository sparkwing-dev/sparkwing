package store

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Storage tiers a team's quota can be drawn from.
const (
	storageTierFree = "free"
	storageTierPaid = "paid"
)

// Quota limits, named so a refusal says which one it hit.
const (
	storageLimitBytesPerRun   = "bytes_per_run"
	storageLimitBytesPerMonth = "bytes_per_month"
	storageLimitObjectsPerRun = "objects_per_run"
)

// freeTierQuota is the free tier's storage allowance: ten megabytes per run,
// which keeps an active five-person team under a gigabyte without a lifetime
// cap.
var freeTierQuota = StorageQuota{
	Tier:             storageTierFree,
	MaxBytesPerRun:   10 << 20,
	MaxBytesPerMonth: 1 << 30,
	MaxObjectsPerRun: 1000,
}

// paidTierQuota is the paid tier's storage allowance.
var paidTierQuota = StorageQuota{
	Tier:             storageTierPaid,
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
	AuditRetentionDays      int64
	RunRetentionDays        int64
	NodeMetricRetentionDays int64
	DatabaseAlarmBytes      int64

	// DefaultTier applies only to teams without an explicit quota row.
	DefaultTier string
}

// RetentionOn reports whether audit, run or metric expiration is enabled.
func (s StorageSettings) RetentionOn() bool {
	return s.AuditRetentionDays > 0 || s.RunRetentionDays > 0 || s.NodeMetricRetentionDays > 0
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

// unlimited reports whether this quota refuses nothing.
func (q StorageQuota) unlimited() bool {
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
	case storageTierFree:
		return freeTierQuota, true
	case storageTierPaid:
		return paidTierQuota, true
	default:
		return StorageQuota{}, false
	}
}

// StorageMonth is the calendar month a charge is counted against. A charge
// belongs to the month it is written in, never to the month its run started,
// so a long-lived run does not spend one month's budget forever.
func StorageMonth(t time.Time) string { return t.UTC().Format("2006-01") }
