package store

import (
	"context"
	"database/sql"
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

func (s *Store) forUpdate() string {
	if s.dialect == DialectPostgres {
		return " FOR UPDATE"
	}
	return ""
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

func (s *Store) exec(ctx context.Context, q string, args ...any) (sql.Result, error) {
	return s.db.ExecContext(ctx, rewritePh(s.dialect, q), args...)
}

func (s *Store) execNoCtx(q string, args ...any) (sql.Result, error) {
	return s.db.Exec(rewritePh(s.dialect, q), args...)
}

func (s *Store) query(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	return s.db.QueryContext(ctx, rewritePh(s.dialect, q), args...)
}

func (s *Store) queryNoCtx(q string, args ...any) (*sql.Rows, error) {
	return s.db.Query(rewritePh(s.dialect, q), args...)
}

func (s *Store) queryRow(ctx context.Context, q string, args ...any) *sql.Row {
	return s.db.QueryRowContext(ctx, rewritePh(s.dialect, q), args...)
}

func (s *Store) queryRowNoCtx(q string, args ...any) *sql.Row {
	return s.db.QueryRow(rewritePh(s.dialect, q), args...)
}

type storeTx struct {
	clockErr     error
	ctx          context.Context
	deletingTeam Team
	tx           *sql.Tx
	dialect      Dialect
	store        *Store
	snapshotRead bool
	rotatingRoot *capability.Capability
	createdTeam  *TeamInfo
}

// SetTestClock overrides the SQLite store clock until the test ends.
func (s *Store) SetTestClock(t interface{ Cleanup(func()) }, clock func() time.Time) {
	original := s.clock
	t.Cleanup(func() { s.clock = original })
	s.clock = clock
}

func (s *Store) now() time.Time {
	if s.clock != nil {
		return s.clock().UTC()
	}
	return time.Now().UTC()
}

func (t *storeTx) now() time.Time {
	if t.dialect == DialectSQLite && t.store.clock != nil {
		return t.store.now()
	}
	if t.clockErr != nil {
		return time.Time{}
	}
	var ns int64
	t.clockErr = t.QueryRowContext(t.ctx, databaseTimeSQL(t.dialect)).Scan(&ns)
	if t.clockErr != nil {
		return time.Time{}
	}
	return time.Unix(0, ns).UTC()
}

func databaseTimeSQL(d Dialect) string {
	if d == DialectPostgres {
		return `SELECT (extract(epoch FROM clock_timestamp())*1000000000)::bigint`
	}
	return `SELECT CAST(strftime('%s','now') AS BIGINT)*1000000000+CAST(substr(strftime('%f','now'),4) AS BIGINT)*1000000`
}

func (s *Store) databaseNow(ctx context.Context) (time.Time, error) {
	if s.dialect == DialectSQLite && s.clock != nil {
		return s.now(), nil
	}
	var ns int64
	if err := s.queryRow(ctx, databaseTimeSQL(s.dialect)).Scan(&ns); err != nil {
		return time.Time{}, err
	}
	return time.Unix(0, ns).UTC(), nil
}

// safety: Postgres takes a fresh snapshot per statement unless the isolation
// is named, so a write landing mid-read is counted by one statement and not
// another. SQLite is already one snapshot per transaction and its driver
// refuses the level, so it keeps the default.
func (s *Store) beginSnapshotReadTx(ctx context.Context) (*storeTx, error) {
	if s.dialect != DialectPostgres {
		tx, err := s.beginTx(ctx)
		if err == nil {
			tx.snapshotRead = true
		}
		return tx, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{
		Isolation: sql.LevelRepeatableRead,
		ReadOnly:  true,
	})
	if err != nil {
		return nil, err
	}
	return &storeTx{tx: tx, ctx: ctx, dialect: s.dialect, store: s, snapshotRead: true}, nil
}

func (s *Store) beginTx(ctx context.Context) (*storeTx, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	return &storeTx{tx: tx, ctx: ctx, dialect: s.dialect, store: s}, nil
}

func (t *storeTx) forUpdate() string {
	if t.dialect == DialectPostgres && !t.snapshotRead {
		return " FOR UPDATE"
	}
	return ""
}

func (t *storeTx) forKeyShare() string {
	if t.dialect == DialectPostgres && !t.snapshotRead {
		return " FOR KEY SHARE"
	}
	return ""
}

func (t *storeTx) forShare() string {
	if t.dialect == DialectPostgres && !t.snapshotRead {
		return " FOR SHARE"
	}
	return ""
}

// safety: FOR UPDATE also blocks the key-share lock an event insert's foreign
// key takes on its run while holding the run's sequence lock, so a transaction
// that locks its run and then appends an event deadlocks with that insert.
func (t *storeTx) forNoKeyUpdate() string {
	if t.dialect == DialectPostgres && !t.snapshotRead {
		return " FOR NO KEY UPDATE"
	}
	return ""
}

func (t *storeTx) Commit() error {
	if t.clockErr != nil {
		_ = t.tx.Rollback()
		return t.clockErr
	}
	return t.tx.Commit()
}
func (t *storeTx) Rollback() error { return t.tx.Rollback() }

func (t *storeTx) Exec(q string, args ...any) (sql.Result, error) {
	return t.tx.Exec(rewritePh(t.dialect, q), args...)
}

func (t *storeTx) ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error) {
	return t.tx.ExecContext(ctx, rewritePh(t.dialect, q), args...)
}

func (t *storeTx) Query(q string, args ...any) (*sql.Rows, error) {
	return t.tx.Query(rewritePh(t.dialect, q), args...)
}

func (t *storeTx) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	return t.tx.QueryContext(ctx, rewritePh(t.dialect, q), args...)
}

func (t *storeTx) QueryRow(q string, args ...any) *sql.Row {
	return t.tx.QueryRow(rewritePh(t.dialect, q), args...)
}

func (t *storeTx) QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row {
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
