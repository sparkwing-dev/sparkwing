package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

// Dialect identifies the SQL flavor a Store is backed by. The Store
// type itself is dialect-agnostic; methods branch on Dialect at the
// handful of points where SQLite and Postgres syntax diverge
// (placeholders, upsert clauses, row-level locks). Most query strings
// are otherwise identical between the two.
type Dialect int

const (
	// DialectSQLite is the modernc.org/sqlite-backed dialect: single
	// writer serialized at the database level, `?` placeholders,
	// INSERT OR REPLACE upserts.
	DialectSQLite Dialect = iota
	// DialectPostgres is the pgx/stdlib-backed dialect: multi-writer,
	// `$N` placeholders, ON CONFLICT upserts, explicit FOR UPDATE
	// SKIP LOCKED on hot claim paths.
	DialectPostgres
)

// String returns a short stable name suitable for test subtest names
// and log fields.
func (d Dialect) String() string {
	switch d {
	case DialectSQLite:
		return "sqlite"
	case DialectPostgres:
		return "postgres"
	default:
		return "unknown"
	}
}

// safety: SQLite's variadic max() is a scalar function; Postgres spells that
// GREATEST and reserves max for an aggregate, which no WHERE clause accepts.
func (s *Store) greatest() string {
	if s.dialect == DialectPostgres {
		return "GREATEST"
	}
	return "max"
}

func rewritePh(dialect Dialect, q string) string {
	if dialect != DialectPostgres {
		return q
	}
	var b strings.Builder
	b.Grow(len(q) + 8)
	inStr := false
	n := 0
	for i := 0; i < len(q); i++ {
		c := q[i]
		if c == '\'' {
			inStr = !inStr
			b.WriteByte(c)
			continue
		}
		if c == '?' && !inStr {
			n++
			b.WriteByte('$')
			b.WriteString(itoa(n))
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

type sqlRunner interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
	BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
}

func (s *Store) sqlRunner() sqlRunner {
	if s.conn != nil {
		return s.conn
	}
	return s.db
}

func (s *Store) exec(ctx context.Context, q string, args ...any) (sql.Result, error) {
	var result sql.Result
	err := s.inTx(ctx, func(tx *storeTx) (err error) {
		result, err = tx.ExecContext(ctx, q, args...)
		return err
	})
	return result, err
}

func (s *Store) execNoCtx(q string, args ...any) (sql.Result, error) {
	return s.exec(context.Background(), q, args...)
}

func (s *Store) query(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	if err := refuseLedgerWrite(q); err != nil {
		return nil, err
	}
	return s.sqlRunner().QueryContext(ctx, rewritePh(s.dialect, q), args...)
}

func (s *Store) queryNoCtx(q string, args ...any) (*sql.Rows, error) {
	return s.query(context.Background(), q, args...)
}

func (s *Store) queryRow(ctx context.Context, q string, args ...any) rowScanner {
	if err := refuseLedgerWrite(q); err != nil {
		return refusedLedgerRow{err}
	}
	return s.sqlRunner().QueryRowContext(ctx, rewritePh(s.dialect, q), args...)
}

func (s *Store) queryRowNoCtx(q string, args ...any) rowScanner {
	return s.queryRow(context.Background(), q, args...)
}

type storeTx struct {
	operator             *Operator
	authorityLocks       []rowLock
	insertedAuthorities  []capability.AuthorityRef
	finishedLocalSecrets []localSecretKey
	appAdmission         *GitHubAppAdmission
	scheduleAdmission    *scheduleAdmission
	unpricedDemand       *UnitDemand
	clock                func(context.Context) (time.Time, error)
	err                  error
	cancelingTeam        Team
	tx                   *sql.Tx
	dialect              Dialect
	store                *Store
	snapshotRead         bool
	rotatingRoot         *capability.Capability
	createdTeam          *TeamInfo
	blobIO               *blobIO
	matchingRuns         map[matchingRunKey]*matchingRun
}

func (s *Store) now() time.Time {
	if s.clock != nil {
		return s.clock().UTC()
	}
	return time.Now().UTC()
}

// Read-only decisions may never commit, so clock failure must refuse construction.
func (s *Store) snapshotTx(ctx context.Context, raw *sql.Tx, read bool) (*storeTx, error) {
	t := &storeTx{operator: &Operator{s: s}, tx: raw, dialect: s.dialect, store: s, snapshotRead: read}
	if read && s.dialect == DialectSQLite {
		// SQLite establishes its snapshot on the first table read.
		var version int
		if err := t.QueryRowContext(ctx, `SELECT COUNT(*) FROM sparkwing_schema_version`).Scan(&version); err != nil {
			return nil, errors.Join(err, t.Rollback())
		}
	}
	now, err := t.readClock(ctx)
	if err != nil {
		return nil, errors.Join(err, t.Rollback())
	}
	t.clock = t.readClock
	if read {
		t.clock = func(context.Context) (time.Time, error) { return now, nil }
	}
	return t, nil
}

// safety: read snapshots stay fixed while write fences use fresh time; clock
// failure poisons SQL access and commit even if the caller ignores the time.
func (t *storeTx) now(ctx context.Context) time.Time {
	if t.err != nil {
		return time.Time{}
	}
	now, err := t.clock(ctx)
	t.err = err
	return now
}

func (t *storeTx) readClock(ctx context.Context) (time.Time, error) {
	s := t.store
	if s.pinnedClock != nil {
		return s.pinnedClock().UTC(), nil
	}
	if s.dialect == DialectSQLite && s.clock != nil {
		return s.now(), nil
	}
	var ns int64
	if err := t.QueryRowContext(ctx, databaseTimeSQL(s.dialect)).Scan(&ns); err != nil {
		return time.Time{}, err
	}
	return time.Unix(0, ns).UTC(), nil
}

func databaseTimeSQL(d Dialect) string {
	if d == DialectPostgres {
		return `SELECT (extract(epoch FROM clock_timestamp())*1000000000)::bigint`
	}
	return `SELECT CAST(strftime('%s','now') AS BIGINT)*1000000000+CAST(substr(strftime('%f','now'),4) AS BIGINT)*1000000`
}

func (s *Store) databaseNow(ctx context.Context) (time.Time, error) {
	tx, err := s.beginSnapshotReadTx(ctx)
	if err != nil {
		return time.Time{}, err
	}
	defer rollbackOrLog(tx)
	return tx.now(ctx), tx.err
}

// safety: Postgres takes a fresh snapshot per statement unless the isolation
// is named, so a write landing mid-read is counted by one statement and not
// another. SQLite is already one snapshot per transaction and its driver
// refuses the level, so it keeps the default.
func (s *Store) beginSnapshotReadTx(ctx context.Context) (*storeTx, error) {
	if s.dialect != DialectPostgres {
		return s.beginStoreTx(ctx, true)
	}
	tx, err := s.sqlRunner().BeginTx(ctx, &sql.TxOptions{
		Isolation: sql.LevelRepeatableRead,
		ReadOnly:  true,
	})
	if err != nil {
		return nil, err
	}
	return s.snapshotTx(ctx, tx, true)
}

var errControllerFenced = errors.New("controller superseded by promotion")

func (s *Store) beginTx(ctx context.Context) (*storeTx, error) {
	return s.beginStoreTx(ctx, false)
}

func (s *Store) beginStoreTx(ctx context.Context, read bool) (*storeTx, error) {
	tx, err := s.sqlRunner().BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	if s.controllerEpoch != 0 {
		var epoch int64
		err := tx.QueryRowContext(ctx, controllerEpochLockSQL).Scan(&epoch)
		if err != nil || epoch != s.controllerEpoch {
			fenced := err == nil || errors.Is(err, sql.ErrNoRows)
			err = errors.Join(err, rollbackTx(tx))
			if fenced {
				err = errors.Join(fmt.Errorf("controller epoch lost (promoted=%d, current=%d): %w", s.controllerEpoch, epoch, errControllerFenced), err)
				s.controllerFailStop(err)
			}
			return nil, err
		}
	}
	return s.snapshotTx(ctx, tx, read)
}

func (t *storeTx) Commit() error {
	if t.err != nil {
		return errors.Join(t.err, t.Rollback())
	}
	if err := t.tx.Commit(); err != nil {
		return err
	}
	for _, key := range t.finishedLocalSecrets {
		t.store.localSecrets.Delete(key)
	}
	return nil
}
func (t *storeTx) Rollback() error { return rollbackTx(t.tx) }

// Cancellation may have already rolled back the transaction in database/sql.
func rollbackTx(tx *sql.Tx) error {
	err := tx.Rollback()
	if errors.Is(err, sql.ErrTxDone) {
		return nil
	}
	return err
}

func (t *storeTx) checkSQL(q string) error {
	if t.err != nil {
		return t.err
	}
	return refuseLedgerWrite(q)
}

func (t *storeTx) Exec(q string, args ...any) (sql.Result, error) {
	return t.ExecContext(context.Background(), q, args...)
}

func (t *storeTx) ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error) {
	if err := t.checkSQL(q); err != nil {
		return nil, err
	}
	return t.tx.ExecContext(ctx, rewritePh(t.dialect, q), args...)
}

func (t *storeTx) Query(q string, args ...any) (*sql.Rows, error) {
	return t.QueryContext(context.Background(), q, args...)
}

func (t *storeTx) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	if err := t.checkSQL(q); err != nil {
		return nil, err
	}
	return t.tx.QueryContext(ctx, rewritePh(t.dialect, q), args...)
}

func (t *storeTx) QueryRow(q string, args ...any) rowScanner {
	return t.QueryRowContext(context.Background(), q, args...)
}

func (t *storeTx) QueryRowContext(ctx context.Context, q string, args ...any) rowScanner {
	if err := t.checkSQL(q); err != nil {
		return refusedLedgerRow{err}
	}
	return t.tx.QueryRowContext(ctx, rewritePh(t.dialect, q), args...)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [10]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
