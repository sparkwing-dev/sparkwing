package store_test

import (
	"context"

	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func seedRunAndNode(t *testing.T, s *store.Store, runID, nodeID string) {
	t.Helper()
	ctx := context.Background()
	if err := s.CreateRun(ctx, store.Run{
		ID:        runID,
		Pipeline:  "demo",
		Status:    "running",
		StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := s.CreateNode(ctx, store.Node{
		RunID:  runID,
		NodeID: nodeID,
		Status: "pending",
	}); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
}

func setNodeReadyAt(t *testing.T, s *store.Store, runID, nodeID string, at time.Time) {
	t.Helper()
	res, err := s.DB().Exec(storetest.Rebind(s,
		`UPDATE nodes SET ready_at = ? WHERE run_id = ? AND node_id = ?`),
		at.UnixNano(), runID, nodeID,
	)
	if err != nil {
		t.Fatalf("set ready_at: %v", err)
	}
	changed, err := res.RowsAffected()
	if err != nil {
		t.Fatalf("count ready_at rows: %v", err)
	}
	if changed != 1 {
		t.Fatalf("ready_at rows = %d, want 1", changed)
	}
}

func expireNodeClaim(t *testing.T, s *store.Store, runID, nodeID string) {
	t.Helper()
	res, err := s.DB().Exec(storetest.Rebind(s,
		`UPDATE nodes SET lease_expires_at = ? WHERE run_id = ? AND node_id = ? AND claimed_by IS NOT NULL`),
		time.Now().Add(-time.Second).UnixNano(), runID, nodeID,
	)
	if err != nil {
		t.Fatalf("expire node claim: %v", err)
	}
	changed, err := res.RowsAffected()
	if err != nil {
		t.Fatalf("count expired node claims: %v", err)
	}
	if changed != 1 {
		t.Fatalf("expired node claims = %d, want 1", changed)
	}
}

func TestNodeClaim_MarkReadyIsIdempotent(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	seedRunAndNode(t, s, "run-1", "node-a")

	if err := s.MarkNodeReady(ctx, "run-1", "node-a"); err != nil {
		t.Fatalf("MarkNodeReady: %v", err)
	}
	n1, err := s.GetNode(ctx, "run-1", "node-a")
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if n1.ReadyAt == nil {
		t.Fatal("ready_at not set after MarkNodeReady")
	}
	first := time.Unix(123, 456)
	setNodeReadyAt(t, s, "run-1", "node-a", first)

	if err := s.MarkNodeReady(ctx, "run-1", "node-a"); err != nil {
		t.Fatalf("MarkNodeReady 2: %v", err)
	}
	n2, _ := s.GetNode(ctx, "run-1", "node-a")
	if !n2.ReadyAt.Equal(first) {
		t.Fatalf("ready_at changed on 2nd MarkNodeReady: %v -> %v", first, *n2.ReadyAt)
	}
}

func seedNodeWithLabels(t *testing.T, s *store.Store, runID, nodeID string, labels []string) {
	t.Helper()
	ctx := context.Background()
	if err := s.CreateRun(ctx, store.Run{
		ID: runID, Pipeline: "demo", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := s.CreateNode(ctx, store.Node{
		RunID: runID, NodeID: nodeID, Status: "pending", NeedsLabels: labels,
	}); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	if err := s.MarkNodeReady(ctx, runID, nodeID); err != nil {
		t.Fatalf("MarkNodeReady: %v", err)
	}
}
