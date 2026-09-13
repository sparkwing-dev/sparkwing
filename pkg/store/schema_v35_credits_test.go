package store_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func assertCreditsSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	for _, q := range []string{
		`SELECT COUNT(*) FROM credit_grants`,
		`SELECT COUNT(*) FROM credit_charges`,
		`SELECT COUNT(*) FROM credit_charges WHERE kind = 'usage'`,
		`SELECT COUNT(*) FROM tokens WHERE metered = 0`,
		`SELECT COUNT(*) FROM nodes WHERE credit_charged_through = 0`,
	} {
		var n int
		if err := db.QueryRowContext(ctx, q).Scan(&n); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

func TestSchemaV35FreshSQLiteCreditsShape(t *testing.T) {
	st, err := storetest.NewSQLite(t).TryOpen()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	assertCreditsSchema(t, st.DB())
	if got := readSchemaVersion(t, st.DB()); got != store.ExpectedSchemaVersion() {
		t.Fatalf("schema version = %d, want %d", got, store.ExpectedSchemaVersion())
	}
}

// safety: stripping everything v35 adds is what makes reopening the database
// run the migration against the shape a v34 binary left behind.
func downgradeCreditsToV34(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	statements := []string{
		`DROP INDEX IF EXISTS idx_nodes_credit_window`,
		`DROP TABLE credit_charges`,
		`DROP TABLE credit_grants`,
		`ALTER TABLE tokens DROP COLUMN metered`,
		`ALTER TABLE nodes DROP COLUMN credit_charged_through`,
		`DELETE FROM sparkwing_schema_version WHERE version >= 35`,
	}
	for _, q := range statements {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

func TestSchemaV35UpgradesRealV34SQLiteShape(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v34.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	downgradeCreditsToV34(t, st.DB())
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	up, err := store.Open(path)
	if err != nil {
		t.Fatalf("upgrade v34 to v35: %v", err)
	}
	defer func() { _ = up.Close() }()
	assertCreditsSchema(t, up.DB())
	if got := readSchemaVersion(t, up.DB()); got != store.ExpectedSchemaVersion() {
		t.Fatalf("schema version = %d, want %d", got, store.ExpectedSchemaVersion())
	}
}

func TestSchemaV35UpgradesRealV34PostgresShape(t *testing.T) {
	dsn := pgTestSchemaDSN(t)
	ctx := context.Background()
	st, err := store.OpenPostgres(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	downgradeCreditsToV34(t, st.DB())
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	up, err := store.OpenPostgres(ctx, dsn)
	if err != nil {
		t.Fatalf("upgrade v34 to v35 on Postgres: %v", err)
	}
	defer func() { _ = up.Close() }()
	assertCreditsSchema(t, up.DB())
	if got := readSchemaVersion(t, up.DB()); got != store.ExpectedSchemaVersion() {
		t.Fatalf("schema version = %d, want %d", got, store.ExpectedSchemaVersion())
	}
	if _, err := up.GrantCredits(ctx, store.CreditGrantPaid, store.MicroCreditsPerCredit, "pay_pg", "root"); err != nil {
		t.Fatalf("grant on the migrated Postgres store: %v", err)
	}
	balance, err := up.CreditBalanceMicro(ctx)
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	if balance != store.MicroCreditsPerCredit {
		t.Fatalf("balance = %d, want one credit", balance)
	}
}

// A v35 store from an earlier commit on this branch carries the charge table
// without its kind column, so the migration repairs that shape in place.
func TestSchemaV35RepairsAV35StoreMissingTheChargeKind(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v35-early.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`DROP INDEX IF EXISTS idx_credit_charges_kind_amount`); err != nil {
		t.Fatalf("drop the index over the charge kind: %v", err)
	}
	if _, err := st.DB().Exec(`ALTER TABLE credit_charges DROP COLUMN kind`); err != nil {
		t.Fatalf("strip the charge kind: %v", err)
	}
	if _, err := st.DB().Exec(`DELETE FROM sparkwing_schema_version WHERE version >= 35`); err != nil {
		t.Fatalf("rewind the stamp: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	up, err := store.Open(path)
	if err != nil {
		t.Fatalf("reopen a v35 store short of the charge kind: %v", err)
	}
	defer func() { _ = up.Close() }()
	assertCreditsSchema(t, up.DB())
}
