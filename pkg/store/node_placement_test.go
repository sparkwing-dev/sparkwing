package store_test

import (
	"context"
	"encoding/json"
	"errors"
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

var cloudRunner = store.ClaimIdentity{Principal: "cloud", TokenPrefix: "swr_cloud"}

var localRunner = store.ClaimIdentity{Principal: "local", TokenPrefix: "swr_local"}

func liveLocalPool() store.ClaimPlacement {
	return store.ClaimPlacement{
		Hold: time.Minute,
		Live: []store.RunnerPresence{{Name: "laptop", Labels: []string{"location=local"}}},
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

func TestClaimPlacement_HoldsNodeFromRunnerMissingThePreference(t *testing.T) {
	s := storetest.Open(t)
	ctx := store.WithClaimPlacement(context.Background(), liveLocalPool())
	seedPreferringNode(t, s, "run-1", "node-a", []string{"location=local"})

	_, err := s.ClaimNextReadyNode(ctx, cloudRunner, "runner:cloud:1", time.Minute, []string{"location=cloud"})
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cloud claim during the hold: %v, want ErrNotFound", err)
	}

	n, err := s.ClaimNextReadyNode(ctx, localRunner, "runner:laptop:1", time.Minute, []string{"location=local"})
	if err != nil {
		t.Fatalf("local claim: %v", err)
	}
	if n.PlacementReason != store.PlacementPreferred {
		t.Fatalf("placement reason = %q, want %q", n.PlacementReason, store.PlacementPreferred)
	}
	placed := placementEvents(t, s, "run-1")
	if len(placed) != 1 {
		t.Fatalf("node_placed events = %d, want 1", len(placed))
	}
	var payload struct {
		HolderID string   `json:"holder_id"`
		Reason   string   `json:"reason"`
		Prefers  []string `json:"prefers"`
	}
	if err := json.Unmarshal(placed[0].Payload, &payload); err != nil {
		t.Fatalf("decode node_placed: %v", err)
	}
	if payload.HolderID != "runner:laptop:1" || payload.Reason != store.PlacementPreferred {
		t.Fatalf("node_placed payload = %+v", payload)
	}
}

func TestClaimPlacement_FallsBackAfterTheHoldWindow(t *testing.T) {
	s := storetest.Open(t)
	ctx := store.WithClaimPlacement(context.Background(), liveLocalPool())
	seedPreferringNode(t, s, "run-1", "node-a", []string{"location=local"})
	setNodeReadyAt(t, s, "run-1", "node-a", time.Now().Add(-2*time.Minute))

	n, err := s.ClaimNextReadyNode(ctx, cloudRunner, "runner:cloud:1", time.Minute, []string{"location=cloud"})
	if err != nil {
		t.Fatalf("cloud claim after the hold: %v", err)
	}
	if n.PlacementReason != store.PlacementFallback {
		t.Fatalf("placement reason = %q, want %q", n.PlacementReason, store.PlacementFallback)
	}
	stored, err := s.GetNode(context.Background(), "run-1", "node-a")
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if stored.PlacementReason != store.PlacementFallback {
		t.Fatalf("stored placement reason = %q", stored.PlacementReason)
	}
	if len(placementEvents(t, s, "run-1")) != 1 {
		t.Fatal("fallback placement recorded no node_placed event")
	}
}

func TestClaimPlacement_HoldsNothingWithoutAPreference(t *testing.T) {
	s := storetest.Open(t)
	ctx := store.WithClaimPlacement(context.Background(), liveLocalPool())
	seedPreferringNode(t, s, "run-1", "node-a", nil)

	n, err := s.ClaimNextReadyNode(ctx, cloudRunner, "runner:cloud:1", time.Minute, []string{"location=cloud"})
	if err != nil {
		t.Fatalf("cloud claim: %v", err)
	}
	if n.PlacementReason != store.PlacementNone {
		t.Fatalf("placement reason = %q, want %q", n.PlacementReason, store.PlacementNone)
	}
	if events := placementEvents(t, s, "run-1"); len(events) != 0 {
		t.Fatalf("node_placed events = %d, want none", len(events))
	}
}

func TestClaimPlacement_ControllerDefaultAppliesToNodesWithoutPrefers(t *testing.T) {
	s := storetest.Open(t)
	placement := liveLocalPool()
	placement.DefaultPrefers = []string{"location=local"}
	ctx := store.WithClaimPlacement(context.Background(), placement)
	seedPreferringNode(t, s, "run-1", "node-a", nil)

	_, err := s.ClaimNextReadyNode(ctx, cloudRunner, "runner:cloud:1", time.Minute, []string{"location=cloud"})
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cloud claim under the default preference: %v, want ErrNotFound", err)
	}
}

func TestClaimPlacement_SaturatedPreferredRunnerHoldsNothing(t *testing.T) {
	s := storetest.Open(t)
	placement := liveLocalPool()
	placement.Live[0].AtCapacity = true
	ctx := store.WithClaimPlacement(context.Background(), placement)
	seedPreferringNode(t, s, "run-1", "node-a", []string{"location=local"})

	n, err := s.ClaimNextReadyNode(ctx, cloudRunner, "runner:cloud:1", time.Minute, []string{"location=cloud"})
	if err != nil {
		t.Fatalf("cloud claim with the local runner full: %v", err)
	}
	if n.PlacementReason != store.PlacementFallback {
		t.Fatalf("placement reason = %q, want %q", n.PlacementReason, store.PlacementFallback)
	}
}

func TestClaimPlacement_NoLiveRunnerHoldsNothing(t *testing.T) {
	s := storetest.Open(t)
	ctx := store.WithClaimPlacement(context.Background(), store.ClaimPlacement{Hold: time.Minute})
	seedPreferringNode(t, s, "run-1", "node-a", []string{"location=local"})

	n, err := s.ClaimNextReadyNode(ctx, cloudRunner, "runner:cloud:1", time.Minute, []string{"location=cloud"})
	if err != nil {
		t.Fatalf("cloud claim with no local runner live: %v", err)
	}
	if n.PlacementReason != store.PlacementFallback {
		t.Fatalf("placement reason = %q, want %q", n.PlacementReason, store.PlacementFallback)
	}
}

func TestClaimPlacement_HeldNodeDoesNotBlockTheQueue(t *testing.T) {
	s := storetest.Open(t)
	ctx := store.WithClaimPlacement(context.Background(), liveLocalPool())
	seedPreferringNode(t, s, "run-1", "held", []string{"location=local"})
	seedPreferringNode(t, s, "run-2", "open", nil)
	setNodeReadyAt(t, s, "run-1", "held", time.Now().Add(-time.Second))

	n, err := s.ClaimNextReadyNode(ctx, cloudRunner, "runner:cloud:1", time.Minute, []string{"location=cloud"})
	if err != nil {
		t.Fatalf("cloud claim past the held node: %v", err)
	}
	if n.NodeID != "open" {
		t.Fatalf("claimed node = %q, want the unheld one", n.NodeID)
	}
	held, err := s.GetNode(context.Background(), "run-1", "held")
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if held.Claimed {
		t.Fatal("held node was claimed")
	}
	if held.ReadyAt == nil || held.ReadyAt.After(time.Now().Add(-time.Millisecond)) {
		t.Fatalf("hold rewrote ready_at to %v, which would restart its own window", held.ReadyAt)
	}
}

func TestClaimPlacement_UnconfiguredClaimIsFirstInFirstOut(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	seedPreferringNode(t, s, "run-1", "node-a", []string{"location=local"})

	n, err := s.ClaimNextReadyNode(ctx, cloudRunner, "runner:cloud:1", time.Minute, []string{"location=cloud"})
	if err != nil {
		t.Fatalf("cloud claim without a placement policy: %v", err)
	}
	if n.NodeID != "node-a" {
		t.Fatalf("claimed node = %q", n.NodeID)
	}
	if n.PlacementReason != store.PlacementFallback {
		t.Fatalf("placement reason = %q, want %q", n.PlacementReason, store.PlacementFallback)
	}
}

func TestClaimPlacement_SelfAssertedLocationSatisfiesNoRequirement(t *testing.T) {
	s := storetest.Open(t)
	ctx := store.WithClaimPlacement(context.Background(), liveLocalPool())
	if err := s.CreateRun(ctx, store.Run{
		ID: "run-1", Pipeline: "demo", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := s.CreateNode(ctx, store.Node{
		RunID: "run-1", NodeID: "node-a", Status: "pending",
		NeedsLabels: []string{"location=local"}, PrefersLabels: []string{"location=local"},
	}); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	if err := s.MarkNodeReady(ctx, "run-1", "node-a"); err != nil {
		t.Fatalf("MarkNodeReady: %v", err)
	}

	_, err := s.ClaimNextReadyNode(ctx, localRunner, "runner:laptop:1", time.Minute, []string{"location=local"})
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("claim on a self-asserted hard requirement: %v, want ErrNotFound", err)
	}
}
