package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
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

func TestNodeClaim_ClaimReturnsReadyNodeOnly(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	seedRunAndNode(t, s, "run-1", "node-a")

	n, err := s.ClaimNextReadyNode(ctx, store.ClaimIdentity{Principal: "runner-principal", TokenPrefix: "swr_runner-principal"}, "pod-1", 30*time.Second, nil)
	if err == nil {
		t.Fatalf("expected ErrNotFound, got node %v", n)
	}
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("wrong err: %v", err)
	}

	if err := s.MarkNodeReady(ctx, "run-1", "node-a"); err != nil {
		t.Fatal(err)
	}
	n, err = s.ClaimNextReadyNode(ctx, store.ClaimIdentity{Principal: "runner-principal", TokenPrefix: "swr_runner-principal"}, "pod-1", 30*time.Second, nil)
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

	_, err = s.ClaimNextReadyNode(ctx, store.ClaimIdentity{Principal: "runner-principal", TokenPrefix: "swr_runner-principal"}, "pod-2", 30*time.Second, nil)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected ErrNotFound after claim, got %v", err)
	}
}

func TestNodeClaim_FIFOOrdering(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	seedRunAndNode(t, s, "run-1", "older")
	seedRunAndNode(t, s, "run-2", "newer")

	if err := s.MarkNodeReady(ctx, "run-1", "older"); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkNodeReady(ctx, "run-2", "newer"); err != nil {
		t.Fatal(err)
	}
	setNodeReadyAt(t, s, "run-1", "older", time.Unix(100, 0))
	setNodeReadyAt(t, s, "run-2", "newer", time.Unix(200, 0))

	n, err := s.ClaimNextReadyNode(ctx, store.ClaimIdentity{Principal: "runner-principal", TokenPrefix: "swr_runner-principal"}, "pod-1", 30*time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n.NodeID != "older" {
		t.Fatalf("FIFO violated: got %s first, expected 'older'", n.NodeID)
	}
}

func TestNodeClaim_ReapReleasesExpiredClaim(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	seedRunAndNode(t, s, "run-1", "node-a")
	if err := s.MarkNodeReady(ctx, "run-1", "node-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimNextReadyNode(ctx, store.ClaimIdentity{Principal: "runner-principal", TokenPrefix: "swr_runner-principal"}, "pod-dead", 1*time.Millisecond, nil); err != nil {
		t.Fatal(err)
	}
	expireNodeClaim(t, s, "run-1", "node-a")

	pairs, err := teststore.ReapUnits(ctx, s)
	if err != nil {
		t.Fatalf("ReapUnits: %v", err)
	}
	if len(pairs) != 1 || (pairs[0].RunID != "run-1" || pairs[0].NodeID != "node-a") {
		t.Fatalf("unexpected reap output: %v", pairs)
	}

	n, err := s.ClaimNextReadyNode(ctx, store.ClaimIdentity{Principal: "runner-principal", TokenPrefix: "swr_runner-principal"}, "pod-live", 30*time.Second, nil)
	if err != nil {
		t.Fatalf("claim after reap: %v", err)
	}
	if n.ClaimedBy != "pod-live" {
		t.Fatalf("claimed_by: %q", n.ClaimedBy)
	}
}

func TestNodeClaim_DoneNodesNotClaimable(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	seedRunAndNode(t, s, "run-1", "node-a")
	if err := s.MarkNodeReady(ctx, "run-1", "node-a"); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishNode(ctx, "run-1", "node-a", "success", "", []byte(`"ok"`)); err != nil {
		t.Fatal(err)
	}
	_, err := s.ClaimNextReadyNode(ctx, store.ClaimIdentity{Principal: "runner-principal", TokenPrefix: "swr_runner-principal"}, "pod-1", 30*time.Second, nil)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected done nodes to be unclaimable, got %v", err)
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

func TestNodeClaim_LabelsExactMatch(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	seedNodeWithLabels(t, s, "run-1", "build", []string{"arm64", "laptop"})

	n, err := s.ClaimNextReadyNode(ctx, store.ClaimIdentity{Principal: "runner-principal", TokenPrefix: "swr_runner-principal"}, "pod-1", 30*time.Second, []string{"arm64", "laptop"})
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
	s := storetest.Open(t)
	ctx := context.Background()
	seedNodeWithLabels(t, s, "run-1", "build", []string{"arm64"})

	n, err := s.ClaimNextReadyNode(ctx, store.ClaimIdentity{Principal: "runner-principal", TokenPrefix: "swr_runner-principal"}, "pod-1", 30*time.Second, []string{"arm64", "laptop"})
	if err != nil {
		t.Fatalf("ClaimNextReadyNode: %v", err)
	}
	if n.NodeID != "build" {
		t.Fatalf("wrong node: %+v", n)
	}
}

func TestNodeClaim_LabelsUnmatchedSkipped(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()

	seedNodeWithLabels(t, s, "run-1", "gpu-only", []string{"gpu"})
	seedNodeWithLabels(t, s, "run-2", "build", []string{"arm64"})
	setNodeReadyAt(t, s, "run-1", "gpu-only", time.Unix(100, 0))
	setNodeReadyAt(t, s, "run-2", "build", time.Unix(200, 0))

	n, err := s.ClaimNextReadyNode(ctx, store.ClaimIdentity{Principal: "runner-principal", TokenPrefix: "swr_runner-principal"}, "pod-1", 30*time.Second, []string{"arm64", "laptop"})
	if err != nil {
		t.Fatalf("ClaimNextReadyNode: %v", err)
	}
	if n.NodeID != "build" {
		t.Fatalf("expected 'build' after skipping gpu-only, got %+v", n)
	}

	n2, err := s.ClaimNextReadyNode(ctx, store.ClaimIdentity{Principal: "runner-principal", TokenPrefix: "swr_runner-principal"}, "pod-gpu", 30*time.Second, []string{"gpu"})
	if err != nil {
		t.Fatalf("gpu-only should still be claimable by a gpu runner: %v", err)
	}
	if n2.NodeID != "gpu-only" {
		t.Fatalf("wrong node: %+v", n2)
	}
}

func TestNodeClaim_UnlabeledNodeAlwaysClaimable(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	seedRunAndNode(t, s, "run-1", "node-a")
	if err := s.MarkNodeReady(ctx, "run-1", "node-a"); err != nil {
		t.Fatal(err)
	}
	n, err := s.ClaimNextReadyNode(ctx, store.ClaimIdentity{Principal: "runner-principal", TokenPrefix: "swr_runner-principal"}, "pod-1", 30*time.Second, []string{"arm64"})
	if err != nil {
		t.Fatal(err)
	}
	if n.NodeID != "node-a" {
		t.Fatalf("wrong node: %+v", n)
	}
}

func TestNodeClaim_RunnerCannotSelfAssertReservedLocation(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.3s of real work; the fast class runs under -short")
	}
	for _, selector := range []string{"local", "location=coordinator", "location=local", "location=cloud"} {
		t.Run(selector, func(t *testing.T) {
			s := storetest.Open(t)
			ctx := context.Background()
			seedNodeWithLabels(t, s, "run", "work", []string{selector})
			node, err := s.ClaimNextReadyNode(ctx,
				store.ClaimIdentity{Principal: "runner-principal", TokenPrefix: "swr_runner-principal"},
				"pod", 30*time.Second, []string{selector})
			if !errors.Is(err, store.ErrNotFound) || node != nil {
				t.Fatalf("self-asserted %q claim = %+v, %v", selector, node, err)
			}
		})
	}
}
