package store

import (
	"context"
	"database/sql"
	"strings"
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

// DetectDialect infers the dialect from a DSN string. A URL with a
// `postgres://` or `postgresql://` scheme is Postgres; anything else is
// treated as a SQLite path (the historical default). Callers that need
// to be explicit should use the Open / OpenPostgres constructors
// directly rather than relying on detection.
func DetectDialect(dsn string) Dialect {
	low := strings.ToLower(strings.TrimSpace(dsn))
	if strings.HasPrefix(low, "postgres://") || strings.HasPrefix(low, "postgresql://") {
		return DialectPostgres
	}
	return DialectSQLite
}

// forUpdateSkipLocked returns the row-locking suffix to append to a
// SELECT used as the read half of a claim/reap transaction. SQLite
// serializes writers at the database level and needs no suffix; on
// Postgres the suffix is " FOR UPDATE SKIP LOCKED" so concurrent
// claimants pick disjoint rows without blocking on each other.
//
// Append to the SELECT before any closing parenthesis; do not insert
// between SELECT and FROM. Composes with `LIMIT` and `ORDER BY`
// clauses by appearing after them.
func (s *Store) forUpdateSkipLocked() string {
	if s.dialect == DialectPostgres {
		return " FOR UPDATE SKIP LOCKED"
	}
	return ""
}

// insertionOrderColumn returns the column to ORDER BY when callers
// want rows in insertion (storage) order. SQLite exposes the implicit
// `rowid`; Postgres exposes `ctid`, the physical tuple identifier.
// Both are stable for read-only ordering within a transaction. Use
// for display ordering where deterministic semantic ordering is not
// available.
func (s *Store) insertionOrderColumn() string {
	if s.dialect == DialectPostgres {
		return "ctid"
	}
	return "rowid"
}

// forUpdate returns the row-locking suffix for the read half of a
// transaction that serializes on a specific known row (as opposed to
// the first-eligible-row pattern handled by forUpdateSkipLocked).
// SQLite returns empty for the same reason as above.
func (s *Store) forUpdate() string {
	if s.dialect == DialectPostgres {
		return " FOR UPDATE"
	}
	return ""
}

// rewritePh rewrites `?` placeholders to `$1`, `$2`, ... when dialect
// is Postgres. SQLite (and any unrecognized dialect) returns q
// unchanged. Question marks inside single-quoted SQL string literals
// are skipped so embedded JSON or regex literals survive.
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

// storeTx wraps the underlying *sql.Tx so transactional queries get
// the same placeholder rewriting that *Store does on the bare
// connection. Its method names match the *sql.Tx surface so call
// sites only change the variable type, not the call shape.
type storeTx struct {
	tx      *sql.Tx
	dialect Dialect
}

func (s *Store) beginTx(ctx context.Context) (*storeTx, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	return &storeTx{tx: tx, dialect: s.dialect}, nil
}

func (t *storeTx) Commit() error   { return t.tx.Commit() }
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

// itoa formats small positive integers without going through strconv;
// keeps the placeholder rewriter allocation-light on the hot query
// path.
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
