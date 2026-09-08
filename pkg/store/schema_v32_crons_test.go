package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestSchemaV32FreshDatabaseHasCronTables(t *testing.T) {
	st, err := storetest.New(t).TryOpen()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	assertCronSchemaShape(t, st)
}

func TestSchemaV32UpgradesV31AndKeepsRuns(t *testing.T) {
	target := storetest.New(t)
	ctx := context.Background()
	st, err := target.TryOpen()
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateRun(ctx, store.Run{
		ID: "kept", Pipeline: "nightly", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`DROP TABLE cron_fires`,
		`DROP TABLE cron_schedules`,
		`DELETE FROM sparkwing_schema_version WHERE version >= 32`,
	} {
		if _, err := st.DB().ExecContext(ctx, statement); err != nil {
			_ = st.Close()
			t.Fatalf("downgrade with %q: %v", statement, err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	up, err := target.TryOpen()
	if err != nil {
		t.Fatalf("upgrade v31 to v32: %v", err)
	}
	defer func() { _ = up.Close() }()
	assertCronSchemaShape(t, up)
	if got, err := up.CurrentSchemaVersion(ctx); err != nil || got != store.ExpectedSchemaVersion() {
		t.Fatalf("schema version = %d, err = %v, want %d", got, err, store.ExpectedSchemaVersion())
	}
	run, err := up.GetRun(ctx, "kept")
	if err != nil || run == nil {
		t.Fatalf("GetRun after upgrade = (%v, %v), want the pre-migration run", run, err)
	}
}

func assertCronSchemaShape(t *testing.T, st *store.Store) {
	t.Helper()
	ctx := context.Background()
	for _, table := range []string{"cron_schedules", "cron_fires"} {
		var rows int
		if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&rows); err != nil {
			t.Fatalf("read %s: %v", table, err)
		}
	}
	query := `SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`
	if st.Dialect() == store.DialectPostgres {
		query = `SELECT COUNT(*) FROM pg_indexes WHERE schemaname = current_schema() AND indexname = ?`
	}
	var indexes int
	if err := st.DB().QueryRowContext(ctx, storetest.Rebind(st, query),
		"idx_cron_fires_schedule_decided").Scan(&indexes); err != nil {
		t.Fatalf("inspect cron fire index: %v", err)
	}
	if indexes != 1 {
		t.Errorf("idx_cron_fires_schedule_decided count = %d, want 1", indexes)
	}
}
