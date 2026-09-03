package controller_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func seedRunNode(t *testing.T, st *store.Store, runID, nodeID string) {
	t.Helper()
	ctx := context.Background()
	if err := st.CreateRun(ctx, store.Run{
		ID: runID, Pipeline: "demo", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{
		RunID: runID, NodeID: nodeID, Status: "pending",
	}); err != nil {
		t.Fatal(err)
	}
}

func setNodeReadyAt(t *testing.T, st *store.Store, runID, nodeID string, at time.Time) {
	t.Helper()
	res, err := st.DB().Exec(
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

func TestNodeClaim_HTTPRoundTrip(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	srv := httptest.NewServer(controller.New(st, nil).Handler())
	defer srv.Close()
	c := client.New(srv.URL, nil)
	ctx := context.Background()

	n, err := c.ClaimNode(ctx, "pod-1", nil, 30*time.Second, nil)
	if err != nil || n != nil {
		t.Fatalf("expected (nil, nil) on empty queue, got (%v, %v)", n, err)
	}

	seedRunNode(t, st, "run-1", "node-a")
	if err := c.MarkNodeReady(ctx, "run-1", "node-a"); err != nil {
		t.Fatalf("MarkNodeReady: %v", err)
	}

	n, err = c.ClaimNode(ctx, "pod-1", nil, 30*time.Second, nil)
	if err != nil {
		t.Fatalf("ClaimNode: %v", err)
	}
	if n == nil || n.RunID != "run-1" || n.NodeID != "node-a" {
		t.Fatalf("wrong claim response: %+v", n)
	}
	if n.ClaimedBy != "pod-1" {
		t.Fatalf("claimed_by: %q", n.ClaimedBy)
	}

	if err := c.HeartbeatNodeClaim(ctx, "run-1", "node-a", "pod-1", 30*time.Second, nil); err != nil {
		t.Fatalf("HeartbeatNodeClaim (holder): %v", err)
	}

	err = c.HeartbeatNodeClaim(ctx, "run-1", "node-a", "pod-2", 30*time.Second, nil)
	if !errors.Is(err, store.ErrLockHeld) {
		t.Fatalf("expected ErrLockHeld, got %v", err)
	}

	revoked, err := c.RevokeNodeReady(ctx, "run-1", "node-a")
	if err != nil {
		t.Fatalf("RevokeNodeReady: %v", err)
	}
	if revoked {
		t.Fatal("revoke should be false when node is claimed")
	}
}

func TestNodeClaim_RevokeAfterReadyNoPodClaimedYet(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	srv := httptest.NewServer(controller.New(st, nil).Handler())
	defer srv.Close()
	c := client.New(srv.URL, nil)
	ctx := context.Background()

	seedRunNode(t, st, "run-1", "node-a")
	if err := c.MarkNodeReady(ctx, "run-1", "node-a"); err != nil {
		t.Fatal(err)
	}
	revoked, err := c.RevokeNodeReady(ctx, "run-1", "node-a")
	if err != nil {
		t.Fatal(err)
	}
	if !revoked {
		t.Fatal("revoke should succeed on ready, unclaimed node")
	}
	n, err := c.ClaimNode(ctx, "pod-1", nil, 30*time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n != nil {
		t.Fatalf("node should not be claimable after revoke: %+v", n)
	}
}

func TestNodeClaim_HTTPLabelFiltering(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	srv := httptest.NewServer(controller.New(st, nil).Handler())
	defer srv.Close()
	c := client.New(srv.URL, nil)
	ctx := context.Background()

	if err := st.CreateRun(ctx, store.Run{
		ID: "run-1", Pipeline: "demo", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{
		RunID: "run-1", NodeID: "special", Status: "pending", NeedsLabels: []string{"special"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{
		RunID: "run-1", NodeID: "anyone", Status: "pending",
	}); err != nil {
		t.Fatal(err)
	}
	if err := c.MarkNodeReady(ctx, "run-1", "special"); err != nil {
		t.Fatal(err)
	}
	if err := c.MarkNodeReady(ctx, "run-1", "anyone"); err != nil {
		t.Fatal(err)
	}
	setNodeReadyAt(t, st, "run-1", "special", time.Unix(100, 0))
	setNodeReadyAt(t, st, "run-1", "anyone", time.Unix(200, 0))

	n, err := c.ClaimNode(ctx, "plain-runner", nil, 30*time.Second, nil)
	if err != nil {
		t.Fatalf("ClaimNode plain: %v", err)
	}
	if n == nil || n.NodeID != "anyone" {
		t.Fatalf("plain runner claim: %+v", n)
	}

	n, err = c.ClaimNode(ctx, "special-runner", []string{"special"}, 30*time.Second, nil)
	if err != nil {
		t.Fatalf("ClaimNode labeled: %v", err)
	}
	if n == nil || n.NodeID != "special" {
		t.Fatalf("special runner claim: %+v", n)
	}
	if n.ClaimedBy != "special-runner" {
		t.Fatalf("claimed_by: %q", n.ClaimedBy)
	}
}

type fixedNodeClaimPolicy struct {
	ceiling   int
	effective int
}

func (p fixedNodeClaimPolicy) HighestEligiblePriority(context.Context, *store.Node) (int, error) {
	return p.ceiling, nil
}

func (p fixedNodeClaimPolicy) Resolver(_ context.Context, _ store.ClaimIdentity, req controller.NodeClaimRequest) (store.NodeClaimResolver, error) {
	return store.NodeClaimResolverFunc(func(*store.Node) (store.NodeClaimResolution, bool) {
		return store.NodeClaimResolution{
			WorkerID:          "registered-worker",
			ExecutorKind:      "gateway",
			ReservationID:     req.ReservationID,
			BasePriority:      40,
			EffectivePriority: p.effective,
		}, true
	}), nil
}

func TestNodeClaimOffer_HTTPUsesTrustedPolicyAndPreservesPendingState(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	srv := httptest.NewServer(controller.New(st, nil).WithNodeClaimPolicy(fixedNodeClaimPolicy{ceiling: 100, effective: 75}).Handler())
	defer srv.Close()
	c := client.New(srv.URL, nil)
	ctx := context.Background()
	seedRunNode(t, st, "run-1", "node-a")
	if err := c.MarkNodeReady(ctx, "run-1", "node-a"); err != nil {
		t.Fatal(err)
	}
	executor := client.NodeClaimExecutor{
		HolderID: "holder", WorkerID: "untrusted-worker", ExecutorKind: "direct",
		ReservationID: "reservation", ClaimPriority: 100,
	}
	summary, err := c.PrepareNodeClaim(ctx, executor)
	if err != nil || summary == nil || summary.RunID != "run-1" || summary.NodeID != "node-a" {
		t.Fatalf("PrepareNodeClaim = %+v, %v", summary, err)
	}
	result, err := c.OfferNodeClaim(ctx, executor, summary.RunID, summary.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Pending || result.Node != nil {
		t.Fatalf("first offer = %+v, want pending", result)
	}

	if _, err := st.DB().Exec(`UPDATE nodes SET offer_started_at = ? WHERE run_id = ? AND node_id = ?`,
		time.Now().Add(-6*time.Second).UnixNano(), "run-1", "node-a"); err != nil {
		t.Fatal(err)
	}
	result, err = c.OfferNodeClaim(ctx, executor, summary.RunID, summary.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Node == nil || result.Pending {
		t.Fatalf("ceiling offer = %+v", result)
	}
	if result.Node.ClaimPriority != 75 || result.Node.ClaimBasePriority != 40 ||
		result.Node.ClaimWorkerID != "registered-worker" || result.Node.ClaimExecutorKind != "gateway" {
		t.Fatalf("trusted claim metadata = %+v", result.Node)
	}
}
