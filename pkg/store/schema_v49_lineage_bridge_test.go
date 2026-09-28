package store_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

// safety: the fixtures are the whole database v0.63.0 left after migrating to
// its v49, which numbered the node claim token column where this lineage
// numbers the tenant key, and then writing a few rows through its own API.

func loadV49MainFixture(t *testing.T, driver, dsn, fixture string, extra ...string) {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", fixture))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	db, err := sql.Open(driver, dsn)
	if err != nil {
		t.Fatalf("open fixture database: %v", err)
	}
	defer func() { _ = db.Close() }()
	for _, stmt := range append([]string{string(body)}, extra...) {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("load %s: %v", fixture, err)
		}
	}
}

func sqliteV49Main(t *testing.T, extra ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.db")
	loadV49MainFixture(t, "sqlite", path, "schema_v49_v0.63.0_sqlite.sql", extra...)
	return path
}

func v49Count(t *testing.T, db *sql.DB, query string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(query).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

func assertV49MainRowsCarried(t *testing.T, st *store.Store) {
	t.Helper()
	db := st.DB()
	for table, want := range map[string]int{
		"runs": 2, "nodes": 2, "events": 3, "secrets": 2, "concurrency_entries": 1,
		"concurrency_holders": 1, "pipeline_profiles": 1, "triggers": 1,
	} {
		if got := v49Count(t, db, `SELECT COUNT(*) FROM `+table); got != want {
			t.Errorf("%s rows = %d, want %d", table, got, want)
		}
	}
	for _, table := range store.TenantTablesForTest() {
		if got := v49Count(t, db, `SELECT COUNT(*) FROM `+table+` WHERE team <> 'default'`); got != 0 {
			t.Errorf("%s has %d rows outside the default team", table, got)
		}
	}
	for query, want := range map[string]int{
		`SELECT COUNT(*) FROM nodes WHERE node_id = 'compile' AND claim_token_prefix = 'swt_seed'`:        1,
		`SELECT event_count FROM runs WHERE id = 'run-a'`:                                                 3,
		`SELECT COUNT(*) FROM teams WHERE name = 'default' AND credit_exhausted_at = 1790000000000000000`: 1,
		`SELECT COUNT(*) FROM sparkwing_meta WHERE key = 'credit_exhausted_at'`:                           0,
	} {
		if got := v49Count(t, db, query); got != want {
			t.Errorf("%s = %d, want %d", query, got, want)
		}
	}
}

func schemaShape(t *testing.T, st *store.Store) []string {
	t.Helper()
	q := `SELECT 'column ' || m.name || '.' || p.name FROM sqlite_master m, pragma_table_info(m.name) p
	       WHERE m.type = 'table' AND m.name NOT LIKE 'sqlite_%'
	      UNION ALL
	      SELECT 'index ' || name FROM sqlite_master WHERE type = 'index' AND name NOT LIKE 'sqlite_%'`
	if st.Dialect() == store.DialectPostgres {
		q = `SELECT 'column ' || table_name || '.' || column_name FROM information_schema.columns
		      WHERE table_schema = current_schema()
		     UNION ALL
		     SELECT 'index ' || indexname FROM pg_indexes WHERE schemaname = current_schema()`
	}
	rows, err := st.DB().Query(q)
	if err != nil {
		t.Fatalf("read schema shape: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("scan schema shape: %v", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read schema shape: %v", err)
	}
	slices.Sort(out)
	return out
}

func assertMatchesFreshSchema(t *testing.T, upgraded, fresh *store.Store) {
	t.Helper()
	var version int
	if err := upgraded.DB().QueryRow(`SELECT MAX(version) FROM sparkwing_schema_version`).Scan(&version); err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	if version != store.ExpectedSchemaVersion() {
		t.Fatalf("schema version = %d, want %d", version, store.ExpectedSchemaVersion())
	}
	var teams int
	if err := upgraded.DB().QueryRow(`SELECT COUNT(*) FROM teams WHERE name = 'default'`).Scan(&teams); err != nil {
		t.Fatalf("read default team: %v", err)
	}
	if teams != 1 {
		t.Fatalf("default team rows = %d, want 1", teams)
	}
	got, want := schemaShape(t, upgraded), schemaShape(t, fresh)
	for _, s := range want {
		if !slices.Contains(got, s) {
			t.Errorf("upgraded database lacks %s", s)
		}
	}
	for _, s := range got {
		if !slices.Contains(want, s) {
			t.Errorf("upgraded database has %s, which a fresh one lacks", s)
		}
	}
}

func TestSchemaV49MainLineageSQLiteGainsTenantKey(t *testing.T) {
	for name, extra := range map[string][]string{
		"plain":             nil,
		"stray teams table": {`CREATE TABLE teams (name TEXT PRIMARY KEY, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL)`},
	} {
		t.Run(name, func(t *testing.T) {
			upgraded, err := store.Open(sqliteV49Main(t, extra...))
			if err != nil {
				t.Fatalf("open v0.63.0 database: %v", err)
			}
			defer func() { _ = upgraded.Close() }()
			assertMatchesFreshSchema(t, upgraded, storetest.OpenSQLite(t))
			assertV49MainRowsCarried(t, upgraded)
		})
	}
}

func TestSchemaV49MainLineageRefusesUnusableTeamsTable(t *testing.T) {
	path := sqliteV49Main(t, `CREATE TABLE teams (id INTEGER)`)
	if st, err := store.Open(path); err == nil {
		_ = st.Close()
		t.Fatal("open with an unusable teams table succeeded, want refusal")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if got := v49Count(t, db, `SELECT MAX(version) FROM sparkwing_schema_version`); got != 49 {
		t.Fatalf("schema version after refusal = %d, want 49", got)
	}
}

// A SQLite upgrade that stops after v50 must already carry a requirement the
// v0.63.0 binary does not know, so that binary refuses the half-moved clock.
func TestSchemaV49MainLineageInterruptedAtV50RefusesOlderBinary(t *testing.T) {
	// safety: a key-less secrets table holding a duplicate lets v49 and v50
	// commit and then fails v51's primary-key rebuild.
	path := sqliteV49Main(t,
		`CREATE TABLE secrets_unkeyed AS SELECT * FROM secrets`,
		`INSERT INTO secrets_unkeyed SELECT * FROM secrets`,
		`DROP TABLE secrets`,
		`ALTER TABLE secrets_unkeyed RENAME TO secrets`)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	rows, err := db.Query(`SELECT name FROM sparkwing_requirements`)
	if err != nil {
		t.Fatal(err)
	}
	known := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		known[name] = true
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}

	if st, err := store.Open(path); err == nil {
		_ = st.Close()
		t.Fatal("open with duplicate secrets succeeded, want v51 to fail")
	}
	if got := v49Count(t, db, `SELECT MAX(version) FROM sparkwing_schema_version`); got != 50 {
		t.Fatalf("schema version after interruption = %d, want 50", got)
	}
	st, err := store.OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	listed, err := st.Requirements(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(listed, func(name string) bool { return !known[name] }) {
		t.Fatalf("requirements at v50 = %v; v0.63.0 knows every one, so it would reopen the database", listed)
	}
}

func TestSchemaV49MainLineagePostgresGainsTenantKey(t *testing.T) {
	dsn := storetest.NewPostgres(t).DSN()
	loadV49MainFixture(t, "pgx", dsn, "schema_v49_v0.63.0_postgres.sql")
	upgraded, err := store.OpenPostgres(context.Background(), dsn)
	if err != nil {
		t.Fatalf("open v0.63.0 database: %v", err)
	}
	defer func() { _ = upgraded.Close() }()
	assertMatchesFreshSchema(t, upgraded, storetest.OpenPostgres(t))
	assertV49MainRowsCarried(t, upgraded)
}

func TestSchemaV73UpgradesToV74(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target func(*testing.T) *storetest.Target
	}{
		{"sqlite", storetest.NewSQLite},
		{"postgres", storetest.NewPostgres},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tg := tc.target(t)
			st, err := tg.TryOpen()
			if err != nil {
				t.Fatalf("open fresh store: %v", err)
			}
			// safety: a controller created before the base schema named the
			// claim token column reaches v73 without it.
			for _, stmt := range []string{
				`DELETE FROM sparkwing_schema_version WHERE version > 73`,
				`ALTER TABLE nodes DROP COLUMN claim_token_prefix`,
			} {
				if _, err := st.DB().Exec(stmt); err != nil {
					t.Fatalf("wind back to v73 with %q: %v", stmt, err)
				}
			}
			if err := st.Close(); err != nil {
				t.Fatalf("close v73 store: %v", err)
			}
			upgraded := tg.Open(t)
			fresh := storetest.OpenSQLite(t)
			if tc.name == "postgres" {
				fresh = storetest.OpenPostgres(t)
			}
			assertMatchesFreshSchema(t, upgraded, fresh)
		})
	}
}
