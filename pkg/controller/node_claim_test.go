package controller_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/v2/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/v2/controller/client"
	"github.com/sparkwing-dev/sparkwing/v2/orchestrator/store"
)

// seedRunNode is a local helper so these tests don't depend on the
// run/trigger path -- we only care about the node-claim endpoints.
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

// TestNodeClaim_HTTPRoundTrip exercises the full mark-ready / claim /
// heartbeat / revoke surface via the Go client against an httptest
// server. Covers: empty queue returns (nil, nil); mark ready +
// claim returns the node; heartbeat with a different holder id
// returns ErrLockHeld; revoke on a claimed node returns false.
func TestNodeClaim_HTTPRoundTrip(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	srv := httptest.NewServer(controller.New(st, nil).Handler())
	defer srv.Close()
	c := client.New(srv.URL, nil)
	ctx := context.Background()

	// Empty queue.
	n, err := c.ClaimNode(ctx, "pod-1", nil, 30*time.Second)
	if err != nil || n != nil {
		t.Fatalf("expected (nil, nil) on empty queue, got (%v, %v)", n, err)
	}

	seedRunNode(t, st, "run-1", "node-a")
	if err := c.MarkNodeReady(ctx, "run-1", "node-a"); err != nil {
		t.Fatalf("MarkNodeReady: %v", err)
	}

	n, err = c.ClaimNode(ctx, "pod-1", nil, 30*time.Second)
	if err != nil {
		t.Fatalf("ClaimNode: %v", err)
	}
	if n == nil || n.RunID != "run-1" || n.NodeID != "node-a" {
		t.Fatalf("wrong claim response: %+v", n)
	}
	if n.ClaimedBy != "pod-1" {
		t.Fatalf("claimed_by: %q", n.ClaimedBy)
	}

	// Heartbeat by the holder succeeds.
	if err := c.HeartbeatNodeClaim(ctx, "run-1", "node-a", "pod-1", 30*time.Second); err != nil {
		t.Fatalf("HeartbeatNodeClaim (holder): %v", err)
	}

	// Heartbeat by a different pod -> ErrLockHeld.
	err = c.HeartbeatNodeClaim(ctx, "run-1", "node-a", "pod-2", 30*time.Second)
	if !errors.Is(err, store.ErrLockHeld) {
		t.Fatalf("expected ErrLockHeld, got %v", err)
	}

	// Revoke while claimed -> false.
	revoked, err := c.RevokeNodeReady(ctx, "run-1", "node-a")
	if err != nil {
		t.Fatalf("RevokeNodeReady: %v", err)
	}
	if revoked {
		t.Fatal("revoke should be false when node is claimed")
	}
}

// TestNodeClaim_RevokeAfterReadyNoPodClaimedYet covers the warm-pool
// Runner's fallback path: if the orchestrator calls MarkNodeReady but
// no pool pod has claimed yet, RevokeNodeReady returns true and the
// node is no longer claimable until the next MarkNodeReady call.
func TestNodeClaim_RevokeAfterReadyNoPodClaimedYet(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

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
	// Now unclaimable.
	n, err := c.ClaimNode(ctx, "pod-1", nil, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if n != nil {
		t.Fatalf("node should not be claimable after revoke: %+v", n)
	}
}

// TestNodeClaim_HTTPLabelFiltering covers the runs_on wire path: a
// runner posting labels only gets nodes whose needs_labels are a
// subset of those labels; a labeled node skipped by one runner is
// still claimable by a matching runner.
func TestNodeClaim_HTTPLabelFiltering(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

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
	// Mark special ready first so FIFO would hand it out before anyone,
	// proving the label filter -- not ordering -- is what skips it.
	if err := c.MarkNodeReady(ctx, "run-1", "special"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	if err := c.MarkNodeReady(ctx, "run-1", "anyone"); err != nil {
		t.Fatal(err)
	}

	// Unlabeled runner claims the unlabeled node; 'special' is skipped.
	n, err := c.ClaimNode(ctx, "plain-runner", nil, 30*time.Second)
	if err != nil {
		t.Fatalf("ClaimNode plain: %v", err)
	}
	if n == nil || n.NodeID != "anyone" {
		t.Fatalf("plain runner claim: %+v", n)
	}

	// Labeled runner claims the skipped node.
	n, err = c.ClaimNode(ctx, "special-runner", []string{"special"}, 30*time.Second)
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
