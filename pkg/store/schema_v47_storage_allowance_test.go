package store_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func assertStorageAllowanceSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	for _, q := range []string{
		`SELECT COUNT(storage_allowance_bytes) FROM storage_quotas`,
		`SELECT COUNT(principal), COUNT(storage_bytes) FROM credit_charges`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

// safety: dropping the columns is what makes reopening the database run the
// migration against the shape the previous binary left behind.
func downgradeStorageAllowanceToV46(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	for _, q := range []string{
		`ALTER TABLE storage_quotas DROP COLUMN storage_allowance_bytes`,
		`ALTER TABLE credit_charges DROP COLUMN principal`,
		`ALTER TABLE credit_charges DROP COLUMN storage_bytes`,
		`DELETE FROM sparkwing_schema_version WHERE version >= 47`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

func TestSchemaV47FreshSQLiteStorageAllowanceShape(t *testing.T) {
	st, err := storetest.NewSQLite(t).TryOpen()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	assertStorageAllowanceSchema(t, st.DB())
	if got := readSchemaVersion(t, st.DB()); got != store.ExpectedSchemaVersion() {
		t.Fatalf("schema version = %d, want %d", got, store.ExpectedSchemaVersion())
	}
}

// The columns carry defaults and the version declares no requirement, so the
// binary that wrote the store before the migration keeps writing it after.
func TestSchemaV47UpgradesRealV46SQLiteShapeAndStaysWritableByTheOlderBinary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v46.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	downgradeStorageAllowanceToV46(t, st.DB())
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	up, err := store.Open(path)
	if err != nil {
		t.Fatalf("upgrade v46 to v47: %v", err)
	}
	defer func() { _ = up.Close() }()
	assertStorageAllowanceSchema(t, up.DB())
	if got := readSchemaVersion(t, up.DB()); got != store.ExpectedSchemaVersion() {
		t.Fatalf("schema version = %d, want %d", got, store.ExpectedSchemaVersion())
	}

	ctx := context.Background()
	for _, insert := range []string{
		`INSERT INTO credit_charges (id, run_id, node_id, token_prefix, kind, seconds, amount_micro, charged_at)
		 VALUES ('charge-old', 'run-1', 'build', 'tok', 'usage', 3, 60000, 1)`,
		`INSERT INTO storage_quotas (principal, tier, max_bytes_per_run,
		        max_bytes_per_month, max_objects_per_run, updated_at)
		 VALUES ('legacy', 'free', 10, 20, 30, 1)`,
	} {
		if _, err := up.DB().ExecContext(ctx, insert); err != nil {
			t.Fatalf("an insert naming no new column failed: %v", err)
		}
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
	if len(charges) != 1 || charges[0].Principal != "" || charges[0].StorageBytes != 0 {
		t.Fatalf("the older binary's row reads %+v, want no team and no bytes", charges)
	}
	quota, err := up.StorageQuotaFor(ctx, "legacy")
	if err != nil {
		t.Fatalf("quota: %v", err)
	}
	if quota.AllowanceBytes != 0 {
		t.Fatalf("quota = %+v, want an allowance that keeps everything", quota)
	}
}

func TestSchemaV47UpgradesRealV46PostgresShape(t *testing.T) {
	dsn := pgTestSchemaDSN(t)
	ctx := context.Background()
	st, err := store.OpenPostgres(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	downgradeStorageAllowanceToV46(t, st.DB())
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	up, err := store.OpenPostgres(ctx, dsn)
	if err != nil {
		t.Fatalf("upgrade v46 to v47 on Postgres: %v", err)
	}
	defer func() { _ = up.Close() }()
	assertStorageAllowanceSchema(t, up.DB())
	if got := readSchemaVersion(t, up.DB()); got != store.ExpectedSchemaVersion() {
		t.Fatalf("schema version = %d, want %d", got, store.ExpectedSchemaVersion())
	}
}
