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
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

// BusyTimeoutEnvVar names the environment override for the SQLite
// busy_timeout Open and OpenReadOnly apply, in milliseconds. Unset or
// empty keeps [DefaultBusyTimeoutMS]; anything else must be a positive
// integer or Open fails loudly, naming the variable and value -- a
// typo'd override silently reverting to the default would hide the
// misconfiguration. The knob exists for hosts whose contention profile
// makes 30s wrong in either direction: diagnostic tooling that wants
// to fail fast on a wedged database, or a heavily shared host that
// wants writers to wait longer before erroring with SQLITE_BUSY.
const BusyTimeoutEnvVar = "SPARKWING_SQLITE_BUSY_TIMEOUT_MS"

// DefaultBusyTimeoutMS is the SQLite busy_timeout applied when
// [BusyTimeoutEnvVar] is unset. See Open for why 30s.
const DefaultBusyTimeoutMS = 30000

func busyTimeoutMS() (int, error) {
	raw := os.Getenv(BusyTimeoutEnvVar)
	if raw == "" {
		return DefaultBusyTimeoutMS, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%s=%q: want a positive integer of milliseconds", BusyTimeoutEnvVar, raw)
	}
	return n, nil
}

// Failure reason codes; empty = no structured reason.
const (
	FailureUnknown           = ""
	FailureOOMKilled         = "oom_killed"
	FailureAgentLost         = "agent_lost"
	FailureTimeout           = "timeout"
	FailureNoProgressTimeout = "no_progress_timeout"
	// FailureVerify: the node's action completed but its Verify
	// postcondition returned an error. The failure is at the verify
	// stage, not the action.
	FailureVerify             = "verify"
	FailureQueueTimeout       = "queue_timeout"
	FailureRunnerLeaseExpired = "runner_lease_expired"
	// FailureLogsAuth: the runner's logs.append calls returned 401/403
	// against the controller's auth surface. The run's structured
	// logs are unrecoverable; better to fail loud than report
	// status=success with no observable output.
	FailureLogsAuth = "logs_auth"
	// FailureLogsDropped: log lines were lost because the log store
	// stayed unreachable past the append retry budget. The node's own
	// work may well have succeeded, but its record of that work is
	// incomplete, so the same rule as FailureLogsAuth applies: a run
	// nobody can read is not a run anybody should trust. Adopters who
	// prefer the lossy behavior set SPARKWING_LOGS_DROP_POLICY=warn.
	FailureLogsDropped = "logs_dropped"
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
	db        *sql.DB
	dialect   Dialect
	cleanup   func() error
	csrfKeyMu sync.Mutex
	csrfKey   []byte
}

// Dialect reports the SQL dialect this Store was opened against.
// Useful for callers (tests, diagnostics) that need to know which
// backend they're talking to; query methods on Store handle the
// dialect difference internally.
func (s *Store) Dialect() Dialect { return s.dialect }

// Open initializes a SQLite database at path with WAL + foreign keys.
//
// busy_timeout defaults high (30s; override via [BusyTimeoutEnvVar]) so
// that under N concurrent writers on
// one host -- multiple `sparkwing run` invocations plus the dashboard
// daemon sharing one state.db -- a writer waits on a busy lock instead
// of erroring immediately with SQLITE_BUSY and aborting the run. WAL
// lets readers proceed while a writer commits, so the wait is bounded
// to the brief windows when two writers genuinely overlap.
//
// txlock=immediate makes every transaction take the write lock at BEGIN
// rather than upgrading a read lock to a write lock mid-transaction.
// Without it, two connections that each SELECT-then-INSERT (the shape
// of AcquireConcurrencySlot and most state mutations) can both read the
// same snapshot and then collide on the upgrade -- which surfaces as
// SQLITE_BUSY_SNAPSHOT, a conflict busy_timeout does NOT retry. Taking
// the lock up front turns that race into an ordinary busy-wait the
// timeout absorbs. Single-statement reads outside a transaction are
// unaffected, so read-only queries don't pay for the write lock.
//
// busy_timeout is listed FIRST so it is in force before journal_mode is
// applied: switching a brand-new database to WAL needs a momentary
// exclusive lock, and two processes cold-starting the same state.db at
// once would otherwise have one fail the WAL switch with SQLITE_BUSY
// before any timeout was active. With the timeout set first, that race
// becomes a bounded wait.
//
// synchronous=NORMAL is the WAL-recommended setting. Under the default
// FULL every COMMIT fsyncs the WAL, and an _txlock=immediate transaction
// holds the write lock until COMMIT -- so that fsync serializes every
// co-located writer and caps throughput. NORMAL fsyncs at checkpoint
// instead. WAL stays crash-consistent either way; the cost is a few of
// the most recent commits on an OS or power crash, which for orchestration
// state (leases, runs, cache) is self-healing: a lost lease is reaped, a
// lost run row reconciled.
func Open(path string) (*Store, error) {
	_, statErr := os.Lstat(path)
	newDatabase := errors.Is(statErr, os.ErrNotExist)
	if statErr != nil && !newDatabase {
		return nil, fmt.Errorf("inspect sqlite database %s: %w", path, statErr)
	}
	if newDatabase {
		if err := preparePrivateSQLite(path); err != nil {
			return nil, err
		}
	}
	dsn, err := sqliteDSN(path)
	if err != nil {
		return nil, err
	}
	st, err := openSQL("sqlite", dsn, DialectSQLite)
	if err != nil {
		return nil, err
	}
	return st, nil
}

func preparePrivateSQLite(path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("secure sqlite database %s: %w", path, err)
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return fmt.Errorf("secure sqlite database %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("secure sqlite database %s: %w", path, err)
	}
	return nil
}

func sqliteDSN(path string) (string, error) {
	ms, err := busyTimeoutMS()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("file:%s?_txlock=immediate&_pragma=busy_timeout(%d)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(on)", path, ms), nil
}

func sqliteReadOnlyDSN(path string) (string, error) {
	return sqliteReadOnlyDSNWithMode(path, false)
}

func sqliteReadOnlySnapshotDSN(path string) (string, error) {
	return sqliteReadOnlyDSNWithMode(path, true)
}

func sqliteReadOnlyDSNWithMode(path string, immutable bool) (string, error) {
	ms, err := busyTimeoutMS()
	if err != nil {
		return "", err
	}
	mode := "mode=ro"
	if immutable {
		mode += "&immutable=1"
	}
	return fmt.Sprintf("file:%s?%s&_pragma=busy_timeout(%d)&_pragma=query_only(true)", path, mode, ms), nil
}

// OpenReadOnly opens an existing SQLite state database for reads only.
// The connection sets PRAGMA query_only so it can never take a write
// lock; a read-mostly consumer like the dashboard daemon therefore
// can't starve out the `sparkwing run` processes that actually mutate
// the database. WAL readers don't block the writer either way, so this
// is belt-and-suspenders on top of WAL.
//
// No migration runs: the caller is responsible for ensuring the schema
// exists (the controller / runner that writes the database does this on
// its own Open). Opening a database whose schema this binary doesn't
// understand surfaces as query errors at read time, not here.
func OpenReadOnly(path string) (*Store, error) {
	dsn, err := sqliteReadOnlyDSN(path)
	return openReadOnlyDSN(dsn, err)
}

// OpenReadOnlySnapshot copies a stable database-and-WAL pair from an existing
// SQLite state database and opens the copy read-only. SQLite only opens the
// private copy, so the source directory is never changed. Closing the Store
// removes the temporary copy.
func OpenReadOnlySnapshot(path string) (*Store, error) {
	tempDir, err := os.MkdirTemp("", "sparkwing-store-snapshot-")
	if err != nil {
		return nil, fmt.Errorf("create sqlite snapshot directory: %w", err)
	}
	cleanup := func() error { return os.RemoveAll(tempDir) }
	snapshotPath := filepath.Join(tempDir, "state.db")
	if err := preparePrivateSQLite(snapshotPath); err != nil {
		_ = cleanup()
		return nil, err
	}
	if err := copySQLiteSnapshot(path, snapshotPath); err != nil {
		_ = cleanup()
		return nil, err
	}
	dsn, err := sqliteReadOnlySnapshotDSN(snapshotPath)
	st, err := openReadOnlyDSN(dsn, err)
	if err != nil {
		_ = cleanup()
		return nil, err
	}
	st.cleanup = cleanup
	return st, nil
}

func copySQLiteSnapshot(sourcePath, destinationPath string) error {
	return copySQLiteSnapshotWithInspect(sourcePath, destinationPath, inspectSnapshotSource)
}

type inspectSnapshotSourceFunc func(string, bool) (snapshotSource, error)

func copySQLiteSnapshotWithInspect(sourcePath, destinationPath string, inspect inspectSnapshotSourceFunc) error {
	var lastChange error
	for range 5 {
		beforeMain, err := inspect(sourcePath, false)
		if err != nil {
			return fmt.Errorf("inspect sqlite snapshot source %s: %w", sourcePath, err)
		}
		beforeWAL, err := inspect(sourcePath+"-wal", true)
		if err != nil {
			return fmt.Errorf("inspect sqlite snapshot source %s: %w", sourcePath+"-wal", err)
		}
		for _, suffix := range []string{"-wal", "-shm", "-journal"} {
			if err := os.Remove(destinationPath + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		if err := copySnapshotFile(sourcePath, destinationPath, beforeMain.info); err != nil {
			if errors.Is(err, errSQLiteSnapshotSourceChanged) {
				lastChange = err
				continue
			}
			return err
		}
		if beforeWAL.exists {
			if err := copySnapshotFile(sourcePath+"-wal", destinationPath+"-wal", beforeWAL.info); err != nil {
				if errors.Is(err, errSQLiteSnapshotSourceChanged) {
					lastChange = err
					continue
				}
				return err
			}
		}
		afterMain, err := inspect(sourcePath, false)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				lastChange = fmt.Errorf("%w: %s disappeared during copy", errSQLiteSnapshotSourceChanged, sourcePath)
				continue
			}
			return fmt.Errorf("reinspect sqlite snapshot source %s: %w", sourcePath, err)
		}
		afterWAL, err := inspect(sourcePath+"-wal", true)
		if err != nil {
			return fmt.Errorf("reinspect sqlite snapshot source %s: %w", sourcePath+"-wal", err)
		}
		if !sameSnapshotSource(beforeMain, afterMain) || !sameSnapshotSource(beforeWAL, afterWAL) {
			lastChange = fmt.Errorf("%w: %s database or WAL metadata changed during copy", errSQLiteSnapshotSourceChanged, sourcePath)
			continue
		}
		if err := checkpointSQLiteCopy(destinationPath); err != nil {
			return fmt.Errorf("validate sqlite snapshot copied from %s: %w", sourcePath, err)
		}
		return nil
	}
	return fmt.Errorf("copy sqlite snapshot: source %s kept changing after 5 attempts: %w", sourcePath, lastChange)
}

var errSQLiteSnapshotSourceChanged = errors.New("sqlite snapshot source changed")

type snapshotSource struct {
	exists bool
	info   os.FileInfo
}

func inspectSnapshotSource(path string, optional bool) (snapshotSource, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if optional && errors.Is(err, os.ErrNotExist) {
			return snapshotSource{}, nil
		}
		return snapshotSource{}, err
	}
	if !info.Mode().IsRegular() {
		return snapshotSource{}, fmt.Errorf("sqlite snapshot source %s is not a regular file", path)
	}
	return snapshotSource{exists: true, info: info}, nil
}

func copySnapshotFile(sourcePath, destinationPath string, expected os.FileInfo) error {
	source, err := os.Open(sourcePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w while opening %s", errSQLiteSnapshotSourceChanged, sourcePath)
		}
		return fmt.Errorf("open sqlite snapshot source %s: %w", sourcePath, err)
	}
	opened, err := source.Stat()
	if err != nil || !os.SameFile(expected, opened) {
		_ = source.Close()
		if err != nil {
			return err
		}
		return fmt.Errorf("%w while opening %s", errSQLiteSnapshotSourceChanged, sourcePath)
	}
	destination, err := os.OpenFile(destinationPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		_ = source.Close()
		return fmt.Errorf("open sqlite snapshot destination %s: %w", destinationPath, err)
	}
	if err := destination.Chmod(0o600); err != nil {
		_ = destination.Close()
		_ = source.Close()
		return fmt.Errorf("secure sqlite snapshot destination %s: %w", destinationPath, err)
	}
	_, copyErr := io.Copy(destination, source)
	if err := errors.Join(copyErr, destination.Close(), source.Close()); err != nil {
		return fmt.Errorf("copy sqlite snapshot source %s: %w", sourcePath, err)
	}
	return nil
}

func sameSnapshotSource(a, b snapshotSource) bool {
	if a.exists != b.exists {
		return false
	}
	if !a.exists {
		return true
	}
	return os.SameFile(a.info, b.info) &&
		a.info.Size() == b.info.Size() &&
		a.info.ModTime().Equal(b.info.ModTime())
}

func checkpointSQLiteCopy(path string) error {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(30000)")
	if err != nil {
		return err
	}
	db.SetMaxOpenConns(1)
	var integrity string
	checkErr := db.QueryRow(`PRAGMA quick_check`).Scan(&integrity)
	if checkErr == nil && integrity != "ok" {
		checkErr = fmt.Errorf("sqlite snapshot quick_check: %s", integrity)
	}
	var checkpointErr error
	if checkErr == nil {
		_, checkpointErr = db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	}
	return errors.Join(checkErr, checkpointErr, db.Close())
}

func openReadOnlyDSN(dsn string, err error) (*Store, error) {
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	return &Store{db: db, dialect: DialectSQLite}, nil
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

func openSQL(driver, dsn string, dialect Dialect) (*Store, error) {
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, err
	}
	switch dialect {
	case DialectSQLite:
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

// Close releases the underlying database handle and any private snapshot.
func (s *Store) Close() error {
	dbErr := s.db.Close()
	if s.cleanup == nil {
		return dbErr
	}
	cleanup := s.cleanup
	s.cleanup = nil
	return errors.Join(dbErr, cleanup())
}

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
    -- whole. NULL for rows that predate the column or come from a
    -- backend whose TouchRunHeartbeat is a no-op (S3 mode, which
    -- reconciles orphans via per-node heartbeats instead). The
    -- controller's reaper and the local orphan reconciler both use it
    -- to detect an orchestrator that died between node dispatches,
    -- before any node-level heartbeat exists.
    last_heartbeat_at INTEGER
);

CREATE INDEX IF NOT EXISTS idx_runs_started ON runs(started_at DESC);
CREATE INDEX IF NOT EXISTS idx_runs_pipeline ON runs(pipeline, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_runs_sha_started ON runs(git_sha, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_runs_branch_started ON runs(git_branch, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_runs_repo_slug_started ON runs(repo, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_runs_repo_sha_started ON runs(repo, git_sha, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_runs_repo_branch_started ON runs(repo, git_branch, started_at DESC);

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
    -- claim_principal: the authenticated principal the claim is bound
    -- to; '' when the controller served the claim unauthenticated.
    -- Display only: two tokens may carry the same principal name.
    claim_principal  TEXT NOT NULL DEFAULT '',
    -- claim_token_prefix: the claiming token's prefix segment. Unique
    -- per token, so this is what the ownership predicates match on.
    claim_token_prefix TEXT NOT NULL DEFAULT '',
    lease_expires_at INTEGER,
    -- needs_labels: JSON []string from RunsOn; AND semantics.
    needs_labels     BLOB,
    -- prefers_labels: ordered soft executor preferences.
    prefers_labels   BLOB,
    requested_cores  REAL NOT NULL DEFAULT 0,
    requested_memory_bytes INTEGER NOT NULL DEFAULT 0,
    requested_slots  INTEGER NOT NULL DEFAULT 1,
    offer_started_at INTEGER,
    offer_priority_ceiling INTEGER NOT NULL DEFAULT 100,
    claim_base_priority INTEGER NOT NULL DEFAULT 0,
    claim_priority    INTEGER NOT NULL DEFAULT 0,
    claim_worker_id   TEXT NOT NULL DEFAULT '',
    claim_executor_kind TEXT NOT NULL DEFAULT '',
    claim_reservation_id TEXT NOT NULL DEFAULT '',
    -- status_detail: phase string runners write for the dashboard.
    status_detail    TEXT NOT NULL DEFAULT '',
    -- last_heartbeat: runner liveness; for UI, not lease enforcement.
    last_heartbeat   INTEGER,
    -- failure_reason: Failure* constant; empty = uncategorized.
    failure_reason   TEXT NOT NULL DEFAULT '',
    -- exit_code: process exit; NULL when not tied to a process.
    exit_code        INTEGER,
    -- artifact_manifest: content-addressed digest of the node's
    -- published-artifact manifest; empty when it produced none.
    artifact_manifest TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (run_id, node_id),
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_nodes_claimable
    ON nodes(ready_at)
    WHERE ready_at IS NOT NULL AND claimed_by IS NULL AND status != 'done';
CREATE INDEX IF NOT EXISTS idx_nodes_claimed_lease
    ON nodes(lease_expires_at)
    WHERE claimed_by IS NOT NULL;

CREATE TABLE IF NOT EXISTS node_claim_offers (
    claim_token_prefix TEXT NOT NULL DEFAULT '',
    claim_principal    TEXT NOT NULL DEFAULT '',
    holder_id          TEXT NOT NULL,
    run_id             TEXT NOT NULL,
    node_id            TEXT NOT NULL,
    worker_id          TEXT NOT NULL,
    executor_kind      TEXT NOT NULL DEFAULT '',
    reservation_id     TEXT NOT NULL,
    base_priority      INTEGER NOT NULL,
    effective_priority INTEGER NOT NULL,
    offered_at         INTEGER NOT NULL,
    last_seen_at       INTEGER NOT NULL,
    lease_ns           INTEGER NOT NULL,
    PRIMARY KEY (claim_token_prefix, claim_principal, holder_id),
    FOREIGN KEY (run_id, node_id) REFERENCES nodes(run_id, node_id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_node_claim_offers_award
    ON node_claim_offers(run_id, node_id, effective_priority DESC, offered_at, worker_id, holder_id);

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
    repo_inherited        INTEGER NOT NULL DEFAULT 0,
    retry_of              TEXT NOT NULL DEFAULT '',
    parent_node_id        TEXT NOT NULL DEFAULT '',
    idempotency_key       TEXT NOT NULL DEFAULT '',
    claim_seq             INTEGER NOT NULL DEFAULT 0,
    claim_principal       TEXT NOT NULL DEFAULT '',
    claim_token_prefix    TEXT NOT NULL DEFAULT '',
    webhook_delivery      TEXT NOT NULL DEFAULT '',
    webhook_replay_key    TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_triggers_pending
    ON triggers(status, created_at) WHERE status = 'pending';
CREATE INDEX IF NOT EXISTS idx_triggers_claimed_lease
    ON triggers(status, lease_expires_at) WHERE status = 'claimed';
CREATE INDEX IF NOT EXISTS idx_triggers_source_status_created
    ON triggers(trigger_source, status, created_at);
-- Dedup is enforced by the database, not by a read-then-write in the
-- submitter: two concurrent submissions carrying one key must produce
-- one run, and only a unique constraint decides that without a lock.
-- The partial predicate keeps the empty default (every trigger that
-- was never submitted with a key) out of the index entirely.
--
-- Scoped to the pipeline: a key names one caller's intent, and two
-- pipelines naming their intents independently is ordinary. A global
-- namespace would let a submission of one pipeline be answered with
-- another pipeline's run.
CREATE UNIQUE INDEX IF NOT EXISTS idx_triggers_idempotency_key
    ON triggers(pipeline, idempotency_key) WHERE idempotency_key != '';

-- One trigger per webhook delivery id, across every pipeline. A
-- provider delivery id names one event; replaying it -- to the same
-- pipeline or to another one -- must not produce a second run, and
-- only a unique constraint decides that between concurrent posts.
-- The partial predicate keeps non-webhook submissions out of the index.
CREATE UNIQUE INDEX IF NOT EXISTS idx_triggers_webhook_delivery
    ON triggers(webhook_delivery) WHERE webhook_delivery != '';

-- One trigger per digest of the signed material a webhook carried.
-- The delivery id is a header the provider picks and nothing signs, so
-- keying replay protection on it alone lets anyone who captured one
-- delivery re-send the same signed body under an id of their own. This
-- digest covers the pipeline and the request body, which is exactly
-- what the HMAC covers, so a re-sent body is refused whatever header
-- rides with it.
CREATE UNIQUE INDEX IF NOT EXISTS idx_triggers_webhook_replay_key
    ON triggers(webhook_replay_key) WHERE webhook_replay_key != '';

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
    key               TEXT NOT NULL,
    holder_id         TEXT NOT NULL,
    run_id            TEXT NOT NULL,
    node_id           TEXT NOT NULL DEFAULT '',
    claimed_at        INTEGER NOT NULL,
    queue_arrived_at  INTEGER NOT NULL DEFAULT 0,
    lease_expires_at  INTEGER NOT NULL,
    superseded        INTEGER NOT NULL DEFAULT 0,
    cost              INTEGER NOT NULL DEFAULT 1,
    declared_capacity INTEGER NOT NULL DEFAULT 0,
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
    cost               INTEGER NOT NULL DEFAULT 1,
    declared_capacity  INTEGER NOT NULL DEFAULT 0,
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
CREATE UNIQUE INDEX IF NOT EXISTS idx_tokens_prefix ON tokens(prefix);

-- Browser sessions. hash = sha256 of the raw session id; the CSRF token is
-- an HMAC of that id under a server key, so neither is stored in the clear.
CREATE TABLE IF NOT EXISTS sessions (
    hash          TEXT PRIMARY KEY,
    principal     TEXT NOT NULL,
    scopes        TEXT NOT NULL,
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
    last_login_at INTEGER,
    scopes        TEXT NOT NULL DEFAULT 'admin'
);

-- Pipeline secrets; encryption at rest is up to the volume.
CREATE TABLE IF NOT EXISTS secrets (
    name       TEXT NOT NULL,
    value      TEXT NOT NULL,
    principal  TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    -- masked=0 = non-sensitive config (not redacted in run output).
    masked     INTEGER NOT NULL DEFAULT 1,
    -- repo='' is unscoped: only a shared unscoped row answers a run.
    repo       TEXT NOT NULL DEFAULT '',
    -- shared=1 lets an unscoped row answer a run that names no repo of its own.
    shared     INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (name, repo)
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
    redacted_keys       BLOB,
    PRIMARY KEY (run_id, node_id, seq),
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_node_dispatches_lookup
    ON node_dispatches(run_id, node_id, seq DESC);
`

var schemaPostgres = func() string {
	r := strings.NewReplacer(
		"INTEGER", "BIGINT",
		"BLOB", "BYTEA",
	)
	return r.Replace(schemaSQLite)
}()

const expectedSchemaVersion = 28

const runIdentityIndexes = `
CREATE INDEX IF NOT EXISTS idx_runs_sha_started ON runs(git_sha, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_runs_branch_started ON runs(git_branch, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_runs_repo_slug_started ON runs(repo, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_runs_repo_sha_started ON runs(repo, git_sha, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_runs_repo_branch_started ON runs(repo, git_branch, started_at DESC);`

// ExpectedSchemaVersion returns the schema version this binary
// understands. Useful for diagnostics, version-mismatch reporting,
// and tests that need to assert what Open will write into the
// sparkwing_schema_version table on a fresh database.
func ExpectedSchemaVersion() int { return expectedSchemaVersion }

const metaKeyMinVersion = "min_binary_version"

var binaryVersion string

// SetBinaryVersion records the running binary's version so migrations
// can stamp it into metaKeyMinVersion and the skew-error path can
// report it. The CLI, dashboard supervisor, and pipeline binaries
// call this at startup with their resolved installed version (which
// honors the release ldflag and falls back to the module
// pseudo-version). Safe to leave unset: resolveBinaryVersion reads
// runtime build info instead.
func SetBinaryVersion(v string) { binaryVersion = v }

func resolveBinaryVersion() string {
	if binaryVersion != "" {
		return binaryVersion
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		if v := info.Main.Version; v != "" && v != "(devel)" {
			return v
		}
	}
	return "(devel)"
}

func stampMinVersionTx(ctx context.Context, tx *storeTx) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO sparkwing_meta (key, value, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		metaKeyMinVersion, resolveBinaryVersion(), time.Now().UnixNano())
	return err
}

func (s *Store) readMinVersion(ctx context.Context) string {
	var v string
	if err := s.queryRow(ctx,
		`SELECT value FROM sparkwing_meta WHERE key = ?`, metaKeyMinVersion).Scan(&v); err != nil {
		return ""
	}
	return v
}

func readMinVersionTx(ctx context.Context, tx *storeTx) string {
	var v string
	if err := tx.QueryRowContext(ctx,
		`SELECT value FROM sparkwing_meta WHERE key = ?`, metaKeyMinVersion).Scan(&v); err != nil {
		return ""
	}
	return v
}

// MinBinaryVersion returns the minimum sparkwing binary version this database
// requires -- the version stamped by the binary that last migrated it -- or
// "" when no stamp exists (a database written before the stamp, or one last
// migrated by a development build). It is the read-only counterpart of the
// skew check: a repo pinned below this cannot open the database.
func (s *Store) MinBinaryVersion(ctx context.Context) string {
	return s.readMinVersion(ctx)
}

// CurrentSchemaVersion returns the schema version recorded in the
// database (MAX of sparkwing_schema_version), or 0 when unrecorded.
// A resident reader (the dashboard) polls this to notice a newer
// binary migrating the shared database out from under it.
func (s *Store) CurrentSchemaVersion(ctx context.Context) (int, error) {
	var v int
	if err := s.queryRow(ctx,
		`SELECT COALESCE(MAX(version), 0) FROM sparkwing_schema_version`).Scan(&v); err != nil {
		return 0, err
	}
	return v, nil
}

const schemaVersionTable = `CREATE TABLE IF NOT EXISTS sparkwing_schema_version (
    version    INTEGER NOT NULL,
    applied_at BIGINT NOT NULL,
    PRIMARY KEY (version)
);`

const metaTableSQLite = `CREATE TABLE IF NOT EXISTS sparkwing_meta (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at INTEGER NOT NULL
);`

var metaTablePostgres = strings.NewReplacer("INTEGER", "BIGINT").Replace(metaTableSQLite)

const pipelineProfilesTableSQLite = `CREATE TABLE IF NOT EXISTS pipeline_profiles (
    pipeline            TEXT    NOT NULL,
    node_id             TEXT    NOT NULL,
    p50_duration_ms     INTEGER NOT NULL,
    p99_duration_ms     INTEGER NOT NULL,
    peak_cores          REAL    NOT NULL,
    peak_memory_bytes   INTEGER NOT NULL,
    sample_count        INTEGER NOT NULL,
    updated_at          INTEGER NOT NULL,
    samples_json        BLOB,
    pinned_cores        REAL    NOT NULL DEFAULT 0,
    pinned_memory_bytes INTEGER NOT NULL DEFAULT 0,
    cpu_measured        INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (pipeline, node_id)
);`

var pipelineProfilesTablePostgres = strings.NewReplacer(
	"INTEGER", "BIGINT",
	"BLOB", "BYTEA",
).Replace(pipelineProfilesTableSQLite)

const nodeBouncesTableSQLite = `CREATE TABLE IF NOT EXISTS node_bounces (
    run_id       TEXT    NOT NULL,
    node_id      TEXT    NOT NULL,
    seq          INTEGER NOT NULL,
    requested_at INTEGER NOT NULL,
    requested_by TEXT    NOT NULL DEFAULT '',
    consumed_at  INTEGER,
    outcome      TEXT    NOT NULL DEFAULT '',
    PRIMARY KEY (run_id, node_id, seq),
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_node_bounces_pending
    ON node_bounces(run_id, node_id) WHERE consumed_at IS NULL;`

var nodeBouncesTablePostgres = strings.NewReplacer(
	"INTEGER", "BIGINT",
).Replace(nodeBouncesTableSQLite)

const nodeClaimOffersTableSQLite = `CREATE TABLE IF NOT EXISTS node_claim_offers (
    claim_token_prefix TEXT NOT NULL DEFAULT '',
    claim_principal    TEXT NOT NULL DEFAULT '',
    holder_id          TEXT NOT NULL,
    run_id             TEXT NOT NULL,
    node_id            TEXT NOT NULL,
    worker_id          TEXT NOT NULL,
    executor_kind      TEXT NOT NULL DEFAULT '',
    reservation_id     TEXT NOT NULL,
    base_priority      INTEGER NOT NULL,
    effective_priority INTEGER NOT NULL,
    offered_at         INTEGER NOT NULL,
    last_seen_at       INTEGER NOT NULL,
    lease_ns           INTEGER NOT NULL,
    PRIMARY KEY (claim_token_prefix, claim_principal, holder_id),
    FOREIGN KEY (run_id, node_id) REFERENCES nodes(run_id, node_id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_node_claim_offers_award
    ON node_claim_offers(run_id, node_id, effective_priority DESC, offered_at, worker_id, holder_id);`

var nodeClaimOffersTablePostgres = strings.NewReplacer(
	"INTEGER", "BIGINT",
).Replace(nodeClaimOffersTableSQLite)

// SkewError is returned by Open when the database is at a schema
// version newer than the binary understands. Callers can use
// errors.As to detect the condition (e.g. for surfacing a custom
// upgrade prompt in the CLI); the wrapped message is plain English
// and suitable for direct display.
//
// MinVersion and InstalledVersion carry the human-readable version
// strings when they are known: MinVersion is the minimum binary
// version the database records for its schema (the sparkwing_meta
// row a migrating binary stamps), and InstalledVersion is the
// running binary's own version. When both are present Error() names
// them and the upgrade command; when MinVersion is absent (a database
// migrated before version stamping shipped) it falls back to the
// raw schema numbers.
type SkewError struct {
	DBVersion        int
	BinaryVersion    int
	MinVersion       string
	InstalledVersion string
}

func (e *SkewError) Error() string {
	if e.MinVersion != "" && e.MinVersion != "(devel)" {
		installed := e.InstalledVersion
		if installed == "" || installed == "(devel)" {
			installed = "an older build"
		}
		return fmt.Sprintf(
			"sparkwing: this state database needs sparkwing >= %s; you have %s "+
				"(database schema %d, this binary understands %d). "+
				"Run `sparkwing version update --cli` to upgrade.",
			e.MinVersion, installed, e.DBVersion, e.BinaryVersion,
		)
	}
	return fmt.Sprintf(
		"sparkwing: database is at schema version %d; this binary expects %d. Upgrade sparkwing or restore the database to a matching version.",
		e.DBVersion, e.BinaryVersion,
	)
}

func (s *Store) migrate() error {
	ctx := context.Background()
	if s.dialect == DialectPostgres {
		return s.migratePostgres(ctx)
	}
	return retryOnBusy(func() error {
		if _, err := s.exec(ctx, schemaVersionTable); err != nil {
			return fmt.Errorf("create sparkwing_schema_version table: %w", err)
		}
		return s.migrateSQLite(ctx)
	})
}

func retryOnBusy(fn func() error) error {
	return retryOnBusyWithSleep(fn, time.Sleep)
}

func retryOnBusyWithSleep(fn func() error, sleep func(time.Duration)) error {
	const attempts = 10
	var err error
	for i := range attempts {
		err = fn()
		if err == nil || !isBusyErr(err) {
			return err
		}
		sleep(time.Duration(i+1) * 50 * time.Millisecond)
	}
	return err
}

func isBusyErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "database is locked") ||
		strings.Contains(msg, "sqlite_busy") ||
		strings.Contains(msg, "database table is locked")
}

// IsProtocolErr reports whether err is a SQLite "locking protocol" /
// SQLITE_PROTOCOL condition: the WAL shared-memory lock range is
// saturated by another live connection. Unlike SQLITE_BUSY it is not
// resolved by retrying -- it clears only when the conflicting process
// releases its locks or exits -- so pollers treat it as immediately
// terminal instead of waiting out a busy budget. Matched on the stable
// message text for the same reason as isBusyErr.
func IsProtocolErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "locking protocol") ||
		strings.Contains(msg, "sqlite_protocol")
}

func (s *Store) migrateSQLite(ctx context.Context) error {
	var current int
	if err := s.queryRow(ctx,
		`SELECT COALESCE(MAX(version), 0) FROM sparkwing_schema_version`,
	).Scan(&current); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if current > expectedSchemaVersion {
		return &SkewError{
			DBVersion:        current,
			BinaryVersion:    expectedSchemaVersion,
			MinVersion:       s.readMinVersion(ctx),
			InstalledVersion: resolveBinaryVersion(),
		}
	}
	for v := current + 1; v <= expectedSchemaVersion; v++ {
		if err := s.applyVersionSQLite(ctx, v); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) applyVersionSQLite(ctx context.Context, version int) error {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return fmt.Errorf("begin migration v%d: %w", version, err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := applyMigrationSQLite(ctx, tx, version); err != nil {
		return fmt.Errorf("apply migration v%d: %w", version, err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO sparkwing_schema_version (version, applied_at) VALUES (?, ?)
		 ON CONFLICT (version) DO NOTHING`,
		version, time.Now().UnixNano()); err != nil {
		return fmt.Errorf("record schema version v%d: %w", version, err)
	}
	if version == expectedSchemaVersion {
		if err := stampMinVersionTx(ctx, tx); err != nil {
			return fmt.Errorf("stamp min version: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration v%d: %w", version, err)
	}
	return nil
}

func (s *Store) migratePostgres(ctx context.Context) error {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx,
		`SELECT pg_advisory_xact_lock(hashtext('sparkwing_migrate'))`); err != nil {
		return fmt.Errorf("acquire migrate advisory lock: %w", err)
	}
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
		return &SkewError{
			DBVersion:        current,
			BinaryVersion:    expectedSchemaVersion,
			MinVersion:       readMinVersionTx(ctx, tx),
			InstalledVersion: resolveBinaryVersion(),
		}
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
	if err := stampMinVersionTx(ctx, tx); err != nil {
		return fmt.Errorf("stamp min version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return backfillRunAnnotationRollup(ctx, storeExecer{s: s})
}

// safety: the SQLite handle allows one connection, so a migration reaching for *Store deadlocks against its own tx.
func applyMigrationSQLite(ctx context.Context, tx *storeTx, version int) error {
	switch version {
	case 1:
		if _, err := tx.ExecContext(ctx, schemaSQLite); err != nil {
			return err
		}
		if err := ensureColumnsAllSQLite(ctx, tx); err != nil {
			return err
		}
		return backfillRunAnnotationRollup(ctx, tx)
	case 2, 3:
		return ensureColumnsAllSQLite(ctx, tx)
	case 4:
		_, err := tx.ExecContext(ctx, metaTableSQLite)
		return err
	case 5:
		return ensureColumnsAllSQLite(ctx, tx)
	case 6:
		return ensureColumnsAllSQLite(ctx, tx)
	case 7:
		_, err := tx.ExecContext(ctx, pipelineProfilesTableSQLite)
		return err
	case 8:
		if err := ensureColumnsSQLite(ctx, tx, "pipeline_profiles", pipelineProfilesCPUMeasuredCols); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, pipelineProfilesCPUMeasuredBackfill)
		return err
	case 9:
		return ensureColumnsSQLite(ctx, tx, "pipeline_profiles", pipelineProfilesWaitCols)
	case 10:
		return ensureColumnsSQLite(ctx, tx, "pipeline_profiles", pipelineProfilesContendedCols)
	case 11:
		return ensureColumnsSQLite(ctx, tx, "pipeline_profiles", pipelineProfilesVersioningCols)
	case 12:
		return ensureColumnsSQLite(ctx, tx, "triggers", triggerRepoInheritedCols)
	case 13:
		if err := ensureColumnsSQLite(ctx, tx, "triggers", triggerSubmissionCols); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, triggerIdempotencyIndex)
		return err
	case 14:
		if err := ensureColumnsSQLite(ctx, tx, "pipeline_profiles", pipelineProfilesSustainedCols); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, pipelineProfilesSustainedBackfill)
		return err
	case 15:
		if err := ensureColumnsSQLite(ctx, tx, "nodes", nodesUsageCols); err != nil {
			return err
		}
		return ensureColumnsSQLite(ctx, tx, "node_metrics", nodeMetricsCPUTimeCols)
	case 16:
		_, err := tx.ExecContext(ctx, nodeBouncesTableSQLite)
		return err
	case 17:
		_, err := tx.ExecContext(ctx, runIdentityIndexes)
		return err
	case 18:
		return scrubSecretInputHashes(ctx, tx)
	case 19:
		return ensureColumnsSQLite(ctx, tx, "users", usersScopesCols)
	case 20:
		return ensureColumnsSQLite(ctx, tx, "node_dispatches", nodeDispatchRedactionCols)
	case 21:
		return rehashSessions(ctx, tx)
	case 22:
		return addSecretRepoScope(ctx, tx)
	case 23:
		if err := ensureColumnsSQLite(ctx, tx, "secrets", secretsSharedCols); err != nil {
			return err
		}
		return ensureColumnsSQLite(ctx, tx, "triggers", triggerClaimOwnerCols)
	case 24:
		return addTriggerWebhookDelivery(ctx, tx)
	case 25:
		return addTriggerWebhookReplayKey(ctx, tx)
	case 26:
		return uniqueTokenPrefixIndexTx(ctx, tx)
	case 27:
		return rewriteLegacyInheritedHolderMarkers(ctx, tx)
	case 28:
		if err := ensureColumnsSQLite(ctx, tx, "nodes", nodesOfferCols); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, nodeClaimOffersTableSQLite)
		return err
	default:
		return fmt.Errorf("no migration registered for v%d", version)
	}
}

func (s *Store) applyMigrationPostgresTx(ctx context.Context, tx *storeTx, version int) error {
	switch version {
	case 1:
		if _, err := tx.ExecContext(ctx, schemaPostgres); err != nil {
			return err
		}
		return s.ensureColumnsAllTx(ctx, tx)
	case 2, 3:
		return s.ensureColumnsAllTx(ctx, tx)
	case 4:
		_, err := tx.ExecContext(ctx, metaTablePostgres)
		return err
	case 5:
		return s.ensureColumnsAllTx(ctx, tx)
	case 6:
		return s.ensureColumnsAllTx(ctx, tx)
	case 7:
		_, err := tx.ExecContext(ctx, pipelineProfilesTablePostgres)
		return err
	case 8:
		if err := addColumnsTx(ctx, tx, "pipeline_profiles", pipelineProfilesCPUMeasuredCols); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, pipelineProfilesCPUMeasuredBackfill)
		return err
	case 9:
		return addColumnsTx(ctx, tx, "pipeline_profiles", pipelineProfilesWaitCols)
	case 10:
		return addColumnsTx(ctx, tx, "pipeline_profiles", pipelineProfilesContendedCols)
	case 11:
		return addColumnsTx(ctx, tx, "pipeline_profiles", pipelineProfilesVersioningCols)
	case 12:
		return addColumnsTx(ctx, tx, "triggers", triggerRepoInheritedCols)
	case 13:
		if err := addColumnsTx(ctx, tx, "triggers", triggerSubmissionCols); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, triggerIdempotencyIndex)
		return err
	case 14:
		if err := addColumnsTx(ctx, tx, "pipeline_profiles", pipelineProfilesSustainedCols); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, pipelineProfilesSustainedBackfill)
		return err
	case 15:
		if err := addColumnsTx(ctx, tx, "nodes", nodesUsageCols); err != nil {
			return err
		}
		return addColumnsTx(ctx, tx, "node_metrics", nodeMetricsCPUTimeCols)
	case 16:
		_, err := tx.ExecContext(ctx, nodeBouncesTablePostgres)
		return err
	case 17:
		_, err := tx.ExecContext(ctx, runIdentityIndexes)
		return err
	case 18:
		return scrubSecretInputHashes(ctx, tx)
	case 19:
		return addColumnsTx(ctx, tx, "users", usersScopesCols)
	case 20:
		return addColumnsTx(ctx, tx, "node_dispatches", nodeDispatchRedactionCols)
	case 21:
		if _, err := tx.ExecContext(ctx, `DELETE FROM sessions`); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `ALTER TABLE sessions DROP COLUMN IF EXISTS csrf_token`)
		return err
	case 22:
		for _, stmt := range secretRepoScopePostgres {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return err
			}
		}
		return nil
	case 23:
		if err := addColumnsTx(ctx, tx, "secrets", secretsSharedCols); err != nil {
			return err
		}
		return addColumnsTx(ctx, tx, "triggers", triggerClaimOwnerCols)
	case 24:
		if err := addColumnsTx(ctx, tx, "triggers", triggerWebhookDeliveryCols); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, triggerWebhookDeliveryIndex)
		return err
	case 25:
		if err := addColumnsTx(ctx, tx, "triggers", triggerWebhookReplayKeyCols); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, triggerWebhookReplayKeyIndex)
		return err
	case 26:
		return uniqueTokenPrefixIndexTx(ctx, tx)
	case 27:
		// safety: Postgres refused every write of the NUL marker with
		// SQLSTATE 22021, so no row here can carry it and the rewrite
		// would only bind a NUL that Postgres rejects again.
		return nil
	case 28:
		if err := addColumnsTx(ctx, tx, "nodes", nodesOfferCols); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, nodeClaimOffersTablePostgres)
		return err
	default:
		return fmt.Errorf("no migration registered for v%d", version)
	}
}

type migrationQueryExecer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func scrubSecretInputHashes(ctx context.Context, q migrationQueryExecer) error {
	rows, err := q.QueryContext(ctx, `SELECT id, args_json, invocation_json FROM runs WHERE invocation_json IS NOT NULL`)
	if err != nil {
		return err
	}
	type update struct {
		id   string
		json []byte
	}
	var updates []update
	for rows.Next() {
		var id string
		var argsRaw []byte
		var raw []byte
		if err := rows.Scan(&id, &argsRaw, &raw); err != nil {
			_ = rows.Close()
			return err
		}
		var args map[string]string
		argsText := strings.TrimSpace(string(argsRaw))
		if argsText != "" && argsText != "null" {
			if err := json.Unmarshal(argsRaw, &args); err != nil {
				_ = rows.Close()
				return fmt.Errorf("decode run %s args_json: %w", id, err)
			}
		}
		var inv map[string]any
		if err := json.Unmarshal(raw, &inv); err != nil {
			_ = rows.Close()
			return fmt.Errorf("decode run %s invocation_json: %w", id, err)
		}
		if !errors.Is(ValidateRunInvocation(Run{Args: args, Invocation: inv}), ErrSecretInputHash) {
			continue
		}
		delete(inv, "inputs_hash")
		cleaned, err := json.Marshal(inv)
		if err != nil {
			_ = rows.Close()
			return err
		}
		updates = append(updates, update{id: id, json: cleaned})
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, item := range updates {
		if _, err := q.ExecContext(ctx, `UPDATE runs SET invocation_json = ? WHERE id = ?`, item.json, item.id); err != nil {
			return err
		}
	}
	return nil
}

func addColumnsTx(ctx context.Context, tx *storeTx, table string, cols map[string]string) error {
	for name, typ := range cols {
		stmt := fmt.Sprintf(
			`ALTER TABLE %q ADD COLUMN IF NOT EXISTS %q %s`,
			table, name, translateColumnType(typ),
		)
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("add column %s.%s: %w", table, name, err)
		}
	}
	return nil
}

type columnSpec struct {
	table string
	cols  map[string]string
}

var columnMigrations = []columnSpec{
	{"node_steps", map[string]string{
		"annotations_json": "BLOB",
		"summary":          "TEXT NOT NULL DEFAULT ''",
	}},
	// #nosec G101 -- column names, not credentials
	{"nodes", map[string]string{
		"ready_at":               "INTEGER",
		"claimed_by":             "TEXT",
		"claim_principal":        "TEXT NOT NULL DEFAULT ''",
		"claim_token_prefix":     "TEXT NOT NULL DEFAULT ''",
		"lease_expires_at":       "INTEGER",
		"needs_labels":           "BLOB",
		"status_detail":          "TEXT NOT NULL DEFAULT ''",
		"last_heartbeat":         "INTEGER",
		"failure_reason":         "TEXT NOT NULL DEFAULT ''",
		"exit_code":              "INTEGER",
		"annotations_json":       "BLOB",
		"summary":                "TEXT NOT NULL DEFAULT ''",
		"artifact_manifest":      "TEXT NOT NULL DEFAULT ''",
		"prefers_labels":         "BLOB",
		"requested_cores":        "REAL NOT NULL DEFAULT 0",
		"requested_memory_bytes": "INTEGER NOT NULL DEFAULT 0",
		"requested_slots":        "INTEGER NOT NULL DEFAULT 1",
		"offer_started_at":       "INTEGER",
		"offer_priority_ceiling": "INTEGER NOT NULL DEFAULT 100",
		"claim_base_priority":    "INTEGER NOT NULL DEFAULT 0",
		"claim_priority":         "INTEGER NOT NULL DEFAULT 0",
		"claim_worker_id":        "TEXT NOT NULL DEFAULT ''",
		"claim_executor_kind":    "TEXT NOT NULL DEFAULT ''",
		"claim_reservation_id":   "TEXT NOT NULL DEFAULT ''",
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
		"parent_run_id":   "TEXT",
		"repo":            "TEXT NOT NULL DEFAULT ''",
		"repo_url":        "TEXT NOT NULL DEFAULT ''",
		"github_owner":    "TEXT NOT NULL DEFAULT ''",
		"github_repo":     "TEXT NOT NULL DEFAULT ''",
		"repo_inherited":  "INTEGER NOT NULL DEFAULT 0",
		"retry_of":        "TEXT NOT NULL DEFAULT ''",
		"retry_source":    "TEXT NOT NULL DEFAULT ''",
		"parent_node_id":  "TEXT NOT NULL DEFAULT ''",
		"full":            "INTEGER NOT NULL DEFAULT 0",
		"idempotency_key": "TEXT NOT NULL DEFAULT ''",
		"claim_seq":       "INTEGER NOT NULL DEFAULT 0",
	}},
	{"concurrency_waiters", map[string]string{
		"holder_id":         "TEXT NOT NULL DEFAULT ''",
		"cost":              "INTEGER NOT NULL DEFAULT 1",
		"declared_capacity": "INTEGER NOT NULL DEFAULT 0",
	}},
	{"concurrency_holders", map[string]string{
		"cost":              "INTEGER NOT NULL DEFAULT 1",
		"declared_capacity": "INTEGER NOT NULL DEFAULT 0",
		"queue_arrived_at":  "INTEGER NOT NULL DEFAULT 0",
	}},
	{"secrets", map[string]string{
		"masked": "INTEGER NOT NULL DEFAULT 1",
	}},
}

var pipelineProfilesCPUMeasuredCols = map[string]string{
	"cpu_measured": "INTEGER NOT NULL DEFAULT 0",
}

const pipelineProfilesCPUMeasuredBackfill = `UPDATE pipeline_profiles SET cpu_measured = 1 WHERE peak_cores > 0`

var pipelineProfilesWaitCols = map[string]string{
	"wait_samples_json": "BLOB",
	"wait_p50_ms":       "INTEGER NOT NULL DEFAULT 0",
	"wait_p99_ms":       "INTEGER NOT NULL DEFAULT 0",
	"wait_sample_count": "INTEGER NOT NULL DEFAULT 0",
}

var pipelineProfilesContendedCols = map[string]string{
	"contended_count": "INTEGER NOT NULL DEFAULT 0",
}

var pipelineProfilesVersioningCols = map[string]string{
	"plan_hash":              "TEXT NOT NULL DEFAULT ''",
	"floor_cores":            "REAL NOT NULL DEFAULT 0",
	"floor_memory_bytes":     "INTEGER NOT NULL DEFAULT 0",
	"prev_peak_cores":        "REAL NOT NULL DEFAULT 0",
	"prev_peak_memory_bytes": "INTEGER NOT NULL DEFAULT 0",
}

var pipelineProfilesSustainedCols = map[string]string{
	"sustained_cores":      "REAL NOT NULL DEFAULT 0",
	"prev_sustained_cores": "REAL NOT NULL DEFAULT 0",
}

const pipelineProfilesSustainedBackfill = `UPDATE pipeline_profiles
   SET sustained_cores = peak_cores, prev_sustained_cores = prev_peak_cores
 WHERE sustained_cores = 0`

var nodesUsageCols = map[string]string{
	"cpu_nanos":          "INTEGER NOT NULL DEFAULT 0",
	"max_rss_bytes":      "INTEGER NOT NULL DEFAULT 0",
	"process_wall_nanos": "INTEGER NOT NULL DEFAULT 0",
}

var nodesOfferCols = map[string]string{
	"prefers_labels":         "BLOB",
	"requested_cores":        "REAL NOT NULL DEFAULT 0",
	"requested_memory_bytes": "INTEGER NOT NULL DEFAULT 0",
	"requested_slots":        "INTEGER NOT NULL DEFAULT 1",
	"offer_started_at":       "INTEGER",
	"offer_priority_ceiling": "INTEGER NOT NULL DEFAULT 100",
	"claim_base_priority":    "INTEGER NOT NULL DEFAULT 0",
	"claim_priority":         "INTEGER NOT NULL DEFAULT 0",
	"claim_worker_id":        "TEXT NOT NULL DEFAULT ''",
	"claim_executor_kind":    "TEXT NOT NULL DEFAULT ''",
	"claim_reservation_id":   "TEXT NOT NULL DEFAULT ''",
}

var nodeDispatchRedactionCols = map[string]string{
	"redacted_keys": "BLOB",
}

var nodeMetricsCPUTimeCols = map[string]string{
	"cpu_time_nanos": "INTEGER NOT NULL DEFAULT 0",
}

var usersScopesCols = map[string]string{
	"scopes": "TEXT NOT NULL DEFAULT 'admin'",
}

var triggerRepoInheritedCols = map[string]string{
	"repo_inherited": "INTEGER NOT NULL DEFAULT 0",
}

var triggerSubmissionCols = map[string]string{
	"idempotency_key": "TEXT NOT NULL DEFAULT ''",
	"claim_seq":       "INTEGER NOT NULL DEFAULT 0",
}

// #nosec G101 -- column names, not credentials
var triggerClaimOwnerCols = map[string]string{
	"claim_principal":    "TEXT NOT NULL DEFAULT ''",
	"claim_token_prefix": "TEXT NOT NULL DEFAULT ''",
}

var secretsSharedCols = map[string]string{
	"shared": "INTEGER NOT NULL DEFAULT 0",
}

var triggerWebhookDeliveryCols = map[string]string{
	"webhook_delivery": "TEXT NOT NULL DEFAULT ''",
}

var triggerWebhookReplayKeyCols = map[string]string{
	"webhook_replay_key": "TEXT NOT NULL DEFAULT ''",
}

// TriggerIdempotencyIndexName is the unique index enforcing at most one
// trigger per (pipeline, idempotency key). Exported so a schema test can
// assert the constraint exists by name rather than inferring it from an
// insert that happens to fail.
const TriggerIdempotencyIndexName = "idx_triggers_idempotency_key"

const triggerIdempotencyIndex = `CREATE UNIQUE INDEX IF NOT EXISTS ` + TriggerIdempotencyIndexName + `
    ON triggers(pipeline, idempotency_key) WHERE idempotency_key != ''`

// TriggerWebhookDeliveryIndexName is the unique index enforcing at most
// one trigger per webhook delivery id. Exported so a schema test can
// assert the constraint exists by name.
const TriggerWebhookDeliveryIndexName = "idx_triggers_webhook_delivery"

const triggerWebhookDeliveryColumn = "webhook_delivery"

const triggerWebhookDeliveryIndex = `CREATE UNIQUE INDEX IF NOT EXISTS ` + TriggerWebhookDeliveryIndexName + `
    ON triggers(` + triggerWebhookDeliveryColumn + `) WHERE ` + triggerWebhookDeliveryColumn + ` != ''`

// TriggerWebhookReplayKeyIndexName is the unique index enforcing at most
// one trigger per digest of the signed material a webhook delivery
// carried. Exported so a schema test can assert the constraint exists by
// name.
const TriggerWebhookReplayKeyIndexName = "idx_triggers_webhook_replay_key"

const triggerWebhookReplayKeyColumn = "webhook_replay_key"

const triggerWebhookReplayKeyIndex = `CREATE UNIQUE INDEX IF NOT EXISTS ` + TriggerWebhookReplayKeyIndexName + `
    ON triggers(` + triggerWebhookReplayKeyColumn + `) WHERE ` + triggerWebhookReplayKeyColumn + ` != ''`

// TokenPrefixIndexName is the unique index enforcing one token row per
// prefix. Exported so a schema test can assert the constraint exists by
// name.
const TokenPrefixIndexName = "idx_tokens_prefix" // #nosec G101 -- an index name, not a credential

const tokenPrefixColumn = "prefix"

const tokenPrefixIndex = `CREATE UNIQUE INDEX IF NOT EXISTS ` + TokenPrefixIndexName +
	` ON tokens(` + tokenPrefixColumn + `)`

const tokenPrefixIndexDrop = `DROP INDEX IF EXISTS ` + TokenPrefixIndexName

// #nosec G101 -- a query over the prefix column, which holds a token handle and no secret
const tokenPrefixDuplicates = `SELECT prefix FROM tokens GROUP BY prefix HAVING COUNT(*) > 1 ORDER BY prefix`

func ensureColumnsAllSQLite(ctx context.Context, tx *storeTx) error {
	for _, spec := range columnMigrations {
		if err := ensureColumnsSQLite(ctx, tx, spec.table, spec.cols); err != nil {
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

func translateColumnType(t string) string {
	r := strings.NewReplacer(
		"INTEGER", "BIGINT",
		"BLOB", "BYTEA",
	)
	return r.Replace(t)
}

func backfillRunAnnotationRollup(ctx context.Context, q migrationQueryExecer) error {
	rows, err := q.QueryContext(ctx, `SELECT id FROM runs WHERE annotation_count = 0`)
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
		gathered, err := gatherRunAnnotations(ctx, q, id)
		if err != nil {
			return err
		}
		if len(gathered) == 0 {
			continue
		}
		blob, _ := json.Marshal(gathered)
		if _, err := q.ExecContext(ctx, `
UPDATE runs SET annotation_count = ?, top_annotation = ?, annotations_json = ?
WHERE id = ?`, len(gathered), gathered[len(gathered)-1], blob, id); err != nil {
			return err
		}
	}
	return nil
}

func gatherRunAnnotations(ctx context.Context, q migrationQueryExecer, runID string) ([]string, error) {
	var out []string
	rows, err := q.QueryContext(ctx, `
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

// hack: SQLite cannot alter a primary key in place, so widening it to (name, repo) rebuilds the table.
var secretsRepoRebuildSQLite = []string{
	`DROP TABLE IF EXISTS secrets_repo_scoped`,
	`CREATE TABLE secrets_repo_scoped (
    name       TEXT NOT NULL,
    value      TEXT NOT NULL,
    principal  TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    masked     INTEGER NOT NULL DEFAULT 1,
    repo       TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (name, repo)
)`,
	`INSERT INTO secrets_repo_scoped (name, value, principal, created_at, updated_at, masked, repo)
    SELECT name, value, principal, created_at, updated_at, masked, '' FROM secrets`,
	`DROP TABLE secrets`,
	`ALTER TABLE secrets_repo_scoped RENAME TO secrets`,
}

var secretRepoScopePostgres = []string{
	`ALTER TABLE secrets ADD COLUMN IF NOT EXISTS repo TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE secrets DROP CONSTRAINT IF EXISTS secrets_pkey`,
	`ALTER TABLE secrets ADD PRIMARY KEY (name, repo)`,
}

func addSecretRepoScope(ctx context.Context, tx *storeTx) error {
	have, err := tableColumns(ctx, tx, "secrets")
	if err != nil {
		return err
	}
	if have["repo"] {
		return nil
	}
	for _, stmt := range secretsRepoRebuildSQLite {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

func addTriggerWebhookDelivery(ctx context.Context, tx *storeTx) error {
	have, err := tableColumns(ctx, tx, "triggers")
	if err != nil {
		return err
	}
	if !have[triggerWebhookDeliveryColumn] {
		if _, err := tx.ExecContext(ctx,
			`ALTER TABLE triggers ADD COLUMN "webhook_delivery" TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, triggerWebhookDeliveryIndex)
	return err
}

func addTriggerWebhookReplayKey(ctx context.Context, tx *storeTx) error {
	have, err := tableColumns(ctx, tx, "triggers")
	if err != nil {
		return err
	}
	if !have[triggerWebhookReplayKeyColumn] {
		if _, err := tx.ExecContext(ctx,
			`ALTER TABLE triggers ADD COLUMN "webhook_replay_key" TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, triggerWebhookReplayKeyIndex)
	return err
}

func uniqueTokenPrefixIndexTx(ctx context.Context, q migrationQueryExecer) error {
	dupes, err := duplicateTokenPrefixes(ctx, q)
	if err != nil {
		return err
	}
	if len(dupes) > 0 {
		return fmt.Errorf(
			"tokens: prefix %s names more than one token row; keep the live row, "+
				"delete the revoked duplicates, then upgrade",
			strings.Join(dupes, ", "))
	}
	if _, err := q.ExecContext(ctx, tokenPrefixIndexDrop); err != nil {
		return err
	}
	_, err = q.ExecContext(ctx, tokenPrefixIndex)
	return err
}

func duplicateTokenPrefixes(ctx context.Context, q migrationQueryExecer) ([]string, error) {
	rows, err := q.QueryContext(ctx, tokenPrefixDuplicates)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var prefix string
		if err := rows.Scan(&prefix); err != nil {
			return nil, err
		}
		out = append(out, prefix)
	}
	return out, rows.Err()
}

func ensureColumnsSQLite(ctx context.Context, tx *storeTx, table string, cols map[string]string) error {
	have, err := tableColumns(ctx, tx, table)
	if err != nil {
		return err
	}
	for name, typ := range cols {
		if have[name] {
			continue
		}
		stmt := fmt.Sprintf(`ALTER TABLE %q ADD COLUMN %q %s`, table, name, typ)
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			if isDuplicateColumnErr(err) {
				continue
			}
			return fmt.Errorf("add column %s.%s: %w", table, name, err)
		}
	}
	return nil
}

func tableColumns(ctx context.Context, tx *storeTx, table string) (map[string]bool, error) {
	rows, err := tx.QueryContext(ctx, fmt.Sprintf(`PRAGMA table_info(%q)`, table))
	if err != nil {
		return nil, err
	}
	have := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			_ = rows.Close()
			return nil, err
		}
		have[name] = true
	}
	_ = rows.Close()
	return have, rows.Err()
}

func isDuplicateColumnErr(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "duplicate column name")
}

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
// The repository identity (repo, repo URL, GitHub owner and name) is
// written once, at the insert that first names it, and every later
// upsert keeps it: secret resolution reads it, so a caller that can
// upsert a run must not be able to repoint it.
func (s *Store) CreateRun(ctx context.Context, r Run) error {
	if err := ValidateRunInvocation(r); err != nil {
		return err
	}
	argsJSON, _ := json.Marshal(r.Args)
	var invocationJSON []byte
	if len(r.Invocation) > 0 {
		invocationJSON, _ = json.Marshal(r.Invocation)
	}
	var parent sql.NullString
	if r.ParentRunID != "" {
		parent = sql.NullString{String: r.ParentRunID, Valid: true}
	}
	created := r.CreatedAt
	if created.IsZero() {
		created = r.StartedAt
	}
	var heartbeat sql.NullInt64
	if r.Status == runStatusRunning {
		heartbeat = sql.NullInt64{Int64: time.Now().UnixNano(), Valid: true}
	}
	_, err := s.exec(
		ctx, `
INSERT INTO runs (id, pipeline, status, trigger_source, git_branch, git_sha, args_json, plan_json, created_at, started_at, parent_run_id, repo, repo_url, github_owner, github_repo, retry_of, retried_as, retry_source, replay_of_run_id, replay_of_node_id, invocation_json, last_heartbeat_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
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
    repo            = CASE WHEN runs.repo = '' THEN excluded.repo ELSE runs.repo END,
    repo_url        = CASE WHEN runs.repo_url = '' THEN excluded.repo_url ELSE runs.repo_url END,
    github_owner    = CASE WHEN runs.github_owner = '' THEN excluded.github_owner ELSE runs.github_owner END,
    github_repo     = CASE WHEN runs.github_repo = '' THEN excluded.github_repo ELSE runs.github_repo END,
    retry_of        = excluded.retry_of,
    retried_as      = excluded.retried_as,
    retry_source    = excluded.retry_source,
    replay_of_run_id  = excluded.replay_of_run_id,
    replay_of_node_id = excluded.replay_of_node_id,
    invocation_json   = excluded.invocation_json,
    last_heartbeat_at = COALESCE(excluded.last_heartbeat_at, runs.last_heartbeat_at)
WHERE runs.status = '`+runStatusPending+`'`,
		r.ID, r.Pipeline, r.Status, r.TriggerSource, r.GitBranch, r.GitSHA,
		argsJSON, r.PlanSnapshot, created.UnixNano(), r.StartedAt.UnixNano(), parent,
		r.Repo, r.RepoURL, r.GithubOwner, r.GithubRepo,
		r.RetryOf, r.RetriedAs, r.RetrySource, r.ReplayOfRunID, r.ReplayOfNodeID,
		invocationJSON, heartbeat,
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

// FinishRunsIfActive atomically finalizes the named non-terminal runs. A
// failure rolls back every member, so one shared lease cannot be partly
// cancelled.
func (s *Store) FinishRunsIfActive(ctx context.Context, runIDs []string, status, errMsg string) error {
	if len(runIDs) == 0 {
		return nil
	}
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UnixNano()
	for _, runID := range runIDs {
		if _, err := tx.ExecContext(ctx, `UPDATE runs SET status = ?, error = ?, finished_at = ? WHERE id = ? AND status NOT IN ('success','failed','cancelled')`, status, errMsg, now, runID); err != nil {
			return err
		}
	}
	return tx.Commit()
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
	Pipelines      []string
	Statuses       []string
	GitSHAPrefixes []string
	GitBranches    []string
	Repos          []string
	RepoURLs       []string
	Since          time.Time
	Limit          int // <=0 = default
	ParentRunID    string
	RootOnly       bool
}

// ListRuns returns runs ordered newest-first, filtered by f.
func (s *Store) ListRuns(ctx context.Context, f RunFilter) ([]*Run, error) {
	normalizedPrefixes := make([]string, len(f.GitSHAPrefixes))
	for i, prefix := range f.GitSHAPrefixes {
		prefix = strings.ToLower(strings.TrimSpace(prefix))
		if prefix == "" || strings.IndexFunc(prefix, func(r rune) bool {
			return (r < '0' || r > '9') && (r < 'a' || r > 'f')
		}) >= 0 {
			return nil, fmt.Errorf("git SHA prefix %q must contain hexadecimal characters", prefix)
		}
		normalizedPrefixes[i] = prefix
	}
	f.GitSHAPrefixes = normalizedPrefixes
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	// safety: clamp again here so a non-HTTP caller cannot ask for every row.
	limit = min(limit, MaxRunListLimit)

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
	addIn("git_branch", f.GitBranches)
	addIn("repo", f.Repos)
	addIn("repo_url", f.RepoURLs)
	addClause := func(clause string, values ...any) {
		if where == "" {
			where = " WHERE " + clause
		} else {
			where += " AND " + clause
		}
		args = append(args, values...)
	}
	if len(f.GitSHAPrefixes) > 0 {
		parts := make([]string, 0, len(f.GitSHAPrefixes))
		values := make([]any, 0, len(f.GitSHAPrefixes)*2)
		for _, prefix := range f.GitSHAPrefixes {
			if upper, ok := prefixUpperBound(prefix); ok {
				parts = append(parts, "(git_sha >= ? AND git_sha < ?)")
				values = append(values, prefix, upper)
			} else {
				parts = append(parts, "git_sha = ?")
				values = append(values, prefix)
			}
		}
		addClause("("+strings.Join(parts, " OR ")+")", values...)
	}
	if f.ParentRunID != "" {
		addClause("parent_run_id = ?", f.ParentRunID)
	} else if f.RootOnly {
		addClause("(parent_run_id IS NULL OR parent_run_id = '')")
	}
	if !f.Since.IsZero() {
		addClause("started_at >= ?", f.Since.UnixNano())
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

func prefixUpperBound(prefix string) (string, bool) {
	b := []byte(prefix)
	for i := len(b) - 1; i >= 0; i-- {
		if b[i] == 0xff {
			continue
		}
		b[i]++
		return string(b[:i+1]), true
	}
	return "", false
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
		     AND `+runTerminalIn,
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
	ReadyAt              *time.Time `json:"ready_at,omitempty"`
	ClaimedBy            string     `json:"claimed_by,omitempty"`
	LeaseExpiresAt       *time.Time `json:"lease_expires_at,omitempty"`
	OfferStartedAt       *time.Time `json:"offer_started_at,omitempty"`
	OfferPriorityCeiling int        `json:"offer_priority_ceiling,omitempty"`
	ClaimBasePriority    int        `json:"claim_base_priority,omitempty"`
	ClaimPriority        int        `json:"claim_priority,omitempty"`
	ClaimWorkerID        string     `json:"claim_worker_id,omitempty"`
	ClaimExecutorKind    string     `json:"claim_executor_kind,omitempty"`
	ClaimReservationID   string     `json:"claim_reservation_id,omitempty"`

	// NeedsLabels: runner labels required (AND semantics). Empty = any.
	NeedsLabels []string `json:"needs_labels,omitempty"`
	// PrefersLabels orders soft executor preferences.
	PrefersLabels []string `json:"prefers_labels,omitempty"`

	RequestedCores       float64 `json:"requested_cores,omitempty"`
	RequestedMemoryBytes int64   `json:"requested_memory_bytes,omitempty"`
	RequestedSlots       int     `json:"requested_slots,omitempty"`

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

	// CPUNanos is the user plus system CPU time the kernel accounted to
	// the process that executed this node, MaxRSSBytes the peak resident
	// set size it reported for that process, and ProcessWallNanos the
	// span that process existed for, spawn to reap. They are written by
	// a runner that supervised a process of its own, and stay zero for a
	// node executed anywhere else -- a Kubernetes pod, a node run inside
	// the dispatcher; zero means absent, not measured-as-nothing. Exact
	// where the per-interval sampler is not: a node shorter than one
	// sampling interval has no metric samples at all and still reports
	// what it cost here.
	//
	// ProcessWallNanos is the denominator CPUNanos belongs over. It is
	// wider than FinishedAt.Sub(StartedAt), which the node stamps from
	// inside itself once startup is done, and both figures are kept
	// because they answer different questions: how long the box was
	// occupied, and how long the node's own work took. A node retried in
	// place accumulates every attempt here -- the machine paid for all of
	// them -- while MaxRSSBytes keeps the high-water across attempts.
	CPUNanos         int64 `json:"cpu_nanos,omitempty"`
	MaxRSSBytes      int64 `json:"max_rss_bytes,omitempty"`
	ProcessWallNanos int64 `json:"process_wall_nanos,omitempty"`

	// ArtifactManifest is the content-addressed digest of the manifest
	// describing the files this node published as artifacts (see
	// JobNode.Outputs). Empty when the node declared no outputs or no
	// artifact store was configured. A cache replay copies this digest
	// onto the replayed node so the producer's file set reproduces
	// without re-running it.
	ArtifactManifest string `json:"artifact_manifest,omitempty"`
}

// CreateNode inserts a node in the "pending" state.
func (s *Store) CreateNode(ctx context.Context, n Node) error {
	depsJSON, _ := json.Marshal(n.Deps)
	var labelsJSON []byte
	if len(n.NeedsLabels) > 0 {
		labelsJSON, _ = json.Marshal(n.NeedsLabels)
	}
	var prefersJSON []byte
	if len(n.PrefersLabels) > 0 {
		prefersJSON, _ = json.Marshal(n.PrefersLabels)
	}
	requestedSlots := n.RequestedSlots
	if requestedSlots < 1 {
		requestedSlots = 1
	}
	_, err := s.exec(ctx, `
INSERT INTO nodes (run_id, node_id, status, deps_json, needs_labels, prefers_labels,
                   requested_cores, requested_memory_bytes, requested_slots)
VALUES (?,?,?,?,?,?,?,?,?)`, n.RunID, n.NodeID, n.Status, depsJSON, labelsJSON, prefersJSON,
		n.RequestedCores, n.RequestedMemoryBytes, requestedSlots)
	return err
}

// StartNode marks a node as running and stamps started_at.
//
// It is idempotent for a node that has not finished, which is what a
// re-executed node needs: a bounced node's replacement process calls
// it again, and the fresh stamp is what makes the node's recorded
// duration measure the attempt that survived.
//
// It will not reopen a node that already recorded its outcome. A
// terminal row is the executing process's own verdict and the last
// word on the node -- FinishNodeWithReason refuses to overwrite one
// for the same reason -- so a start arriving after it is a no-op
// rather than a resurrection. Without that guard a re-execution racing
// a terminal write would flip the row back to running with its outcome
// still attached, and the second execution's finish, which the
// terminal guard would otherwise have refused, would land on a node
// that had already succeeded.
//
// A no-op is silent: every caller starts a node it is about to
// execute and finish, and none reads the row count.
func (s *Store) StartNode(ctx context.Context, runID, nodeID string) error {
	_, err := s.exec(ctx, `
UPDATE nodes SET status = ?, started_at = ?
 WHERE run_id = ? AND node_id = ? AND status != ?`,
		nodeStatusRunning, time.Now().UnixNano(), runID, nodeID, nodeStatusDone)
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
	   SET status = ?, outcome = ?, error = ?, output_json = ?, finished_at = ?,
	       failure_reason = ?, exit_code = ?
	 WHERE run_id = ? AND node_id = ? AND NOT (status = ? AND outcome != '')`,
		nodeStatusDone, outcome, errMsg, output, time.Now().UnixNano(),
		reason, code,
		runID, nodeID, nodeStatusDone)
	return err
}

// SetNodeArtifactManifest records the content-addressed digest of a
// node's published-artifact manifest. Written before the terminal
// FinishNode flip so a consumer dispatched on completion always sees
// the reference. Empty digest is a no-op-equivalent clear.
func (s *Store) SetNodeArtifactManifest(ctx context.Context, runID, nodeID, manifestDigest string) error {
	_, err := s.exec(ctx,
		`UPDATE nodes SET artifact_manifest = ? WHERE run_id = ? AND node_id = ?`,
		manifestDigest, runID, nodeID)
	return err
}

// NodeUsage is the kernel's exit accounting for one process that
// executed a node: CPUTime is user plus system time across that process
// tree, MaxRSSBytes its peak resident set size, and Wall the span it
// existed for, spawn to reap.
type NodeUsage struct {
	CPUTime     time.Duration
	MaxRSSBytes int64
	Wall        time.Duration
}

// AddNodeUsage folds one finished process's accounting into a node's
// row. Called by the runner that supervised the process, after the
// process it describes has exited and therefore after the node's own
// terminal row -- which is why it is a separate write rather than a
// FinishNode argument: the figures do not exist until the process is
// reaped, and the node that wrote its own outcome is not the process
// that can read them.
//
// It adds rather than replaces because a node can be executed more than
// once: an auto-retry runs a fresh process per attempt, and the machine
// paid for every one of them, so CPU and wall accumulate. Peak RSS takes
// the high-water instead, since the attempts did not hold their peaks at
// the same time. A non-positive figure contributes nothing, and zero
// stays the value every reader treats as absent.
func (s *Store) AddNodeUsage(ctx context.Context, runID, nodeID string, u NodeUsage) error {
	cpuNanos := max(int64(u.CPUTime), 0)
	wallNanos := max(int64(u.Wall), 0)
	maxRSSBytes := max(u.MaxRSSBytes, 0)
	_, err := s.exec(ctx, `
UPDATE nodes
   SET cpu_nanos = cpu_nanos + ?,
       process_wall_nanos = process_wall_nanos + ?,
       max_rss_bytes = CASE WHEN ? > max_rss_bytes THEN ? ELSE max_rss_bytes END
 WHERE run_id = ? AND node_id = ?`,
		cpuNanos, wallNanos, maxRSSBytes, maxRSSBytes, runID, nodeID)
	return err
}

// ListNodes returns the nodes for a run in insertion order.
func (s *Store) ListNodes(ctx context.Context, runID string) ([]*Node, error) {
	rows, err := s.query(ctx, `SELECT `+nodeSelectColumns+`
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
	row := s.queryRow(ctx, `SELECT `+nodeSelectColumns+`
  FROM nodes
 WHERE run_id = ? AND node_id = ?`, runID, nodeID)
	n := &Node{}
	if err := scanNodeRow(row, n); err != nil {
		return nil, err
	}
	return n, nil
}

const nodeSelectColumns = `run_id, node_id, status, outcome, deps_json, error, output_json, started_at, finished_at,
       ready_at, claimed_by, lease_expires_at, needs_labels, prefers_labels,
       requested_cores, requested_memory_bytes, requested_slots,
       offer_started_at, offer_priority_ceiling, claim_base_priority, claim_priority,
       claim_worker_id, claim_executor_kind, claim_reservation_id,
       status_detail, last_heartbeat, failure_reason, exit_code, annotations_json, summary,
       artifact_manifest, cpu_nanos, max_rss_bytes, process_wall_nanos`

func scanNodeRow(rs rowScanner, n *Node) error {
	var depsJSON, outputJSON, labelsJSON, prefersJSON, annotationsJSON []byte
	var startedNS, finishedNS, readyNS, leaseNS, offerStartedNS, heartbeatNS sql.NullInt64
	var claimedBy sql.NullString
	var exitCode sql.NullInt64
	err := rs.Scan(&n.RunID, &n.NodeID, &n.Status, &n.Outcome,
		&depsJSON, &n.Error, &outputJSON, &startedNS, &finishedNS,
		&readyNS, &claimedBy, &leaseNS, &labelsJSON, &prefersJSON,
		&n.RequestedCores, &n.RequestedMemoryBytes, &n.RequestedSlots,
		&offerStartedNS, &n.OfferPriorityCeiling, &n.ClaimBasePriority, &n.ClaimPriority,
		&n.ClaimWorkerID, &n.ClaimExecutorKind, &n.ClaimReservationID,
		&n.StatusDetail, &heartbeatNS,
		&n.FailureReason, &exitCode, &annotationsJSON, &n.Summary, &n.ArtifactManifest,
		&n.CPUNanos, &n.MaxRSSBytes, &n.ProcessWallNanos)
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
	if len(prefersJSON) > 0 {
		_ = json.Unmarshal(prefersJSON, &n.PrefersLabels)
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
	if offerStartedNS.Valid {
		t := time.Unix(0, offerStartedNS.Int64)
		n.OfferStartedAt = &t
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
// step_start and transitioned to passed/failed/cancelled on step_end. Skipped
// steps insert directly as StepSkipped with started_at == finished_at.
const (
	StepRunning   = "running"
	StepPassed    = "passed"
	StepFailed    = "failed"
	StepCancelled = "cancelled"
	StepSkipped   = "skipped"
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

// FinishNodeStep transitions a running step to passed/failed/cancelled and
// stamps finished_at. Caller passes StepPassed, StepFailed, or StepCancelled.
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

// MarkNodeReady opens the node's offer round with the absolute priority ceiling.
func (s *Store) MarkNodeReady(ctx context.Context, runID, nodeID string) error {
	return s.MarkNodeReadyWithPriorityCeiling(ctx, runID, nodeID, 100)
}

// MarkNodeReadyWithPriorityCeiling opens an idempotent offer round with the
// highest effective priority among eligible executors at that instant.
func (s *Store) MarkNodeReadyWithPriorityCeiling(ctx context.Context, runID, nodeID string, ceiling int) error {
	if ceiling < 0 || ceiling > 100 {
		return fmt.Errorf("node offer priority ceiling %d: expected 0 through 100", ceiling)
	}
	now := time.Now().UnixNano()
	res, err := s.exec(
		ctx,
		`UPDATE nodes SET ready_at = COALESCE(ready_at, ?),
		                  offer_started_at = COALESCE(offer_started_at, ?),
		                  offer_priority_ceiling = CASE WHEN offer_started_at IS NULL THEN ? ELSE offer_priority_ceiling END
		  WHERE run_id = ? AND node_id = ?`,
		now, now, ceiling, runID, nodeID,
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

// RevokeNodeReady cancels an unclaimed offer round.
func (s *Store) RevokeNodeReady(ctx context.Context, runID, nodeID string) (bool, error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx,
		`UPDATE nodes SET ready_at = NULL, offer_started_at = NULL
		  WHERE run_id = ? AND node_id = ?
		    AND claimed_by IS NULL AND `+nodeNotDone,
		runID, nodeID,
	)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if n == 0 {
		if err := tx.Commit(); err != nil {
			return false, err
		}
		return false, nil
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM node_claim_offers WHERE run_id = ? AND node_id = ?`, runID, nodeID); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// ClaimNextReadyNode flips the oldest claimable node to holderID with
// a fresh lease, bound to principal. Claimable = ready_at set,
// unclaimed, !done, and every needs_labels entry appears in
// runnerLabels. ErrNotFound when no candidate matches.
// Label-mismatched candidates have their ready_at bumped 1us so they
// don't starve the FIFO queue.
//
// claimant is the authenticated token the claim answers to;
// [Store.PrincipalHoldsNodeClaim] and [Store.HeartbeatNodeClaim] admit
// only that token afterwards. Pass the zero value when the caller is
// unauthenticated, which leaves the claim unbound. lease is clamped to
// [MaxLeaseDuration].
func (s *Store) ClaimNextReadyNode(ctx context.Context, claimant ClaimIdentity, holderID string, lease time.Duration, runnerLabels []string) (*Node, error) {
	lease = clampNodeLease(lease)
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
		err = scanNodeRow(tx.QueryRowContext(ctx, `SELECT `+nodeSelectColumns+`
  FROM nodes
 WHERE ready_at IS NOT NULL AND claimed_by IS NULL AND `+nodeNotDone+`
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
			`UPDATE nodes SET claimed_by = ?, claim_principal = ?, claim_token_prefix = ?,
			        lease_expires_at = ?
			  WHERE run_id = ? AND node_id = ? AND claimed_by IS NULL`,
			holderID, claimant.Principal, claimant.TokenPrefix, expires.UnixNano(), n.RunID, n.NodeID,
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
// caller no longer owns the claim. The token the claim was bound to and
// the holder id it was taken under must both match. lease is clamped to
// [MaxLeaseDuration], so a heartbeat cannot outrun the claim cap.
func (s *Store) HeartbeatNodeClaim(ctx context.Context, runID, nodeID string, claimant ClaimIdentity, holderID string, lease time.Duration) error {
	expires := time.Now().Add(clampNodeLease(lease)).UnixNano()
	res, err := s.exec(
		ctx,
		`UPDATE nodes SET lease_expires_at = ?
		  WHERE run_id = ? AND node_id = ? AND claimed_by = ?
		    AND COALESCE(claim_principal, '') = ?
		    AND COALESCE(claim_token_prefix, '') = ?`,
		expires, runID, nodeID, holderID, claimant.Principal, claimant.TokenPrefix,
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

const nodeClaimLiveSQL = `claimed_by IS NOT NULL
		    AND lease_expires_at IS NOT NULL AND lease_expires_at > ?`

const triggerClaimLiveSQL = `status = 'claimed'
		    AND lease_expires_at IS NOT NULL AND lease_expires_at > ?`

// PrincipalHoldsTriggerClaim reports whether claimant holds the
// trigger's unexpired claim. A trigger id is the id of the run it
// creates, so this is the ownership proof a dispatcher has before any
// node of that run is claimed. An unbound claimant holds nothing.
func (s *Store) PrincipalHoldsTriggerClaim(ctx context.Context, triggerID string, claimant ClaimIdentity, now time.Time) (bool, error) {
	if !claimant.bound() {
		return false, nil
	}
	var held int
	err := s.queryRow(ctx,
		`SELECT COUNT(*) FROM triggers
		  WHERE id = ? AND claim_principal = ?
		    AND claim_token_prefix = ?
		    AND `+triggerClaimLiveSQL,
		triggerID, claimant.Principal, claimant.TokenPrefix, now.UnixNano()).Scan(&held)
	if err != nil {
		return false, err
	}
	return held > 0, nil
}

// PrincipalHoldsNodeClaim reports whether claimant holds the node's
// unexpired claim. An unbound claimant holds nothing, so an
// unauthenticated caller never passes this check.
func (s *Store) PrincipalHoldsNodeClaim(ctx context.Context, runID, nodeID string, claimant ClaimIdentity, now time.Time) (bool, error) {
	if !claimant.bound() {
		return false, nil
	}
	var held int
	err := s.queryRow(ctx,
		`SELECT COUNT(*) FROM nodes
		  WHERE run_id = ? AND node_id = ? AND claim_principal = ?
		    AND claim_token_prefix = ?
		    AND `+nodeClaimLiveSQL,
		runID, nodeID, claimant.Principal, claimant.TokenPrefix, now.UnixNano()).Scan(&held)
	if err != nil {
		return false, err
	}
	return held > 0, nil
}

// PrincipalHoldsRunClaim reports whether claimant holds an unexpired
// claim on any node of the run. An unbound claimant holds nothing.
func (s *Store) PrincipalHoldsRunClaim(ctx context.Context, runID string, claimant ClaimIdentity, now time.Time) (bool, error) {
	if !claimant.bound() {
		return false, nil
	}
	var held int
	err := s.queryRow(ctx,
		`SELECT COUNT(*) FROM nodes
		  WHERE run_id = ? AND claim_principal = ?
		    AND claim_token_prefix = ?
		    AND `+nodeClaimLiveSQL,
		runID, claimant.Principal, claimant.TokenPrefix, now.UnixNano()).Scan(&held)
	if err != nil {
		return false, err
	}
	return held > 0, nil
}

// PrincipalHoldsPipelineClaim reports whether claimant holds an
// unexpired claim on any node of any run of the named pipeline. It is
// the ownership proof for a write that names a pipeline instead of a
// run. An unbound claimant holds nothing.
func (s *Store) PrincipalHoldsPipelineClaim(ctx context.Context, pipeline string, claimant ClaimIdentity, now time.Time) (bool, error) {
	if !claimant.bound() {
		return false, nil
	}
	var held int
	err := s.queryRow(ctx,
		`SELECT COUNT(*) FROM nodes
		  WHERE run_id IN (SELECT id FROM runs WHERE pipeline = ?)
		    AND claim_principal = ? AND claim_token_prefix = ?
		    AND `+nodeClaimLiveSQL,
		pipeline, claimant.Principal, claimant.TokenPrefix, now.UnixNano()).Scan(&held)
	if err != nil {
		return false, err
	}
	return held > 0, nil
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
		    AND lease_expires_at < ? AND `+nodeNotDone+s.forUpdateSkipLocked(),
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
		`UPDATE nodes SET claimed_by = NULL, claim_principal = '', claim_token_prefix = '',
		        lease_expires_at = NULL
		  WHERE claimed_by IS NOT NULL AND lease_expires_at IS NOT NULL
		    AND lease_expires_at < ? AND `+nodeNotDone,
		now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return pairs, nil
}

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
		    AND lease_expires_at < ? AND `+nodeNotDone+s.forUpdateSkipLocked(),
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
   SET `+nodeFailSet+`,
       error = 'runner heartbeat expired',
       failure_reason = ?, finished_at = ?,
       claimed_by = NULL, claim_principal = '', claim_token_prefix = '',
       lease_expires_at = NULL
 WHERE run_id = ? AND node_id = ? AND `+nodeNotDone,
			FailureAgentLost, now, p[0], p[1]); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return pairs, nil
}

func (s *Store) failNodesInRun(ctx context.Context, runID, errMsg, failureReason string) ([]string, error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.QueryContext(ctx,
		`SELECT node_id FROM nodes WHERE run_id = ? AND `+nodeNotDone, runID)
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
   SET `+nodeFailSet+`,
       error = ?, failure_reason = ?, finished_at = ?,
       ready_at = NULL
 WHERE run_id = ? AND node_id = ? AND `+nodeNotDone,
			errMsg, failureReason, now, runID, nid); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return nodeIDs, nil
}

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
		    AND ready_at < ? AND `+nodeNotDone,
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
   SET `+nodeFailSet+`,
       error = 'no runner claimed this node before the queue deadline',
       failure_reason = ?, finished_at = ?,
       ready_at = NULL
 WHERE run_id = ? AND node_id = ? AND claimed_by IS NULL AND `+nodeNotDone,
			FailureQueueTimeout, now, p[0], p[1]); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return pairs, nil
}

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
	limit = min(limit, MaxRunListLimit)
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
	// RepoInherited distinguishes an implicit same-repository await from an
	// explicit cross-repository request after parent provenance is copied.
	RepoInherited bool `json:"repo_inherited,omitempty"`
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
	// IdempotencyKey is a caller-supplied deduplication token, scoped to
	// the pipeline. At most one trigger may carry any given (pipeline,
	// non-empty key) pair -- a partial unique index enforces it -- so a
	// submitter that retries after an ambiguous failure re-reaches the
	// original run instead of starting a second one. Empty is the norm
	// and is exempt from the index.
	//
	// The scope is the pipeline and not the whole store because a key is
	// a caller's name for one intent, and two pipelines naming their
	// intents independently is the normal case: a global namespace would
	// let `submit beta --idempotency-key nightly` answer with alpha's run
	// and never execute beta at all.
	//
	// It is deliberately not the trigger's tracing identity: a caller
	// correlating log lines wants a fresh id per attempt, while dedup
	// wants the same token across attempts of one intent. Tracing ids
	// ride in TriggerEnv; only this field decides whether a submission
	// is a duplicate.
	IdempotencyKey string `json:"idempotency_key,omitempty"`
	// ClaimSeq counts how many times this trigger has been claimed. It
	// is the fence that makes a re-claim invalidate the previous
	// dispatch: a claim reads the value it was given, and a terminal
	// write that presents a stale value is refused rather than applied.
	//
	// Without it, a dispatch whose claim lapsed and was taken by another
	// consumer could still finish and stamp its own outcome over the run
	// the new claim is producing -- two writers, one run row, and the
	// last to finish wins regardless of which one the store considers
	// current.
	ClaimSeq int64 `json:"claim_seq,omitempty"`
	// WebhookDelivery is the provider's delivery id for the webhook that
	// created this trigger, empty for every other submission path. At
	// most one trigger may carry any given non-empty value across the
	// whole store -- a partial unique index enforces it -- so replaying
	// a signed delivery, whether at the pipeline it was sent to or at
	// another one, is refused instead of producing a second run.
	//
	// The scope is global and not the pipeline because a delivery id
	// names one event at the provider, not one caller's intent: the same
	// id arriving at two pipelines is a replay, not two requests.
	WebhookDelivery string `json:"webhook_delivery,omitempty"`
	// WebhookReplayKey is a digest of the material the webhook's
	// signature covered -- the pipeline and the request body -- and it
	// carries the replay decision. At most one trigger may hold any
	// given non-empty value, so re-sending a body that was already
	// accepted is refused however its delivery id reads.
	//
	// WebhookDelivery cannot carry that decision alone: it is a header
	// the sender picks and the HMAC does not cover, so anyone who
	// captured one delivery could re-send it under an id of their own.
	//
	// The store writes this field and never reads it back: it exists for
	// the unique constraint, and [Store.FindTriggerByWebhookReplay]
	// resolves a collision to the trigger that won it.
	WebhookReplayKey string `json:"webhook_replay_key,omitempty"`
}

// DefaultLeaseDuration is the claim lease TTL. Wide enough to survive
// CPU-bound pauses; short enough to re-queue dead runners.
const DefaultLeaseDuration = 3 * time.Minute

// MaxLeaseDuration caps the lease a claimant may ask for. The claim is
// an authorization window, so the claimant must not pick how long its
// own access lasts; longer requests are clamped, not rejected, because
// a heartbeat renews well inside the cap.
const MaxLeaseDuration = 10 * time.Minute

// safety: an unbounded lease would make a claim an unreapable, permanent grant.
func clampNodeLease(lease time.Duration) time.Duration {
	if lease <= 0 {
		return DefaultLeaseDuration
	}
	return min(lease, MaxLeaseDuration)
}

// ClaimIdentity is the token a node claim answers to. TokenPrefix is
// unique per token and is what the ownership predicates match on;
// Principal is the display label, which two tokens may share.
type ClaimIdentity struct {
	Principal   string
	TokenPrefix string
}

func (c ClaimIdentity) bound() bool { return c.TokenPrefix != "" }

// CreateTrigger inserts a new trigger with status='pending'.
func (s *Store) CreateTrigger(ctx context.Context, t Trigger) error {
	argsJSON, _ := json.Marshal(t.Args)
	envJSON, _ := json.Marshal(t.TriggerEnv)
	status := t.Status
	if status == "" {
		status = triggerStatusPending
	}
	var parent sql.NullString
	if t.ParentRunID != "" {
		parent = sql.NullString{String: t.ParentRunID, Valid: true}
	}
	fullInt := 0
	if t.Full {
		fullInt = 1
	}
	repoInheritedInt := 0
	if t.RepoInherited {
		repoInheritedInt = 1
	}
	_, err := s.exec(
		ctx, `
INSERT INTO triggers (id, pipeline, args_json, trigger_source, trigger_user,
                      trigger_env, git_branch, git_sha, status, created_at, parent_run_id,
		              repo, repo_url, github_owner, github_repo, repo_inherited, retry_of, retry_source, parent_node_id, "full",
		              idempotency_key, webhook_delivery, webhook_replay_key)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		t.ID, t.Pipeline, argsJSON, t.TriggerSource, t.TriggerUser,
		envJSON, t.GitBranch, t.GitSHA, status, t.CreatedAt.UnixNano(), parent,
		t.Repo, t.RepoURL, t.GithubOwner, t.GithubRepo, repoInheritedInt, t.RetryOf, t.RetrySource, t.ParentNodeID, fullInt,
		t.IdempotencyKey, t.WebhookDelivery, t.WebhookReplayKey,
	)
	if err != nil && isUniqueViolation(err) {
		if t.WebhookReplayKey != "" && strings.Contains(err.Error(), triggerWebhookReplayKeyColumn) {
			return fmt.Errorf("%w: replay key %q", ErrDuplicateWebhookDelivery, t.WebhookReplayKey)
		}
		if t.WebhookDelivery != "" && strings.Contains(err.Error(), triggerWebhookDeliveryColumn) {
			return fmt.Errorf("%w: delivery %q", ErrDuplicateWebhookDelivery, t.WebhookDelivery)
		}
		return fmt.Errorf("%w: idempotency key %q", ErrDuplicateIdempotencyKey, t.IdempotencyKey)
	}
	return err
}

// ErrDuplicateIdempotencyKey reports that a trigger insert lost the race
// to another submission carrying the same idempotency key. It is not a
// failure for the caller to report: the winning trigger is the answer,
// and the caller resolves it with [Store.FindTriggerByIdempotencyKey].
var ErrDuplicateIdempotencyKey = errors.New("store: idempotency key already claimed by another trigger")

// ErrDuplicateWebhookDelivery reports that a trigger insert carried a
// webhook delivery id an earlier trigger already holds. The delivery is
// a replay of work the store has already accepted, so the caller
// refuses it rather than starting a second run.
var ErrDuplicateWebhookDelivery = errors.New("store: webhook delivery already accepted")

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique constraint") ||
		strings.Contains(msg, "duplicate key value")
}

// FindTriggerByIdempotencyKey returns the trigger that already claimed
// key for pipeline, or ErrNotFound when the pair is unused. An empty key
// is never stored as a claim, so it always reports ErrNotFound rather
// than matching the many rows that carry the empty default.
//
// The pipeline is part of the lookup for the same reason it is part of
// the index: a key names one caller's intent, and answering a
// submission of one pipeline with another pipeline's run would mean the
// requested pipeline never runs at all.
func (s *Store) FindTriggerByIdempotencyKey(ctx context.Context, pipeline, key string) (*Trigger, error) {
	if key == "" || pipeline == "" {
		return nil, ErrNotFound
	}
	var id string
	err := s.queryRow(ctx,
		`SELECT id FROM triggers WHERE pipeline = ? AND idempotency_key = ?`,
		pipeline, key).Scan(&id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return s.GetTrigger(ctx, id)
}

// FindTriggerByWebhookReplay returns the trigger a refused webhook
// delivery collided with: the one holding replayKey, or failing that the
// one holding delivery. It reports ErrNotFound when neither is stored.
//
// The caller is answering a duplicate, so it needs the run the first
// delivery produced rather than a bare refusal; a redelivery from the
// provider is then answered with that run's id instead of a dead end.
func (s *Store) FindTriggerByWebhookReplay(ctx context.Context, replayKey, delivery string) (*Trigger, error) {
	if replayKey == "" && delivery == "" {
		return nil, ErrNotFound
	}
	var id string
	err := s.queryRow(ctx,
		`SELECT id FROM triggers
		  WHERE (webhook_replay_key != '' AND webhook_replay_key = ?)
		     OR (webhook_delivery != '' AND webhook_delivery = ?)
		  ORDER BY created_at LIMIT 1`,
		replayKey, delivery).Scan(&id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return s.GetTrigger(ctx, id)
}

// FinishTriggerAtGeneration marks a trigger done only while seq is still
// its current claim generation, reporting false when it is not.
//
// This is the fence a superseded dispatch meets. Once a lapsed claim has
// been re-taken by another consumer the trigger's generation moves on,
// and the old dispatch -- which may still be running, and may still be
// about to finish -- must not close out a claim it no longer holds. A
// caller that sees false knows its work was superseded and that the
// current claim owns the outcome.
func (s *Store) FinishTriggerAtGeneration(ctx context.Context, id string, seq int64) (bool, error) {
	res, err := s.exec(ctx,
		`UPDATE triggers SET status = ?, lease_expires_at = NULL
		  WHERE id = ? AND claim_seq = ?`,
		triggerStatusDone, id, seq)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// FinishRunAtGeneration writes a run's terminal status only while seq is
// still its trigger's current claim generation.
//
// It pairs with FinishTriggerAtGeneration for the run row: a superseded
// dispatch must not stamp its own outcome over the run the current claim
// is producing. Reports false without writing when the generation has
// moved on.
func (s *Store) FinishRunAtGeneration(ctx context.Context, runID string, seq int64, status, errMsg string) (bool, error) {
	current, err := s.TriggerClaimGeneration(ctx, runID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return true, s.FinishRun(ctx, runID, status, errMsg)
		}
		return false, err
	}
	if current != seq {
		return false, nil
	}
	return true, s.FinishRun(ctx, runID, status, errMsg)
}

// ListExpiredClaims returns the ids of claimed triggers whose lease has
// lapsed, without changing anything.
//
// It is deliberately a read. ReapExpiredTriggers flips every lapsed
// claim straight back to pending, which is the right move for a cluster
// worker but not for a local consumer: a lapsed lease there can mean a
// dead dispatch or merely a suspended laptop, and the caller has to look
// at the run row -- and at what it is executing itself -- before
// deciding. Separating the observation from the action is what lets that
// judgment happen.
func (s *Store) ListExpiredClaims(ctx context.Context) ([]string, error) {
	rows, err := s.query(ctx,
		`SELECT id FROM triggers
		  WHERE status = ? AND lease_expires_at IS NOT NULL AND lease_expires_at < ?`,
		triggerStatusClaimed, time.Now().UnixNano())
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

// RequeueUnstartedClaim returns a lapsed claim to the pending queue only
// while its run has not started -- no run row, or one still pending.
//
// The run-status guard is what keeps recovery from becoming duplication.
// A run that reached `running` has a process behind it that the lease
// alone cannot see, so requeueing it would put a second copy of live
// work on the queue; that case belongs to the orphan reaper, which
// judges by heartbeat. Both conditions are checked inside one
// transaction so a run that starts concurrently cannot slip between the
// check and the requeue.
func (s *Store) RequeueUnstartedClaim(ctx context.Context, id string) (bool, error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()

	var status string
	err = tx.QueryRowContext(ctx, `SELECT status FROM runs WHERE id = ?`, id).Scan(&status)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		status = runStatusPending
	case err != nil:
		return false, err
	}
	if status != runStatusPending {
		return false, nil
	}

	res, err := tx.ExecContext(ctx,
		`UPDATE triggers
		    SET status = ?, claimed_at = NULL, lease_expires_at = NULL
		  WHERE id = ? AND status = ?`,
		triggerStatusPending, id, triggerStatusClaimed)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if n == 0 {
		return false, nil
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// ReleaseClaimAtGeneration returns a claimed trigger to the pending
// queue, but only while seq is still its current claim generation.
//
// A consumer shutting down mid-dispatch uses it so the interrupted work
// is immediately claimable by the next consumer instead of waiting out
// a lease. The generation guard keeps a shutting-down consumer from
// yanking a claim another consumer has already taken.
func (s *Store) ReleaseClaimAtGeneration(ctx context.Context, id string, seq int64) (bool, error) {
	res, err := s.exec(ctx,
		`UPDATE triggers
		    SET status = ?, claimed_at = NULL, lease_expires_at = NULL
		  WHERE id = ? AND claim_seq = ? AND status = ?`,
		triggerStatusPending, id, seq, triggerStatusClaimed)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// TriggerClaimGeneration returns a trigger's current claim generation,
// so a dispatch can tell whether it still owns the claim it started
// under before it writes anything terminal.
func (s *Store) TriggerClaimGeneration(ctx context.Context, id string) (int64, error) {
	var seq int64
	if err := s.queryRow(ctx,
		`SELECT claim_seq FROM triggers WHERE id = ?`, id).Scan(&seq); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrNotFound
		}
		return 0, err
	}
	return seq, nil
}

// CancelPendingTrigger cancels a submitted run that no consumer has
// claimed yet, in one transaction: the trigger leaves the pending queue
// and its run row becomes terminal. It reports false without touching
// anything when the trigger is not pending -- already claimed, already
// done, or unknown -- which is what makes it safe to try first and fall
// back to the running-run cancellation path.
//
// The status guard in the UPDATE is the whole race defense. A consumer
// claiming the same trigger runs the mirror-image statement, so exactly
// one of the two sees a row affected; a cancel that loses reports false
// and the caller escalates to cancelling the now-running run.
func (s *Store) CancelPendingTrigger(ctx context.Context, id string) (bool, error) {
	now := time.Now()
	tx, err := s.beginTx(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx,
		`UPDATE triggers
		    SET status = ?, cancel_requested_at = COALESCE(cancel_requested_at, ?)
		  WHERE id = ? AND status = ?`,
		triggerStatusDone, now.UnixNano(), id, triggerStatusPending)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if n == 0 {
		return false, nil
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE runs
		    SET status = ?, finished_at = ?, error = ?
		  WHERE id = ? AND status = ?`,
		runStatusCancelled, now.UnixNano(),
		"cancelled before dispatch", id, runStatusPending); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
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
	const maxDepth = 64
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
		if cur != runID {
			out = append(out, pipeline)
		}
		if !parent.Valid || parent.String == "" {
			return out, nil
		}
		cur = parent.String
	}
	return out, nil
}

// ClaimNextTrigger flips the oldest pending trigger to 'claimed'.
// ErrNotFound when empty. lease=0 uses DefaultLeaseDuration.
func (s *Store) ClaimNextTrigger(ctx context.Context, lease time.Duration) (*Trigger, error) {
	return s.ClaimNextTriggerFor(ctx, ClaimIdentity{}, lease, nil, nil)
}

// ClaimNextTriggerFor adds pipeline/source filter sets (AND semantics)
// and records claimant as the token the claim answers to.
func (s *Store) ClaimNextTriggerFor(ctx context.Context, claimant ClaimIdentity, lease time.Duration, pipelines, sources []string) (*Trigger, error) {
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
       repo, repo_url, github_owner, github_repo, repo_inherited, retry_of, retry_source, parent_node_id, "full",
       idempotency_key, claim_seq, webhook_delivery
  FROM triggers
 WHERE status = ?`
	args := []any{triggerStatusPending}
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
	var fullInt, repoInheritedInt int
	err = tx.QueryRowContext(ctx, sel, args...).Scan(
		&t.ID, &t.Pipeline, &argsJSON, &t.TriggerSource, &t.TriggerUser,
		&envJSON, &t.GitBranch, &t.GitSHA, &t.Status, &createdNS, &parent,
		&t.Repo, &t.RepoURL, &t.GithubOwner, &t.GithubRepo, &repoInheritedInt, &t.RetryOf, &t.RetrySource, &t.ParentNodeID, &fullInt,
		&t.IdempotencyKey, &t.ClaimSeq, &t.WebhookDelivery,
	)
	if parent.Valid {
		t.ParentRunID = parent.String
	}
	t.Full = fullInt != 0
	t.RepoInherited = repoInheritedInt != 0
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
		`UPDATE triggers SET status = ?, claimed_at = ?, lease_expires_at = ?, claim_seq = claim_seq + 1,
		        claim_principal = ?, claim_token_prefix = ?
		  WHERE id = ?`,
		triggerStatusClaimed, now.UnixNano(), expires.UnixNano(),
		claimant.Principal, claimant.TokenPrefix, t.ID,
	); err != nil {
		return nil, err
	}
	if err := tx.QueryRowContext(ctx,
		`SELECT claim_seq FROM triggers WHERE id = ?`, t.ID).Scan(&t.ClaimSeq); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	t.Status = triggerStatusClaimed
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
		  WHERE id = ? AND status = ?`,
		expires, id, triggerStatusClaimed)
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

func (s *Store) reapExpiredTriggers(ctx context.Context) ([]string, error) {
	now := time.Now().UnixNano()

	tx, err := s.beginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.QueryContext(ctx,
		`SELECT id FROM triggers
		  WHERE status = ? AND lease_expires_at IS NOT NULL
		    AND lease_expires_at < ?`,
		triggerStatusClaimed, now)
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
		    SET status = ?,
		        claimed_at = NULL,
		        lease_expires_at = NULL
		  WHERE status = ? AND lease_expires_at IS NOT NULL
		    AND lease_expires_at < ?`,
		triggerStatusPending, triggerStatusClaimed, now); err != nil {
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
		`UPDATE triggers SET status = ?, lease_expires_at = NULL WHERE id = ?`,
		triggerStatusDone, id)
	return err
}

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
		WHERE r.status = ?
		  AND r.started_at > 0
		  AND r.started_at < ?
		  AND EXISTS (
		      SELECT 1 FROM triggers t
		       WHERE t.id = r.id AND t.status = ?
		  )
	`, runStatusPending, cutoff, triggerStatusDone)
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
			`UPDATE runs SET status = ?, error = ?, finished_at = ?
			  WHERE id = ? AND status = ?`,
			runStatusFailed, reason, now, id, runStatusPending); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return ids, nil
}

func (s *Store) reapStaleRunningRuns(ctx context.Context, grace time.Duration, reason string) ([]string, error) {
	cutoff := time.Now().Add(-grace).UnixNano()
	now := time.Now().UnixNano()

	rows, err := s.query(ctx, `
SELECT id FROM runs
 WHERE status = ?
   AND last_heartbeat_at IS NOT NULL
   AND last_heartbeat_at < ?`, runStatusRunning, cutoff)
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
		if err := s.cascadeOrphanedNodes(ctx, id, reason, now); err != nil {
			return nil, err
		}
		if err := s.FinishRun(ctx, id, runStatusFailed, reason); err != nil {
			return nil, err
		}
	}
	return ids, nil
}

func (s *Store) cascadeOrphanedNodes(ctx context.Context, runID, errMsg string, nowNS int64) error {
	if _, err := s.exec(ctx, `
UPDATE nodes
   SET `+nodeFailSet+`,
       error          = ?,
       failure_reason = 'orphaned',
       finished_at    = ?
 WHERE run_id = ? AND status = ?`,
		errMsg, nowNS, runID, nodeStatusRunning); err != nil {
		return err
	}
	_, err := s.exec(ctx, `
UPDATE nodes
   SET status         = ?,
       outcome        = 'cancelled',
       error          = 'orphaned: orchestrator process exited before this node ran',
       failure_reason = 'orphaned',
       finished_at    = ?
 WHERE run_id = ? AND status = ?`,
		nodeStatusDone, nowNS, runID, nodeStatusPending)
	return err
}

func (s *Store) reconcileOrphanedLocalRuns(ctx context.Context, threshold time.Duration) (int, error) {
	cutoff := time.Now().Add(-threshold).UnixNano()

	rows, err := s.query(ctx, `
SELECT r.id
  FROM runs r
 WHERE r.status = ?
   AND r.started_at < ?
   AND max(
         COALESCE((SELECT MAX(last_heartbeat) FROM nodes n WHERE n.run_id = r.id), 0),
         COALESCE(r.last_heartbeat_at, 0),
         r.started_at
       ) < ?`,
		runStatusRunning, cutoff, cutoff)
	if err != nil {
		return 0, err
	}
	var orphanIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return 0, err
		}
		orphanIDs = append(orphanIDs, id)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	for _, id := range orphanIDs {
		now := time.Now().UnixNano()
		errMsg := fmt.Sprintf("orphaned: no heartbeat for >%s; orchestrator process is no longer running", threshold)
		if err := s.cascadeOrphanedNodes(ctx, id, errMsg, now); err != nil {
			return 0, err
		}
		if err := s.FinishRun(ctx, id, runStatusFailed, errMsg); err != nil {
			return 0, err
		}
	}

	if _, err := s.exec(ctx, `
UPDATE nodes
   SET status         = ?,
       outcome        = 'cancelled',
       error          = COALESCE(NULLIF(error, ''), 'orphaned: run terminated before this node ran'),
       failure_reason = COALESCE(NULLIF(failure_reason, ''), 'orphaned'),
       finished_at    = ?
 WHERE status = ?
   AND run_id IN (SELECT id FROM runs WHERE `+runTerminalIn+`)`,
		nodeStatusDone, time.Now().UnixNano(), nodeStatusPending); err != nil {
		return len(orphanIDs), err
	}

	return len(orphanIDs), nil
}

// ListPendingTriggersForParent returns every pending trigger whose
// parent_run_id matches parentRunID, oldest first. Used by the
// laptop-local trigger loop to scope its claim queue to the run
// that started it -- without the filter, two parallel local runs
// would steal each other's children. Empty list when no candidates.
func (s *Store) ListPendingTriggersForParent(ctx context.Context, parentRunID string) ([]string, error) {
	rows, err := s.query(ctx, `
SELECT id FROM triggers
 WHERE status = ? AND parent_run_id = ?
 ORDER BY created_at ASC`, triggerStatusPending, parentRunID)
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

// CountPendingTriggers returns how many triggers are waiting to be
// claimed. A resident consumer reads it to decide whether an idle
// window is really idle before it releases its lock and exits.
func (s *Store) CountPendingTriggers(ctx context.Context) (int, error) {
	var n int
	if err := s.queryRow(ctx,
		`SELECT COUNT(*) FROM triggers WHERE status = ?`, triggerStatusPending).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
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
		`UPDATE triggers SET status = ?, claimed_at = ?, lease_expires_at = ?, claim_seq = claim_seq + 1
		  WHERE id = ? AND status = ?`,
		triggerStatusClaimed, now.UnixNano(), expires.UnixNano(), id, triggerStatusPending)
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
	var fullInt, repoInheritedInt int
	if err := tx.QueryRowContext(
		ctx, `
SELECT id, pipeline, args_json, trigger_source, trigger_user,
       trigger_env, git_branch, git_sha, status, created_at, parent_run_id,
       repo, repo_url, github_owner, github_repo, repo_inherited, retry_of, retry_source, parent_node_id, "full",
       idempotency_key, claim_seq, webhook_delivery
  FROM triggers WHERE id = ?`, id,
	).Scan(&t.ID, &t.Pipeline, &argsJSON, &t.TriggerSource, &t.TriggerUser,
		&envJSON, &t.GitBranch, &t.GitSHA, &t.Status, &createdNS, &parent,
		&t.Repo, &t.RepoURL, &t.GithubOwner, &t.GithubRepo, &repoInheritedInt, &t.RetryOf, &t.RetrySource, &t.ParentNodeID, &fullInt,
		&t.IdempotencyKey, &t.ClaimSeq, &t.WebhookDelivery); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	if parent.Valid {
		t.ParentRunID = parent.String
	}
	t.Full = fullInt != 0
	t.RepoInherited = repoInheritedInt != 0
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
	var fullInt, repoInheritedInt int
	err := s.queryRow(
		ctx, `
SELECT id, pipeline, args_json, trigger_source, trigger_user,
       trigger_env, git_branch, git_sha, status, created_at, claimed_at, lease_expires_at,
       repo, repo_url, github_owner, github_repo, repo_inherited, retry_of, retry_source, parent_node_id, parent_run_id, "full",
       idempotency_key, claim_seq, webhook_delivery
  FROM triggers WHERE id = ?`, id,
	).Scan(&t.ID, &t.Pipeline, &argsJSON, &t.TriggerSource, &t.TriggerUser,
		&envJSON, &t.GitBranch, &t.GitSHA, &t.Status, &createdNS, &claimedNS, &leaseNS,
		&t.Repo, &t.RepoURL, &t.GithubOwner, &t.GithubRepo, &repoInheritedInt, &t.RetryOf, &t.RetrySource, &t.ParentNodeID, &parent, &fullInt,
		&t.IdempotencyKey, &t.ClaimSeq, &t.WebhookDelivery)
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
	t.RepoInherited = repoInheritedInt != 0
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
	limit = min(limit, MaxRunListLimit)

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
       repo, repo_url, github_owner, github_repo, repo_inherited, retry_of, retry_source, parent_node_id, "full",
       idempotency_key, claim_seq, webhook_delivery
  FROM triggers` + where + `
 ORDER BY created_at DESC
 LIMIT ?`
	rows, err := s.query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []*Trigger
	for rows.Next() {
		var t Trigger
		var argsJSON, envJSON []byte
		var createdNS int64
		var claimedNS, leaseNS sql.NullInt64
		var parent sql.NullString
		var fullInt, repoInheritedInt int
		if err := rows.Scan(&t.ID, &t.Pipeline, &argsJSON, &t.TriggerSource, &t.TriggerUser,
			&envJSON, &t.GitBranch, &t.GitSHA, &t.Status, &createdNS,
			&claimedNS, &leaseNS, &parent,
			&t.Repo, &t.RepoURL, &t.GithubOwner, &t.GithubRepo, &repoInheritedInt, &t.RetryOf, &t.RetrySource, &t.ParentNodeID, &fullInt,
			&t.IdempotencyKey, &t.ClaimSeq, &t.WebhookDelivery); err != nil {
			return nil, err
		}
		t.Full = fullInt != 0
		t.RepoInherited = repoInheritedInt != 0
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

// ErrLockHeld signals the caller is not the current slot holder. HTTP -> 409.
var ErrLockHeld = errors.New("held by another holder")

// ErrConcurrencySuperseded signals a concurrency holder was superseded.
var ErrConcurrencySuperseded = errors.New("concurrency holder superseded")

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
