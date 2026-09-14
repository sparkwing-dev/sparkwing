package store_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func assertCreditClassSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	for _, q := range []string{
		`SELECT COUNT(cpu_class), COUNT(rate_micro_per_second) FROM credit_charges`,
		`SELECT COUNT(credit_cpu_class) FROM nodes`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

// safety: dropping the columns is what makes reopening the database run the
// migration against the shape the previous binary left behind.
func downgradeCreditClassToV45(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	for _, q := range []string{
		`ALTER TABLE credit_charges DROP COLUMN cpu_class`,
		`ALTER TABLE credit_charges DROP COLUMN rate_micro_per_second`,
		`ALTER TABLE nodes DROP COLUMN credit_cpu_class`,
		`DELETE FROM sparkwing_schema_version WHERE version >= 46`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

func TestSchemaV46FreshSQLiteCreditClassShape(t *testing.T) {
	st, err := storetest.NewSQLite(t).TryOpen()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	assertCreditClassSchema(t, st.DB())
	if got := readSchemaVersion(t, st.DB()); got != store.ExpectedSchemaVersion() {
		t.Fatalf("schema version = %d, want %d", got, store.ExpectedSchemaVersion())
	}
}

// The columns carry defaults and the version declares no requirement, so the
// binary that wrote the store before the migration keeps writing it after.
func TestSchemaV46UpgradesRealV45SQLiteShapeAndStaysWritableByTheOlderBinary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v45.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	downgradeCreditClassToV45(t, st.DB())
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	up, err := store.Open(path)
	if err != nil {
		t.Fatalf("upgrade v45 to v46: %v", err)
	}
	defer func() { _ = up.Close() }()
	assertCreditClassSchema(t, up.DB())
	if got := readSchemaVersion(t, up.DB()); got != store.ExpectedSchemaVersion() {
		t.Fatalf("schema version = %d, want %d", got, store.ExpectedSchemaVersion())
	}

	ctx := context.Background()
	if _, err := up.DB().ExecContext(ctx,
		`INSERT INTO credit_charges (id, run_id, node_id, token_prefix, kind, seconds, amount_micro, charged_at)
		 VALUES ('charge-old', 'run-1', 'build', 'tok', 'usage', 3, 60000, 1)`); err != nil {
		t.Fatalf("an insert naming no new column failed: %v", err)
	}
	adds, err := up.RequirementsWritingWouldAdd(ctx)
	if err != nil {
		t.Fatalf("requirements: %v", err)
	}
	if len(adds) != 0 {
		t.Fatalf("the migration stamps %v, which strands the older binary", adds)
	}

	charges, err := up.ListCreditCharges(ctx, 10)
	if err != nil {
		t.Fatalf("list charges: %v", err)
	}
	if len(charges) != 1 || charges[0].CPUClassCores != 0 || charges[0].RateMicroPerSecond != 0 {
		t.Fatalf("the older binary's row reads %+v, want class 0 at rate 0", charges)
	}
}

func TestSchemaV46UpgradesRealV45PostgresShape(t *testing.T) {
	dsn := pgTestSchemaDSN(t)
	ctx := context.Background()
	st, err := store.OpenPostgres(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	downgradeCreditClassToV45(t, st.DB())
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	up, err := store.OpenPostgres(ctx, dsn)
	if err != nil {
		t.Fatalf("upgrade v45 to v46 on Postgres: %v", err)
	}
	defer func() { _ = up.Close() }()
	assertCreditClassSchema(t, up.DB())
	if got := readSchemaVersion(t, up.DB()); got != store.ExpectedSchemaVersion() {
		t.Fatalf("schema version = %d, want %d", got, store.ExpectedSchemaVersion())
	}
}
