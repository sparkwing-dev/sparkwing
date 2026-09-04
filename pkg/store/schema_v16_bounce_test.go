package store_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestSchemaV16_UpgradeOfARealV15ShapeAddsTheBounceTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "real15.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open#1: %v", err)
	}
	ctx := context.Background()
	if err := st.CreateRun(ctx, store.Run{ID: "r1", Pipeline: "legacy", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatalf("seed run: %v", err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "r1", NodeID: "build", Status: "running"}); err != nil {
		t.Fatalf("seed node: %v", err)
	}
	if _, err := st.DB().Exec(`DROP TABLE node_bounces`); err != nil {
		t.Fatalf("drop node_bounces: %v", err)
	}
	if _, err := st.DB().Exec(`DELETE FROM sparkwing_schema_version WHERE version >= 16`); err != nil {
		t.Fatalf("reset version to 15: %v", err)
	}
	deleteFleetRequirements(t, st.DB())
	if v := readSchemaVersion(t, st.DB()); v != 15 {
		t.Fatalf("seeded version = %d, want 15", v)
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
	if _, err := up.RequestNodeBounce(ctx, "r1", "build", "operator"); err != nil {
		t.Fatalf("RequestNodeBounce after upgrade: %v", err)
	}
	n, err := up.GetNode(ctx, "r1", "build")
	if err != nil || n.Status != "running" {
		t.Errorf("carried node = %+v (%v), want it untouched by the upgrade", n, err)
	}
}

func TestSchemaV16_UpgradeIsSafeToReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "replay16.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open#1: %v", err)
	}
	ctx := context.Background()
	if err := st.CreateRun(ctx, store.Run{ID: "r1", Pipeline: "demo", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatalf("seed run: %v", err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "r1", NodeID: "build", Status: "running"}); err != nil {
		t.Fatalf("seed node: %v", err)
	}
	if _, err := st.RequestNodeBounce(ctx, "r1", "build", "operator"); err != nil {
		t.Fatalf("RequestNodeBounce: %v", err)
	}
	if _, err := st.DB().Exec(`DELETE FROM sparkwing_schema_version WHERE version >= 16`); err != nil {
		t.Fatalf("rewind version stamp: %v", err)
	}
	deleteFleetRequirements(t, st.DB())
	_ = st.Close()

	up, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open#2 (replay): %v", err)
	}
	defer func() { _ = up.Close() }()

	b, err := up.PendingNodeBounce(ctx, "r1", "build")
	if err != nil {
		t.Fatalf("PendingNodeBounce: %v", err)
	}
	if b == nil || b.Seq != 1 {
		t.Errorf("pending bounce after replay = %+v, want the request preserved", b)
	}
}
