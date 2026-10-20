package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func seedRunAndNode(t *testing.T, s *store.Store, runID, nodeID string) {
	t.Helper()
	ctx := context.Background()
	if err := teststore.ForTeam(t, s, store.DefaultTeam).TestOnlySeedRun(ctx, store.Run{
		ID:        runID,
		Pipeline:  "demo",
		Status:    "running",
		StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := teststore.SeedNode(s, ctx, store.Node{
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

func seedNodeWithLabels(t *testing.T, s *store.Store, runID, nodeID string, labels []string) {
	t.Helper()
	ctx := context.Background()
	if err := teststore.ForTeam(t, s, store.DefaultTeam).TestOnlySeedRun(ctx, store.Run{
		ID: runID, Pipeline: "demo", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := teststore.SeedNode(s, ctx, store.Node{
		RunID: runID, NodeID: nodeID, Status: "pending", NeedsLabels: labels,
	}); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	if err := teststore.SeedNodeReady(s, ctx, runID, nodeID); err != nil {
		t.Fatalf("MarkNodeReady: %v", err)
	}
}
