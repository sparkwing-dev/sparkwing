package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
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
	res, err := s.DB().Exec(
		`UPDATE nodes SET ready_at = ? WHERE run_id = ? AND node_id = ?`,
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
	res, err := s.DB().Exec(
		`UPDATE nodes SET lease_expires_at = ? WHERE run_id = ? AND node_id = ? AND claimed_by IS NOT NULL`,
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
	s := newStoreT(t)
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
	s := newStoreT(t)
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

func TestNodeClaim_HeartbeatExtendsLeaseForHolder(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()
	seedRunAndNode(t, s, "run-1", "node-a")
	if err := s.MarkNodeReady(ctx, "run-1", "node-a"); err != nil {
		t.Fatal(err)
	}
	n, err := s.ClaimNextReadyNode(ctx, store.ClaimIdentity{Principal: "runner-principal", TokenPrefix: "swr_runner-principal"}, "pod-1", 2*time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	firstLease := *n.LeaseExpiresAt
	heartbeatCtx := store.WithNodeClaimFence(ctx, store.NodeClaimFence{
		Claimant: store.ClaimIdentity{Principal: "runner-principal", TokenPrefix: "swr_runner-principal"},
		HolderID: n.ClaimedBy, MembershipID: n.ClaimMembershipID,
		ReservationID: n.ReservationID, ClaimGeneration: n.ClaimGeneration,
	})

	if err := s.HeartbeatNodeClaim(heartbeatCtx, "run-1", "node-a", store.ClaimIdentity{Principal: "runner-principal", TokenPrefix: "swr_runner-principal"}, "pod-1", 10*time.Second); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	n2, _ := s.GetNode(ctx, "run-1", "node-a")
	if n2.LeaseExpiresAt == nil || n2.LeaseExpiresAt.Sub(firstLease) < 7*time.Second {
		t.Fatalf("lease did not extend by at least 7s: %v -> %v", firstLease, n2.LeaseExpiresAt)
	}

	err = s.HeartbeatNodeClaim(heartbeatCtx, "run-1", "node-a", store.ClaimIdentity{Principal: "runner-principal", TokenPrefix: "swr_runner-principal"}, "pod-2", 10*time.Second)
	if !errors.Is(err, store.ErrLockHeld) {
		t.Fatalf("expected ErrLockHeld, got %v", err)
	}
}

func TestNodeClaim_HeartbeatRejectsPriorGenerationWithSameHolder(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()
	claimant := store.ClaimIdentity{Principal: "runner-principal", TokenPrefix: "swr_runner-principal"}
	seedRunAndNode(t, s, "run-1", "node-a")
	if err := s.MarkNodeReady(ctx, "run-1", "node-a"); err != nil {
		t.Fatal(err)
	}
	first, err := s.ClaimNextReadyNode(ctx, claimant, "pod-1", time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	staleCtx := store.WithNodeClaimFence(ctx, store.NodeClaimFence{
		Claimant: claimant, HolderID: first.ClaimedBy, MembershipID: first.ClaimMembershipID,
		ReservationID: first.ReservationID, ClaimGeneration: first.ClaimGeneration,
	})
	if _, err := s.DB().ExecContext(ctx, `UPDATE nodes
SET claim_generation = claim_generation + 1, lease_expires_at = ?
WHERE run_id = 'run-1' AND node_id = 'node-a'`, time.Now().Add(time.Minute).UnixNano()); err != nil {
		t.Fatal(err)
	}
	if err := s.HeartbeatNodeClaim(staleCtx, "run-1", "node-a", claimant, "pod-1", time.Minute); !errors.Is(err, store.ErrLockHeld) {
		t.Fatalf("stale generation heartbeat = %v, want ErrLockHeld", err)
	}
	currentCtx := store.WithNodeClaimFence(ctx, store.NodeClaimFence{
		Claimant: claimant, HolderID: first.ClaimedBy, MembershipID: first.ClaimMembershipID,
		ReservationID: first.ReservationID, ClaimGeneration: first.ClaimGeneration + 1,
	})
	if err := s.HeartbeatNodeClaim(currentCtx, "run-1", "node-a", claimant, "pod-1", time.Minute); err != nil {
		t.Fatalf("current generation heartbeat: %v", err)
	}
}

func TestNodeClaim_HeartbeatCannotReviveExpiredLease(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()
	claimant := store.ClaimIdentity{Principal: "runner-principal", TokenPrefix: "swr_runner-principal"}
	seedRunAndNode(t, s, "run-1", "node-a")
	if err := s.MarkNodeReady(ctx, "run-1", "node-a"); err != nil {
		t.Fatal(err)
	}
	node, err := s.ClaimNextReadyNode(ctx, claimant, "pod-1", time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	heartbeatCtx := store.WithNodeClaimFence(ctx, store.NodeClaimFence{
		Claimant: claimant, HolderID: node.ClaimedBy, MembershipID: node.ClaimMembershipID,
		ReservationID: node.ReservationID, ClaimGeneration: node.ClaimGeneration,
	})
	if _, err := s.DB().ExecContext(ctx, `UPDATE nodes SET lease_expires_at = ? WHERE run_id = 'run-1' AND node_id = 'node-a'`, time.Now().Add(-time.Second).UnixNano()); err != nil {
		t.Fatal(err)
	}
	if err := s.HeartbeatNodeClaim(heartbeatCtx, "run-1", "node-a", claimant, "pod-1", time.Minute); !errors.Is(err, store.ErrLockHeld) {
		t.Fatalf("expired heartbeat = %v, want ErrLockHeld", err)
	}
}

func TestNodeClaim_PrincipalBindingGatesClaimOwnership(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()
	seedRunAndNode(t, s, "run-1", "node-a")
	if err := s.MarkNodeReady(ctx, "run-1", "node-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimNextReadyNode(ctx, store.ClaimIdentity{Principal: "runner-a", TokenPrefix: "swr_runner-a"}, "pod-1", time.Minute, nil); err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	claimant := store.ClaimIdentity{Principal: "runner-a", TokenPrefix: "swr_runner-a"}
	for _, tc := range []struct {
		name     string
		claimant store.ClaimIdentity
		at       time.Time
		want     bool
	}{
		{"claiming token", claimant, now, true},
		{"another principal", store.ClaimIdentity{Principal: "runner-b", TokenPrefix: "swr_runner-b"}, now, false},
		{"same principal name, another token", store.ClaimIdentity{Principal: "runner-a", TokenPrefix: "swr_other"}, now, false},
		{"no principal", store.ClaimIdentity{}, now, false},
		{"claiming token after the lease expires", claimant, now.Add(2 * time.Minute), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			held, err := s.PrincipalHoldsNodeClaim(ctx, "run-1", "node-a", tc.claimant, tc.at)
			if err != nil {
				t.Fatalf("PrincipalHoldsNodeClaim: %v", err)
			}
			if held != tc.want {
				t.Errorf("PrincipalHoldsNodeClaim(%+v) = %v, want %v", tc.claimant, held, tc.want)
			}
			held, err = s.PrincipalHoldsRunClaim(ctx, "run-1", tc.claimant, tc.at)
			if err != nil {
				t.Fatalf("PrincipalHoldsRunClaim: %v", err)
			}
			if held != tc.want {
				t.Errorf("PrincipalHoldsRunClaim(%+v) = %v, want %v", tc.claimant, held, tc.want)
			}
		})
	}

	wrongCtx := store.WithNodeClaimFence(ctx, store.NodeClaimFence{
		Claimant: store.ClaimIdentity{Principal: "runner-b", TokenPrefix: "swr_runner-b"},
		HolderID: "pod-1", ClaimGeneration: 1,
	})
	if err := s.HeartbeatNodeClaim(wrongCtx, "run-1", "node-a", store.ClaimIdentity{Principal: "runner-b", TokenPrefix: "swr_runner-b"}, "pod-1", time.Minute); !errors.Is(err, store.ErrLockHeld) {
		t.Errorf("heartbeat from another principal holding the same holder id = %v, want ErrLockHeld", err)
	}
}

func TestNodeClaim_ReapReleasesExpiredClaim(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()
	seedRunAndNode(t, s, "run-1", "node-a")
	if err := s.MarkNodeReady(ctx, "run-1", "node-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimNextReadyNode(ctx, store.ClaimIdentity{Principal: "runner-principal", TokenPrefix: "swr_runner-principal"}, "pod-dead", 1*time.Millisecond, nil); err != nil {
		t.Fatal(err)
	}
	expireNodeClaim(t, s, "run-1", "node-a")

	pairs, err := s.ReapExpiredNodeClaims(ctx)
	if err != nil {
		t.Fatalf("ReapExpiredNodeClaims: %v", err)
	}
	if len(pairs) != 1 || pairs[0] != [2]string{"run-1", "node-a"} {
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

func TestNodeClaim_RevokeOnlyWhenUnclaimed(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()
	seedRunAndNode(t, s, "run-1", "node-a")
	if err := s.MarkNodeReady(ctx, "run-1", "node-a"); err != nil {
		t.Fatal(err)
	}

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

	if err := s.MarkNodeReady(ctx, "run-1", "node-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimNextReadyNode(ctx, store.ClaimIdentity{Principal: "runner-principal", TokenPrefix: "swr_runner-principal"}, "pod-1", 30*time.Second, nil); err != nil {
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
	s := newStoreT(t)
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
	s := newStoreT(t)
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
	s := newStoreT(t)
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
	s := newStoreT(t)
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

func TestNodeClaim_LegacyRunnerCannotSelfAssertReservedLocation(t *testing.T) {
	for _, selector := range []string{"local", "location=coordinator", "location=local", "location=cloud"} {
		t.Run(selector, func(t *testing.T) {
			s := newStoreT(t)
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

func TestNodeClaim_LeaseIsClampedToTheServerCap(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()
	seedRunAndNode(t, s, "run-1", "node-a")
	if err := s.MarkNodeReady(ctx, "run-1", "node-a"); err != nil {
		t.Fatal(err)
	}
	claimant := store.ClaimIdentity{Principal: "runner-a", TokenPrefix: "swr_runner-a"}

	day := 24 * time.Hour
	n, err := s.ClaimNextReadyNode(ctx, claimant, "pod-1", day, nil)
	if err != nil {
		t.Fatalf("ClaimNextReadyNode: %v", err)
	}
	limit := time.Now().Add(store.MaxLeaseDuration).Add(time.Minute)
	if n.LeaseExpiresAt == nil || n.LeaseExpiresAt.After(limit) {
		t.Errorf("claim lease = %v, want no later than %s", n.LeaseExpiresAt, limit)
	}

	heartbeatCtx := store.WithNodeClaimFence(ctx, store.NodeClaimFence{
		Claimant: claimant, HolderID: n.ClaimedBy, MembershipID: n.ClaimMembershipID,
		ReservationID: n.ReservationID, ClaimGeneration: n.ClaimGeneration,
	})
	if err := s.HeartbeatNodeClaim(heartbeatCtx, "run-1", "node-a", claimant, "pod-1", day); err != nil {
		t.Fatalf("HeartbeatNodeClaim: %v", err)
	}
	held, err := s.PrincipalHoldsNodeClaim(ctx, "run-1", "node-a", claimant,
		time.Now().Add(store.MaxLeaseDuration).Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if held {
		t.Error("a day-long heartbeat kept the claim alive past the cap")
	}
}
