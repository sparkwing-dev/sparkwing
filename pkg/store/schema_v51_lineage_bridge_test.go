package store_test

import (
	"context"
	"slices"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

// safety: v0.65.0 and v0.65.1 took the v0.63.0 database on to their own v50
// and v51, which added node_metrics.kind and stamped two requirements; these
// statements replay that on the v49 fixture.
var v51MainSteps = []string{
	`ALTER TABLE node_metrics ADD COLUMN kind TEXT NOT NULL DEFAULT ''`,
	`UPDATE node_metrics SET kind = 'command' WHERE cpu_time_nanos > 0 AND kind = ''`,
	`INSERT INTO sparkwing_schema_version (version, applied_at) VALUES (50, 1790600000000000000), (51, 1790600000000000001)`,
	`INSERT INTO sparkwing_requirements (name, added_at, added_by_version) VALUES
	    ('metric-sample-kind', 1790600000000000000, 'v0.65.0'),
	    ('process-tree-accounting', 1790600000000000001, 'v0.65.0')`,
}

func assertV51MainCompleted(t *testing.T, st *store.Store) {
	t.Helper()
	requirements, err := st.Requirements(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"metric-sample-kind", "process-tree-accounting", "team-credit-exhaustion-v1", "team-scoped-user-keys", "claim-cache-scope-v1"} {
		if !slices.Contains(requirements, name) {
			t.Errorf("requirements = %v; want %s", requirements, name)
		}
	}
}

func TestSchemaV51MainLineageSQLiteRunsTheSkippedTenantMigrations(t *testing.T) {
	upgraded, err := store.Open(sqliteV49Main(t, append([]string{v49MainScaleStepSQL}, v51MainSteps...)...))
	if err != nil {
		t.Fatalf("open v0.65.1 database: %v", err)
	}
	defer func() { _ = upgraded.Close() }()
	assertMatchesFreshSchema(t, upgraded, storetest.OpenSQLite(t))
	assertV49MainRowsCarried(t, upgraded)
	assertV49MainCreditRestated(t, upgraded)
	assertV51MainCompleted(t, upgraded)
}

func TestSchemaV51MainLineagePostgresRunsTheSkippedTenantMigrations(t *testing.T) {
	dsn := storetest.NewPostgres(t).DSN()
	loadV49MainFixture(t, "pgx", dsn, "schema_v49_v0.63.0_postgres.sql", append([]string{v49MainScaleStepSQL}, v51MainSteps...)...)
	upgraded, err := store.OpenPostgres(context.Background(), dsn)
	if err != nil {
		t.Fatalf("open v0.65.1 database: %v", err)
	}
	defer func() { _ = upgraded.Close() }()
	assertMatchesFreshSchema(t, upgraded, storetest.OpenPostgres(t))
	assertV49MainRowsCarried(t, upgraded)
	assertV49MainCreditRestated(t, upgraded)
	assertV51MainCompleted(t, upgraded)
}
