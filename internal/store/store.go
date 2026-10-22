// Package store persists pipeline-run state to a SQL database. SQLite
// (modernc.org/sqlite) and Postgres (jackc/pgx via the stdlib driver)
// are both supported behind the same *Store type; the dialect is
// chosen at Open time.
package store

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// DefaultBusyTimeoutMS is the SQLite busy_timeout Open applies, in
// milliseconds. See Open for why 30s.
const DefaultBusyTimeoutMS = 30000

var busyTimeoutMS = DefaultBusyTimeoutMS

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
	// FailureCreditsExhausted: the team's prepaid balance or a hold refused
	// the node's next slice, and the node stopped within its drain.
	FailureCreditsExhausted = "credits_exhausted"
	// FailureUnpricedCPUClass: the node's cpu request is above the largest
	// class the credit rate table prices, so no claim could be billed.
	FailureUnpricedCPUClass = "unpriced_cpu_class"
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
	// prefer the lossy behavior set logs.drop_policy: warn in config.yaml.
	FailureLogsDropped = "logs_dropped"
	// FailureSourceUnavailable: a launcher Job's init container was refused
	// the run's source credential, which answers the same on every attempt,
	// so the node is not retried.
	FailureSourceUnavailable = "source_unavailable"
	// FailureSourceFetch: a launcher Job's init container could not check
	// out the run's source for a reason a later attempt may not meet. The
	// node is retried within its budget, a planning node included.
	FailureSourceFetch = "source_fetch"
)

// RetrySourceManual identifies an explicitly retried run.
const RetrySourceManual = "manual"

// Store is the persistent state layer. One instance per process; safe
// for concurrent use by multiple orchestrator goroutines. The
// underlying database is SQLite or Postgres depending on which
// constructor opened it; dialect-aware methods branch on s.dialect.
type Store struct {
	creditMeteringOff  atomic.Bool
	controllerEpoch    int64
	credentialEpoch    string
	controllerFailStop func(error)

	runEnvCipher     SessionKeyCipher
	localSecrets     sync.Map
	resultRecords    nodeResultRecords
	materializations materializationRecords
	claimProfiles    ClaimProfileResolver
	intakePolicy     IntakePolicy
	planSecretOpener PlanSecretOpener
	delegation       delegationContracts
	consolePolicy    consolePolicy
	clock            func() time.Time
	pinnedClock      func() time.Time // safety: PostgreSQL time stays authoritative outside clock-pinned fixtures.
	db               *sql.DB
	conn             *sql.Conn // Break-glass verification and writes share this session.
	githubLocks      *sql.DB
	dialect          Dialect
	cleanup          func() error
	csrfKeyMu        sync.Mutex
	csrfKey          []byte
	csrfCipher       SessionKeyCipher
	githubCheckMu    sync.Mutex
}

// Dialect reports the SQL dialect this Store was opened against.
// Useful for callers (tests, diagnostics) that need to know which
// backend they're talking to; query methods on Store handle the
// dialect difference internally.
func (s *Store) Dialect() Dialect { return s.dialect }

// Open initializes a SQLite database at path with WAL + foreign keys.
//
// busy_timeout is high (30s) so
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
// of admission and most state mutations) can both read the
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
func Open(path string, opts ...Option) (*Store, error) {
	_, statErr := os.Lstat(path)
	newDatabase := errors.Is(statErr, os.ErrNotExist)
	if statErr != nil && !newDatabase {
		return nil, fmt.Errorf("inspect sqlite database %s: %w", path, statErr)
	}
	if newDatabase {
		if err := preparePrivateSQLite(path); err != nil {
			return nil, err
		}
	} else if err := tightenExistingSQLite(path); err != nil {
		return nil, err
	}
	st, err := openSQL(context.Background(), "sqlite", sqliteDSN(path), DialectSQLite, "delete "+path, opts)
	if err != nil {
		return nil, err
	}
	return st, nil
}

// safety: SQLite copies the seed's private mode on Unix and coordinates
// its own descriptor closes with locks held by other database connections.
func preparePrivateSQLite(path string) (err error) {
	f, err := os.CreateTemp(filepath.Dir(path), ".sqlite-create-*")
	if err != nil {
		return fmt.Errorf("prepare sqlite database %s: %w", path, err)
	}
	temporary := f.Name()
	defer func() {
		if removeErr := os.Remove(temporary); removeErr != nil {
			err = errors.Join(err, fmt.Errorf("remove sqlite creation file: %w", removeErr))
		}
	}()
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return fmt.Errorf("secure sqlite database %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close sqlite creation file %s: %w", path, err)
	}
	db, err := sql.Open("sqlite", sqliteURI(path)+"?modeof="+strings.ReplaceAll(url.QueryEscape(temporary), "+", "%20"))
	if err != nil {
		return fmt.Errorf("create sqlite database %s: %w", path, err)
	}
	connection, err := db.Conn(context.Background())
	if err != nil {
		return errors.Join(fmt.Errorf("create sqlite database %s: %w", path, err), db.Close())
	}
	if err := errors.Join(connection.Close(), db.Close()); err != nil {
		return fmt.Errorf("create sqlite database %s: %w", path, err)
	}
	return tightenExistingSQLite(path)
}

// safety: SQLite creates the -wal and -shm sidecars with the database's own
// mode, and a database or sidecar written before the store created files
// 0600 keeps whatever mode it had, so every one that exists is narrowed to
// its owner before the store reads run data from it.
func tightenExistingSQLite(path string) error {
	for _, name := range []string{path, path + "-wal", path + "-shm", path + "-journal"} {
		if err := tightenSQLiteFile(name); err != nil {
			return err
		}
	}
	return nil
}

// safety: closing a raw descriptor releases this process's SQLite locks,
// including locks held by other connections to the same database.
func tightenSQLiteFile(name string) error {
	info, err := os.Stat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect sqlite file %s: %w", name, err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 == 0 {
		return nil
	}
	if err := os.Chmod(name, info.Mode().Perm()&0o700); err != nil {
		return fmt.Errorf("secure sqlite file %s: %w", name, err)
	}
	return nil
}

func sqliteDSN(path string) string {
	return fmt.Sprintf("%s?_txlock=immediate&_pragma=busy_timeout(%d)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(on)", sqliteURI(path), busyTimeoutMS)
}

// safety: SQLite ends a file: URI path at the first unescaped `?` or `#` and
// percent-decodes what precedes it, so an unescaped path carrying one of those
// or a literal `%` names a different file than the caller stat'd and hardened,
// and swallows the query string that carries every connection pragma.
func sqliteURI(path string) string {
	escaped := (&url.URL{Path: path}).EscapedPath()
	if strings.HasPrefix(escaped, "//") {
		// safety: an explicit empty authority keeps a leading `//` in the
		// path from being parsed as a host name.
		return "file://" + escaped
	}
	return "file:" + escaped
}

// OpenPostgres initializes a Store against the Postgres database
// identified by dsn (`postgres://user:pass@host:port/db?sslmode=...`).
// Migrations run on first connect; concurrent OpenPostgres calls
// against the same fresh database are coordinated by a transactional
// advisory lock so exactly one runner applies the schema.
func OpenPostgres(ctx context.Context, dsn string, opts ...Option) (*Store, error) {
	if dsn == "" {
		return nil, errors.New("OpenPostgres: dsn is required")
	}
	cfg, err := postgresConfig(dsn)
	if err != nil {
		return nil, err
	}
	return openPostgresConfig(ctx, cfg, opts...)
}

func openPostgresConfig(ctx context.Context, cfg *pgx.ConnConfig, opts ...Option) (*Store, error) {
	dsn := stdlib.RegisterConnConfig(cfg)
	st, err := openSQL(ctx, "pgx", dsn, DialectPostgres, "drop the database", opts)
	if err != nil {
		stdlib.UnregisterConnConfig(dsn)
		return nil, err
	}
	st.cleanup = func() error { stdlib.UnregisterConnConfig(dsn); return nil }
	return st, nil
}

func openSQL(ctx context.Context, driver, dsn string, dialect Dialect, remedy string, opts []Option) (*Store, error) {
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
	for _, opt := range opts {
		if opt.apply != nil {
			opt.apply(s)
		}
	}
	if err := s.migrate(ctx, remedy); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	if dialect == DialectPostgres {
		s.githubLocks, err = sql.Open(driver, dsn)
		if err != nil {
			return nil, errors.Join(err, db.Close())
		}
		s.githubLocks.SetMaxOpenConns(2)
		s.githubLocks.SetMaxIdleConns(2)
		s.githubLocks.SetConnMaxIdleTime(5 * time.Minute)
	}
	for _, opt := range opts {
		if opt.policies != nil {
			policies, err := opt.policies(s)
			if err != nil {
				return nil, errors.Join(err, s.Close())
			}
			s.configurePolicies(policies)
		}
	}
	return s, nil
}

// Close releases the underlying database handles.
func (s *Store) Close() error {
	s.localSecrets.Clear()
	var dbErr error
	if s.conn != nil {
		dbErr = s.conn.Close()
	}
	dbErr = errors.Join(dbErr, s.db.Close())
	if s.githubLocks != nil {
		dbErr = errors.Join(dbErr, s.githubLocks.Close())
	}
	if s.cleanup == nil {
		return dbErr
	}
	cleanup := s.cleanup
	s.cleanup = nil
	return errors.Join(dbErr, cleanup())
}

// Ping reports whether the database answers.
func (o *Operator) Ping(ctx context.Context) error { return o.s.db.PingContext(ctx) }

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
	if isPGConflict(err) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "database is locked") ||
		strings.Contains(msg, "sqlite_busy") ||
		strings.Contains(msg, "database table is locked")
}

func isPGConflict(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && (pgErr.Code == "40P01" || pgErr.Code == "40001")
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

// InvocationSecretArgsKey labels argument classification in the reporting view.
const InvocationSecretArgsKey = "secret_args"

// Run is the redacted reporting view of a persisted run.
type Run struct {
	team            Team
	RepoInherited   bool              `json:"repo_inherited,omitempty"`
	ID              string            `json:"id"`
	Number          int64             `json:"number"`
	Pipeline        string            `json:"pipeline"`
	Status          string            `json:"status"`
	TriggerSource   string            `json:"trigger_source,omitempty"`
	GitBranch       string            `json:"git_branch,omitempty"`
	GitSHA          string            `json:"git_sha,omitempty"`
	Args            map[string]string `json:"args,omitempty"`
	PlanSnapshot    []byte            `json:"-"`
	Error           string            `json:"error,omitempty"`
	CreatedAt       time.Time         `json:"created_at,omitempty"`
	StartedAt       time.Time         `json:"started_at"`
	FinishedAt      *time.Time        `json:"finished_at,omitempty"`
	ParentRunID     string            `json:"parent_run_id,omitempty"`
	Admission       string            `json:"admission,omitempty"`
	DeclaredRepo    string            `json:"repo,omitempty"`
	RepoURL         string            `json:"repo_url,omitempty"`
	GithubOwner     string            `json:"github_owner,omitempty"`
	GithubRepo      string            `json:"github_repo,omitempty"`
	RetryOf         string            `json:"retry_of,omitempty"`
	RetriedAs       string            `json:"retried_as,omitempty"`
	RetrySource     string            `json:"retry_source,omitempty"`
	ReplayOfRunID   string            `json:"replay_of_run_id,omitempty"`
	ReplayOfNodeID  string            `json:"replay_of_node_id,omitempty"`
	Invocation      map[string]any    `json:"invocation,omitempty"`
	AnnotationCount int               `json:"annotation_count,omitempty"`
	TopAnnotation   string            `json:"top_annotation,omitempty"`
	Annotations     []string          `json:"annotations,omitempty"`
	NeedsAttention  string            `json:"needs_attention,omitempty"`
	TriggerUser     string            `json:"trigger_user,omitempty"`
	PullRequest     *RunPullRequest   `json:"pull_request,omitempty"`
}

// RunPullRequest is the pull request a run's trigger fired for.
type RunPullRequest struct {
	Number  int    `json:"number"`
	Action  string `json:"action,omitempty"`
	BaseRef string `json:"base_ref,omitempty"`
	BaseSHA string `json:"base_sha,omitempty"`
	HeadRef string `json:"head_ref,omitempty"`
	HeadSHA string `json:"head_sha,omitempty"`
}

// ErrIDOwnedByAnotherTeam reports a write whose key already names a row of
// a different team. It is not [ErrNotFound] because the caller supplied the
// id and the id is taken, and it is not success because nothing was written.
var ErrIDOwnedByAnotherTeam = errors.New("store: id already belongs to another team")

// safety: takes no team because the question is who owns the id, and an
// answer scoped to the asker is no answer.
func (op *Operator) runOwnerTx(ctx context.Context, tx *storeTx, runID string) (Team, bool, error) {
	var owner string
	err := tx.QueryRowContext(ctx, `SELECT team FROM runs WHERE id = ?`, runID).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return Team(owner), true, nil
}

const runColumns = `runs.team,runs.id,runs.number,runs.pipeline,runs.status,runs.trigger_source,runs.git_branch,runs.git_sha,
 runs.request_json,runs.plan_json,runs.error,runs.created_at,runs.started_at,runs.finished_at,
 runs.parent_run_id,runs.repo_display,runs.retry_of,
 COALESCE((SELECT retry.id FROM runs retry WHERE retry.team=runs.team AND retry.retry_of=runs.id ORDER BY retry.created_at DESC,retry.id DESC LIMIT 1),''),
 runs.lineage_json,runs.replay_of_run_id,runs.replay_of_node_id,runs.annotation_count,runs.top_annotation,runs.annotations_json,runs.admission,` + runAttentionColumn

// RunFilter narrows ListRuns results; zero value matches everything.
type RunFilter struct {
	Pipelines      []string
	Statuses       []string
	GitSHAPrefixes []string
	GitBranches    []string
	DeclaredRepos  []string
	RepoURLs       []string
	Since          time.Time
	Limit          int // <=0 = default
	ParentRunID    string
	RootOnly       bool

	// The Exclude lists drop a run matching any of their values.
	ExcludePipelines      []string
	ExcludeStatuses       []string
	ExcludeGitSHAPrefixes []string
	ExcludeGitBranches    []string
	TriggerSources        []string
	ExcludeTriggerSources []string
	// RepoNames match the name the dashboard labels a run with: the last path segment of
	// its declared repository, else of its GitHub repository, else "unknown".
	RepoNames        []string
	ExcludeRepoNames []string
	// StartedBefore, FinishedAfter and FinishedBefore are inclusive bounds; a finished
	// bound excludes every unfinished run.
	StartedBefore  time.Time
	FinishedAfter  time.Time
	FinishedBefore time.Time
	// Text keeps runs containing every term, and ExcludeText drops runs containing any,
	// in the id, pipeline, repository, branch, SHA, error, trigger source or status. Only
	// ASCII letters match regardless of case, the same on every backend.
	Text        []string
	ExcludeText []string

	// AfterStartedAt and AfterID resume a listing after one already-seen run. The id
	// breaks ties among runs sharing an instant, which an instant alone would skip.
	AfterStartedAt int64
	AfterID        string
	// BeforeStartedAt and BeforeID page the other way: the Limit runs just newer than
	// one already-seen run, still returned newest first.
	BeforeStartedAt int64
	BeforeID        string

	// ProbeMayBeClamped marks a limit this process chose rather than one a caller asked
	// for. A server clamping the probe row away then leaves the result reported as cut
	// rather than refused, because refusing would name a limit the caller cannot lower.
	ProbeMayBeClamped bool
}

// HasCursor reports whether the filter resumes after an already-seen run. It keys on
// the id alone, because a run started at the Unix epoch carries an instant of zero.
func (f RunFilter) HasCursor() bool { return f.AfterID != "" }

// HasBeforeCursor reports whether the filter pages toward newer runs.
func (f RunFilter) HasBeforeCursor() bool { return f.BeforeID != "" }

// ListRuns returns runs ordered newest-first, then by id descending, filtered
// by f. The cursor clause depends on that order.
//
// Scope: this reads every team. Tenant callers use [Tenant.ListRuns].
func (o *Operator) ListRuns(ctx context.Context, f RunFilter) (_ []*Run, err error) {
	where, args, err := runFilterWhere(o.s.dialect, f)
	if err != nil {
		return nil, err
	}
	return o.s.listRuns(ctx, `SELECT `+runColumns+` FROM runs`+where, args, f)
}

func (s *Store) listRuns(ctx context.Context, query string, args []any, f RunFilter) (_ []*Run, err error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	// safety: clamp again here, because a non-HTTP caller never passes ParseRunFilter's clamp.
	limit = min(limit, maxRunListFetch)
	args = append(args, limit)

	order := "COALESCE(started_at,0) DESC, id DESC"
	if f.HasBeforeCursor() {
		order = "COALESCE(started_at,0) ASC, id ASC"
	}
	query += ` ORDER BY ` + order + ` LIMIT ?`

	rows, err := s.query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()

	var out []*Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	if err := s.refreshRunPresentations(ctx, out); err != nil {
		return nil, err
	}
	if f.HasBeforeCursor() {
		slices.Reverse(out)
	}
	return out, nil
}

// safety: the scanner maps every started_at at or below zero to the zero time, so a
// cursor on a never-started run addresses that whole group and orders it by id. Exact
// while those rows carry one stored value, which is what a single writer produces; a
// group holding more would order by the raw value first and this would skip part of it.
func runCursorClause(afterStartedAt int64) string {
	if afterStartedAt <= 0 {
		return "(COALESCE(started_at,0) <= 0 AND id < ?)"
	}
	// hack: the expanded form rather than a row-value comparison, so the predicate
	// holds on every SQLite build this driver may sit on.
	return "(COALESCE(started_at,0) < ? OR (started_at = ? AND id < ?))"
}

func runCursorArgs(f RunFilter) []any {
	if f.AfterStartedAt <= 0 {
		return []any{f.AfterID}
	}
	return []any{f.AfterStartedAt, f.AfterStartedAt, f.AfterID}
}

func runFilterWhere(dialect Dialect, f RunFilter) (string, []any, error) {
	var err error
	if f.GitSHAPrefixes, err = normalizeSHAPrefixes(f.GitSHAPrefixes); err != nil {
		return "", nil, err
	}
	if f.ExcludeGitSHAPrefixes, err = normalizeSHAPrefixes(f.ExcludeGitSHAPrefixes); err != nil {
		return "", nil, err
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
	addIn("git_branch", f.GitBranches)
	addIn("repo_display", f.DeclaredRepos)
	addIn("repo_display", f.RepoURLs)
	addClause := func(clause string, values ...any) {
		if where == "" {
			where = " WHERE " + clause
		} else {
			where += " AND " + clause
		}
		args = append(args, values...)
	}
	if len(f.GitSHAPrefixes) > 0 {
		clause, values := shaPrefixClause(f.GitSHAPrefixes)
		addClause(clause, values...)
	}
	if f.ParentRunID != "" {
		addClause("parent_run_id = ?", f.ParentRunID)
	} else if f.RootOnly {
		addClause("(parent_run_id IS NULL OR parent_run_id = '')")
	}
	if !f.Since.IsZero() {
		addClause("started_at >= ?", f.Since.UnixNano())
	}
	if f.HasCursor() && f.HasBeforeCursor() {
		return "", nil, errBothCursors
	}
	if f.HasCursor() {
		addClause(runCursorClause(f.AfterStartedAt), runCursorArgs(f)...)
	}
	if f.HasBeforeCursor() {
		addClause(runBeforeCursorClause(f.BeforeStartedAt), runBeforeCursorArgs(f)...)
	}
	for _, c := range runDisplayFilterClauses(dialect, f) {
		addClause(c.sql, c.args...)
	}
	return where, args, nil
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
func (o *Operator) GetLatestRun(ctx context.Context, pipeline string, statuses []string, maxAge time.Duration) (*Run, error) {
	return o.s.getLatestRun(ctx, `SELECT team,id FROM runs WHERE pipeline=?`, []any{pipeline}, pipeline, statuses, maxAge)
}

// GetLatestRun returns t's newest run for pipeline matching statuses within
// maxAge. ErrNotFound on miss.
func (t *Tenant) GetLatestRun(ctx context.Context, pipeline string, statuses []string, maxAge time.Duration) (*Run, error) {
	if t.team == "" {
		return nil, ErrNoTeam
	}
	return t.s.getLatestRun(ctx, `SELECT team,id FROM runs WHERE team=? AND pipeline=?`, []any{t.team, pipeline}, pipeline, statuses, maxAge)
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanRun(rs rowScanner, tail ...any) (*Run, error) {
	var r Run
	var request, lineage, annotations []byte
	var created int64
	var started, finished sql.NullInt64
	var parent sql.NullString
	destinations := []any{
		&r.team, &r.ID, &r.Number, &r.Pipeline, &r.Status, &r.TriggerSource, &r.GitBranch, &r.GitSHA,
		&request, &r.PlanSnapshot, &r.Error, &created, &started, &finished, &parent, &r.DeclaredRepo, &r.RetryOf, &r.RetriedAs,
		&lineage, &r.ReplayOfRunID, &r.ReplayOfNodeID, &r.AnnotationCount, &r.TopAnnotation, &annotations, &r.Admission, &r.NeedsAttention,
	}
	if err := rs.Scan(append(destinations, tail...)...); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, notFound("run", "")
		}
		return nil, err
	}
	var sealed runspec.SealedRequest
	if err := json.Unmarshal(request, &sealed); err != nil {
		return nil, fmt.Errorf("decode run request: %w", err)
	}
	display := runspec.DisplayRequest(sealed)
	r.RepoInherited = sealed.Request.Lineage.RepoInherited
	r.RepoURL = sealed.Request.Source.Location.RemoteURL
	if len(annotations) > 0 {
		if err := json.Unmarshal(annotations, &r.Annotations); err != nil {
			return nil, fmt.Errorf("decode run annotations: %w", err)
		}
	}
	r.CreatedAt, r.FinishedAt, r.ParentRunID = time.Unix(0, created).UTC(), stateTime(finished), parent.String
	if started.Valid {
		r.StartedAt = time.Unix(0, started.Int64).UTC()
	}
	r.Invocation = map[string]any{InvocationSecretArgsKey: display.SecretArgs}
	r.TriggerUser = display.Actor.Display
	if r.TriggerUser == "" {
		r.TriggerUser = display.Actor.ID
	}
	if display.PullRequest.Set {
		pr := display.PullRequest.Value
		r.PullRequest = &RunPullRequest{Number: int(pr.Number), Action: pr.Action, BaseRef: pr.BaseRef, BaseSHA: pr.BaseSHA, HeadRef: pr.HeadRef, HeadSHA: pr.HeadSHA}
	}
	if r.RetryOf != "" {
		r.RetrySource = RetrySourceManual
	}
	return &r, nil
}

// Node reports persisted job metadata and attempt history.
type Node struct {
	RunID        string     `json:"run_id,omitempty"`
	NodeID       string     `json:"id"`
	Status       string     `json:"status"`
	Outcome      string     `json:"outcome,omitempty"`
	Deps         []string   `json:"deps"`
	Error        string     `json:"error,omitempty"`
	OutputDigest string     `json:"output_digest,omitempty"`
	StartedAt    *time.Time `json:"started_at,omitempty"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`

	ReadyAt            *time.Time         `json:"ready_at,omitempty"`
	ClaimedBy          string             `json:"claimed_by,omitempty"`
	Claimed            bool               `json:"claimed,omitempty"`
	LeaseExpiresAt     *time.Time         `json:"lease_expires_at,omitempty"`
	OfferStartedAt     *time.Time         `json:"offer_started_at,omitempty"`
	ClaimWorkerID      string             `json:"claim_worker_id,omitempty"`
	ClaimExecutorKind  string             `json:"claim_executor_kind,omitempty"`
	ClaimReservationID string             `json:"claim_reservation_id,omitempty"`
	CoordinatorID      string             `json:"coordinator_id,omitempty"`
	ClaimGeneration    int64              `json:"claim_generation,omitempty"`
	ClaimMembershipID  string             `json:"claim_membership_id,omitempty"`
	ExecutorKind       string             `json:"executor_kind,omitempty"`
	ExecutorName       string             `json:"executor_name,omitempty"`
	ExecutorID         string             `json:"executor_id,omitempty"`
	ExecutorLocation   string             `json:"executor_location,omitempty"`
	ExecutionSite      string             `json:"execution_site,omitempty"`
	ExecutionSiteName  string             `json:"execution_site_name,omitempty"`
	ExecutionStartedAt *time.Time         `json:"execution_started_at,omitempty"`
	ReservationID      string             `json:"reservation_id,omitempty"`
	AttemptsConsumed   int                `json:"attempts_consumed,omitempty"`
	RetryRootRunID     string             `json:"retry_root_run_id,omitempty"`
	ExecutionAttempts  []ExecutionAttempt `json:"execution_attempts,omitempty"`

	PlacementReason   string     `json:"placement_reason,omitempty"`
	PlacementHoldFrom *time.Time `json:"placement_hold_from,omitempty"`

	CreditCPUClassCores       int64 `json:"credit_cpu_class_cores,omitempty"`
	CreditCPUClassMemoryBytes int64 `json:"credit_cpu_class_memory_bytes,omitempty"`

	NeedsLabels   []string `json:"needs_labels,omitempty"`
	PrefersLabels []string `json:"prefers_labels,omitempty"`

	RequestedCores       float64 `json:"requested_cores,omitempty"`
	RequestedMemoryBytes int64   `json:"requested_memory_bytes,omitempty"`
	RequestedSlots       int     `json:"requested_slots,omitempty"`

	StatusDetail  string     `json:"status_detail,omitempty"`
	LastHeartbeat *time.Time `json:"last_heartbeat,omitempty"`

	FailureReason string `json:"failure_reason,omitempty"`
	ExitCode      *int   `json:"exit_code,omitempty"`

	Annotations []string `json:"annotations,omitempty"`

	Summary string `json:"summary,omitempty"`

	CPUNanos         int64          `json:"cpu_nanos,omitempty"`
	MaxRSSBytes      int64          `json:"max_rss_bytes,omitempty"`
	ProcessWallNanos int64          `json:"process_wall_nanos,omitempty"`
	SpecHash         string         `json:"spec_hash,omitempty"`
	Generated        *GeneratedNode `json:"generated,omitempty"`

	outputRecord []byte
}

// GeneratedNode is how a generator added a node to its run.
type GeneratedNode struct {
	// Source is the node whose generator added this one, and Generator that
	// generator's closure id.
	Source    string `json:"source"`
	Generator string `json:"generator"`
	// Batch is how many nodes that generator added together, so a reader
	// that finds fewer rows knows the batch was not fully recorded.
	Batch int `json:"batch"`
	// Spec is the node's plan document, spec_hash included.
	Spec json.RawMessage `json:"spec"`
}

// ListNodes returns this tenant's jobs in insertion order.
func (t *Tenant) ListNodes(ctx context.Context, runID string) ([]*Node, error) {
	rows, err := t.s.query(ctx, `SELECT `+nodeSelectColumns+`
  FROM nodes
 WHERE team = ? AND run_id = ?
 ORDER BY seq, node_id`, string(t.team), runID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []*Node
	for rows.Next() {
		record := &Node{}
		if err := scanNodeRow(rows, record); err != nil {
			return nil, err
		}
		out = append(out, record)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for _, n := range out {
		attempts, err := t.ListNodeExecutionAttempts(ctx, runID, n.NodeID)
		if err != nil {
			return nil, err
		}
		n.ExecutionAttempts = attempts
		if err := t.populateNodeOutput(ctx, n); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// GetNode returns this tenant's job or ErrNotFound.
func (t *Tenant) GetNode(ctx context.Context, runID, nodeID string) (*Node, error) {
	row := t.s.queryRow(ctx, `SELECT `+nodeSelectColumns+`
  FROM nodes
 WHERE team = ? AND run_id = ? AND node_id = ?`, string(t.team), runID, nodeID)
	record := &Node{}
	if err := scanNodeRow(row, record); err != nil {
		return nil, err
	}
	attempts, err := t.ListNodeExecutionAttempts(ctx, runID, nodeID)
	if err != nil {
		return nil, err
	}
	record.ExecutionAttempts = attempts
	if err := t.populateNodeOutput(ctx, record); err != nil {
		return nil, err
	}
	return record, nil
}

const nodeSelectColumns = `run_id,node_id,status,outcome,deps_json,error,started_at,finished_at,
 ready_at,claimed_by,lease_expires_at,needs_labels,prefers_labels,requested_cores,requested_memory_bytes,requested_slots,
 offer_started_at,status_detail,last_heartbeat,failure_reason,exit_code,annotations_json,summary,cpu_nanos,max_rss_bytes,process_wall_nanos,
 generation,executor_kind,executor_id,executor_location,execution_started_at,attempt,placement_reason,placement_hold_from,COALESCE((SELECT c.cpu_class FROM billing_awards a JOIN credit_charges c ON c.team=a.team AND c.capability_prefix=a.capability_prefix AND c.kind='reservation' WHERE a.team=nodes.team AND a.run_id=nodes.run_id AND a.node_id=nodes.node_id AND a.unit_kind='node' AND a.generation=nodes.generation),0),spec_hash,generated_json,output_record`

func scanNodeRow(rs rowScanner, n *Node) error {
	var deps, labels, prefers, annotations, generated []byte
	var started, finished, ready, lease, offered, heartbeat, executed, hold sql.NullInt64
	var holder sql.NullString
	var exit sql.NullInt64
	err := rs.Scan(&n.RunID, &n.NodeID, &n.Status, &n.Outcome, &deps, &n.Error, &started, &finished,
		&ready, &holder, &lease, &labels, &prefers, &n.RequestedCores, &n.RequestedMemoryBytes, &n.RequestedSlots,
		&offered, &n.StatusDetail, &heartbeat, &n.FailureReason, &exit, &annotations, &n.Summary, &n.CPUNanos, &n.MaxRSSBytes, &n.ProcessWallNanos,
		&n.ClaimGeneration, &n.ExecutorKind, &n.ExecutorID, &n.ExecutorLocation, &executed, &n.AttemptsConsumed,
		&n.PlacementReason, &hold, &n.CreditCPUClassCores, &n.SpecHash, &generated, &n.outputRecord)
	if errors.Is(err, sql.ErrNoRows) {
		return notFound("job", "")
	}
	if err != nil {
		return err
	}
	for _, part := range []struct {
		raw []byte
		dst any
	}{
		{deps, &n.Deps}, {labels, &n.NeedsLabels}, {prefers, &n.PrefersLabels}, {annotations, &n.Annotations}, {generated, &n.Generated},
	} {
		if len(part.raw) > 0 {
			if err := json.Unmarshal(part.raw, part.dst); err != nil {
				return fmt.Errorf("decode job %s: %w", n.NodeID, err)
			}
		}
	}
	n.StartedAt, n.FinishedAt, n.ReadyAt = stateTime(started), stateTime(finished), stateTime(ready)
	n.LeaseExpiresAt, n.OfferStartedAt, n.LastHeartbeat = stateTime(lease), stateTime(offered), stateTime(heartbeat)
	n.ExecutionStartedAt, n.PlacementHoldFrom = stateTime(executed), stateTime(hold)
	n.ClaimedBy, n.Claimed = holder.String, holder.Valid
	n.CreditCPUClassMemoryBytes = CPUClassMemoryBytes(n.CreditCPUClassCores)
	if exit.Valid {
		code := int(exit.Int64)
		n.ExitCode = &code
	}
	return nil
}

// Step status constants. Steps are inserted as StepRunning on
// step_start and transitioned to passed/failed/cancelled on step_end. Skipped
// steps insert directly as StepSkipped with both lifecycle timestamps set.
const (
	StepRunning   = "running"
	StepPassed    = "passed"
	StepFailed    = "failed"
	StepCancelled = "cancelled"
	StepSkipped   = "skipped"
)

// NodeStep is one row from the node_steps table: per-step runtime
// state for the inner-Work DAG. Status moves from running to passed, failed, or
// cancelled once; skipped is terminal at insert.
type NodeStep struct {
	RunID       string     `json:"run_id,omitempty"`
	NodeID      string     `json:"node_id"`
	StepID      string     `json:"step_id"`
	Attempt     int64      `json:"attempt"`
	Status      string     `json:"status"`
	Error       string     `json:"error,omitempty"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
	Annotations []string   `json:"annotations,omitempty"`
	// Summary is the latest markdown run summary emitted by
	// sparkwing.Summary inside this step's body. Overwrite-on-write:
	// only the last value is kept.
	Summary string `json:"summary,omitempty"`
}

// ListNodeSteps returns persisted step observations for a tenant run.
func (t *Tenant) ListNodeSteps(ctx context.Context, runID string) ([]*NodeStep, error) {
	rows, err := t.s.query(ctx, `
SELECT node_id, step_id, attempt, status, error, COALESCE(source_started_at,started_at), COALESCE(source_finished_at,finished_at), annotations_json, summary
FROM node_steps
WHERE team = ? AND run_id = ?
ORDER BY node_id, attempt, started_at, step_id`, string(t.team), runID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []*NodeStep
	for rows.Next() {
		ns := &NodeStep{RunID: runID}
		var started, finished sql.NullInt64
		var annotations []byte
		if err := rows.Scan(&ns.NodeID, &ns.StepID, &ns.Attempt, &ns.Status, &ns.Error, &started, &finished, &annotations, &ns.Summary); err != nil {
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

// safety: cleanup failures must stay visible when the caller has already returned.
func rollbackOrLog(tx *storeTx) {
	if err := tx.Rollback(); err != nil {
		slog.Warn("rolling back a store transaction failed", "err", err)
	}
}

// safety: closes the cursor on an early return, where the caller's own Close
// never runs; a second close after that one reports nothing.
func closeRowsOrLog(rows rowCursor) {
	if err := rows.Close(); err != nil {
		slog.Warn("closing a store row cursor failed", "err", err)
	}
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

// ListEventsAfter returns this tenant's run events after the supplied sequence.
func (t *Tenant) ListEventsAfter(ctx context.Context, runID string, afterSeq int64, limit int) ([]Event, error) {
	if limit <= 0 {
		limit = 500
	}
	limit = min(limit, MaxRunListLimit)
	rows, err := t.s.query(ctx, `
SELECT run_id, seq, node_id, kind, ts, payload
  FROM events
 WHERE team = ? AND run_id = ? AND seq > ?
 ORDER BY seq ASC
 LIMIT ?`, string(t.team), runID, afterSeq, limit)
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

// AppendEvent writes the run's next event in order.
func (t *Tenant) AppendEvent(ctx context.Context, runID, nodeID, kind string, payload []byte) error {
	return t.s.inTx(ctx, func(tx *storeTx) error {
		_, err := appendEventTx(ctx, tx, t.team, runID, nodeID, kind, payload, tx.now(ctx))
		if errors.Is(err, sql.ErrNoRows) {
			return notFound("run", runID)
		}
		return err
	})
}

func appendEventTx(ctx context.Context, tx *storeTx, team Team, runID, nodeID, kind string, payload any, at time.Time) (int64, error) {
	var raw []byte
	var err error
	switch value := payload.(type) {
	case []byte:
		raw = value
	case json.RawMessage:
		raw = value
	default:
		raw, err = json.Marshal(value)
	}
	if err != nil {
		return 0, err
	}
	var seq int64
	if err := tx.QueryRowContext(ctx, `UPDATE runs SET event_bytes=event_bytes+?,event_count=event_count+1 WHERE team=? AND id=? RETURNING event_count`, eventBytes(kind, raw), team, runID).Scan(&seq); err != nil {
		return 0, err
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO events (team, run_id, seq, node_id, kind, ts, payload)
VALUES (?,?,?,?,?,?,?)`, string(team), runID, seq, nodeID, kind, at.UnixNano(), raw)
	if err != nil {
		return 0, err
	}
	return seq, err
}

// ErrNotFound is returned when a lookup misses.
var ErrNotFound = errors.New("not found")

func notFound(resource, id string) error {
	if id == "" {
		return fmt.Errorf("%s: %w", resource, ErrNotFound)
	}
	return fmt.Errorf("%s %q: %w", resource, id, ErrNotFound)
}

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

// GetActiveDebugPause returns the open pause for a node of one of t's runs, if any.
func (t *Tenant) GetActiveDebugPause(ctx context.Context, runID, nodeID string) (*DebugPause, error) {
	row := t.s.queryRow(ctx, `
SELECT run_id, node_id, reason, paused_at, expires_at, released_at, released_by, release_kind
  FROM debug_pauses
 WHERE team = ? AND run_id = ? AND node_id = ? AND released_at IS NULL
 ORDER BY paused_at DESC
 LIMIT 1`, string(t.team), runID, nodeID)
	return scanDebugPause(row)
}

// ListDebugPauses returns all pause rows for one of t's runs, newest first.
func (t *Tenant) ListDebugPauses(ctx context.Context, runID string) (_ []*DebugPause, err error) {
	rows, err := t.s.query(ctx, `
SELECT run_id, node_id, reason, paused_at, expires_at, released_at, released_by, release_kind
  FROM debug_pauses
 WHERE team = ? AND run_id = ?
 ORDER BY paused_at DESC`, string(t.team), runID)
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
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

func scanDebugPause(rs rowScanner) (*DebugPause, error) {
	var p DebugPause
	var pausedNS, expiresNS int64
	var releasedNS sql.NullInt64
	err := rs.Scan(&p.RunID, &p.NodeID, &p.Reason,
		&pausedNS, &expiresNS, &releasedNS, &p.ReleasedBy, &p.ReleaseKind)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, notFound("debug pause", "")
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

// Trigger combines sealed run display with queue state.
type Trigger struct {
	ID               string            `json:"id"`
	Pipeline         string            `json:"pipeline"`
	Args             map[string]string `json:"args,omitempty"`
	TriggerSource    string            `json:"trigger_source,omitempty"`
	TriggerUser      string            `json:"trigger_user,omitempty"`
	TriggerEnv       map[string]string `json:"trigger_env,omitempty"`
	GitBranch        string            `json:"git_branch,omitempty"`
	GitSHA           string            `json:"git_sha,omitempty"`
	Status           string            `json:"status"`
	Error            string            `json:"error,omitempty"`
	CreatedAt        time.Time         `json:"created_at"`
	ClaimedAt        *time.Time        `json:"claimed_at,omitempty"`
	LeaseExpiresAt   *time.Time        `json:"lease_expires_at,omitempty"`
	ParentRunID      string            `json:"parent_run_id,omitempty"`
	Repo             string            `json:"repo,omitempty"`
	RepoURL          string            `json:"repo_url,omitempty"`
	GithubOwner      string            `json:"github_owner,omitempty"`
	GithubRepo       string            `json:"github_repo,omitempty"`
	GithubRepoID     int64             `json:"-"`
	RepoInherited    bool              `json:"repo_inherited,omitempty"`
	RetryOf          string            `json:"retry_of,omitempty"`
	RetrySource      string            `json:"retry_source,omitempty"`
	ParentNodeID     string            `json:"parent_node_id,omitempty"`
	Full             bool              `json:"full,omitempty"`
	IdempotencyKey   string            `json:"idempotency_key,omitempty"`
	ClaimSeq         int64             `json:"claim_seq,omitempty"`
	WebhookDelivery  string            `json:"webhook_delivery,omitempty"`
	WebhookReplayKey string            `json:"webhook_replay_key,omitempty"`
	Team             Team              `json:"team,omitempty"`
	RunnerPin        string            `json:"-"`
}

// DefaultLeaseDuration is the configured runner claim lease TTL.
const DefaultLeaseDuration = 3 * time.Minute

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique constraint") ||
		strings.Contains(msg, "duplicate key value")
}

// SpawnedChild identifies a child pipeline run for its parent job's display.
type SpawnedChild struct {
	ParentNodeID string `json:"parent_node_id"`
	Pipeline     string `json:"pipeline"`
	ChildRunID   string `json:"child_run_id"`
}

// GetTrigger returns one tenant trigger's sealed display and queue state.
func (t *Tenant) GetTrigger(ctx context.Context, id string) (*Trigger, error) {
	trigger, err := scanTrigger(t.s.queryRow(ctx, triggerSelect+` WHERE triggers.team=? AND triggers.id=?`, t.team, id))
	if err != nil {
		return nil, err
	}
	if err := t.s.refreshTriggerPresentations(ctx, []*Trigger{trigger}); err != nil {
		return nil, err
	}
	return trigger, nil
}

// TriggerFilter narrows ListTriggers; zero value matches all.
type TriggerFilter struct {
	Statuses  []string
	Pipelines []string
	Repo      string
	Limit     int
}

const triggerSelect = `SELECT ` + runColumns + `,triggers.team,triggers.status,triggers.claimed_at,triggers.lease_expires_at,
 triggers.generation,triggers.webhook_delivery,triggers.webhook_replay_key,runs.idempotency_key,runs.parent_node_id
 FROM runs JOIN triggers ON triggers.team=runs.team AND triggers.id=runs.id`

// ListTriggers returns this tenant's sealed trigger displays, newest first.
func (t *Tenant) ListTriggers(ctx context.Context, f TriggerFilter) (_ []*Trigger, err error) {
	if t.team == "" {
		return nil, ErrNoTeam
	}

	filter := RunFilter{Pipelines: f.Pipelines}
	if f.Repo != "" {
		filter.RepoNames = []string{f.Repo}
	}
	where, args, err := runFilterWhere(t.s.dialect, filter)
	if err != nil {
		return nil, err
	}
	if len(f.Statuses) > 0 {
		if where == "" {
			where = " WHERE "
		} else {
			where += " AND "
		}
		where += "triggers.status IN (" + placeholders(len(f.Statuses)) + ")"
		args = append(args, stringsToAny(f.Statuses)...)
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 20
	}
	args = append(args, min(limit, MaxRunListLimit))
	args = append([]any{t.team}, args...)
	where = strings.Replace(where, " WHERE ", " AND ", 1)
	rows, err := t.s.query(ctx, triggerSelect+` WHERE runs.team = ?`+where+` ORDER BY triggers.created_at DESC,triggers.id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	var out []*Trigger
	for rows.Next() {
		trigger, err := scanTrigger(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, trigger)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	if err := t.s.refreshTriggerPresentations(ctx, out); err != nil {
		return nil, err
	}
	return out, nil
}

func scanTrigger(rs rowScanner) (*Trigger, error) {
	var tr Trigger
	var claimed, lease sql.NullInt64
	run, err := scanRun(rs, &tr.Team, &tr.Status, &claimed, &lease, &tr.ClaimSeq, &tr.WebhookDelivery, &tr.WebhookReplayKey, &tr.IdempotencyKey, &tr.ParentNodeID)
	if err != nil {
		return nil, err
	}
	tr.ID, tr.Pipeline, tr.Args = run.ID, run.Pipeline, run.Args
	tr.TriggerSource, tr.TriggerUser, tr.Error = run.TriggerSource, run.TriggerUser, run.Error
	tr.GitBranch, tr.GitSHA, tr.CreatedAt = run.GitBranch, run.GitSHA, run.CreatedAt
	tr.ClaimedAt, tr.LeaseExpiresAt = stateTime(claimed), stateTime(lease)
	tr.ParentRunID, tr.RetryOf, tr.RetrySource = run.ParentRunID, run.RetryOf, run.RetrySource
	tr.Repo, tr.RepoURL, tr.RepoInherited = run.DeclaredRepo, run.RepoURL, run.RepoInherited
	return &tr, nil
}

// ErrLockHeld signals the caller is not the current slot holder. HTTP -> 409.
var ErrLockHeld = errors.New("held by another holder")

// ErrConcurrencySuperseded signals a concurrency holder was superseded.
var ErrConcurrencySuperseded = errors.New("concurrency holder superseded")

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

// GetApproval returns the row of a node of one of t's runs, or ErrNotFound.
func (t *Tenant) GetApproval(ctx context.Context, runID, nodeID string) (*Approval, error) {
	row := t.s.queryRow(ctx, `
SELECT run_id, node_id, requested_at, message, timeout_ms, on_timeout,
       approver, resolved_at, resolution, comment
  FROM approvals WHERE team = ? AND run_id = ? AND node_id = ?`, string(t.team), runID, nodeID)
	return scanApproval(row)
}

// ListApprovalsForRun returns the rows of one of t's runs in request order.
func (t *Tenant) ListApprovalsForRun(ctx context.Context, runID string) (_ []*Approval, err error) {
	rows, err := t.s.query(ctx, `
SELECT run_id, node_id, requested_at, message, timeout_ms, on_timeout,
       approver, resolved_at, resolution, comment
  FROM approvals WHERE team = ? AND run_id = ?
 ORDER BY requested_at ASC`, string(t.team), runID)
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
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
			return nil, notFound("approval", "")
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

//go:embed schema.sql
var schemaNeutral string

// safety: the baseline is the only schema; a store recording any other version is refused, never upgraded.
const expectedSchemaVersion = 22

// ExpectedSchemaVersion returns the schema version this binary
// understands. Useful for diagnostics, version-mismatch reporting,
// and tests that need to assert what Open will write into the
// sparkwing_schema_version table on a fresh database.
func ExpectedSchemaVersion() int { return expectedSchemaVersion }

// CurrentSchemaVersion returns the schema version recorded in the
// database (MAX of sparkwing_schema_version), or 0 when unrecorded.
func (s *Store) CurrentSchemaVersion(ctx context.Context) (int, error) {
	var v int
	if err := s.queryRow(ctx,
		`SELECT COALESCE(MAX(version), 0) FROM sparkwing_schema_version`).Scan(&v); err != nil {
		return 0, err
	}
	return v, nil
}

// TriggerIdempotencyIndexName is the unique index enforcing at most one
// trigger per (pipeline, idempotency key).
const TriggerIdempotencyIndexName = "idx_triggers_idempotency_key"

// TriggerWebhookReplayKeyIndexName is the unique index enforcing at most
// one trigger per digest of the signed material a webhook delivery
// carried.
const TriggerWebhookReplayKeyIndexName = "idx_triggers_webhook_replay_key"

// safety: two Postgres openers of an empty database would both create the
// baseline, so each takes this lock before looking.
const migrateAdvisoryLock = `SELECT pg_advisory_xact_lock(hashtext('sparkwing_migrate'))`

// safety: remedy is how the operator discards a store at another version, since nothing upgrades one.
func (s *Store) migrate(ctx context.Context, remedy string) error {
	if s.dialect == DialectPostgres {
		return s.createBaseline(ctx, remedy)
	}
	return retryOnBusy(func() error { return s.createBaseline(ctx, remedy) })
}

func (s *Store) createBaseline(ctx context.Context, remedy string) error {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return fmt.Errorf("begin schema setup: %w", err)
	}
	defer rollbackOrLog(tx)
	if s.dialect == DialectPostgres {
		if _, err := tx.ExecContext(ctx, migrateAdvisoryLock); err != nil {
			return fmt.Errorf("acquire migrate advisory lock: %w", err)
		}
	}
	version, err := recordedSchemaVersion(ctx, tx)
	if err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	switch version {
	case expectedSchemaVersion:
		return nil
	case 0:
		empty, err := databaseEmpty(ctx, tx)
		if err != nil {
			return fmt.Errorf("inspect the database before creating the baseline: %w", err)
		}
		if !empty {
			return errors.New("sparkwing: this database is not empty and is not a Sparkwing store " +
				"(it holds tables but no sparkwing_schema_version); point sparkwing at an empty database")
		}
	default:
		return schemaMismatchError(version, remedy)
	}
	translated, err := TranslateSchema(schemaNeutral, s.dialect)
	if err == nil {
		err = execStatements(ctx, tx, translated)
	}
	if err != nil {
		return fmt.Errorf("create baseline schema: %w", err)
	}
	if err := tx.operator.installAuditProtection(ctx, tx); err != nil {
		return fmt.Errorf("protect audit history: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO sparkwing_schema_version (version, applied_at) VALUES (?, ?)`,
		expectedSchemaVersion, time.Now().UnixNano()); err != nil {
		return fmt.Errorf("record schema version: %w", err)
	}
	if err := recordDefaultTeamTx(ctx, tx); err != nil {
		return fmt.Errorf("record default team: %w", err)
	}
	if err := seedSignUpGateTx(ctx, tx); err != nil {
		return fmt.Errorf("seed sign-up gate: %w", err)
	}
	return tx.Commit()
}

func recordedSchemaVersion(ctx context.Context, tx *storeTx) (int, error) {
	exists := `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'sparkwing_schema_version'`
	if tx.dialect == DialectPostgres {
		exists = `SELECT COUNT(*) FROM (SELECT to_regclass('sparkwing_schema_version') AS t) r WHERE r.t IS NOT NULL`
	}
	var n int
	if err := tx.QueryRowContext(ctx, exists).Scan(&n); err != nil || n == 0 {
		return 0, err
	}
	var version int
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM sparkwing_schema_version`).Scan(&version)
	return version, err
}

// safety: a database with no version table may still be someone else's, and the baseline must not land beside it.
func databaseEmpty(ctx context.Context, tx *storeTx) (bool, error) {
	q := `SELECT COUNT(*) FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'`
	if tx.dialect == DialectPostgres {
		q = `SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = current_schema()`
	}
	var n int
	err := tx.QueryRowContext(ctx, q).Scan(&n)
	return n == 0, err
}

func isTerminalRunStatus(status string) bool {
	return status == "success" || status == runStatusFailed || status == runStatusCancelled
}
