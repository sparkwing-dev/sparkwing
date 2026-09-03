package store_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

var orderedNodeIDs = []string{"zulu", "yankee", "xray", "whiskey", "victor"}

func seedOrderedNodes(t *testing.T, st *store.Store) {
	t.Helper()
	ctx := context.Background()
	if err := st.CreateRun(ctx, store.Run{
		ID: "run-1", Pipeline: "demo", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	for _, id := range orderedNodeIDs {
		if err := st.CreateNode(ctx, store.Node{RunID: "run-1", NodeID: id, Status: "pending"}); err != nil {
			t.Fatalf("CreateNode(%s): %v", id, err)
		}
	}
	for i := len(orderedNodeIDs) - 1; i >= 0; i-- {
		if err := st.StartNode(ctx, "run-1", orderedNodeIDs[i]); err != nil {
			t.Fatalf("StartNode(%s): %v", orderedNodeIDs[i], err)
		}
		if err := st.TouchNodeHeartbeat(ctx, "run-1", orderedNodeIDs[i]); err != nil {
			t.Fatalf("TouchNodeHeartbeat(%s): %v", orderedNodeIDs[i], err)
		}
	}
}

func checkNodeOrder(t *testing.T, st *store.Store) {
	t.Helper()
	nodes, err := st.ListNodes(context.Background(), "run-1")
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
			t.Fatalf("ListNodes order = %v, want %v", got, orderedNodeIDs)
		}
	}
}

// TestListNodesKeepsInsertionOrderAcrossUpdates is the SQLite half of the
// same promise, where rowid ordering already held it.
func TestListNodesKeepsInsertionOrderAcrossUpdates(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "nodes.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	seedOrderedNodes(t, st)
	checkNodeOrder(t, st)
}

// TestPostgresListNodesKeepsInsertionOrderAcrossUpdates pins the promise
// ListNodes makes. It used to implement it on Postgres as ORDER BY ctid, the
// physical tuple location, which every update moves; the ids run backwards
// through the alphabet and the rows are rewritten in reverse creation order,
// so neither the heap layout nor an alphabetical fallback would answer with
// the order the nodes were created in.
func TestPostgresListNodesKeepsInsertionOrderAcrossUpdates(t *testing.T) {
	st := openPGTestStore(t)
	seedOrderedNodes(t, st)
	checkNodeOrder(t, st)
}
