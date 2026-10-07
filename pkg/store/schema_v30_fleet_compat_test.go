package store_test

import (
	"context"
	"database/sql"
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

var fleetRequirementNames = []string{
	"executor-enrollment-v1",
	"executor-offer-arbitration-v1",
	"agent-loss-attempt-fencing-v1",
	"assisted-execution-policy-v1",
}

// safety: every requirement a migration above v29 stamps, so a fixture wound
// back below one of them does not keep listing what it can no longer support.
var postV29RequirementNames = append(append([]string{}, fleetRequirementNames...),
	"cron-schedule-names-v1")

func deleteFleetRequirements(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, name := range postV29RequirementNames {
		if _, err := db.Exec(`DELETE FROM sparkwing_requirements WHERE name = '` + name + `'`); err != nil {
			t.Fatalf("delete Fleet requirement %s: %v", name, err)
		}
	}
}

func assertSQLiteWave2AndFleetV30(t *testing.T, db *sql.DB) {
	t.Helper()
	if got := readSchemaVersion(t, db); got != store.ExpectedSchemaVersion() {
		t.Fatalf("schema version = %d, want %d", got, store.ExpectedSchemaVersion())
	}
	if got, want := requirementNames(t, db), store.KnownRequirements(); !reflect.DeepEqual(got, want) {
		t.Fatalf("requirements = %v, want %v", got, want)
	}
	assertSQLiteNodeMetricsRunCascade(t, db)
	if got := countIndexesNamed(t, db, "idx_concurrency_cache_origin_run"); got != 1 {
		t.Errorf("concurrency origin index count = %d, want 1", got)
	}
	for table, columns := range map[string][]string{
		"nodes":                   {"seq", "claim_executor", "offer_started_at", "claim_generation"},
		"executors":               {"executor_id"},
		"node_claim_offers":       {"reservation_id"},
		"agent_loss_retries":      {"deadline_at"},
		"node_execution_attempts": {"attempt_ordinal"},
		"run_definition_plans":    {"plan_hash"},
	} {
		for _, column := range columns {
			var count int
			if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, table, column).Scan(&count); err != nil {
				t.Fatalf("inspect %s.%s: %v", table, column, err)
			}
			if count != 1 {
				t.Errorf("missing %s.%s", table, column)
			}
		}
	}
}

func assertSQLiteNodeMetricsRunCascade(t *testing.T, db *sql.DB) {
	t.Helper()
	var total, exact int
	if err := db.QueryRow(`SELECT COUNT(*),
       COALESCE(SUM(CASE
         WHEN "from" = 'run_id' AND "to" = 'id' AND upper(on_delete) = 'CASCADE' THEN 1
         ELSE 0
       END), 0)
  FROM pragma_foreign_key_list('node_metrics')
 WHERE "table" = 'runs'`).Scan(&total, &exact); err != nil {
		t.Fatalf("inspect node_metrics run foreign key: %v", err)
	}
	if total != 1 || exact != 1 {
		t.Errorf("node_metrics run foreign keys: total=%d exact-cascade=%d, want 1 and 1", total, exact)
	}
}

func TestSchemaV30FreshSQLiteHasWave2AndFleetShape(t *testing.T) {
	st, err := storetest.NewSQLite(t).TryOpen()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	assertSQLiteWave2AndFleetV30(t, st.DB())
}

func assertPostgresWave2AndFleetV30(t *testing.T, st *store.Store) {
	t.Helper()
	ctx := context.Background()
	if got, err := st.CurrentSchemaVersion(ctx); err != nil || got != store.ExpectedSchemaVersion() {
		t.Fatalf("schema version = %d, %v; want %d", got, err, store.ExpectedSchemaVersion())
	}
	if got, want := requirementNames(t, st.DB()), store.KnownRequirements(); !reflect.DeepEqual(got, want) {
		t.Fatalf("requirements = %v, want %v", got, want)
	}
	var totalRunFK, exactCascade, originIndex, nodeSeq int
	if err := st.DB().QueryRowContext(ctx, `WITH run_foreign_keys AS (
    SELECT c.confdeltype,
           c.conkey = ARRAY[child_column.attnum]
             AND c.confkey = ARRAY[parent_column.attnum] AS exact
      FROM pg_constraint c
      JOIN pg_class child_table ON child_table.oid = c.conrelid
      JOIN pg_namespace child_schema ON child_schema.oid = child_table.relnamespace
      JOIN pg_class parent_table ON parent_table.oid = c.confrelid
      JOIN pg_namespace parent_schema ON parent_schema.oid = parent_table.relnamespace
      JOIN pg_attribute child_column
        ON child_column.attrelid = child_table.oid AND child_column.attname = 'run_id'
      JOIN pg_attribute parent_column
        ON parent_column.attrelid = parent_table.oid AND parent_column.attname = 'id'
      WHERE c.contype = 'f'
        AND child_schema.nspname = current_schema()
        AND parent_schema.nspname = current_schema()
        AND child_table.relname = 'node_metrics'
        AND parent_table.relname = 'runs'
)
SELECT
    (SELECT COUNT(*) FROM run_foreign_keys),
    (SELECT COUNT(*) FROM run_foreign_keys WHERE confdeltype = 'c' AND exact),
    (SELECT COUNT(*) FROM pg_indexes
      WHERE schemaname = current_schema() AND indexname = 'idx_concurrency_cache_origin_run'),
    (SELECT COUNT(*) FROM information_schema.columns
      WHERE table_schema = current_schema() AND table_name = 'nodes' AND column_name = 'seq')`,
	).Scan(&totalRunFK, &exactCascade, &originIndex, &nodeSeq); err != nil {
		t.Fatal(err)
	}
	if totalRunFK != 1 || exactCascade != 1 || originIndex != 1 || nodeSeq != 1 {
		t.Errorf("wave2 invariants: run-fk=%d exact-cascade=%d origin-index=%d node-seq=%d", totalRunFK, exactCascade, originIndex, nodeSeq)
	}
	for table, column := range map[string]string{
		"executors":               "executor_id",
		"node_claim_offers":       "reservation_id",
		"agent_loss_retries":      "deadline_at",
		"node_execution_attempts": "attempt_ordinal",
		"run_definition_plans":    "plan_hash",
	} {
		var count int
		if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.columns
             WHERE table_schema = current_schema() AND table_name = $1 AND column_name = $2`,
			table, column).Scan(&count); err != nil {
			t.Fatalf("inspect %s.%s: %v", table, column, err)
		}
		if count != 1 {
			t.Errorf("missing %s.%s", table, column)
		}
	}
}

func TestSchemaV30FreshPostgresHasWave2AndFleetShape(t *testing.T) {
	st := openPGTestStore(t)
	assertPostgresWave2AndFleetV30(t, st)
}
