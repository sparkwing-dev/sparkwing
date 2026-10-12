package store_test

import (
	"database/sql"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

// The two baseline files are written by hand, so this is what keeps them one
// schema: every table, column, key, index and seed row a fresh SQLite store
// has, a fresh Postgres store has too.
func TestBaselineSchemaIsTheSameOnBothDialects(t *testing.T) {
	lite := storetest.OpenSQLite(t)
	pg := storetest.OpenPostgres(t)
	for _, st := range []*store.Store{lite, pg} {
		var version int
		if err := st.DB().QueryRow(`SELECT MAX(version) FROM sparkwing_schema_version`).Scan(&version); err != nil {
			t.Fatal(err)
		}
		if version != store.ExpectedSchemaVersion() {
			t.Errorf("%s records schema version %d, want %d", st.Dialect(), version, store.ExpectedSchemaVersion())
		}
	}
	want, got := sqliteShape(t, lite.DB()), postgresShape(t, pg.DB())
	for _, line := range lineDiff(want, got) {
		t.Error(line)
	}
}

// A store this build did not create is refused whole, with the remedy in the
// message, rather than read with a schema it may not have.
func TestOpenRefusesAStoreFromAnotherBaseline(t *testing.T) {
	target := storetest.New(t)
	st, err := target.TryOpen()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(storetest.Rebind(st,
		`INSERT INTO sparkwing_schema_version (version, applied_at) VALUES (?, ?)`), 100, 1); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = target.TryOpen()
	if err == nil {
		t.Fatal("Open accepted a store recording schema version 100")
	}
	remedy := "drop the database"
	if st.Dialect() == store.DialectSQLite {
		remedy = "delete "
	}
	for _, want := range []string{"predates the baseline", remedy} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Open error %q does not say %q", err, want)
		}
	}
}

func sqliteShape(t *testing.T, db *sql.DB) []string {
	t.Helper()
	var out []string
	for _, table := range column(t, db, `SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`) {
		for _, r := range rows(t, db, `SELECT name, type, "notnull", COALESCE(dflt_value, ''), pk FROM pragma_table_info(?)`, table) {
			out = append(out, fmt.Sprintf("column %s.%s %s notnull=%t default=%s", table, r[0], typeClass(r[1]),
				r[2] == "1" || r[4] != "0", normalizeDefault(r[3])))
		}
		pk := rows(t, db, `SELECT name FROM pragma_table_info(?) WHERE pk > 0 ORDER BY pk`, table)
		if len(pk) > 0 {
			out = append(out, fmt.Sprintf("primary key %s(%s)", table, joinFirst(pk)))
		}
		for _, r := range rows(t, db, `SELECT "table", group_concat("from"), group_concat(COALESCE("to", '')), on_delete
FROM pragma_foreign_key_list(?) GROUP BY id`, table) {
			out = append(out, fmt.Sprintf("foreign key %s(%s) -> %s(%s) on delete %s", table, r[1], r[0], r[2], strings.ToLower(r[3])))
		}
		for _, ix := range rows(t, db, `SELECT name, "unique", origin, partial FROM pragma_index_list(?)`, table) {
			cols := joinFirst(rows(t, db, `SELECT COALESCE(name, '<expr>') FROM pragma_index_info(?) ORDER BY seqno`, ix[0]))
			switch ix[2] {
			case "c":
				out = append(out, fmt.Sprintf("index %s on %s(%s) unique=%t partial=%t", ix[0], table, cols, ix[1] == "1", ix[3] == "1"))
			case "u":
				out = append(out, fmt.Sprintf("unique %s(%s)", table, cols))
			}
		}
		out = append(out, seedRows(t, db, table)...)
	}
	sort.Strings(out)
	return out
}

func postgresShape(t *testing.T, db *sql.DB) []string {
	t.Helper()
	var out []string
	for _, table := range column(t, db, `SELECT c.relname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = current_schema() AND c.relkind = 'r'`) {
		for _, r := range rows(t, db, `SELECT a.attname, format_type(a.atttypid, a.atttypmod), a.attnotnull,
       COALESCE(pg_get_expr(d.adbin, d.adrelid), '')
  FROM pg_attribute a LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
 WHERE a.attrelid = $1::regclass AND a.attnum > 0 AND NOT a.attisdropped`, table) {
			out = append(out, fmt.Sprintf("column %s.%s %s notnull=%t default=%s", table, r[0], typeClass(r[1]),
				r[2] == "true", normalizeDefault(r[3])))
		}
		for _, r := range rows(t, db, `SELECT k.contype, k.conname,
       (SELECT string_agg(a.attname, ',' ORDER BY u.ord) FROM unnest(k.conkey) WITH ORDINALITY u(attnum, ord)
          JOIN pg_attribute a ON a.attrelid = k.conrelid AND a.attnum = u.attnum),
       COALESCE((SELECT string_agg(a.attname, ',' ORDER BY u.ord) FROM unnest(k.confkey) WITH ORDINALITY u(attnum, ord)
          JOIN pg_attribute a ON a.attrelid = k.confrelid AND a.attnum = u.attnum), ''),
       COALESCE(k.confrelid::regclass::text, ''), k.confdeltype
  FROM pg_constraint k WHERE k.conrelid = $1::regclass AND k.contype IN ('p', 'u', 'f')`, table) {
			switch r[0] {
			case "p":
				out = append(out, fmt.Sprintf("primary key %s(%s)", table, r[2]))
			case "u":
				out = append(out, fmt.Sprintf("unique %s(%s)", table, r[2]))
			case "f":
				out = append(out, fmt.Sprintf("foreign key %s(%s) -> %s(%s) on delete %s", table, r[2], r[4], r[3], foreignKeyAction(r[5])))
			}
		}
		for _, r := range rows(t, db, `SELECT ic.relname, i.indisunique, i.indpred IS NOT NULL,
       (SELECT string_agg(CASE WHEN k.attnum = 0 THEN '<expr>' ELSE a.attname END, ',' ORDER BY k.ord)
          FROM unnest(i.indkey::int2[]) WITH ORDINALITY k(attnum, ord)
          LEFT JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = k.attnum)
  FROM pg_index i JOIN pg_class ic ON ic.oid = i.indexrelid
 WHERE i.indrelid = $1::regclass
   AND NOT EXISTS (SELECT 1 FROM pg_constraint k WHERE k.conindid = i.indexrelid AND k.contype IN ('p', 'u'))`, table) {
			out = append(out, fmt.Sprintf("index %s on %s(%s) unique=%t partial=%t", r[0], table, r[3], r[1] == "true", r[2] == "true"))
		}
		out = append(out, seedRows(t, db, table)...)
	}
	sort.Strings(out)
	return out
}

// safety: the seed rows carry their creation time, so a row is named by the
// table and its first column, which is its key in every seeded table.
func seedRows(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()
	var out []string
	for _, r := range rows(t, db, `SELECT * FROM `+table) {
		out = append(out, fmt.Sprintf("row %s[%s]", table, r[0]))
	}
	return out
}

func typeClass(declared string) string {
	switch t := strings.ToLower(declared); t {
	case "integer", "bigint", "int":
		return "int"
	case "blob", "bytea":
		return "bytes"
	case "real", "double precision":
		return "real"
	// safety: SQLite has no JSON type, so a JSON column is TEXT there.
	case "jsonb":
		return "text"
	default:
		return t
	}
}

var (
	postgresCast  = regexp.MustCompile(`::[a-z ]+`)
	quotedInteger = regexp.MustCompile(`^'(-?[0-9]+)'$`)
)

// hack: Postgres stores a default with a cast, parentheses and a quoted negative integer, so those are stripped.
func normalizeDefault(expr string) string {
	expr = postgresCast.ReplaceAllString(expr, "")
	for strings.HasPrefix(expr, "(") && strings.HasSuffix(expr, ")") {
		expr = expr[1 : len(expr)-1]
	}
	return strings.ToLower(quotedInteger.ReplaceAllString(expr, "$1"))
}

func foreignKeyAction(code string) string {
	switch code {
	case "c":
		return "cascade"
	case "n":
		return "set null"
	case "r":
		return "restrict"
	case "d":
		return "set default"
	default:
		return "no action"
	}
}

func lineDiff(sqlite, postgres []string) []string {
	var out []string
	for _, line := range sqlite {
		if !slices.Contains(postgres, line) {
			out = append(out, "only on SQLite: "+line)
		}
	}
	for _, line := range postgres {
		if !slices.Contains(sqlite, line) {
			out = append(out, "only on Postgres: "+line)
		}
	}
	return out
}

func column(t *testing.T, db *sql.DB, query string, args ...any) []string {
	t.Helper()
	var out []string
	for _, r := range rows(t, db, query, args...) {
		out = append(out, r[0])
	}
	return out
}

func joinFirst(rs [][]string) string {
	var cols []string
	for _, r := range rs {
		cols = append(cols, r[0])
	}
	return strings.Join(cols, ",")
}

func rows(t *testing.T, db *sql.DB, query string, args ...any) [][]string {
	t.Helper()
	rs, err := db.Query(query, args...)
	if err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	defer func() { _ = rs.Close() }()
	cols, err := rs.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var out [][]string
	for rs.Next() {
		vals := make([]sql.NullString, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rs.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		row := make([]string, len(cols))
		for i, v := range vals {
			row[i] = v.String
		}
		out = append(out, row)
	}
	if err := rs.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}
