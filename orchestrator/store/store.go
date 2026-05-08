// Package store persists pipeline-run state to SQLite.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

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
	// Pre-v0.34 alias retained for backward-compatible deserialization.
	FailureWorkerLeaseExpired = FailureRunnerLeaseExpired
	// FailureLogsAuth: the runner's logs.append calls returned 401/403
	// against the controller's auth surface. The run's structured
	// logs are unrecoverable; better to fail loud than report
	// status=success with no observable output. (IMP-002.)
	FailureLogsAuth = "logs_auth"
)

// RetrySource values for runs.retry_source.
const (
	RetrySourceManual = "manual"
	RetrySourceAuto   = "auto"
)

// Store is the persistent state layer. One instance per process; safe
// for concurrent use by multiple orchestrator goroutines.
type Store struct {
	db *sql.DB
}

// Open initializes a SQLite database at path with WAL + foreign keys.
func Open(path string) (*Store, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=foreign_keys(on)&_pragma=busy_timeout(5000)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// SQLite serializes writes; explicit single-connection avoids
	// "database is locked" under load.
	db.SetMaxOpenConns(1)

	s := &Store{db: db}
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

const schema = `
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
    -- triggers.created_at for trigger-originated runs. IMP-004 added
    -- this so pre-claim "pending" runs have a wall-clock anchor
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
    replay_of_node_id TEXT NOT NULL DEFAULT ''
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

func (s *Store) migrate() error {
	if _, err := s.db.Exec(schema); err != nil {
		return err
	}
	// SQLite lacks ADD COLUMN IF NOT EXISTS; probe table_info and add
	// missing columns to keep laptop dev DBs moving.
	if err := s.ensureColumns("nodes", map[string]string{
		"ready_at":         "INTEGER",
		"claimed_by":       "TEXT",
		"lease_expires_at": "INTEGER",
		"needs_labels":     "BLOB",
		"status_detail":    "TEXT NOT NULL DEFAULT ''",
		"last_heartbeat":   "INTEGER",
		"failure_reason":   "TEXT NOT NULL DEFAULT ''",
		"exit_code":        "INTEGER",
	}); err != nil {
		return err
	}
	if err := s.ensureColumns("runs", map[string]string{
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
		// IMP-004: created_at lets pending (pre-orchestrator) runs
		// carry a real timestamp without lying about started_at.
		"created_at": "INTEGER NOT NULL DEFAULT 0",
		// IMP-016: receipt + cost queryable summary. Full receipt
		// JSON is recomputed on demand from runs+nodes; only these
		// queryable fields persist. cost_settled flips to 1 when
		// IMP-018's cloud-billing reconciliation lands.
		"receipt_sha":   "TEXT NOT NULL DEFAULT ''",
		"cost_cents":    "INTEGER NOT NULL DEFAULT 0",
		"cost_currency": "TEXT NOT NULL DEFAULT 'USD'",
		"cost_settled":  "INTEGER NOT NULL DEFAULT 0",
		// Invocation snapshot: a single BLOB captures the same
		// shape that flows into run_start.attrs (binary_source, cwd,
		// flags, args, reproducer, inputs_hash, hints, etc.) so the
		// dashboard / runs status / runs receipt can show "how was
		// this run started" without scanning the envelope log.
		// Stored as a map blob rather than column-per-field so adding
		// a new flag is a code-only change -- no migration. SQLite
		// JSON1 (json_extract) handles the rare query case where a
		// caller wants to filter by a specific key.
		"invocation_json": "BLOB",
	}); err != nil {
		return err
	}
	if err := s.ensureColumns("triggers", map[string]string{
		"parent_run_id":  "TEXT",
		"repo":           "TEXT NOT NULL DEFAULT ''",
		"repo_url":       "TEXT NOT NULL DEFAULT ''",
		"github_owner":   "TEXT NOT NULL DEFAULT ''",
		"github_repo":    "TEXT NOT NULL DEFAULT ''",
		"retry_of":       "TEXT NOT NULL DEFAULT ''",
		"retry_source":   "TEXT NOT NULL DEFAULT ''",
		"parent_node_id": "TEXT NOT NULL DEFAULT ''",
	}); err != nil {
		return err
	}
	// RUN-015: concurrency_waiters gained a holder_id column after the
	// initial landing so caller identity survives promotion. Old rows
	// default to "" which the promotion path treats as "synthesize
	// runID/nodeID" (the pre-fix behavior).
	if err := s.ensureColumns("concurrency_waiters", map[string]string{
		"holder_id": "TEXT NOT NULL DEFAULT ''",
	}); err != nil {
		return err
	}
	// REG-019: per-entry mask flag for secrets. Existing rows
	// default to masked=1 so behavior matches pre-REG-019 (treat
	// every entry as sensitive). Newer writes can opt in to
	// masked=0 for non-secret config values.
	if err := s.ensureColumns("secrets", map[string]string{
		"masked": "INTEGER NOT NULL DEFAULT 1",
	}); err != nil {
		return err
	}
	return nil
}

// ensureColumns adds any of the named columns missing from the table.
// Types are the literal SQL fragments appended after the column name.
// Returning on the first error is safe because subsequent opens will
// finish the job.
func (s *Store) ensureColumns(table string, cols map[string]string) error {
	rows, err := s.db.Query(fmt.Sprintf(`PRAGMA table_info(%q)`, table))
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
			rows.Close()
			return err
		}
		have[name] = true
	}
	rows.Close()
	for name, typ := range cols {
		if have[name] {
			continue
		}
		stmt := fmt.Sprintf(`ALTER TABLE %q ADD COLUMN %s %s`, table, name, typ)
		if _, err := s.db.Exec(stmt); err != nil {
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
	// time for direct CreateRun callers). IMP-004 added this so
	// "pending" runs have a wall-clock anchor distinct from StartedAt.
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
}

// CreateRun inserts a run row, or upgrades an existing 'pending' row
// (controller-pre-allocated at trigger-intake; IMP-004) to the
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
	_, err := s.db.ExecContext(ctx, `
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
	_, err := s.db.ExecContext(ctx, `
UPDATE runs
   SET status = ?, error = ?, finished_at = ?
 WHERE id = ?`,
		status, errMsg, time.Now().UnixNano(), runID)
	return err
}

// UpdatePlanSnapshot replaces the stored plan JSON for a run.
func (s *Store) UpdatePlanSnapshot(ctx context.Context, runID string, snapshot []byte) error {
	_, err := s.db.ExecContext(ctx, `UPDATE runs SET plan_json = ? WHERE id = ?`, snapshot, runID)
	return err
}

// SetRetriedAs stores the reverse retry pointer on runID. Idempotent.
func (s *Store) SetRetriedAs(ctx context.Context, runID, newID string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE runs SET retried_as = ? WHERE id = ?`, newID, runID)
	return err
}

// GetRun fetches a single run by ID.
func (s *Store) GetRun(ctx context.Context, runID string) (*Run, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, pipeline, status, trigger_source, git_branch, git_sha, args_json, plan_json, error, created_at, started_at, finished_at, parent_run_id, repo, repo_url, github_owner, github_repo, retry_of, retried_as, retry_source, replay_of_run_id, replay_of_node_id, invocation_json
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
SELECT id, pipeline, status, trigger_source, git_branch, git_sha, args_json, plan_json, error, created_at, started_at, finished_at, parent_run_id, repo, repo_url, github_owner, github_repo, retry_of, retried_as, retry_source, replay_of_run_id, replay_of_node_id, invocation_json
  FROM runs` + where + `
 ORDER BY started_at DESC
 LIMIT ?`

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

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
SELECT id, pipeline, status, trigger_source, git_branch, git_sha, args_json, plan_json, error, created_at, started_at, finished_at, parent_run_id, repo, repo_url, github_owner, github_repo, retry_of, retried_as, retry_source, replay_of_run_id, replay_of_node_id, invocation_json
  FROM runs ` + where + `
 ORDER BY started_at DESC
 LIMIT 1`
	return scanRun(s.db.QueryRowContext(ctx, q, args...))
}

// DeleteRun removes the run + its trigger; CASCADE handles children.
func (s *Store) DeleteRun(ctx context.Context, runID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM runs WHERE id = ?`, runID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM triggers WHERE id = ?`, runID); err != nil {
		return err
	}
	return tx.Commit()
}

// PruneRunsOlderThan deletes terminal runs older than cutoff and
// returns their ids so callers can purge log files / cache blobs.
func (s *Store) PruneRunsOlderThan(ctx context.Context, cutoff time.Time) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
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
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
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
	var argsJSON, planJSON, invocationJSON []byte
	var createdNS, startedNS int64
	var finishedNS sql.NullInt64
	var parent sql.NullString
	err := rs.Scan(&r.ID, &r.Pipeline, &r.Status, &r.TriggerSource,
		&r.GitBranch, &r.GitSHA, &argsJSON, &planJSON, &r.Error,
		&createdNS, &startedNS, &finishedNS, &parent,
		&r.Repo, &r.RepoURL, &r.GithubOwner, &r.GithubRepo,
		&r.RetryOf, &r.RetriedAs, &r.RetrySource,
		&r.ReplayOfRunID, &r.ReplayOfNodeID, &invocationJSON)
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
	if parent.Valid {
		r.ParentRunID = parent.String
	}
	if len(argsJSON) > 0 {
		_ = json.Unmarshal(argsJSON, &r.Args)
	}
	if len(invocationJSON) > 0 {
		_ = json.Unmarshal(invocationJSON, &r.Invocation)
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
}

// CreateNode inserts a node in the "pending" state.
func (s *Store) CreateNode(ctx context.Context, n Node) error {
	depsJSON, _ := json.Marshal(n.Deps)
	var labelsJSON []byte
	if len(n.NeedsLabels) > 0 {
		labelsJSON, _ = json.Marshal(n.NeedsLabels)
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO nodes (run_id, node_id, status, deps_json, needs_labels)
VALUES (?,?,?,?,?)`, n.RunID, n.NodeID, n.Status, depsJSON, labelsJSON)
	return err
}

// StartNode marks a node as running.
func (s *Store) StartNode(ctx context.Context, runID, nodeID string) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE nodes SET status = 'running', started_at = ? WHERE run_id = ? AND node_id = ?`,
		time.Now().UnixNano(), runID, nodeID)
	return err
}

// SetNodeStatus updates only the status column.
func (s *Store) SetNodeStatus(ctx context.Context, runID, nodeID, status string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE nodes SET status = ? WHERE run_id = ? AND node_id = ?`,
		status, runID, nodeID)
	return err
}

// UpdateNodeDeps rewrites a node's stored dependency list.
func (s *Store) UpdateNodeDeps(ctx context.Context, runID, nodeID string, deps []string) error {
	depsJSON, _ := json.Marshal(deps)
	_, err := s.db.ExecContext(ctx,
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
	_, err := s.db.ExecContext(ctx, `
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
	rows, err := s.db.QueryContext(ctx, `
SELECT run_id, node_id, status, outcome, deps_json, error, output_json, started_at, finished_at,
       ready_at, claimed_by, lease_expires_at, needs_labels, status_detail, last_heartbeat,
       failure_reason, exit_code
  FROM nodes
 WHERE run_id = ?
 ORDER BY rowid`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
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
	row := s.db.QueryRowContext(ctx, `
SELECT run_id, node_id, status, outcome, deps_json, error, output_json, started_at, finished_at,
       ready_at, claimed_by, lease_expires_at, needs_labels, status_detail, last_heartbeat,
       failure_reason, exit_code
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
	var depsJSON, outputJSON, labelsJSON []byte
	var startedNS, finishedNS, readyNS, leaseNS, heartbeatNS sql.NullInt64
	var claimedBy sql.NullString
	var exitCode sql.NullInt64
	err := rs.Scan(&n.RunID, &n.NodeID, &n.Status, &n.Outcome,
		&depsJSON, &n.Error, &outputJSON, &startedNS, &finishedNS,
		&readyNS, &claimedBy, &leaseNS, &labelsJSON,
		&n.StatusDetail, &heartbeatNS,
		&n.FailureReason, &exitCode)
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
	return nil
}

// MarkNodeReady stamps ready_at if unset. Idempotent.
func (s *Store) MarkNodeReady(ctx context.Context, runID, nodeID string) error {
	res, err := s.db.ExecContext(ctx,
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
	res, err := s.db.ExecContext(ctx,
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
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return nil, err
		}
		n := &Node{}
		err = scanNodeRow(tx.QueryRowContext(ctx, `
SELECT run_id, node_id, status, outcome, deps_json, error, output_json, started_at, finished_at,
       ready_at, claimed_by, lease_expires_at, needs_labels, status_detail, last_heartbeat,
       failure_reason, exit_code
  FROM nodes
 WHERE ready_at IS NOT NULL AND claimed_by IS NULL AND status != 'done'
 ORDER BY ready_at ASC
 LIMIT 1`), n)
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
			if _, err := tx.ExecContext(ctx,
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
		if _, err := tx.ExecContext(ctx,
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

// labelsSatisfied reports whether every label in needed appears in
// have. Empty or nil needed matches any have (including empty).
func labelsSatisfied(needed []string, have map[string]struct{}) bool {
	for _, l := range needed {
		if l == "" {
			continue
		}
		if _, ok := have[l]; !ok {
			return false
		}
	}
	return true
}

// UpdateNodeActivity sets status_detail and bumps last_heartbeat.
func (s *Store) UpdateNodeActivity(ctx context.Context, runID, nodeID, detail string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE nodes SET status_detail = ?, last_heartbeat = ?
		  WHERE run_id = ? AND node_id = ?`,
		detail, time.Now().UnixNano(), runID, nodeID)
	return err
}

// TouchNodeHeartbeat stamps last_heartbeat=now.
func (s *Store) TouchNodeHeartbeat(ctx context.Context, runID, nodeID string) error {
	_, err := s.db.ExecContext(ctx,
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
	res, err := s.db.ExecContext(ctx,
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
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx,
		`SELECT run_id, node_id FROM nodes
		  WHERE claimed_by IS NOT NULL AND lease_expires_at IS NOT NULL
		    AND lease_expires_at < ? AND status != 'done'`,
		now)
	if err != nil {
		return nil, err
	}
	var pairs [][2]string
	for rows.Next() {
		var rid, nid string
		if err := rows.Scan(&rid, &nid); err != nil {
			rows.Close()
			return nil, err
		}
		pairs = append(pairs, [2]string{rid, nid})
	}
	rows.Close()
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

// FailExpiredNodeClaims terminates expired claims with FailureAgentLost.
func (s *Store) FailExpiredNodeClaims(ctx context.Context) ([][2]string, error) {
	now := time.Now().UnixNano()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx,
		`SELECT run_id, node_id FROM nodes
		  WHERE claimed_by IS NOT NULL AND lease_expires_at IS NOT NULL
		    AND lease_expires_at < ? AND status != 'done'`,
		now)
	if err != nil {
		return nil, err
	}
	var pairs [][2]string
	for rows.Next() {
		var rid, nid string
		if err := rows.Scan(&rid, &nid); err != nil {
			rows.Close()
			return nil, err
		}
		pairs = append(pairs, [2]string{rid, nid})
	}
	rows.Close()
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

// FailNodesInRun marks every non-terminal node in runID as failed.
// Used by the reaper to avoid zombie nodes when a worker lease expires.
func (s *Store) FailNodesInRun(ctx context.Context, runID, errMsg, failureReason string) ([]string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx,
		`SELECT node_id FROM nodes WHERE run_id = ? AND status != 'done'`, runID)
	if err != nil {
		return nil, err
	}
	var nodeIDs []string
	for rows.Next() {
		var nid string
		if err := rows.Scan(&nid); err != nil {
			rows.Close()
			return nil, err
		}
		nodeIDs = append(nodeIDs, nid)
	}
	rows.Close()
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

// FailStaleQueuedNodes terminates unclaimed nodes whose ready_at is
// older than olderThan with FailureQueueTimeout.
func (s *Store) FailStaleQueuedNodes(ctx context.Context, olderThan time.Duration) ([][2]string, error) {
	if olderThan <= 0 {
		return nil, nil
	}
	threshold := time.Now().Add(-olderThan).UnixNano()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

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
			rows.Close()
			return nil, err
		}
		pairs = append(pairs, [2]string{rid, nid})
	}
	rows.Close()
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
	rows, err := s.db.QueryContext(ctx, `
SELECT run_id, seq, node_id, kind, ts, payload
  FROM events
 WHERE run_id = ? AND seq > ?
 ORDER BY seq ASC
 LIMIT ?`, runID, afterSeq, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
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
			e.Payload = json.RawMessage(payload)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// AppendEvent writes an ordered event; returns the assigned seq.
func (s *Store) AppendEvent(ctx context.Context, runID, nodeID, kind string, payload []byte) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

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

// --- Debug pauses (REG-013) ---

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
	_, err := s.db.ExecContext(ctx, `
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
	row := s.db.QueryRowContext(ctx, `
SELECT run_id, node_id, reason, paused_at, expires_at, released_at, released_by, release_kind
  FROM debug_pauses
 WHERE run_id = ? AND node_id = ? AND released_at IS NULL
 ORDER BY paused_at DESC
 LIMIT 1`, runID, nodeID)
	return scanDebugPause(row)
}

// ListDebugPauses returns all pause rows for a run, newest first.
func (s *Store) ListDebugPauses(ctx context.Context, runID string) ([]*DebugPause, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT run_id, node_id, reason, paused_at, expires_at, released_at, released_by, release_kind
  FROM debug_pauses
 WHERE run_id = ?
 ORDER BY paused_at DESC`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
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
	res, err := s.db.ExecContext(ctx, `
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
	_, err := s.db.ExecContext(ctx, `
INSERT INTO triggers (id, pipeline, args_json, trigger_source, trigger_user,
                      trigger_env, git_branch, git_sha, status, created_at, parent_run_id,
                      repo, repo_url, github_owner, github_repo, retry_of, retry_source, parent_node_id)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		t.ID, t.Pipeline, argsJSON, t.TriggerSource, t.TriggerUser,
		envJSON, t.GitBranch, t.GitSHA, status, t.CreatedAt.UnixNano(), parent,
		t.Repo, t.RepoURL, t.GithubOwner, t.GithubRepo, t.RetryOf, t.RetrySource, t.ParentNodeID,
	)
	return err
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
		err := s.db.QueryRowContext(ctx,
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
func (s *Store) ClaimNextTriggerFor(ctx context.Context, lease time.Duration, pipelines []string, sources []string) (*Trigger, error) {
	if lease <= 0 {
		lease = DefaultLeaseDuration
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	sel := `
SELECT id, pipeline, args_json, trigger_source, trigger_user,
       trigger_env, git_branch, git_sha, status, created_at, parent_run_id,
       repo, repo_url, github_owner, github_repo, retry_of, retry_source, parent_node_id
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
 LIMIT 1`

	var t Trigger
	var argsJSON, envJSON []byte
	var createdNS int64
	var parent sql.NullString
	err = tx.QueryRowContext(ctx, sel, args...).Scan(
		&t.ID, &t.Pipeline, &argsJSON, &t.TriggerSource, &t.TriggerUser,
		&envJSON, &t.GitBranch, &t.GitSHA, &t.Status, &createdNS, &parent,
		&t.Repo, &t.RepoURL, &t.GithubOwner, &t.GithubRepo, &t.RetryOf, &t.RetrySource, &t.ParentNodeID)
	if parent.Valid {
		t.ParentRunID = parent.String
	}
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}

	now := time.Now()
	expires := now.Add(lease)
	if _, err := tx.ExecContext(ctx,
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

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

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
	if err := tx.QueryRowContext(ctx,
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
	res, err := s.db.ExecContext(ctx,
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

// ReapExpiredTriggers flips lease-expired claimed triggers back to
// pending. Returns reaped IDs; matching runs are caller-reconciled.
func (s *Store) ReapExpiredTriggers(ctx context.Context) ([]string, error) {
	now := time.Now().UnixNano()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

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
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
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
	_, err := s.db.ExecContext(ctx,
		`UPDATE triggers SET status = 'done', lease_expires_at = NULL WHERE id = ?`, id)
	return err
}

// ListPendingTriggersForParent returns every pending trigger whose
// parent_run_id matches parentRunID, oldest first. Used by the
// laptop-local trigger loop to scope its claim queue to the run
// that started it -- without the filter, two parallel local runs
// would steal each other's children. Empty list when no candidates.
func (s *Store) ListPendingTriggersForParent(ctx context.Context, parentRunID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id FROM triggers
 WHERE status = 'pending' AND parent_run_id = ?
 ORDER BY created_at ASC`, parentRunID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
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
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

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
	if err := tx.QueryRowContext(ctx, `
SELECT id, pipeline, args_json, trigger_source, trigger_user,
       trigger_env, git_branch, git_sha, status, created_at, parent_run_id,
       repo, repo_url, github_owner, github_repo, retry_of, retry_source, parent_node_id
  FROM triggers WHERE id = ?`, id,
	).Scan(&t.ID, &t.Pipeline, &argsJSON, &t.TriggerSource, &t.TriggerUser,
		&envJSON, &t.GitBranch, &t.GitSHA, &t.Status, &createdNS, &parent,
		&t.Repo, &t.RepoURL, &t.GithubOwner, &t.GithubRepo, &t.RetryOf, &t.RetrySource, &t.ParentNodeID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	if parent.Valid {
		t.ParentRunID = parent.String
	}
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
	err := s.db.QueryRowContext(ctx, `
SELECT id, pipeline, args_json, trigger_source, trigger_user,
       trigger_env, git_branch, git_sha, status, created_at, claimed_at, lease_expires_at,
       repo, repo_url, github_owner, github_repo, retry_of, retry_source, parent_node_id, parent_run_id
  FROM triggers WHERE id = ?`, id,
	).Scan(&t.ID, &t.Pipeline, &argsJSON, &t.TriggerSource, &t.TriggerUser,
		&envJSON, &t.GitBranch, &t.GitSHA, &t.Status, &createdNS, &claimedNS, &leaseNS,
		&t.Repo, &t.RepoURL, &t.GithubOwner, &t.GithubRepo, &t.RetryOf, &t.RetrySource, &t.ParentNodeID, &parent)
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
	err := s.db.QueryRowContext(ctx, `
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
       repo, repo_url, github_owner, github_repo, retry_of, retry_source, parent_node_id
  FROM triggers` + where + `
 ORDER BY created_at DESC
 LIMIT ?`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// Repo filter is client-side; trigger_env is an unindexed JSON blob.
	var out []*Trigger
	for rows.Next() {
		var t Trigger
		var argsJSON, envJSON []byte
		var createdNS int64
		var claimedNS, leaseNS sql.NullInt64
		var parent sql.NullString
		if err := rows.Scan(&t.ID, &t.Pipeline, &argsJSON, &t.TriggerSource, &t.TriggerUser,
			&envJSON, &t.GitBranch, &t.GitSHA, &t.Status, &createdNS,
			&claimedNS, &leaseNS, &parent,
			&t.Repo, &t.RepoURL, &t.GithubOwner, &t.GithubRepo, &t.RetryOf, &t.RetrySource, &t.ParentNodeID); err != nil {
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

// ErrLockHeld: caller is not the current slot holder. HTTP -> 409.
var ErrLockHeld = errors.New("held by another holder")

// CountPendingNodes returns the count of unclaimed ready nodes.
func (s *Store) CountPendingNodes(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM nodes
		  WHERE ready_at IS NOT NULL AND (claimed_by IS NULL OR claimed_by = '')`,
	).Scan(&n)
	return n, err
}

// CountActiveRunners counts distinct claimed_by within `window`.
func (s *Store) CountActiveRunners(ctx context.Context, window time.Duration) (int, error) {
	threshold := time.Now().Add(-window).UnixNano()
	var n int
	err := s.db.QueryRowContext(ctx,
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
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
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
	row := s.db.QueryRowContext(ctx, `
SELECT run_id, node_id, requested_at, message, timeout_ms, on_timeout,
       approver, resolved_at, resolution, comment
  FROM approvals WHERE run_id = ? AND node_id = ?`, runID, nodeID)
	return scanApproval(row)
}

// ResolveApproval stamps resolution on a pending row.
// ErrNotFound when missing; ErrLockHeld when already resolved.
func (s *Store) ResolveApproval(ctx context.Context, runID, nodeID, resolution, approver, comment string) (*Approval, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var pkRun, pkNode string
	var resolvedNS sql.NullInt64
	err = tx.QueryRowContext(ctx,
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
	rows, err := s.db.QueryContext(ctx, `
SELECT run_id, node_id, requested_at, message, timeout_ms, on_timeout,
       approver, resolved_at, resolution, comment
  FROM approvals WHERE run_id = ?
 ORDER BY requested_at ASC`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
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
	rows, err := s.db.QueryContext(ctx, `
SELECT run_id, node_id, requested_at, message, timeout_ms, on_timeout,
       approver, resolved_at, resolution, comment
  FROM approvals WHERE resolved_at IS NULL
 ORDER BY requested_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
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
