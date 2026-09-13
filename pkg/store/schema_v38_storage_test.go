package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

// The storage tables arrive on a database an earlier binary stamped at 37 and
// keeps writing, because v38 adds tables nothing older reads and no column.
func TestSchemaV38_UpgradeFromAStoreStampedAt37(t *testing.T) {
	target := storetest.New(t)
	seeded, err := target.TryOpen()
	if err != nil {
		t.Fatalf("Open#1: %v", err)
	}
	ctx := context.Background()
	if err := seeded.CreateRun(ctx, store.Run{
		ID: "r1", Pipeline: "legacy", Status: "running", StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed run: %v", err)
	}
	for _, stmt := range []string{
		`DROP TABLE storage_run_usage`,
		`DROP TABLE storage_month_usage`,
		`DROP TABLE storage_quotas`,
		`DROP INDEX idx_events_ts`,
		`DROP INDEX idx_node_metrics_ts`,
	} {
		if _, err := seeded.DB().Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if _, err := seeded.DB().Exec(`DELETE FROM sparkwing_schema_version WHERE version >= 38`); err != nil {
		t.Fatalf("reset version to 37: %v", err)
	}
	if v := readSchemaVersion(t, seeded.DB()); v != 37 {
		t.Fatalf("seeded version = %d, want 37", v)
	}
	if err := seeded.Close(); err != nil {
		t.Fatalf("close the seeded store: %v", err)
	}

	upgraded, err := target.TryOpen()
	if err != nil {
		t.Fatalf("Open#2 (upgrade): %v", err)
	}
	defer func() { _ = upgraded.Close() }()
	if v := readSchemaVersion(t, upgraded.DB()); v != store.ExpectedSchemaVersion() {
		t.Fatalf("version after upgrade = %d, want %d", v, store.ExpectedSchemaVersion())
	}
	if err := upgraded.SetStorageQuota(ctx, store.StorageQuota{
		Principal: "alice", Tier: store.StorageTierFree,
	}); err != nil {
		t.Fatalf("write a quota after the upgrade: %v", err)
	}
	if err := upgraded.CreateNode(ctx, store.Node{RunID: "r1", NodeID: "n1", Status: "pending"}); err != nil {
		t.Fatalf("create a node after the upgrade: %v", err)
	}
	if _, err := upgraded.AppendEventCharged(ctx, "alice", "r1", "n1", "note", make([]byte, 32)); err != nil {
		t.Fatalf("charge after the upgrade: %v", err)
	}
	month := store.StorageMonth(time.Now().UTC())
	usage, err := upgraded.StorageUsageFor(ctx, "alice", "r1", month)
	if err != nil {
		t.Fatalf("usage after the upgrade: %v", err)
	}
	if usage.RunBytes != 32 || usage.MonthBytes != 32 {
		t.Fatalf("usage = %+v, want the charged 32 bytes", usage)
	}
	if _, err := upgraded.GetRun(ctx, "r1"); err != nil {
		t.Fatalf("the run seeded before the upgrade: %v", err)
	}
}
