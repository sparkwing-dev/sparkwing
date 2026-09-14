package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// The queue claim and the named claim share one award, and the flag between
// them is the only thing keeping the queue claim off a node the controller has
// withdrawn from the queue between the scan and the award.
func TestAwardScannedNodeHonoursTheQueueRequirement(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	ctx := context.Background()
	if err := s.CreateRun(ctx, Run{ID: "run-1", Pipeline: "p", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateNode(ctx, Node{RunID: "run-1", NodeID: "build", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	coordinatorID, err := s.CoordinatorID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	candidate := claimCandidate{
		runID: "run-1", nodeID: "build", decision: placementDecision{reason: PlacementNone},
	}

	awarded, err := s.awardScannedNode(ctx, candidate, ClaimIdentity{}, "agent:box-a",
		coordinatorID, time.Minute, ClaimPlacement{}, true)
	if err != nil {
		t.Fatalf("queued award: %v", err)
	}
	if awarded != nil {
		t.Fatalf("the queue claim took a node with no ready_at, awarding it to %q", awarded.ClaimedBy)
	}

	awarded, err = s.awardScannedNode(ctx, candidate, ClaimIdentity{}, "k8s-job:sw-1",
		coordinatorID, time.Minute, ClaimPlacement{}, false)
	if err != nil {
		t.Fatalf("named award: %v", err)
	}
	if awarded == nil || awarded.ClaimedBy != "k8s-job:sw-1" {
		t.Fatalf("the named claim was refused a node it may take: %+v", awarded)
	}
}
