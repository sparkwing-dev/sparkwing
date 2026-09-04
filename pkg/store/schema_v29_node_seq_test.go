package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/storetest"
)

func seedV28Nodes(t *testing.T, target *storetest.Target) {
	t.Helper()
	st, err := target.TryOpen()
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
	if _, err := st.DB().Exec(`DELETE FROM sparkwing_schema_version WHERE version >= 29`); err != nil {
		t.Fatalf("reset version to 28: %v", err)
	}
	deleteFleetRequirements(t, st.DB())
	if v := readSchemaVersion(t, st.DB()); v != 28 {
		t.Fatalf("seeded version = %d, want 28", v)
	}
	_ = st.Close()
}

// TestSchemaV29_UpgradeOfARealV28ShapeKeepsSQLiteInsertionOrder upgrades a
// file shaped the way a v28 binary left it and checks the backfill kept the
// order that store returned.
func TestSchemaV29_UpgradeOfARealV28ShapeKeepsSQLiteInsertionOrder(t *testing.T) {
	target := storetest.NewSQLite(t)
	seedV28Nodes(t, target)

	up, err := target.TryOpen()
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
			t.Fatalf("order after backfill = %v, want the order the v28 store wrote them, %v",
				got, orderedNodeIDs)
		}
	}
}

// TestSchemaV29_NodeCreatedAfterUpgradeSortsLast pins that a node created
// after the upgrade sorts after the backfilled ones, which holds only while
// the backfilled values and the newly assigned ones share one scale.
func TestSchemaV29_NodeCreatedAfterUpgradeSortsLast(t *testing.T) {
	target := storetest.New(t)
	seedV28Nodes(t, target)

	up, err := target.TryOpen()
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

func TestSchemaV29_UpgradeIsSafeToReplay(t *testing.T) {
	target := storetest.NewSQLite(t)
	seedV28Nodes(t, target)

	for i := 0; i < 2; i++ {
		replay, err := target.TryOpen()
		if err != nil {
			t.Fatalf("Open (upgrade pass %d): %v", i, err)
		}
		_ = replay.Close()
	}
	st, err := target.TryOpen()
	if err != nil {
		t.Fatalf("Open (final): %v", err)
	}
	defer func() { _ = st.Close() }()
	if _, err := st.DB().Exec(`DELETE FROM sparkwing_schema_version WHERE version >= 29`); err != nil {
		t.Fatalf("reset version: %v", err)
	}
	deleteFleetRequirements(t, st.DB())
	_ = st.Close()

	replayed, err := target.TryOpen()
	if err != nil {
		t.Fatalf("Open (replay of v29): %v", err)
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

// TestSchemaV29_ReplayLeavesTheV28CascadeIntact guards the seam the two
// migrations share. v29 rebuilds nothing v28 built, so a store that has
// replayed it must still drop a deleted run's metric rows -- the invariant
// v28 exists for.
func TestSchemaV29_ReplayLeavesTheV28CascadeIntact(t *testing.T) {
	target := storetest.New(t)
	seedV28Nodes(t, target)

	upgraded, err := target.TryOpen()
	if err != nil {
		t.Fatalf("Open (upgrade): %v", err)
	}
	if _, err := upgraded.DB().Exec(`DELETE FROM sparkwing_schema_version WHERE version >= 29`); err != nil {
		t.Fatalf("reset version: %v", err)
	}
	deleteFleetRequirements(t, upgraded.DB())
	_ = upgraded.Close()

	replayed, err := target.TryOpen()
	if err != nil {
		t.Fatalf("Open (replay of v29): %v", err)
	}
	defer func() { _ = replayed.Close() }()

	ctx := context.Background()
	seedRunWithMetricSample(t, replayed, "doomed", time.Now())
	if got := countNodeMetrics(t, replayed.DB()); got != 1 {
		t.Fatalf("seeded node_metrics = %d, want 1", got)
	}
	if err := replayed.DeleteRun(ctx, "doomed"); err != nil {
		t.Fatalf("DeleteRun: %v", err)
	}
	if got := countNodeMetrics(t, replayed.DB()); got != 0 {
		t.Errorf("node_metrics after DeleteRun = %d, want 0: replaying v29 lost v28's cascade", got)
	}
}
