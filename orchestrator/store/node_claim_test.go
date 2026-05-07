package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
)

// seedRunAndNode creates a run + a single node in the 'pending'
// status. Shared helper for the node-claim tests so each test stays
// focused on the one behavior it's asserting.
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

func TestNodeClaim_MarkReadyIsIdempotent(t *testing.T) {
	s := newStoreT(t)
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
	first := *n1.ReadyAt

	time.Sleep(2 * time.Millisecond)
	if err := s.MarkNodeReady(ctx, "run-1", "node-a"); err != nil {
		t.Fatalf("MarkNodeReady 2: %v", err)
	}
	n2, _ := s.GetNode(ctx, "run-1", "node-a")
	if !n2.ReadyAt.Equal(first) {
		t.Fatalf("ready_at changed on 2nd MarkNodeReady: %v -> %v", first, *n2.ReadyAt)
	}
}

func TestNodeClaim_ClaimReturnsReadyNodeOnly(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()
	seedRunAndNode(t, s, "run-1", "node-a")

	// Not-ready node: claim should miss.
	n, err := s.ClaimNextReadyNode(ctx, "pod-1", 30*time.Second, nil)
	if err == nil {
		t.Fatalf("expected ErrNotFound, got node %v", n)
	}
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("wrong err: %v", err)
	}

	// Mark ready -> claim succeeds.
	if err := s.MarkNodeReady(ctx, "run-1", "node-a"); err != nil {
		t.Fatal(err)
	}
	n, err = s.ClaimNextReadyNode(ctx, "pod-1", 30*time.Second, nil)
	if err != nil {
		t.Fatalf("ClaimNextReadyNode: %v", err)
	}
	if n.RunID != "run-1" || n.NodeID != "node-a" {
		t.Fatalf("wrong node: %+v", n)
	}
	if n.ClaimedBy != "pod-1" {
		t.Fatalf("claimed_by: %q", n.ClaimedBy)
	}
	if n.LeaseExpiresAt == nil || !n.LeaseExpiresAt.After(time.Now()) {
		t.Fatalf("lease_expires_at not in future: %v", n.LeaseExpiresAt)
	}

	// Second claim -> empty queue.
	_, err = s.ClaimNextReadyNode(ctx, "pod-2", 30*time.Second, nil)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected ErrNotFound after claim, got %v", err)
	}
}

func TestNodeClaim_FIFOOrdering(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()
	seedRunAndNode(t, s, "run-1", "older")
	seedRunAndNode(t, s, "run-2", "newer")

	if err := s.MarkNodeReady(ctx, "run-1", "older"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	if err := s.MarkNodeReady(ctx, "run-2", "newer"); err != nil {
		t.Fatal(err)
	}

	n, err := s.ClaimNextReadyNode(ctx, "pod-1", 30*time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n.NodeID != "older" {
		t.Fatalf("FIFO violated: got %s first, expected 'older'", n.NodeID)
	}
}

func TestNodeClaim_HeartbeatExtendsLeaseForHolder(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()
	seedRunAndNode(t, s, "run-1", "node-a")
	if err := s.MarkNodeReady(ctx, "run-1", "node-a"); err != nil {
		t.Fatal(err)
	}
	n, err := s.ClaimNextReadyNode(ctx, "pod-1", 2*time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	firstLease := *n.LeaseExpiresAt
	time.Sleep(5 * time.Millisecond)

	if err := s.HeartbeatNodeClaim(ctx, "run-1", "node-a", "pod-1", 10*time.Second); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	n2, _ := s.GetNode(ctx, "run-1", "node-a")
	if !n2.LeaseExpiresAt.After(firstLease) {
		t.Fatalf("lease did not extend: %v -> %v", firstLease, *n2.LeaseExpiresAt)
	}

	// Wrong holder -> ErrLockHeld.
	err = s.HeartbeatNodeClaim(ctx, "run-1", "node-a", "pod-2", 10*time.Second)
	if !errors.Is(err, store.ErrLockHeld) {
		t.Fatalf("expected ErrLockHeld, got %v", err)
	}
}

func TestNodeClaim_ReapReleasesExpiredClaim(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()
	seedRunAndNode(t, s, "run-1", "node-a")
	if err := s.MarkNodeReady(ctx, "run-1", "node-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimNextReadyNode(ctx, "pod-dead", 1*time.Millisecond, nil); err != nil {
		t.Fatal(err)
	}
	// Lease is already expired.
	time.Sleep(5 * time.Millisecond)

	pairs, err := s.ReapExpiredNodeClaims(ctx)
	if err != nil {
		t.Fatalf("ReapExpiredNodeClaims: %v", err)
	}
	if len(pairs) != 1 || pairs[0] != [2]string{"run-1", "node-a"} {
		t.Fatalf("unexpected reap output: %v", pairs)
	}

	// Another pod can now claim.
	n, err := s.ClaimNextReadyNode(ctx, "pod-live", 30*time.Second, nil)
	if err != nil {
		t.Fatalf("claim after reap: %v", err)
	}
	if n.ClaimedBy != "pod-live" {
		t.Fatalf("claimed_by: %q", n.ClaimedBy)
	}
}

func TestNodeClaim_RevokeOnlyWhenUnclaimed(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()
	seedRunAndNode(t, s, "run-1", "node-a")
	if err := s.MarkNodeReady(ctx, "run-1", "node-a"); err != nil {
		t.Fatal(err)
	}

	// Unclaimed -> revoke succeeds, ready_at nulled.
	ok, err := s.RevokeNodeReady(ctx, "run-1", "node-a")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("revoke should have succeeded on unclaimed ready node")
	}
	n, _ := s.GetNode(ctx, "run-1", "node-a")
	if n.ReadyAt != nil {
		t.Fatalf("ready_at still set after revoke: %v", *n.ReadyAt)
	}

	// Mark ready + claim -> revoke should now refuse.
	if err := s.MarkNodeReady(ctx, "run-1", "node-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimNextReadyNode(ctx, "pod-1", 30*time.Second, nil); err != nil {
		t.Fatal(err)
	}
	ok, err = s.RevokeNodeReady(ctx, "run-1", "node-a")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("revoke should refuse when node is claimed")
	}
}

func TestNodeClaim_DoneNodesNotClaimable(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()
	seedRunAndNode(t, s, "run-1", "node-a")
	if err := s.MarkNodeReady(ctx, "run-1", "node-a"); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishNode(ctx, "run-1", "node-a", "success", "", []byte(`"ok"`)); err != nil {
		t.Fatal(err)
	}
	_, err := s.ClaimNextReadyNode(ctx, "pod-1", 30*time.Second, nil)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected done nodes to be unclaimable, got %v", err)
	}
}

// seedNodeWithLabels inserts a pending node with a needs_labels
// selector and marks it ready. Kept inline to the label-match tests
// so each case reads top-to-bottom.
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

func TestNodeClaim_LabelsExactMatch(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()
	seedNodeWithLabels(t, s, "run-1", "build", []string{"arm64", "laptop"})

	n, err := s.ClaimNextReadyNode(ctx, "pod-1", 30*time.Second, []string{"arm64", "laptop"})
	if err != nil {
		t.Fatalf("ClaimNextReadyNode: %v", err)
	}
	if n.NodeID != "build" {
		t.Fatalf("wrong node: %+v", n)
	}
	if len(n.NeedsLabels) != 2 {
		t.Fatalf("needs_labels round-trip failed: %v", n.NeedsLabels)
	}
}

func TestNodeClaim_LabelsSupersetClaims(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()
	// Node needs arm64; runner advertises arm64 + laptop (superset).
	seedNodeWithLabels(t, s, "run-1", "build", []string{"arm64"})

	n, err := s.ClaimNextReadyNode(ctx, "pod-1", 30*time.Second, []string{"arm64", "laptop"})
	if err != nil {
		t.Fatalf("ClaimNextReadyNode: %v", err)
	}
	if n.NodeID != "build" {
		t.Fatalf("wrong node: %+v", n)
	}
}

func TestNodeClaim_LabelsUnmatchedSkipped(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()

	// Older unmatchable node: needs gpu. Runner doesn't advertise gpu.
	seedNodeWithLabels(t, s, "run-1", "gpu-only", []string{"gpu"})
	time.Sleep(2 * time.Millisecond)
	// Younger matchable node: needs arm64. Runner advertises arm64.
	seedNodeWithLabels(t, s, "run-2", "build", []string{"arm64"})

	n, err := s.ClaimNextReadyNode(ctx, "pod-1", 30*time.Second, []string{"arm64", "laptop"})
	if err != nil {
		t.Fatalf("ClaimNextReadyNode: %v", err)
	}
	if n.NodeID != "build" {
		t.Fatalf("expected 'build' after skipping gpu-only, got %+v", n)
	}

	// gpu-only is still claimable by a runner that advertises gpu.
	n2, err := s.ClaimNextReadyNode(ctx, "pod-gpu", 30*time.Second, []string{"gpu"})
	if err != nil {
		t.Fatalf("gpu-only should still be claimable by a gpu runner: %v", err)
	}
	if n2.NodeID != "gpu-only" {
		t.Fatalf("wrong node: %+v", n2)
	}
}

func TestNodeClaim_UnlabeledNodeAlwaysClaimable(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()
	// needs_labels not set -> a runner advertising any set of labels
	// (including unrelated ones) can still claim.
	seedRunAndNode(t, s, "run-1", "node-a")
	if err := s.MarkNodeReady(ctx, "run-1", "node-a"); err != nil {
		t.Fatal(err)
	}
	n, err := s.ClaimNextReadyNode(ctx, "pod-1", 30*time.Second, []string{"arm64"})
	if err != nil {
		t.Fatal(err)
	}
	if n.NodeID != "node-a" {
		t.Fatalf("wrong node: %+v", n)
	}
}
