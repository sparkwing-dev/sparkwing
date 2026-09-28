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

// safety: the fixtures are the schema and bookkeeping rows v0.63.0 left after
// migrating an empty database to its v49, which numbered the node claim token
// column where this lineage numbers the tenant key.

func loadV49MainFixture(t *testing.T, driver, dsn, fixture string) {
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
	if _, err := db.Exec(string(body)); err != nil {
		t.Fatalf("load %s: %v", fixture, err)
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
	path := filepath.Join(t.TempDir(), "state.db")
	loadV49MainFixture(t, "sqlite", path, "schema_v49_v0.63.0_sqlite.sql")
	upgraded, err := store.Open(path)
	if err != nil {
		t.Fatalf("open v0.63.0 database: %v", err)
	}
	defer func() { _ = upgraded.Close() }()
	assertMatchesFreshSchema(t, upgraded, storetest.OpenSQLite(t))
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
