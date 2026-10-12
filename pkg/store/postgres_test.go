package store_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func pgTestDSN(t *testing.T) string {
	t.Helper()
	return storetest.PostgresURL(t)
}

func openPGTestStore(t *testing.T) *store.Store {
	t.Helper()
	return storetest.OpenPostgres(t)
}

func pgTestSchemaDSN(t *testing.T) string {
	t.Helper()
	return storetest.NewPostgres(t).DSN()
}

func TestPostgresOpenAndMigrate(t *testing.T) {
	st := openPGTestStore(t)
	if got, want := st.Dialect(), store.DialectPostgres; got != want {
		t.Errorf("Dialect = %v, want %v", got, want)
	}
	ctx := context.Background()
	r := store.Run{
		ID:        "pg-open-test",
		Pipeline:  "p",
		Status:    "running",
		StartedAt: time.Now(),
	}
	if err := st.CreateRun(ctx, r); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	got, err := st.GetRun(ctx, r.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got == nil || got.ID != r.ID {
		t.Fatalf("GetRun returned %+v, want id %q", got, r.ID)
	}
}

func TestSchemaV29_PostgresFreshShape(t *testing.T) {
	st := openPGTestStore(t)
	assertPostgresNodeOfferColumnTypes(t, st)
}

func TestSchemaV31PostgresFreshAndV30UpgradeShape(t *testing.T) {
	t.Run("fresh", func(t *testing.T) {
		assertPostgresExecutionPolicyShape(t, openPGTestStore(t))
	})
	t.Run("v30 upgrade", func(t *testing.T) {
		scoped := pgTestSchemaDSN(t)
		ctx := context.Background()
		st, err := store.OpenPostgres(ctx, scoped)
		if err != nil {
			t.Fatal(err)
		}
		statements := []string{
			`DROP INDEX idx_nodes_assisted_claimable`,
			`DROP TABLE agent_loss_retry_legacy_deny_all`,
			`DROP TABLE agent_loss_retry_node_sources`,
		}
		for _, column := range []string{
			"execution_policy_json", "execution_policy_hash", "execution_policy_version", "execution_body_protocol",
			"execution_supervisor_requirements_json", "execution_supervisor_requirements_hash",
			"execution_body_requirements_json", "execution_body_requirements_hash",
		} {
			statements = append(statements, `ALTER TABLE nodes DROP COLUMN `+column)
		}
		statements = append(statements,
			`DELETE FROM sparkwing_requirements WHERE name = 'assisted-execution-policy-v1'`,
			`DELETE FROM sparkwing_schema_version WHERE version >= 31`,
		)
		for _, statement := range statements {
			if _, err := st.DB().ExecContext(ctx, statement); err != nil {
				_ = st.Close()
				t.Fatalf("downgrade with %q: %v", statement, err)
			}
		}
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
		up, err := store.OpenPostgres(ctx, scoped)
		if err != nil {
			t.Fatalf("upgrade v30: %v", err)
		}
		defer up.Close()
		assertPostgresExecutionPolicyShape(t, up)
	})
}

func TestSchemaV32PostgresFreshAndV31UpgradeShape(t *testing.T) {
	t.Run("fresh", func(t *testing.T) {
		assertPostgresCronShape(t, openPGTestStore(t))
	})
	t.Run("v31 upgrade", func(t *testing.T) {
		scoped := pgTestSchemaDSN(t)
		ctx := context.Background()
		st, err := store.OpenPostgres(ctx, scoped)
		if err != nil {
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
		up, err := store.OpenPostgres(ctx, scoped)
		if err != nil {
			t.Fatalf("upgrade v31: %v", err)
		}
		defer up.Close()
		assertPostgresCronShape(t, up)
		if got, err := up.CurrentSchemaVersion(ctx); err != nil || got != store.ExpectedSchemaVersion() {
			t.Fatalf("schema version = %d, err = %v, want %d", got, err, store.ExpectedSchemaVersion())
		}
	})
}

func assertPostgresCronShape(t *testing.T, st *store.Store) {
	t.Helper()
	ctx := context.Background()
	for table, columns := range map[string][]string{
		"cron_schedules": {
			"id", "repo_path", "pipeline", "cron", "tz", "overlap", "catch_up_ns",
			"paused", "declared", "armed_at", "armed_by", "updated_at", "cursor_at",
			"last_fired_at", "last_run_id", "last_outcome", "next_due_at",
		},
		"cron_fires": {"id", "schedule_id", "due_at", "decided_at", "outcome", "run_id", "detail"},
	} {
		for _, column := range columns {
			var present int
			if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.columns
 WHERE table_schema = current_schema() AND table_name = $1 AND column_name = $2`,
				table, column).Scan(&present); err != nil {
				t.Fatalf("inspect %s.%s: %v", table, column, err)
			}
			if present != 1 {
				t.Errorf("postgres schema missing %s.%s", table, column)
			}
		}
	}
	// safety: the SQLite twin declares these as INTEGER, so a replacer miss
	// would silently give Postgres a 32-bit column that truncates UnixNano.
	for table, columns := range map[string][]string{
		"cron_schedules": {"catch_up_ns", "armed_at", "updated_at", "cursor_at", "last_fired_at", "next_due_at"},
		"cron_fires":     {"due_at", "decided_at"},
	} {
		for _, column := range columns {
			var dataType string
			if err := st.DB().QueryRowContext(ctx, `SELECT data_type FROM information_schema.columns
 WHERE table_schema = current_schema() AND table_name = $1 AND column_name = $2`,
				table, column).Scan(&dataType); err != nil {
				t.Fatalf("inspect %s.%s type: %v", table, column, err)
			}
			if dataType != "bigint" {
				t.Errorf("%s.%s is %s, want bigint", table, column, dataType)
			}
		}
	}
	var indexes int
	if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM pg_indexes
 WHERE schemaname = current_schema() AND indexname = 'idx_cron_fires_schedule_decided'`).Scan(&indexes); err != nil {
		t.Fatal(err)
	}
	if indexes != 1 {
		t.Errorf("idx_cron_fires_schedule_decided count = %d, want 1", indexes)
	}
}

func TestSchemaV33PostgresFreshAndV32UpgradeShape(t *testing.T) {
	t.Run("fresh", func(t *testing.T) {
		assertPostgresNamedCronShape(t, openPGTestStore(t))
	})
	t.Run("v32 upgrade", func(t *testing.T) {
		scoped := pgTestSchemaDSN(t)
		ctx := context.Background()
		st, err := store.OpenPostgres(ctx, scoped)
		if err != nil {
			t.Fatal(err)
		}
		statements := []string{`DROP INDEX idx_cron_schedules_repo_pipeline_name`}
		for _, column := range []string{
			"schedule_name", "where_", "git_branch", "args", "locked_ref", "locked_binary", "locked_digest",
			"override_cron", "override_tz", "override_overlap", "override_catch_up_ns",
			"override_args", "override_base", "override_set_at",
		} {
			statements = append(statements, `ALTER TABLE cron_schedules DROP COLUMN `+column)
		}
		statements = append(statements,
			// safety: unnamed, so Postgres mints the constraint name a v32
			// database really carries rather than the test asserting a name it
			// chose for the migration to find.
			`ALTER TABLE cron_schedules ADD UNIQUE (repo_path, pipeline)`,
			`ALTER TABLE cron_fires DROP COLUMN args`,
			`DELETE FROM sparkwing_schema_version WHERE version >= 33`,
		)
		for _, statement := range statements {
			if _, err := st.DB().ExecContext(ctx, statement); err != nil {
				_ = st.Close()
				t.Fatalf("downgrade with %q: %v", statement, err)
			}
		}
		seedV32CronRowsPostgres(t, st)
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
		up, err := store.OpenPostgres(ctx, scoped)
		if err != nil {
			t.Fatalf("upgrade v32: %v", err)
		}
		defer up.Close()
		assertPostgresNamedCronShape(t, up)
		if got, err := up.CurrentSchemaVersion(ctx); err != nil || got != store.ExpectedSchemaVersion() {
			t.Fatalf("schema version = %d, err = %v, want %d", got, err, store.ExpectedSchemaVersion())
		}
		assertTwoNamesOnOnePipeline(t, up, "/repo/one", "nightly")
	})
}

// safety: the rows a v32 store held, so the upgrade is measured against a table
// with data in it rather than an empty one.
func seedV32CronRowsPostgres(t *testing.T, st *store.Store) {
	t.Helper()
	ctx := context.Background()
	armedNS := cronBase.UnixNano()
	if _, err := st.DB().ExecContext(ctx, storetest.Rebind(st,
		`INSERT INTO cron_schedules
         (id, repo_path, pipeline, cron, tz, overlap, catch_up_ns, armed_at, armed_by,
          updated_at, cursor_at, last_run_id, last_outcome)
         VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		"crn_nightly", "/repo/one", "nightly", "0 * * * *", "UTC", store.CronOverlapSkip,
		int64(time.Hour), armedNS, "korey", armedNS, armedNS, "run-1", store.CronOutcomeFired,
	); err != nil {
		t.Fatalf("insert a v32 schedule: %v", err)
	}
	if _, err := st.DB().ExecContext(ctx, storetest.Rebind(st,
		`INSERT INTO cron_fires (id, schedule_id, due_at, decided_at, outcome, run_id, detail)
         VALUES (?, ?, ?, ?, ?, ?, ?)`),
		"crf_nightly", "crn_nightly", armedNS, armedNS, store.CronOutcomeFired, "run-1", "",
	); err != nil {
		t.Fatalf("insert a v32 fire: %v", err)
	}
}

func assertPostgresNamedCronShape(t *testing.T, st *store.Store) {
	t.Helper()
	ctx := context.Background()
	for table, columns := range map[string][]string{
		"cron_schedules": {
			"schedule_name", "where_", "git_branch", "args", "locked_ref", "locked_binary", "locked_digest",
			"override_cron", "override_tz", "override_overlap", "override_catch_up_ns",
			"override_args", "override_base", "override_set_at",
		},
		"cron_fires": {"args"},
	} {
		for _, column := range columns {
			var present int
			if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.columns
 WHERE table_schema = current_schema() AND table_name = $1 AND column_name = $2`,
				table, column).Scan(&present); err != nil {
				t.Fatalf("inspect %s.%s: %v", table, column, err)
			}
			if present != 1 {
				t.Errorf("postgres schema missing %s.%s", table, column)
			}
		}
	}
	// safety: the SQLite twin declares these as INTEGER, so a replacer miss
	// would silently give Postgres a 32-bit column that truncates UnixNano.
	for _, column := range []string{"override_catch_up_ns", "override_set_at"} {
		var dataType string
		if err := st.DB().QueryRowContext(ctx, `SELECT data_type FROM information_schema.columns
 WHERE table_schema = current_schema() AND table_name = 'cron_schedules' AND column_name = $1`,
			column).Scan(&dataType); err != nil {
			t.Fatalf("inspect cron_schedules.%s type: %v", column, err)
		}
		if dataType != "bigint" {
			t.Errorf("cron_schedules.%s is %s, want bigint", column, dataType)
		}
	}
	var indexes int
	if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM pg_indexes
 WHERE schemaname = current_schema() AND indexname = 'idx_cron_schedules_repo_pipeline_name'`).Scan(&indexes); err != nil {
		t.Fatal(err)
	}
	if indexes != 1 {
		t.Errorf("idx_cron_schedules_repo_pipeline_name count = %d, want 1", indexes)
	}
	var narrow int
	if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM pg_constraint
 WHERE conrelid = 'cron_schedules'::regclass AND conname = 'cron_schedules_repo_path_pipeline_key'`).Scan(&narrow); err != nil {
		t.Fatal(err)
	}
	if narrow != 0 {
		t.Errorf("the (repo_path, pipeline) unique constraint survived the widening")
	}
}

func assertPostgresExecutionPolicyShape(t *testing.T, st *store.Store) {
	t.Helper()
	ctx := context.Background()
	for table, columns := range map[string][]string{
		"nodes": {
			"execution_policy_json", "execution_policy_hash", "execution_policy_version", "execution_body_protocol",
			"execution_supervisor_requirements_json", "execution_supervisor_requirements_hash",
			"execution_body_requirements_json", "execution_body_requirements_hash",
		},
		"agent_loss_retry_node_sources": {
			"retry_run_id", "source_run_id", "node_id", "deps_json", "needs_labels_json", "prefers_labels_json",
			"requested_cores", "requested_memory_bytes", "requested_slots", "attempts_consumed",
			"required_coordinator_id", "required_executor_location", "avoid_coordinator_id", "avoid_executor_kind",
			"avoid_executor_id", "avoid_until", "policy_json", "policy_hash", "policy_version", "body_protocol",
			"supervisor_requirements_json", "supervisor_requirements_hash", "body_requirements_json", "body_requirements_hash",
		},
		"agent_loss_retry_legacy_deny_all": {"retry_run_id", "source_run_id", "reason"},
	} {
		for _, column := range columns {
			var exists bool
			if err := st.DB().QueryRowContext(ctx, `SELECT EXISTS (
  SELECT 1 FROM information_schema.columns
   WHERE table_schema = current_schema() AND table_name = $1 AND column_name = $2
)`, table, column).Scan(&exists); err != nil {
				t.Fatal(err)
			}
			if !exists {
				t.Errorf("missing %s.%s", table, column)
			}
		}
	}
	for column, want := range map[[2]string]string{
		{"nodes", "execution_policy_json"}:                   "bytea",
		{"nodes", "execution_policy_version"}:                "bigint",
		{"agent_loss_retry_node_sources", "requested_cores"}: "double precision",
		{"agent_loss_retry_node_sources", "requested_slots"}: "bigint",
		{"agent_loss_retry_node_sources", "policy_json"}:     "bytea",
		{"agent_loss_retry_node_sources", "avoid_until"}:     "bigint",
	} {
		var got string
		if err := st.DB().QueryRowContext(ctx, `SELECT data_type FROM information_schema.columns
 WHERE table_schema = current_schema() AND table_name = $1 AND column_name = $2`, column[0], column[1]).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("%s.%s type = %q, want %q", column[0], column[1], got, want)
		}
	}
	var retryCascade, sourceForeignKeys int
	if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FILTER (WHERE pg_get_constraintdef(c.oid) = 'FOREIGN KEY (retry_run_id) REFERENCES runs(id) ON DELETE CASCADE'),
       COUNT(*) FILTER (WHERE pg_get_constraintdef(c.oid) LIKE 'FOREIGN KEY (source_run_id)%')
  FROM pg_constraint c
  JOIN pg_class rel ON rel.oid = c.conrelid
 WHERE rel.relnamespace = current_schema()::regnamespace
   AND rel.relname = 'agent_loss_retry_node_sources' AND c.contype = 'f'`).Scan(&retryCascade, &sourceForeignKeys); err != nil {
		t.Fatal(err)
	}
	if retryCascade != 1 || sourceForeignKeys != 0 {
		t.Errorf("retry snapshot FKs: cascade=%d source=%d", retryCascade, sourceForeignKeys)
	}
	var requirement int
	if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM sparkwing_requirements WHERE name = 'assisted-execution-policy-v1'`).Scan(&requirement); err != nil {
		t.Fatal(err)
	}
	if requirement != 1 {
		t.Errorf("policy requirement count = %d, want 1", requirement)
	}
}

func assertPostgresNodeOfferColumnTypes(t *testing.T, st *store.Store) {
	t.Helper()
	ctx := context.Background()
	for _, column := range []struct{ name, want string }{
		{name: "prefers_labels", want: "bytea"},
		{name: "requested_cores", want: "double precision"},
		{name: "requested_memory_bytes", want: "bigint"},
		{name: "requested_slots", want: "bigint"},
		{name: "offer_started_at", want: "bigint"},
		{name: "offer_priority_target", want: "bigint"},
		{name: "claim_base_priority", want: "bigint"},
		{name: "claim_priority", want: "bigint"},
		{name: "claim_worker_id", want: "text"},
		{name: "claim_executor_kind", want: "text"},
		{name: "claim_reservation_id", want: "text"},
	} {
		var dataType string
		if err := st.DB().QueryRowContext(ctx, `
SELECT data_type FROM information_schema.columns
 WHERE table_schema = current_schema() AND table_name = 'nodes' AND column_name = $1`,
			column.name).Scan(&dataType); err != nil {
			t.Fatal(err)
		}
		if dataType != column.want {
			t.Errorf("nodes.%s type = %q, want %s", column.name, dataType, column.want)
		}
	}
}

func TestSchemaV18_PostgresScrubsSecretInputHash(t *testing.T) {
	scoped := pgTestSchemaDSN(t)
	ctx := context.Background()
	st, err := store.OpenPostgres(ctx, scoped)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateRun(ctx, store.Run{
		ID: "legacy-secret", Pipeline: "deploy", Status: "success", StartedAt: time.Now(),
		Args: map[string]string{"token": "low-entropy"},
		Invocation: map[string]any{
			"args":                        map[string]string{"token": "low-entropy"},
			store.InvocationSecretArgsKey: []string{"token"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	legacy := []byte(`{"args":{"token":"low-entropy"},"inputs_hash":"sha256:oracle","secret_args":["token"]}`)
	if _, err := st.DB().ExecContext(ctx, `UPDATE runs SET invocation_json = $1 WHERE id = 'legacy-secret'`, legacy); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().ExecContext(ctx, `DELETE FROM sparkwing_schema_version WHERE version >= 18`); err != nil {
		t.Fatal(err)
	}
	deleteFleetRequirements(t, st.DB())
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	up, err := store.OpenPostgres(ctx, scoped)
	if err != nil {
		t.Fatal(err)
	}
	defer up.Close()
	var raw []byte
	if err := up.DB().QueryRowContext(ctx,
		`SELECT invocation_json FROM runs WHERE id = 'legacy-secret'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "inputs_hash") {
		t.Errorf("Postgres migration retained secret inputs_hash: %s", raw)
	}
}

func TestPostgresReconcileOrphanedLocalRuns(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()

	if err := st.CreateRun(ctx, store.Run{
		ID: "pg-orphan", Pipeline: "p", Status: "running",
		StartedAt: time.Now().Add(-10 * time.Minute),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if _, err := st.DB().ExecContext(ctx,
		`UPDATE runs SET last_heartbeat_at = NULL WHERE id = $1`, "pg-orphan"); err != nil {
		t.Fatalf("clear run heartbeat: %v", err)
	}
	if err := st.CreateNode(ctx, store.Node{
		RunID: "pg-orphan", NodeID: "build", Status: "running",
	}); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}

	n, err := store.Maintenance.ReconcileOrphanedLocalRuns(st, ctx, 60*time.Second)
	if err != nil {
		t.Fatalf("ReconcileOrphanedLocalRuns: %v", err)
	}
	if n != 1 {
		t.Fatalf("reconciled count = %d, want 1", n)
	}
	got, err := st.GetRun(ctx, "pg-orphan")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got == nil || got.Status != "failed" {
		t.Fatalf("run = %+v, want status failed", got)
	}
}

func TestSchemaV30CompositeRestoresAgentLossFieldsPostgres(t *testing.T) {
	scoped := pgTestSchemaDSN(t)
	ctx := context.Background()
	st, err := store.OpenPostgres(ctx, scoped)
	if err != nil {
		t.Fatal(err)
	}
	deleteFleetRequirements(t, st.DB())
	for _, stmt := range []string{
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
		`DELETE FROM sparkwing_schema_version WHERE version >= 30`,
	} {
		if _, err := st.DB().ExecContext(ctx, stmt); err != nil {
			_ = st.Close()
			t.Fatalf("downgrade with %q: %v", stmt, err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	up, err := store.OpenPostgres(ctx, scoped)
	if err != nil {
		t.Fatalf("open v29 shape at schema %d: %v", store.ExpectedSchemaVersion(), err)
	}
	defer up.Close()
	if got, err := up.CurrentSchemaVersion(ctx); err != nil || got != store.ExpectedSchemaVersion() {
		t.Fatalf("schema version = %d, err=%v, want %d", got, err, store.ExpectedSchemaVersion())
	}
	for table, names := range map[string][]string{
		"runs":     {"retry_cause_node_id", "retry_avoid_coordinator_id", "retry_avoid_executor_kind", "retry_avoid_executor_id", "retry_avoid_until"},
		"nodes":    {"coordinator_id", "executor_kind", "executor_id", "execution_started_at", "reservation_id", "avoid_coordinator_id", "avoid_executor_kind", "avoid_executor_id", "avoid_until", "claim_generation", "claim_membership_id", "attempts_consumed", "retry_root_run_id", "executor_location", "required_coordinator_id", "required_executor_location"},
		"triggers": {"available_at"},
	} {
		for _, name := range names {
			var exists bool
			if err := up.DB().QueryRowContext(ctx, `SELECT EXISTS (
  SELECT 1 FROM information_schema.columns
   WHERE table_schema = current_schema() AND table_name = $1 AND column_name = $2
)`, table, name).Scan(&exists); err != nil {
				t.Fatal(err)
			}
			if !exists {
				t.Errorf("migrated schema missing %s.%s", table, name)
			}
		}
	}
	for _, table := range []string{"agent_loss_retries", "node_execution_attempts", "run_definition_plans"} {
		var exists bool
		if err := up.DB().QueryRowContext(ctx, `SELECT EXISTS (
  SELECT 1 FROM information_schema.tables
   WHERE table_schema = current_schema() AND table_name = $1
)`, table).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Errorf("migrated schema missing table %s", table)
		}
	}
	for column, want := range map[[2]string]string{
		{"runs", "retry_avoid_until"}:                   "bigint",
		{"nodes", "execution_started_at"}:               "bigint",
		{"nodes", "claim_generation"}:                   "bigint",
		{"nodes", "attempts_consumed"}:                  "bigint",
		{"triggers", "available_at"}:                    "bigint",
		{"agent_loss_retries", "cause_nodes_json"}:      "bytea",
		{"agent_loss_retries", "deadline_at"}:           "bigint",
		{"node_execution_attempts", "attempt_ordinal"}:  "bigint",
		{"node_execution_attempts", "claim_generation"}: "bigint",
		{"node_execution_attempts", "started_at"}:       "bigint",
		{"node_execution_attempts", "finished_at"}:      "bigint",
	} {
		var got string
		if err := up.DB().QueryRowContext(ctx, `SELECT data_type
  FROM information_schema.columns
 WHERE table_schema = current_schema() AND table_name = $1 AND column_name = $2`,
			column[0], column[1]).Scan(&got); err != nil {
			t.Fatalf("inspect %s.%s type: %v", column[0], column[1], err)
		}
		if got != want {
			t.Errorf("%s.%s type = %q, want %q", column[0], column[1], got, want)
		}
	}
}

func TestSchemaV30_PostgresPersistsAttemptAndSchedulesAgentLossRetry(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()
	createRetryRunAndReadyNode(t, st, "run-pg-agent-loss", 1)
	identity := store.ClaimIdentity{Principal: "agent:pg-desktop", TokenPrefix: "swr_pg_desktop"}
	claimed := claimRetryNode(t, st, "run-pg-agent-loss", identity, "agent:pg-desktop:0")
	ackNodeAttempt(t, st, claimed, identity, 1)
	if _, err := st.DB().ExecContext(ctx, `UPDATE nodes SET lease_expires_at = $1 WHERE run_id = $2 AND node_id = $3`,
		time.Now().Add(-time.Second).UnixNano(), claimed.RunID, claimed.NodeID); err != nil {
		t.Fatal(err)
	}
	recovered, err := store.Maintenance.RecoverExpiredNodeClaims(st, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered) != 1 || recovered[0].RetryRunID == "" || !recovered[0].Started || recovered[0].Invocations != 1 {
		t.Fatalf("recovery = %+v", recovered)
	}
	source, err := st.GetNode(ctx, claimed.RunID, claimed.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	if source.FailureReason != store.FailureAgentLost || len(source.ExecutionAttempts) != 1 ||
		source.ExecutionAttempts[0].FinishedAt == nil || source.ExecutionAttempts[0].FailureReason != store.FailureAgentLost ||
		source.ExecutionAttempts[0].RetryRunID != recovered[0].RetryRunID {
		t.Fatalf("source attempt = %+v", source)
	}
	retry, err := st.GetRun(ctx, recovered[0].RetryRunID)
	if err != nil {
		t.Fatal(err)
	}
	if retry.Status != "pending" || retry.RetryOf != claimed.RunID || retry.RetryAvailableAt == nil || retry.RetryDeadlineAt == nil {
		t.Fatalf("retry = %+v", retry)
	}
}

func TestPostgresClaimNextReadyNode_Concurrent(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()

	if err := st.CreateRun(ctx, store.Run{
		ID: "r1", Pipeline: "p", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := st.CreateNode(ctx, store.Node{
		RunID: "r1", NodeID: "n1", Status: "ready",
	}); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	if err := st.MarkNodeReady(ctx, "r1", "n1"); err != nil {
		t.Fatalf("MarkNodeReady: %v", err)
	}

	var winners int
	var losers int
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			n, err := st.ClaimNextReadyNode(ctx, store.ClaimIdentity{Principal: "runner-principal", TokenPrefix: "swr_runner-principal"}, fmt.Sprintf("h-%d", id), time.Minute, nil)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil && n != nil:
				winners++
			case errors.Is(err, store.ErrNotFound):
				losers++
			default:
				t.Errorf("unexpected: n=%v err=%v", n, err)
			}
		}(i)
	}
	wg.Wait()

	if winners != 1 {
		t.Errorf("winners = %d, want 1", winners)
	}
	if losers != 3 {
		t.Errorf("losers = %d, want 3", losers)
	}
}

func TestPostgresAcquireConcurrencySlot_Serializes(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()

	const key = "shared-slot"
	const n = 5

	for i := 0; i < n; i++ {
		runID := fmt.Sprintf("r-%d", i)
		if err := st.CreateRun(ctx, store.Run{
			ID: runID, Pipeline: "p", Status: "running", StartedAt: time.Now(),
		}); err != nil {
			t.Fatalf("CreateRun %s: %v", runID, err)
		}
	}

	type result struct {
		kind store.AcquireKind
		err  error
	}
	results := make([]result, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			resp, err := st.AcquireConcurrencySlot(ctx, store.AcquireSlotRequest{
				Key:      key,
				RunID:    fmt.Sprintf("r-%d", i),
				NodeID:   "n",
				HolderID: fmt.Sprintf("h-%d", i),
				Capacity: 1,
				Policy:   store.OnLimitQueue,
				Lease:    time.Minute,
			})
			results[i] = result{kind: resp.Kind, err: err}
		}(i)
	}
	wg.Wait()

	var granted, queued int
	for _, r := range results {
		if r.err != nil {
			t.Errorf("unexpected error: %v", r.err)
			continue
		}
		switch r.kind {
		case store.AcquireGranted:
			granted++
		case store.AcquireQueued:
			queued++
		default:
			t.Errorf("unexpected acquire kind: %v", r.kind)
		}
	}
	if granted != 1 {
		t.Errorf("granted = %d, want 1", granted)
	}
	if queued != n-1 {
		t.Errorf("queued = %d, want %d", queued, n-1)
	}
}

func TestPostgresResolveWaiterPromotesOnceUnderConcurrentPoll(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()

	acquireT(t, st, store.AcquireSlotRequest{
		Key: "resolve-slot", HolderID: "leader", RunID: "leader", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitQueue,
	})
	for i := 0; i < 2; i++ {
		acquireT(t, st, store.AcquireSlotRequest{
			Key: "resolve-slot", HolderID: fmt.Sprintf("w-%d", i), RunID: fmt.Sprintf("waiter-%d", i), NodeID: "n",
			Capacity: 1, Policy: store.OnLimitQueue,
		})
	}
	if _, err := st.DB().ExecContext(ctx,
		`DELETE FROM concurrency_holders WHERE key = $1 AND holder_id = $2`,
		"resolve-slot", "leader"); err != nil {
		t.Fatalf("manual drop: %v", err)
	}

	type result struct {
		resolution store.WaiterResolution
		err        error
	}
	results := make([]result, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			resolution, err := st.ResolveWaiter(ctx, "resolve-slot", fmt.Sprintf("waiter-%d", i), "n", "", "", "", false)
			results[i] = result{resolution: resolution, err: err}
		}(i)
	}
	wg.Wait()

	var promoted, waiting int
	for _, result := range results {
		if result.err != nil {
			t.Fatalf("resolve error: %v", result.err)
		}
		switch result.resolution.Status {
		case store.WaiterPromoted:
			promoted++
		case store.WaiterStillWaiting:
			waiting++
		default:
			t.Fatalf("unexpected resolution: %+v", result.resolution)
		}
	}
	if promoted != 1 || waiting != 1 {
		t.Fatalf("promoted=%d waiting=%d, want promoted=1 waiting=1", promoted, waiting)
	}
	state, err := st.GetConcurrencyState(ctx, "resolve-slot")
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if len(state.Holders) != 1 || state.Holders[0].RunID != "waiter-0" {
		t.Fatalf("holders = %+v", state.Holders)
	}
	if len(state.Waiters) != 1 || state.Waiters[0].RunID != "waiter-1" {
		t.Fatalf("waiters = %+v", state.Waiters)
	}
}
