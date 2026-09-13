package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

// A database another lineage stamped at 35 takes this migration on its own,
// because v36 adds every placement column rather than assuming what 35 did.
func TestSchemaV36_UpgradeFromAStoreStampedAt35(t *testing.T) {
	target := storetest.New(t)
	seeded, err := target.TryOpen()
	if err != nil {
		t.Fatalf("Open#1: %v", err)
	}
	ctx := context.Background()
	if err := seeded.CreateRun(ctx, store.Run{
		ID: "r1", Pipeline: "legacy", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("seed run: %v", err)
	}
	if err := seeded.CreateNode(ctx, store.Node{
		RunID: "r1", NodeID: "n1", Status: "pending", PrefersLabels: []string{"location=local"},
	}); err != nil {
		t.Fatalf("seed node: %v", err)
	}
	for _, stmt := range []string{
		`ALTER TABLE nodes DROP COLUMN placement_reason`,
		`ALTER TABLE nodes DROP COLUMN placement_hold_from`,
	} {
		if _, err := seeded.DB().Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if _, err := seeded.DB().Exec(`DELETE FROM sparkwing_schema_version WHERE version >= 36`); err != nil {
		t.Fatalf("reset version to 35: %v", err)
	}
	if v := readSchemaVersion(t, seeded.DB()); v != 35 {
		t.Fatalf("seeded version = %d, want 35", v)
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
	if err := upgraded.MarkNodeReady(ctx, "r1", "n1"); err != nil {
		t.Fatalf("MarkNodeReady after upgrade: %v", err)
	}
	n, err := upgraded.ClaimNextReadyNode(ctx, store.ClaimIdentity{Principal: "p", TokenPrefix: "swr_p"},
		"runner:laptop:1", time.Minute, []string{"location=local"})
	if err != nil {
		t.Fatalf("claim after upgrade: %v", err)
	}
	if n.PlacementReason != store.PlacementPreferred {
		t.Fatalf("placement reason = %q, want %q", n.PlacementReason, store.PlacementPreferred)
	}
	if n.PlacementHoldFrom == nil {
		t.Fatal("the upgraded store stamped no hold-from time")
	}
}
