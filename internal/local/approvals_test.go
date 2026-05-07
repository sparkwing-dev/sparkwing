package local_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/controller/client"
	controller "github.com/sparkwing-dev/sparkwing/internal/local"
	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
)

// seedRunWithNode is a small helper for the approval endpoint tests.
// Creates a run + a single pending node so CreateApproval has a row to
// flip.
func seedRunWithNode(t *testing.T, st *store.Store, runID, nodeID string) {
	t.Helper()
	ctx := context.Background()
	if err := st.CreateRun(ctx, store.Run{
		ID: runID, Pipeline: "demo", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: runID, NodeID: nodeID, Status: "pending"}); err != nil {
		t.Fatal(err)
	}
}

func TestApprovals_RequestThenApprove(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	srv := controller.New(st, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	c := client.New(ts.URL, nil)

	ctx := context.Background()
	seedRunWithNode(t, st, "run-1", "gate")

	if err := c.CreateApproval(ctx, store.Approval{
		RunID: "run-1", NodeID: "gate", Message: "ship it?",
		TimeoutMS: 60000, OnTimeout: store.ApprovalOnTimeoutFail,
	}); err != nil {
		t.Fatalf("CreateApproval: %v", err)
	}

	// Node flipped to approval_pending.
	n, err := st.GetNode(ctx, "run-1", "gate")
	if err != nil {
		t.Fatal(err)
	}
	if n.Status != store.NodeStatusApprovalPending {
		t.Fatalf("status: %q", n.Status)
	}

	got, err := c.GetApproval(ctx, "run-1", "gate")
	if err != nil {
		t.Fatalf("GetApproval: %v", err)
	}
	if got.Message != "ship it?" || got.ResolvedAt != nil {
		t.Fatalf("unexpected: %+v", got)
	}

	resolved, err := c.ResolveApproval(ctx, "run-1", "gate",
		store.ApprovalResolutionApproved, "alice", "lgtm")
	if err != nil {
		t.Fatalf("ResolveApproval: %v", err)
	}
	if resolved.Resolution != store.ApprovalResolutionApproved {
		t.Fatalf("resolution: %q", resolved.Resolution)
	}
	if resolved.ResolvedAt == nil {
		t.Fatalf("ResolvedAt should be set")
	}
}

func TestApprovals_ResolveTwiceIsConflict(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	srv := controller.New(st, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	c := client.New(ts.URL, nil)

	ctx := context.Background()
	seedRunWithNode(t, st, "run-1", "gate")
	if err := c.CreateApproval(ctx, store.Approval{RunID: "run-1", NodeID: "gate"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ResolveApproval(ctx, "run-1", "gate",
		store.ApprovalResolutionApproved, "alice", ""); err != nil {
		t.Fatal(err)
	}
	_, err = c.ResolveApproval(ctx, "run-1", "gate",
		store.ApprovalResolutionDenied, "bob", "")
	if !errors.Is(err, store.ErrLockHeld) {
		t.Fatalf("expected ErrLockHeld, got %v", err)
	}
}

func TestApprovals_ResolveMissingIs404(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	srv := controller.New(st, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	c := client.New(ts.URL, nil)

	_, err = c.ResolveApproval(context.Background(),
		"nope", "nope", store.ApprovalResolutionApproved, "alice", "")
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestApprovals_ListPendingAndForRun(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	srv := controller.New(st, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	c := client.New(ts.URL, nil)

	ctx := context.Background()
	seedRunWithNode(t, st, "run-1", "a")
	seedRunWithNode(t, st, "run-2", "b")

	if err := c.CreateApproval(ctx, store.Approval{RunID: "run-1", NodeID: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := c.CreateApproval(ctx, store.Approval{RunID: "run-2", NodeID: "b"}); err != nil {
		t.Fatal(err)
	}

	pend, err := c.ListPendingApprovals(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pend) != 2 {
		t.Fatalf("expected 2 pending, got %d", len(pend))
	}

	forRun, err := c.ListApprovalsForRun(ctx, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(forRun) != 1 || forRun[0].NodeID != "a" {
		t.Fatalf("unexpected forRun: %+v", forRun)
	}
}
