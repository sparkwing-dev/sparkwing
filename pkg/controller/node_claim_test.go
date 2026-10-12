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
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
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
	st, err := teststore.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	srv := httptest.NewServer(controller.New(st, nil).EnableAuthFromStore().Handler())
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
	visible, err := c.GetNode(ctx, "run-1", "node-a")
	if err != nil {
		t.Fatal(err)
	}
	if !visible.Claimed || visible.ClaimedBy != "" {
		t.Fatalf("ordinary node response exposed holder or lost claim state: %+v", visible)
	}
	claimCtx := store.WithNodeClaimFence(ctx, store.NodeClaimFence{
		HolderID: n.ClaimedBy, MembershipID: n.ClaimMembershipID,
		ReservationID: n.ReservationID, ClaimGeneration: n.ClaimGeneration,
	})

	if err := c.HeartbeatNodeClaim(claimCtx, "run-1", "node-a", "pod-1", 30*time.Second, nil); err != nil {
		t.Fatalf("HeartbeatNodeClaim (holder): %v", err)
	}

	err = c.HeartbeatNodeClaim(claimCtx, "run-1", "node-a", "pod-2", 30*time.Second, nil)
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

func TestNodeClaim_ExecutionStartAcknowledgement(t *testing.T) {
	st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	srv := httptest.NewServer(controller.New(st, nil).Handler())
	defer srv.Close()
	c := client.New(srv.URL, nil)
	ctx := context.Background()
	seedRunNode(t, st, "run-start", "build")
	if err := c.MarkNodeReady(ctx, "run-start", "build"); err != nil {
		t.Fatal(err)
	}
	n, err := c.ClaimNode(ctx, "gateway:edge-a:1", nil, time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.AcknowledgeNodeExecutionStart(ctx, n.RunID, n.NodeID, store.ExecutionStart{HolderID: n.ClaimedBy, ClaimGeneration: n.ClaimGeneration, AttemptOrdinal: 1}); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetNode(ctx, n.RunID, n.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ExecutionStartedAt == nil || got.ExecutorKind != "" || got.ExecutorID != "" || got.ReservationID != "" || got.ExecutorLocation != "unknown" {
		t.Fatalf("execution attempt = %+v", got)
	}
	if err := c.AcknowledgeNodeExecutionStart(ctx, n.RunID, n.NodeID, store.ExecutionStart{HolderID: "gateway:edge-b:1", ClaimGeneration: n.ClaimGeneration, AttemptOrdinal: 1}); !errors.Is(err, store.ErrLockHeld) {
		t.Fatalf("wrong holder acknowledgement = %v, want ErrLockHeld", err)
	}
	invalid := store.ExecutionAttemptFinish{
		HolderID: n.ClaimedBy, ClaimGeneration: n.ClaimGeneration, AttemptOrdinal: 1,
		Outcome: "failed", FailureReason: "credential-shaped-free-text",
	}
	if err := c.FinishNodeExecutionAttempt(ctx, n.RunID, n.NodeID, invalid); err == nil || errors.Is(err, store.ErrLockHeld) {
		t.Fatalf("unstructured execution finish = %v, want bad request", err)
	}
	finish := store.ExecutionAttemptFinish{
		HolderID: n.ClaimedBy, ClaimGeneration: n.ClaimGeneration, AttemptOrdinal: 1,
		Outcome: "failed", FailureReason: store.FailureVerify,
	}
	if err := c.FinishNodeExecutionAttempt(ctx, n.RunID, n.NodeID, finish); err != nil {
		t.Fatal(err)
	}
	if err := c.FinishNodeExecutionAttempt(ctx, n.RunID, n.NodeID, finish); err != nil {
		t.Fatalf("idempotent execution finish: %v", err)
	}
	finish.Outcome = "success"
	finish.FailureReason = ""
	if err := c.FinishNodeExecutionAttempt(ctx, n.RunID, n.NodeID, finish); !errors.Is(err, store.ErrLockHeld) {
		t.Fatalf("conflicting execution finish = %v, want ErrLockHeld", err)
	}
	if err := c.AcknowledgeNodeExecutionStart(ctx, n.RunID, n.NodeID, store.ExecutionStart{
		HolderID: n.ClaimedBy, ClaimGeneration: n.ClaimGeneration, AttemptOrdinal: 2,
	}); err != nil {
		t.Fatal(err)
	}
	if err := c.FinishNodeExecutionAttempt(ctx, n.RunID, n.NodeID, store.ExecutionAttemptFinish{
		HolderID: n.ClaimedBy, ClaimGeneration: n.ClaimGeneration, AttemptOrdinal: 2,
		Outcome: "cancelled",
	}); err != nil {
		t.Fatalf("cancelled execution finish: %v", err)
	}
}

func TestNodeClaim_RevokeAfterReadyNoPodClaimedYet(t *testing.T) {
	dir := t.TempDir()
	st, err := teststore.Open(filepath.Join(dir, "state.db"))
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
	st, err := teststore.Open(filepath.Join(dir, "state.db"))
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
