package store_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

var boundExecutionTokenColumns = []string{
	"execution_run_id",
	"execution_root_node_id",
	"delegated_principal",
	"delegated_token_prefix",
	"execution_holder_id",
	"execution_claim_generation",
}

func downgradeBoundExecutionToV47(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`DROP INDEX IF EXISTS idx_nodes_credit_quota_principal`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`ALTER TABLE nodes DROP COLUMN claim_quota_principal`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`ALTER TABLE triggers DROP COLUMN requested_output_node_id`); err != nil {
		t.Fatal(err)
	}
	for _, column := range boundExecutionTokenColumns {
		if _, err := db.Exec(`ALTER TABLE tokens DROP COLUMN ` + column); err != nil {
			t.Fatalf("drop tokens.%s: %v", column, err)
		}
	}
	if _, err := db.Exec(`DELETE FROM sparkwing_requirements WHERE name = 'bound-execution-credentials'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM sparkwing_schema_version WHERE version >= 48`); err != nil {
		t.Fatal(err)
	}
}

func seedActiveLegacyClaim(t *testing.T, st *store.Store, bind string) {
	t.Helper()
	ctx := store.WithCreatingPrincipal(context.Background(), "tenant-a")
	if err := st.CreateRun(ctx, store.Run{ID: "legacy-run", Pipeline: "demo", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "legacy-run", NodeID: "build", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`UPDATE nodes
		SET claim_principal = 'shared-pool', credit_charged_through = `+bind+`
		WHERE run_id = 'legacy-run' AND node_id = 'build'`, time.Now().Add(time.Minute).UnixNano()); err != nil {
		t.Fatal(err)
	}
}

func assertBoundExecutionV48(t *testing.T, st *store.Store) {
	t.Helper()
	var quotaPrincipal string
	if err := st.DB().QueryRow(`SELECT claim_quota_principal FROM nodes
		WHERE run_id = 'legacy-run' AND node_id = 'build'`).Scan(&quotaPrincipal); err != nil {
		t.Fatal(err)
	}
	if quotaPrincipal != "tenant-a" {
		t.Fatalf("claim quota principal = %q, want tenant-a", quotaPrincipal)
	}
	ctx := context.Background()
	if err := st.CreateTrigger(ctx, store.Trigger{
		ID: "child", Pipeline: "demo", Status: "pending", CreatedAt: time.Now(),
		ParentRunID: "legacy-run", ParentNodeID: "build", RequestedOutputNodeID: "artifact",
	}); err != nil {
		t.Fatal(err)
	}
	trigger, err := st.GetTrigger(ctx, "child")
	if err != nil {
		t.Fatal(err)
	}
	if trigger.RequestedOutputNodeID != "artifact" {
		t.Fatalf("requested output = %q, want artifact", trigger.RequestedOutputNodeID)
	}
}

func TestSchemaV48UpgradesBoundExecutionSQLite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v47.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	seedActiveLegacyClaim(t, st, "?")
	downgradeBoundExecutionToV47(t, st.DB())
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	up, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = up.Close() }()
	assertBoundExecutionV48(t, up)
}

func TestSchemaV48UpgradesBoundExecutionPostgres(t *testing.T) {
	dsn := pgTestSchemaDSN(t)
	ctx := context.Background()
	st, err := store.OpenPostgres(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	seedActiveLegacyClaim(t, st, "$1")
	downgradeBoundExecutionToV47(t, st.DB())
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	up, err := store.OpenPostgres(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = up.Close() }()
	assertBoundExecutionV48(t, up)
}
