package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

// safety: zero means unbounded for every one of these, so a controller that
// set none keeps exactly what it kept before they existed.
const (
	metaKeyEventRetentionDays      = "retention_event_days"
	metaKeyNodeMetricRetentionDays = "retention_node_metric_days"
	metaKeyDatabaseAlarmBytes      = "storage_alarm_database_bytes"
	metaKeyDefaultStorageTier      = "storage_default_tier"
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

// RetentionSweepBatch is how many rows one delete statement removes before
// the sweep issues the next, so a first sweep on a long-unbounded database
// does not hold one transaction over millions of rows.
const RetentionSweepBatch = 5000

// safety: a sweep that never converges must end anyway, so the loop is
// bounded and the next pass finishes what this one left.
const retentionSweepMaxBatches = 200

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
// retention windows the compaction sweep applies, the size that raises an
// alarm, and the tier a team without a quota row of its own inherits.
type StorageSettings struct {
	EventRetentionDays      int64
	NodeMetricRetentionDays int64
	DatabaseAlarmBytes      int64

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
	MaxBytesPerRun   int64
	MaxBytesPerMonth int64
	MaxObjectsPerRun int64
	// AllowanceBytes is how many retained bytes the customer asked to keep.
	// It is the ceiling the allowance sweep expires oldest-first down to and
	// the most a team is billed for, and zero keeps everything. Only
	// [Store.SetStorageAllowance] writes it; [Store.SetStorageQuota] leaves it
	// where it stands.
	AllowanceBytes int64
}

// Unlimited reports whether this quota refuses nothing.
func (q StorageQuota) Unlimited() bool {
	return q.MaxBytesPerRun <= 0 && q.MaxBytesPerMonth <= 0 && q.MaxObjectsPerRun <= 0
}

// StorageUsage is what one team has stored: the totals for one run, and the
// maintained totals for one calendar month.
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
// over a month.
type StorageTeamUsage struct {
	Principal string
	Month     string
	Bytes     int64
	Objects   int64
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

// DatabaseSize is a size sample taken off the database itself. It counts the
// pages a database actually holds, never the free pages a delete left behind,
// so an alarm clears once retention has removed the rows.
type DatabaseSize struct {
	TotalBytes int64       `json:"total_bytes"`
	Tables     []TableSize `json:"tables,omitempty"`
	SampledAt  time.Time   `json:"sampled_at"`
}

const storageQuotasTableSQLite = `CREATE TABLE IF NOT EXISTS storage_quotas (
    principal           TEXT PRIMARY KEY,
    tier                TEXT    NOT NULL DEFAULT '',
    max_bytes_per_run   INTEGER NOT NULL DEFAULT 0,
    max_bytes_per_month INTEGER NOT NULL DEFAULT 0,
    max_objects_per_run INTEGER NOT NULL DEFAULT 0,
    -- storage_allowance_bytes: retained bytes the customer asked to keep;
    -- 0 keeps everything.
    storage_allowance_bytes INTEGER NOT NULL DEFAULT 0,
    updated_at          INTEGER NOT NULL
);`

// safety: the per-run row cascades with its run, so retention that removes a
// run stops holding its bytes against the team's per-run limit forever.
const storageRunUsageTableSQLite = `CREATE TABLE IF NOT EXISTS storage_run_usage (
    principal  TEXT NOT NULL,
    run_id     TEXT NOT NULL,
    bytes      INTEGER NOT NULL DEFAULT 0,
    objects    INTEGER NOT NULL DEFAULT 0,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (principal, run_id),
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
);`

// safety: the month total is one maintained row per team, never a SUM over
// the month's runs, because the charge runs on a request path; it outlives
// the run rows deliberately, so a deleted run does not refund a spent month.
const storageMonthUsageTableSQLite = `CREATE TABLE IF NOT EXISTS storage_month_usage (
    principal  TEXT NOT NULL,
    month      TEXT NOT NULL,
    bytes      INTEGER NOT NULL DEFAULT 0,
    objects    INTEGER NOT NULL DEFAULT 0,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (principal, month)
);`

// safety: retention deletes by age across every run, and both tables lead
// their existing indexes with run_id, so without these the sweep is a scan.
const retentionIndexesSQLite = `CREATE INDEX IF NOT EXISTS idx_events_ts ON events(ts);
CREATE INDEX IF NOT EXISTS idx_node_metrics_ts ON node_metrics(ts);`

var storageTablesPostgres = func() string {
	r := strings.NewReplacer("INTEGER", "BIGINT")
	return r.Replace(storageQuotasTableSQLite) + "\n" +
		r.Replace(storageRunUsageTableSQLite) + "\n" +
		r.Replace(storageMonthUsageTableSQLite)
}()

// safety: zero means what the table already meant, keep everything, so an
// install upgrading into the column keeps exactly what it kept.
var storageQuotaAllowanceCols = map[string]string{
	"storage_allowance_bytes": "INTEGER NOT NULL DEFAULT 0",
}

func applyStorageAllowanceMigrationSQLite(ctx context.Context, tx *storeTx) error {
	if err := ensureColumnsSQLite(ctx, tx, "storage_quotas", storageQuotaAllowanceCols); err != nil {
		return err
	}
	return ensureColumnsSQLite(ctx, tx, "credit_charges", creditChargeStorageCols)
}

func applyStorageAllowanceMigrationPostgres(ctx context.Context, tx *storeTx) error {
	if err := addColumnsTx(ctx, tx, "storage_quotas", storageQuotaAllowanceCols); err != nil {
		return err
	}
	return addColumnsTx(ctx, tx, "credit_charges", creditChargeStorageCols)
}

func applyStorageMigrationSQLite(ctx context.Context, tx *storeTx) error {
	for _, stmt := range []string{
		storageQuotasTableSQLite,
		storageRunUsageTableSQLite,
		storageMonthUsageTableSQLite,
		retentionIndexesSQLite,
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

func applyStorageMigrationPostgres(ctx context.Context, tx *storeTx) error {
	if _, err := tx.ExecContext(ctx, storageTablesPostgres); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, retentionIndexesSQLite)
	return err
}

// StorageSettings returns the retention windows, alarm threshold and default
// tier this controller runs with.
func (s *Store) StorageSettings(ctx context.Context) (StorageSettings, error) {
	out := StorageSettings{}
	for _, field := range []struct {
		key string
		dst *int64
	}{
		{metaKeyEventRetentionDays, &out.EventRetentionDays},
		{metaKeyNodeMetricRetentionDays, &out.NodeMetricRetentionDays},
		{metaKeyDatabaseAlarmBytes, &out.DatabaseAlarmBytes},
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
		{metaKeyDatabaseAlarmBytes, in.DatabaseAlarmBytes},
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

// SweepRetention removes the events and per-node metric samples of finished
// runs once they are older than their retention window, in bounded batches,
// and reports how many rows each removal took. A window of zero removes
// nothing, and a run still pending or running keeps every row it has written
// however old, because its own history is what it is still writing.
func (s *Store) SweepRetention(ctx context.Context, now time.Time) (RetentionSweep, error) {
	settings, err := s.StorageSettings(ctx)
	if err != nil {
		return RetentionSweep{}, err
	}
	out := RetentionSweep{}
	if settings.EventRetentionDays > 0 {
		out.EventCutoff = retentionCutoff(now, settings.EventRetentionDays)
		n, derr := s.sweepTable(ctx, "events", out.EventCutoff)
		if derr != nil {
			return RetentionSweep{}, fmt.Errorf("storage: sweep events: %w", derr)
		}
		out.Events = n
	}
	if settings.NodeMetricRetentionDays > 0 {
		out.NodeMetricCutoff = retentionCutoff(now, settings.NodeMetricRetentionDays)
		n, derr := s.sweepTable(ctx, "node_metrics", out.NodeMetricCutoff)
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

// safety: one statement per batch keeps the write lock short, and the loop
// stops on a short batch, a cancelled context, or the batch ceiling.
func (s *Store) sweepTable(ctx context.Context, table string, cutoff time.Time) (int64, error) {
	query := fmt.Sprintf(`DELETE FROM %s WHERE %s IN (
        SELECT t.%s FROM %s t JOIN runs r ON r.id = t.run_id
         WHERE t.ts < ? AND r.%s
         LIMIT ?)`,
		table, s.rowIdentifier(), s.rowIdentifier(), table, runTerminalIn)
	var total int64
	for range retentionSweepMaxBatches {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		res, err := s.exec(ctx, query, cutoff.UnixNano(), RetentionSweepBatch)
		if err != nil {
			return total, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return total, err
		}
		total += n
		if n < RetentionSweepBatch {
			return total, nil
		}
	}
	return total, nil
}

// safety: SQLite names a row's implicit key rowid and Postgres names it ctid;
// a batched delete needs one of them to bound the statement.
func (s *Store) rowIdentifier() string {
	if s.dialect == DialectPostgres {
		return "ctid"
	}
	return "rowid"
}

// DatabaseSize samples how much room the metadata database occupies. SQLite
// answers with the pages it holds less the pages on its free list, so a sweep
// shrinks the sample even though the file keeps its size until an operator
// runs VACUUM; Postgres answers per relation and the total is their sum.
func (s *Store) DatabaseSize(ctx context.Context) (DatabaseSize, error) {
	if s.dialect == DialectPostgres {
		return s.postgresDatabaseSize(ctx)
	}
	return s.sqliteDatabaseSize(ctx)
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

// StoragePrincipalMaxLen is the longest principal a quota row may be filed
// under. It is a storage bound, not a naming rule.
const StoragePrincipalMaxLen = 256

// ValidStoragePrincipal reports whether a name may key a quota row. A token
// principal is a free-form label, so this refuses only what cannot be a
// principal at all: nothing and something longer than any token carries.
// Narrowing it further would leave an existing team unable to hold a quota
// while a default tier still bound it.
func ValidStoragePrincipal(name string) bool {
	return name != "" && len(name) <= StoragePrincipalMaxLen
}

// SetStorageQuota writes one team's allowance. A quota naming a known tier and
// leaving every limit zero takes that tier's limits, so an operator moves a
// team between tiers without restating three numbers.
func (s *Store) SetStorageQuota(ctx context.Context, q StorageQuota) error {
	if !ValidStoragePrincipal(q.Principal) {
		return fmt.Errorf("storage: %q is not a usable principal name", q.Principal)
	}
	if q.Tier != "" {
		tier, ok := TierQuota(q.Tier)
		if !ok {
			return fmt.Errorf("storage: unknown tier %q", q.Tier)
		}
		if q.Unlimited() {
			tier.Principal = q.Principal
			q = tier
		}
	}
	for _, v := range []int64{q.MaxBytesPerRun, q.MaxBytesPerMonth, q.MaxObjectsPerRun} {
		if v < 0 {
			return errors.New("storage: quota limits must not be negative")
		}
	}
	// safety: the allowance has one writer, SetStorageAllowance, so a quota
	// rewrite that says nothing about it cannot silently drop what a team
	// asked to keep.
	_, err := s.exec(ctx, `
INSERT INTO storage_quotas (principal, tier, max_bytes_per_run,
        max_bytes_per_month, max_objects_per_run, updated_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT (principal) DO UPDATE SET
        tier = excluded.tier,
        max_bytes_per_run = excluded.max_bytes_per_run,
        max_bytes_per_month = excluded.max_bytes_per_month,
        max_objects_per_run = excluded.max_objects_per_run,
        updated_at = excluded.updated_at`,
		q.Principal, q.Tier, q.MaxBytesPerRun,
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
SELECT tier, max_bytes_per_run, max_bytes_per_month, max_objects_per_run, storage_allowance_bytes
  FROM storage_quotas WHERE principal = ?`, principal).
		Scan(&q.Tier, &q.MaxBytesPerRun, &q.MaxBytesPerMonth, &q.MaxObjectsPerRun, &q.AllowanceBytes)
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
SELECT principal, tier, max_bytes_per_run, max_bytes_per_month, max_objects_per_run,
        storage_allowance_bytes
  FROM storage_quotas ORDER BY principal ASC`)
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	out := []StorageQuota{}
	for rows.Next() {
		var q StorageQuota
		if err := rows.Scan(&q.Principal, &q.Tier,
			&q.MaxBytesPerRun, &q.MaxBytesPerMonth, &q.MaxObjectsPerRun, &q.AllowanceBytes); err != nil {
			return nil, err
		}
		out = append(out, q)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// StorageMonth is the calendar month a charge is counted against. A charge
// belongs to the month it is written in, never to the month its run started,
// so a long-lived run does not spend one month's budget forever.
func StorageMonth(t time.Time) string { return t.UTC().Format("2006-01") }

// StorageUsageFor reports what a team has stored against one run and over one
// calendar month. An empty run id reports the month alone.
func (s *Store) StorageUsageFor(ctx context.Context, principal, runID, month string) (StorageUsage, error) {
	out := StorageUsage{Principal: principal, RunID: runID, Month: month}
	if runID != "" {
		err := s.queryRow(ctx,
			`SELECT bytes, objects FROM storage_run_usage WHERE principal = ? AND run_id = ?`,
			principal, runID).Scan(&out.RunBytes, &out.RunObjects)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return StorageUsage{}, err
		}
	}
	err := s.queryRow(ctx,
		`SELECT bytes, objects FROM storage_month_usage WHERE principal = ? AND month = ?`,
		principal, month).Scan(&out.MonthBytes, &out.MonthObjects)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return StorageUsage{}, err
	}
	return out, nil
}

// safety: the charge and the durable write it pays for share one transaction,
// so a write that fails rolls the charge back with it and a retry pays once.
func (s *Store) chargeStorageTx(
	ctx context.Context, tx *storeTx, principal, runID string, bytes, objects int64, now time.Time,
) error {
	if principal == "" || runID == "" || (bytes <= 0 && objects <= 0) {
		return nil
	}
	rate, err := creditSettingTx(ctx, tx, metaKeyStorageRateMicroPerGBDay, 0)
	if err != nil {
		return err
	}
	if rate > 0 && bytes > 0 {
		team, err := creditTeamForRunTx(ctx, tx, runID)
		if err != nil {
			return err
		}
		if err := refuseStorageGrowthOnEmptyBalanceTx(ctx, tx, team, principal, bytes); err != nil {
			return err
		}
	}
	quota, err := storageQuotaForTx(ctx, tx, principal)
	if err != nil {
		return err
	}
	// safety: priced storage bills every team's bytes, so the total is kept
	// for all of them; unpriced storage keeps it only where a limit reads it.
	if rate <= 0 && quota.Unlimited() && quota.AllowanceBytes <= 0 {
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
	_, err = tx.ExecContext(ctx, `
INSERT INTO storage_month_usage (principal, month, bytes, objects, updated_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT (principal, month) DO UPDATE SET
        bytes = storage_month_usage.bytes + excluded.bytes,
        objects = storage_month_usage.objects + excluded.objects,
        updated_at = excluded.updated_at`,
		principal, month, bytes, objects, now.UnixNano())
	return err
}

func storageQuotaForTx(ctx context.Context, tx *storeTx, principal string) (StorageQuota, error) {
	q := StorageQuota{Principal: principal}
	err := tx.QueryRowContext(ctx, `
SELECT tier, max_bytes_per_run, max_bytes_per_month, max_objects_per_run, storage_allowance_bytes
  FROM storage_quotas WHERE principal = ?`, principal).
		Scan(&q.Tier, &q.MaxBytesPerRun, &q.MaxBytesPerMonth, &q.MaxObjectsPerRun, &q.AllowanceBytes)
	if err == nil {
		return q, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return StorageQuota{}, err
	}
	var tier string
	err = tx.QueryRowContext(ctx,
		`SELECT value FROM sparkwing_meta WHERE key = ?`, metaKeyDefaultStorageTier).Scan(&tier)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return StorageQuota{}, err
	}
	if def, ok := TierQuota(strings.TrimSpace(tier)); ok {
		def.Principal = principal
		return def, nil
	}
	return StorageQuota{Principal: principal}, nil
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

// AppendEventCharged appends an event and charges its payload to principal's
// storage quota in the same transaction, so a refused event is never written
// and a written event is always paid for. An empty principal charges nothing
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
		if err := s.assertNodeMutationFenceTx(ctx, tx, runID, nodeID); err != nil {
			return 0, err
		}
	} else if err := s.assertRunMutationFenceInRunsTeamTx(ctx, tx, runID); err != nil {
		return 0, err
	}
	if err := refuseEventOverLimitsTx(ctx, tx, principal, runID, int64(len(payload))); err != nil {
		return 0, err
	}
	if err := s.chargeStorageTx(ctx, tx, principal, runID, int64(len(payload)), 0, time.Now().UTC()); err != nil {
		return 0, err
	}
	seq, err := appendEventTx(ctx, tx, runID, nodeID, kind, payload, time.Now())
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return seq, nil
}

// SetNodeArtifactManifestCharged records a node's artifact manifest and
// charges one object against principal's quota in the same transaction, so a
// run cannot publish more manifests than its team is allowed. An empty
// principal charges nothing.
func (s *Store) SetNodeArtifactManifestCharged(
	ctx context.Context, principal, runID, nodeID, manifestDigest string,
) (err error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackUnlessDone(tx, &err)
	if err := s.assertNodeMutationFenceTx(ctx, tx, runID, nodeID); err != nil {
		return err
	}
	if err := s.chargeStorageTx(ctx, tx, principal, runID, 0, 1, time.Now().UTC()); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE nodes SET artifact_manifest = ? WHERE run_id = ? AND node_id = ?`,
		manifestDigest, runID, nodeID)
	if err != nil {
		return err
	}
	if err := fencedRows(res, hasClaimFence(ctx)); err != nil {
		return err
	}
	return tx.Commit()
}

// TopStorageTeams reports the teams that stored the most over a month,
// largest first, which is the report an operator reads to find the teams
// worth talking to.
func (s *Store) TopStorageTeams(ctx context.Context, month string, limit int) (_ []StorageTeamUsage, err error) {
	if limit <= 0 {
		limit = 10
	}
	rows, err := s.query(ctx, `
SELECT principal, bytes, objects
  FROM storage_month_usage WHERE month = ?
 ORDER BY bytes DESC, principal ASC
 LIMIT ?`, month, limit)
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	out := []StorageTeamUsage{}
	for rows.Next() {
		u := StorageTeamUsage{Month: month}
		if err := rows.Scan(&u.Principal, &u.Bytes, &u.Objects); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
