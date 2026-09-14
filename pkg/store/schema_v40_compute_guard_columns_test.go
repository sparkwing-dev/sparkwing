package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

// TestSchemaV40_UpgradeFromAStoreMissingTheIndexedColumns upgrades a database that
// reached the compute-guard step without the columns that step indexes. Such a database
// takes each column rather than refusing to open.
func TestSchemaV40_UpgradeFromAStoreMissingTheIndexedColumns(t *testing.T) {
	target := storetest.New(t)
	seeded, err := target.TryOpen()
	if err != nil {
		t.Fatalf("Open#1: %v", err)
	}
	ctx := context.Background()
	if err := seeded.CreateRun(ctx, store.Run{
		ID: "r39", Pipeline: "legacy", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("seed run: %v", err)
	}
	if err := seeded.CreateNode(ctx, store.Node{RunID: "r39", NodeID: "n1", Status: "pending"}); err != nil {
		t.Fatalf("seed node: %v", err)
	}
	for _, stmt := range []string{
		`DROP INDEX IF EXISTS idx_nodes_credit_active`,
		`DROP INDEX IF EXISTS idx_nodes_credit_window`,
		`DROP INDEX IF EXISTS idx_nodes_credit_principal`,
		`DROP INDEX IF EXISTS idx_runs_created`,
		`DROP INDEX IF EXISTS idx_runs_principal_created`,
		`ALTER TABLE nodes DROP COLUMN claim_principal`,
		`ALTER TABLE nodes DROP COLUMN credit_charged_through`,
		`ALTER TABLE runs DROP COLUMN created_at`,
		`ALTER TABLE runs DROP COLUMN created_principal`,
		`DELETE FROM sparkwing_schema_version WHERE version >= 40`,
	} {
		if _, err := seeded.DB().Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if v := readSchemaVersion(t, seeded.DB()); v != 39 {
		t.Fatalf("seeded version = %d, want 39", v)
	}
	_ = seeded.Close()

	upgraded, err := target.TryOpen()
	if err != nil {
		t.Fatalf("a store missing the column the compute-guard step indexes refused to open: %v", err)
	}
	defer func() { _ = upgraded.Close() }()

	if v := readSchemaVersion(t, upgraded.DB()); v != store.ExpectedSchemaVersion() {
		t.Fatalf("version after upgrade = %d, want %d", v, store.ExpectedSchemaVersion())
	}
	// safety: selecting the column is how both dialects answer whether it
	// exists; the row set stays empty so each query reads the schema alone.
	for _, probe := range []string{
		`SELECT claim_principal, credit_charged_through FROM nodes WHERE 1 = 0`,
		`SELECT created_at, created_principal FROM runs WHERE 1 = 0`,
	} {
		rows, err := upgraded.DB().Query(probe)
		if err != nil {
			t.Fatalf("the upgrade did not carry every column it indexes (%s): %v", probe, err)
		}
		if err := rows.Close(); err != nil {
			t.Fatalf("close the schema probe: %v", err)
		}
	}
	node, err := upgraded.GetNode(ctx, "r39", "n1")
	if err != nil {
		t.Fatalf("a node seeded before the upgrade is unreadable after it: %v", err)
	}
	if node.NodeID != "n1" {
		t.Fatalf("node after upgrade = %+v, want the seeded one", node)
	}
}
