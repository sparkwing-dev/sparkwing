package store_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/executionpolicy"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestSchemaV31FreshSQLiteExecutionPolicyShape(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "fresh.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	assertExecutionPolicySchemaSQLite(t, st.DB())
}

func TestSchemaV31UpgradesRealV30SQLiteShape(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v30.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	downgradeExecutionPolicyToV30SQLite(t, st.DB())
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	up, err := store.Open(path)
	if err != nil {
		t.Fatalf("upgrade v30 to v31: %v", err)
	}
	defer up.Close()
	assertExecutionPolicySchemaSQLite(t, up.DB())
	if got := readSchemaVersion(t, up.DB()); got != store.ExpectedSchemaVersion() {
		t.Fatalf("schema version = %d, want %d", got, store.ExpectedSchemaVersion())
	}
}

func TestSchemaV31KeepsStoreAvailableWhenLegacyRetrySourceWasPruned(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v30-missing-source.db")
	ctx := context.Background()
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateRun(ctx, store.Run{ID: "source", Pipeline: "build", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "source", NodeID: "build", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateRun(ctx, store.Run{ID: "retry", Pipeline: "build", Status: "pending", RetryOf: "source"}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateTrigger(ctx, store.Trigger{ID: "retry", Pipeline: "build", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`INSERT INTO agent_loss_retries
		(run_id, source_run_id, root_run_id, cause_nodes_json, available_at, deadline_at, retry_count)
		VALUES ('retry', 'source', 'source', '["build"]', 1, 2, 1)`); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteRun(ctx, "source"); err != nil {
		t.Fatal(err)
	}
	downgradeExecutionPolicyToV30SQLite(t, st.DB())
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	up, err := store.Open(path)
	if err != nil {
		t.Fatalf("upgrade with pruned legacy retry source: %v", err)
	}
	defer up.Close()
	retry, err := up.GetRun(ctx, "retry")
	if err != nil {
		t.Fatal(err)
	}
	if retry.Status != "pending" {
		t.Fatalf("legacy retry status = %q, want pending and inspectable", retry.Status)
	}
	var markers int
	if err := up.DB().QueryRow(`SELECT COUNT(*) FROM agent_loss_retry_legacy_deny_all WHERE retry_run_id = 'retry' AND source_run_id = 'source'`).Scan(&markers); err != nil {
		t.Fatal(err)
	}
	if markers != 1 {
		t.Fatalf("legacy deny-all markers = %d, want 1", markers)
	}
	if err := up.CreateNode(ctx, store.Node{RunID: "retry", NodeID: "build", Status: "pending"}); !errors.Is(err, executionpolicy.ErrExecutionPolicyInvalid) {
		t.Fatalf("legacy retry without authority accepted work: %v", err)
	}
}

func downgradeExecutionPolicyToV30SQLite(t *testing.T, db *sql.DB) {
	t.Helper()
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
	for _, column := range []string{
		"supported_body_protocol_min", "supported_body_protocol_max", "supervisor_requirements_json",
		"body_runtime_requirements_json", "runner_build_identity_json",
	} {
		statements = append(statements, `ALTER TABLE executors DROP COLUMN `+column)
	}
	for _, column := range []string{
		"execution_policy_hash", "execution_policy_version", "execution_body_protocol",
		"execution_supervisor_requirements_hash", "execution_body_requirements_hash",
	} {
		statements = append(statements, `ALTER TABLE node_claim_offers DROP COLUMN `+column)
	}
	statements = append(statements,
		`DELETE FROM sparkwing_requirements WHERE name = 'assisted-execution-policy-v1'`,
		`DELETE FROM sparkwing_schema_version WHERE version >= 31`,
	)
	for _, statement := range statements {
		if _, err := db.ExecContext(context.Background(), statement); err != nil {
			t.Fatalf("downgrade v31 with %q: %v", statement, err)
		}
	}
}

func assertExecutionPolicySchemaSQLite(t *testing.T, db *sql.DB) {
	t.Helper()
	for table, columns := range map[string][]string{
		"nodes": {
			"execution_policy_json", "execution_policy_hash", "execution_policy_version", "execution_body_protocol",
			"execution_supervisor_requirements_json", "execution_supervisor_requirements_hash",
			"execution_body_requirements_json", "execution_body_requirements_hash",
		},
		"executors": {
			"supported_body_protocol_min", "supported_body_protocol_max", "supervisor_requirements_json",
			"body_runtime_requirements_json", "runner_build_identity_json",
		},
		"node_claim_offers": {
			"execution_policy_hash", "execution_policy_version", "execution_body_protocol",
			"execution_supervisor_requirements_hash", "execution_body_requirements_hash",
		},
		"agent_loss_retry_node_sources": {
			"retry_run_id", "source_run_id", "node_id", "deps_json", "needs_labels_json", "prefers_labels_json",
			"requested_cores", "requested_memory_bytes", "requested_slots", "attempts_consumed",
			"required_coordinator_id", "required_executor_location", "policy_json", "policy_hash",
			"avoid_coordinator_id", "avoid_executor_kind", "avoid_executor_id", "avoid_until",
			"policy_version", "body_protocol", "supervisor_requirements_json", "supervisor_requirements_hash",
			"body_requirements_json", "body_requirements_hash",
		},
		"agent_loss_retry_legacy_deny_all": {"retry_run_id", "source_run_id", "reason"},
	} {
		for _, column := range columns {
			var count int
			if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, table, column).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 1 {
				t.Errorf("missing %s.%s", table, column)
			}
		}
	}
	if got := countIndexesNamed(t, db, "idx_nodes_assisted_claimable"); got != 1 {
		t.Errorf("assisted claimable index count = %d, want 1", got)
	}
	var exactFK int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_list('agent_loss_retry_node_sources')
		WHERE "from" = 'retry_run_id' AND "table" = 'runs' AND "to" = 'id' AND UPPER(on_delete) = 'CASCADE'`).Scan(&exactFK); err != nil {
		t.Fatal(err)
	}
	if exactFK != 1 {
		t.Errorf("retry snapshot FK count = %d, want exact retry_run_id cascade", exactFK)
	}
	var sourceFK int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_list('agent_loss_retry_node_sources') WHERE "from" = 'source_run_id'`).Scan(&sourceFK); err != nil {
		t.Fatal(err)
	}
	if sourceFK != 0 {
		t.Errorf("source_run_id has %d FK(s), retry snapshot would not survive source pruning", sourceFK)
	}
	if names := requirementNames(t, db); !containsRequirement(names, "assisted-execution-policy-v1") {
		t.Errorf("requirements omit assisted-execution-policy-v1: %v", names)
	}
}

func containsRequirement(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
