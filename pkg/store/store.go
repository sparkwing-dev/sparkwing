// Package store persists pipeline-run state to a SQL database. SQLite
// (modernc.org/sqlite) and Postgres (jackc/pgx via the stdlib driver)
// are both supported behind the same *Store type; the dialect is
// chosen at Open time.
package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/hex"
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
	"strconv"
	"strings"
	"sync"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"

	"github.com/sparkwing-dev/sparkwing/pkg/match"
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
	planSecretOpener PlanSecretOpener
	delegation       delegationContracts
	provisions       ProvisionPolicy
	sourcePolicy     sourceReleaseContracts
	clock            func() time.Time
	db               *sql.DB
	dialect          Dialect
	cleanup          func() error
	csrfKeyMu        sync.Mutex
	csrfKey          []byte
	csrfCipher       SessionKeyCipher
	launchResumeMu   sync.Mutex
	launchResume     *launchCursor
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

// Annotation bounds. Every node and step annotation is also appended to its
// run's list, so that list is where the per-run bounds hold.
const (
	MaxAnnotationBytes    = 64 << 10
	MaxRunAnnotations     = 1000
	MaxRunAnnotationBytes = 4 << 20
)

var (
	// ErrAnnotationTooLarge refuses one annotation over [MaxAnnotationBytes].
	ErrAnnotationTooLarge = errors.New("store: an annotation holds at most 64 KiB")
	// ErrRunAnnotationLimit refuses an annotation past [MaxRunAnnotations].
	ErrRunAnnotationLimit = errors.New("store: a run holds at most 1000 annotations")
	// ErrRunAnnotationBytes refuses an annotation that would take the run's
	// JSON-encoded annotation list past [MaxRunAnnotationBytes].
	ErrRunAnnotationBytes = errors.New("store: a run's annotations total at most 4 MiB")
)

// perf: every append rereads and rewrites the run's whole list, and the
// node's or step's, so a run's annotations cost O(n^2) bytes written; the
// bounds above cap that at about 1000 rewrites of at most 4 MiB each.
func appendRunAnnotation(tx *storeTx, team Team, runID, msg string) error {
	if len(msg) > MaxAnnotationBytes {
		return ErrAnnotationTooLarge
	}
	var blob []byte
	err := tx.QueryRow(
		`SELECT annotations_json FROM runs WHERE team = ? AND id = ?`+tx.forUpdate(), string(team), runID).Scan(&blob)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var list []string
	if len(blob) > 0 {
		if err := json.Unmarshal(blob, &list); err != nil {
			return fmt.Errorf("read run %s annotations: %w", runID, err)
		}
	}
	if len(list) >= MaxRunAnnotations {
		return ErrRunAnnotationLimit
	}
	list = append(list, msg)
	next, err := json.Marshal(list)
	if err != nil {
		return err
	}
	if len(next) > MaxRunAnnotationBytes {
		return ErrRunAnnotationBytes
	}
	_, err = tx.Exec(`UPDATE runs SET annotations_json = ? WHERE team = ? AND id = ?`, next, string(team), runID)
	return err
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
	// the RunAdmission constants; it is empty for any other run.
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
	// LastHeartbeatAt is the most recent run-level liveness ping from
	// the dispatching orchestrator. NULL for rows that predate the
	// column or come from backends that don't ping it (local + S3
	// modes use per-node heartbeats for orphan detection instead).
	LastHeartbeatAt *time.Time `json:"last_heartbeat_at,omitempty"`
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
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackOrLog(tx)
	// safety: this method has not moved to Tenant yet, so it writes the
	// team the column's default writes; a run it created landing in any
	// other team would be invisible to the install that asked for it.
	if err := s.createRunTx(ctx, tx, DefaultTeam, r); err != nil {
		return err
	}
	return tx.Commit()
}

// CreateTriggerWithRun writes a trigger and the pending run it names in one
// transaction, so a guard that refuses the run leaves no trigger behind for a
// worker to claim. It maps the same duplicate-key errors [Store.CreateTrigger]
// does.
func (s *Store) CreateTriggerWithRun(ctx context.Context, t Trigger, r Run) error {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackOrLog(tx)
	if err := createTriggerTx(ctx, tx, DefaultTeam, t); err != nil {
		return err
	}
	if err := s.createRunTx(ctx, tx, DefaultTeam, r); err != nil {
		return err
	}
	if err := routeRunDispatchTx(ctx, tx, DefaultTeam, t, time.Now()); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) createRunTx(ctx context.Context, tx *storeTx, team Team, r Run) error {
	if err := ValidateRunInvocation(r); err != nil {
		return err
	}
	if r.FinishedAt != nil && !isTerminalRunStatus(r.Status) {
		return fmt.Errorf("%w: a non-terminal run cannot have finished_at", ErrInvalidInput)
	}
	if err := s.assertRunMutationFenceTx(ctx, tx, team, r.ID); err != nil {
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
	if r.StartedAt.IsZero() {
		r.StartedAt = time.Now()
	}
	created := r.CreatedAt
	if created.IsZero() {
		created = r.StartedAt
	}
	var heartbeat sql.NullInt64
	if r.Status == runStatusRunning {
		heartbeat = sql.NullInt64{Int64: time.Now().UnixNano(), Valid: true}
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
INSERT INTO runs (team, id, pipeline, status, trigger_source, git_branch, git_sha, args_json, plan_json, created_at, started_at, finished_at, parent_run_id, declared_repo, repo_url, github_owner, github_repo, retry_of, retried_as, retry_source, replay_of_run_id, replay_of_node_id, invocation_json, last_heartbeat_at, created_principal)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
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
    last_heartbeat_at = COALESCE(excluded.last_heartbeat_at, runs.last_heartbeat_at),
    created_principal = CASE WHEN runs.created_principal = '' THEN excluded.created_principal ELSE runs.created_principal END
WHERE runs.team = excluded.team AND runs.status = '`+runStatusPending+`' AND runs.finished_at IS NULL`,
		string(team), r.ID, r.Pipeline, r.Status, r.TriggerSource, r.GitBranch, r.GitSHA,
		argsJSON, r.PlanSnapshot, created.UnixNano(), r.StartedAt.UnixNano(), finished, parent,
		r.DeclaredRepo, r.RepoURL, r.GithubOwner, r.GithubRepo,
		r.RetryOf, r.RetriedAs, r.RetrySource,
		r.ReplayOfRunID, r.ReplayOfNodeID,
		invocationJSON, heartbeat, creatingPrincipal(ctx),
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

// FinishRun marks a run terminal with the given status and optional error.
func (s *Store) FinishRun(ctx context.Context, runID, status, errMsg string) error {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.assertRunMutationFenceTx(ctx, tx, DefaultTeam, runID); err != nil {
		return err
	}
	if err := finishRunOnceTx(ctx, tx, runID, status, errMsg, DefaultTeam); err != nil {
		return err
	}
	return tx.Commit()
}

// FinishRunIfActive records a terminal outcome once and reports whether it
// committed the transition. See [Tenant.FinishRunIfActive].
func (s *Store) FinishRunIfActive(ctx context.Context, runID, status, errMsg string) (changed bool, err error) {
	return s.defaultTenant().FinishRunIfActive(ctx, runID, status, errMsg)
}

// FinishRunsIfActive atomically finalizes the named non-terminal default-team runs. A
// failure rolls back every member, so one shared lease cannot be partly
// cancelled.
func (s *Store) FinishRunsIfActive(ctx context.Context, runIDs []string, status, errMsg string) error {
	return s.defaultTenant().FinishRunsIfActive(ctx, runIDs, status, errMsg)
}

// TouchRunHeartbeat stamps last_heartbeat_at=now for the run row. The
// dispatching orchestrator calls this on a ticker while the run is
// active so the controller's reaper can detect a fully-orphaned run
// (laptop closed, network gone, process killed) and flip it to
// failed instead of leaving status='running' forever.
func (s *Store) TouchRunHeartbeat(ctx context.Context, runID string) error {
	return s.defaultTenant().TouchRunHeartbeat(ctx, runID)
}

// UpdatePlanSnapshot replaces the stored plan JSON for a run.
func (s *Store) UpdatePlanSnapshot(ctx context.Context, runID string, snapshot []byte) error {
	return s.updatePlanSnapshot(ctx, DefaultTeam, runID, snapshot)
}

// UpdatePlanSnapshot records the plan of one of t's runs. The trigger fence a
// runner's request carries is checked in t's team, so an orchestrator of any
// team can record the plan of the run it holds.
func (t *Tenant) UpdatePlanSnapshot(ctx context.Context, runID string, snapshot []byte) error {
	return t.s.updatePlanSnapshot(ctx, t.team, runID, snapshot)
}

func (s *Store) updatePlanSnapshot(ctx context.Context, team Team, runID string, snapshot []byte) error {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.assertRunMutationFenceTx(ctx, tx, team, runID); err != nil {
		return err
	}
	var accepted int64
	err = tx.QueryRowContext(ctx, `SELECT plan_accepted_generation FROM runs WHERE team = ? AND id = ?`+tx.forNoKeyUpdate(),
		string(team), runID).Scan(&accepted)
	if errors.Is(err, sql.ErrNoRows) {
		return notFound("run", runID)
	}
	if err != nil {
		return err
	}
	if accepted != 0 {
		return ErrPlanAccepted
	}
	sum := sha256.Sum256(snapshot)
	planHash := "sha256:" + hex.EncodeToString(sum[:])
	if _, err := tx.ExecContext(ctx, `INSERT INTO run_definition_plans (team, run_id, plan_hash)
SELECT team, id, ? FROM runs WHERE team = ? AND id = ?
ON CONFLICT(run_id) DO UPDATE SET plan_hash = run_definition_plans.plan_hash
 WHERE run_definition_plans.team = excluded.team`, planHash, string(team), runID); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `UPDATE runs SET plan_json = ?, claim_wait_ns = ? WHERE team = ? AND id = ?`,
		snapshot, planClaimWait(snapshot), string(team), runID)
	if err != nil {
		return err
	}
	changed, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return notFound("run", runID)
	}
	return tx.Commit()
}

// SetRetriedAs stores the reverse retry pointer within the tenant.
func (t *Tenant) SetRetriedAs(ctx context.Context, runID, newID string) error {
	_, err := t.s.exec(ctx,
		`UPDATE runs SET retried_as = ? WHERE team = ? AND id = ?`, newID, string(t.team), runID)
	return err
}

// safety: every run read shares this list, because a column added to runs
// has to reach each of them and scanRun together or the scan misaligns.
const runColumns = `id, pipeline, status, trigger_source, git_branch, git_sha, args_json, plan_json, error, created_at, started_at, finished_at, parent_run_id, declared_repo, repo_url, github_owner, github_repo, retry_of, retried_as, retry_source, replay_of_run_id, replay_of_node_id, invocation_json, annotation_count, top_annotation, annotations_json, last_heartbeat_at, admission, ` + runAttentionColumn

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
// The raw method remains part of pkg/storage.StateStoreOld.
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

// CountRuns returns how many runs match f, ignoring its Limit.
//
// Scope: this counts every team. See [Store.ListRuns].
func (s *Store) CountRuns(ctx context.Context, f RunFilter) (int, error) {
	return s.countRuns(ctx, allTeams(), f)
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

func (s *Store) getLatestRun(ctx context.Context, scope teamScope, pipeline string, statuses []string, maxAge time.Duration) (*Run, error) {
	if pipeline == "" {
		return nil, errors.New("GetLatestRun: pipeline is required")
	}
	where := "WHERE pipeline = ?"
	args := []any{pipeline}
	if !scope.all {
		if scope.team == "" {
			return nil, ErrNoTeam
		}
		where += " AND team = ?"
		args = append(args, string(scope.team))
	}
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
SELECT ` + runColumns + `
  FROM runs ` + where + `
 ORDER BY started_at DESC
 LIMIT 1`
	return scanRun(s.queryRow(ctx, q, args...))
}

// DeleteRun removes the run, its trigger and its memo entries;
// CASCADE handles children.
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
	return s.defaultTenant().DeleteRun(ctx, runID)
}

// DeleteRun is [Store.DeleteRun] confined to t's team.
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
	var finishedNS, heartbeatNS sql.NullInt64
	var parent sql.NullString
	err := rs.Scan(&r.ID, &r.Pipeline, &r.Status, &r.TriggerSource,
		&r.GitBranch, &r.GitSHA, &argsJSON, &planJSON, &r.Error,
		&createdNS, &startedNS, &finishedNS, &parent,
		&r.DeclaredRepo, &r.RepoURL, &r.GithubOwner, &r.GithubRepo,
		&r.RetryOf, &r.RetriedAs, &r.RetrySource,
		&r.ReplayOfRunID, &r.ReplayOfNodeID, &invocationJSON,
		&r.AnnotationCount, &r.TopAnnotation, &annotationsJSON,
		&heartbeatNS, &r.Admission, &r.NeedsAttention)
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
	if err := s.assertRunMutationFenceTx(ctx, tx, team, n.RunID); err != nil {
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
	return s.defaultTenant().StartNode(ctx, runID, nodeID)
}

// StartNode is [Store.StartNode] confined to t's team.
func (t *Tenant) StartNode(ctx context.Context, runID, nodeID string) error {
	res, fenced, err := t.execNodeMutation(ctx, runID, nodeID, `
UPDATE nodes SET status = ?, started_at = ?
 WHERE team = ? AND run_id = ? AND node_id = ? AND status != ?`,
		nodeStatusRunning, time.Now().UnixNano(), string(t.team), runID, nodeID, nodeStatusDone)
	if err != nil {
		return err
	}
	return fencedRows(res, fenced)
}

// SetNodeStatus updates only the status column.
func (s *Store) SetNodeStatus(ctx context.Context, runID, nodeID, status string) error {
	return s.defaultTenant().SetNodeStatus(ctx, runID, nodeID, status)
}

// SetNodeStatus is [Store.SetNodeStatus] confined to t's team.
func (t *Tenant) SetNodeStatus(ctx context.Context, runID, nodeID, status string) error {
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := t.assertNodeMutationTx(ctx, tx, runID, nodeID); err != nil {
		return err
	}
	var found int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM nodes WHERE team = ? AND run_id = ? AND node_id = ?`+tx.forUpdate(),
		string(t.team), runID, nodeID).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `UPDATE nodes SET status = ? WHERE team = ? AND run_id = ? AND node_id = ?`,
		status, string(t.team), runID, nodeID)
	if err != nil {
		return err
	}
	if err := fencedRows(res, hasClaimFence(ctx)); err != nil {
		return err
	}
	return tx.Commit()
}

// UpdateNodeDeps rewrites a node's stored dependency list.
func (s *Store) UpdateNodeDeps(ctx context.Context, runID, nodeID string, deps []string) error {
	return s.defaultTenant().UpdateNodeDeps(ctx, runID, nodeID, deps)
}

// UpdateNodeDeps is [Store.UpdateNodeDeps] confined to t's team.
func (t *Tenant) UpdateNodeDeps(ctx context.Context, runID, nodeID string, deps []string) error {
	depsJSON, _ := json.Marshal(deps)
	res, fenced, err := t.execNodeMutation(ctx, runID, nodeID,
		`UPDATE nodes SET deps_json = ?
		  WHERE team = ? AND run_id = ? AND node_id = ? AND ready_at IS NULL`,
		depsJSON, string(t.team), runID, nodeID)
	if err != nil {
		return err
	}
	if affected, err := res.RowsAffected(); err != nil {
		return err
	} else if affected != 1 {
		return ErrLockHeld
	}
	return fencedRows(res, fenced)
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

// FinishNodeWithOutputRef finishes a node whose output, when there is one,
// is already a committed object named by ref.
func (s *Store) FinishNodeWithOutputRef(ctx context.Context, runID, nodeID, outcome, errMsg string, output *OutputRef, reason string, exitCode *int) error {
	return s.defaultTenant().FinishNodeWithOutputRef(ctx, runID, nodeID, outcome, errMsg, output, reason, exitCode)
}

// FinishNodeWithOutputRef is [Store.FinishNodeWithOutputRef] confined to t's team.
func (t *Tenant) FinishNodeWithOutputRef(ctx context.Context, runID, nodeID, outcome, errMsg string, output *OutputRef, reason string, exitCode *int) error {
	return t.finishNode(ctx, runID, nodeID, outcome, errMsg, output, nil, reason, exitCode)
}

func (t *Tenant) finishNode(ctx context.Context, runID, nodeID, outcome, errMsg string, output *OutputRef, copied *copySource,
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
	if err := t.s.assertNodeMutationFenceTx(ctx, tx, t.team, runID, nodeID); err != nil {
		return err
	}
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
	if copied != nil {
		if output, err = sourceOutputRefTx(ctx, tx, Team(team), copied.run, copied.node); err != nil {
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
	if err := fencedRows(res, hasClaimFence(ctx)); err != nil {
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

// SetNodeArtifactManifest records the content-addressed digest of a
// node's published-artifact manifest. Written before the terminal
// FinishNode flip so a consumer dispatched on completion always sees
// the reference. Empty digest is a no-op-equivalent clear.
func (s *Store) SetNodeArtifactManifest(ctx context.Context, runID, nodeID, manifestDigest string) error {
	return s.defaultTenant().SetNodeArtifactManifest(ctx, runID, nodeID, manifestDigest)
}

// SetNodeArtifactManifest is [Store.SetNodeArtifactManifest] confined to t's team.
func (t *Tenant) SetNodeArtifactManifest(ctx context.Context, runID, nodeID, manifestDigest string) error {
	res, fenced, err := t.execNodeMutation(ctx, runID, nodeID,
		`UPDATE nodes SET artifact_manifest = ? WHERE team = ? AND run_id = ? AND node_id = ?`,
		manifestDigest, string(t.team), runID, nodeID)
	if err != nil {
		return err
	}
	return fencedRows(res, fenced)
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
	if err := t.s.assertNodeMutationFenceTx(ctx, tx, t.team, runID, nodeID); err != nil {
		return err
	}
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
	result, err := tx.ExecContext(ctx, `
UPDATE nodes
   SET cpu_nanos = cpu_nanos + ?,
       process_wall_nanos = process_wall_nanos + ?,
       max_rss_bytes = CASE WHEN ? > max_rss_bytes THEN ? ELSE max_rss_bytes END
 WHERE team = ? AND run_id = ? AND node_id = ?`,
		int64(u.CPUTime), int64(u.Wall), u.MaxRSSBytes, u.MaxRSSBytes, string(t.team), runID, nodeID)
	if err != nil {
		return err
	}
	if err := fencedRows(result, hasClaimFence(ctx)); err != nil {
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
		attempts, err := t.s.ListNodeExecutionAttempts(ctx, runID, n.NodeID)
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
	attempts, err := t.s.ListNodeExecutionAttempts(ctx, runID, nodeID)
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

// AppendNodeAnnotation appends one annotation string to the node's
// annotations list. Implemented as read-modify-write inside a single
// transaction that holds the rows it rewrites, which is what keeps
// concurrent appenders from losing entries: the transaction alone does
// not, since Postgres runs it at READ COMMITTED.
func (s *Store) AppendNodeAnnotation(ctx context.Context, runID, nodeID, msg string) error {
	return s.defaultTenant().AppendNodeAnnotation(ctx, runID, nodeID, msg)
}

// AppendNodeAnnotation is [Store.AppendNodeAnnotation] confined to t's team.
func (t *Tenant) AppendNodeAnnotation(ctx context.Context, runID, nodeID, msg string) error {
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := t.assertNodeMutationTx(ctx, tx, runID, nodeID); err != nil {
		return err
	}
	var current []byte
	row := tx.QueryRowContext(ctx,
		`SELECT annotations_json FROM nodes WHERE team = ? AND run_id = ? AND node_id = ?`+tx.forUpdate(),
		string(t.team), runID, nodeID)
	if err := row.Scan(&current); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return notFound("node", runID+"/"+nodeID)
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
		`UPDATE nodes SET annotations_json = ? WHERE team = ? AND run_id = ? AND node_id = ?`,
		next, string(t.team), runID, nodeID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE runs SET annotation_count = annotation_count + 1, top_annotation = ? WHERE team = ? AND id = ?`,
		msg, string(t.team), runID); err != nil {
		return err
	}
	if err := appendRunAnnotation(tx, t.team, runID, msg); err != nil {
		return err
	}
	return tx.Commit()
}

// SetNodeSummary replaces the node's markdown summary with md.
// Overwrite-on-write: later calls supersede earlier ones. Returns
// ErrNotFound if the node row doesn't exist. Driven by
// sparkwing.Summary() emitted outside any step body.
func (s *Store) SetNodeSummary(ctx context.Context, runID, nodeID, md string) error {
	return s.defaultTenant().SetNodeSummary(ctx, runID, nodeID, md)
}

// SetNodeSummary is [Store.SetNodeSummary] confined to t's team.
func (t *Tenant) SetNodeSummary(ctx context.Context, runID, nodeID, md string) error {
	res, fenced, err := t.execNodeMutation(ctx, runID, nodeID,
		`UPDATE nodes SET summary = ? WHERE team = ? AND run_id = ? AND node_id = ?`, md, string(t.team), runID, nodeID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 && fenced {
		return ErrLockHeld
	}
	if n == 0 {
		return notFound("node", runID+"/"+nodeID)
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

// ValidateStepTerminalStatus accepts the outcomes a finished WorkStep can
// persist. StepSkipped has its own transition because it never starts.
func ValidateStepTerminalStatus(status string) error {
	switch status {
	case StepPassed, StepFailed, StepCancelled:
		return nil
	default:
		return fmt.Errorf("step status %q: expected passed, failed, or cancelled", status)
	}
}

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

// StartNodeStep inserts a row in the running state, stamping
// started_at. Idempotent: a repeat call for the same (run, node,
// step) is a no-op, leaving the original started_at intact so a
// retry doesn't reset the clock.
func (s *Store) StartNodeStep(ctx context.Context, runID, nodeID, stepID string) error {
	return s.defaultTenant().StartNodeStep(ctx, runID, nodeID, stepID)
}

// StartNodeStep is [Store.StartNodeStep] confined to t's team.
func (t *Tenant) StartNodeStep(ctx context.Context, runID, nodeID, stepID string) error {
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := t.assertNodeMutationTx(ctx, tx, runID, nodeID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO node_steps (team, run_id, node_id, step_id, status, started_at)
VALUES (?,?,?,?,?,?)
ON CONFLICT(run_id, node_id, step_id) DO UPDATE SET status = node_steps.status
  WHERE node_steps.team = excluded.team`,
		string(t.team), runID, nodeID, stepID, StepRunning, time.Now().UnixNano()); err != nil {
		return err
	}
	return tx.Commit()
}

// FinishNodeStep transitions a running step to passed, failed, or cancelled and
// stamps finished_at. Skipped steps use SkipNodeStep because they never enter
// the running state. Creates the row if missing so the rare reorder where
// step_end lands before step_start still records terminal state.
func (s *Store) FinishNodeStep(ctx context.Context, runID, nodeID, stepID, status string) error {
	return s.defaultTenant().FinishNodeStep(ctx, runID, nodeID, stepID, status)
}

// FinishNodeStep is [Store.FinishNodeStep] confined to t's team.
func (t *Tenant) FinishNodeStep(ctx context.Context, runID, nodeID, stepID, status string) error {
	if err := ValidateStepTerminalStatus(status); err != nil {
		return err
	}
	now := time.Now().UnixNano()
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := t.assertNodeMutationTx(ctx, tx, runID, nodeID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO node_steps (team, run_id, node_id, step_id, status, started_at, finished_at)
VALUES (?,?,?,?,?,?,?)
ON CONFLICT(run_id, node_id, step_id) DO UPDATE SET
    status      = excluded.status,
    finished_at = excluded.finished_at
  WHERE node_steps.team = excluded.team`,
		string(t.team), runID, nodeID, stepID, status, now, now); err != nil {
		return err
	}
	return tx.Commit()
}

// SkipNodeStep marks a step as skipped (single insert; no running
// phase). started_at == finished_at == now so duration computes to 0
// without special-casing nulls in the wire-shape serializer.
func (s *Store) SkipNodeStep(ctx context.Context, runID, nodeID, stepID string) error {
	return s.defaultTenant().SkipNodeStep(ctx, runID, nodeID, stepID)
}

// SkipNodeStep is [Store.SkipNodeStep] confined to t's team.
func (t *Tenant) SkipNodeStep(ctx context.Context, runID, nodeID, stepID string) error {
	now := time.Now().UnixNano()
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := t.assertNodeMutationTx(ctx, tx, runID, nodeID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO node_steps (team, run_id, node_id, step_id, status, started_at, finished_at)
VALUES (?,?,?,?,?,?,?)
ON CONFLICT(run_id, node_id, step_id) DO UPDATE SET
    status      = excluded.status,
    finished_at = excluded.finished_at
  WHERE node_steps.team = excluded.team`,
		string(t.team), runID, nodeID, stepID, StepSkipped, now, now); err != nil {
		return err
	}
	return tx.Commit()
}

// ListNodeSteps returns every step row for the run, across all
// nodes. Returned in (node_id, started_at) order so callers can
// stream-bucket by node without a second sort.
func (s *Store) ListNodeSteps(ctx context.Context, runID string) ([]*NodeStep, error) {
	return s.defaultTenant().ListNodeSteps(ctx, runID)
}

// ListNodeSteps is [Store.ListNodeSteps] confined to t's team.
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

// AppendStepAnnotation appends one summary string to a step's
// annotations list. Inserts a placeholder row if the step doesn't
// yet exist (annotations may fire before step_start lands in the
// rare reorder case). Read-modify-write inside one transaction that
// holds the rows it rewrites, which is what keeps concurrent
// appenders from losing entries: the placeholder upsert returns the
// row it locked, so an appender that lost the insert waits for the
// winner rather than reading past it.
func (s *Store) AppendStepAnnotation(ctx context.Context, runID, nodeID, stepID, msg string) error {
	return s.defaultTenant().AppendStepAnnotation(ctx, runID, nodeID, stepID, msg)
}

// AppendStepAnnotation is [Store.AppendStepAnnotation] confined to t's team.
func (t *Tenant) AppendStepAnnotation(ctx context.Context, runID, nodeID, stepID, msg string) error {
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := t.assertNodeMutationTx(ctx, tx, runID, nodeID); err != nil {
		return err
	}

	// safety: only DO UPDATE waits on a conflicting insert that has not
	// committed; DO NOTHING skips it and the read that followed saw no row,
	// dropping the annotation. The assignment rewrites status to itself
	// because the row, not the value, is what this needs.
	var current []byte
	if err := tx.QueryRowContext(ctx, `
INSERT INTO node_steps (team, run_id, node_id, step_id, status)
VALUES (?,?,?,?,?)
ON CONFLICT(run_id, node_id, step_id) DO UPDATE SET status = node_steps.status
  WHERE node_steps.team = excluded.team
RETURNING annotations_json`,
		string(t.team), runID, nodeID, stepID, StepRunning).Scan(&current); err != nil {
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
WHERE team = ? AND run_id = ? AND node_id = ? AND step_id = ?`,
		next, string(t.team), runID, nodeID, stepID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE runs SET annotation_count = annotation_count + 1, top_annotation = ? WHERE team = ? AND id = ?`,
		msg, string(t.team), runID); err != nil {
		return err
	}
	if err := appendRunAnnotation(tx, t.team, runID, msg); err != nil {
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
	return s.defaultTenant().SetStepSummary(ctx, runID, nodeID, stepID, md)
}

// SetStepSummary is [Store.SetStepSummary] confined to t's team.
func (t *Tenant) SetStepSummary(ctx context.Context, runID, nodeID, stepID, md string) error {
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := t.assertNodeMutationTx(ctx, tx, runID, nodeID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO node_steps (team, run_id, node_id, step_id, status)
VALUES (?,?,?,?,?)
ON CONFLICT(run_id, node_id, step_id) DO UPDATE SET status = node_steps.status
  WHERE node_steps.team = excluded.team`,
		string(t.team), runID, nodeID, stepID, StepRunning); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE node_steps SET summary = ?
WHERE team = ? AND run_id = ? AND node_id = ? AND step_id = ?`,
		md, string(t.team), runID, nodeID, stepID); err != nil {
		return err
	}
	return tx.Commit()
}

// MarkNodeReady opens the node's offer round with the highest attainable
// executor priority from the same transaction that snapshots its requirements.
// The node is opened in its run's own team, where CreateNode wrote it.
func (s *Store) MarkNodeReady(ctx context.Context, runID, nodeID string) error {
	t, err := s.runTenant(ctx, runID)
	if err != nil {
		return err
	}
	return t.MarkNodeReady(ctx, runID, nodeID)
}

// MarkNodeReady is [Store.MarkNodeReady] confined to t's team.
func (t *Tenant) MarkNodeReady(ctx context.Context, runID, nodeID string) error {
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	node := &Node{}
	if err := scanNodeRow(tx.QueryRowContext(ctx, `SELECT `+nodeSelectColumns+`
  FROM nodes WHERE team = ? AND run_id = ? AND node_id = ?`+tx.forUpdate(), string(t.team), runID, nodeID), node); err != nil {
		return err
	}
	opened := node.OfferStartedAt != nil
	var summary claimRoundSummary
	if !opened {
		summary, err = claimRoundSummaryTx(ctx, tx, runID, nodeID)
		if err != nil {
			return err
		}
	}
	now := time.Now()
	class, err := readyCPUClassTx(ctx, tx, summary.Resources)
	if err != nil {
		return err
	}
	offerStart := now
	if !opened {
		idle, err := t.s.offerRoundIdleTx(ctx, tx, runID, summary, class, now)
		if err != nil {
			return err
		}
		if idle {
			offerStart = now.Add(-nodeClaimOfferWindow)
		}
	}
	res, err := tx.ExecContext(
		ctx,
		`UPDATE nodes SET ready_at = COALESCE(ready_at, ?),
		                  placement_hold_from = COALESCE(placement_hold_from, ?),
		                  offer_started_at = COALESCE(offer_started_at, ?),
		                  credit_cpu_class = ?
		  WHERE team = ? AND run_id = ? AND node_id = ?`,
		now.UnixNano(), now.UnixNano(), offerStart.UnixNano(), class, string(t.team), runID, nodeID,
	)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return notFound("node", runID+"/"+nodeID)
	}
	if !opened {
		if _, err := appendEventTx(ctx, tx, t.team, runID, nodeID, "executor_offer_round_opened", map[string]any{
			"deadline":               offerStart.Add(nodeClaimOfferWindow),
			"hard_capabilities":      summary.HardCapabilities,
			"preferred_capabilities": summary.PreferredCapabilities,
			"resources":              summary.Resources,
			"slots":                  summary.Slots,
			"run_priority":           summary.RunPriority,
		}, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// RevokeNodeReady cancels an unclaimed offer round.
func (s *Store) RevokeNodeReady(ctx context.Context, runID, nodeID string) (bool, error) {
	return s.defaultTenant().RevokeNodeReady(ctx, runID, nodeID)
}

// RevokeNodeReady is [Store.RevokeNodeReady] confined to t's team.
func (t *Tenant) RevokeNodeReady(ctx context.Context, runID, nodeID string) (bool, error) {
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx,
		`UPDATE nodes SET ready_at = NULL, placement_hold_from = NULL, offer_started_at = NULL
		  WHERE team = ? AND run_id = ? AND node_id = ?
		    AND claimed_by IS NULL AND `+nodeNotDone,
		string(t.team), runID, nodeID,
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
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// ClaimNextReadyNode flips the oldest claimable node to holderID with
// a fresh lease, bound to principal. Claimable = ready_at set,
// unclaimed, !done, and every needs_labels entry appears in
// runnerLabels. ErrNotFound when no candidate matches.
//
// A [ClaimPlacement] on ctx adds the local-first hold: a candidate whose
// preference runnerLabels does not satisfy is passed over while a live runner
// that satisfies it has a slot and the hold window from the node's ready time
// has not run out. Every claim stamps why the node went where it did, as one of
// [PlacementPreferred], [PlacementFallback], or [PlacementNone].
//
// claimant is the authenticated token the claim answers to;
// [Store.HeartbeatNodeClaim] admits only that token afterwards. Pass the zero value when the caller is
// unauthenticated, which leaves the claim unbound. lease is clamped to
// [MaxLeaseDuration].
//
// The claim never crosses teams. The team comes off the claimant's own
// credential, so a machine holding one team's token cannot see another
// team's queue at all. A metered credential is no exception, because
// metering decides who pays and not whose work the claimant may see.
func (s *Store) ClaimNextReadyNode(ctx context.Context, claimant ClaimIdentity, holderID string, lease time.Duration, runnerLabels []string) (*Node, error) {
	lease = clampNodeLease(lease)
	coordinatorID, err := s.CoordinatorID(ctx)
	if err != nil {
		return nil, err
	}
	profile := claimProfileFrom(ctx, claimant, runnerLabels)
	placement, _ := ClaimPlacementFromContext(ctx)
	warm, err := s.warmClassFilter(ctx, claimant)
	if err != nil {
		return nil, err
	}
	if warm.metered {
		profile = withCloudTools(profile)
	}
	scope, err := s.claimScope(ctx, claimant)
	if err != nil {
		return nil, err
	}
	placement, err = s.placementForTeam(ctx, placement, scope.team)
	if err != nil {
		return nil, err
	}

	for range maxClaimAttempts {
		target, err := s.scanClaimCandidates(ctx, profile, runnerLabels, placement, warm, scope)
		if err != nil {
			return nil, err
		}
		if target == nil {
			return nil, notFound("ready node", "")
		}
		claimed, err := s.awardScannedNode(ctx, *target, claimant, holderID, coordinatorID, lease, placement, true, scope)
		if err != nil {
			return nil, err
		}
		if claimed != nil {
			return claimed, nil
		}
	}
	return nil, notFound("ready node", "")
}

const (
	// safety: one read walks this many queued nodes and the rounds look past
	// held or unsuitable ones, so a poll costs reads and never a transaction
	// per candidate.
	claimScanBatch  = 64
	claimScanRounds = 8
	// safety: bounds the races this claim loses to another runner between
	// reading a candidate and awarding it.
	maxClaimAttempts = 8
)

// safety: carries only what a placement decision needs, so the scan reads
// narrow rows and only the awarded node is read in full.
type claimCandidate struct {
	runID    string
	nodeID   string
	readyAt  int64
	holdFrom *time.Time
	needs    []string
	prefers  []string
	request  match.Resources
	decision placementDecision
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

// safety: an unmetered claimant, which is every local claim-mode agent, gets a
// zero filter and the whole queue.
func (s *Store) warmClassFilter(ctx context.Context, claimant ClaimIdentity) (warmClassFilter, error) {
	metered, err := s.TokenMetered(ctx, claimant.TokenPrefix)
	if err != nil || !metered {
		return warmClassFilter{}, err
	}
	table, err := s.CreditRateTable(ctx)
	if err != nil {
		return warmClassFilter{}, err
	}
	cores, err := s.WarmCPUClassCores(ctx)
	if err != nil {
		return warmClassFilter{}, err
	}
	return warmClassFilter{table: table, warmCores: cores, metered: true}, nil
}

// safety: a warm runner shares one machine with its neighbors, so a node whose
// class is larger than the pool serves is passed over here and executed on a
// node sized to that class instead. The filter reads the same charge the ledger
// prices, so what a node is billed and where it runs cannot disagree.
type warmClassFilter struct {
	table     CreditRateTable
	warmCores int64
	// safety: the operator's own pool token is the only one the ladder governs
	// and the only one that may claim it sizes an executor to a class.
	metered bool
}

func (f warmClassFilter) refuses(ctx context.Context, q rowQuerier, runID, nodeID string) (bool, error) {
	if !f.metered {
		return false, nil
	}
	charge, err := nodeChargeTx(ctx, q, runID, nodeID)
	if err != nil {
		return false, err
	}
	return f.refusesCharge(charge), nil
}

// safety: a request no class covers is passed through, because the claim that
// prices it fails the node with the class to add and a filter that hid the node
// would leave it queued forever.
func (f warmClassFilter) refusesCharge(charge ExecutorResource) bool {
	if !f.metered {
		return false
	}
	class, err := f.table.ClassForResource(charge)
	if err != nil {
		return false
	}
	return class.Cores > f.warmCores
}

// safety: walks the queue outside any transaction, so a poll that takes nothing
// writes nothing and holds no lock.
func (s *Store) scanClaimCandidates(ctx context.Context, profile match.Profile, runnerLabels []string,
	placement ClaimPlacement, warm warmClassFilter, scope teamScope,
) (*claimCandidate, error) {
	var cursor *claimCandidate
	ghScope, scoped := GitHubRunnerScopeFrom(ctx)
	admitted := map[string]bool{}
	needRepo := profile.Accept != nil || placement.needsRunRepository()
	repos := map[string]match.Repository{}
	for range claimScanRounds {
		batch, err := s.readClaimCandidates(ctx, cursor, warm, scope)
		if err != nil {
			return nil, err
		}
		for i := range batch {
			n := &batch[i]
			if !match.Evaluate(profile, match.Demand{Selector: n.needs}).OK() {
				continue
			}
			if scoped {
				ok, err := ghScope.admitsRun(ctx, s, n.runID, admitted)
				if err != nil {
					return nil, err
				}
				if !ok {
					continue
				}
			}
			// safety: the request the plan declared is on the row, so matching it
			// costs no read however many nodes a hold passes over.
			demand := match.Demand{Selector: n.needs, Request: n.request}
			if needRepo {
				repo, err := s.runRepositoryOf(ctx, scope, n.runID, repos)
				if err != nil {
					return nil, err
				}
				demand.Repo = &repo
			}
			// safety: the node stays queued for a runner whose list admits its
			// repository and whose capacity holds it, so passing over it writes
			// nothing.
			if !match.Evaluate(profile, demand).OK() {
				continue
			}
			refused, err := warm.refuses(ctx, storeRowQuerier{s}, n.runID, n.nodeID)
			if err != nil {
				return nil, err
			}
			// safety: the node is waiting for a Kubernetes node of its own,
			// which claims it by name, so passing over it writes nothing: a
			// bump would rewrite every queued row on every idle poll and move
			// nothing closer to running.
			if refused {
				continue
			}
			n.decision = placement.decide(demand, n.prefers, runnerLabels, n.holdFrom, time.Now())
			if n.decision.hold {
				continue
			}
			return n, nil
		}
		if len(batch) < claimScanBatch {
			return nil, nil
		}
		cursor = &batch[len(batch)-1]
	}
	return nil, nil
}

func (s *Store) readClaimCandidates(
	ctx context.Context, after *claimCandidate, warm warmClassFilter, scope teamScope,
) ([]claimCandidate, error) {
	teamClause, teamArgs, err := claimTeamWhere(scope, "team")
	if err != nil {
		return nil, err
	}
	args := teamArgs
	// safety: the stamped class keeps a queue of large nodes out of the scan
	// window, so a small node behind thousands of them is still reachable.
	classClause := ""
	if warm.metered {
		classClause = ` AND credit_cpu_class <= ?`
		args = append(args, warm.warmCores)
	}
	scopeClause := ""
	if ghScope, ok := GitHubRunnerScopeFrom(ctx); ok {
		var scopeArgs []any
		scopeClause, scopeArgs = ghScope.nodeClause()
		args = append(args, scopeArgs...)
	}
	keyset := ""
	if after != nil {
		keyset = ` AND (ready_at > ? OR (ready_at = ? AND (run_id > ? OR (run_id = ? AND node_id > ?))))`
		args = append(args, after.readyAt, after.readyAt, after.runID, after.runID, after.nodeID)
	}
	args = append(args, claimScanBatch)
	rows, err := s.query(ctx, `SELECT run_id, node_id, ready_at, placement_hold_from, needs_labels, prefers_labels,
        requested_cores, requested_memory_bytes
 FROM nodes
	WHERE ready_at IS NOT NULL AND claimed_by IS NULL AND `+nodeNotDone+`
	AND `+nodeRunActiveSQL("nodes.")+`
	AND `+nodeTriggerDispatched+teamClause+classClause+scopeClause+keyset+`
 ORDER BY ready_at ASC, run_id ASC, node_id ASC
	 LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer closeRowsOrLog(rows)
	var out []claimCandidate
	for rows.Next() {
		var candidate claimCandidate
		var holdFromNS sql.NullInt64
		var needsJSON, prefersJSON []byte
		if err := rows.Scan(&candidate.runID, &candidate.nodeID, &candidate.readyAt,
			&holdFromNS, &needsJSON, &prefersJSON, &candidate.request.Cores, &candidate.request.MemoryBytes); err != nil {
			return nil, err
		}
		if holdFromNS.Valid {
			t := time.Unix(0, holdFromNS.Int64)
			candidate.holdFrom = &t
		} else {
			t := time.Unix(0, candidate.readyAt)
			// safety: a row written before the hold-from column existed has only
			// ready_at to run its hold from, which a bump may since have moved.
			candidate.holdFrom = &t
		}
		if !decodeCandidateLabels(candidate.runID, candidate.nodeID, needsJSON, &candidate.needs) ||
			!decodeCandidateLabels(candidate.runID, candidate.nodeID, prefersJSON, &candidate.prefers) {
			continue
		}
		out = append(out, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, rows.Close()
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

// safety: the same column carries a node's hard requirements, so a row whose
// labels will not decode is passed over rather than claimed as unconstrained.
// It waits, and the queue deadline fails it where an operator can see it.
func decodeCandidateLabels(runID, nodeID string, raw []byte, out *[]string) bool {
	if len(raw) == 0 {
		return true
	}
	if err := json.Unmarshal(raw, out); err != nil {
		slog.Warn("skipping a claim candidate whose labels will not decode",
			"run_id", runID, "node_id", nodeID, "err", err)
		return false
	}
	return true
}

// safety: returns nil when another runner took the node between the scan and
// the award, which the caller answers by scanning again.
func (s *Store) awardScannedNode(ctx context.Context, candidate claimCandidate, claimant ClaimIdentity,
	holderID, coordinatorID string, lease time.Duration, placement ClaimPlacement, queued bool, scope teamScope,
) (*Node, error) {
	readyClause := ""
	if queued {
		readyClause = ` AND ready_at IS NOT NULL`
	}
	tx, err := s.beginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer rollbackOrLog(tx)
	now := time.Now()
	n, err := s.awardScannedNodeTx(ctx, tx, candidate, claimant, holderID, coordinatorID, lease, placement, readyClause, scope, now)
	if err != nil || n == nil {
		return nil, err
	}
	if err := mintRunnerNodeTokenTx(ctx, tx, claimant, scope.team, n, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return n, nil
}

func (s *Store) awardScannedNodeTx(ctx context.Context, tx *storeTx, candidate claimCandidate, claimant ClaimIdentity,
	holderID, coordinatorID string, lease time.Duration, placement ClaimPlacement, readyClause string, scope teamScope,
	now time.Time,
) (*Node, error) {
	// safety: the award repeats the scan's team predicate rather than trusting
	// it, because [Store.ClaimNamedNode] reaches this with a node the caller
	// named and no scan behind it.
	teamClause, teamArgs, err := claimTeamWhere(scope, "team")
	if err != nil {
		return nil, err
	}
	// safety: reservation claims lock their node before its run; sharing that order avoids a cycle.
	var lockedNode string
	err = tx.QueryRowContext(ctx, `SELECT node_id FROM nodes WHERE run_id = ? AND node_id = ?`+teamClause+tx.forUpdate(),
		append([]any{candidate.runID, candidate.nodeID}, teamArgs...)...).Scan(&lockedNode)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	found, err := lockTeamRunRowTx(ctx, tx, scope.team, candidate.runID)
	if err != nil || !found {
		return nil, err
	}
	expires := now.Add(lease)
	awardArgs := []any{
		holderID, claimant.Principal, claimant.TokenPrefix, expires.UnixNano(),
		coordinatorID, candidate.decision.reason, candidate.runID, candidate.nodeID,
	}
	awardArgs = append(awardArgs, teamArgs...)
	res, err := tx.ExecContext(
		ctx,
		`UPDATE nodes SET attention_reason = '', claimed_by = ?, claim_principal = ?, claim_token_prefix = ?,
		        lease_expires_at = ?, coordinator_id = ?, executor_kind = '', executor_id = '',
		        executor_location = 'unknown', reservation_id = '', claim_membership_id = '',
		        credit_charged_through = 0,
		        placement_reason = ?, claim_generation = claim_generation + 1
		  WHERE run_id = ? AND node_id = ? AND claimed_by IS NULL`+readyClause+`
		    AND `+nodeNotDone+nodeRunNotCancelled+` AND `+nodeRunActiveSQL("nodes.")+teamClause,
		awardArgs...,
	)
	if err != nil {
		return nil, err
	}
	awarded, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	if awarded == 0 {
		return nil, nil
	}
	if err := s.reserveNodeCreditsTx(ctx, tx, claimant, candidate.runID, candidate.nodeID, now, readyClause != ""); err != nil {
		return nil, err
	}
	// safety: a preference the controller supplies for every node it never
	// hears about decides nothing per node, so only an overridden preference or
	// one the plan declared itself earns an event on every claim.
	if candidate.decision.reason == PlacementFallback ||
		(candidate.decision.reason == PlacementPreferred && candidate.decision.nodeOwned) {
		if _, err := appendRunEventTx(ctx, tx, candidate.runID, candidate.nodeID, "node_placed", nodePlacementEvent{
			HolderID: holderID, Reason: candidate.decision.reason,
			Prefers: placement.prefersFor(candidate.prefers),
		}, now); err != nil {
			return nil, err
		}
	}
	n := &Node{}
	if err := scanNodeRow(tx.QueryRowContext(ctx, `SELECT `+nodeSelectColumns+`
 FROM nodes WHERE run_id = ? AND node_id = ?`, candidate.runID, candidate.nodeID), n); err != nil {
		return nil, err
	}
	return n, nil
}

// UpdateNodeActivity sets status_detail and bumps last_heartbeat.
func (s *Store) UpdateNodeActivity(ctx context.Context, runID, nodeID, detail string) error {
	return s.defaultTenant().UpdateNodeActivity(ctx, runID, nodeID, detail)
}

// UpdateNodeActivity is [Store.UpdateNodeActivity] confined to t's team.
func (t *Tenant) UpdateNodeActivity(ctx context.Context, runID, nodeID, detail string) error {
	res, fenced, err := t.execNodeMutation(ctx, runID, nodeID,
		`UPDATE nodes SET status_detail = ?, last_heartbeat = ?
		  WHERE team = ? AND run_id = ? AND node_id = ?`, detail, time.Now().UnixNano(), string(t.team), runID, nodeID)
	if err != nil {
		return err
	}
	return fencedRows(res, fenced)
}

// TouchNodeHeartbeat stamps last_heartbeat=now.
func (s *Store) TouchNodeHeartbeat(ctx context.Context, runID, nodeID string) error {
	return s.defaultTenant().TouchNodeHeartbeat(ctx, runID, nodeID)
}

// TouchNodeHeartbeat is [Store.TouchNodeHeartbeat] confined to t's team.
func (t *Tenant) TouchNodeHeartbeat(ctx context.Context, runID, nodeID string) error {
	res, fenced, err := t.execNodeMutation(ctx, runID, nodeID,
		`UPDATE nodes SET last_heartbeat = ? WHERE team = ? AND run_id = ? AND node_id = ?`,
		time.Now().UnixNano(), string(t.team), runID, nodeID)
	if err != nil {
		return err
	}
	return fencedRows(res, fenced)
}

// HeartbeatNodeClaim extends the exact claim generation carried by ctx.
// The lease cannot outrun the claim cap or revive expired capacity.
func (s *Store) HeartbeatNodeClaim(ctx context.Context, runID, nodeID string, claimant ClaimIdentity, holderID string, lease time.Duration) error {
	_, _, err := s.HeartbeatNodeClaimWithCredits(ctx, runID, nodeID, claimant, holderID, lease, false)
	return err
}

// HeartbeatNodeClaimWithCredits renews a claim and charges a licensed metered
// token in one transaction. It reports the charge and whether the token was
// metered. A failed ledger write leaves the lease unchanged.
func (s *Store) HeartbeatNodeClaimWithCredits(ctx context.Context, runID, nodeID string, claimant ClaimIdentity, holderID string, lease time.Duration, meteringLicensed bool) (CreditChargeResult, bool, error) {
	fence, ok := NodeClaimFenceFromContext(ctx)
	if !ok || fence.HolderID != holderID || fence.Claimant != claimant || fence.ClaimGeneration < 1 {
		return CreditChargeResult{}, false, ErrLockHeld
	}
	var charge CreditChargeResult
	var metered bool
	err := s.inTx(ctx, func(tx *storeTx) error {
		var currentLease int64
		err := tx.QueryRowContext(ctx, `SELECT COALESCE(lease_expires_at, 0) FROM nodes
		  WHERE run_id = ? AND node_id = ? AND claimed_by = ?
		    AND COALESCE(claim_principal, '') = ?
		    AND COALESCE(claim_token_prefix, '') = ?
		    AND COALESCE(claim_membership_id, '') = ?
		    AND COALESCE(reservation_id, '') = ?
		    AND claim_generation = ? AND cancel_requested_at IS NULL AND `+nodeRunActiveSQL("")+nodeRunNotCancelled+tx.forUpdate(),
			runID, nodeID, holderID, claimant.Principal, claimant.TokenPrefix,
			fence.MembershipID, fence.ReservationID, fence.ClaimGeneration).Scan(&currentLease)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrLockHeld
		}
		if err != nil {
			return err
		}
		now := time.Now()
		if currentLease < now.UnixNano() {
			return ErrLockHeld
		}
		if meteringLicensed {
			metered, err = tokenMeteredTx(ctx, tx, claimant.TokenPrefix)
			if err != nil {
				return err
			}
			if metered {
				charged, err := s.chargeNodeTx(ctx, tx, runID, nodeID, claimant.TokenPrefix, now, false)
				if err != nil {
					return err
				}
				charge = charged.CreditChargeResult
				if charge.Cancel {
					return nil
				}
			}
		}
		// safety: the ledger can block past the lease, so renewal uses a fresh clock.
		renewAt := time.Now()
		if currentLease < renewAt.UnixNano() {
			return ErrLockHeld
		}
		expires := renewAt.Add(clampNodeLease(lease)).UnixNano()
		res, err := tx.ExecContext(ctx, `UPDATE nodes SET lease_expires_at = ?
		  WHERE run_id = ? AND node_id = ? AND claimed_by = ?
		    AND COALESCE(claim_principal, '') = ?
		    AND COALESCE(claim_token_prefix, '') = ?
		    AND COALESCE(claim_membership_id, '') = ?
		    AND COALESCE(reservation_id, '') = ?
		    AND claim_generation = ? AND cancel_requested_at IS NULL AND `+nodeRunActiveSQL("")+nodeRunNotCancelled+` AND `+nodeClaimLiveSQL(""),
			expires, runID, nodeID, holderID, claimant.Principal, claimant.TokenPrefix,
			fence.MembershipID, fence.ReservationID, fence.ClaimGeneration, renewAt.UnixNano())
		if err != nil {
			return err
		}
		updated, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if updated == 0 {
			return ErrLockHeld
		}
		return nil
	})
	if err != nil {
		return CreditChargeResult{}, false, err
	}
	return charge, metered, nil
}

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
		    AND `+triggerClaimLiveSQL(""),
		triggerID, claimant.Principal, claimant.TokenPrefix, now.UnixNano()).Scan(&held)
	if err != nil {
		return false, err
	}
	return held > 0, nil
}

// TriggerClaimant returns the claimant recorded on the trigger row,
// whatever state that claim is in now. [Store.PrincipalHoldsTriggerClaim]
// is the live-lease question; this is the "who took it" question, so the
// holder is still recognisable on a lease that has just lapsed and on a
// trigger it already finished, which is what lets it retry a close.
// Every requeue path clears the record, so a reaped trigger comes back
// with the zero identity -- nobody holds it -- and a trigger that does
// not exist comes back ErrNotFound.
func (s *Store) TriggerClaimant(ctx context.Context, triggerID string) (ClaimIdentity, error) {
	return s.defaultTenant().TriggerClaimant(ctx, triggerID)
}

// TriggerClaimant is [Store.TriggerClaimant] confined to t's team.
func (t *Tenant) TriggerClaimant(ctx context.Context, triggerID string) (ClaimIdentity, error) {
	var claimant ClaimIdentity
	err := t.s.queryRow(ctx,
		`SELECT claim_principal, claim_token_prefix FROM triggers WHERE team = ? AND id = ?`,
		string(t.team), triggerID).Scan(&claimant.Principal, &claimant.TokenPrefix)
	if errors.Is(err, sql.ErrNoRows) {
		return ClaimIdentity{}, ErrNotFound
	}
	if err != nil {
		return ClaimIdentity{}, err
	}
	return claimant, nil
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
		    AND `+nodeClaimLiveSQL(""),
		runID, claimant.Principal, claimant.TokenPrefix, now.UnixNano()).Scan(&held)
	if err != nil {
		return false, err
	}
	return held > 0, nil
}

// PrincipalHoldsProfileClaim reports whether claimant may write the
// capacity profile stored under key. The proof is a live claim on a node
// of a run of that pipeline, or on that run's trigger. The repository
// half of the key is not part of the proof, because a run's repository
// is a string its submitter typed and two callers may type the same one.
// An unbound claimant holds nothing.
func (s *Store) PrincipalHoldsProfileClaim(ctx context.Context, key string, claimant ClaimIdentity, now time.Time) (bool, error) {
	if !claimant.bound() {
		return false, nil
	}
	_, pipeline := SplitProfileKey(key)
	if pipeline == "" {
		return false, nil
	}
	// safety: the trigger arm stands on its own, because a claimed trigger
	// prices its pipeline's admission wait before its run row exists.
	for _, query := range []string{
		`SELECT COUNT(*) FROM triggers
		  WHERE pipeline = ? AND claim_principal = ? AND claim_token_prefix = ?
		    AND ` + triggerClaimLiveSQL(""),
		`SELECT COUNT(*) FROM nodes
		  WHERE run_id IN (SELECT id FROM runs WHERE pipeline = ?)
		    AND claim_principal = ? AND claim_token_prefix = ?
		    AND ` + nodeClaimLiveSQL(""),
	} {
		var held int
		if err := s.queryRow(ctx, query,
			pipeline, claimant.Principal, claimant.TokenPrefix, now.UnixNano()).Scan(&held); err != nil {
			return false, err
		}
		if held > 0 {
			return true, nil
		}
	}
	return false, nil
}

// ReapExpiredNodeClaims clears claimed_by/lease_expires_at on expired
// claims; ready_at is left intact. An expired claim on a controller-dispatched
// run ends as a lost attempt instead, through [Store.ReportAttempt]'s retry
// and settle, in a pass of its own after this one. Returns reaped pairs.
func (s *Store) ReapExpiredNodeClaims(ctx context.Context) (out [][2]string, err error) {
	defer func() { out = append(out, s.expireDispatchClaimsLogged(ctx, time.Now())...) }()
	tx, err := s.beginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UnixNano()

	rows, err := tx.QueryContext(ctx,
		`SELECT run_id, node_id, credit_charged_through, lease_expires_at, claim_token_prefix FROM nodes
		  WHERE claimed_by IS NOT NULL AND lease_expires_at IS NOT NULL
		    AND lease_expires_at < ? AND kind = '' AND `+nodeNotDone+s.forUpdateSkipLocked(),
		now)
	if err != nil {
		return nil, err
	}
	var pairs [][2]string
	var lapsed []expiredClaim
	for rows.Next() {
		var rid, nid, prefix string
		var chargeWindow, lease int64
		if err := rows.Scan(&rid, &nid, &chargeWindow, &lease, &prefix); err != nil {
			_ = rows.Close()
			return nil, err
		}
		pairs = append(pairs, [2]string{rid, nid})
		if chargeWindow != 0 {
			lapsed = append(lapsed, expiredClaim{runID: rid, nodeID: nid, tokenPrefix: prefix, leaseNS: lease})
		}
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(pairs) == 0 {
		return nil, nil
	}
	for _, claim := range lapsed {
		if err := s.settleExpiredClaimTx(ctx, tx, claim, now); err != nil {
			return nil, err
		}
	}
	// safety: only the rows this pass selected, locked and settled are
	// cleared. On Postgres the select skips a row another transaction holds,
	// and clearing every expired claim would clear that one too once its
	// holder let go, dropping the tail nobody settled.
	for _, pair := range pairs {
		if _, err := tx.ExecContext(ctx,
			`UPDATE nodes SET claimed_by = NULL, claim_principal = '', claim_token_prefix = '',
			        claim_executor = '', claim_cores = 0, claim_memory_bytes = 0,
			        claim_reservation = '', claim_slot = -1, lease_expires_at = NULL,
			        credit_charged_through = 0
			  WHERE run_id = ? AND node_id = ? AND claimed_by IS NOT NULL AND lease_expires_at IS NOT NULL
			    AND lease_expires_at < ? AND `+nodeNotDone,
			pair[0], pair[1], now); err != nil {
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
func lockRunRow(ctx context.Context, tx *storeTx, runID string) error {
	var id string
	err := tx.QueryRowContext(ctx,
		`SELECT id FROM runs WHERE id = ?`+tx.forNoKeyUpdate(), runID).Scan(&id)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return nil
}

// AppendEvent writes the run's next event in order.
func (s *Store) AppendEvent(ctx context.Context, runID, nodeID, kind string, payload []byte) error {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if nodeID != "" {
		if err := s.assertNodeMutationFenceInRunsTeamTx(ctx, tx, runID, nodeID); err != nil {
			return err
		}
	} else if err := s.assertRunMutationFenceInRunsTeamTx(ctx, tx, runID); err != nil {
		return err
	}
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

// safety: an append bumps the run row's event counters, so it takes the run's
// row lock before the event-sequence lock, the order a claim round takes
// them in; the other order deadlocks the two on PostgreSQL.
func lockEventSequenceTx(ctx context.Context, tx *storeTx, team Team, runID string) error {
	if tx.dialect != DialectPostgres {
		return nil
	}
	if _, err := lockTeamRunRowTx(ctx, tx, team, runID); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext(?))`, runID)
	return err
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

// CreateDebugPause inserts (or upserts) an open pause row in the default team.
func (s *Store) CreateDebugPause(ctx context.Context, p DebugPause) error {
	return s.defaultTenant().CreateDebugPause(ctx, p)
}

// CreateDebugPause inserts (or upserts) an open pause row on one of t's
// runs. A run of another team reads as [ErrNotFound].
func (t *Tenant) CreateDebugPause(ctx context.Context, p DebugPause) error {
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackOrLog(tx)
	if err := assertRunBelongsToTeamTx(ctx, tx, t.team, p.RunID); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `
INSERT INTO debug_pauses (team, run_id, node_id, reason, paused_at, expires_at)
VALUES (?,?,?,?,?,?)
ON CONFLICT(run_id, node_id, reason) DO UPDATE SET
    paused_at = excluded.paused_at,
    expires_at = excluded.expires_at,
    released_at = NULL,
    released_by = '',
    release_kind = ''
  WHERE debug_pauses.team = excluded.team`,
		string(t.team), p.RunID, p.NodeID, p.Reason,
		p.PausedAt.UnixNano(), p.ExpiresAt.UnixNano())
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		if err != nil {
			return err
		}
		return notFound("run", p.RunID)
	}
	return tx.Commit()
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

// ListDebugPauses returns all pause rows for a run of the default team, newest first.
func (s *Store) ListDebugPauses(ctx context.Context, runID string) ([]*DebugPause, error) {
	return s.defaultTenant().ListDebugPauses(ctx, runID)
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

// ReleaseDebugPause closes the open pause of a default-team node; ErrNotFound when none.
func (s *Store) ReleaseDebugPause(ctx context.Context, runID, nodeID, releasedBy, kind string) error {
	return s.defaultTenant().ReleaseDebugPause(ctx, runID, nodeID, releasedBy, kind)
}

// ReleaseDebugPause closes the open pause of a node of one of t's runs;
// ErrNotFound when none.
func (t *Tenant) ReleaseDebugPause(ctx context.Context, runID, nodeID, releasedBy, kind string) error {
	res, err := t.s.exec(ctx, `
UPDATE debug_pauses
   SET released_at = ?, released_by = ?, release_kind = ?
 WHERE team = ? AND run_id = ? AND node_id = ? AND released_at IS NULL`,
		time.Now().UnixNano(), releasedBy, kind, string(t.team), runID, nodeID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return notFound("debug pause", runID+"/"+nodeID)
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

const metaKeyCoordinatorID = "coordinator_id"

func (s *Store) CoordinatorID(ctx context.Context) (string, error) {
	var id string
	if err := s.queryRow(ctx, `SELECT value FROM sparkwing_meta WHERE key = ?`, metaKeyCoordinatorID).Scan(&id); err == nil && id != "" {
		return id, nil
	} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	var suffix [16]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", err
	}
	candidate := "coord-" + hex.EncodeToString(suffix[:])
	if _, err := s.exec(ctx, `INSERT INTO sparkwing_meta (key, value, updated_at) VALUES (?, ?, ?) ON CONFLICT (key) DO NOTHING`,
		metaKeyCoordinatorID, candidate, time.Now().UnixNano()); err != nil {
		return "", err
	}
	if err := s.queryRow(ctx, `SELECT value FROM sparkwing_meta WHERE key = ?`, metaKeyCoordinatorID).Scan(&id); err != nil {
		return "", err
	}
	return id, nil
}

func coordinatorIDTx(ctx context.Context, tx *storeTx) (string, error) {
	var id string
	err := tx.QueryRowContext(ctx, `SELECT value FROM sparkwing_meta WHERE key = ?`, metaKeyCoordinatorID).Scan(&id)
	if err == nil && id != "" {
		return id, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	var suffix [16]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", err
	}
	id = "coord-" + hex.EncodeToString(suffix[:])
	if _, err := tx.ExecContext(ctx, `INSERT INTO sparkwing_meta (key, value, updated_at) VALUES (?, ?, ?) ON CONFLICT (key) DO NOTHING`,
		metaKeyCoordinatorID, id, time.Now().UnixNano()); err != nil {
		return "", err
	}
	if err := tx.QueryRowContext(ctx, `SELECT value FROM sparkwing_meta WHERE key = ?`, metaKeyCoordinatorID).Scan(&id); err != nil {
		return "", err
	}
	return id, nil
}

// CreateTrigger inserts a new trigger with status='pending'.
func (s *Store) CreateTrigger(ctx context.Context, t Trigger) error {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackOrLog(tx)
	// safety: the unscoped twin writes [DefaultTeam], the team the migration
	// put every pre-tenant row in.
	if err := createTriggerTx(ctx, tx, DefaultTeam, t); err != nil {
		return err
	}
	return tx.Commit()
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
// and the caller resolves it with [Store.FindTriggerByIdempotencyKey].
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
	return s.defaultTenant().FindTriggerByIdempotencyKey(ctx, pipeline, key)
}

// FinishTriggerAtGeneration closes a trigger only while seq is still
// its current claim generation, reporting false when it is not.
//
// This is the fence a superseded dispatch meets. Once a lapsed claim has
// been re-taken by another consumer the trigger's generation moves on,
// and the old dispatch -- which may still be running, and may still be
// about to finish -- must not close out a claim it no longer holds. A
// caller that sees false knows its work was superseded and that the
// current claim owns the outcome.
func (s *Store) FinishTriggerAtGeneration(ctx context.Context, id string, seq int64) (bool, error) {
	return s.defaultTenant().FinishTriggerAtGeneration(ctx, id, seq)
}

// FinishTriggerAtGeneration is [Store.FinishTriggerAtGeneration] confined to t's team.
func (t *Tenant) FinishTriggerAtGeneration(ctx context.Context, id string, seq int64) (bool, error) {
	return t.endTriggerClaim(ctx, id, false,
		`UPDATE triggers SET status = CASE WHEN EXISTS
		   (SELECT 1 FROM runs WHERE runs.team = triggers.team AND runs.id = triggers.id AND runs.status = 'failed')
		   THEN 'failed' ELSE 'done' END, lease_expires_at = NULL
		  WHERE team = ? AND id = ? AND claim_seq = ?`, string(t.team), id, seq)
}

// safety: a refused write rolls the settlement back with it, so a superseded caller cannot settle a
// claim it no longer holds.
// safety: a trigger of another team reads as missing before its credits are
// settled, so ending a foreign claim settles nothing and changes nothing.
func (t *Tenant) endTriggerClaim(ctx context.Context, id string, refundAll bool, query string, args ...any) (_ bool, err error) {
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return false, err
	}
	defer rollbackUnlessDone(tx, &err)
	if owned, err := rowPresentTx(ctx, tx, `SELECT 1 FROM triggers WHERE team = ? AND id = ?`, string(t.team), id); err != nil || !owned {
		return false, err
	}
	if err := settleTriggerCreditsTx(ctx, tx, id, time.Now(), refundAll); err != nil {
		return false, err
	}
	res, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil || n == 0 {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// FinishRunAtGeneration writes a run's terminal status only while seq is
// still its trigger's current claim generation.
//
// It pairs with FinishTriggerAtGeneration for the run row: a superseded
// dispatch must not stamp its own outcome over the run the current claim
// is producing. Reports false without writing when the generation has
// moved on.
func (s *Store) FinishRunAtGeneration(ctx context.Context, runID string, seq int64, status, errMsg string) (bool, error) {
	return s.defaultTenant().FinishRunAtGeneration(ctx, runID, seq, status, errMsg)
}

// FinishRunAtGeneration is [Store.FinishRunAtGeneration] confined to t's team.
func (t *Tenant) FinishRunAtGeneration(ctx context.Context, runID string, seq int64, status, errMsg string) (bool, error) {
	if !isTerminalRunStatus(status) {
		return false, fmt.Errorf("%w: %q is not a terminal run status", ErrInvalidInput, status)
	}
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()

	// safety: the trigger row stays held from the generation read through the
	// run write. Read them apart and a re-claim lands between the two, which
	// is the one interleaving this fence exists to refuse.
	var current int64
	switch err := tx.QueryRowContext(ctx,
		`SELECT claim_seq FROM triggers WHERE team = ? AND id = ?`+tx.forUpdate(), string(t.team), runID).
		Scan(&current); {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, err
	case current != seq:
		return false, nil
	}

	if err := finishRunOnceTx(ctx, tx, runID, status, errMsg, t.team); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// RequeueUnstartedClaim returns a lapsed claim to the pending queue only
// while its run has not started -- no run row, or one still pending.
//
// The run-status guard is what keeps recovery from becoming duplication.
// A run that reached `running` has a process behind it that the lease
// alone cannot see, so requeueing it would put a second copy of live
// work on the queue; that case belongs to the orphan reaper, which
// judges by heartbeat. The run's status is a correlated subquery inside
// the requeue's own WHERE rather than a read before it, which narrows
// the window a run can start in from a round trip to a single
// statement. It does not close it: the subquery reads that statement's
// snapshot, so a run that commits its start afterwards is invisible
// here, and nothing this function locks would make it visible -- the
// consumer that starts a run does not touch the trigger row. A run with
// no row at all reads as not started, which is also what an unclaimed
// trigger looks like before its consumer gets that far.
func (s *Store) RequeueUnstartedClaim(ctx context.Context, id string) (bool, error) {
	return s.defaultTenant().RequeueUnstartedClaim(ctx, id)
}

// RequeueUnstartedClaim is [Store.RequeueUnstartedClaim] confined to t's team.
func (t *Tenant) RequeueUnstartedClaim(ctx context.Context, id string) (bool, error) {
	return t.endTriggerClaim(ctx, id, true,
		`UPDATE triggers
		    SET status = ?, claimed_at = NULL, lease_expires_at = NULL,
		        claim_principal = '', claim_token_prefix = ''
		  WHERE team = ? AND id = ? AND status = ?
		    AND COALESCE((SELECT status FROM runs WHERE runs.team = triggers.team AND runs.id = triggers.id), ?) = ?`,
		triggerStatusPending, string(t.team), id, triggerStatusClaimed, runStatusPending, runStatusPending)
}

// ReleaseClaimAtGeneration returns a claimed trigger to the pending
// queue, but only while seq is still its current claim generation.
//
// A consumer shutting down mid-dispatch uses it so the interrupted work
// is immediately claimable by the next consumer instead of waiting out
// a lease. The generation guard keeps a shutting-down consumer from
// yanking a claim another consumer has already taken.
func (s *Store) ReleaseClaimAtGeneration(ctx context.Context, id string, seq int64) (bool, error) {
	return s.defaultTenant().ReleaseClaimAtGeneration(ctx, id, seq)
}

// ReleaseClaimAtGeneration is [Store.ReleaseClaimAtGeneration] confined to t's team.
func (t *Tenant) ReleaseClaimAtGeneration(ctx context.Context, id string, seq int64) (bool, error) {
	return t.endTriggerClaim(ctx, id, false,
		`UPDATE triggers
		    SET status = ?, claimed_at = NULL, lease_expires_at = NULL,
		        claim_principal = '', claim_token_prefix = ''
		  WHERE team = ? AND id = ? AND claim_seq = ? AND status = ?`,
		triggerStatusPending, string(t.team), id, seq, triggerStatusClaimed)
}

// TriggerClaimGeneration returns a trigger's current claim generation,
// so a dispatch can tell whether it still owns the claim it started
// under before it writes anything terminal.
func (s *Store) TriggerClaimGeneration(ctx context.Context, id string) (int64, error) {
	return s.defaultTenant().TriggerClaimGeneration(ctx, id)
}

// TriggerClaimGeneration is [Store.TriggerClaimGeneration] confined to t's team.
func (t *Tenant) TriggerClaimGeneration(ctx context.Context, id string) (int64, error) {
	var seq int64
	if err := t.s.queryRow(ctx,
		`SELECT claim_seq FROM triggers WHERE team = ? AND id = ?`, string(t.team), id).Scan(&seq); err != nil {
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
	return s.defaultTenant().CancelPendingTrigger(ctx, id)
}

// CancelPendingTrigger is [Store.CancelPendingTrigger] confined to t's team.
func (t *Tenant) CancelPendingTrigger(ctx context.Context, id string) (bool, error) {
	now := time.Now()
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx,
		`UPDATE triggers
		    SET status = ?, cancel_requested_at = COALESCE(cancel_requested_at, ?)
		  WHERE team = ? AND id = ? AND status = ?`,
		triggerStatusDone, now.UnixNano(), string(t.team), id, triggerStatusPending)
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
		  WHERE team = ? AND id = ? AND status = ?`,
		runStatusCancelled, now.UnixNano(),
		"cancelled before dispatch", string(t.team), id, runStatusPending); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// safety: a trigger cancelled while claimed, then released or lapsed back to the queue, is refused by
// the claim filter, so without finalizing it here it would sit in the queue forever.
func cancelRequeuedCancelledTriggersTx(ctx context.Context, tx *storeTx, now time.Time) error {
	if _, err := tx.ExecContext(ctx,
		`UPDATE runs
		    SET status = ?, finished_at = ?, error = ?
		  WHERE status = ? AND id IN (
		        SELECT id FROM triggers
		         WHERE status = ? AND cancel_requested_at IS NOT NULL)`,
		runStatusCancelled, now.UnixNano(), "cancelled before dispatch",
		runStatusPending, triggerStatusPending); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx,
		`UPDATE triggers SET status = ?, lease_expires_at = NULL
		  WHERE status = ? AND cancel_requested_at IS NOT NULL`,
		triggerStatusDone, triggerStatusPending)
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

// ListSpawnedChildrenByRun returns this tenant's child runs ordered by parent job and creation time.
func (t *Tenant) ListSpawnedChildrenByRun(ctx context.Context, runID string) ([]SpawnedChild, error) {
	rows, err := t.s.query(ctx, `
SELECT parent_node_id, pipeline, id
FROM triggers
WHERE team = ? AND parent_run_id = ? AND parent_node_id != ''
ORDER BY parent_node_id, created_at`, string(t.team), runID)
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
	return s.defaultTenant().GetRunAncestorPipelines(ctx, runID)
}

// GetRunAncestorPipelines walks parent_run_id links inside t's team; an
// ancestor in another team ends the walk as a missing one does. See
// [Store.GetRunAncestorPipelines].
func (t *Tenant) GetRunAncestorPipelines(ctx context.Context, runID string) ([]string, error) {
	if runID == "" {
		return nil, nil
	}
	var out []string
	cur := runID
	const maxDepth = 64
	for range maxDepth {
		var parent sql.NullString
		var pipeline string
		err := t.s.queryRow(
			ctx,
			`SELECT pipeline, parent_run_id FROM runs WHERE team = ? AND id = ?`, string(t.team), cur,
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
//
// The claim never crosses teams: the team comes off the claimant's own
// credential exactly as for [Store.ClaimNextReadyNode], because a claimed
// trigger is what a runner's whole run, and every secret it reads, rests on.
func (s *Store) ClaimNextTriggerFor(ctx context.Context, claimant ClaimIdentity, lease time.Duration, pipelines, sources []string) (*Trigger, error) {
	if lease <= 0 {
		lease = DefaultLeaseDuration
	}
	teamClause, teamArgs, err := s.claimTeamClause(ctx, claimant, "triggers.team")
	if err != nil {
		return nil, err
	}
	tx, err := s.beginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now()
	if err := cancelRequeuedCancelledTriggersTx(ctx, tx, now); err != nil {
		return nil, err
	}

	sel := `
SELECT id, pipeline, args_json, trigger_source, trigger_user,
       trigger_env, git_branch, git_sha, status, created_at, parent_run_id,
       repo, repo_url, github_owner, github_repo, repo_inherited, retry_of, retry_source, parent_node_id, "full",
       idempotency_key, claim_seq, webhook_delivery, team, webhook_replay_key
  FROM triggers
 WHERE status = ? AND available_at <= ? AND cancel_requested_at IS NULL
   AND credit_reserved_at = 0 AND runner_pin = ''` + teamClause
	args := []any{triggerStatusPending, now.UnixNano()}
	args = append(args, teamArgs...)
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
	scope, scoped := GitHubRunnerScopeFrom(ctx)
	if scoped {
		clause, scopeArgs := scope.triggerClause("")
		sel += clause
		args = append(args, scopeArgs...)
	}
	profile := claimProfileFrom(ctx, claimant, nil)
	filtered := profile.Accept != nil
	// safety: a filtered claimant reads the queue in batches and passes over
	// every trigger its list does not admit, so another repository's run at the
	// head of the queue never hides the one it may build.
	batch, rounds := 1, 1
	if filtered {
		batch, rounds = claimScanBatch, claimScanRounds
	}
	var t Trigger
	var argsJSON, envJSON []byte
	var createdNS int64
	found := false
	var after *triggerCursor
	for round := 0; round < rounds && !found; round++ {
		query, queryArgs := sel, args
		if after != nil {
			query += ` AND (created_at > ? OR (created_at = ? AND id > ?))`
			queryArgs = append(append([]any(nil), args...), after.createdNS, after.createdNS, after.id)
		}
		query += `
 ORDER BY created_at ASC, id ASC
 LIMIT ` + strconv.Itoa(batch) + s.forUpdateSkipLocked()
		rows, err := scanTriggerCandidates(ctx, tx, query, queryArgs)
		if err != nil {
			return nil, err
		}
		for i := range rows {
			row := &rows[i]
			if scoped {
				env, decoded := decodeTriggerEnv(row.envJSON)
				row.t.TriggerEnv = env
				// safety: the oldest trigger stays pending for other workers, and this
				// scope reports an empty queue until one of them takes it.
				if !decoded || !scope.admits(&row.t) {
					if commitErr := tx.Commit(); commitErr != nil {
						return nil, commitErr
					}
					return nil, notFound("claimable trigger", "")
				}
			}
			repo := triggerRunRepository(&row.t, row.envJSON)
			if filtered && !match.Evaluate(profile, match.Demand{Repo: &repo, Trigger: true}).OK() {
				continue
			}
			t, argsJSON, envJSON, createdNS, found = row.t, row.argsJSON, row.envJSON, row.createdNS, true
			break
		}
		if len(rows) < batch {
			break
		}
		last := rows[len(rows)-1]
		after = &triggerCursor{createdNS: last.createdNS, id: last.t.ID}
	}
	if !found {
		if commitErr := tx.Commit(); commitErr != nil {
			return nil, commitErr
		}
		return nil, notFound("claimable trigger", "")
	}
	if err := reserveTriggerCreditsTx(ctx, tx, claimant, t.Team, t.ID, now); err != nil {
		return nil, err
	}

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
	if err := mintTriggerClaimTokenTx(ctx, tx, claimant, &t, now); err != nil {
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

// HeartbeatTrigger charges a metered claim through the heartbeat before
// extending its lease. It returns whether cancel was requested.
// ErrNotFound when not claimed.
func (s *Store) HeartbeatTrigger(ctx context.Context, id string, lease time.Duration) (cancelled bool, err error) {
	return s.defaultTenant().HeartbeatTrigger(ctx, id, lease)
}

// HeartbeatTrigger is [Store.HeartbeatTrigger] confined to t's team.
func (t *Tenant) HeartbeatTrigger(ctx context.Context, id string, lease time.Duration) (cancelled bool, err error) {
	if lease <= 0 {
		lease = DefaultLeaseDuration
	}
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()

	var principal, tokenPrefix string
	var reservedAt, claimSeq int64
	var oldLease, cancelNS sql.NullInt64
	err = tx.QueryRowContext(ctx,
		`SELECT credit_reserved_at, lease_expires_at, cancel_requested_at,
		        claim_principal, claim_token_prefix, claim_seq
		   FROM triggers WHERE team = ? AND id = ? AND status = ?`+tx.forUpdate(),
		string(t.team), id, triggerStatusClaimed).Scan(&reservedAt, &oldLease, &cancelNS,
		&principal, &tokenPrefix, &claimSeq)
	if errors.Is(err, sql.ErrNoRows) {
		if _, fenced := TriggerClaimFenceFromContext(ctx); fenced {
			return false, ErrLockHeld
		}
		return false, ErrNotFound
	}
	if err != nil {
		return false, err
	}
	if fence, ok := TriggerClaimFenceFromContext(ctx); ok &&
		(fence.Claimant.Principal != principal || fence.Claimant.TokenPrefix != tokenPrefix ||
			fence.ClaimGeneration != claimSeq) {
		return false, ErrLockHeld
	}
	if reservedAt != 0 && !creditMeteringDisabled(ctx) {
		// safety: exhaustion finishes the run under the ledger, so the run
		// row is locked first, in the order lockTeamRunRowTx names.
		if _, err := lockTeamRunRowTx(ctx, tx, t.team, id); err != nil {
			return false, err
		}
		if err := lockCreditLedgerTx(ctx, tx); err != nil {
			return false, err
		}
	}
	now := time.Now()
	_, fenced := TriggerClaimFenceFromContext(ctx)
	if (fenced || reservedAt != 0) && (!oldLease.Valid || oldLease.Int64 <= now.UnixNano()) {
		return false, ErrLockHeld
	}
	if reservedAt != 0 && !creditMeteringDisabled(ctx) {
		if err := settleTriggerWindowTx(ctx, tx, t.team, id, reservedAt, now.UnixNano(), false, false); err != nil {
			if errors.Is(err, ErrInsufficientCredits) {
				if _, stopErr := tx.ExecContext(ctx,
					`UPDATE triggers SET status = ?, lease_expires_at = NULL, credit_reserved_at = 0,
					        credit_paid_seconds = 0, credit_paid_amount_micro = 0, credit_reservation_id = '',
					        cancel_requested_at = COALESCE(cancel_requested_at, ?)
					  WHERE id = ? AND team = ? AND status = ?`,
					triggerStatusFailed, now.UnixNano(), id, string(t.team), triggerStatusClaimed); stopErr != nil {
					return false, stopErr
				}
				if _, stopErr := tx.ExecContext(ctx, finishRunStmt,
					runStatusFailed, "trigger credits exhausted", now.UnixNano(), id, string(t.team)); stopErr != nil {
					return false, stopErr
				}
				if commitErr := tx.Commit(); commitErr != nil {
					return false, commitErr
				}
			}
			return false, err
		}
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE triggers SET lease_expires_at = ? WHERE team = ? AND id = ? AND status = ?`,
		now.Add(lease).UnixNano(), string(t.team), id, triggerStatusClaimed); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return cancelNS.Valid, nil
}

// RequestCancel cancels a run; idempotent. A trigger no consumer has
// claimed is finalized on the spot through [Store.CancelPendingTrigger];
// a claimed one is only flagged, and its holder sees the flag on its next
// lease renewal and winds the run down. A claim that lands between the
// two statements leaves the flag on a claimed trigger, which is the
// cooperative path again.
func (s *Store) RequestCancel(ctx context.Context, id string) error {
	return s.defaultTenant().RequestCancel(ctx, id)
}

// RequestCancel is [Store.RequestCancel] confined to t's team.
func (t *Tenant) RequestCancel(ctx context.Context, id string) error {
	if handled, err := t.s.requestControllerRunCancel(ctx, t.team, id, time.Now()); handled || err != nil {
		return err
	}
	cancelled, err := t.CancelPendingTrigger(ctx, id)
	if err != nil {
		return err
	}
	if cancelled {
		return nil
	}
	now := time.Now().UnixNano()
	res, err := t.s.exec(ctx,
		`UPDATE triggers
		    SET cancel_requested_at = COALESCE(cancel_requested_at, ?)
		  WHERE team = ? AND id = ?`,
		now, string(t.team), id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return notFound("trigger", id)
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
		    AND lease_expires_at < ?`+tx.forUpdate(),
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
	for _, id := range ids {
		if err := settleTriggerCreditsTx(ctx, tx, id, time.Unix(0, now), false); err != nil {
			return nil, err
		}
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE triggers
		    SET status = ?,
		        claimed_at = NULL,
		        lease_expires_at = NULL,
		        claim_principal = '',
		        claim_token_prefix = ''
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

// FinishTrigger closes a trigger, recording a failed run as a failed trigger; idempotent.
func (s *Store) FinishTrigger(ctx context.Context, id string) error {
	return s.defaultTenant().FinishTrigger(ctx, id)
}

// FinishTrigger is [Store.FinishTrigger] confined to t's team.
func (t *Tenant) FinishTrigger(ctx context.Context, id string) error {
	query := `UPDATE triggers SET status = CASE WHEN EXISTS
	   (SELECT 1 FROM runs WHERE runs.team = triggers.team AND runs.id = triggers.id AND runs.status = 'failed')
	   THEN 'failed' ELSE 'done' END, lease_expires_at = NULL WHERE team = ? AND id = ?`
	args := []any{string(t.team), id}
	_, fenced := TriggerClaimFenceFromContext(ctx)
	if fence, ok := TriggerClaimFenceFromContext(ctx); ok {
		query += ` AND claim_principal = ? AND claim_token_prefix = ? AND claim_seq = ?
		             AND (status IN (?, ?) OR (status = ? AND lease_expires_at > ?))`
		args = append(args, fence.Claimant.Principal, fence.Claimant.TokenPrefix,
			fence.ClaimGeneration, triggerStatusDone, triggerStatusFailed, triggerStatusClaimed, time.Now().UnixNano())
	}
	finished, err := t.endTriggerClaim(ctx, id, false, query, args...)
	if err != nil || !fenced || finished {
		return err
	}
	return ErrLockHeld
}

// ListPendingTriggersForParent returns every pending trigger whose
// parent_run_id matches parentRunID, oldest first. Used by the
// laptop-local trigger loop to scope its claim queue to the run
// that started it -- without the filter, two parallel local runs
// would steal each other's children. Empty list when no candidates.
func (s *Store) ListPendingTriggersForParent(ctx context.Context, parentRunID string) ([]string, error) {
	return s.defaultTenant().ListPendingTriggersForParent(ctx, parentRunID)
}

// ListPendingTriggersForParent is [Store.ListPendingTriggersForParent] confined to t's team.
func (t *Tenant) ListPendingTriggersForParent(ctx context.Context, parentRunID string) ([]string, error) {
	now := time.Now()
	rows, err := t.s.query(ctx, `
SELECT id FROM triggers
 WHERE team = ? AND status = ? AND parent_run_id = ? AND available_at <= ?
	ORDER BY created_at ASC`, string(t.team), triggerStatusPending, parentRunID, now.UnixNano())
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

// CountPendingTriggers returns how many triggers are waiting to be claimed.
func (s *Store) CountPendingTriggers(ctx context.Context) (int, error) {
	return s.defaultTenant().CountPendingTriggers(ctx)
}

// ClaimSpecificTrigger flips a known pending trigger to 'claimed'
// without recording a claimant; ErrNotFound when not pending. Use it
// only in-process, where the caller and the store share a lifetime and
// no later request has to prove ownership.
func (s *Store) ClaimSpecificTrigger(ctx context.Context, id string, lease time.Duration) (*Trigger, error) {
	return s.ClaimSpecificTriggerFor(ctx, id, ClaimIdentity{}, lease)
}

// ClaimSpecificTriggerFor is ClaimSpecificTrigger with the claimant
// recorded, so [Store.PrincipalHoldsTriggerClaim] can later prove the
// claim and the writes it authorizes. A trigger id is the id of the run
// it creates, so this claim is what the claimant's whole run rests on.
// ErrNotFound when the trigger is not pending, and equally when it belongs
// to a team the claimant's credential does not, so naming another team's
// trigger id learns nothing about whether it exists.
func (s *Store) ClaimSpecificTriggerFor(ctx context.Context, id string, claimant ClaimIdentity, lease time.Duration) (*Trigger, error) {
	if lease <= 0 {
		lease = DefaultLeaseDuration
	}
	teamClause, teamArgs, err := s.claimTeamClause(ctx, claimant, "triggers.team")
	if err != nil {
		return nil, err
	}
	tx, err := s.beginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now()
	if err := cancelRequeuedCancelledTriggersTx(ctx, tx, now); err != nil {
		return nil, err
	}
	expires := now.Add(lease)
	// safety: the claimant replaces whatever a previous holder left, unbound
	// included; a pending row has no live claim, so keeping the old principal
	// would let it prove ownership of a claim it does not hold.
	// safety: a pinned trigger answers only its own credential, only for its
	// first claim and only inside the claim window, so a lapsed here-runner or a
	// credential left unused cannot take the run later.
	args := []any{
		triggerStatusClaimed, now.UnixNano(), expires.UnixNano(),
		claimant.Principal, claimant.TokenPrefix,
		id, triggerStatusPending, claimant.TokenPrefix, now.Add(-HereRunnerClaimWindow).UnixNano(),
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE triggers SET status = ?, claimed_at = ?, lease_expires_at = ?, claim_seq = claim_seq + 1,
		        claim_principal = ?, claim_token_prefix = ?
		  WHERE id = ? AND status = ? AND (runner_pin = '' OR (runner_pin = ? AND claim_seq = 0 AND created_at >= ?))
		    AND available_at <= ? AND cancel_requested_at IS NULL
		    AND credit_reserved_at = 0`+teamClause,
		append(append(args, now.UnixNano()), teamArgs...)...)
	if err != nil {
		return nil, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	if n == 0 {
		var prior int64
		lookupArgs := append([]any{id, triggerStatusPending}, teamArgs...)
		lookupErr := tx.QueryRowContext(ctx,
			`SELECT credit_reserved_at FROM triggers WHERE id = ? AND status = ?`+teamClause,
			lookupArgs...).Scan(&prior)
		if lookupErr == nil && prior != 0 {
			return nil, unsettledTriggerCredits(id)
		}
		if lookupErr != nil && !errors.Is(lookupErr, sql.ErrNoRows) {
			return nil, lookupErr
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return nil, notFound("trigger", id)
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
       idempotency_key, claim_seq, webhook_delivery, team
  FROM triggers WHERE id = ?`, id,
	).Scan(&t.ID, &t.Pipeline, &argsJSON, &t.TriggerSource, &t.TriggerUser,
		&envJSON, &t.GitBranch, &t.GitSHA, &t.Status, &createdNS, &parent,
		&t.Repo, &t.RepoURL, &t.GithubOwner, &t.GithubRepo, &repoInheritedInt, &t.RetryOf, &t.RetrySource, &t.ParentNodeID, &fullInt,
		&t.IdempotencyKey, &t.ClaimSeq, &t.WebhookDelivery, &t.Team); err != nil {
		return nil, err
	}
	if err := reserveTriggerCreditsTx(ctx, tx, claimant, t.Team, t.ID, now); err != nil {
		return nil, err
	}
	if err := mintTriggerClaimTokenTx(ctx, tx, claimant, &t, now); err != nil {
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
	return s.defaultTenant().GetTrigger(ctx, id)
}

// GetTrigger is [Store.GetTrigger] confined to t's team.
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

// FindSpawnedChildTriggerID returns the most-recent child trigger
// for (parentRunID, parentNodeID, pipeline), or "".
func (s *Store) FindSpawnedChildTriggerID(ctx context.Context, parentRunID, parentNodeID, pipeline string) (string, error) {
	return s.defaultTenant().FindSpawnedChildTriggerID(ctx, parentRunID, parentNodeID, pipeline)
}

// FindSpawnedChildTriggerID is [Store.FindSpawnedChildTriggerID] confined to t's team.
func (t *Tenant) FindSpawnedChildTriggerID(ctx context.Context, parentRunID, parentNodeID, pipeline string) (string, error) {
	if parentRunID == "" || parentNodeID == "" || pipeline == "" {
		return "", nil
	}
	var id string
	err := t.s.queryRow(
		ctx, `
SELECT id FROM triggers
 WHERE team = ? AND parent_run_id = ? AND parent_node_id = ? AND pipeline = ?
 ORDER BY created_at DESC
 LIMIT 1`, string(t.team), parentRunID, parentNodeID, pipeline,
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
	Statuses  []string // "pending"|"claimed"|"done"|"failed"
	Pipelines []string
	Repo      string // matches GITHUB_REPOSITORY in trigger_env
	Limit     int    // <=0 = 20
}

const (
	triggerRepoScanBatch = 200
	triggerRepoScanCap   = 5000
)

// ListTriggers returns triggers newest-first, filtered by f.
//
// Repo lives in a blob no dialect can filter on, so rows are read in
// pages and matched in Go until the limit is filled, over at most the
// newest 5,000 triggers that pass the other fields. Pages walk a keyset
// cursor on (created_at, id) rather than an offset, so a trigger created
// or deleted while the walk is in flight cannot shift a later page onto
// a row already read or past one not yet read. Stopping at the horizon
// with the limit unfilled is logged, since the caller cannot tell that
// from a genuinely empty result.
func (s *Store) ListTriggers(ctx context.Context, f TriggerFilter) ([]*Trigger, error) {
	return s.listTriggers(ctx, allTeams(), f)
}

// ListTriggers returns t's triggers newest-first, filtered by f, with the
// same paging and repo matching as [Store.ListTriggers].
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

// CountPendingNodes returns the count of unclaimed ready nodes.
func (s *Store) CountPendingNodes(ctx context.Context) (int, error) {
	var n int
	err := s.queryRow(
		ctx,
		`SELECT COUNT(*) FROM nodes
		  WHERE ready_at IS NOT NULL AND (claimed_by IS NULL OR claimed_by = '')
		    AND `+nodeNotDone+` AND `+nodeRunActiveSQL("nodes."),
	).Scan(&n)
	return n, err
}

// Queue states a node occupies before it reaches a terminal outcome. The set
// is closed, so a caller may use it as a metric label without growing the
// series count.
const (
	// QueueStateWaiting is a node whose dependencies have not all finished.
	QueueStateWaiting = "waiting"
	// QueueStateReady is a node any eligible runner may claim.
	QueueStateReady = "ready"
	// QueueStateClaimed is a node a runner holds but has not started.
	QueueStateClaimed = "claimed"
	// QueueStateRunning is a node a runner is executing.
	QueueStateRunning = "running"
	// QueueStateApprovalPending is a node waiting on a human decision.
	QueueStateApprovalPending = "approval_pending"
)

// QueueStates lists every state [Store.CountNodesByQueueState] reports, in the
// order a node passes through them.
func QueueStates() []string {
	return []string{
		QueueStateWaiting, QueueStateReady, QueueStateClaimed,
		QueueStateRunning, QueueStateApprovalPending,
	}
}

// CountNodesByQueueState counts the nodes sitting in each queue state, keyed
// by the [QueueStates] names. Terminal nodes are absent: the answer is the
// work still outstanding, which is what a queue-depth alert reads. The read is
// bounded by the outstanding-node index rather than the table.
func (s *Store) CountNodesByQueueState(ctx context.Context) (map[string]int, error) {
	var waiting, ready, claimed, running, approval int
	err := s.queryRow(ctx,
		`SELECT
                    COALESCE(SUM(CASE WHEN status = ? AND ready_at IS NULL THEN 1 ELSE 0 END), 0),
                    COALESCE(SUM(CASE WHEN status = ? AND ready_at IS NOT NULL
                                      AND (claimed_by IS NULL OR claimed_by = '') THEN 1 ELSE 0 END), 0),
                    COALESCE(SUM(CASE WHEN status = ? AND claimed_by IS NOT NULL
                                      AND claimed_by != '' THEN 1 ELSE 0 END), 0),
                    COALESCE(SUM(CASE WHEN status = ? THEN 1 ELSE 0 END), 0),
                    COALESCE(SUM(CASE WHEN status = ? THEN 1 ELSE 0 END), 0)
                   FROM nodes
                  WHERE `+nodeNotDone+` AND `+nodeRunActiveSQL("nodes."),
		nodeStatusPending, nodeStatusPending, nodeStatusPending,
		nodeStatusRunning, NodeStatusApprovalPending,
	).Scan(&waiting, &ready, &claimed, &running, &approval)
	if err != nil {
		return nil, err
	}
	return map[string]int{
		QueueStateWaiting:         waiting,
		QueueStateReady:           ready,
		QueueStateClaimed:         claimed,
		QueueStateRunning:         running,
		QueueStateApprovalPending: approval,
	}, nil
}

// NodeMetering says whether the credential that claimed a node was metered.
type NodeMetering int

const (
	// MeteringUnknown means the claiming credential is no longer on file, so
	// the node is attributed to neither side rather than guessed at.
	MeteringUnknown NodeMetering = iota
	// MeteringFree is a credential the operator never marked metered.
	MeteringFree
	// MeteringPaid is a metered credential, whose work the ledger bills.
	MeteringPaid
)

// NodeSettlement is what one node's own row says about settling it: how long it
// ran, the credential that claimed it, how that credential was metered, and
// whether a credit reservation is still open against it.
type NodeSettlement struct {
	Seconds          float64
	ClaimTokenPrefix string
	Metering         NodeMetering
	// ChargeWindowOpen reports a reservation the ledger has not released. It
	// stays true for a node whose claiming credential was revoked, so a
	// terminal node always has something to settle against.
	ChargeWindowOpen bool
}

// NodeSettlement reads what settling one node needs from the node's own row.
// The claim recorded there names the credential the ledger priced the work
// against, so a finish posted by another principal settles the same way.
func (s *Store) NodeSettlement(ctx context.Context, runID, nodeID string) (NodeSettlement, error) {
	return s.defaultTenant().NodeSettlement(ctx, runID, nodeID)
}

// NodeSettlement is [Store.NodeSettlement] confined to t's team.
func (t *Tenant) NodeSettlement(ctx context.Context, runID, nodeID string) (NodeSettlement, error) {
	var out NodeSettlement
	var startedAt, finishedAt sql.NullInt64
	var chargedThrough int64
	var metered sql.NullBool
	err := t.s.queryRow(ctx,
		`SELECT started_at, finished_at, claim_token_prefix, credit_charged_through,
                        (SELECT metered FROM tokens WHERE tokens.team = nodes.team AND prefix = nodes.claim_token_prefix)
                   FROM nodes WHERE team = ? AND run_id = ? AND node_id = ?`,
		string(t.team), runID, nodeID,
	).Scan(&startedAt, &finishedAt, &out.ClaimTokenPrefix, &chargedThrough, &metered)
	if err != nil {
		return out, err
	}
	out.ChargeWindowOpen = chargedThrough != 0
	if metered.Valid {
		out.Metering = MeteringFree
		if metered.Bool {
			out.Metering = MeteringPaid
		}
	}
	if startedAt.Valid && finishedAt.Valid && finishedAt.Int64 > startedAt.Int64 {
		out.Seconds = time.Duration(finishedAt.Int64 - startedAt.Int64).Seconds()
	}
	return out, nil
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

// CreateApproval inserts a pending approval on a default-team node and flips
// its status to approval_pending in one txn. Re-runs a gate from scratch.
func (s *Store) CreateApproval(ctx context.Context, a Approval) error {
	return s.defaultTenant().CreateApproval(ctx, a)
}

// CreateApproval inserts a pending approval on a node of one of t's runs and
// flips the node's status to approval_pending in one txn. Re-runs a gate from
// scratch. A run of another team reads as [ErrNotFound].
func (t *Tenant) CreateApproval(ctx context.Context, a Approval) error {
	if a.OnTimeout == "" {
		a.OnTimeout = ApprovalOnTimeoutFail
	}
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := assertRunBelongsToTeamTx(ctx, tx, t.team, a.RunID); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `
INSERT INTO approvals (team, run_id, node_id, requested_at, message, timeout_ms, on_timeout)
VALUES (?,?,?,?,?,?,?)
ON CONFLICT(run_id, node_id) DO UPDATE SET
    requested_at = excluded.requested_at,
    message      = excluded.message,
    timeout_ms   = excluded.timeout_ms,
    on_timeout   = excluded.on_timeout,
    approver     = '',
    resolved_at  = NULL,
    resolution   = '',
    comment      = ''
  WHERE approvals.team = excluded.team`,
		string(t.team), a.RunID, a.NodeID, a.RequestedAt.UnixNano(),
		a.Message, a.TimeoutMS, a.OnTimeout)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		if err != nil {
			return err
		}
		return notFound("run", a.RunID)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE nodes SET status = ? WHERE team = ? AND run_id = ? AND node_id = ?`,
		NodeStatusApprovalPending, string(t.team), a.RunID, a.NodeID); err != nil {
		return err
	}
	return tx.Commit()
}

// GetApproval returns the row of a default-team node, or ErrNotFound.
func (s *Store) GetApproval(ctx context.Context, runID, nodeID string) (*Approval, error) {
	return s.defaultTenant().GetApproval(ctx, runID, nodeID)
}

// GetApproval returns the row of a node of one of t's runs, or ErrNotFound.
func (t *Tenant) GetApproval(ctx context.Context, runID, nodeID string) (*Approval, error) {
	row := t.s.queryRow(ctx, `
SELECT run_id, node_id, requested_at, message, timeout_ms, on_timeout,
       approver, resolved_at, resolution, comment
  FROM approvals WHERE team = ? AND run_id = ? AND node_id = ?`, string(t.team), runID, nodeID)
	return scanApproval(row)
}

// ResolveApproval stamps resolution on a pending default-team row.
// ErrNotFound when missing; ErrLockHeld when already resolved.
func (s *Store) ResolveApproval(ctx context.Context, runID, nodeID, resolution, approver, comment string) (*Approval, error) {
	return s.defaultTenant().ResolveApproval(ctx, runID, nodeID, resolution, approver, comment)
}

// ResolveApproval stamps resolution on a pending row of one of t's runs.
// ErrNotFound when missing or another team's; ErrLockHeld when already
// resolved.
func (t *Tenant) ResolveApproval(ctx context.Context, runID, nodeID, resolution, approver, comment string) (*Approval, error) {
	if handled, err := t.s.resolveControllerApproval(ctx, t.team, runID, nodeID, resolution, approver, comment); err != nil {
		return nil, err
	} else if handled {
		return t.GetApproval(ctx, runID, nodeID)
	}
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	var resolvedNS sql.NullInt64
	err = tx.QueryRowContext(ctx,
		`SELECT resolved_at FROM approvals WHERE team = ? AND run_id = ? AND node_id = ?`,
		string(t.team), runID, nodeID).Scan(&resolvedNS)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, notFound("approval", runID+"/"+nodeID)
		}
		return nil, err
	}
	if resolvedNS.Valid {
		return nil, ErrLockHeld
	}

	if _, err := tx.ExecContext(ctx, `
UPDATE approvals
   SET resolution = ?, approver = ?, comment = ?, resolved_at = ?
 WHERE team = ? AND run_id = ? AND node_id = ? AND resolved_at IS NULL`,
		resolution, approver, comment, time.Now().UnixNano(),
		string(t.team), runID, nodeID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return t.GetApproval(ctx, runID, nodeID)
}

// ListApprovalsForRun returns a default-team run's rows in request order.
func (s *Store) ListApprovalsForRun(ctx context.Context, runID string) ([]*Approval, error) {
	return s.defaultTenant().ListApprovalsForRun(ctx, runID)
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

// ListPendingApprovals returns the default team's unresolved approvals
// oldest-first.
func (s *Store) ListPendingApprovals(ctx context.Context) ([]*Approval, error) {
	return s.defaultTenant().ListPendingApprovals(ctx)
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

type triggerCursor struct {
	createdNS int64
	id        string
}

type triggerCandidate struct {
	t         Trigger
	argsJSON  []byte
	envJSON   []byte
	createdNS int64
}

func scanTriggerCandidates(ctx context.Context, tx *storeTx, query string, args []any) (_ []triggerCandidate, err error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	var out []triggerCandidate
	for rows.Next() {
		var c triggerCandidate
		var parent sql.NullString
		var fullInt, repoInheritedInt int
		if err := rows.Scan(
			&c.t.ID, &c.t.Pipeline, &c.argsJSON, &c.t.TriggerSource, &c.t.TriggerUser,
			&c.envJSON, &c.t.GitBranch, &c.t.GitSHA, &c.t.Status, &c.createdNS, &parent,
			&c.t.Repo, &c.t.RepoURL, &c.t.GithubOwner, &c.t.GithubRepo, &repoInheritedInt, &c.t.RetryOf, &c.t.RetrySource, &c.t.ParentNodeID, &fullInt,
			&c.t.IdempotencyKey, &c.t.ClaimSeq, &c.t.WebhookDelivery, &c.t.Team, &c.t.WebhookReplayKey,
		); err != nil {
			return nil, err
		}
		c.t.ParentRunID = parent.String
		c.t.Full = fullInt != 0
		c.t.RepoInherited = repoInheritedInt != 0
		out = append(out, c)
	}
	return out, rows.Err()
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
