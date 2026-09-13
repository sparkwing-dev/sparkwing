package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"
)

// safety: zero means unbounded for every one of these, so a controller that
// set none keeps exactly what it kept before they existed.
const (
	metaKeyEventRetentionDays      = "retention_event_days"
	metaKeyNodeMetricRetentionDays = "retention_node_metric_days"
	metaKeyBackupRetentionDays     = "retention_backup_days"
	metaKeyDatabaseAlarmBytes      = "storage_alarm_database_bytes"
	metaKeyBackupAlarmBytes        = "storage_alarm_backup_bytes"
	metaKeyDefaultStorageTier      = "storage_default_tier"
)

// Retention windows a cloud-provisioned controller sets. They are the values
// provisioning writes rather than defaults any controller reads: an install
// that sets nothing retains everything.
const (
	CloudEventRetentionDays      = 30
	CloudNodeMetricRetentionDays = 14
	CloudBackupRetentionDays     = 30
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

// FreeTierQuota is the free tier's storage allowance: fourteen days of
// retention and ten megabytes per run, which keeps an active five-person team
// under a gigabyte without a lifetime cap.
var FreeTierQuota = StorageQuota{
	Tier:             StorageTierFree,
	RetentionDays:    14,
	MaxBytesPerRun:   10 << 20,
	MaxBytesPerMonth: 1 << 30,
	MaxObjectsPerRun: 1000,
}

// PaidTierQuota is the paid tier's storage allowance.
var PaidTierQuota = StorageQuota{
	Tier:             StorageTierPaid,
	RetentionDays:    90,
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
}

func (e *StorageQuotaError) Error() string {
	return fmt.Sprintf("storage quota exceeded: %s for team %s: %d of %d %s used and this write adds %d",
		e.Limit, e.Principal, e.Used, e.Allowed, e.Unit, e.Requested)
}

// Unwrap reports [ErrStorageQuota], so a caller matches the condition without
// knowing this type.
func (e *StorageQuotaError) Unwrap() error { return ErrStorageQuota }

// StorageSettings is the controller-wide half of storage policy: the
// retention windows the compaction sweep applies, the sizes that raise an
// alarm, and the tier a team without a quota row of its own inherits.
type StorageSettings struct {
	EventRetentionDays      int64
	NodeMetricRetentionDays int64
	BackupRetentionDays     int64
	DatabaseAlarmBytes      int64
	BackupAlarmBytes        int64

	// DefaultTier names the tier a principal with no quota row is held to.
	// Empty leaves every such principal unlimited, which is what an install
	// that never enabled quotas reads.
	DefaultTier string
}

// RetentionOn reports whether any retention window is set, which is what says
// the compaction sweep has work to do.
func (s StorageSettings) RetentionOn() bool {
	return s.EventRetentionDays > 0 || s.NodeMetricRetentionDays > 0
}

// StorageQuota is one team's allowance. A zero limit is unbounded, so a quota
// may cap bytes without capping objects.
type StorageQuota struct {
	Principal        string
	Tier             string
	RetentionDays    int64
	MaxBytesPerRun   int64
	MaxBytesPerMonth int64
	MaxObjectsPerRun int64
}

// Unlimited reports whether this quota refuses nothing.
func (q StorageQuota) Unlimited() bool {
	return q.MaxBytesPerRun <= 0 && q.MaxBytesPerMonth <= 0 && q.MaxObjectsPerRun <= 0
}

// StorageUsage is what one team has stored: the totals for one run, and the
// totals across every run of the calendar month that run belongs to.
type StorageUsage struct {
	Principal    string
	RunID        string
	Month        string
	RunBytes     int64
	RunObjects   int64
	MonthBytes   int64
	MonthObjects int64
}

// StorageTeamUsage is one row of the largest-teams report: what a team stored
// over a month, and how many runs it spread that over.
type StorageTeamUsage struct {
	Principal string
	Month     string
	Bytes     int64
	Objects   int64
	Runs      int64
}

// RetentionSweep reports what one compaction pass removed and the cutoffs it
// removed against.
type RetentionSweep struct {
	Events           int64
	NodeMetrics      int64
	EventCutoff      time.Time
	NodeMetricCutoff time.Time
}

// TableSize is one table's total size on disk, indexes and toast included.
type TableSize struct {
	Name  string `json:"name"`
	Bytes int64  `json:"bytes"`
}

// DatabaseSize is a size sample taken off the database itself: the whole file
// on SQLite, and the sum of every relation on Postgres, where the per-table
// breakdown is also available.
type DatabaseSize struct {
	TotalBytes int64       `json:"total_bytes"`
	Tables     []TableSize `json:"tables,omitempty"`
	SampledAt  time.Time   `json:"sampled_at"`
}

const storageQuotasTableSQLite = `CREATE TABLE IF NOT EXISTS storage_quotas (
    principal           TEXT PRIMARY KEY,
    tier                TEXT    NOT NULL DEFAULT '',
    retention_days      INTEGER NOT NULL DEFAULT 0,
    max_bytes_per_run   INTEGER NOT NULL DEFAULT 0,
    max_bytes_per_month INTEGER NOT NULL DEFAULT 0,
    max_objects_per_run INTEGER NOT NULL DEFAULT 0,
    updated_at          INTEGER NOT NULL
);`

// safety: no foreign key to runs, because a month's total has to survive the
// run rows retention removes and a cascade would take it with them.
const storageUsageTableSQLite = `CREATE TABLE IF NOT EXISTS storage_usage (
    principal  TEXT NOT NULL,
    run_id     TEXT NOT NULL,
    month      TEXT NOT NULL,
    bytes      INTEGER NOT NULL DEFAULT 0,
    objects    INTEGER NOT NULL DEFAULT 0,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (principal, run_id)
);
CREATE INDEX IF NOT EXISTS idx_storage_usage_month ON storage_usage(principal, month);`

var storageTablesPostgres = func() string {
	r := strings.NewReplacer("INTEGER", "BIGINT")
	return r.Replace(storageQuotasTableSQLite) + "\n" + r.Replace(storageUsageTableSQLite)
}()

func applyStorageMigrationSQLite(ctx context.Context, tx *storeTx) error {
	if _, err := tx.ExecContext(ctx, storageQuotasTableSQLite); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, storageUsageTableSQLite)
	return err
}

func applyStorageMigrationPostgres(ctx context.Context, tx *storeTx) error {
	_, err := tx.ExecContext(ctx, storageTablesPostgres)
	return err
}

// StorageSettings returns the retention windows, alarm thresholds and default
// tier this controller runs with.
func (s *Store) StorageSettings(ctx context.Context) (StorageSettings, error) {
	out := StorageSettings{}
	for _, field := range []struct {
		key string
		dst *int64
	}{
		{metaKeyEventRetentionDays, &out.EventRetentionDays},
		{metaKeyNodeMetricRetentionDays, &out.NodeMetricRetentionDays},
		{metaKeyBackupRetentionDays, &out.BackupRetentionDays},
		{metaKeyDatabaseAlarmBytes, &out.DatabaseAlarmBytes},
		{metaKeyBackupAlarmBytes, &out.BackupAlarmBytes},
	} {
		v, err := s.storageSetting(ctx, field.key)
		if err != nil {
			return StorageSettings{}, err
		}
		*field.dst = v
	}
	tier, err := s.storageText(ctx, metaKeyDefaultStorageTier)
	if err != nil {
		return StorageSettings{}, err
	}
	out.DefaultTier = tier
	return out, nil
}

// SetStorageSettings writes every field, so a caller reads the current
// settings, changes what it means to change, and writes them back.
func (s *Store) SetStorageSettings(ctx context.Context, in StorageSettings) error {
	if in.DefaultTier != "" {
		if _, ok := TierQuota(in.DefaultTier); !ok {
			return fmt.Errorf("storage: unknown tier %q", in.DefaultTier)
		}
	}
	for _, field := range []struct {
		key   string
		value int64
	}{
		{metaKeyEventRetentionDays, in.EventRetentionDays},
		{metaKeyNodeMetricRetentionDays, in.NodeMetricRetentionDays},
		{metaKeyBackupRetentionDays, in.BackupRetentionDays},
		{metaKeyDatabaseAlarmBytes, in.DatabaseAlarmBytes},
		{metaKeyBackupAlarmBytes, in.BackupAlarmBytes},
	} {
		if field.value < 0 {
			return fmt.Errorf("storage: %s must not be negative", field.key)
		}
		if err := s.setStorageMeta(ctx, field.key, strconv.FormatInt(field.value, 10)); err != nil {
			return err
		}
	}
	return s.setStorageMeta(ctx, metaKeyDefaultStorageTier, in.DefaultTier)
}

func (s *Store) storageSetting(ctx context.Context, key string) (int64, error) {
	raw, err := s.storageText(ctx, key)
	if err != nil || raw == "" {
		return 0, err
	}
	v, perr := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if perr != nil {
		// safety: an unparsable settings row must not break a read, so the
		// unbounded default stands and the stored value is named in the log.
		slog.Warn("storage: a stored setting is not an integer; treating it as unbounded",
			"key", key, "value", raw, "err", perr)
		return 0, nil
	}
	return v, nil
}

func (s *Store) storageText(ctx context.Context, key string) (string, error) {
	var raw string
	err := s.queryRow(ctx, `SELECT value FROM sparkwing_meta WHERE key = ?`, key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(raw), nil
}

func (s *Store) setStorageMeta(ctx context.Context, key, value string) error {
	_, err := s.exec(ctx,
		`INSERT INTO sparkwing_meta (key, value, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, value, time.Now().UnixNano())
	return err
}

// SweepRetention removes the events and per-node metric samples older than
// their retention windows and reports how many rows each removal took. A
// window of zero removes nothing, so a controller that set no retention is
// swept without losing a row.
func (s *Store) SweepRetention(ctx context.Context, now time.Time) (RetentionSweep, error) {
	settings, err := s.StorageSettings(ctx)
	if err != nil {
		return RetentionSweep{}, err
	}
	out := RetentionSweep{}
	if settings.EventRetentionDays > 0 {
		out.EventCutoff = retentionCutoff(now, settings.EventRetentionDays)
		n, derr := s.deleteOlderThan(ctx, `DELETE FROM events WHERE ts < ?`, out.EventCutoff)
		if derr != nil {
			return RetentionSweep{}, fmt.Errorf("storage: sweep events: %w", derr)
		}
		out.Events = n
	}
	if settings.NodeMetricRetentionDays > 0 {
		out.NodeMetricCutoff = retentionCutoff(now, settings.NodeMetricRetentionDays)
		n, derr := s.deleteOlderThan(ctx, `DELETE FROM node_metrics WHERE ts < ?`, out.NodeMetricCutoff)
		if derr != nil {
			return RetentionSweep{}, fmt.Errorf("storage: sweep node metrics: %w", derr)
		}
		out.NodeMetrics = n
	}
	return out, nil
}

func retentionCutoff(now time.Time, days int64) time.Time {
	return now.Add(-time.Duration(days) * 24 * time.Hour)
}

func (s *Store) deleteOlderThan(ctx context.Context, query string, cutoff time.Time) (int64, error) {
	res, err := s.exec(ctx, query, cutoff.UnixNano())
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return n, nil
}

// DatabaseSize samples how much room the metadata database occupies. SQLite
// answers with its page count, which covers the whole file and no table on
// its own; Postgres answers per relation and the total is their sum.
func (s *Store) DatabaseSize(ctx context.Context) (DatabaseSize, error) {
	if s.dialect == DialectPostgres {
		return s.postgresDatabaseSize(ctx)
	}
	return s.sqliteDatabaseSize(ctx)
}

func (s *Store) sqliteDatabaseSize(ctx context.Context) (DatabaseSize, error) {
	var pages, pageSize int64
	if err := s.queryRow(ctx, `PRAGMA page_count`).Scan(&pages); err != nil {
		return DatabaseSize{}, fmt.Errorf("storage: page count: %w", err)
	}
	if err := s.queryRow(ctx, `PRAGMA page_size`).Scan(&pageSize); err != nil {
		return DatabaseSize{}, fmt.Errorf("storage: page size: %w", err)
	}
	return DatabaseSize{TotalBytes: pages * pageSize, SampledAt: time.Now().UTC()}, nil
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

// SetStorageQuota writes one team's allowance. A quota naming a known tier and
// leaving every limit zero takes that tier's limits, so an operator moves a
// team between tiers without restating four numbers.
func (s *Store) SetStorageQuota(ctx context.Context, q StorageQuota) error {
	if q.Principal == "" {
		return errors.New("storage: quota principal required")
	}
	if q.Tier != "" {
		tier, ok := TierQuota(q.Tier)
		if !ok {
			return fmt.Errorf("storage: unknown tier %q", q.Tier)
		}
		if q.RetentionDays == 0 && q.Unlimited() {
			tier.Principal = q.Principal
			q = tier
		}
	}
	for _, v := range []int64{q.RetentionDays, q.MaxBytesPerRun, q.MaxBytesPerMonth, q.MaxObjectsPerRun} {
		if v < 0 {
			return errors.New("storage: quota limits must not be negative")
		}
	}
	_, err := s.exec(ctx, `
INSERT INTO storage_quotas (principal, tier, retention_days, max_bytes_per_run,
        max_bytes_per_month, max_objects_per_run, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (principal) DO UPDATE SET
        tier = excluded.tier,
        retention_days = excluded.retention_days,
        max_bytes_per_run = excluded.max_bytes_per_run,
        max_bytes_per_month = excluded.max_bytes_per_month,
        max_objects_per_run = excluded.max_objects_per_run,
        updated_at = excluded.updated_at`,
		q.Principal, q.Tier, q.RetentionDays, q.MaxBytesPerRun,
		q.MaxBytesPerMonth, q.MaxObjectsPerRun, time.Now().UnixNano())
	return err
}

// StorageQuotaFor returns the allowance a principal is held to: its own row
// when it has one, the default tier's limits when the controller set one, and
// an unlimited quota otherwise.
func (s *Store) StorageQuotaFor(ctx context.Context, principal string) (StorageQuota, error) {
	q, found, err := s.storageQuotaRow(ctx, principal)
	if err != nil || found {
		return q, err
	}
	tier, err := s.storageText(ctx, metaKeyDefaultStorageTier)
	if err != nil {
		return StorageQuota{}, err
	}
	if def, ok := TierQuota(tier); ok {
		def.Principal = principal
		return def, nil
	}
	return StorageQuota{Principal: principal}, nil
}

func (s *Store) storageQuotaRow(ctx context.Context, principal string) (StorageQuota, bool, error) {
	q := StorageQuota{Principal: principal}
	err := s.queryRow(ctx, `
SELECT tier, retention_days, max_bytes_per_run, max_bytes_per_month, max_objects_per_run
  FROM storage_quotas WHERE principal = ?`, principal).
		Scan(&q.Tier, &q.RetentionDays, &q.MaxBytesPerRun, &q.MaxBytesPerMonth, &q.MaxObjectsPerRun)
	if errors.Is(err, sql.ErrNoRows) {
		return StorageQuota{Principal: principal}, false, nil
	}
	if err != nil {
		return StorageQuota{}, false, err
	}
	return q, true, nil
}

// ListStorageQuotas returns every team with a quota row, by principal.
func (s *Store) ListStorageQuotas(ctx context.Context) (_ []StorageQuota, err error) {
	rows, err := s.query(ctx, `
SELECT principal, tier, retention_days, max_bytes_per_run, max_bytes_per_month, max_objects_per_run
  FROM storage_quotas ORDER BY principal ASC`)
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	out := []StorageQuota{}
	for rows.Next() {
		var q StorageQuota
		if err := rows.Scan(&q.Principal, &q.Tier, &q.RetentionDays,
			&q.MaxBytesPerRun, &q.MaxBytesPerMonth, &q.MaxObjectsPerRun); err != nil {
			return nil, err
		}
		out = append(out, q)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// StorageMonth is the calendar month a usage row is counted against.
func StorageMonth(t time.Time) string { return t.UTC().Format("2006-01") }

// StorageUsageFor reports what a team has stored against one run and across
// the calendar month that run's usage row belongs to.
func (s *Store) StorageUsageFor(ctx context.Context, principal, runID string, now time.Time) (StorageUsage, error) {
	month := StorageMonth(now)
	out := StorageUsage{Principal: principal, RunID: runID, Month: month}
	var stored string
	err := s.queryRow(ctx,
		`SELECT bytes, objects, month FROM storage_usage WHERE principal = ? AND run_id = ?`,
		principal, runID).Scan(&out.RunBytes, &out.RunObjects, &stored)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return StorageUsage{}, err
	default:
		out.Month = stored
	}
	var bytes, objects sql.NullInt64
	if err := s.queryRow(ctx,
		`SELECT SUM(bytes), SUM(objects) FROM storage_usage WHERE principal = ? AND month = ?`,
		principal, out.Month).Scan(&bytes, &objects); err != nil {
		return StorageUsage{}, err
	}
	out.MonthBytes, out.MonthObjects = bytes.Int64, objects.Int64
	return out, nil
}

// ReserveStorage counts bytes and objects a team is about to store against
// its quota and refuses the write with a [StorageQuotaError] when any limit
// would be passed. A team with no limits is not counted at all, so a
// controller that never enabled quotas writes no usage rows.
func (s *Store) ReserveStorage(
	ctx context.Context, principal, runID string, bytes, objects int64, now time.Time,
) (err error) {
	if principal == "" || runID == "" || (bytes <= 0 && objects <= 0) {
		return nil
	}
	quota, err := s.StorageQuotaFor(ctx, principal)
	if err != nil {
		return err
	}
	if quota.Unlimited() {
		return nil
	}
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackUnlessDone(tx, &err)
	if err := lockStorageUsageTx(ctx, tx, principal); err != nil {
		return err
	}
	month := StorageMonth(now)
	var runBytes, runObjects int64
	var stored string
	err = tx.QueryRowContext(ctx,
		`SELECT bytes, objects, month FROM storage_usage WHERE principal = ? AND run_id = ?`,
		principal, runID).Scan(&runBytes, &runObjects, &stored)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return err
	default:
		month = stored
	}
	var monthBytes sql.NullInt64
	if err := tx.QueryRowContext(ctx,
		`SELECT SUM(bytes) FROM storage_usage WHERE principal = ? AND month = ?`,
		principal, month).Scan(&monthBytes); err != nil {
		return err
	}
	if refusal := quotaRefusal(quota, runBytes, runObjects, monthBytes.Int64, bytes, objects); refusal != nil {
		return refusal
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO storage_usage (principal, run_id, month, bytes, objects, updated_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT (principal, run_id) DO UPDATE SET
        bytes = storage_usage.bytes + excluded.bytes,
        objects = storage_usage.objects + excluded.objects,
        updated_at = excluded.updated_at`,
		principal, runID, month, max(bytes, 0), max(objects, 0), now.UnixNano()); err != nil {
		return err
	}
	return tx.Commit()
}

func quotaRefusal(quota StorageQuota, runBytes, runObjects, monthBytes, bytes, objects int64) error {
	if quota.MaxBytesPerRun > 0 && runBytes+bytes > quota.MaxBytesPerRun {
		return &StorageQuotaError{
			Principal: quota.Principal, Limit: StorageLimitBytesPerRun, Unit: "bytes",
			Used: runBytes, Allowed: quota.MaxBytesPerRun, Requested: bytes,
		}
	}
	if quota.MaxBytesPerMonth > 0 && monthBytes+bytes > quota.MaxBytesPerMonth {
		return &StorageQuotaError{
			Principal: quota.Principal, Limit: StorageLimitBytesPerMonth, Unit: "bytes",
			Used: monthBytes, Allowed: quota.MaxBytesPerMonth, Requested: bytes,
		}
	}
	if quota.MaxObjectsPerRun > 0 && runObjects+objects > quota.MaxObjectsPerRun {
		return &StorageQuotaError{
			Principal: quota.Principal, Limit: StorageLimitObjectsPerRun, Unit: "objects",
			Used: runObjects, Allowed: quota.MaxObjectsPerRun, Requested: objects,
		}
	}
	return nil
}

// safety: Postgres runs concurrent writes in their own transactions, so the
// read of a team's month total and the write that grows it have to serialize
// on something; SQLite allows one writing connection and is already serial.
func lockStorageUsageTx(ctx context.Context, tx *storeTx, principal string) error {
	if tx.dialect != DialectPostgres {
		return nil
	}
	_, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext(?))`,
		"sparkwing/storage-usage/"+principal)
	return err
}

// TopStorageTeams reports the teams that stored the most over a month,
// largest first, which is the report an operator reads to find the teams
// worth talking to.
func (s *Store) TopStorageTeams(ctx context.Context, month string, limit int) (_ []StorageTeamUsage, err error) {
	if limit <= 0 {
		limit = 10
	}
	rows, err := s.query(ctx, `
SELECT principal, SUM(bytes), SUM(objects), COUNT(*)
  FROM storage_usage WHERE month = ?
 GROUP BY principal
 ORDER BY 2 DESC, 1 ASC
 LIMIT ?`, month, limit)
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	out := []StorageTeamUsage{}
	for rows.Next() {
		u := StorageTeamUsage{Month: month}
		if err := rows.Scan(&u.Principal, &u.Bytes, &u.Objects, &u.Runs); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Bytes > out[j].Bytes })
	return out, nil
}
