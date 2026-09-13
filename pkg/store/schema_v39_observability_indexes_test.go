package store_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

// A database stamped a version short of the indexes takes them on its own, and
// the rows it already holds stay readable because the migration adds nothing
// but indexes.
func TestSchemaV39_UpgradeFromAStoreStampedShortOfTheIndexes(t *testing.T) {
	target := storetest.New(t)
	seeded, err := target.TryOpen()
	if err != nil {
		t.Fatalf("Open#1: %v", err)
	}
	ctx := context.Background()
	if err := seeded.CreateRun(ctx, store.Run{
		ID: "r38", Pipeline: "legacy", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("seed run: %v", err)
	}
	if err := seeded.CreateNode(ctx, store.Node{RunID: "r38", NodeID: "n1", Status: "pending"}); err != nil {
		t.Fatalf("seed node: %v", err)
	}
	if _, err := seeded.GrantCredits(ctx, store.CreditGrantPaid, 1_000_000, "invoice", "admin"); err != nil {
		t.Fatalf("seed grant: %v", err)
	}
	for _, stmt := range []string{
		`DROP INDEX IF EXISTS idx_nodes_outstanding`,
		`DROP INDEX IF EXISTS idx_credit_grants_kind_amount`,
		`DROP INDEX IF EXISTS idx_credit_charges_kind_amount`,
	} {
		if _, err := seeded.DB().Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if _, err := seeded.DB().Exec(`DELETE FROM sparkwing_schema_version WHERE version >= 39`); err != nil {
		t.Fatalf("rewind the stamp past the indexes: %v", err)
	}
	if v := readSchemaVersion(t, seeded.DB()); v != 38 {
		t.Fatalf("seeded version = %d, want %d", v, 38)
	}
	_ = seeded.Close()

	upgraded, err := target.TryOpen()
	if err != nil {
		t.Fatalf("Open#2 (upgrade): %v", err)
	}
	defer func() { _ = upgraded.Close() }()
	if v := readSchemaVersion(t, upgraded.DB()); v != store.ExpectedSchemaVersion() {
		t.Fatalf("version after upgrade = %d, want %d", v, store.ExpectedSchemaVersion())
	}

	counts, err := upgraded.CountNodesByQueueState(ctx)
	if err != nil {
		t.Fatalf("CountNodesByQueueState after upgrade: %v", err)
	}
	if counts[store.QueueStateWaiting] != 1 {
		t.Errorf("waiting = %d after upgrade, want the seeded node", counts[store.QueueStateWaiting])
	}
	totals, err := upgraded.CreditLedgerTotals(ctx)
	if err != nil {
		t.Fatalf("CreditLedgerTotals after upgrade: %v", err)
	}
	if totals.GrantedPaidMicro != 1_000_000 {
		t.Errorf("paid grants = %d after upgrade, want the seeded grant", totals.GrantedPaidMicro)
	}
	assertIndexUsed(t, upgraded.DB())
}

// safety: the point of the migration is that the queue sweep stops reading the
// table, so the plan is what proves it rather than the row counts.
func assertIndexUsed(t *testing.T, db *sql.DB) {
	t.Helper()
	rows, err := db.Query(`EXPLAIN QUERY PLAN SELECT COUNT(*) FROM nodes WHERE status != 'done'`)
	if err != nil {
		t.Skipf("this dialect does not answer EXPLAIN QUERY PLAN: %v", err)
		return
	}
	defer func() { _ = rows.Close() }()
	plan := ""
	for rows.Next() {
		cols, cerr := rows.Columns()
		if cerr != nil {
			t.Fatalf("plan columns: %v", cerr)
		}
		cells := make([]any, len(cols))
		for i := range cells {
			cells[i] = new(sql.RawBytes)
		}
		if err := rows.Scan(cells...); err != nil {
			t.Fatalf("scan plan: %v", err)
		}
		for _, c := range cells {
			plan += string(*(c.(*sql.RawBytes))) + " "
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("plan rows: %v", err)
	}
	if !strings.Contains(plan, "idx_nodes_outstanding") {
		t.Errorf("the outstanding-node sweep plan does not use the index: %s", plan)
	}
}
