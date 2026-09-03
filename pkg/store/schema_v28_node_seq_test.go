package store_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func seedV27Nodes(t *testing.T, path string) {
	t.Helper()
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open#1: %v", err)
	}
	ctx := context.Background()
	if err := st.CreateRun(ctx, store.Run{
		ID: "r1", Pipeline: "legacy", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("seed run: %v", err)
	}
	for _, id := range orderedNodeIDs {
		if err := st.CreateNode(ctx, store.Node{RunID: "r1", NodeID: id, Status: "pending"}); err != nil {
			t.Fatalf("seed node %s: %v", id, err)
		}
	}
	if _, err := st.DB().Exec(`ALTER TABLE nodes DROP COLUMN seq`); err != nil {
		t.Fatalf("drop seq column: %v", err)
	}
	if _, err := st.DB().Exec(`DELETE FROM sparkwing_schema_version WHERE version >= 28`); err != nil {
		t.Fatalf("reset version to 27: %v", err)
	}
	if v := readSchemaVersion(t, st.DB()); v != 27 {
		t.Fatalf("seeded version = %d, want 27", v)
	}
	_ = st.Close()
}

// TestSchemaV28_UpgradeOfARealV27ShapeKeepsSQLiteInsertionOrder upgrades a
// file shaped the way a v27 binary left it and checks the backfill kept the
// order that store returned.
func TestSchemaV28_UpgradeOfARealV27ShapeKeepsSQLiteInsertionOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "real27.db")
	seedV27Nodes(t, path)

	up, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open#2 (upgrade): %v", err)
	}
	defer func() { _ = up.Close() }()

	if v := readSchemaVersion(t, up.DB()); v != store.ExpectedSchemaVersion() {
		t.Errorf("version after upgrade = %d, want %d", v, store.ExpectedSchemaVersion())
	}
	nodes, err := up.ListNodes(context.Background(), "r1")
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	if len(nodes) != len(orderedNodeIDs) {
		t.Fatalf("nodes = %d, want %d", len(nodes), len(orderedNodeIDs))
	}
	for i, n := range nodes {
		if n.NodeID != orderedNodeIDs[i] {
			got := make([]string, len(nodes))
			for j, x := range nodes {
				got[j] = x.NodeID
			}
			t.Fatalf("order after backfill = %v, want the order the v27 store wrote them, %v",
				got, orderedNodeIDs)
		}
	}
}

// TestSchemaV28_NodeCreatedAfterUpgradeSortsLast pins that a node created
// after the upgrade sorts after the backfilled ones, which holds only while
// the backfilled values and the newly assigned ones share one scale.
func TestSchemaV28_NodeCreatedAfterUpgradeSortsLast(t *testing.T) {
	path := filepath.Join(t.TempDir(), "append27.db")
	seedV27Nodes(t, path)

	up, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open#2 (upgrade): %v", err)
	}
	defer func() { _ = up.Close() }()

	ctx := context.Background()
	if err := up.CreateNode(ctx, store.Node{RunID: "r1", NodeID: "alpha", Status: "pending"}); err != nil {
		t.Fatalf("CreateNode after upgrade: %v", err)
	}
	nodes, err := up.ListNodes(ctx, "r1")
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	if last := nodes[len(nodes)-1].NodeID; last != "alpha" {
		t.Errorf("last node = %s, want alpha", last)
	}
}

func TestSchemaV28_UpgradeIsSafeToReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "replay28.db")
	seedV27Nodes(t, path)

	for i := 0; i < 2; i++ {
		if _, err := store.Open(path); err != nil {
			t.Fatalf("Open (upgrade pass %d): %v", i, err)
		}
	}
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open (final): %v", err)
	}
	defer func() { _ = st.Close() }()
	if _, err := st.DB().Exec(`DELETE FROM sparkwing_schema_version WHERE version >= 28`); err != nil {
		t.Fatalf("reset version: %v", err)
	}
	_ = st.Close()

	replayed, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open (replay of v28): %v", err)
	}
	defer func() { _ = replayed.Close() }()
	nodes, err := replayed.ListNodes(context.Background(), "r1")
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	for i, n := range nodes {
		if n.NodeID != orderedNodeIDs[i] {
			t.Fatalf("order after replay = %s at %d, want %s", n.NodeID, i, orderedNodeIDs[i])
		}
	}
}
