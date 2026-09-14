package store_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestEgressUsage_RoundTrip(t *testing.T) {
	st := storetest.New(t).Open(t)
	ctx := context.Background()

	if err := st.RecordEgressUsage(ctx, []store.EgressUsage{
		{Principal: "alice", Month: "2026-09", Bytes: 400},
		{Principal: "bob", Month: "2026-09", Bytes: 900},
		{Principal: "alice", Month: "2026-08", Bytes: 10},
	}); err != nil {
		t.Fatalf("RecordEgressUsage: %v", err)
	}

	rows, err := st.ListEgressUsage(ctx, "2026-09")
	if err != nil {
		t.Fatalf("ListEgressUsage: %v", err)
	}
	if len(rows) != 2 || rows[0].Principal != "bob" || rows[0].Bytes != 900 {
		t.Fatalf("rows = %+v, want bob first at 900", rows)
	}
	if rows[1].Principal != "alice" || rows[1].Bytes != 400 || rows[1].Month != "2026-09" {
		t.Fatalf("second row = %+v, want alice at 400 in 2026-09", rows[1])
	}
	if rows[0].UpdatedAt.IsZero() {
		t.Error("UpdatedAt is zero")
	}

	all, err := st.ListEgressUsage(ctx, "")
	if err != nil {
		t.Fatalf("ListEgressUsage over every month: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("rows over every month = %d, want 3", len(all))
	}
}

func TestEgressUsageOnlyRises(t *testing.T) {
	st := storetest.New(t).Open(t)
	ctx := context.Background()

	if err := st.RecordEgressUsage(ctx, []store.EgressUsage{
		{Principal: "alice", Month: "2026-09", Bytes: 900},
	}); err != nil {
		t.Fatalf("RecordEgressUsage: %v", err)
	}
	// safety: a writer whose memory is behind the row must not hand back
	// budget the row says is already spent.
	if err := st.RecordEgressUsage(ctx, []store.EgressUsage{
		{Principal: "alice", Month: "2026-09", Bytes: 100},
	}); err != nil {
		t.Fatalf("RecordEgressUsage with a lower total: %v", err)
	}
	rows, err := st.ListEgressUsage(ctx, "2026-09")
	if err != nil {
		t.Fatalf("ListEgressUsage: %v", err)
	}
	if len(rows) != 1 || rows[0].Bytes != 900 {
		t.Fatalf("rows = %+v, want alice held at 900", rows)
	}

	if err := st.RecordEgressUsage(ctx, []store.EgressUsage{
		{Principal: "alice", Month: "2026-09", Bytes: 1200},
	}); err != nil {
		t.Fatalf("RecordEgressUsage with a higher total: %v", err)
	}
	rows, err = st.ListEgressUsage(ctx, "2026-09")
	if err != nil {
		t.Fatalf("ListEgressUsage: %v", err)
	}
	if len(rows) != 1 || rows[0].Bytes != 1200 {
		t.Fatalf("rows = %+v, want alice raised to 1200", rows)
	}
}

func TestEgressUsageSkipsUnnamedRowsAndPrunesOldMonths(t *testing.T) {
	st := storetest.New(t).Open(t)
	ctx := context.Background()

	if err := st.RecordEgressUsage(ctx, []store.EgressUsage{
		{Principal: "", Month: "2026-09", Bytes: 5},
		{Principal: "alice", Month: "", Bytes: 5},
		{Principal: "alice", Month: "2026-07", Bytes: 5},
		{Principal: "alice", Month: "2026-09", Bytes: 5},
	}); err != nil {
		t.Fatalf("RecordEgressUsage: %v", err)
	}
	if err := st.RecordEgressUsage(ctx, nil); err != nil {
		t.Fatalf("RecordEgressUsage with nothing to write: %v", err)
	}
	all, err := st.ListEgressUsage(ctx, "")
	if err != nil {
		t.Fatalf("ListEgressUsage: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("rows = %+v, want only the two fully named ones", all)
	}

	n, err := st.PruneEgressUsage(ctx, "2026-09")
	if err != nil {
		t.Fatalf("PruneEgressUsage: %v", err)
	}
	if n != 1 {
		t.Fatalf("pruned %d rows, want 1", n)
	}
	if zero, err := st.PruneEgressUsage(ctx, ""); err != nil || zero != 0 {
		t.Fatalf("PruneEgressUsage with no month = %d, %v; want 0 and no error", zero, err)
	}
}

func assertEgressSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	var n int
	const q = `SELECT COUNT(*) FROM egress_usage`
	if err := db.QueryRowContext(context.Background(), q).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func TestSchemaV41FreshSQLiteEgressShape(t *testing.T) {
	st, err := storetest.NewSQLite(t).TryOpen()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	assertEgressSchema(t, st.DB())
	if got := readSchemaVersion(t, st.DB()); got != store.ExpectedSchemaVersion() {
		t.Fatalf("schema version = %d, want %d", got, store.ExpectedSchemaVersion())
	}
}

// safety: dropping the table is what makes reopening the database run the
// migration against the shape a v40 binary left behind.
func downgradeEgressToV40(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	for _, q := range []string{
		`DROP TABLE egress_usage`,
		`DELETE FROM sparkwing_schema_version WHERE version >= 41`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

func TestSchemaV41UpgradesRealV40SQLiteShape(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v40.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	downgradeEgressToV40(t, st.DB())
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	up, err := store.Open(path)
	if err != nil {
		t.Fatalf("upgrade v40 to v41: %v", err)
	}
	defer func() { _ = up.Close() }()
	assertEgressSchema(t, up.DB())
	if got := readSchemaVersion(t, up.DB()); got != store.ExpectedSchemaVersion() {
		t.Fatalf("schema version = %d, want %d", got, store.ExpectedSchemaVersion())
	}
}

func TestSchemaV41UpgradesRealV40PostgresShape(t *testing.T) {
	dsn := pgTestSchemaDSN(t)
	ctx := context.Background()
	st, err := store.OpenPostgres(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	downgradeEgressToV40(t, st.DB())
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	up, err := store.OpenPostgres(ctx, dsn)
	if err != nil {
		t.Fatalf("upgrade v40 to v41 on Postgres: %v", err)
	}
	defer func() { _ = up.Close() }()
	assertEgressSchema(t, up.DB())
	if err := up.RecordEgressUsage(ctx, []store.EgressUsage{
		{Principal: "alice", Month: "2026-09", Bytes: 7},
	}); err != nil {
		t.Fatalf("record on the migrated Postgres store: %v", err)
	}
	rows, err := up.ListEgressUsage(ctx, "2026-09")
	if err != nil {
		t.Fatalf("list on the migrated Postgres store: %v", err)
	}
	if len(rows) != 1 || rows[0].Bytes != 7 {
		t.Fatalf("rows = %+v, want alice at 7", rows)
	}
}
