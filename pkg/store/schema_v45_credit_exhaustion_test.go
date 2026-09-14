package store_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func assertCreditExhaustionSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	var n int
	const q = `SELECT COUNT(*) FROM nodes WHERE credit_exhausted_anchor = 0`
	if err := db.QueryRowContext(context.Background(), q).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func TestSchemaV45FreshSQLiteCreditExhaustionShape(t *testing.T) {
	st, err := storetest.NewSQLite(t).TryOpen()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	assertCreditExhaustionSchema(t, st.DB())
	if got := readSchemaVersion(t, st.DB()); got != store.ExpectedSchemaVersion() {
		t.Fatalf("schema version = %d, want %d", got, store.ExpectedSchemaVersion())
	}
}

// safety: stripping the column is what makes reopening the database run the
// migration against the shape a v44 binary left behind.
func downgradeCreditExhaustionToV44(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	for _, q := range []string{
		`ALTER TABLE nodes DROP COLUMN credit_exhausted_anchor`,
		`DELETE FROM sparkwing_schema_version WHERE version >= 45`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

func TestSchemaV45UpgradesRealV44SQLiteShape(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v44.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	downgradeCreditExhaustionToV44(t, st.DB())
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	up, err := store.Open(path)
	if err != nil {
		t.Fatalf("upgrade v44 to v45: %v", err)
	}
	defer func() { _ = up.Close() }()
	assertCreditExhaustionSchema(t, up.DB())
	if got := readSchemaVersion(t, up.DB()); got != store.ExpectedSchemaVersion() {
		t.Fatalf("schema version = %d, want %d", got, store.ExpectedSchemaVersion())
	}
}

func TestSchemaV45UpgradesRealV44PostgresShape(t *testing.T) {
	dsn := pgTestSchemaDSN(t)
	ctx := context.Background()
	st, err := store.OpenPostgres(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	downgradeCreditExhaustionToV44(t, st.DB())
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	up, err := store.OpenPostgres(ctx, dsn)
	if err != nil {
		t.Fatalf("upgrade v44 to v45 on Postgres: %v", err)
	}
	defer func() { _ = up.Close() }()
	assertCreditExhaustionSchema(t, up.DB())
	if got := readSchemaVersion(t, up.DB()); got != store.ExpectedSchemaVersion() {
		t.Fatalf("schema version = %d, want %d", got, store.ExpectedSchemaVersion())
	}
}
