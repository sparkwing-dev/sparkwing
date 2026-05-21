package store

import (
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

// ph rewrites `?` placeholders to `$1`, `$2`, ... when the Store is
// backed by Postgres. SQLite path returns q unchanged. Question marks
// inside single-quoted SQL string literals are skipped so embedded JSON
// or regex literals survive.
//
// Use ph at the call site for every query string the dialects share.
func (s *Store) ph(q string) string {
	if s.dialect != DialectPostgres {
		return q
	}
	var b strings.Builder
	b.Grow(len(q) + 8)
	inStr := false
	n := 0
	for i := 0; i < len(q); i++ {
		c := q[i]
		if c == '\'' {
			// SQL single-quote escape is doubled (''); just flip the
			// flag — a doubled '' will flip back into-string on the
			// next char which is fine for placeholder rewriting.
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
