// Package store persists pipeline-run state to a SQL database. SQLite
// (modernc.org/sqlite) and Postgres (jackc/pgx via the stdlib driver)
// are both supported behind the same *Store type; the dialect is
// chosen at Open time.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

// Failure reason codes; empty = no structured reason.
const (
	FailureUnknown            = ""
	FailureOOMKilled          = "oom_killed"
	FailureAgentLost          = "agent_lost"
	FailureTimeout            = "timeout"
	FailureQueueTimeout       = "queue_timeout"
	FailureRunnerLeaseExpired = "runner_lease_expired"
	// FailureLogsAuth: the runner's logs.append calls returned 401/403
	// against the controller's auth surface. The run's structured
	// logs are unrecoverable; better to fail loud than report
	// status=success with no observable output.
	FailureLogsAuth = "logs_auth"
)

// RetrySource values for runs.retry_source.
const (
	RetrySourceManual = "manual"
	RetrySourceAuto   = "auto"
)

// Store is the persistent state layer. One instance per process; safe
// for concurrent use by multiple orchestrator goroutines. The
// underlying database is SQLite or Postgres depending on which
// constructor opened it; dialect-aware methods branch on s.dialect.
type Store struct {
	db      *sql.DB
	dialect Dialect
}

// Dialect reports the SQL dialect this Store was opened against.
// Useful for callers (tests, diagnostics) that need to know which
// backend they're talking to; query methods on Store handle the
// dialect difference internally.
func (s *Store) Dialect() Dialect { return s.dialect }

// Open initializes a SQLite database at path with WAL + foreign keys.
func Open(path string) (*Store, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=foreign_keys(on)&_pragma=busy_timeout(5000)", path)
	return openSQL("sqlite", dsn, DialectSQLite)
}

// OpenPostgres initializes a Store against the Postgres database
// identified by dsn (`postgres://user:pass@host:port/db?sslmode=...`).
// Migrations run on first connect; concurrent OpenPostgres calls
// against the same fresh database are coordinated by a transactional
// advisory lock so exactly one runner applies the schema.
func OpenPostgres(_ context.Context, dsn string) (*Store, error) {
	if dsn == "" {
		return nil, errors.New("OpenPostgres: dsn is required")
	}
	return openSQL("pgx", dsn, DialectPostgres)
}

// openSQL is the shared constructor for both dialects. Pool sizing
// differs: SQLite is single-writer by construction; Postgres uses a
// modest connection pool to absorb orchestrator concurrency without
// overwhelming the server. Migration runs immediately after Open so
// callers observe a fully provisioned schema on success.
func openSQL(driver, dsn string, dialect Dialect) (*Store, error) {
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, err
	}
	switch dialect {
	case DialectSQLite:
		// SQLite serializes writes; an explicit single-connection
		// avoids the "database is locked" failure mode under load.
		db.SetMaxOpenConns(1)
	case DialectPostgres:
		db.SetMaxOpenConns(25)
		db.SetMaxIdleConns(5)
		db.SetConnMaxIdleTime(5 * time.Minute)
	}

	s := &Store{db: db, dialect: dialect}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the underlying database handle.
func (s *Store) Close() error { return s.db.Close() }

// DB returns the underlying handle for read-side aggregations only.
func (s *Store) DB() *sql.DB { return s.db }

const schemaSQLite = `
CREATE TABLE IF NOT EXISTS runs (
    id              TEXT PRIMARY KEY,
    pipeline        TEXT NOT NULL,
    status          TEXT NOT NULL,
    trigger_source  TEXT NOT NULL DEFAULT '',
    git_branch      TEXT NOT NULL DEFAULT '',
    git_sha         TEXT NOT NULL DEFAULT '',
    args_json       BLOB,
    plan_json       BLOB,
    error           TEXT NOT NULL DEFAULT '',
    -- created_at: when the controller first saw the trigger; matches
    -- triggers.created_at for trigger-originated runs. Lets pre-claim
    -- "pending" runs have a wall-clock anchor
    -- distinct from started_at (which becomes non-NULL only when the
    -- orchestrator actually starts executing).
    created_at      INTEGER NOT NULL DEFAULT 0,
    started_at      INTEGER NOT NULL,
    finished_at     INTEGER,
    repo            TEXT NOT NULL DEFAULT '',
    repo_url        TEXT NOT NULL DEFAULT '',
    github_owner    TEXT NOT NULL DEFAULT '',
    github_repo     TEXT NOT NULL DEFAULT '',
    -- retry_of: source run; retried_as: newest retry pointer.
    retry_of        TEXT NOT NULL DEFAULT '',
    retried_as      TEXT NOT NULL DEFAULT '',
    -- retry_source: 'manual' (operator) or 'auto' (AutoRetry modifier).
    retry_source    TEXT NOT NULL DEFAULT '',
    -- replay_of_*: single-node replay lineage.
    replay_of_run_id  TEXT NOT NULL DEFAULT '',
    replay_of_node_id TEXT NOT NULL DEFAULT '',
    -- last_heartbeat_at: orchestrator liveness ping for the run as a
    -- whole. NULL for rows that predate the column or come from
    -- backends that don't drive a run-level heartbeat (local + S3
    -- modes reconcile orphans via per-node heartbeats instead). The
    -- controller's reaper uses this to detect orchestrators that died
    -- between node dispatches.
    last_heartbeat_at INTEGER
);

CREATE INDEX IF NOT EXISTS idx_runs_started ON runs(started_at DESC);
CREATE INDEX IF NOT EXISTS idx_runs_pipeline ON runs(pipeline, started_at DESC);

CREATE TABLE IF NOT EXISTS nodes (
    run_id           TEXT NOT NULL,
    node_id          TEXT NOT NULL,
    status           TEXT NOT NULL,
    outcome          TEXT NOT NULL DEFAULT '',
    deps_json        BLOB,
    started_at       INTEGER,
    finished_at      INTEGER,
    error            TEXT NOT NULL DEFAULT '',
    output_json      BLOB,
    -- Warm-pool dispatch: ready_at + claimed_by + lease_expires_at.
    -- All NULL on laptop / K8sRunner paths.
    ready_at         INTEGER,
    claimed_by       TEXT,
    lease_expires_at INTEGER,
    -- needs_labels: JSON []string from RunsOn; AND semantics.
    needs_labels     BLOB,
    -- status_detail: phase string runners write for the dashboard.
    status_detail    TEXT NOT NULL DEFAULT '',
    -- last_heartbeat: runner liveness; for UI, not lease enforcement.
    last_heartbeat   INTEGER,
    -- failure_reason: Failure* constant; empty = uncategorized.
    failure_reason   TEXT NOT NULL DEFAULT '',
    -- exit_code: process exit; NULL when not tied to a process.
    exit_code        INTEGER,
    PRIMARY KEY (run_id, node_id),
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_nodes_claimable
    ON nodes(ready_at)
    WHERE ready_at IS NOT NULL AND claimed_by IS NULL AND status != 'done';
CREATE INDEX IF NOT EXISTS idx_nodes_claimed_lease
    ON nodes(lease_expires_at)
    WHERE claimed_by IS NOT NULL;

CREATE TABLE IF NOT EXISTS events (
    run_id   TEXT NOT NULL,
    seq      INTEGER NOT NULL,
    node_id  TEXT NOT NULL DEFAULT '',
    kind     TEXT NOT NULL,
    ts       INTEGER NOT NULL,
    payload  BLOB,
    PRIMARY KEY (run_id, seq),
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_events_run_ts ON events(run_id, ts);

-- triggers: external-intake queue. Webhook handlers insert a row
-- with status='pending'; workers claim atomically (pending -> claimed)
-- and execute the named pipeline, producing a run with matching id.
-- Claim ordering is FIFO on created_at.
--
-- lease_expires_at: crash-recovery lease set at claim time and
-- extended by worker heartbeats. A reaper sweeps claimed triggers
-- with expired leases back to pending so a fresh worker can pick
-- them up.
CREATE TABLE IF NOT EXISTS triggers (
    id                    TEXT PRIMARY KEY,
    pipeline              TEXT NOT NULL,
    args_json             BLOB,
    trigger_source        TEXT NOT NULL DEFAULT '',
    trigger_user          TEXT NOT NULL DEFAULT '',
    trigger_env           BLOB,
    git_branch            TEXT NOT NULL DEFAULT '',
    git_sha               TEXT NOT NULL DEFAULT '',
    status                TEXT NOT NULL DEFAULT 'pending',
    created_at            INTEGER NOT NULL,
    claimed_at            INTEGER,
    lease_expires_at      INTEGER,
    cancel_requested_at   INTEGER,
    repo                  TEXT NOT NULL DEFAULT '',
    repo_url              TEXT NOT NULL DEFAULT '',
    github_owner          TEXT NOT NULL DEFAULT '',
    github_repo           TEXT NOT NULL DEFAULT '',
    retry_of              TEXT NOT NULL DEFAULT '',
    parent_node_id        TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_triggers_pending
    ON triggers(status, created_at) WHERE status = 'pending';
CREATE INDEX IF NOT EXISTS idx_triggers_claimed_lease
    ON triggers(status, lease_expires_at) WHERE status = 'claimed';
CREATE INDEX IF NOT EXISTS idx_triggers_source_status_created
    ON triggers(trigger_source, status, created_at);

-- Unified concurrency primitive (.Cache DSL).
-- Capacity per-key on entries; policy per-arrival on waiters.
-- previous_capacity surfaces config drift via a warn event.
-- holders has no FK to entries; missing entry rows aren't corruption.
-- cache.output_ref is opaque ("run/node"); 35d TTL bound.
CREATE TABLE IF NOT EXISTS concurrency_entries (
    key                 TEXT PRIMARY KEY,
    capacity            INTEGER NOT NULL DEFAULT 1,
    previous_capacity   INTEGER,
    last_write_run_id   TEXT NOT NULL DEFAULT '',
    last_write_node_id  TEXT NOT NULL DEFAULT '',
    updated_at          INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS concurrency_holders (
    key              TEXT NOT NULL,
    holder_id        TEXT NOT NULL,
    run_id           TEXT NOT NULL,
    node_id          TEXT NOT NULL DEFAULT '',
    claimed_at       INTEGER NOT NULL,
    lease_expires_at INTEGER NOT NULL,
    superseded       INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (key, holder_id)
);
CREATE INDEX IF NOT EXISTS idx_concurrency_holders_key_claimed
    ON concurrency_holders(key, claimed_at);
CREATE INDEX IF NOT EXISTS idx_concurrency_holders_lease
    ON concurrency_holders(lease_expires_at);

CREATE TABLE IF NOT EXISTS concurrency_waiters (
    key                TEXT NOT NULL,
    run_id             TEXT NOT NULL,
    node_id            TEXT NOT NULL DEFAULT '',
    holder_id          TEXT NOT NULL DEFAULT '',
    arrived_at         INTEGER NOT NULL,
    policy             TEXT NOT NULL,
    cache_key_hash     TEXT NOT NULL DEFAULT '',
    leader_run_id      TEXT NOT NULL DEFAULT '',
    leader_node_id     TEXT NOT NULL DEFAULT '',
    cancel_timeout_ns  INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (key, run_id, node_id)
);
CREATE INDEX IF NOT EXISTS idx_concurrency_waiters_arrived
    ON concurrency_waiters(key, arrived_at);

CREATE TABLE IF NOT EXISTS concurrency_cache (
    key             TEXT NOT NULL,
    cache_key_hash  TEXT NOT NULL,
    output_ref      TEXT NOT NULL,
    origin_run_id   TEXT NOT NULL,
    origin_node_id  TEXT NOT NULL,
    created_at      INTEGER NOT NULL,
    expires_at      INTEGER NOT NULL,
    last_hit_at     INTEGER NOT NULL,
    PRIMARY KEY (key, cache_key_hash)
);
CREATE INDEX IF NOT EXISTS idx_concurrency_cache_expires
    ON concurrency_cache(expires_at);
CREATE INDEX IF NOT EXISTS idx_concurrency_cache_lru
    ON concurrency_cache(last_hit_at);

-- Per-node resource samples; append-only.
-- Per-step runtime state. One row per (run, node, step). Status is
-- one of running | passed | failed | skipped. Skipped steps insert
-- with started_at == finished_at and never transition. Rows are
-- written by the orchestrator on step_start / step_end / step_skipped.
-- Reads serve the dashboard's per-node step DAG.
CREATE TABLE IF NOT EXISTS node_steps (
    run_id      TEXT NOT NULL,
    node_id     TEXT NOT NULL,
    step_id     TEXT NOT NULL,
    status      TEXT NOT NULL,
    started_at  INTEGER,
    finished_at INTEGER,
    PRIMARY KEY (run_id, node_id, step_id),
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_node_steps_lookup
    ON node_steps(run_id, node_id);

CREATE TABLE IF NOT EXISTS node_metrics (
    run_id          TEXT NOT NULL,
    node_id         TEXT NOT NULL,
    ts              INTEGER NOT NULL,
    cpu_millicores  INTEGER NOT NULL,
    memory_bytes    INTEGER NOT NULL,
    PRIMARY KEY (run_id, node_id, ts)
);

CREATE INDEX IF NOT EXISTS idx_node_metrics_lookup
    ON node_metrics(run_id, node_id, ts);

-- Bearer tokens. hash = argon2id digest; raw value is returned once.
-- Lookups prefix-match first then argon2-verify; constant-cost per req.
CREATE TABLE IF NOT EXISTS tokens (
    hash         TEXT PRIMARY KEY,
    prefix       TEXT NOT NULL,
    principal    TEXT NOT NULL,
    kind         TEXT NOT NULL,        -- user | runner | service
    scopes       TEXT NOT NULL,        -- comma-separated set
    created_at   INTEGER NOT NULL,
    expires_at   INTEGER,              -- NULL = never expires
    last_used_at INTEGER,
    revoked_at   INTEGER,
    replaced_by  TEXT                  -- prefix of rotation successor
);
CREATE INDEX IF NOT EXISTS idx_tokens_prefix ON tokens(prefix);

-- Browser sessions; same hash treatment as tokens.
CREATE TABLE IF NOT EXISTS sessions (
    hash          TEXT PRIMARY KEY,
    principal     TEXT NOT NULL,
    scopes        TEXT NOT NULL,
    csrf_token    TEXT NOT NULL,
    created_at    INTEGER NOT NULL,
    expires_at    INTEGER NOT NULL,
    last_used_at  INTEGER
);
CREATE INDEX IF NOT EXISTS idx_sessions_expires ON sessions(expires_at);

-- Dashboard user credentials; pw_hash = argon2id, plaintext never persisted.
CREATE TABLE IF NOT EXISTS users (
    name          TEXT PRIMARY KEY,
    pw_hash       TEXT NOT NULL,
    created_at    INTEGER NOT NULL,
    last_login_at INTEGER
);

-- Pipeline secrets; encryption at rest is up to the volume.
CREATE TABLE IF NOT EXISTS secrets (
    name       TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    principal  TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    -- masked=0 = non-sensitive config (not redacted in run output).
    masked     INTEGER NOT NULL DEFAULT 1
);

-- Debug pauses; one row per (run, node, reason).
CREATE TABLE IF NOT EXISTS debug_pauses (
    run_id       TEXT NOT NULL,
    node_id      TEXT NOT NULL,
    reason       TEXT NOT NULL,
    paused_at    INTEGER NOT NULL,
    expires_at   INTEGER NOT NULL,
    released_at  INTEGER,
    released_by  TEXT NOT NULL DEFAULT '',
    release_kind TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (run_id, node_id, reason),
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_debug_pauses_open
    ON debug_pauses(run_id) WHERE released_at IS NULL;

-- Manual approval gates. One row per Approval node per run.
CREATE TABLE IF NOT EXISTS approvals (
    run_id       TEXT    NOT NULL,
    node_id      TEXT    NOT NULL,
    requested_at INTEGER NOT NULL,
    message      TEXT    NOT NULL DEFAULT '',
    timeout_ms   INTEGER NOT NULL DEFAULT 0,
    on_timeout   TEXT    NOT NULL DEFAULT 'fail',
    approver     TEXT    NOT NULL DEFAULT '',
    resolved_at  INTEGER,
    resolution   TEXT    NOT NULL DEFAULT '',
    comment      TEXT    NOT NULL DEFAULT '',
    PRIMARY KEY (run_id, node_id),
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_approvals_pending
    ON approvals(requested_at) WHERE resolved_at IS NULL;

-- Dispatch frame snapshots for replay/rerun. seq is the per-(run,node)
-- attempt counter (warm-pool re-claim; not step retries within one
-- executeNode call). input_envelope_json: {version, type_name,
-- scalar_fields}, masked, capped at 4MB (over-cap stores a stub).
CREATE TABLE IF NOT EXISTS node_dispatches (
    run_id              TEXT NOT NULL,
    node_id             TEXT NOT NULL,
    seq                 INTEGER NOT NULL,
    dispatched_at       INTEGER NOT NULL,
    code_version        TEXT NOT NULL DEFAULT '',
    binary_hash         TEXT NOT NULL DEFAULT '',
    runner_labels       BLOB,
    env_json            BLOB,
    workdir             TEXT NOT NULL DEFAULT '',
    input_envelope_json BLOB,
    input_size_bytes    INTEGER NOT NULL DEFAULT 0,
    secret_redactions   INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (run_id, node_id, seq),
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_node_dispatches_lookup
    ON node_dispatches(run_id, node_id, seq DESC);
`

// schemaPostgres is derived from schemaSQLite by substituting the two
// type names that differ. The full SQLite schema is otherwise valid
// Postgres: partial indexes, RETURNING, ON CONFLICT, FK CASCADE,
// composite PRIMARY KEY, and DEFAULT '<literal>' all carry over
// unchanged. JSON-encoded columns stay as BYTEA so read paths can
// remain dialect-agnostic; JSONB is a future optimization.
var schemaPostgres = func() string {
	r := strings.NewReplacer(
		"INTEGER", "BIGINT",
		"BLOB", "BYTEA",
	)
	return r.Replace(schemaSQLite)
}()

// expectedSchemaVersion is the schema version this binary understands.
// Bumped each time a new migrateToVN step is appended below. On Open,
// a database recording a higher version refuses; a database recording
// a lower (or no) version is brought forward by running the missing
// steps in order inside a single transaction (on Postgres, guarded by
// pg_advisory_xact_lock so N runners coordinate cleanly).
const expectedSchemaVersion = 2

// ExpectedSchemaVersion returns the schema version this binary
// understands. Useful for diagnostics, version-mismatch reporting,
// and tests that need to assert what Open will write into the
// sparkwing_schema_version table on a fresh database.
func ExpectedSchemaVersion() int { return expectedSchemaVersion }

// schemaVersionTable is created unconditionally on every Open before
// the version check runs; the check needs the table to exist in order
// to read from it. The same DDL is valid in both dialects (INTEGER +
// BIGINT translate identically here).
const schemaVersionTable = `CREATE TABLE IF NOT EXISTS sparkwing_schema_version (
    version    INTEGER NOT NULL,
    applied_at BIGINT NOT NULL,
    PRIMARY KEY (version)
);`

// SkewError is returned by Open when the database is at a schema
// version newer than the binary understands. Callers can use
// errors.As to detect the condition (e.g. for surfacing a custom
// upgrade prompt in the CLI); the wrapped message is plain English
// and suitable for direct display.
type SkewError struct {
	DBVersion     int
	BinaryVersion int
}

func (e *SkewError) Error() string {
	return fmt.Sprintf(
		"sparkwing: database is at schema version %d; this binary expects %d. Upgrade sparkwing or restore the database to a matching version.",
		e.DBVersion, e.BinaryVersion,
	)
}

// migrate brings the database up to expectedSchemaVersion. The flow
// is identical across dialects:
//
//  1. Ensure sparkwing_schema_version exists (idempotent CREATE TABLE
//     IF NOT EXISTS).
//  2. Read MAX(version). NULL → 0; treat a brand-new database the
//     same as one stuck at the pre-history version 0.
//  3. If current > expectedSchemaVersion: return SkewError. Do not
//     touch the database.
//  4. If current < expectedSchemaVersion: run migrateToVN(...) for
//     each missing step in order. Each step ends by INSERTing its
//     version row.
//  5. If current == expectedSchemaVersion: no-op.
//
// On Postgres the version check and migration steps run inside one
// transaction guarded by pg_advisory_xact_lock so concurrent opens
// against a fresh database produce exactly one execution. SQLite
// serializes writers at the database level; concurrent opens
// converge via INSERT ... ON CONFLICT DO NOTHING on the version
// row.
func (s *Store) migrate() error {
	ctx := context.Background()
	if s.dialect == DialectPostgres {
		// CREATE TABLE IF NOT EXISTS on Postgres races at the system
		// catalog under contention (pg_type_typname_nsp_index unique
		// violation when N concurrent CREATE statements collide). The
		// pg migrate path therefore creates the version table inside
		// the advisory-lock-guarded transaction so it serializes too.
		return s.migratePostgres(ctx)
	}
	// SQLite serializes writers at the database level, so an unlocked
	// CREATE TABLE IF NOT EXISTS is safe even across processes that
	// open the same file simultaneously.
	if _, err := s.exec(ctx, schemaVersionTable); err != nil {
		return fmt.Errorf("create sparkwing_schema_version table: %w", err)
	}
	return s.migrateSQLite(ctx)
}

func (s *Store) migrateSQLite(ctx context.Context) error {
	var current int
	if err := s.queryRow(ctx,
		`SELECT COALESCE(MAX(version), 0) FROM sparkwing_schema_version`,
	).Scan(&current); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if current > expectedSchemaVersion {
		return &SkewError{DBVersion: current, BinaryVersion: expectedSchemaVersion}
	}
	for v := current + 1; v <= expectedSchemaVersion; v++ {
		if err := s.applyMigrationSQLite(ctx, v); err != nil {
			return fmt.Errorf("apply migration v%d: %w", v, err)
		}
		if _, err := s.exec(ctx,
			`INSERT INTO sparkwing_schema_version (version, applied_at) VALUES (?, ?)
			 ON CONFLICT (version) DO NOTHING`,
			v, time.Now().UnixNano()); err != nil {
			return fmt.Errorf("record schema version v%d: %w", v, err)
		}
	}
	return nil
}

func (s *Store) migratePostgres(ctx context.Context) error {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	// pg_advisory_xact_lock auto-releases on commit/rollback so N
	// runners opening the same fresh database serialize cleanly on
	// the migration path. The hash key is stable across versions.
	if _, err := tx.ExecContext(ctx,
		`SELECT pg_advisory_xact_lock(hashtext('sparkwing_migrate'))`); err != nil {
		return fmt.Errorf("acquire migrate advisory lock: %w", err)
	}
	// Create the version table under the lock; outside it, CREATE TABLE
	// IF NOT EXISTS races at the catalog level when N processes
	// arrive simultaneously against a fresh database.
	if _, err := tx.ExecContext(ctx, schemaVersionTable); err != nil {
		return fmt.Errorf("create sparkwing_schema_version table: %w", err)
	}
	var current int
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(version), 0) FROM sparkwing_schema_version`,
	).Scan(&current); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if current > expectedSchemaVersion {
		return &SkewError{DBVersion: current, BinaryVersion: expectedSchemaVersion}
	}
	if current == expectedSchemaVersion {
		return tx.Commit()
	}
	for v := current + 1; v <= expectedSchemaVersion; v++ {
		if err := s.applyMigrationPostgresTx(ctx, tx, v); err != nil {
			return fmt.Errorf("apply migration v%d: %w", v, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO sparkwing_schema_version (version, applied_at) VALUES (?, ?)
			 ON CONFLICT (version) DO NOTHING`,
			v, time.Now().UnixNano()); err != nil {
			return fmt.Errorf("record schema version v%d: %w", v, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	// backfillRunAnnotationRollup runs after the lock is released so
	// it doesn't hold writers; it's idempotent and re-running across
	// retries is harmless.
	return s.backfillRunAnnotationRollup()
}

// applyMigrationSQLite dispatches on version. Each case body is the
// canonical SQLite-side migration step for that version; new
// versions append a case here and bump expectedSchemaVersion.
func (s *Store) applyMigrationSQLite(ctx context.Context, version int) error {
	switch version {
	case 1:
		if _, err := s.exec(ctx, schemaSQLite); err != nil {
			return err
		}
		if err := s.ensureColumnsAll(); err != nil {
			return err
		}
		return s.backfillRunAnnotationRollup()
	case 2:
		return s.ensureColumnsAll()
	default:
		return fmt.Errorf("no migration registered for v%d", version)
	}
}

// applyMigrationPostgresTx dispatches on version inside the open
// migration transaction. Pairs with applyMigrationSQLite -- the same
// version number maps to a semantically equivalent step on each
// dialect.
func (s *Store) applyMigrationPostgresTx(ctx context.Context, tx *storeTx, version int) error {
	switch version {
	case 1:
		if _, err := tx.ExecContext(ctx, schemaPostgres); err != nil {
			return err
		}
		return s.ensureColumnsAllTx(ctx, tx)
	case 2:
		return s.ensureColumnsAllTx(ctx, tx)
	default:
		return fmt.Errorf("no migration registered for v%d", version)
	}
}

// columnMigrations enumerates the additive column changes that have
// landed since the canonical schema first shipped. Types are written
// in SQLite syntax; the Postgres path translates them at apply time
// via translateColumnType.
//
// New columns must keep the existing row default behavior compatible:
// either NOT NULL DEFAULT <literal> or NULL-able. This list is part of
// the schema contract; reorderings and deletions both count as
// schema-version bumps.
type columnSpec struct {
	table string
	cols  map[string]string
}

var columnMigrations = []columnSpec{
	{"node_steps", map[string]string{
		"annotations_json": "BLOB",
		"summary":          "TEXT NOT NULL DEFAULT ''",
	}},
	{"nodes", map[string]string{
		"ready_at":         "INTEGER",
		"claimed_by":       "TEXT",
		"lease_expires_at": "INTEGER",
		"needs_labels":     "BLOB",
		"status_detail":    "TEXT NOT NULL DEFAULT ''",
		"last_heartbeat":   "INTEGER",
		"failure_reason":   "TEXT NOT NULL DEFAULT ''",
		"exit_code":        "INTEGER",
		"annotations_json": "BLOB",
		"summary":          "TEXT NOT NULL DEFAULT ''",
	}},
	{"runs", map[string]string{
		"parent_run_id":     "TEXT",
		"repo":              "TEXT NOT NULL DEFAULT ''",
		"repo_url":          "TEXT NOT NULL DEFAULT ''",
		"github_owner":      "TEXT NOT NULL DEFAULT ''",
		"github_repo":       "TEXT NOT NULL DEFAULT ''",
		"retry_of":          "TEXT NOT NULL DEFAULT ''",
		"retried_as":        "TEXT NOT NULL DEFAULT ''",
		"retry_source":      "TEXT NOT NULL DEFAULT ''",
		"replay_of_run_id":  "TEXT NOT NULL DEFAULT ''",
		"replay_of_node_id": "TEXT NOT NULL DEFAULT ''",
		"created_at":        "INTEGER NOT NULL DEFAULT 0",
		"receipt_sha":       "TEXT NOT NULL DEFAULT ''",
		"cost_cents":        "INTEGER NOT NULL DEFAULT 0",
		"cost_currency":     "TEXT NOT NULL DEFAULT 'USD'",
		"cost_settled":      "INTEGER NOT NULL DEFAULT 0",
		"annotation_count":  "INTEGER NOT NULL DEFAULT 0",
		"top_annotation":    "TEXT NOT NULL DEFAULT ''",
		"annotations_json":  "BLOB",
		"invocation_json":   "BLOB",
		"last_heartbeat_at": "INTEGER",
	}},
	{"triggers", map[string]string{
		"parent_run_id":  "TEXT",
		"repo":           "TEXT NOT NULL DEFAULT ''",
		"repo_url":       "TEXT NOT NULL DEFAULT ''",
		"github_owner":   "TEXT NOT NULL DEFAULT ''",
		"github_repo":    "TEXT NOT NULL DEFAULT ''",
		"retry_of":       "TEXT NOT NULL DEFAULT ''",
		"retry_source":   "TEXT NOT NULL DEFAULT ''",
		"parent_node_id": "TEXT NOT NULL DEFAULT ''",
		"full":           "INTEGER NOT NULL DEFAULT 0",
	}},
	{"concurrency_waiters", map[string]string{
		"holder_id": "TEXT NOT NULL DEFAULT ''",
	}},
	{"secrets", map[string]string{
		"masked": "INTEGER NOT NULL DEFAULT 1",
	}},
}

func (s *Store) ensureColumnsAll() error {
	for _, spec := range columnMigrations {
		if err := s.ensureColumns(spec.table, spec.cols); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ensureColumnsAllTx(ctx context.Context, tx *storeTx) error {
	for _, spec := range columnMigrations {
		for name, typ := range spec.cols {
			stmt := fmt.Sprintf(
				`ALTER TABLE %q ADD COLUMN IF NOT EXISTS %q %s`,
				spec.table, name, translateColumnType(typ),
			)
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("add column %s.%s: %w", spec.table, name, err)
			}
		}
	}
	return nil
}

// translateColumnType rewrites a SQLite column-type fragment into its
// Postgres equivalent. Only the two name substitutions used in the
// canonical schema are handled; the rest of the SQL fragment
// (NULL/NOT NULL, DEFAULT, etc.) is byte-identical between dialects.
func translateColumnType(t string) string {
	r := strings.NewReplacer(
		"INTEGER", "BIGINT",
		"BLOB", "BYTEA",
	)
	return r.Replace(t)
}

// backfillRunAnnotationRollup populates the runs annotation columns
// from per-node + per-step annotations for rows that predate the
// live-bump writes in AppendNodeAnnotation / AppendStepAnnotation.
// Idempotent: only rows whose count is still 0 get re-computed, and
// the computation yields 0 for runs that genuinely have no
// annotations, so this is a no-op the second time around.
func (s *Store) backfillRunAnnotationRollup() error {
	rows, err := s.queryNoCtx(`SELECT id FROM runs WHERE annotation_count = 0`)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	_ = rows.Close()
	for _, id := range ids {
		gathered, err := s.gatherRunAnnotations(id)
		if err != nil {
			return err
		}
		if len(gathered) == 0 {
			continue
		}
		blob, _ := json.Marshal(gathered)
		if _, err := s.execNoCtx(`
UPDATE runs SET annotation_count = ?, top_annotation = ?, annotations_json = ?
WHERE id = ?`, len(gathered), gathered[len(gathered)-1], blob, id); err != nil {
			return err
		}
	}
	return nil
}

// gatherRunAnnotations reads every annotation across the run's nodes
// and steps in append order. Order is by table then natural row
// order -- close to event order in practice.
func (s *Store) gatherRunAnnotations(runID string) ([]string, error) {
	var out []string
	rows, err := s.queryNoCtx(`
SELECT annotations_json FROM nodes WHERE run_id = ? AND annotations_json IS NOT NULL AND annotations_json != ''
UNION ALL
SELECT annotations_json FROM node_steps WHERE run_id = ? AND annotations_json IS NOT NULL AND annotations_json != ''`,
		runID, runID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var blob []byte
		if err := rows.Scan(&blob); err != nil {
			return nil, err
		}
		var list []string
		if err := json.Unmarshal(blob, &list); err != nil {
			continue
		}
		out = append(out, list...)
	}
	return out, nil
}

// appendRunAnnotation appends one entry to runs.annotations_json
// inside the supplied transaction. Caller is responsible for the
// surrounding txn lifecycle so the per-node/per-step write and the
// run-row rollup stay in sync.
func appendRunAnnotation(tx *storeTx, runID, msg string) error {
	var blob []byte
	err := tx.QueryRow(`SELECT annotations_json FROM runs WHERE id = ?`, runID).Scan(&blob)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var list []string
	if len(blob) > 0 {
		_ = json.Unmarshal(blob, &list)
	}
	list = append(list, msg)
	next, err := json.Marshal(list)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`UPDATE runs SET annotations_json = ? WHERE id = ?`, next, runID)
	return err
}

// ensureColumns adds any of the named columns missing from the table.
// Types are the literal SQL fragments appended after the column name.
// Returning on the first error is safe because subsequent opens will
// finish the job.
func (s *Store) ensureColumns(table string, cols map[string]string) error {
	rows, err := s.queryNoCtx(fmt.Sprintf(`PRAGMA table_info(%q)`, table))
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			_ = rows.Close()
			return err
		}
		have[name] = true
	}
	_ = rows.Close()
	for name, typ := range cols {
		if have[name] {
			continue
		}
		stmt := fmt.Sprintf(`ALTER TABLE %q ADD COLUMN %q %s`, table, name, typ)
		if _, err := s.execNoCtx(stmt); err != nil {
			return fmt.Errorf("add column %s.%s: %w", table, name, err)
		}
	}
	return nil
}

// --- Runs ---

// Run is one row in the runs table.
type Run struct {
	ID            string            `json:"id"`
	Pipeline      string            `json:"pipeline"`
	Status        string            `json:"status"`
	TriggerSource string            `json:"trigger_source,omitempty"`
	GitBranch     string            `json:"git_branch,omitempty"`
	GitSHA        string            `json:"git_sha,omitempty"`
	Args          map[string]string `json:"args,omitempty"`
	PlanSnapshot  []byte            `json:"-"`
	Error         string            `json:"error,omitempty"`
	// CreatedAt is when the controller first persisted the run row
	// (trigger-intake time for trigger-originated runs, or CreateRun
	// time for direct CreateRun callers). Lets "pending" runs have a
	// wall-clock anchor distinct from StartedAt.
	CreatedAt  time.Time  `json:"created_at,omitempty"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	// ParentRunID identifies the spawning RunAndAwait caller.
	ParentRunID string `json:"parent_run_id,omitempty"`
	// Repo is the short name (e.g. "my-app").
	Repo string `json:"repo,omitempty"`
	// RepoURL is `git remote get-url origin` at trigger time.
	RepoURL string `json:"repo_url,omitempty"`
	// GithubOwner/Repo: parsed when origin is github.
	GithubOwner string `json:"github_owner,omitempty"`
	GithubRepo  string `json:"github_repo,omitempty"`
	// RetryOf points at the source run; RetriedAs is the reverse.
	RetryOf   string `json:"retry_of,omitempty"`
	RetriedAs string `json:"retried_as,omitempty"`
	// RetrySource is RetrySourceManual or RetrySourceAuto.
	RetrySource string `json:"retry_source,omitempty"`
	// Replay lineage; independent of retry chain.
	ReplayOfRunID  string `json:"replay_of_run_id,omitempty"`
	ReplayOfNodeID string `json:"replay_of_node_id,omitempty"`
	// Invocation snapshots how this run was started: flags, args,
	// binary_source, cwd, reproducer, hashes, and anything else the
	// orchestrator chooses to include in run_start.attrs. Stored as
	// a free-form map so adding a new context field is a one-line
	// emitter change with no schema migration. Empty/nil for runs
	// created before the column landed.
	Invocation map[string]any `json:"invocation,omitempty"`
	// Annotation rollup surfaced to list views. Updated server-side on
	// each sparkwing.Annotate call; the dashboard renders these
	// without needing a per-row aggregate query.
	AnnotationCount int      `json:"annotation_count,omitempty"`
	TopAnnotation   string   `json:"top_annotation,omitempty"`
	Annotations     []string `json:"annotations,omitempty"`
	// LastHeartbeatAt is the most recent run-level liveness ping from
	// the dispatching orchestrator. NULL for rows that predate the
	// column or come from backends that don't ping it (local + S3
	// modes use per-node heartbeats for orphan detection instead).
	LastHeartbeatAt *time.Time `json:"last_heartbeat_at,omitempty"`
}

// CreateRun inserts a run row, or upgrades an existing 'pending' row
// (controller-pre-allocated at trigger-intake) to the
// caller's status. Idempotent for the (pending -> running) transition
// the orchestrator performs at start-of-run; non-pending existing rows
// are left untouched so this stays a no-op on retry / replay paths.
func (s *Store) CreateRun(ctx context.Context, r Run) error {
	argsJSON, _ := json.Marshal(r.Args)
	// Invocation snapshot is omitted (NULL) when nil/empty so the
	// scanner can distinguish "no snapshot recorded" from "explicitly
	// empty map". Existing rows from before the column landed will
	// also read NULL.
	var invocationJSON []byte
	if len(r.Invocation) > 0 {
		invocationJSON, _ = json.Marshal(r.Invocation)
	}
	// NULL parent so ancestor walks terminate via IS NULL.
	var parent sql.NullString
	if r.ParentRunID != "" {
		parent = sql.NullString{String: r.ParentRunID, Valid: true}
	}
	created := r.CreatedAt
	if created.IsZero() {
		// Direct CreateRun (no controller pre-allocation): created_at
		// = started_at so the column is never zero outside the migration.
		created = r.StartedAt
	}
	// ON CONFLICT DO UPDATE WHERE existing.status = 'pending':
	// the only legal transition for an existing row is the
	// orchestrator promoting a controller-allocated pending run to
	// running. We deliberately do NOT clobber created_at on the
	// upsert so the trigger-intake timestamp survives.
	_, err := s.exec(
		ctx, `
INSERT INTO runs (id, pipeline, status, trigger_source, git_branch, git_sha, args_json, plan_json, created_at, started_at, parent_run_id, repo, repo_url, github_owner, github_repo, retry_of, retried_as, retry_source, replay_of_run_id, replay_of_node_id, invocation_json)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET
    pipeline        = excluded.pipeline,
    status          = excluded.status,
    trigger_source  = excluded.trigger_source,
    git_branch      = excluded.git_branch,
    git_sha         = excluded.git_sha,
    args_json       = excluded.args_json,
    plan_json       = excluded.plan_json,
    started_at      = excluded.started_at,
    parent_run_id   = excluded.parent_run_id,
    repo            = excluded.repo,
    repo_url        = excluded.repo_url,
    github_owner    = excluded.github_owner,
    github_repo     = excluded.github_repo,
    retry_of        = excluded.retry_of,
    retried_as      = excluded.retried_as,
    retry_source    = excluded.retry_source,
    replay_of_run_id  = excluded.replay_of_run_id,
    replay_of_node_id = excluded.replay_of_node_id,
    invocation_json   = excluded.invocation_json
WHERE runs.status = 'pending'`,
		r.ID, r.Pipeline, r.Status, r.TriggerSource, r.GitBranch, r.GitSHA,
		argsJSON, r.PlanSnapshot, created.UnixNano(), r.StartedAt.UnixNano(), parent,
		r.Repo, r.RepoURL, r.GithubOwner, r.GithubRepo,
		r.RetryOf, r.RetriedAs, r.RetrySource, r.ReplayOfRunID, r.ReplayOfNodeID,
		invocationJSON,
	)
	return err
}

// FinishRun marks a run terminal with the given status and optional error.
func (s *Store) FinishRun(ctx context.Context, runID, status, errMsg string) error {
	_, err := s.exec(ctx, `
UPDATE runs
   SET status = ?, error = ?, finished_at = ?
 WHERE id = ?`,
		status, errMsg, time.Now().UnixNano(), runID)
	return err
}

// TouchRunHeartbeat stamps last_heartbeat_at=now for the run row. The
// dispatching orchestrator calls this on a ticker while the run is
// active so the controller's reaper can detect a fully-orphaned run
// (laptop closed, network gone, process killed) and flip it to
// failed instead of leaving status='running' forever.
func (s *Store) TouchRunHeartbeat(ctx context.Context, runID string) error {
	_, err := s.exec(ctx,
		`UPDATE runs SET last_heartbeat_at = ? WHERE id = ?`,
		time.Now().UnixNano(), runID)
	return err
}

// UpdatePlanSnapshot replaces the stored plan JSON for a run.
func (s *Store) UpdatePlanSnapshot(ctx context.Context, runID string, snapshot []byte) error {
	_, err := s.exec(ctx, `UPDATE runs SET plan_json = ? WHERE id = ?`, snapshot, runID)
	return err
}

// SetRetriedAs stores the reverse retry pointer on runID. Idempotent.
func (s *Store) SetRetriedAs(ctx context.Context, runID, newID string) error {
	_, err := s.exec(ctx,
		`UPDATE runs SET retried_as = ? WHERE id = ?`, newID, runID)
	return err
}

// ListRunRetryTree returns every run in the retry tree that runID
// belongs to, ordered by created_at (oldest first). The "root" is
// found by walking retry_of upward until it hits "", then the result
// includes the root plus every descendant whose retry_of chain leads
// back to it. Branching is preserved: if attempt #2 was retried twice
// (creating #3 and #4 with the same retry_of=#2), both #3 and #4
// appear as siblings in the list.
//
// Numbering / display: callers number the returned slice 1..N in
// order; the chronological position is the user-visible "Attempt N".
//
// Cycle guard: a hard cap on the upward walk keeps a corrupted
// retry_of cycle from spinning forever.
func (s *Store) ListRunRetryTree(ctx context.Context, runID string) ([]*Run, error) {
	if runID == "" {
		return nil, nil
	}
	// Walk up to the root.
	const maxDepth = 256
	rootID := runID
	for range maxDepth {
		row := s.queryRow(ctx,
			`SELECT retry_of FROM runs WHERE id = ?`, rootID)
		var parent string
		if err := row.Scan(&parent); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, nil
			}
			return nil, err
		}
		if parent == "" || parent == rootID {
			break
		}
		rootID = parent
	}
	// BFS down: collect every run whose retry_of chain reaches rootID.
	collected := map[string]*Run{}
	root, err := s.GetRun(ctx, rootID)
	if err != nil {
		return nil, err
	}
	if root == nil {
		return nil, nil
	}
	collected[rootID] = root
	frontier := []string{rootID}
	for len(frontier) > 0 {
		next := frontier[:0:0]
		for _, id := range frontier {
			rows, err := s.query(ctx,
				`SELECT id, pipeline, status, trigger_source, git_branch, git_sha, args_json, plan_json, error, created_at, started_at, finished_at, parent_run_id, repo, repo_url, github_owner, github_repo, retry_of, retried_as, retry_source, replay_of_run_id, replay_of_node_id, invocation_json, annotation_count, top_annotation, annotations_json, last_heartbeat_at
				   FROM runs WHERE retry_of = ?`, id)
			if err != nil {
				return nil, err
			}
			for rows.Next() {
				r, scanErr := scanRun(rows)
				if scanErr != nil {
					_ = rows.Close()
					return nil, scanErr
				}
				if _, dup := collected[r.ID]; dup {
					continue
				}
				collected[r.ID] = r
				next = append(next, r.ID)
			}
			_ = rows.Close()
		}
		frontier = next
	}
	out := make([]*Run, 0, len(collected))
	for _, r := range collected {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, nil
}

// GetRun fetches a single run by ID.
func (s *Store) GetRun(ctx context.Context, runID string) (*Run, error) {
	row := s.queryRow(ctx, `
SELECT id, pipeline, status, trigger_source, git_branch, git_sha, args_json, plan_json, error, created_at, started_at, finished_at, parent_run_id, repo, repo_url, github_owner, github_repo, retry_of, retried_as, retry_source, replay_of_run_id, replay_of_node_id, invocation_json, annotation_count, top_annotation, annotations_json, last_heartbeat_at
  FROM runs WHERE id = ?`, runID)
	return scanRun(row)
}

// RunFilter narrows ListRuns results; zero value matches everything.
type RunFilter struct {
	Pipelines   []string
	Statuses    []string
	Since       time.Time
	Limit       int // <=0 = default
	ParentRunID string
}

// ListRuns returns runs ordered newest-first, filtered by f.
func (s *Store) ListRuns(ctx context.Context, f RunFilter) ([]*Run, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}

	where := ""
	args := []any{}
	addIn := func(col string, values []string) {
		if len(values) == 0 {
			return
		}
		placeholders := make([]string, len(values))
		for i, v := range values {
			placeholders[i] = "?"
			args = append(args, v)
		}
		clause := col + " IN (" + strings.Join(placeholders, ",") + ")"
		if where == "" {
			where = " WHERE " + clause
		} else {
			where += " AND " + clause
		}
	}
	addIn("pipeline", f.Pipelines)
	addIn("status", f.Statuses)
	if f.ParentRunID != "" {
		if where == "" {
			where = " WHERE parent_run_id = ?"
		} else {
			where += " AND parent_run_id = ?"
		}
		args = append(args, f.ParentRunID)
	}
	if !f.Since.IsZero() {
		if where == "" {
			where = " WHERE started_at >= ?"
		} else {
			where += " AND started_at >= ?"
		}
		args = append(args, f.Since.UnixNano())
	}
	args = append(args, limit)

	query := `
SELECT id, pipeline, status, trigger_source, git_branch, git_sha, args_json, plan_json, error, created_at, started_at, finished_at, parent_run_id, repo, repo_url, github_owner, github_repo, retry_of, retried_as, retry_source, replay_of_run_id, replay_of_node_id, invocation_json, annotation_count, top_annotation, annotations_json, last_heartbeat_at
  FROM runs` + where + `
 ORDER BY started_at DESC
 LIMIT ?`

	rows, err := s.query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []*Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetLatestRun returns the newest run for pipeline matching statuses
// within maxAge. ErrNotFound on miss.
func (s *Store) GetLatestRun(ctx context.Context, pipeline string, statuses []string, maxAge time.Duration) (*Run, error) {
	if pipeline == "" {
		return nil, errors.New("GetLatestRun: pipeline is required")
	}
	where := "WHERE pipeline = ?"
	args := []any{pipeline}
	if len(statuses) > 0 {
		ph := make([]string, len(statuses))
		for i, st := range statuses {
			ph[i] = "?"
			args = append(args, st)
		}
		where += " AND status IN (" + strings.Join(ph, ",") + ")"
	}
	if maxAge > 0 {
		// COALESCE so in-flight rows don't slip past the bound.
		where += " AND COALESCE(finished_at, started_at) >= ?"
		args = append(args, time.Now().Add(-maxAge).UnixNano())
	}
	q := `
SELECT id, pipeline, status, trigger_source, git_branch, git_sha, args_json, plan_json, error, created_at, started_at, finished_at, parent_run_id, repo, repo_url, github_owner, github_repo, retry_of, retried_as, retry_source, replay_of_run_id, replay_of_node_id, invocation_json, annotation_count, top_annotation, annotations_json, last_heartbeat_at
  FROM runs ` + where + `
 ORDER BY started_at DESC
 LIMIT 1`
	return scanRun(s.queryRow(ctx, q, args...))
}

// DeleteRun removes the run + its trigger; CASCADE handles children.
//
// Triggers carrying parent_node_id are the cross-pipeline spawn
// linkage from their PARENT run -- they double as the dispatch row
// AND the edge the parent's DAG renders as "spawned this child".
// Deleting that trigger would silently strip the spawn pill from the
// parent and flip the node back to its declared Inline pill, which
// surprises operators who only meant to discard the child. We keep
// the trigger row in that case (orphaned: child run is gone, edge
// remains visible) so parent DAGs stay stable.
func (s *Store) DeleteRun(ctx context.Context, runID string) error {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM runs WHERE id = ?`, runID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM triggers WHERE id = ? AND parent_node_id = ''`, runID); err != nil {
		return err
	}
	return tx.Commit()
}

// PruneRunsOlderThan deletes terminal runs older than cutoff and
// returns their ids so callers can purge log files / cache blobs.
func (s *Store) PruneRunsOlderThan(ctx context.Context, cutoff time.Time) ([]string, error) {
	rows, err := s.query(ctx,
		`SELECT id FROM runs
		   WHERE started_at < ?
		     AND status IN ('success','failed','cancelled')`,
		cutoff.UnixNano())
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	_ = rows.Close()
	for _, id := range ids {
		if err := s.DeleteRun(ctx, id); err != nil {
			return ids, err
		}
	}
	return ids, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanRun(rs rowScanner) (*Run, error) {
	var r Run
	var argsJSON, planJSON, invocationJSON, annotationsJSON []byte
	var createdNS, startedNS int64
	var finishedNS, heartbeatNS sql.NullInt64
	var parent sql.NullString
	err := rs.Scan(&r.ID, &r.Pipeline, &r.Status, &r.TriggerSource,
		&r.GitBranch, &r.GitSHA, &argsJSON, &planJSON, &r.Error,
		&createdNS, &startedNS, &finishedNS, &parent,
		&r.Repo, &r.RepoURL, &r.GithubOwner, &r.GithubRepo,
		&r.RetryOf, &r.RetriedAs, &r.RetrySource,
		&r.ReplayOfRunID, &r.ReplayOfNodeID, &invocationJSON,
		&r.AnnotationCount, &r.TopAnnotation, &annotationsJSON,
		&heartbeatNS)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if createdNS > 0 {
		r.CreatedAt = time.Unix(0, createdNS)
	}
	r.StartedAt = time.Unix(0, startedNS)
	if finishedNS.Valid {
		t := time.Unix(0, finishedNS.Int64)
		r.FinishedAt = &t
	}
	if heartbeatNS.Valid {
		t := time.Unix(0, heartbeatNS.Int64)
		r.LastHeartbeatAt = &t
	}
	if parent.Valid {
		r.ParentRunID = parent.String
	}
	if len(argsJSON) > 0 {
		_ = json.Unmarshal(argsJSON, &r.Args)
	}
	if len(invocationJSON) > 0 {
		_ = json.Unmarshal(invocationJSON, &r.Invocation)
	}
	if len(annotationsJSON) > 0 {
		_ = json.Unmarshal(annotationsJSON, &r.Annotations)
	}
	r.PlanSnapshot = planJSON
	return &r, nil
}

// --- Nodes ---

// Node is one row in the nodes table.
type Node struct {
	RunID      string     `json:"run_id,omitempty"`
	NodeID     string     `json:"id"`
	Status     string     `json:"status"`
	Outcome    string     `json:"outcome,omitempty"`
	Deps       []string   `json:"deps"`
	Error      string     `json:"error,omitempty"`
	Output     []byte     `json:"output,omitempty"` // raw JSON of the job's Run output
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`

	// Warm-pool dispatch; zero on laptop / K8sRunner paths.
	ReadyAt        *time.Time `json:"ready_at,omitempty"`
	ClaimedBy      string     `json:"claimed_by,omitempty"`
	LeaseExpiresAt *time.Time `json:"lease_expires_at,omitempty"`

	// NeedsLabels: runner labels required (AND semantics). Empty = any.
	NeedsLabels []string `json:"needs_labels,omitempty"`

	// StatusDetail: phase string for the dashboard.
	StatusDetail string `json:"status_detail,omitempty"`
	// LastHeartbeat: liveness for UI; LeaseExpiresAt is for ownership.
	LastHeartbeat *time.Time `json:"last_heartbeat,omitempty"`

	// FailureReason: Failure* constant; empty = no structured reason.
	FailureReason string `json:"failure_reason,omitempty"`
	// ExitCode: process exit; nil when not applicable.
	ExitCode *int `json:"exit_code,omitempty"`

	// Annotations is the accumulated list of summary strings emitted
	// by sparkwing.Annotate during the node's execution. Each call
	// appends one entry; order preserved. Surfaced to the dashboard
	// alongside the node's status.
	Annotations []string `json:"annotations,omitempty"`

	// Summary is the latest markdown run summary emitted by
	// sparkwing.Summary while the node was running outside any step
	// body. Overwrite-on-write: only the last value is kept. Empty
	// when no node-scoped summary was emitted; step-scoped summaries
	// live on NodeStep.Summary instead.
	Summary string `json:"summary,omitempty"`
}

// CreateNode inserts a node in the "pending" state.
func (s *Store) CreateNode(ctx context.Context, n Node) error {
	depsJSON, _ := json.Marshal(n.Deps)
	var labelsJSON []byte
	if len(n.NeedsLabels) > 0 {
		labelsJSON, _ = json.Marshal(n.NeedsLabels)
	}
	_, err := s.exec(ctx, `
INSERT INTO nodes (run_id, node_id, status, deps_json, needs_labels)
VALUES (?,?,?,?,?)`, n.RunID, n.NodeID, n.Status, depsJSON, labelsJSON)
	return err
}

// StartNode marks a node as running.
func (s *Store) StartNode(ctx context.Context, runID, nodeID string) error {
	_, err := s.exec(ctx, `
UPDATE nodes SET status = 'running', started_at = ? WHERE run_id = ? AND node_id = ?`,
		time.Now().UnixNano(), runID, nodeID)
	return err
}

// SetNodeStatus updates only the status column.
func (s *Store) SetNodeStatus(ctx context.Context, runID, nodeID, status string) error {
	_, err := s.exec(ctx,
		`UPDATE nodes SET status = ? WHERE run_id = ? AND node_id = ?`,
		status, runID, nodeID)
	return err
}

// UpdateNodeDeps rewrites a node's stored dependency list.
func (s *Store) UpdateNodeDeps(ctx context.Context, runID, nodeID string, deps []string) error {
	depsJSON, _ := json.Marshal(deps)
	_, err := s.exec(ctx,
		`UPDATE nodes SET deps_json = ? WHERE run_id = ? AND node_id = ?`,
		depsJSON, runID, nodeID)
	return err
}

// FinishNode marks terminal with outcome + optional output/error.
func (s *Store) FinishNode(ctx context.Context, runID, nodeID, outcome, errMsg string, output []byte) error {
	return s.FinishNodeWithReason(ctx, runID, nodeID, outcome, errMsg, output, FailureUnknown, nil)
}

// FinishNodeWithReason additionally records a Failure* code + exit.
func (s *Store) FinishNodeWithReason(ctx context.Context, runID, nodeID, outcome, errMsg string, output []byte, reason string, exitCode *int) error {
	var code any
	if exitCode != nil {
		code = *exitCode
	}
	_, err := s.exec(ctx, `
UPDATE nodes
   SET status = 'done', outcome = ?, error = ?, output_json = ?, finished_at = ?,
       failure_reason = ?, exit_code = ?
 WHERE run_id = ? AND node_id = ?`,
		outcome, errMsg, output, time.Now().UnixNano(),
		reason, code,
		runID, nodeID)
	return err
}

// ListNodes returns the nodes for a run in insertion order.
func (s *Store) ListNodes(ctx context.Context, runID string) ([]*Node, error) {
	rows, err := s.query(ctx, `
SELECT run_id, node_id, status, outcome, deps_json, error, output_json, started_at, finished_at,
       ready_at, claimed_by, lease_expires_at, needs_labels, status_detail, last_heartbeat,
       failure_reason, exit_code, annotations_json, summary
  FROM nodes
 WHERE run_id = ?
 ORDER BY `+s.insertionOrderColumn(), runID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []*Node
	for rows.Next() {
		n := &Node{}
		if err := scanNodeRow(rows, n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// GetNode fetches a single node row; ErrNotFound when missing.
func (s *Store) GetNode(ctx context.Context, runID, nodeID string) (*Node, error) {
	row := s.queryRow(ctx, `
SELECT run_id, node_id, status, outcome, deps_json, error, output_json, started_at, finished_at,
       ready_at, claimed_by, lease_expires_at, needs_labels, status_detail, last_heartbeat,
       failure_reason, exit_code, annotations_json, summary
  FROM nodes
 WHERE run_id = ? AND node_id = ?`, runID, nodeID)
	n := &Node{}
	if err := scanNodeRow(row, n); err != nil {
		return nil, err
	}
	return n, nil
}

// scanNodeRow reads one row into n.
func scanNodeRow(rs rowScanner, n *Node) error {
	var depsJSON, outputJSON, labelsJSON, annotationsJSON []byte
	var startedNS, finishedNS, readyNS, leaseNS, heartbeatNS sql.NullInt64
	var claimedBy sql.NullString
	var exitCode sql.NullInt64
	err := rs.Scan(&n.RunID, &n.NodeID, &n.Status, &n.Outcome,
		&depsJSON, &n.Error, &outputJSON, &startedNS, &finishedNS,
		&readyNS, &claimedBy, &leaseNS, &labelsJSON,
		&n.StatusDetail, &heartbeatNS,
		&n.FailureReason, &exitCode, &annotationsJSON, &n.Summary)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	_ = json.Unmarshal(depsJSON, &n.Deps)
	n.Output = outputJSON
	if len(labelsJSON) > 0 {
		_ = json.Unmarshal(labelsJSON, &n.NeedsLabels)
	}
	if startedNS.Valid {
		t := time.Unix(0, startedNS.Int64)
		n.StartedAt = &t
	}
	if finishedNS.Valid {
		t := time.Unix(0, finishedNS.Int64)
		n.FinishedAt = &t
	}
	if readyNS.Valid {
		t := time.Unix(0, readyNS.Int64)
		n.ReadyAt = &t
	}
	if claimedBy.Valid {
		n.ClaimedBy = claimedBy.String
	}
	if leaseNS.Valid {
		t := time.Unix(0, leaseNS.Int64)
		n.LeaseExpiresAt = &t
	}
	if heartbeatNS.Valid {
		t := time.Unix(0, heartbeatNS.Int64)
		n.LastHeartbeat = &t
	}
	if exitCode.Valid {
		v := int(exitCode.Int64)
		n.ExitCode = &v
	}
	if len(annotationsJSON) > 0 {
		_ = json.Unmarshal(annotationsJSON, &n.Annotations)
	}
	return nil
}

// AppendNodeAnnotation appends one annotation string to the node's
// annotations list. Implemented as read-modify-write inside a single
// transaction so concurrent appenders don't lose entries.
func (s *Store) AppendNodeAnnotation(ctx context.Context, runID, nodeID, msg string) error {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var current []byte
	row := tx.QueryRowContext(ctx,
		`SELECT annotations_json FROM nodes WHERE run_id = ? AND node_id = ?`,
		runID, nodeID)
	if err := row.Scan(&current); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	var list []string
	if len(current) > 0 {
		if err := json.Unmarshal(current, &list); err != nil {
			list = nil
		}
	}
	list = append(list, msg)
	next, err := json.Marshal(list)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE nodes SET annotations_json = ? WHERE run_id = ? AND node_id = ?`,
		next, runID, nodeID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE runs SET annotation_count = annotation_count + 1, top_annotation = ? WHERE id = ?`,
		msg, runID); err != nil {
		return err
	}
	if err := appendRunAnnotation(tx, runID, msg); err != nil {
		return err
	}
	return tx.Commit()
}

// SetNodeSummary replaces the node's markdown summary with md.
// Overwrite-on-write: later calls supersede earlier ones. Returns
// ErrNotFound if the node row doesn't exist. Driven by
// sparkwing.Summary() emitted outside any step body.
func (s *Store) SetNodeSummary(ctx context.Context, runID, nodeID, md string) error {
	res, err := s.exec(ctx,
		`UPDATE nodes SET summary = ? WHERE run_id = ? AND node_id = ?`,
		md, runID, nodeID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// Step status constants. Steps are inserted as StepRunning on
// step_start and transitioned to passed/failed on step_end. Skipped
// steps insert directly as StepSkipped with started_at == finished_at.
const (
	StepRunning = "running"
	StepPassed  = "passed"
	StepFailed  = "failed"
	StepSkipped = "skipped"
)

// NodeStep is one row from the node_steps table: per-step runtime
// state for the inner-Work DAG. Status moves running -> passed/failed
// once; skipped is terminal at insert.
type NodeStep struct {
	RunID       string     `json:"run_id,omitempty"`
	NodeID      string     `json:"node_id"`
	StepID      string     `json:"step_id"`
	Status      string     `json:"status"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
	Annotations []string   `json:"annotations,omitempty"`
	// Summary is the latest markdown run summary emitted by
	// sparkwing.Summary inside this step's body. Overwrite-on-write:
	// only the last value is kept.
	Summary string `json:"summary,omitempty"`
}

// StartNodeStep inserts a row in the running state, stamping
// started_at. Idempotent: a repeat call for the same (run, node,
// step) is a no-op, leaving the original started_at intact so a
// retry doesn't reset the clock.
func (s *Store) StartNodeStep(ctx context.Context, runID, nodeID, stepID string) error {
	_, err := s.exec(ctx, `
INSERT INTO node_steps (run_id, node_id, step_id, status, started_at)
VALUES (?,?,?,?,?)
ON CONFLICT(run_id, node_id, step_id) DO NOTHING`,
		runID, nodeID, stepID, StepRunning, time.Now().UnixNano())
	return err
}

// FinishNodeStep transitions a running step to passed/failed and
// stamps finished_at. Caller passes StepPassed or StepFailed.
// Creates the row if missing so the rare reorder where step_end
// lands before step_start still records terminal state.
func (s *Store) FinishNodeStep(ctx context.Context, runID, nodeID, stepID, status string) error {
	now := time.Now().UnixNano()
	_, err := s.exec(ctx, `
INSERT INTO node_steps (run_id, node_id, step_id, status, started_at, finished_at)
VALUES (?,?,?,?,?,?)
ON CONFLICT(run_id, node_id, step_id) DO UPDATE SET
    status      = excluded.status,
    finished_at = excluded.finished_at`,
		runID, nodeID, stepID, status, now, now)
	return err
}

// SkipNodeStep marks a step as skipped (single insert; no running
// phase). started_at == finished_at == now so duration computes to 0
// without special-casing nulls in the wire-shape serializer.
func (s *Store) SkipNodeStep(ctx context.Context, runID, nodeID, stepID string) error {
	now := time.Now().UnixNano()
	_, err := s.exec(ctx, `
INSERT INTO node_steps (run_id, node_id, step_id, status, started_at, finished_at)
VALUES (?,?,?,?,?,?)
ON CONFLICT(run_id, node_id, step_id) DO UPDATE SET
    status      = excluded.status,
    finished_at = excluded.finished_at`,
		runID, nodeID, stepID, StepSkipped, now, now)
	return err
}

// ListNodeSteps returns every step row for the run, across all
// nodes. Returned in (node_id, started_at) order so callers can
// stream-bucket by node without a second sort.
func (s *Store) ListNodeSteps(ctx context.Context, runID string) ([]*NodeStep, error) {
	rows, err := s.query(ctx, `
SELECT node_id, step_id, status, started_at, finished_at, annotations_json, summary
FROM node_steps
WHERE run_id = ?
ORDER BY node_id, started_at`, runID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []*NodeStep
	for rows.Next() {
		ns := &NodeStep{RunID: runID}
		var started, finished sql.NullInt64
		var annotations []byte
		if err := rows.Scan(&ns.NodeID, &ns.StepID, &ns.Status, &started, &finished, &annotations, &ns.Summary); err != nil {
			return nil, err
		}
		if started.Valid {
			t := time.Unix(0, started.Int64)
			ns.StartedAt = &t
		}
		if finished.Valid {
			t := time.Unix(0, finished.Int64)
			ns.FinishedAt = &t
		}
		if len(annotations) > 0 {
			_ = json.Unmarshal(annotations, &ns.Annotations)
		}
		out = append(out, ns)
	}
	return out, rows.Err()
}

// AppendStepAnnotation appends one summary string to a step's
// annotations list. Inserts a placeholder row if the step doesn't
// yet exist (annotations may fire before step_start lands in the
// rare reorder case). Read-modify-write inside one txn to keep
// concurrent appenders from losing entries.
func (s *Store) AppendStepAnnotation(ctx context.Context, runID, nodeID, stepID, msg string) error {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `
INSERT INTO node_steps (run_id, node_id, step_id, status)
VALUES (?,?,?,?)
ON CONFLICT(run_id, node_id, step_id) DO NOTHING`,
		runID, nodeID, stepID, StepRunning); err != nil {
		return err
	}
	var current []byte
	row := tx.QueryRowContext(ctx, `
SELECT annotations_json FROM node_steps
WHERE run_id = ? AND node_id = ? AND step_id = ?`,
		runID, nodeID, stepID)
	if err := row.Scan(&current); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	var list []string
	if len(current) > 0 {
		if err := json.Unmarshal(current, &list); err != nil {
			list = nil
		}
	}
	list = append(list, msg)
	next, err := json.Marshal(list)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE node_steps SET annotations_json = ?
WHERE run_id = ? AND node_id = ? AND step_id = ?`,
		next, runID, nodeID, stepID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE runs SET annotation_count = annotation_count + 1, top_annotation = ? WHERE id = ?`,
		msg, runID); err != nil {
		return err
	}
	if err := appendRunAnnotation(tx, runID, msg); err != nil {
		return err
	}
	return tx.Commit()
}

// SetStepSummary replaces a step's markdown summary with md.
// Overwrite-on-write: later calls supersede earlier ones. Inserts a
// placeholder row if the step doesn't yet exist (a summary may fire
// before step_start lands in the rare reorder case), matching the
// pattern AppendStepAnnotation uses. Driven by sparkwing.Summary()
// emitted inside a step body.
func (s *Store) SetStepSummary(ctx context.Context, runID, nodeID, stepID, md string) error {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `
INSERT INTO node_steps (run_id, node_id, step_id, status)
VALUES (?,?,?,?)
ON CONFLICT(run_id, node_id, step_id) DO NOTHING`,
		runID, nodeID, stepID, StepRunning); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE node_steps SET summary = ?
WHERE run_id = ? AND node_id = ? AND step_id = ?`,
		md, runID, nodeID, stepID); err != nil {
		return err
	}
	return tx.Commit()
}

// MarkNodeReady stamps ready_at if unset. Idempotent.
func (s *Store) MarkNodeReady(ctx context.Context, runID, nodeID string) error {
	res, err := s.exec(
		ctx,
		`UPDATE nodes SET ready_at = COALESCE(ready_at, ?)
		  WHERE run_id = ? AND node_id = ?`,
		time.Now().UnixNano(), runID, nodeID,
	)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// RevokeNodeReady clears ready_at when unclaimed. Returns false when
// a pod already claimed the node.
func (s *Store) RevokeNodeReady(ctx context.Context, runID, nodeID string) (bool, error) {
	res, err := s.exec(
		ctx,
		`UPDATE nodes SET ready_at = NULL
		  WHERE run_id = ? AND node_id = ?
		    AND claimed_by IS NULL AND status != 'done'`,
		runID, nodeID,
	)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// ClaimNextReadyNode flips the oldest claimable node to holderID with
// a fresh lease. Claimable = ready_at set, unclaimed, !done, and
// every needs_labels entry appears in runnerLabels. ErrNotFound when
// no candidate matches. Label-mismatched candidates have their
// ready_at bumped 1us so they don't starve the FIFO queue.
func (s *Store) ClaimNextReadyNode(ctx context.Context, holderID string, lease time.Duration, runnerLabels []string) (*Node, error) {
	if lease <= 0 {
		lease = DefaultLeaseDuration
	}
	labelSet := make(map[string]struct{}, len(runnerLabels))
	for _, l := range runnerLabels {
		if l != "" {
			labelSet[l] = struct{}{}
		}
	}

	const maxCandidates = 64
	for range maxCandidates {
		tx, err := s.beginTx(ctx)
		if err != nil {
			return nil, err
		}
		n := &Node{}
		err = scanNodeRow(tx.QueryRowContext(ctx, `
SELECT run_id, node_id, status, outcome, deps_json, error, output_json, started_at, finished_at,
       ready_at, claimed_by, lease_expires_at, needs_labels, status_detail, last_heartbeat,
       failure_reason, exit_code, annotations_json, summary
  FROM nodes
 WHERE ready_at IS NOT NULL AND claimed_by IS NULL AND status != 'done'
 ORDER BY ready_at ASC
 LIMIT 1`+s.forUpdateSkipLocked()), n)
		if err != nil {
			_ = tx.Rollback()
			return nil, err
		}

		if !labelsSatisfied(n.NeedsLabels, labelSet) {
			bump := time.Now().UnixNano()
			if n.ReadyAt != nil {
				cand := n.ReadyAt.UnixNano() + int64(time.Microsecond)
				bump = max(bump, cand)
			}
			if _, err := tx.ExecContext(
				ctx,
				`UPDATE nodes SET ready_at = ?
				  WHERE run_id = ? AND node_id = ? AND claimed_by IS NULL`,
				bump, n.RunID, n.NodeID,
			); err != nil {
				_ = tx.Rollback()
				return nil, err
			}
			if err := tx.Commit(); err != nil {
				return nil, err
			}
			continue
		}

		now := time.Now()
		expires := now.Add(lease)
		if _, err := tx.ExecContext(
			ctx,
			`UPDATE nodes SET claimed_by = ?, lease_expires_at = ?
			  WHERE run_id = ? AND node_id = ? AND claimed_by IS NULL`,
			holderID, expires.UnixNano(), n.RunID, n.NodeID,
		); err != nil {
			_ = tx.Rollback()
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		n.ClaimedBy = holderID
		n.LeaseExpiresAt = &expires
		return n, nil
	}
	return nil, ErrNotFound
}

// labelsSatisfied reports whether the have label set satisfies the
// needed label expression. Each entry in needed is a single term;
// within a term, comma-separated values are alternatives (OR), and
// across terms results compose with AND. Empty or nil needed matches
// any have (including empty). Mirrors sparkwingruntime.MatchLabels; kept
// in-package to avoid an import cycle between store and sparkwing.
func labelsSatisfied(needed []string, have map[string]struct{}) bool {
	for _, term := range needed {
		if term == "" {
			continue
		}
		if !labelTermSatisfied(term, have) {
			return false
		}
	}
	return true
}

func labelTermSatisfied(term string, have map[string]struct{}) bool {
	if !strings.ContainsRune(term, ',') {
		_, ok := have[strings.TrimSpace(term)]
		return ok
	}
	for _, alt := range strings.Split(term, ",") {
		alt = strings.TrimSpace(alt)
		if alt == "" {
			continue
		}
		if _, ok := have[alt]; ok {
			return true
		}
	}
	return false
}

// UpdateNodeActivity sets status_detail and bumps last_heartbeat.
func (s *Store) UpdateNodeActivity(ctx context.Context, runID, nodeID, detail string) error {
	_, err := s.exec(ctx,
		`UPDATE nodes SET status_detail = ?, last_heartbeat = ?
		  WHERE run_id = ? AND node_id = ?`,
		detail, time.Now().UnixNano(), runID, nodeID)
	return err
}

// TouchNodeHeartbeat stamps last_heartbeat=now.
func (s *Store) TouchNodeHeartbeat(ctx context.Context, runID, nodeID string) error {
	_, err := s.exec(ctx,
		`UPDATE nodes SET last_heartbeat = ? WHERE run_id = ? AND node_id = ?`,
		time.Now().UnixNano(), runID, nodeID)
	return err
}

// HeartbeatNodeClaim extends the claim lease; ErrLockHeld when the
// caller no longer owns the claim.
func (s *Store) HeartbeatNodeClaim(ctx context.Context, runID, nodeID, holderID string, lease time.Duration) error {
	if lease <= 0 {
		lease = DefaultLeaseDuration
	}
	expires := time.Now().Add(lease).UnixNano()
	res, err := s.exec(
		ctx,
		`UPDATE nodes SET lease_expires_at = ?
		  WHERE run_id = ? AND node_id = ? AND claimed_by = ?`,
		expires, runID, nodeID, holderID,
	)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrLockHeld
	}
	return nil
}

// ReapExpiredNodeClaims clears claimed_by/lease_expires_at on expired
// claims; ready_at is left intact. Returns reaped pairs.
func (s *Store) ReapExpiredNodeClaims(ctx context.Context) ([][2]string, error) {
	now := time.Now().UnixNano()
	tx, err := s.beginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.QueryContext(ctx,
		`SELECT run_id, node_id FROM nodes
		  WHERE claimed_by IS NOT NULL AND lease_expires_at IS NOT NULL
		    AND lease_expires_at < ? AND status != 'done'`+s.forUpdateSkipLocked(),
		now)
	if err != nil {
		return nil, err
	}
	var pairs [][2]string
	for rows.Next() {
		var rid, nid string
		if err := rows.Scan(&rid, &nid); err != nil {
			_ = rows.Close()
			return nil, err
		}
		pairs = append(pairs, [2]string{rid, nid})
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(pairs) == 0 {
		return nil, nil
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE nodes SET claimed_by = NULL, lease_expires_at = NULL
		  WHERE claimed_by IS NOT NULL AND lease_expires_at IS NOT NULL
		    AND lease_expires_at < ? AND status != 'done'`,
		now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return pairs, nil
}

// failExpiredNodeClaims terminates expired claims with FailureAgentLost.
func (s *Store) failExpiredNodeClaims(ctx context.Context) ([][2]string, error) {
	now := time.Now().UnixNano()
	tx, err := s.beginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.QueryContext(ctx,
		`SELECT run_id, node_id FROM nodes
		  WHERE claimed_by IS NOT NULL AND lease_expires_at IS NOT NULL
		    AND lease_expires_at < ? AND status != 'done'`+s.forUpdateSkipLocked(),
		now)
	if err != nil {
		return nil, err
	}
	var pairs [][2]string
	for rows.Next() {
		var rid, nid string
		if err := rows.Scan(&rid, &nid); err != nil {
			_ = rows.Close()
			return nil, err
		}
		pairs = append(pairs, [2]string{rid, nid})
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(pairs) == 0 {
		return nil, nil
	}
	for _, p := range pairs {
		if _, err := tx.ExecContext(ctx, `
UPDATE nodes
   SET status = 'done', outcome = 'failed',
       error = 'runner heartbeat expired',
       failure_reason = ?, finished_at = ?,
       claimed_by = NULL, lease_expires_at = NULL
 WHERE run_id = ? AND node_id = ? AND status != 'done'`,
			FailureAgentLost, now, p[0], p[1]); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return pairs, nil
}

// failNodesInRun marks every non-terminal node in runID as failed.
// Used by the reaper to avoid zombie nodes when a worker lease expires.
func (s *Store) failNodesInRun(ctx context.Context, runID, errMsg, failureReason string) ([]string, error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.QueryContext(ctx,
		`SELECT node_id FROM nodes WHERE run_id = ? AND status != 'done'`, runID)
	if err != nil {
		return nil, err
	}
	var nodeIDs []string
	for rows.Next() {
		var nid string
		if err := rows.Scan(&nid); err != nil {
			_ = rows.Close()
			return nil, err
		}
		nodeIDs = append(nodeIDs, nid)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(nodeIDs) == 0 {
		return nil, nil
	}
	now := time.Now().UnixNano()
	for _, nid := range nodeIDs {
		if _, err := tx.ExecContext(ctx, `
UPDATE nodes
   SET status = 'done', outcome = 'failed',
       error = ?, failure_reason = ?, finished_at = ?,
       ready_at = NULL
 WHERE run_id = ? AND node_id = ? AND status != 'done'`,
			errMsg, failureReason, now, runID, nid); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return nodeIDs, nil
}

// failStaleQueuedNodes terminates unclaimed nodes whose ready_at is
// older than olderThan with FailureQueueTimeout.
func (s *Store) failStaleQueuedNodes(ctx context.Context, olderThan time.Duration) ([][2]string, error) {
	if olderThan <= 0 {
		return nil, nil
	}
	threshold := time.Now().Add(-olderThan).UnixNano()
	tx, err := s.beginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.QueryContext(ctx,
		`SELECT run_id, node_id FROM nodes
		  WHERE ready_at IS NOT NULL AND claimed_by IS NULL
		    AND ready_at < ? AND status != 'done'`,
		threshold)
	if err != nil {
		return nil, err
	}
	var pairs [][2]string
	for rows.Next() {
		var rid, nid string
		if err := rows.Scan(&rid, &nid); err != nil {
			_ = rows.Close()
			return nil, err
		}
		pairs = append(pairs, [2]string{rid, nid})
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(pairs) == 0 {
		return nil, nil
	}
	now := time.Now().UnixNano()
	for _, p := range pairs {
		if _, err := tx.ExecContext(ctx, `
UPDATE nodes
   SET status = 'done', outcome = 'failed',
       error = 'no runner claimed this node before the queue deadline',
       failure_reason = ?, finished_at = ?,
       ready_at = NULL
 WHERE run_id = ? AND node_id = ? AND claimed_by IS NULL AND status != 'done'`,
			FailureQueueTimeout, now, p[0], p[1]); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return pairs, nil
}

// --- Events ---

// Event is one audit/wire record for a run; Seq is per-run monotonic.
type Event struct {
	RunID   string          `json:"run_id"`
	Seq     int64           `json:"seq"`
	NodeID  string          `json:"node_id,omitempty"`
	Kind    string          `json:"kind"`
	TS      time.Time       `json:"ts"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// ListEventsAfter returns events with seq > afterSeq, ascending.
// Pass 0 for full backlog; empty slice when there's nothing new.
func (s *Store) ListEventsAfter(ctx context.Context, runID string, afterSeq int64, limit int) ([]Event, error) {
	if limit <= 0 {
		limit = 500
	}
	rows, err := s.query(ctx, `
SELECT run_id, seq, node_id, kind, ts, payload
  FROM events
 WHERE run_id = ? AND seq > ?
 ORDER BY seq ASC
 LIMIT ?`, runID, afterSeq, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Event
	for rows.Next() {
		var e Event
		var tsNanos int64
		var payload []byte
		if err := rows.Scan(&e.RunID, &e.Seq, &e.NodeID, &e.Kind, &tsNanos, &payload); err != nil {
			return nil, err
		}
		e.TS = time.Unix(0, tsNanos)
		if len(payload) > 0 {
			// Several AppendEvent callsites write plain strings (error
			// reason text, "upstream-failed" markers) rather than JSON
			// objects. json.RawMessage refuses to marshal anything that
			// isn't valid JSON, so leaving the raw bytes in place would
			// break the entire response any time one of those events is
			// included. Wrap unparseable payloads as JSON strings so
			// readers get the original text and the list response can
			// always be encoded cleanly.
			if json.Valid(payload) {
				e.Payload = json.RawMessage(payload)
			} else {
				wrapped, _ := json.Marshal(string(payload))
				e.Payload = json.RawMessage(wrapped)
			}
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// AppendEvent writes an ordered event; returns the assigned seq.
func (s *Store) AppendEvent(ctx context.Context, runID, nodeID, kind string, payload []byte) (int64, error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	var seq int64
	err = tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(seq), 0) + 1 FROM events WHERE run_id = ?`, runID).Scan(&seq)
	if err != nil {
		return 0, err
	}

	_, err = tx.ExecContext(ctx, `
INSERT INTO events (run_id, seq, node_id, kind, ts, payload)
VALUES (?,?,?,?,?,?)`, runID, seq, nodeID, kind, time.Now().UnixNano(), payload)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return seq, nil
}

// ErrNotFound is returned when a lookup misses.
var ErrNotFound = errors.New("not found")

// --- Debug pauses ---

// Pause reasons; exported wire values.
const (
	PauseReasonBefore    = "pause-before"
	PauseReasonAfter     = "pause-after"
	PauseReasonOnFailure = "pause-on-failure"
)

// PauseRelease kinds: how the pause ended.
const (
	PauseReleaseManual  = "manual"
	PauseReleaseTimeout = "timeout-released"
)

// DebugPause is one row in the debug_pauses table.
type DebugPause struct {
	RunID       string     `json:"run_id"`
	NodeID      string     `json:"node_id"`
	Reason      string     `json:"reason"`
	PausedAt    time.Time  `json:"paused_at"`
	ExpiresAt   time.Time  `json:"expires_at"`
	ReleasedAt  *time.Time `json:"released_at,omitempty"`
	ReleasedBy  string     `json:"released_by,omitempty"`
	ReleaseKind string     `json:"release_kind,omitempty"`
}

// CreateDebugPause inserts (or upserts) an open pause row.
func (s *Store) CreateDebugPause(ctx context.Context, p DebugPause) error {
	_, err := s.exec(ctx, `
INSERT INTO debug_pauses (run_id, node_id, reason, paused_at, expires_at)
VALUES (?,?,?,?,?)
ON CONFLICT(run_id, node_id, reason) DO UPDATE SET
    paused_at = excluded.paused_at,
    expires_at = excluded.expires_at,
    released_at = NULL,
    released_by = '',
    release_kind = ''`,
		p.RunID, p.NodeID, p.Reason,
		p.PausedAt.UnixNano(), p.ExpiresAt.UnixNano())
	return err
}

// GetActiveDebugPause returns the open pause for a node, if any.
func (s *Store) GetActiveDebugPause(ctx context.Context, runID, nodeID string) (*DebugPause, error) {
	row := s.queryRow(ctx, `
SELECT run_id, node_id, reason, paused_at, expires_at, released_at, released_by, release_kind
  FROM debug_pauses
 WHERE run_id = ? AND node_id = ? AND released_at IS NULL
 ORDER BY paused_at DESC
 LIMIT 1`, runID, nodeID)
	return scanDebugPause(row)
}

// ListDebugPauses returns all pause rows for a run, newest first.
func (s *Store) ListDebugPauses(ctx context.Context, runID string) ([]*DebugPause, error) {
	rows, err := s.query(ctx, `
SELECT run_id, node_id, reason, paused_at, expires_at, released_at, released_by, release_kind
  FROM debug_pauses
 WHERE run_id = ?
 ORDER BY paused_at DESC`, runID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []*DebugPause
	for rows.Next() {
		p, err := scanDebugPause(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ReleaseDebugPause closes the open pause; ErrNotFound when none.
func (s *Store) ReleaseDebugPause(ctx context.Context, runID, nodeID, releasedBy, kind string) error {
	res, err := s.exec(ctx, `
UPDATE debug_pauses
   SET released_at = ?, released_by = ?, release_kind = ?
 WHERE run_id = ? AND node_id = ? AND released_at IS NULL`,
		time.Now().UnixNano(), releasedBy, kind, runID, nodeID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func scanDebugPause(rs rowScanner) (*DebugPause, error) {
	var p DebugPause
	var pausedNS, expiresNS int64
	var releasedNS sql.NullInt64
	err := rs.Scan(&p.RunID, &p.NodeID, &p.Reason,
		&pausedNS, &expiresNS, &releasedNS, &p.ReleasedBy, &p.ReleaseKind)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	p.PausedAt = time.Unix(0, pausedNS)
	p.ExpiresAt = time.Unix(0, expiresNS)
	if releasedNS.Valid {
		t := time.Unix(0, releasedNS.Int64)
		p.ReleasedAt = &t
	}
	return &p, nil
}

// --- Triggers ---

// Trigger is one row in the triggers table; ID becomes the run ID.
type Trigger struct {
	ID             string            `json:"id"`
	Pipeline       string            `json:"pipeline"`
	Args           map[string]string `json:"args,omitempty"`
	TriggerSource  string            `json:"trigger_source,omitempty"`
	TriggerUser    string            `json:"trigger_user,omitempty"`
	TriggerEnv     map[string]string `json:"trigger_env,omitempty"`
	GitBranch      string            `json:"git_branch,omitempty"`
	GitSHA         string            `json:"git_sha,omitempty"`
	Status         string            `json:"status"`
	CreatedAt      time.Time         `json:"created_at"`
	ClaimedAt      *time.Time        `json:"claimed_at,omitempty"`
	LeaseExpiresAt *time.Time        `json:"lease_expires_at,omitempty"`
	// ParentRunID: spawning RunAndAwait; for cycle detection.
	ParentRunID string `json:"parent_run_id,omitempty"`
	// Mirror of Run repo fields; threaded into CreateRun.
	Repo        string `json:"repo,omitempty"`
	RepoURL     string `json:"repo_url,omitempty"`
	GithubOwner string `json:"github_owner,omitempty"`
	GithubRepo  string `json:"github_repo,omitempty"`
	// RetryOf is threaded into the persisted Run row.
	RetryOf string `json:"retry_of,omitempty"`
	// RetrySource is "manual" or "auto".
	RetrySource string `json:"retry_source,omitempty"`
	// ParentNodeID: which parent node spawned this; for retry-lineage
	// chaining across nested spawns.
	ParentNodeID string `json:"parent_node_id,omitempty"`
	// Full: "rerun all" mode for manual retries. When true, the
	// orchestrator ignores skip-passed rehydration and re-executes
	// every node even though retry_of is set. The dashboard's
	// "Rerun all" choice flips this; "Rerun from failed" leaves
	// it false (the default).
	Full bool `json:"full,omitempty"`
}

// DefaultLeaseDuration is the claim lease TTL. Wide enough to survive
// CPU-bound pauses; short enough to re-queue dead runners.
const DefaultLeaseDuration = 3 * time.Minute

// CreateTrigger inserts a new trigger with status='pending'.
func (s *Store) CreateTrigger(ctx context.Context, t Trigger) error {
	argsJSON, _ := json.Marshal(t.Args)
	envJSON, _ := json.Marshal(t.TriggerEnv)
	status := t.Status
	if status == "" {
		status = "pending"
	}
	var parent sql.NullString
	if t.ParentRunID != "" {
		parent = sql.NullString{String: t.ParentRunID, Valid: true}
	}
	fullInt := 0
	if t.Full {
		fullInt = 1
	}
	_, err := s.exec(
		ctx, `
INSERT INTO triggers (id, pipeline, args_json, trigger_source, trigger_user,
                      trigger_env, git_branch, git_sha, status, created_at, parent_run_id,
                      repo, repo_url, github_owner, github_repo, retry_of, retry_source, parent_node_id, "full")
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		t.ID, t.Pipeline, argsJSON, t.TriggerSource, t.TriggerUser,
		envJSON, t.GitBranch, t.GitSHA, status, t.CreatedAt.UnixNano(), parent,
		t.Repo, t.RepoURL, t.GithubOwner, t.GithubRepo, t.RetryOf, t.RetrySource, t.ParentNodeID, fullInt,
	)
	return err
}

// SpawnedChild is one row of the cross-pipeline spawn relation. A
// node X in run R that invoked sparkwing.RunAndAwait("target") yields
// a SpawnedChild{ParentNodeID: X, Pipeline: "target", ChildRunID: ...}
// for each invocation. Surfaced to the dashboard so a node carrying
// a cross-pipeline call paints a distinct corner pill.
type SpawnedChild struct {
	ParentNodeID string `json:"parent_node_id"`
	Pipeline     string `json:"pipeline"`
	ChildRunID   string `json:"child_run_id"`
}

// ListSpawnedChildrenByRun returns every cross-pipeline spawn the
// nodes of runID triggered. Each child trigger was enqueued by an
// awaiter inside a parent node's body; the row carries parent_node_id
// + pipeline so the caller can attribute the spawn back to its node.
// Ordered by parent_node_id, created_at so callers can stream-bucket.
func (s *Store) ListSpawnedChildrenByRun(ctx context.Context, runID string) ([]SpawnedChild, error) {
	rows, err := s.query(ctx, `
SELECT parent_node_id, pipeline, id
FROM triggers
WHERE parent_run_id = ? AND parent_node_id != ''
ORDER BY parent_node_id, created_at`, runID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []SpawnedChild
	for rows.Next() {
		var c SpawnedChild
		if err := rows.Scan(&c.ParentNodeID, &c.Pipeline, &c.ChildRunID); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetRunAncestorPipelines returns ancestor pipeline names from
// parent_run_id walks (excludes runID's own). Missing ancestors and
// data cycles terminate cleanly; partial chains are still useful for
// cycle detection.
func (s *Store) GetRunAncestorPipelines(ctx context.Context, runID string) ([]string, error) {
	if runID == "" {
		return nil, nil
	}
	var out []string
	cur := runID
	const maxDepth = 64 // generous; real chains are <=5 in practice
	for range maxDepth {
		var parent sql.NullString
		var pipeline string
		err := s.queryRow(
			ctx,
			`SELECT pipeline, parent_run_id FROM runs WHERE id = ?`, cur,
		).Scan(&pipeline, &parent)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return out, nil
			}
			return nil, err
		}
		// Skip the seed; callers want only ancestors.
		if cur != runID {
			out = append(out, pipeline)
		}
		if !parent.Valid || parent.String == "" {
			return out, nil
		}
		cur = parent.String
	}
	// Max depth: data is cyclic; partial result still detects cycles.
	return out, nil
}

// ClaimNextTrigger flips the oldest pending trigger to 'claimed'.
// ErrNotFound when empty. lease=0 uses DefaultLeaseDuration.
func (s *Store) ClaimNextTrigger(ctx context.Context, lease time.Duration) (*Trigger, error) {
	return s.ClaimNextTriggerFor(ctx, lease, nil, nil)
}

// ClaimNextTriggerFor adds pipeline/source filter sets (AND semantics).
func (s *Store) ClaimNextTriggerFor(ctx context.Context, lease time.Duration, pipelines, sources []string) (*Trigger, error) {
	if lease <= 0 {
		lease = DefaultLeaseDuration
	}
	tx, err := s.beginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	sel := `
SELECT id, pipeline, args_json, trigger_source, trigger_user,
       trigger_env, git_branch, git_sha, status, created_at, parent_run_id,
       repo, repo_url, github_owner, github_repo, retry_of, retry_source, parent_node_id, "full"
  FROM triggers
 WHERE status = 'pending'`
	args := []any{}
	if len(pipelines) > 0 {
		ph := make([]string, len(pipelines))
		for i, p := range pipelines {
			ph[i] = "?"
			args = append(args, p)
		}
		sel += " AND pipeline IN (" + strings.Join(ph, ",") + ")"
	}
	if len(sources) > 0 {
		ph := make([]string, len(sources))
		for i, src := range sources {
			ph[i] = "?"
			args = append(args, src)
		}
		sel += " AND trigger_source IN (" + strings.Join(ph, ",") + ")"
	}
	sel += `
 ORDER BY created_at ASC
 LIMIT 1` + s.forUpdateSkipLocked()

	var t Trigger
	var argsJSON, envJSON []byte
	var createdNS int64
	var parent sql.NullString
	var fullInt int
	err = tx.QueryRowContext(ctx, sel, args...).Scan(
		&t.ID, &t.Pipeline, &argsJSON, &t.TriggerSource, &t.TriggerUser,
		&envJSON, &t.GitBranch, &t.GitSHA, &t.Status, &createdNS, &parent,
		&t.Repo, &t.RepoURL, &t.GithubOwner, &t.GithubRepo, &t.RetryOf, &t.RetrySource, &t.ParentNodeID, &fullInt,
	)
	if parent.Valid {
		t.ParentRunID = parent.String
	}
	t.Full = fullInt != 0
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}

	now := time.Now()
	expires := now.Add(lease)
	if _, err := tx.ExecContext(
		ctx,
		`UPDATE triggers SET status = 'claimed', claimed_at = ?, lease_expires_at = ? WHERE id = ?`,
		now.UnixNano(), expires.UnixNano(), t.ID,
	); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	t.Status = "claimed"
	t.CreatedAt = time.Unix(0, createdNS)
	t.ClaimedAt = &now
	t.LeaseExpiresAt = &expires
	if len(argsJSON) > 0 {
		_ = json.Unmarshal(argsJSON, &t.Args)
	}
	if len(envJSON) > 0 {
		_ = json.Unmarshal(envJSON, &t.TriggerEnv)
	}
	return &t, nil
}

// HeartbeatTrigger extends the claim lease and returns whether cancel
// was requested. ErrNotFound when not claimed.
func (s *Store) HeartbeatTrigger(ctx context.Context, id string, lease time.Duration) (cancelled bool, err error) {
	if lease <= 0 {
		lease = DefaultLeaseDuration
	}
	expires := time.Now().Add(lease).UnixNano()

	tx, err := s.beginTx(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx,
		`UPDATE triggers
		    SET lease_expires_at = ?
		  WHERE id = ? AND status = 'claimed'`,
		expires, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if n == 0 {
		return false, ErrNotFound
	}

	var cancelNS sql.NullInt64
	if err := tx.QueryRowContext(
		ctx,
		`SELECT cancel_requested_at FROM triggers WHERE id = ?`, id,
	).Scan(&cancelNS); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return cancelNS.Valid, nil
}

// RequestCancel flags a trigger for cancellation; idempotent.
func (s *Store) RequestCancel(ctx context.Context, id string) error {
	now := time.Now().UnixNano()
	res, err := s.exec(ctx,
		`UPDATE triggers
		    SET cancel_requested_at = COALESCE(cancel_requested_at, ?)
		  WHERE id = ?`,
		now, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// reapExpiredTriggers flips lease-expired claimed triggers back to
// pending. Returns reaped IDs; matching runs are caller-reconciled.
func (s *Store) reapExpiredTriggers(ctx context.Context) ([]string, error) {
	now := time.Now().UnixNano()

	tx, err := s.beginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.QueryContext(ctx,
		`SELECT id FROM triggers
		  WHERE status = 'claimed' AND lease_expires_at IS NOT NULL
		    AND lease_expires_at < ?`,
		now)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE triggers
		    SET status = 'pending',
		        claimed_at = NULL,
		        lease_expires_at = NULL
		  WHERE status = 'claimed' AND lease_expires_at IS NOT NULL
		    AND lease_expires_at < ?`,
		now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return ids, nil
}

// FinishTrigger marks a trigger 'done'; idempotent.
func (s *Store) FinishTrigger(ctx context.Context, id string) error {
	_, err := s.exec(ctx,
		`UPDATE triggers SET status = 'done', lease_expires_at = NULL WHERE id = ?`, id)
	return err
}

// reapTimedOutApprovals resolves approvals whose timeout window has
// elapsed without any human (or live orchestrator) acting first.
// Writes ApprovalResolutionTimedOut + a sentinel approver so a
// re-attached orchestrator can map the resolution to its
// author-configured on_timeout policy via the usual code path. Idle
// approver string ("controller-reaper") distinguishes
// controller-initiated timeouts from orchestrator-initiated ones
// ("sparkwing") in audit logs.
//
// Returns (run_id, node_id) pairs for the approvals that were
// reaped. The caller logs them; no further cleanup is needed at the
// store layer.
//
// Notes for future work: this only resolves the APPROVAL state. If
// the dispatching orchestrator process is fully dead, the run row
// will still sit at status='running' because no one drives the
// downstream node dispatch. A run-level heartbeat reaper would
// catch that case separately.
func (s *Store) reapTimedOutApprovals(ctx context.Context) ([][2]string, error) {
	now := time.Now()
	nowNS := now.UnixNano()

	tx, err := s.beginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.QueryContext(ctx, `
		SELECT run_id, node_id FROM approvals
		WHERE resolved_at IS NULL
		  AND timeout_ms > 0
		  AND requested_at + (timeout_ms * 1000000) < ?
	`, nowNS)
	if err != nil {
		return nil, err
	}
	var pairs [][2]string
	for rows.Next() {
		var rid, nid string
		if err := rows.Scan(&rid, &nid); err != nil {
			_ = rows.Close()
			return nil, err
		}
		pairs = append(pairs, [2]string{rid, nid})
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(pairs) == 0 {
		return nil, nil
	}

	for _, p := range pairs {
		if _, err := tx.ExecContext(ctx, `
			UPDATE approvals
			   SET resolved_at = ?,
			       resolution  = ?,
			       approver    = ?,
			       comment     = ?
			 WHERE run_id = ? AND node_id = ?
			   AND resolved_at IS NULL
		`,
			nowNS,
			ApprovalResolutionTimedOut,
			"controller-reaper",
			"timeout enforced by controller (orchestrator silent past timeout_ms)",
			p[0], p[1],
		); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return pairs, nil
}

// reapStalePendingRuns marks runs failed whose trigger has already
// transitioned to 'done' (terminal) but whose run row never moved
// past 'pending'. This catches the gap a trigger consumer can leave
// behind when it FinishTriggers a claim but crashes / errors before
// (or instead of) calling FinishRun -- e.g. an older runner image
// whose source-fetch path doesn't propagate failure to the run row,
// or any future bug along that boundary. The threshold grace lets
// the normal pending -> running transition complete without a race
// against this sweep.
//
// Returns the run IDs that were reaped. Each reaped run gets
// error="..." set to the supplied reason so operators see why it
// flipped rather than a bare "failed" with no context.
func (s *Store) reapStalePendingRuns(ctx context.Context, grace time.Duration, reason string) ([]string, error) {
	cutoff := time.Now().Add(-grace).UnixNano()
	now := time.Now().UnixNano()

	tx, err := s.beginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.QueryContext(ctx, `
		SELECT r.id FROM runs r
		WHERE r.status = 'pending'
		  AND r.started_at > 0
		  AND r.started_at < ?
		  AND EXISTS (
		      SELECT 1 FROM triggers t
		       WHERE t.id = r.id AND t.status = 'done'
		  )
	`, cutoff)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}

	for _, id := range ids {
		if _, err := tx.ExecContext(ctx,
			`UPDATE runs SET status = 'failed', error = ?, finished_at = ?
			  WHERE id = ? AND status = 'pending'`,
			reason, now, id); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return ids, nil
}

// reapStaleRunningRuns marks runs failed whose last_heartbeat_at is
// older than grace. Catches fully-orphaned dispatching orchestrators
// in Mode 4 (hosted controller): the laptop died between node
// dispatches with no active claim to expire via the node-claim
// reaper. Rows with NULL last_heartbeat_at are ignored -- those
// predate the column or come from backends that don't drive a
// run-level heartbeat (local + S3 modes reconcile orphans via per-
// node heartbeats elsewhere). Each reaped run also has its non-done
// nodes cascade-failed: running -> failed, pending -> cancelled, both
// with failure_reason='orphaned', matching the local orphan
// reconciler so downstream readers don't have to special-case the
// controller-side sweep.
func (s *Store) reapStaleRunningRuns(ctx context.Context, grace time.Duration, reason string) ([]string, error) {
	cutoff := time.Now().Add(-grace).UnixNano()
	now := time.Now().UnixNano()

	rows, err := s.query(ctx, `
SELECT id FROM runs
 WHERE status = 'running'
   AND last_heartbeat_at IS NOT NULL
   AND last_heartbeat_at < ?`, cutoff)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for _, id := range ids {
		if _, err := s.exec(ctx, `
UPDATE nodes
   SET status         = 'done',
       outcome        = 'failed',
       error          = ?,
       failure_reason = 'orphaned',
       finished_at    = ?
 WHERE run_id = ? AND status = 'running'`,
			reason, now, id); err != nil {
			return nil, err
		}
		if _, err := s.exec(ctx, `
UPDATE nodes
   SET status         = 'done',
       outcome        = 'cancelled',
       error          = 'orphaned: orchestrator process exited before this node ran',
       failure_reason = 'orphaned',
       finished_at    = ?
 WHERE run_id = ? AND status = 'pending'`,
			now, id); err != nil {
			return nil, err
		}
		if err := s.FinishRun(ctx, id, "failed", reason); err != nil {
			return nil, err
		}
	}
	return ids, nil
}

// ListPendingTriggersForParent returns every pending trigger whose
// parent_run_id matches parentRunID, oldest first. Used by the
// laptop-local trigger loop to scope its claim queue to the run
// that started it -- without the filter, two parallel local runs
// would steal each other's children. Empty list when no candidates.
func (s *Store) ListPendingTriggersForParent(ctx context.Context, parentRunID string) ([]string, error) {
	rows, err := s.query(ctx, `
SELECT id FROM triggers
 WHERE status = 'pending' AND parent_run_id = ?
 ORDER BY created_at ASC`, parentRunID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ClaimSpecificTrigger flips a known pending trigger to 'claimed';
// ErrNotFound when not pending.
func (s *Store) ClaimSpecificTrigger(ctx context.Context, id string, lease time.Duration) (*Trigger, error) {
	if lease <= 0 {
		lease = DefaultLeaseDuration
	}
	tx, err := s.beginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now()
	expires := now.Add(lease)
	res, err := tx.ExecContext(ctx,
		`UPDATE triggers SET status = 'claimed', claimed_at = ?, lease_expires_at = ?
		  WHERE id = ? AND status = 'pending'`,
		now.UnixNano(), expires.UnixNano(), id)
	if err != nil {
		return nil, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, ErrNotFound
	}

	var t Trigger
	var argsJSON, envJSON []byte
	var createdNS int64
	var parent sql.NullString
	var fullInt int
	if err := tx.QueryRowContext(
		ctx, `
SELECT id, pipeline, args_json, trigger_source, trigger_user,
       trigger_env, git_branch, git_sha, status, created_at, parent_run_id,
       repo, repo_url, github_owner, github_repo, retry_of, retry_source, parent_node_id, "full"
  FROM triggers WHERE id = ?`, id,
	).Scan(&t.ID, &t.Pipeline, &argsJSON, &t.TriggerSource, &t.TriggerUser,
		&envJSON, &t.GitBranch, &t.GitSHA, &t.Status, &createdNS, &parent,
		&t.Repo, &t.RepoURL, &t.GithubOwner, &t.GithubRepo, &t.RetryOf, &t.RetrySource, &t.ParentNodeID, &fullInt); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	if parent.Valid {
		t.ParentRunID = parent.String
	}
	t.Full = fullInt != 0
	t.CreatedAt = time.Unix(0, createdNS)
	t.ClaimedAt = &now
	t.LeaseExpiresAt = &expires
	if len(argsJSON) > 0 {
		_ = json.Unmarshal(argsJSON, &t.Args)
	}
	if len(envJSON) > 0 {
		_ = json.Unmarshal(envJSON, &t.TriggerEnv)
	}
	return &t, nil
}

// GetTrigger fetches a single trigger by ID.
func (s *Store) GetTrigger(ctx context.Context, id string) (*Trigger, error) {
	var t Trigger
	var argsJSON, envJSON []byte
	var createdNS int64
	var claimedNS, leaseNS sql.NullInt64
	var parent sql.NullString
	var fullInt int
	err := s.queryRow(
		ctx, `
SELECT id, pipeline, args_json, trigger_source, trigger_user,
       trigger_env, git_branch, git_sha, status, created_at, claimed_at, lease_expires_at,
       repo, repo_url, github_owner, github_repo, retry_of, retry_source, parent_node_id, parent_run_id, "full"
  FROM triggers WHERE id = ?`, id,
	).Scan(&t.ID, &t.Pipeline, &argsJSON, &t.TriggerSource, &t.TriggerUser,
		&envJSON, &t.GitBranch, &t.GitSHA, &t.Status, &createdNS, &claimedNS, &leaseNS,
		&t.Repo, &t.RepoURL, &t.GithubOwner, &t.GithubRepo, &t.RetryOf, &t.RetrySource, &t.ParentNodeID, &parent, &fullInt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	t.CreatedAt = time.Unix(0, createdNS)
	if claimedNS.Valid {
		ct := time.Unix(0, claimedNS.Int64)
		t.ClaimedAt = &ct
	}
	if leaseNS.Valid {
		lt := time.Unix(0, leaseNS.Int64)
		t.LeaseExpiresAt = &lt
	}
	if parent.Valid {
		t.ParentRunID = parent.String
	}
	t.Full = fullInt != 0
	if len(argsJSON) > 0 {
		_ = json.Unmarshal(argsJSON, &t.Args)
	}
	if len(envJSON) > 0 {
		_ = json.Unmarshal(envJSON, &t.TriggerEnv)
	}
	return &t, nil
}

// FindSpawnedChildTriggerID returns the most-recent child trigger
// for (parentRunID, parentNodeID, pipeline), or "".
func (s *Store) FindSpawnedChildTriggerID(ctx context.Context, parentRunID, parentNodeID, pipeline string) (string, error) {
	if parentRunID == "" || parentNodeID == "" || pipeline == "" {
		return "", nil
	}
	var id string
	err := s.queryRow(
		ctx, `
SELECT id FROM triggers
 WHERE parent_run_id = ? AND parent_node_id = ? AND pipeline = ?
 ORDER BY created_at DESC
 LIMIT 1`, parentRunID, parentNodeID, pipeline,
	).Scan(&id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	return id, nil
}

// TriggerFilter narrows ListTriggers; zero value matches all.
type TriggerFilter struct {
	Statuses  []string // "pending"|"claimed"|"done"
	Pipelines []string
	Repo      string // matches GITHUB_REPOSITORY in trigger_env
	Limit     int    // <=0 = 20
}

// ListTriggers returns triggers newest-first, filtered by f.
func (s *Store) ListTriggers(ctx context.Context, f TriggerFilter) ([]*Trigger, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 20
	}

	where := ""
	args := []any{}
	addIn := func(col string, values []string) {
		if len(values) == 0 {
			return
		}
		placeholders := make([]string, len(values))
		for i, v := range values {
			placeholders[i] = "?"
			args = append(args, v)
		}
		clause := col + " IN (" + strings.Join(placeholders, ",") + ")"
		if where == "" {
			where = " WHERE " + clause
		} else {
			where += " AND " + clause
		}
	}
	addIn("status", f.Statuses)
	addIn("pipeline", f.Pipelines)
	args = append(args, limit)

	query := `
SELECT id, pipeline, args_json, trigger_source, trigger_user,
       trigger_env, git_branch, git_sha, status, created_at,
       claimed_at, lease_expires_at, parent_run_id,
       repo, repo_url, github_owner, github_repo, retry_of, retry_source, parent_node_id, "full"
  FROM triggers` + where + `
 ORDER BY created_at DESC
 LIMIT ?`
	rows, err := s.query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	// Repo filter is client-side; trigger_env is an unindexed JSON blob.
	var out []*Trigger
	for rows.Next() {
		var t Trigger
		var argsJSON, envJSON []byte
		var createdNS int64
		var claimedNS, leaseNS sql.NullInt64
		var parent sql.NullString
		var fullInt int
		if err := rows.Scan(&t.ID, &t.Pipeline, &argsJSON, &t.TriggerSource, &t.TriggerUser,
			&envJSON, &t.GitBranch, &t.GitSHA, &t.Status, &createdNS,
			&claimedNS, &leaseNS, &parent,
			&t.Repo, &t.RepoURL, &t.GithubOwner, &t.GithubRepo, &t.RetryOf, &t.RetrySource, &t.ParentNodeID, &fullInt); err != nil {
			return nil, err
		}
		t.Full = fullInt != 0
		t.CreatedAt = time.Unix(0, createdNS)
		if claimedNS.Valid {
			ct := time.Unix(0, claimedNS.Int64)
			t.ClaimedAt = &ct
		}
		if leaseNS.Valid {
			lt := time.Unix(0, leaseNS.Int64)
			t.LeaseExpiresAt = &lt
		}
		if parent.Valid {
			t.ParentRunID = parent.String
		}
		if len(argsJSON) > 0 {
			_ = json.Unmarshal(argsJSON, &t.Args)
		}
		if len(envJSON) > 0 {
			_ = json.Unmarshal(envJSON, &t.TriggerEnv)
		}
		if f.Repo != "" {
			if t.TriggerEnv["GITHUB_REPOSITORY"] != f.Repo {
				continue
			}
		}
		out = append(out, &t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// --- Locks ---

// ErrLockHeld signals the caller is not the current slot holder. HTTP -> 409.
var ErrLockHeld = errors.New("held by another holder")

// CountPendingNodes returns the count of unclaimed ready nodes.
func (s *Store) CountPendingNodes(ctx context.Context) (int, error) {
	var n int
	err := s.queryRow(
		ctx,
		`SELECT COUNT(*) FROM nodes
		  WHERE ready_at IS NOT NULL AND (claimed_by IS NULL OR claimed_by = '')`,
	).Scan(&n)
	return n, err
}

// CountActiveRunners counts distinct claimed_by within `window`.
func (s *Store) CountActiveRunners(ctx context.Context, window time.Duration) (int, error) {
	threshold := time.Now().Add(-window).UnixNano()
	var n int
	err := s.queryRow(
		ctx,
		`SELECT COUNT(DISTINCT claimed_by) FROM nodes
		  WHERE claimed_by IS NOT NULL AND claimed_by != ''
		    AND lease_expires_at IS NOT NULL AND lease_expires_at >= ?`,
		threshold,
	).Scan(&n)
	return n, err
}

// --- Approvals (approval-gate primitive) ---

// NodeStatusApprovalPending = nodes.status while waiting on a human.
const NodeStatusApprovalPending = "approval_pending"

// Approval resolutions. Empty means "still pending."
const (
	ApprovalResolutionApproved = "approved"
	ApprovalResolutionDenied   = "denied"
	ApprovalResolutionTimedOut = "timed_out"
)

// Approval on-timeout policies. Operator chooses per-gate; default
// is "fail" (surface the timeout as an explicit error).
const (
	ApprovalOnTimeoutFail    = "fail"
	ApprovalOnTimeoutDeny    = "deny"
	ApprovalOnTimeoutApprove = "approve"
)

// Approval is one row in the approvals table. resolved_at + resolution
// are populated only after a human or the waiter has decided.
type Approval struct {
	RunID       string     `json:"run_id"`
	NodeID      string     `json:"node_id"`
	RequestedAt time.Time  `json:"requested_at"`
	Message     string     `json:"message,omitempty"`
	TimeoutMS   int64      `json:"timeout_ms,omitempty"`
	OnTimeout   string     `json:"on_timeout,omitempty"`
	Approver    string     `json:"approver,omitempty"`
	ResolvedAt  *time.Time `json:"resolved_at,omitempty"`
	Resolution  string     `json:"resolution,omitempty"`
	Comment     string     `json:"comment,omitempty"`
}

// CreateApproval inserts a pending approval and flips node status to
// approval_pending in one txn. Re-runs a gate from scratch.
func (s *Store) CreateApproval(ctx context.Context, a Approval) error {
	if a.OnTimeout == "" {
		a.OnTimeout = ApprovalOnTimeoutFail
	}
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `
INSERT INTO approvals (run_id, node_id, requested_at, message, timeout_ms, on_timeout)
VALUES (?,?,?,?,?,?)
ON CONFLICT(run_id, node_id) DO UPDATE SET
    requested_at = excluded.requested_at,
    message      = excluded.message,
    timeout_ms   = excluded.timeout_ms,
    on_timeout   = excluded.on_timeout,
    approver     = '',
    resolved_at  = NULL,
    resolution   = '',
    comment      = ''`,
		a.RunID, a.NodeID, a.RequestedAt.UnixNano(),
		a.Message, a.TimeoutMS, a.OnTimeout); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE nodes SET status = ? WHERE run_id = ? AND node_id = ?`,
		NodeStatusApprovalPending, a.RunID, a.NodeID); err != nil {
		return err
	}
	return tx.Commit()
}

// GetApproval returns the row, or ErrNotFound.
func (s *Store) GetApproval(ctx context.Context, runID, nodeID string) (*Approval, error) {
	row := s.queryRow(ctx, `
SELECT run_id, node_id, requested_at, message, timeout_ms, on_timeout,
       approver, resolved_at, resolution, comment
  FROM approvals WHERE run_id = ? AND node_id = ?`, runID, nodeID)
	return scanApproval(row)
}

// ResolveApproval stamps resolution on a pending row.
// ErrNotFound when missing; ErrLockHeld when already resolved.
func (s *Store) ResolveApproval(ctx context.Context, runID, nodeID, resolution, approver, comment string) (*Approval, error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	var pkRun, pkNode string
	var resolvedNS sql.NullInt64
	err = tx.QueryRowContext(
		ctx,
		`SELECT run_id, node_id, resolved_at FROM approvals
		  WHERE run_id = ? AND node_id = ?`, runID, nodeID,
	).Scan(&pkRun, &pkNode, &resolvedNS)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if resolvedNS.Valid {
		return nil, ErrLockHeld
	}

	if _, err := tx.ExecContext(ctx, `
UPDATE approvals
   SET resolution = ?, approver = ?, comment = ?, resolved_at = ?
 WHERE run_id = ? AND node_id = ? AND resolved_at IS NULL`,
		resolution, approver, comment, time.Now().UnixNano(),
		runID, nodeID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetApproval(ctx, runID, nodeID)
}

// ListApprovalsForRun returns all rows in request order.
func (s *Store) ListApprovalsForRun(ctx context.Context, runID string) ([]*Approval, error) {
	rows, err := s.query(ctx, `
SELECT run_id, node_id, requested_at, message, timeout_ms, on_timeout,
       approver, resolved_at, resolution, comment
  FROM approvals WHERE run_id = ?
 ORDER BY requested_at ASC`, runID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []*Approval
	for rows.Next() {
		a, err := scanApproval(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ListPendingApprovals returns unresolved approvals oldest-first.
func (s *Store) ListPendingApprovals(ctx context.Context) ([]*Approval, error) {
	rows, err := s.query(ctx, `
SELECT run_id, node_id, requested_at, message, timeout_ms, on_timeout,
       approver, resolved_at, resolution, comment
  FROM approvals WHERE resolved_at IS NULL
 ORDER BY requested_at ASC`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []*Approval
	for rows.Next() {
		a, err := scanApproval(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func scanApproval(rs rowScanner) (*Approval, error) {
	var a Approval
	var requestedNS int64
	var resolvedNS sql.NullInt64
	err := rs.Scan(&a.RunID, &a.NodeID, &requestedNS, &a.Message,
		&a.TimeoutMS, &a.OnTimeout, &a.Approver, &resolvedNS,
		&a.Resolution, &a.Comment)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	a.RequestedAt = time.Unix(0, requestedNS)
	if resolvedNS.Valid {
		t := time.Unix(0, resolvedNS.Int64)
		a.ResolvedAt = &t
	}
	return &a, nil
}
