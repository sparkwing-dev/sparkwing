package store_test

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

var fleetRequirementNames = []string{
	"executor-enrollment-v1",
	"executor-offer-arbitration-v1",
	"agent-loss-attempt-fencing-v1",
}

func deleteFleetRequirements(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, name := range fleetRequirementNames {
		if _, err := db.Exec(`DELETE FROM sparkwing_requirements WHERE name = '` + name + `'`); err != nil {
			t.Fatalf("delete Fleet requirement %s: %v", name, err)
		}
	}
}

func seedLegacyFleetPostgres(t *testing.T, dsn string, stage int) {
	t.Helper()
	ctx := context.Background()
	st, err := store.OpenPostgres(ctx, dsn)
	if err != nil {
		t.Fatalf("open Postgres fixture: %v", err)
	}
	defer func() { _ = st.Close() }()

	statements := []string{
		`DROP INDEX idx_concurrency_cache_origin_run`,
		`ALTER TABLE nodes DROP COLUMN seq`,
		`ALTER TABLE node_metrics DROP CONSTRAINT node_metrics_run_id_fkey`,
	}
	if stage < 30 {
		statements = append(statements,
			`DROP TABLE run_definition_plans`,
			`DROP TABLE node_execution_attempts`,
			`DROP TABLE agent_loss_retries`,
			`DROP INDEX idx_triggers_pending`,
			`ALTER TABLE triggers DROP COLUMN available_at`,
			`CREATE INDEX idx_triggers_pending ON triggers(status, created_at) WHERE status = 'pending'`,
			`ALTER TABLE nodes DROP COLUMN required_executor_location`,
			`ALTER TABLE nodes DROP COLUMN required_coordinator_id`,
			`ALTER TABLE nodes DROP COLUMN executor_location`,
			`ALTER TABLE nodes DROP COLUMN retry_root_run_id`,
			`ALTER TABLE nodes DROP COLUMN attempts_consumed`,
			`ALTER TABLE nodes DROP COLUMN claim_membership_id`,
			`ALTER TABLE nodes DROP COLUMN claim_generation`,
			`ALTER TABLE nodes DROP COLUMN avoid_until`,
			`ALTER TABLE nodes DROP COLUMN avoid_executor_id`,
			`ALTER TABLE nodes DROP COLUMN avoid_executor_kind`,
			`ALTER TABLE nodes DROP COLUMN avoid_coordinator_id`,
			`ALTER TABLE nodes DROP COLUMN reservation_id`,
			`ALTER TABLE nodes DROP COLUMN execution_started_at`,
			`ALTER TABLE nodes DROP COLUMN executor_id`,
			`ALTER TABLE nodes DROP COLUMN executor_kind`,
			`ALTER TABLE nodes DROP COLUMN coordinator_id`,
			`ALTER TABLE runs DROP COLUMN retry_avoid_until`,
			`ALTER TABLE runs DROP COLUMN retry_avoid_executor_id`,
			`ALTER TABLE runs DROP COLUMN retry_avoid_executor_kind`,
			`ALTER TABLE runs DROP COLUMN retry_avoid_coordinator_id`,
			`ALTER TABLE runs DROP COLUMN retry_cause_node_id`,
		)
	}
	if stage < 29 {
		statements = append(statements,
			`DROP TABLE node_claim_offers`,
			`ALTER TABLE nodes DROP COLUMN prefers_labels`,
			`ALTER TABLE nodes DROP COLUMN requested_cores`,
			`ALTER TABLE nodes DROP COLUMN requested_memory_bytes`,
			`ALTER TABLE nodes DROP COLUMN requested_slots`,
			`ALTER TABLE nodes DROP COLUMN offer_started_at`,
			`ALTER TABLE nodes DROP COLUMN offer_priority_target`,
			`ALTER TABLE nodes DROP COLUMN claim_base_priority`,
			`ALTER TABLE nodes DROP COLUMN claim_priority`,
			`ALTER TABLE nodes DROP COLUMN claim_worker_id`,
			`ALTER TABLE nodes DROP COLUMN claim_executor_kind`,
			`ALTER TABLE nodes DROP COLUMN claim_reservation_id`,
			`DROP INDEX idx_executors_executor_id`,
			`ALTER TABLE executors DROP COLUMN executor_id`,
		)
	}
	for _, statement := range statements {
		if _, err := st.DB().ExecContext(ctx, statement); err != nil {
			t.Fatalf("seed old Fleet Postgres v%d with %q: %v", stage, statement, err)
		}
	}
	if _, err := st.DB().ExecContext(ctx,
		fmt.Sprintf(`DELETE FROM sparkwing_schema_version WHERE version > %d`, stage)); err != nil {
		t.Fatalf("record old Fleet Postgres v%d: %v", stage, err)
	}
	deleteFleetRequirements(t, st.DB())
	for i, name := range fleetRequirementNames {
		if i+28 > stage {
			break
		}
		if _, err := st.DB().ExecContext(ctx,
			`INSERT INTO sparkwing_requirements (name, added_at, added_by_version) VALUES ('`+name+`', 1, 'v0.0.0-old-fleet')`); err != nil {
			t.Fatalf("record old Fleet Postgres requirement %s: %v", name, err)
		}
	}
}

func seedLegacyFleetSQLite(t *testing.T, path string, stage int) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer func() { _ = st.Close() }()

	statements := []string{
		`DROP INDEX idx_concurrency_cache_origin_run`,
		`ALTER TABLE nodes DROP COLUMN seq`,
		`DROP TABLE node_metrics`,
		`CREATE TABLE node_metrics (
            run_id TEXT NOT NULL, node_id TEXT NOT NULL, ts INTEGER NOT NULL,
            cpu_millicores INTEGER NOT NULL, memory_bytes INTEGER NOT NULL,
            cpu_time_nanos INTEGER NOT NULL DEFAULT 0,
            PRIMARY KEY (run_id, node_id, ts)
        )`,
		`CREATE INDEX idx_node_metrics_lookup ON node_metrics(run_id, node_id, ts)`,
	}
	if stage < 30 {
		statements = append(statements,
			`DROP TABLE run_definition_plans`,
			`DROP TABLE node_execution_attempts`,
			`DROP TABLE agent_loss_retries`,
			`DROP INDEX idx_triggers_pending`,
			`ALTER TABLE triggers DROP COLUMN available_at`,
			`CREATE INDEX idx_triggers_pending ON triggers(status, created_at) WHERE status = 'pending'`,
			`ALTER TABLE nodes DROP COLUMN required_executor_location`,
			`ALTER TABLE nodes DROP COLUMN required_coordinator_id`,
			`ALTER TABLE nodes DROP COLUMN executor_location`,
			`ALTER TABLE nodes DROP COLUMN retry_root_run_id`,
			`ALTER TABLE nodes DROP COLUMN attempts_consumed`,
			`ALTER TABLE nodes DROP COLUMN claim_membership_id`,
			`ALTER TABLE nodes DROP COLUMN claim_generation`,
			`ALTER TABLE nodes DROP COLUMN avoid_until`,
			`ALTER TABLE nodes DROP COLUMN avoid_executor_id`,
			`ALTER TABLE nodes DROP COLUMN avoid_executor_kind`,
			`ALTER TABLE nodes DROP COLUMN avoid_coordinator_id`,
			`ALTER TABLE nodes DROP COLUMN reservation_id`,
			`ALTER TABLE nodes DROP COLUMN execution_started_at`,
			`ALTER TABLE nodes DROP COLUMN executor_id`,
			`ALTER TABLE nodes DROP COLUMN executor_kind`,
			`ALTER TABLE nodes DROP COLUMN coordinator_id`,
			`ALTER TABLE runs DROP COLUMN retry_avoid_until`,
			`ALTER TABLE runs DROP COLUMN retry_avoid_executor_id`,
			`ALTER TABLE runs DROP COLUMN retry_avoid_executor_kind`,
			`ALTER TABLE runs DROP COLUMN retry_avoid_coordinator_id`,
			`ALTER TABLE runs DROP COLUMN retry_cause_node_id`,
		)
	}
	if stage < 29 {
		statements = append(statements,
			`DROP TABLE node_claim_offers`,
			`ALTER TABLE nodes DROP COLUMN prefers_labels`,
			`ALTER TABLE nodes DROP COLUMN requested_cores`,
			`ALTER TABLE nodes DROP COLUMN requested_memory_bytes`,
			`ALTER TABLE nodes DROP COLUMN requested_slots`,
			`ALTER TABLE nodes DROP COLUMN offer_started_at`,
			`ALTER TABLE nodes DROP COLUMN offer_priority_target`,
			`ALTER TABLE nodes DROP COLUMN claim_base_priority`,
			`ALTER TABLE nodes DROP COLUMN claim_priority`,
			`ALTER TABLE nodes DROP COLUMN claim_worker_id`,
			`ALTER TABLE nodes DROP COLUMN claim_executor_kind`,
			`ALTER TABLE nodes DROP COLUMN claim_reservation_id`,
			`DROP INDEX idx_executors_executor_id`,
			`ALTER TABLE executors DROP COLUMN executor_id`,
		)
	}
	for _, statement := range statements {
		if _, err := st.DB().ExecContext(ctx, statement); err != nil {
			t.Fatalf("seed old Fleet v%d with %q: %v", stage, statement, err)
		}
	}
	if _, err := st.DB().ExecContext(ctx,
		`DELETE FROM sparkwing_schema_version WHERE version > ?`, stage); err != nil {
		t.Fatalf("record old Fleet v%d: %v", stage, err)
	}
	deleteFleetRequirements(t, st.DB())
	for i, name := range fleetRequirementNames {
		if i+28 > stage {
			break
		}
		if _, err := st.DB().ExecContext(ctx,
			`INSERT INTO sparkwing_requirements (name, added_at, added_by_version) VALUES (?, 1, 'v0.0.0-old-fleet')`,
			name); err != nil {
			t.Fatalf("record old Fleet requirement %s: %v", name, err)
		}
	}
}

func assertSQLiteWave2AndFleetV30(t *testing.T, db *sql.DB) {
	t.Helper()
	if got := readSchemaVersion(t, db); got != 30 {
		t.Fatalf("schema version = %d, want 30", got)
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
	st, err := store.Open(filepath.Join(t.TempDir(), "fresh.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	assertSQLiteWave2AndFleetV30(t, st.DB())
}

func assertPostgresWave2AndFleetV30(t *testing.T, st *store.Store) {
	t.Helper()
	ctx := context.Background()
	if got, err := st.CurrentSchemaVersion(ctx); err != nil || got != 30 {
		t.Fatalf("schema version = %d, %v; want 30", got, err)
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

func TestSchemaV30RepairsRecognizableOldFleetPostgresLineages(t *testing.T) {
	for _, stage := range []int{28, 29, 30} {
		t.Run(fmt.Sprintf("v%d", stage), func(t *testing.T) {
			dsn := pgTestSchemaDSN(t)
			seedLegacyFleetPostgres(t, dsn, stage)
			upgraded, err := store.OpenPostgres(context.Background(), dsn)
			if err != nil {
				t.Fatalf("open old Fleet Postgres v%d: %v", stage, err)
			}
			defer func() { _ = upgraded.Close() }()
			assertPostgresWave2AndFleetV30(t, upgraded)
		})
	}
}

func TestSchemaV30RepairsRecognizableOldFleetSQLiteLineages(t *testing.T) {
	for _, stage := range []int{28, 29, 30} {
		t.Run(fmt.Sprintf("v%d", stage), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "old-fleet.db")
			seedLegacyFleetSQLite(t, path, stage)
			upgraded, err := store.Open(path)
			if err != nil {
				t.Fatalf("open old Fleet v%d: %v", stage, err)
			}
			defer func() { _ = upgraded.Close() }()
			assertSQLiteWave2AndFleetV30(t, upgraded.DB())
		})
	}
}

func TestSchemaV30OldFleetBridgeReplacesSQLiteNoActionRunForeignKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old-fleet-no-action.db")
	seedLegacyFleetSQLite(t, path, 30)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE node_metrics;
CREATE TABLE node_metrics (
    run_id TEXT NOT NULL,
    node_id TEXT NOT NULL,
    ts INTEGER NOT NULL,
    cpu_millicores INTEGER NOT NULL,
    memory_bytes INTEGER NOT NULL,
    cpu_time_nanos INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (run_id, node_id, ts),
    CONSTRAINT old_fleet_custom_run_fk
      FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE NO ACTION
);
CREATE INDEX idx_node_metrics_lookup ON node_metrics(run_id, node_id, ts)`); err != nil {
		_ = db.Close()
		t.Fatalf("replace node_metrics with old custom foreign key: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	upgraded, err := store.Open(path)
	if err != nil {
		t.Fatalf("open old Fleet database: %v", err)
	}
	defer func() { _ = upgraded.Close() }()
	assertSQLiteNodeMetricsRunCascade(t, upgraded.DB())
	seedRunWithMetricSample(t, upgraded, "delete-me", time.Now())
	if err := upgraded.DeleteRun(context.Background(), "delete-me"); err != nil {
		t.Fatalf("DeleteRun after bridge repair: %v", err)
	}
	if got := countNodeMetrics(t, upgraded.DB()); got != 0 {
		t.Fatalf("node_metrics after DeleteRun = %d, want 0", got)
	}
}

func TestSchemaV30OldFleetBridgeReplacesPostgresCustomNoActionRunForeignKey(t *testing.T) {
	dsn := pgTestSchemaDSN(t)
	seedLegacyFleetPostgres(t, dsn, 30)
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`ALTER TABLE node_metrics
ADD CONSTRAINT "old fleet ""custom"" run fk"
FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE NO ACTION`); err != nil {
		_ = db.Close()
		t.Fatalf("add old custom foreign key: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	upgraded, err := store.OpenPostgres(context.Background(), dsn)
	if err != nil {
		t.Fatalf("open old Fleet Postgres database: %v", err)
	}
	defer func() { _ = upgraded.Close() }()
	assertPostgresWave2AndFleetV30(t, upgraded)
	seedRunWithMetricSample(t, upgraded, "delete-me", time.Now())
	if err := upgraded.DeleteRun(context.Background(), "delete-me"); err != nil {
		t.Fatalf("DeleteRun after bridge repair: %v", err)
	}
	if got := countNodeMetrics(t, upgraded.DB()); got != 0 {
		t.Fatalf("node_metrics after DeleteRun = %d, want 0", got)
	}
}

func TestSchemaV30OldFleetBridgeFailsClosedBeforeRepair(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corrupt-old-fleet.db")
	seedLegacyFleetSQLite(t, path, 28)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`ALTER TABLE nodes DROP COLUMN claim_slot`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := store.Open(path); err == nil || !strings.Contains(err.Error(), "unrecognized or corrupt unpublished Fleet schema v28") {
		t.Fatalf("Open error = %v, want corrupt old Fleet refusal", err)
	}
	db, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if got := countIndexesNamed(t, db, "idx_concurrency_cache_origin_run"); got != 0 {
		t.Fatalf("failed validation partly repaired concurrency index: count=%d", got)
	}
}

func TestSchemaV30OldFleetBridgeRejectsRequirementVersionMismatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mismatched-old-fleet.db")
	seedLegacyFleetSQLite(t, path, 29)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM sparkwing_requirements WHERE name = 'executor-offer-arbitration-v1'`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Open(path); err == nil || !strings.Contains(err.Error(), "unrecognized unpublished Fleet schema lineage at v29") {
		t.Fatalf("Open error = %v, want requirement/version mismatch refusal", err)
	}
}
