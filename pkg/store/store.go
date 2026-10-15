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
	"io"
	"log/slog"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

// DefaultBusyTimeoutMS is the SQLite busy_timeout Open and OpenReadOnly
// apply, in milliseconds. See Open for why 30s.
const DefaultBusyTimeoutMS = 30000

var busyTimeoutMS = DefaultBusyTimeoutMS

// SetTestBusyTimeout makes every SQLite store this process opens use ms as
// its busy_timeout until the test ends.
func SetTestBusyTimeout(t interface{ Cleanup(func()) }, ms int) {
	original := busyTimeoutMS
	t.Cleanup(func() { busyTimeoutMS = original })
	busyTimeoutMS = ms
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
	// FailureCreditsExhausted: the controller's prepaid credit balance ran
	// out and the node was cancelled after the grace period.
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
	resultRecords    nodeResultRecords
	materializations materializationRecords
	claimProfiles    ClaimProfileResolver
	intake           intakeContracts
	intakePolicy     IntakePolicy
	plans            planContracts
	billing          billingCalculator
	planSecretOpener PlanSecretOpener
	delegation       delegationContracts
	provisions       ProvisionPolicy
	consolePolicy    consolePolicy
	sourcePolicy     sourceReleaseContracts
	clock            func() time.Time
	db               *sql.DB
	dialect          Dialect
	cleanup          func() error
	csrfKeyMu        sync.Mutex
	csrfKey          []byte
	csrfCipher       SessionKeyCipher
	outputDir        string
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
	} else if err := tightenExistingSQLite(path); err != nil {
		return nil, err
	}
	st, err := openSQL(context.Background(), "sqlite", sqliteDSN(path), DialectSQLite, "delete "+path)
	if err != nil {
		return nil, err
	}
	st.outputDir = filepath.Dir(path)
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

func sqliteReadOnlyDSN(path string) string {
	return sqliteReadOnlyDSNWithMode(path, false)
}

func sqliteReadOnlySnapshotDSN(path string) string {
	return sqliteReadOnlyDSNWithMode(path, true)
}

func sqliteReadOnlyDSNWithMode(path string, immutable bool) string {
	mode := "mode=ro"
	if immutable {
		mode += "&immutable=1"
	}
	return fmt.Sprintf("%s?%s&_pragma=busy_timeout(%d)&_pragma=query_only(true)", sqliteURI(path), mode, busyTimeoutMS)
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
	st, err := openReadOnlyDSN(sqliteReadOnlyDSN(path))
	if err == nil {
		st.outputDir = filepath.Dir(path)
	}
	return st, err
}

// OpenReadOnlySnapshot copies a stable database-and-WAL pair from an existing
// SQLite state database and opens the copy read-only. SQLite only opens the
// private copy, so the source directory is never changed. Raw file copying
// releases any SQLite locks this process holds on the source; callers with
// live source connections must run the snapshot in a separate process.
// Closing the Store
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
	st, err := openReadOnlyDSN(sqliteReadOnlySnapshotDSN(snapshotPath))
	if err != nil {
		_ = cleanup()
		return nil, err
	}
	st.cleanup = cleanup
	st.outputDir = filepath.Dir(path)
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
	db, err := sql.Open("sqlite", sqliteURI(path)+"?_pragma=busy_timeout(30000)")
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

func openReadOnlyDSN(dsn string) (*Store, error) {
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
func OpenPostgres(ctx context.Context, dsn string) (*Store, error) {
	if dsn == "" {
		return nil, errors.New("OpenPostgres: dsn is required")
	}
	return openSQL(ctx, "pgx", dsn, DialectPostgres, "drop the database")
}

func openSQL(ctx context.Context, driver, dsn string, dialect Dialect, remedy string) (*Store, error) {
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
	if err := s.migrate(ctx, remedy); err != nil {
		return nil, errors.Join(err, db.Close())
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

// IsBusyErr reports whether err is a SQLite busy or locked condition, which
// another connection clears by finishing its write. A caller that must tell a
// store it cannot read apart from one it merely cannot read *now* asks this
// before blaming the file.
func IsBusyErr(err error) bool { return isBusyErr(err) }

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
	// Admission is a controller-dispatched run's admission state, one of
	// the current admission phase; it is empty for any other run.
	Admission string `json:"admission,omitempty"`
	// DeclaredRepo is the repository the submitter typed (e.g.
	// "my-app"). It is metadata for display and filtering, and it
	// grants nothing, because no step proves the submitter owns the
	// repository it names.
	// safety: the wire name stays `repo` because a CLI and a controller
	// version independently, and a released client sending `repo` meets a
	// DisallowUnknownFields decoder.
	DeclaredRepo string `json:"repo,omitempty"`
	// RepoURL is `git remote get-url origin` at trigger time.
	RepoURL string `json:"repo_url,omitempty"`
	// GithubOwner/Repo: parsed when origin is github.
	GithubOwner string `json:"github_owner,omitempty"`
	GithubRepo  string `json:"github_repo,omitempty"`
	// RetryOf points at the source run; RetriedAs is the reverse.
	RetryOf   string `json:"retry_of,omitempty"`
	RetriedAs string `json:"retried_as,omitempty"`
	// RetrySource is RetrySourceManual for a manually retried run.
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
	// each sparkwing.AnnotateOld call; the dashboard renders these
	// without needing a per-row aggregate query.
	AnnotationCount int      `json:"annotation_count,omitempty"`
	TopAnnotation   string   `json:"top_annotation,omitempty"`
	Annotations     []string `json:"annotations,omitempty"`
	// NeedsAttention says why a ready node of the run waits with no agent
	// able to claim it; empty when none does.
	NeedsAttention string `json:"needs_attention,omitempty"`
	// TriggerUser and PullRequest are the actor of the run's trigger and
	// the pull request it fired for. The runs row does not hold them: the
	// controller's single-run read fills them from the trigger, so every
	// host that plans the run sees the same values without the trigger.
	TriggerUser string          `json:"trigger_user,omitempty"`
	PullRequest *RunPullRequest `json:"pull_request,omitempty"`
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

func (s *Store) createRunTx(ctx context.Context, tx *storeTx, team Team, r Run) error {
	if err := ValidateRunInvocation(r); err != nil {
		return err
	}
	if r.FinishedAt != nil && !isTerminalRunStatus(r.Status) {
		return fmt.Errorf("%w: a non-terminal run cannot have finished_at", ErrInvalidInput)
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
	if r.StartedAt.IsZero() {
		r.StartedAt = time.Now()
	}
	created := r.CreatedAt
	if created.IsZero() {
		created = r.StartedAt
	}
	// safety: a terminal run with no finish time is indistinguishable from an
	// earlier attempt's leftover row, and every caller that reads one to decide
	// whether work ended reads it wrongly.
	finished := nullableTimeNS(r.FinishedAt)
	if finished == nil && isTerminalRunStatus(r.Status) {
		finished = time.Now().UnixNano()
	}
	// safety: lock order across this store is executor eligibility first, then
	// a guard or ledger advisory lock; taking them the other way around here
	// deadlocks against a claim that holds the executor lock.
	if err := enforceRunsPerHourTx(ctx, tx, team, r.ID, creatingPrincipal(ctx), time.Now()); err != nil {
		return err
	}
	// safety: the team is in the conflict guard, not the conflict target,
	// because the target must name a unique index and runs has only the
	// primary key on id; a (team, id) target would race that key's own
	// violation on a same-team re-create.
	res, err := tx.ExecContext(ctx, `
INSERT INTO runs (team, id, pipeline, status, trigger_source, git_branch, git_sha, args_json, plan_json, created_at, started_at, finished_at, parent_run_id, declared_repo, repo_url, github_owner, github_repo, retry_of, retried_as, retry_source, replay_of_run_id, replay_of_node_id, invocation_json, created_principal)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET
    pipeline        = excluded.pipeline,
    status          = excluded.status,
    trigger_source  = excluded.trigger_source,
    git_branch      = excluded.git_branch,
    git_sha         = excluded.git_sha,
    args_json       = excluded.args_json,
    plan_json       = excluded.plan_json,
    started_at      = excluded.started_at,
    finished_at     = excluded.finished_at,
    parent_run_id   = excluded.parent_run_id,
    declared_repo   = CASE WHEN runs.declared_repo = '' THEN excluded.declared_repo ELSE runs.declared_repo END,
    repo_url        = CASE WHEN runs.repo_url = '' THEN excluded.repo_url ELSE runs.repo_url END,
    github_owner    = CASE WHEN runs.github_owner = '' THEN excluded.github_owner ELSE runs.github_owner END,
    github_repo     = CASE WHEN runs.github_repo = '' THEN excluded.github_repo ELSE runs.github_repo END,
    retry_of        = excluded.retry_of,
    retried_as      = excluded.retried_as,
    retry_source    = excluded.retry_source,
    replay_of_run_id  = excluded.replay_of_run_id,
    replay_of_node_id = excluded.replay_of_node_id,
    invocation_json   = excluded.invocation_json,
    created_principal = CASE WHEN runs.created_principal = '' THEN excluded.created_principal ELSE runs.created_principal END
WHERE runs.team = excluded.team AND runs.status = '`+runStatusPending+`' AND runs.finished_at IS NULL`,
		string(team), r.ID, r.Pipeline, r.Status, r.TriggerSource, r.GitBranch, r.GitSHA,
		argsJSON, r.PlanSnapshot, created.UnixNano(), r.StartedAt.UnixNano(), finished, parent,
		r.DeclaredRepo, r.RepoURL, r.GithubOwner, r.GithubRepo,
		r.RetryOf, r.RetriedAs, r.RetrySource,
		r.ReplayOfRunID, r.ReplayOfNodeID,
		invocationJSON, creatingPrincipal(ctx),
	)
	if err != nil {
		return err
	}
	// safety: a refused conflict guard is not an error, so without this a
	// team told its create succeeded could not read the row it "created"
	// and the other team would run the writer's plan under its own repo.
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		owner, found, err := runOwnerTx(ctx, tx, r.ID)
		if err != nil {
			return err
		}
		if found && owner != team {
			return fmt.Errorf("%w: run %s", ErrIDOwnedByAnotherTeam, r.ID)
		}
	}
	return nil
}

// ErrIDOwnedByAnotherTeam reports a write whose key already names a row of
// a different team. It is not [ErrNotFound] because the caller supplied the
// id and the id is taken, and it is not success because nothing was written.
var ErrIDOwnedByAnotherTeam = errors.New("store: id already belongs to another team")

// safety: takes no team because the question is who owns the id, and an
// answer scoped to the asker is no answer.
func runOwnerTx(ctx context.Context, tx *storeTx, runID string) (Team, bool, error) {
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

const finishRunStmt = `
UPDATE runs
   SET status = ?, error = ?, finished_at = ?
 WHERE id = ? AND team = ? AND finished_at IS NULL AND NOT (` + runTerminalIn + `)`

func finishRunOnceTx(ctx context.Context, tx *storeTx, runID, status, errMsg string, team Team) error {
	if !isTerminalRunStatus(status) {
		return fmt.Errorf("%w: %q is not a terminal run status", ErrInvalidInput, status)
	}
	if team == "" {
		return ErrNoTeam
	}
	res, err := tx.ExecContext(ctx, finishRunStmt,
		status, errMsg, time.Now().UnixNano(), runID, string(team))
	if err != nil {
		return err
	}
	if changed, err := res.RowsAffected(); err != nil || changed != 0 {
		if err != nil || status != outcomeSuccess {
			return err
		}
		return RecordBusinessEvent(tx, BusinessEvent{
			Team: team, Kind: BusinessEventFirstRunSucceeded, Attrs: map[string]any{"run_id": runID},
		})
	}
	var priorStatus, priorError string
	err = tx.QueryRowContext(ctx,
		`SELECT status, error FROM runs WHERE id = ? AND team = ?`, runID, string(team)).
		Scan(&priorStatus, &priorError)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if priorStatus == status && priorError == errMsg {
		return nil
	}
	return fmt.Errorf("%w: run %s already finished as %s", ErrInvalidInput, runID, priorStatus)
}

// safety: every run read shares this list, because a column added to runs
// has to reach each of them and scanRun together or the scan misaligns.
const runColumns = `id, pipeline, status, trigger_source, git_branch, git_sha, args_json, plan_json, error, created_at, started_at, finished_at, parent_run_id, declared_repo, repo_url, github_owner, github_repo, retry_of, retried_as, retry_source, replay_of_run_id, replay_of_node_id, invocation_json, annotation_count, top_annotation, annotations_json, admission, ` + runAttentionColumn

// GetRun fetches a single default-team run by ID.
func (s *Store) GetRun(ctx context.Context, runID string) (*Run, error) {
	return s.defaultTenant().GetRun(ctx, runID)
}

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
func (s *Store) ListRuns(ctx context.Context, f RunFilter) (_ []*Run, err error) {
	return s.listRuns(ctx, allTeams(), f)
}

func (s *Store) listRuns(ctx context.Context, scope teamScope, f RunFilter) (_ []*Run, err error) {
	where, args, err := runFilterWhere(s.dialect, scope, f)
	if err != nil {
		return nil, err
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	// safety: clamp again here, because a non-HTTP caller never passes ParseRunFilter's clamp.
	limit = min(limit, maxRunListFetch)
	args = append(args, limit)

	order := "started_at DESC, id DESC"
	if f.HasBeforeCursor() {
		order = "started_at ASC, id ASC"
	}
	query := `
SELECT ` + runColumns + `
  FROM runs` + where + `
 ORDER BY ` + order + `
 LIMIT ?`

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
	if f.HasBeforeCursor() {
		slices.Reverse(out)
	}
	return out, rows.Err()
}

// safety: the scanner maps every started_at at or below zero to the zero time, so a
// cursor on a never-started run addresses that whole group and orders it by id. Exact
// while those rows carry one stored value, which is what a single writer produces; a
// group holding more would order by the raw value first and this would skip part of it.
func runCursorClause(afterStartedAt int64) string {
	if afterStartedAt <= 0 {
		return "(started_at <= 0 AND id < ?)"
	}
	// hack: the expanded form rather than a row-value comparison, so the predicate
	// holds on every SQLite build this driver may sit on.
	return "(started_at < ? OR (started_at = ? AND id < ?))"
}

func runCursorArgs(f RunFilter) []any {
	if f.AfterStartedAt <= 0 {
		return []any{f.AfterID}
	}
	return []any{f.AfterStartedAt, f.AfterStartedAt, f.AfterID}
}

func (s *Store) countRuns(ctx context.Context, scope teamScope, f RunFilter) (int, error) {
	where, args, err := runFilterWhere(s.dialect, scope, f)
	if err != nil {
		return 0, err
	}
	row := s.queryRow(ctx, `SELECT COUNT(*) FROM runs`+where, args...)
	var n int
	if err := row.Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

func runFilterWhere(dialect Dialect, scope teamScope, f RunFilter) (string, []any, error) {
	if !scope.all && scope.team == "" {
		return "", nil, ErrNoTeam
	}
	var err error
	if f.GitSHAPrefixes, err = normalizeSHAPrefixes(f.GitSHAPrefixes); err != nil {
		return "", nil, err
	}
	if f.ExcludeGitSHAPrefixes, err = normalizeSHAPrefixes(f.ExcludeGitSHAPrefixes); err != nil {
		return "", nil, err
	}

	where := ""
	args := []any{}
	if !scope.all {
		where = " WHERE team = ?"
		args = append(args, string(scope.team))
	}
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
	addIn("declared_repo", f.DeclaredRepos)
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
func (s *Store) GetLatestRun(ctx context.Context, pipeline string, statuses []string, maxAge time.Duration) (*Run, error) {
	return s.getLatestRun(ctx, allTeams(), pipeline, statuses, maxAge)
}

// GetLatestRun returns t's newest run for pipeline matching statuses within
// maxAge. ErrNotFound on miss.
func (t *Tenant) GetLatestRun(ctx context.Context, pipeline string, statuses []string, maxAge time.Duration) (*Run, error) {
	return t.s.getLatestRun(ctx, oneTeam(t.team), pipeline, statuses, maxAge)
}

// DeleteRun removes one tenant run and its retained operational data.
func (t *Tenant) DeleteRun(ctx context.Context, runID string) error {
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var owned int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM runs WHERE team = ? AND id = ?
UNION ALL SELECT 1 FROM triggers WHERE team = ? AND id = ? LIMIT 1`,
		string(t.team), runID, string(t.team), runID).Scan(&owned)
	if errors.Is(err, sql.ErrNoRows) {
		return notFound("run", runID)
	}
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM runs WHERE team = ? AND id = ?`, string(t.team), runID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM triggers WHERE team = ? AND id = ? AND parent_node_id = ''`, string(t.team), runID); err != nil {
		return err
	}
	// safety: concurrency_cache carries no foreign key, so a memo
	// entry left here keeps pointing at output this delete removes,
	// and every later hit on that key fails to fetch it.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM concurrency_cache WHERE team = ? AND origin_run_id = ?`, string(t.team), runID); err != nil {
		return err
	}
	return tx.Commit()
}

type rowScanner interface {
	Scan(dest ...any) error
}

func nullableTimeNS(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UnixNano()
}

func scanRun(rs rowScanner) (*Run, error) {
	var r Run
	var argsJSON, planJSON, invocationJSON, annotationsJSON []byte
	var createdNS, startedNS int64
	var finishedNS sql.NullInt64
	var parent sql.NullString
	err := rs.Scan(&r.ID, &r.Pipeline, &r.Status, &r.TriggerSource,
		&r.GitBranch, &r.GitSHA, &argsJSON, &planJSON, &r.Error,
		&createdNS, &startedNS, &finishedNS, &parent,
		&r.DeclaredRepo, &r.RepoURL, &r.GithubOwner, &r.GithubRepo,
		&r.RetryOf, &r.RetriedAs, &r.RetrySource,
		&r.ReplayOfRunID, &r.ReplayOfNodeID, &invocationJSON,
		&r.AnnotationCount, &r.TopAnnotation, &annotationsJSON,
		&r.Admission, &r.NeedsAttention)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, notFound("run", "")
		}
		return nil, err
	}
	if createdNS > 0 {
		r.CreatedAt = time.Unix(0, createdNS)
	}
	if startedNS > 0 {
		r.StartedAt = time.Unix(0, startedNS)
	}
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
	OutputRef  *OutputRef `json:"output_ref,omitempty"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`

	// Warm-pool dispatch; zero on laptop / K8sRunner paths.
	ReadyAt             *time.Time         `json:"ready_at,omitempty"`
	ClaimedBy           string             `json:"claimed_by,omitempty"`
	Claimed             bool               `json:"claimed,omitempty"`
	LeaseExpiresAt      *time.Time         `json:"lease_expires_at,omitempty"`
	OfferStartedAt      *time.Time         `json:"offer_started_at,omitempty"`
	OfferPriorityTarget int                `json:"offer_priority_target,omitempty"`
	ClaimBasePriority   int                `json:"claim_base_priority,omitempty"`
	ClaimPriority       int                `json:"claim_priority,omitempty"`
	ClaimWorkerID       string             `json:"claim_worker_id,omitempty"`
	ClaimExecutorKind   string             `json:"claim_executor_kind,omitempty"`
	ClaimReservationID  string             `json:"claim_reservation_id,omitempty"`
	CoordinatorID       string             `json:"coordinator_id,omitempty"`
	ClaimGeneration     int64              `json:"claim_generation,omitempty"`
	ClaimMembershipID   string             `json:"claim_membership_id,omitempty"`
	ExecutorKind        string             `json:"executor_kind,omitempty"`
	ExecutorName        string             `json:"executor_name,omitempty"`
	ExecutorID          string             `json:"executor_id,omitempty"`
	ExecutorLocation    string             `json:"executor_location,omitempty"`
	ExecutionSite       string             `json:"execution_site,omitempty"`
	ExecutionSiteName   string             `json:"execution_site_name,omitempty"`
	ExecutionStartedAt  *time.Time         `json:"execution_started_at,omitempty"`
	ReservationID       string             `json:"reservation_id,omitempty"`
	AttemptsConsumed    int                `json:"attempts_consumed,omitempty"`
	RetryRootRunID      string             `json:"retry_root_run_id,omitempty"`
	ExecutionAttempts   []ExecutionAttempt `json:"execution_attempts,omitempty"`

	// PlacementReason names why the claiming runner got this node:
	// "preference", "fallback" after the local-first hold window, or "none"
	// when it carried no preference. Empty on a node no claim path decided.
	PlacementReason string `json:"placement_reason,omitempty"`
	// PlacementHoldFrom is when the node became claimable, which the
	// local-first hold runs from.
	PlacementHoldFrom *time.Time `json:"placement_hold_from,omitempty"`

	// CreditCPUClassCores is the cpu class the claim billed this node at,
	// which is the shape its executor owes it. Zero on a node no metered
	// claim priced.
	CreditCPUClassCores int64 `json:"credit_cpu_class_cores,omitempty"`
	// CreditCPUClassMemoryBytes is the memory that class carries, which is
	// what its executor gives the node alongside the cores.
	CreditCPUClassMemoryBytes int64 `json:"credit_cpu_class_memory_bytes,omitempty"`

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
	// by sparkwing.AnnotateOld during the node's execution. Each call
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

	// ClaimToken is set only on the answer to a runner's claim: the claim
	// token the node's process authenticates with in place of the runner's
	// own credential. It stops working when the claim ends.
	ClaimToken string `json:"claim_token,omitempty"`
	// SpecHash is the node's spec_hash in the plan its run host accepted. A
	// remote host of the node runs it only when its own plan of the node
	// hashes the same.
	SpecHash string `json:"spec_hash,omitempty"`
	// Generated is set on a node a generator added to its run: where it came
	// from and its whole spec, which every later host of the node reads
	// instead of generating it again.
	Generated *GeneratedNode `json:"generated,omitempty"`
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

// CreateNode inserts a node in the "pending" state.
func (s *Store) CreateNode(ctx context.Context, n Node) error {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	// safety: the team comes off the run rather than off the caller, so a node
	// cannot land in a team its run does not belong to.
	team, err := creditTeamForRunTx(ctx, tx, n.RunID)
	if err != nil {
		return err
	}
	if err := enforceNodesPerRunTx(ctx, tx, team, n.RunID, n.NodeID); err != nil {
		return err
	}
	requestedSlots := n.RequestedSlots
	if requestedSlots < 1 {
		requestedSlots = 1
	}
	n.RequestedSlots = requestedSlots
	depsJSON, _ := json.Marshal(n.Deps)
	var generatedJSON []byte
	if n.Generated != nil {
		if generatedJSON, err = json.Marshal(n.Generated); err != nil {
			return fmt.Errorf("node %s generated spec: %w", n.NodeID, err)
		}
	}
	var labelsJSON []byte
	if len(n.NeedsLabels) > 0 {
		labelsJSON, _ = json.Marshal(n.NeedsLabels)
	}
	var prefersJSON []byte
	if len(n.PrefersLabels) > 0 {
		prefersJSON, _ = json.Marshal(n.PrefersLabels)
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO nodes (team, run_id, node_id, status, deps_json, needs_labels, prefers_labels,
                   requested_cores, requested_memory_bytes, requested_slots,
			       attempts_consumed, retry_root_run_id,
			       spec_hash, generated_json, seq)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?,
		(SELECT COALESCE(MAX(seq), 0) + 1 FROM nodes WHERE team = ? AND run_id = ?))`,
		string(team), n.RunID, n.NodeID, n.Status, depsJSON, labelsJSON, prefersJSON,
		n.RequestedCores, n.RequestedMemoryBytes, requestedSlots,
		n.AttemptsConsumed,
		n.RunID,
		n.SpecHash, generatedJSON,
		string(team), n.RunID)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// FinishNode marks terminal with outcome + optional output/error.
func (s *Store) FinishNode(ctx context.Context, runID, nodeID, outcome, errMsg string, output []byte) error {
	return s.FinishNodeWithReason(ctx, runID, nodeID, outcome, errMsg, output, FailureUnknown, nil)
}

// FinishNodeWithReason additionally records a Failure* code + exit. Output
// bytes are written to the store's own output directory, so only a store
// with one ([Store.OutputDir]) accepts them.
func (s *Store) FinishNodeWithReason(ctx context.Context, runID, nodeID, outcome, errMsg string, output []byte, reason string, exitCode *int) error {
	// safety: the output is written into the node's own team and the finish
	// below is the default team's, so a node of another team is refused before
	// its bytes are stored and charged.
	if _, err := s.defaultTenant().GetNode(ctx, runID, nodeID); err != nil {
		return err
	}
	ref, err := s.writeLocalOutput(ctx, runID, nodeID, output)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrOutputNotStored, err)
	}
	return s.defaultTenant().FinishNodeWithOutputRef(ctx, runID, nodeID, outcome, errMsg, ref, reason, exitCode)
}

// FinishNodeWithOutputRef finishes a tenant job with committed output.
func (t *Tenant) FinishNodeWithOutputRef(ctx context.Context, runID, nodeID, outcome, errMsg string, output *OutputRef, reason string, exitCode *int) error {
	return t.finishNode(ctx, runID, nodeID, outcome, errMsg, output, reason, exitCode)
}

func (t *Tenant) finishNode(ctx context.Context, runID, nodeID, outcome, errMsg string, output *OutputRef,
	reason string, exitCode *int,
) error {
	var code any
	if exitCode != nil {
		code = *exitCode
	}
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var team string
	var generation, consumed int64
	err = tx.QueryRowContext(ctx, `SELECT team, claim_generation, attempts_consumed FROM nodes WHERE team = ? AND run_id = ? AND node_id = ?`+tx.forUpdate(), string(t.team), runID, nodeID).Scan(&team, &generation, &consumed)
	if errors.Is(err, sql.ErrNoRows) {
		return notFound("node", runID+"/"+nodeID)
	}
	if err != nil {
		return err
	}
	if output != nil {
		if err := checkOutputRefTx(ctx, tx, Team(team), runID, nodeID, consumed+1, generation, output); err != nil {
			return err
		}
	}
	res, err := tx.ExecContext(ctx, `
UPDATE nodes
	   SET status = ?, outcome = ?, error = ?, finished_at = ?,
	       failure_reason = ?, exit_code = ?
	 WHERE team = ? AND run_id = ? AND node_id = ? AND NOT (status = ? AND outcome != '')`,
		nodeStatusDone, outcome, errMsg, time.Now().UnixNano(), reason, code,
		string(t.team), runID, nodeID, nodeStatusDone)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 1 {
		if err := writeNodeOutputTx(ctx, tx, Team(team), runID, nodeID, generation, output); err != nil {
			return err
		}
		if err := settleRunningStepsTx(ctx, tx, Team(team), runID, nodeID, outcome); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// safety: a node killed mid-step never writes that step's end, so a step
// still running when its node finishes ends with the node.
func settleRunningStepsTx(ctx context.Context, tx *storeTx, team Team, runID, nodeID, outcome string) error {
	status := StepFailed
	switch outcome {
	case outcomeSuccess:
		status = StepPassed
	case outcomeCancelled:
		status = StepCancelled
	}
	_, err := tx.ExecContext(ctx, `
UPDATE node_steps SET status = ?, finished_at = ?
 WHERE team = ? AND run_id = ? AND node_id = ? AND status = ?`,
		status, time.Now().UnixNano(), team, runID, nodeID, StepRunning)
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
// the same time. Negative inputs and overflowing totals reject the entire observation.
// Zero stays the value every reader treats as absent.
func (s *Store) AddNodeUsage(ctx context.Context, runID, nodeID string, u NodeUsage) (err error) {
	return s.defaultTenant().AddNodeUsage(ctx, runID, nodeID, u)
}

// AddNodeUsage is [Store.AddNodeUsage] confined to t's team; a node of
// another team reads as missing, which is the no-op a missing node gets.
func (t *Tenant) AddNodeUsage(ctx context.Context, runID, nodeID string, u NodeUsage) (err error) {
	if u.CPUTime < 0 || u.Wall < 0 || u.MaxRSSBytes < 0 {
		return errors.New("node usage requires nonnegative CPU time, wall time and peak RSS")
	}
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackUnlessDone(tx, &err)
	var cpu, wall int64
	err = tx.QueryRowContext(ctx, `SELECT cpu_nanos, process_wall_nanos FROM nodes
 WHERE team = ? AND run_id = ? AND node_id = ?`+tx.forUpdate(), string(t.team), runID, nodeID).Scan(&cpu, &wall)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if cpu < 0 || wall < 0 {
		return errors.New("stored node usage has negative CPU or wall time")
	}
	if cpu > math.MaxInt64-int64(u.CPUTime) {
		return errors.New("node CPU time overflow")
	}
	if wall > math.MaxInt64-int64(u.Wall) {
		return errors.New("node wall time overflow")
	}
	_, err = tx.ExecContext(ctx, `
UPDATE nodes
   SET cpu_nanos = cpu_nanos + ?,
       process_wall_nanos = process_wall_nanos + ?,
       max_rss_bytes = CASE WHEN ? > max_rss_bytes THEN ? ELSE max_rss_bytes END
 WHERE team = ? AND run_id = ? AND node_id = ?`,
		int64(u.CPUTime), int64(u.Wall), u.MaxRSSBytes, u.MaxRSSBytes, string(t.team), runID, nodeID)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// ListNodes returns the nodes for a run in insertion order, which the
// nodes.seq column records because no physical row order survives an
// update on Postgres. Rows a pre-v29 Postgres store wrote carry no
// sequence and fall back to node id.
func (s *Store) ListNodes(ctx context.Context, runID string) ([]*Node, error) {
	return s.defaultTenant().ListNodes(ctx, runID)
}

// ListNodes is [Store.ListNodes] confined to t's team.
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
	}
	return out, nil
}

// GetNode fetches a single node row; ErrNotFound when missing.
func (s *Store) GetNode(ctx context.Context, runID, nodeID string) (*Node, error) {
	return s.defaultTenant().GetNode(ctx, runID, nodeID)
}

// GetNode is [Store.GetNode] confined to t's team.
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
	return record, nil
}

const nodeSelectColumns = `run_id, node_id, status, outcome, deps_json, error,
       (SELECT o.key FROM node_outputs o WHERE o.team = nodes.team AND o.run_id = nodes.run_id AND o.node_id = nodes.node_id),
       (SELECT o.size FROM node_outputs o WHERE o.team = nodes.team AND o.run_id = nodes.run_id AND o.node_id = nodes.node_id),
       (SELECT o.sha256 FROM node_outputs o WHERE o.team = nodes.team AND o.run_id = nodes.run_id AND o.node_id = nodes.node_id),
       started_at, finished_at,
       ready_at, claimed_by, lease_expires_at, needs_labels, prefers_labels,
       requested_cores, requested_memory_bytes, requested_slots,
       offer_started_at, offer_priority_target, claim_base_priority, claim_priority,
       claim_worker_id, claim_executor_kind, claim_reservation_id,
	   status_detail, last_heartbeat, failure_reason, exit_code, annotations_json, summary,
	   artifact_manifest, cpu_nanos, max_rss_bytes, process_wall_nanos,
	   coordinator_id, claim_generation, claim_membership_id,
	   executor_kind, executor_id, executor_location, execution_started_at, reservation_id,
	   attempts_consumed, retry_root_run_id,
	   placement_reason, placement_hold_from, credit_cpu_class, spec_hash, generated_json`

func scanNodeRow(rs rowScanner, n *Node) error {
	var depsJSON, labelsJSON, prefersJSON, annotationsJSON []byte
	var outputKey, outputSHA sql.NullString
	var outputSize sql.NullInt64
	var generatedJSON []byte
	var startedNS, finishedNS, readyNS, leaseNS, offerStartedNS, heartbeatNS, executionStartedNS sql.NullInt64
	var holdFromNS sql.NullInt64
	var claimedBy sql.NullString
	var exitCode sql.NullInt64
	err := rs.Scan(&n.RunID, &n.NodeID, &n.Status, &n.Outcome,
		&depsJSON, &n.Error, &outputKey, &outputSize, &outputSHA, &startedNS, &finishedNS,
		&readyNS, &claimedBy, &leaseNS, &labelsJSON, &prefersJSON,
		&n.RequestedCores, &n.RequestedMemoryBytes, &n.RequestedSlots,
		&offerStartedNS, &n.OfferPriorityTarget, &n.ClaimBasePriority, &n.ClaimPriority,
		&n.ClaimWorkerID, &n.ClaimExecutorKind, &n.ClaimReservationID,
		&n.StatusDetail, &heartbeatNS,
		&n.FailureReason, &exitCode, &annotationsJSON, &n.Summary, &n.ArtifactManifest,
		&n.CPUNanos, &n.MaxRSSBytes, &n.ProcessWallNanos,
		&n.CoordinatorID, &n.ClaimGeneration, &n.ClaimMembershipID,
		&n.ExecutorKind, &n.ExecutorID, &n.ExecutorLocation, &executionStartedNS, &n.ReservationID,
		&n.AttemptsConsumed, &n.RetryRootRunID,
		&n.PlacementReason, &holdFromNS, &n.CreditCPUClassCores, &n.SpecHash, &generatedJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return notFound("node", "")
	}
	if err != nil {
		return err
	}
	_ = json.Unmarshal(depsJSON, &n.Deps)
	if len(generatedJSON) > 0 {
		n.Generated = &GeneratedNode{}
		if err := json.Unmarshal(generatedJSON, n.Generated); err != nil {
			return fmt.Errorf("node %s generated spec: %w", n.NodeID, err)
		}
	}
	if outputKey.Valid {
		n.OutputRef = &OutputRef{Key: outputKey.String, Size: outputSize.Int64, SHA256: outputSHA.String}
	}
	if len(labelsJSON) > 0 {
		_ = json.Unmarshal(labelsJSON, &n.NeedsLabels)
	}
	if len(prefersJSON) > 0 {
		_ = json.Unmarshal(prefersJSON, &n.PrefersLabels)
	}
	n.CreditCPUClassMemoryBytes = CPUClassMemoryBytes(n.CreditCPUClassCores)
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
	if holdFromNS.Valid {
		t := time.Unix(0, holdFromNS.Int64)
		n.PlacementHoldFrom = &t
	}
	if offerStartedNS.Valid {
		t := time.Unix(0, offerStartedNS.Int64)
		n.OfferStartedAt = &t
	}
	if claimedBy.Valid {
		n.ClaimedBy = claimedBy.String
		n.Claimed = true
	}
	if leaseNS.Valid {
		t := time.Unix(0, leaseNS.Int64)
		n.LeaseExpiresAt = &t
	}
	if executionStartedNS.Valid {
		t := time.Unix(0, executionStartedNS.Int64)
		n.ExecutionStartedAt = &t
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
// state for the inner-Work DAG. Status moves from running to passed, failed, or
// cancelled once; skipped is terminal at insert.
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

// ListNodeSteps returns persisted step observations for a tenant run.
func (t *Tenant) ListNodeSteps(ctx context.Context, runID string) ([]*NodeStep, error) {
	rows, err := t.s.query(ctx, `
SELECT node_id, step_id, status, started_at, finished_at, annotations_json, summary
FROM node_steps
WHERE team = ? AND run_id = ?
ORDER BY node_id, started_at`, string(t.team), runID)
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

// MarkNodeReady publishes a job to its team's queue.
func (s *Store) MarkNodeReady(ctx context.Context, runID, nodeID string) error {
	t, err := s.runTenant(ctx, runID)
	if err != nil {
		return err
	}
	return t.MarkNodeReady(ctx, runID, nodeID)
}

// MarkNodeReady publishes a job to the tenant's queue.
func (t *Tenant) MarkNodeReady(ctx context.Context, runID, nodeID string) error {
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackOrLog(tx)
	node := &Node{}
	if err := scanNodeRow(tx.QueryRowContext(ctx, `SELECT `+nodeSelectColumns+`
 FROM nodes WHERE team = ? AND run_id = ? AND node_id = ?`+tx.forUpdate(), string(t.team), runID, nodeID), node); err != nil {
		return err
	}
	charge, err := nodeChargeTx(ctx, tx, runID, nodeID)
	if err != nil {
		return err
	}
	class, err := readyCPUClassTx(ctx, tx, charge)
	if err != nil {
		return err
	}
	now := tx.now().UnixNano()
	if _, err := tx.ExecContext(ctx, `UPDATE nodes SET ready_at=COALESCE(ready_at,?), placement_hold_from=COALESCE(placement_hold_from,?), credit_cpu_class=? WHERE team=? AND run_id=? AND node_id=?`, now, now, class, t.team, runID, nodeID); err != nil {
		return err
	}
	return tx.Commit()
}

// safety: the class a node becomes ready at is stamped on its row so the queue
// read can leave the classes a warm runner may not have out of the scan window
// entirely. A class no table prices stamps zero, which keeps the node scannable
// so the claim that prices it fails it with the class to add.
func readyCPUClassTx(ctx context.Context, tx *storeTx, charge ExecutorResource) (int64, error) {
	table, err := creditRateTableTx(ctx, tx)
	if err != nil {
		return 0, err
	}
	class, err := table.ClassForResource(charge)
	if err != nil {
		if errors.Is(err, ErrUnpricedCPUClass) {
			return 0, nil
		}
		return 0, err
	}
	return class.Cores, nil
}

// safety: a rollback after a successful commit reports ErrTxDone, which is the
// ordinary path; any other failure lost a cleanup and is worth a line.
func rollbackOrLog(tx *storeTx) {
	if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		slog.Warn("rolling back a store transaction failed", "err", err)
	}
}

// safety: closes the cursor on an early return, where the caller's own Close
// never runs; a second close after that one reports nothing.
func closeRowsOrLog(rows *sql.Rows) {
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

// ListEventsAfter returns events with seq > afterSeq, ascending.
// Pass 0 for full backlog; empty slice when there's nothing new.
func (s *Store) ListEventsAfter(ctx context.Context, runID string, afterSeq int64, limit int) ([]Event, error) {
	return s.defaultTenant().ListEventsAfter(ctx, runID, afterSeq, limit)
}

// ListEventsAfter is [Store.ListEventsAfter] confined to t's team.
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

// safety: holding the run row serializes callers that allocate a per-run
// sequence number from MAX(seq)+1. NO KEY UPDATE leaves foreign-key checks
// on sibling nodes free to proceed while those nodes await their event turn.
// A missing run leaves nothing to lock; the insert then fails its foreign key.
// AppendEvent writes the run's next event in order.
func (s *Store) AppendEvent(ctx context.Context, runID, nodeID, kind string, payload []byte) error {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := appendRunEventTx(ctx, tx, runID, nodeID, kind, payload, time.Now()); err != nil {
		return err
	}
	return tx.Commit()
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

// safety: a caller without a tenant handle appends into its run's own team, the team the run's
// other rows were written into.
func appendRunEventTx(ctx context.Context, tx *storeTx, runID, nodeID, kind string, payload any, at time.Time) (int64, error) {
	team, err := creditTeamForRunTx(ctx, tx, runID)
	if err != nil {
		return 0, err
	}
	return appendEventTx(ctx, tx, team, runID, nodeID, kind, payload, at)
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

// GetActiveDebugPause returns the open pause for a node of the default team, if any.
func (s *Store) GetActiveDebugPause(ctx context.Context, runID, nodeID string) (*DebugPause, error) {
	return s.defaultTenant().GetActiveDebugPause(ctx, runID, nodeID)
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
	Error          string            `json:"error,omitempty"`
	CreatedAt      time.Time         `json:"created_at"`
	ClaimedAt      *time.Time        `json:"claimed_at,omitempty"`
	LeaseExpiresAt *time.Time        `json:"lease_expires_at,omitempty"`
	// ParentRunID: spawning RunAndAwait; for cycle detection.
	ParentRunID string `json:"parent_run_id,omitempty"`
	// Mirror of Run repo fields; threaded into CreateRun.
	Repo         string `json:"repo,omitempty"`
	RepoURL      string `json:"repo_url,omitempty"`
	GithubOwner  string `json:"github_owner,omitempty"`
	GithubRepo   string `json:"github_repo,omitempty"`
	GithubRepoID int64  `json:"-"`
	// RepoInherited distinguishes an implicit same-repository await from an
	// explicit cross-repository request after parent provenance is copied.
	RepoInherited bool `json:"repo_inherited,omitempty"`
	// RetryOf is threaded into the persisted Run row.
	RetryOf string `json:"retry_of,omitempty"`
	// RetrySource is "manual" for a retried run.
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
	// ClaimToken is set only on the answer to a runner's trigger claim: the
	// claim token that reads the run's declared secrets while the claim
	// lives.
	ClaimToken string `json:"claim_token,omitempty"`
	// WebhookDelivery records the provider's delivery header for audit.
	// One delivery can start several pipelines. WebhookReplayKey owns deduplication.
	WebhookDelivery string `json:"webhook_delivery,omitempty"`
	// WebhookReplayKey identifies verified delivery material and its pipeline.
	// It is unique within the team, regardless of the unsigned delivery header.
	WebhookReplayKey string `json:"webhook_replay_key,omitempty"`
	// Team owns the trigger and the run it becomes. A claim and a read
	// return it so the runner that executes the run can place its work
	// on nodes no other team's work shares.
	Team Team `json:"team,omitempty"`
	// RunnerPin is the prefix of the one-shot credential a
	// `--runner here` trigger is pinned to, empty for every other trigger.
	// A pinned trigger is claimable only by that credential, once.
	RunnerPin string `json:"-"`
}

// DefaultLeaseDuration is the claim lease TTL. Wide enough to survive
// CPU-bound pauses; short enough to re-queue dead runners.
const DefaultLeaseDuration = 3 * time.Minute

// MaxLeaseDuration caps the lease a claimant may ask for. The claim is
// an authorization window, so the claimant must not pick how long its
// own access lasts; longer requests are clamped, not rejected, because
// a heartbeat renews well inside the cap.
const MaxLeaseDuration = 10 * time.Minute

// ClaimIdentity is the token a node claim answers to. TokenPrefix is
// unique per token and is what the ownership predicates match on;
// Principal is the display label, which two tokens may share.
type ClaimIdentity struct {
	Principal   string
	TokenPrefix string
}

func createTriggerTx(ctx context.Context, tx *storeTx, team Team, t Trigger) error {
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
	_, err := tx.ExecContext(
		ctx, `
INSERT INTO triggers (team, id, pipeline, args_json, trigger_source, trigger_user,
                      trigger_env, git_branch, git_sha, status, created_at, parent_run_id,
		              repo, repo_url, github_owner, github_repo, github_repo_id, repo_inherited, retry_of, retry_source, parent_node_id, "full",
		              idempotency_key, webhook_delivery, webhook_replay_key, runner_pin)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		string(team), t.ID, t.Pipeline, argsJSON, t.TriggerSource, t.TriggerUser,
		envJSON, t.GitBranch, t.GitSHA, status, t.CreatedAt.UnixNano(), parent,
		t.Repo, t.RepoURL, t.GithubOwner, t.GithubRepo, t.GithubRepoID, repoInheritedInt, t.RetryOf, t.RetrySource, t.ParentNodeID, fullInt,
		t.IdempotencyKey, t.WebhookDelivery, t.WebhookReplayKey, t.RunnerPin,
	)
	if err != nil && isUniqueViolation(err) {
		if t.WebhookReplayKey != "" && strings.Contains(err.Error(), triggerWebhookReplayKeyColumn) {
			return fmt.Errorf("%w: replay key %q", ErrDuplicateWebhookDelivery, t.WebhookReplayKey)
		}
		if t.IdempotencyKey != "" && strings.Contains(err.Error(), "idempotency_key") {
			return fmt.Errorf("%w: idempotency key %q", ErrDuplicateIdempotencyKey, t.IdempotencyKey)
		}
	}
	return err
}

// ErrDuplicateIdempotencyKey reports that a trigger insert lost the race
// to another submission carrying the same idempotency key. It is not a
// failure for the caller to report: the winning trigger is the answer,
// and the caller resolves it with [Tenant.FindTriggerByIdempotencyKey].
var ErrDuplicateIdempotencyKey = errors.New("store: idempotency key already claimed by another trigger")

// ErrDuplicateWebhookDelivery reports a signed replay key already accepted
// by a trigger in the same team. The caller returns that original run.
var ErrDuplicateWebhookDelivery = errors.New("store: webhook delivery already accepted")

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

// GetTrigger returns one tenant trigger.
func (t *Tenant) GetTrigger(ctx context.Context, id string) (*Trigger, error) {
	var tr Trigger
	var argsJSON, envJSON []byte
	var createdNS int64
	var claimedNS, leaseNS sql.NullInt64
	var parent sql.NullString
	var fullInt, repoInheritedInt int
	err := t.s.queryRow(
		ctx, `
SELECT id, pipeline, args_json, trigger_source, trigger_user,
       trigger_env, git_branch, git_sha, status, created_at, claimed_at, lease_expires_at,
       repo, repo_url, github_owner, github_repo, github_repo_id, repo_inherited, retry_of, retry_source, parent_node_id, parent_run_id, "full",
       idempotency_key, claim_seq, webhook_delivery, team,
       COALESCE((SELECT error FROM runs WHERE runs.team = triggers.team AND runs.id = triggers.id), '')
  FROM triggers WHERE team = ? AND id = ?`, string(t.team), id,
	).Scan(&tr.ID, &tr.Pipeline, &argsJSON, &tr.TriggerSource, &tr.TriggerUser,
		&envJSON, &tr.GitBranch, &tr.GitSHA, &tr.Status, &createdNS, &claimedNS, &leaseNS,
		&tr.Repo, &tr.RepoURL, &tr.GithubOwner, &tr.GithubRepo, &tr.GithubRepoID, &repoInheritedInt, &tr.RetryOf, &tr.RetrySource, &tr.ParentNodeID, &parent, &fullInt,
		&tr.IdempotencyKey, &tr.ClaimSeq, &tr.WebhookDelivery, &tr.Team, &tr.Error)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, notFound("trigger", id)
		}
		return nil, err
	}
	tr.CreatedAt = time.Unix(0, createdNS)
	if claimedNS.Valid {
		ct := time.Unix(0, claimedNS.Int64)
		tr.ClaimedAt = &ct
	}
	if leaseNS.Valid {
		lt := time.Unix(0, leaseNS.Int64)
		tr.LeaseExpiresAt = &lt
	}
	if parent.Valid {
		tr.ParentRunID = parent.String
	}
	tr.Full = fullInt != 0
	tr.RepoInherited = repoInheritedInt != 0
	if len(argsJSON) > 0 {
		if err := json.Unmarshal(argsJSON, &tr.Args); err != nil {
			tr.Args = nil
		}
	}
	if len(envJSON) > 0 {
		if err := json.Unmarshal(envJSON, &tr.TriggerEnv); err != nil {
			tr.TriggerEnv = nil
		}
	}
	return &tr, nil
}

// TriggerFilter narrows ListTriggers; zero value matches all.
type TriggerFilter struct {
	Statuses  []string // "pending"|"claimed"|"done"|"failed"
	Pipelines []string
	Repo      string // matches GITHUB_REPOSITORY in trigger_env
	Limit     int    // <=0 = 20
}

const (
	triggerRepoScanBatch = 200
	triggerRepoScanCap   = 5000
)

// ListTriggers returns t's triggers newest-first, filtered by f, with the
// same paging and repo matching as [Tenant.ListTriggers].
func (t *Tenant) ListTriggers(ctx context.Context, f TriggerFilter) ([]*Trigger, error) {
	return t.s.listTriggers(ctx, oneTeam(t.team), f)
}

func (s *Store) listTriggers(ctx context.Context, scope teamScope, f TriggerFilter) ([]*Trigger, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 20
	}
	limit = min(limit, MaxRunListLimit)

	where := ""
	args := []any{}
	addClause := func(clause string) {
		if where == "" {
			where = " WHERE " + clause
		} else {
			where += " AND " + clause
		}
	}
	if !scope.all {
		if scope.team == "" {
			return nil, ErrNoTeam
		}
		addClause("team = ?")
		args = append(args, string(scope.team))
	}
	addIn := func(col string, values []string) {
		if len(values) == 0 {
			return
		}
		placeholders := make([]string, len(values))
		for i, v := range values {
			placeholders[i] = "?"
			args = append(args, v)
		}
		addClause(col + " IN (" + strings.Join(placeholders, ",") + ")")
	}
	addIn("status", f.Statuses)
	addIn("pipeline", f.Pipelines)

	selectTriggers := `
SELECT id, pipeline, args_json, trigger_source, trigger_user,
       trigger_env, git_branch, git_sha, status, created_at,
       claimed_at, lease_expires_at, parent_run_id,
       repo, repo_url, github_owner, github_repo, repo_inherited, retry_of, retry_source, parent_node_id, "full",
       idempotency_key, claim_seq, webhook_delivery,
       COALESCE((SELECT error FROM runs WHERE runs.id = triggers.id), '')
  FROM triggers`
	const orderAndLimit = `
 ORDER BY created_at DESC, id DESC
 LIMIT ?`

	batch := limit
	if f.Repo != "" {
		batch = max(limit, triggerRepoScanBatch)
	}
	var out []*Trigger
	var cursor *Trigger
	scanned := 0
	for {
		pageWhere, pageArgs := where, append([]any{}, args...)
		if cursor != nil {
			cursorNS := cursor.CreatedAt.UnixNano()
			clause := "(created_at < ? OR (created_at = ? AND id < ?))"
			if pageWhere == "" {
				pageWhere = " WHERE " + clause
			} else {
				pageWhere += " AND " + clause
			}
			pageArgs = append(pageArgs, cursorNS, cursorNS, cursor.ID)
		}
		pageArgs = append(pageArgs, batch)
		page, err := s.listTriggerPage(ctx, selectTriggers+pageWhere+orderAndLimit, pageArgs)
		if err != nil {
			return nil, err
		}
		for _, t := range page {
			if f.Repo != "" && t.TriggerEnv["GITHUB_REPOSITORY"] != f.Repo {
				continue
			}
			out = append(out, t)
			if len(out) == limit {
				return out, nil
			}
		}
		scanned += len(page)
		if f.Repo == "" || len(page) < batch {
			return out, nil
		}
		if scanned >= triggerRepoScanCap {
			slog.Warn("trigger repo filter stopped at its search horizon",
				"repo", f.Repo, "scanned", scanned, "matched", len(out), "limit", limit)
			return out, nil
		}
		cursor = page[len(page)-1]
	}
}

func (s *Store) listTriggerPage(ctx context.Context, query string, args []any) ([]*Trigger, error) {
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
			&t.IdempotencyKey, &t.ClaimSeq, &t.WebhookDelivery, &t.Error); err != nil {
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
const expectedSchemaVersion = 3

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

const triggerWebhookReplayKeyColumn = "webhook_replay_key"

// TokenPrefixIndexName is the unique index enforcing one token row per
// prefix.
const TokenPrefixIndexName = "idx_tokens_prefix" // #nosec G101 -- an index name, not a credential

const tokenPrefixColumn = "prefix"

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
		return fmt.Errorf("sparkwing: this store predates the baseline (it records schema version %d, this build "+
			"creates version %d and upgrades none); %s and start again", version, expectedSchemaVersion, remedy)
	}
	if s.dialect == DialectPostgres {
		var translated string
		translated, err = TranslateSchema(schemaNeutral, s.dialect)
		if err == nil {
			err = execStatements(ctx, tx, translated)
		}
	} else {
		_, err = tx.ExecContext(ctx, schemaNeutral)
	}
	if err != nil {
		return fmt.Errorf("create baseline schema: %w", err)
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
