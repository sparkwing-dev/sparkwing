package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestV91GitHubDeliveryEventUpgradesSQLite(t *testing.T) {
	target := storetest.NewSQLite(t)
	st, err := target.TryOpen()
	if err != nil {
		t.Fatal(err)
	}
	downgradeV91DeliveryEvent(t, st)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	up, err := target.TryOpen()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = up.Close() }()
	assertV91DeliveryEvent(t, up, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`)
}

func TestV91GitHubDeliveryEventUpgradesPostgres(t *testing.T) {
	ctx := context.Background()
	scoped := pgTestSchemaDSN(t)
	st, err := store.OpenPostgres(ctx, scoped)
	if err != nil {
		t.Fatal(err)
	}
	downgradeV91DeliveryEvent(t, st)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	up, err := store.OpenPostgres(ctx, scoped)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = up.Close() }()
	assertV91DeliveryEvent(t, up, `SELECT COUNT(*) FROM pg_indexes WHERE schemaname = current_schema() AND indexname = $1`)
}

func downgradeV91DeliveryEvent(t *testing.T, st *store.Store) {
	t.Helper()
	for _, statement := range []string{
		`DROP INDEX idx_github_app_deliveries_received`,
		`ALTER TABLE github_app_deliveries DROP COLUMN event`,
		`DELETE FROM sparkwing_schema_version WHERE version = 91`,
		`INSERT INTO github_app_deliveries (digest, delivery_id, received_at) VALUES ('legacy', 'd-legacy', 1)`,
	} {
		if _, err := st.DB().ExecContext(t.Context(), statement); err != nil {
			t.Fatalf("downgrade v91 with %q: %v", statement, err)
		}
	}
}

func assertV91DeliveryEvent(t *testing.T, st *store.Store, indexQuery string) {
	t.Helper()
	ctx := t.Context()
	if version, err := st.CurrentSchemaVersion(ctx); err != nil || version != store.ExpectedSchemaVersion() {
		t.Fatalf("schema version = %d, %v; want %d", version, err, store.ExpectedSchemaVersion())
	}
	var indexes int
	if err := st.DB().QueryRowContext(ctx, indexQuery, "idx_github_app_deliveries_received").Scan(&indexes); err != nil || indexes != 1 {
		t.Fatalf("received_at index count = %d, %v; want one", indexes, err)
	}
	if seen, err := st.GitHubAppDeliverySeen(ctx, "legacy"); err != nil || !seen {
		t.Fatalf("a digest recorded before v91 = %v, %v; want seen", seen, err)
	}
	if same, err := st.BindGitHubAppDeliveryEvent(ctx, "legacy", "push", "d-legacy", time.Unix(2, 0)); err != nil || !same {
		t.Fatalf("binding after the upgrade = %v, %v", same, err)
	}
}
