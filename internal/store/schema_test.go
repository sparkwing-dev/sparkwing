package store_test

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestSchemaDocumentationNamesBaseline(t *testing.T) {
	for path, claim := range map[string]string{
		"../../CHANGELOG.md":               "schema baseline advances to version %d",
		"../../docs/migrations/v0.68.0.md": "schema baseline moves to version %d",
	} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), fmt.Sprintf(claim, store.ExpectedSchemaVersion())) {
			t.Errorf("%s omits baseline %d", path, store.ExpectedSchemaVersion())
		}
	}
}

// safety: neutral-schema translation must preserve every table, column, key,
// index and seed row across SQLite and PostgreSQL.
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

func TestSchemaShapeIncludesGeneratedDrainDeadlines(t *testing.T) {
	s := storetest.OpenSQLite(t)
	shape := sqliteShape(t, s.DB())
	for _, table := range []string{"runs", "nodes", "triggers"} {
		for _, want := range []string{
			fmt.Sprintf("column %s.drain_deadline_at int notnull=false default=", table),
			fmt.Sprintf("generated %s.drain_deadline_at stored expression=cancel_requested_at + 300000000000", table),
		} {
			if !slices.Contains(shape, want) {
				t.Errorf("schema inspection omitted %q", want)
			}
		}
	}
}

func TestSchemaShapeIncludesGeneratedClaimRequirements(t *testing.T) {
	s := storetest.OpenSQLite(t)
	shape := sqliteShape(t, s.DB())
	for _, table := range []string{"nodes", "triggers"} {
		want := fmt.Sprintf("generated %s.claim_requirements_ref stored expression=nullif(claim_requirements, '')", table)
		if !slices.Contains(shape, want) {
			t.Errorf("schema inspection omitted %q", want)
		}
	}
}

func TestSchemaExpressionNormalizesFunctionSeparators(t *testing.T) {
	for _, expression := range []string{
		"NULLIF(claim_requirements,'')",
		"NULLIF(claim_requirements, ''::text)",
	} {
		if got := normalizeDefault(expression); got != "nullif(claim_requirements, '')" {
			t.Errorf("normalizeDefault(%q) = %q", expression, got)
		}
	}
	if got := normalizeDefault("COALESCE(value,'a,b')"); got != "coalesce(value, 'a,b')" {
		t.Errorf("literal separator changed: %q", got)
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
	for _, want := range []string{"newer than this binary", "start a binary matching the store", "restore a backup matching this binary"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Open error %q does not say %q", err, want)
		}
	}
	for _, unsafe := range []string{"delete ", "drop the database", "predates"} {
		if strings.Contains(err.Error(), unsafe) {
			t.Errorf("rollback error gives destructive guidance: %v", err)
		}
	}
}

func sqliteShape(t *testing.T, db *sql.DB) []string {
	t.Helper()
	var out []string
	for _, table := range column(t, db, `SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`) {
		for _, r := range rows(t, db, `SELECT name, type, "notnull", COALESCE(dflt_value, ''), pk FROM pragma_table_xinfo(?)`, table) {
			out = append(out, fmt.Sprintf("column %s.%s %s notnull=%t default=%s", table, r[0], typeClass(r[1]),
				r[2] == "1" || r[4] != "0", normalizeDefault(r[3])))
		}
		for _, definition := range column(t, db, `SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, table) {
			for _, generated := range generatedColumn.FindAllStringSubmatch(definition, -1) {
				out = append(out, fmt.Sprintf("generated %s.%s %s expression=%s", table, generated[1], strings.ToLower(generated[3]), normalizeDefault(generated[2])))
			}
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
       COALESCE(pg_get_expr(d.adbin, d.adrelid), ''), a.attgenerated
  FROM pg_attribute a LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
 WHERE a.attrelid = $1::regclass AND a.attnum > 0 AND NOT a.attisdropped`, table) {
			defaultExpr := r[3]
			if r[4] != "" {
				mode := "stored"
				if r[4] == "v" {
					mode = "virtual"
				}
				out = append(out, fmt.Sprintf("generated %s.%s %s expression=%s", table, r[0], mode, normalizeDefault(r[3])))
				defaultExpr = ""
			}
			out = append(out, fmt.Sprintf("column %s.%s %s notnull=%t default=%s", table, r[0], typeClass(r[1]),
				r[2] == "true", normalizeDefault(defaultExpr)))
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
	postgresCast    = regexp.MustCompile(`::[a-z ]+`)
	quotedInteger   = regexp.MustCompile(`'(-?[0-9]+)'`)
	generatedColumn = regexp.MustCompile(`(?is)(\w+)\s+\w+\s+GENERATED\s+ALWAYS\s+AS\s*\((.*?)\)\s+(STORED|VIRTUAL)`)
	commaSpacing    = regexp.MustCompile(`'(?:[^']|'')*'|,\s*`)
)

// hack: Postgres stores a default with a cast, parentheses and a quoted negative integer, so those are stripped.
func normalizeDefault(expr string) string {
	// Recording-time behavior is exercised against each database clock separately.
	lower := strings.ToLower(expr)
	if strings.Contains(lower, "unixepoch('subsec')") || strings.Contains(lower, "extract(epoch from statement_timestamp())") {
		return "database_timestamp"
	}
	expr = postgresCast.ReplaceAllString(expr, "")
	expr = commaSpacing.ReplaceAllStringFunc(expr, func(token string) string {
		if strings.HasPrefix(token, ",") {
			return ", "
		}
		return token
	})
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

// A database holding someone else's tables is refused rather than given the
// baseline beside them, whatever the dialect.
func TestOpenRefusesANonEmptyForeignDatabase(t *testing.T) {
	t.Run("sqlite", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "foreign.db")
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		seedForeignTable(t, db)
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := teststore.OpenExisting(path); err == nil || !strings.Contains(err.Error(), "not empty") {
			t.Fatalf("Open over a foreign database = %v, want the not-empty refusal", err)
		}
		db, err = sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = db.Close() }()
		if got := column(t, db, `SELECT name FROM sqlite_master WHERE type = 'table'`); !slices.Equal(got, []string{"customer_records"}) {
			t.Fatalf("tables after the refused open = %v, want only the foreign one", got)
		}
	})
	t.Run("postgres", func(t *testing.T) {
		dsn := storetest.NewPostgres(t).DSN()
		db, err := sql.Open("pgx", dsn)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = db.Close() }()
		seedForeignTable(t, db)
		if _, err := teststore.OpenPostgres(t.Context(), dsn); err == nil || !strings.Contains(err.Error(), "not empty") {
			t.Fatalf("OpenPostgres over a foreign schema = %v, want the not-empty refusal", err)
		}
		got := column(t, db, `SELECT table_name FROM information_schema.tables WHERE table_schema = current_schema()`)
		if !slices.Equal(got, []string{"customer_records"}) {
			t.Fatalf("tables after the refused open = %v, want only the foreign one", got)
		}
	})
}

func seedForeignTable(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, q := range []string{
		`CREATE TABLE customer_records (id INTEGER PRIMARY KEY, value TEXT)`,
		`INSERT INTO customer_records (id, value) VALUES (1, 'foreign data')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

func TestOlderSchemaRequiresBackupBeforeDiscard(t *testing.T) {
	target := storetest.New(t)
	st, err := target.TryOpen()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(storetest.Rebind(st,
		`UPDATE sparkwing_schema_version SET version = ?`), store.ExpectedSchemaVersion()-1); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = target.TryOpen()
	if err == nil {
		t.Fatal("accepted an older schema")
	}
	for _, want := range []string{"older than this binary", "back up the store", "only for a disposable store"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
}

// A store from before the v0.68 rewrite is refused with the move-aside remedy,
// not the matching-binary advice, and is left exactly as it was.
func TestOpenRefusesAPreRewriteStore(t *testing.T) {
	t.Run("sqlite", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "state.db")
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = db.Close() }()
		seedPreRewriteStore(t, db)
		_, err = teststore.OpenExisting(path)
		assertPreRewriteRefusal(t, err, "mv "+path+" "+path+".v067")
		assertPreRewriteStoreIntact(t, db, `SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name`)
	})
	t.Run("postgres", func(t *testing.T) {
		dsn := storetest.NewPostgres(t).DSN()
		db, err := sql.Open("pgx", dsn)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = db.Close() }()
		seedPreRewriteStore(t, db)
		_, err = teststore.OpenPostgres(t.Context(), dsn)
		assertPreRewriteRefusal(t, err, "point the server at an empty database")
		assertPreRewriteStoreIntact(t, db, `SELECT table_name FROM information_schema.tables WHERE table_schema = current_schema() ORDER BY table_name`)
	})
}

// seedPreRewriteStore writes the shape v0.67.0 leaves: its version table at
// schema 94 beside sparkwing_meta, which the rewrite never creates.
func seedPreRewriteStore(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, q := range []string{
		`CREATE TABLE sparkwing_schema_version (version INTEGER NOT NULL, applied_at BIGINT NOT NULL, PRIMARY KEY (version))`,
		`INSERT INTO sparkwing_schema_version (version, applied_at) VALUES (94, 1)`,
		`CREATE TABLE sparkwing_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_at BIGINT NOT NULL)`,
		`CREATE TABLE runs (id TEXT PRIMARY KEY)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

func assertPreRewriteRefusal(t *testing.T, err error, remedy string) {
	t.Helper()
	if err == nil {
		t.Fatal("Open accepted a v0.67 store")
	}
	for _, want := range []string{"Sparkwing v0.67 or earlier", "schema version 94", "cannot read or import it", remedy} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Open error %q does not say %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "newer than this binary") {
		t.Errorf("Open error %q sends the operator to find a matching binary", err)
	}
}

func assertPreRewriteStoreIntact(t *testing.T, db *sql.DB, tables string) {
	t.Helper()
	if got := column(t, db, tables); !slices.Equal(got, []string{"runs", "sparkwing_meta", "sparkwing_schema_version"}) {
		t.Errorf("tables after the refused open = %v, want the old store's only", got)
	}
	if got := column(t, db, `SELECT CAST(version AS TEXT) FROM sparkwing_schema_version`); !slices.Equal(got, []string{"94"}) {
		t.Errorf("recorded versions after the refused open = %v, want [94]", got)
	}
}
