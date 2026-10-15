package store_test

import (
	"context"

	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func seedPreferringNode(t *testing.T, s *store.Store, runID, nodeID string, prefers []string) {
	t.Helper()
	ctx := context.Background()
	if err := s.CreateRun(ctx, store.Run{
		ID: runID, Pipeline: "demo", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := s.CreateNode(ctx, store.Node{
		RunID: runID, NodeID: nodeID, Status: "pending", PrefersLabels: prefers,
	}); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	if err := s.MarkNodeReady(ctx, runID, nodeID); err != nil {
		t.Fatalf("MarkNodeReady: %v", err)
	}
}

func agePlacementHold(t *testing.T, s *store.Store, runID, nodeID string, at time.Time) {
	t.Helper()
	res, err := s.DB().Exec(storetest.Rebind(s,
		`UPDATE nodes SET placement_hold_from = ? WHERE run_id = ? AND node_id = ?`),
		at.UnixNano(), runID, nodeID,
	)
	if err != nil {
		t.Fatalf("age placement hold: %v", err)
	}
	changed, err := res.RowsAffected()
	if err != nil {
		t.Fatalf("count aged rows: %v", err)
	}
	if changed != 1 {
		t.Fatalf("aged rows = %d, want 1", changed)
	}
}

var cloudRunner = store.ClaimIdentity{Principal: "cloud", TokenPrefix: "swr_cloud"}

var localRunner = store.ClaimIdentity{Principal: "local", TokenPrefix: "swr_local"}

func liveLocalPool() store.ClaimPlacement {
	return store.ClaimPlacement{
		Hold: time.Minute,
		Live: []store.RunnerPresence{{Name: "laptop", Labels: []string{"location=local"}, FreeSlots: 2}},
	}
}

func placementEvents(t *testing.T, s *store.Store, runID string) []store.Event {
	t.Helper()
	events, err := s.ListEventsAfter(context.Background(), runID, 0, 100)
	if err != nil {
		t.Fatalf("ListEventsAfter: %v", err)
	}
	var placed []store.Event
	for _, event := range events {
		if event.Kind == "node_placed" {
			placed = append(placed, event)
		}
	}
	return placed
}
