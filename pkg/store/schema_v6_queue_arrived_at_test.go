package store_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// TestSchemaV6_UpgradeAddsQueueArrivedAtColumn reconstructs a schema-5
// runs store without the queue_arrived_at column, then opens it with the
// current binary and asserts the v6 migration makes concurrency state
// queries usable.
func TestSchemaV6_UpgradeAddsQueueArrivedAtColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schema5.db")

	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open#1: %v", err)
	}
	if _, err := st.DB().Exec(`ALTER TABLE concurrency_holders DROP COLUMN queue_arrived_at`); err != nil {
		t.Fatalf("drop queue_arrived_at: %v", err)
	}
	if _, err := st.DB().Exec(`DELETE FROM sparkwing_schema_version WHERE version = 6`); err != nil {
		t.Fatalf("reset version to 5: %v", err)
	}
	if v := readSchemaVersion(t, st.DB()); v != 5 {
		t.Fatalf("seeded version = %d, want 5", v)
	}
	if hasColumn(t, st, "concurrency_holders", "queue_arrived_at") {
		t.Fatal("queue_arrived_at should be absent before upgrade")
	}
	_ = st.Close()

	up, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open#2 (upgrade): %v", err)
	}
	defer func() { _ = up.Close() }()

	if v := readSchemaVersion(t, up.DB()); v != store.ExpectedSchemaVersion() {
		t.Errorf("version after upgrade = %d, want %d", v, store.ExpectedSchemaVersion())
	}
	if !hasColumn(t, up, "concurrency_holders", "queue_arrived_at") {
		t.Fatal("queue_arrived_at should be present after upgrade")
	}

	resp, err := up.AcquireConcurrencySlot(context.Background(), store.AcquireSlotRequest{
		Key:      "box",
		HolderID: "run-1/node-1",
		RunID:    "run-1",
		NodeID:   "node-1",
		Capacity: 1,
		Policy:   store.OnLimitQueue,
		Lease:    time.Minute,
	})
	if err != nil {
		t.Fatalf("AcquireConcurrencySlot: %v", err)
	}
	if resp.Kind != store.AcquireGranted {
		t.Fatalf("AcquireConcurrencySlot kind = %s, want %s", resp.Kind, store.AcquireGranted)
	}
	if _, err := up.GetConcurrencyState(context.Background(), "box"); err != nil {
		t.Fatalf("GetConcurrencyState: %v", err)
	}
}

func hasColumn(t *testing.T, s *store.Store, table, column string) bool {
	t.Helper()
	rows, err := s.DB().Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		t.Fatalf("PRAGMA table_info(%s): %v", table, err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var cid, notnull, pk int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notnull, &defaultValue, &pk); err != nil {
			t.Fatalf("scan table_info(%s): %v", table, err)
		}
		if name == column {
			return true
		}
	}
	return false
}
