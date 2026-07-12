package store_test

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestCancelRun_CancelsNodesAndReleasesConcurrency(t *testing.T) {
	s := newStoreT(t)
	ctx := ctxT(t)

	if err := s.CreateRun(ctx, store.Run{
		ID:        "run-cancel",
		Pipeline:  "pipeline",
		Status:    "running",
		StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := s.CreateNode(ctx, store.Node{RunID: "run-cancel", NodeID: "running-node", Status: "pending"}); err != nil {
		t.Fatalf("CreateNode running-node: %v", err)
	}
	if err := s.StartNode(ctx, "run-cancel", "running-node"); err != nil {
		t.Fatalf("StartNode: %v", err)
	}
	if err := s.CreateNode(ctx, store.Node{RunID: "run-cancel", NodeID: "queued-node", Status: "pending"}); err != nil {
		t.Fatalf("CreateNode queued-node: %v", err)
	}

	acquireT(t, s, store.AcquireSlotRequest{
		Key: "shared", HolderID: "run-holder/node", RunID: "run-holder", NodeID: "node",
		Capacity: 1, Policy: store.OnLimitQueue,
	})
	if resp := acquireBareT(t, s, store.AcquireSlotRequest{
		Key: "shared", HolderID: "run-cancel/queued-node", RunID: "run-cancel", NodeID: "queued-node",
		Capacity: 1, Policy: store.OnLimitQueue,
	}); resp.Kind != store.AcquireQueued {
		t.Fatalf("queued-node acquire = %s, want %s", resp.Kind, store.AcquireQueued)
	}
	if resp := acquireBareT(t, s, store.AcquireSlotRequest{
		Key: "dedicated", HolderID: "run-cancel/running-node", RunID: "run-cancel", NodeID: "running-node",
		Capacity: 1, Policy: store.OnLimitQueue,
	}); resp.Kind != store.AcquireGranted {
		t.Fatalf("running-node acquire = %s, want %s", resp.Kind, store.AcquireGranted)
	}

	if err := s.CancelRun(ctx, "run-cancel", "operator cancelled run"); err != nil {
		t.Fatalf("CancelRun: %v", err)
	}

	run, err := s.GetRun(ctx, "run-cancel")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.Status != "cancelled" {
		t.Fatalf("run status = %q, want cancelled", run.Status)
	}
	if run.FinishedAt == nil {
		t.Fatalf("run finished_at is nil")
	}

	for _, nodeID := range []string{"running-node", "queued-node"} {
		node, err := s.GetNode(ctx, "run-cancel", nodeID)
		if err != nil {
			t.Fatalf("GetNode(%s): %v", nodeID, err)
		}
		if node.Status != "done" || node.Outcome != "cancelled" {
			t.Fatalf("%s status/outcome = %q/%q, want done/cancelled", nodeID, node.Status, node.Outcome)
		}
	}

	if got := waiterCount(t, s, "shared"); got != 0 {
		t.Fatalf("shared waiters = %d, want 0", got)
	}
	if holderExists(t, s, "dedicated", "run-cancel/running-node") {
		t.Fatalf("cancelled run still holds dedicated slot")
	}
	if !holderExists(t, s, "shared", "run-holder/node") {
		t.Fatalf("unrelated holder was removed")
	}
}

func TestCancelRun_BlocksFutureConcurrencyForCancelledRun(t *testing.T) {
	s := newStoreT(t)
	ctx := ctxT(t)

	if err := s.CreateRun(ctx, store.Run{
		ID:        "run-block-future",
		Pipeline:  "pipeline",
		Status:    "running",
		StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := s.CancelRun(ctx, "run-block-future", "operator cancelled run"); err != nil {
		t.Fatalf("CancelRun: %v", err)
	}

	resp, err := s.AcquireConcurrencySlot(ctx, store.AcquireSlotRequest{
		Key: "future", HolderID: "run-block-future/node", RunID: "run-block-future", NodeID: "node",
		Capacity: 1, Policy: store.OnLimitQueue,
	})
	if err != nil {
		t.Fatalf("AcquireConcurrencySlot: %v", err)
	}
	if resp.Kind != store.AcquireFailed {
		t.Fatalf("acquire after cancel = %s, want %s", resp.Kind, store.AcquireFailed)
	}
	if holderExists(t, s, "future", "run-block-future/node") {
		t.Fatalf("cancelled run acquired a future holder")
	}
}

func TestCancelRun_RetriesConcurrencyCleanupForCancelledRun(t *testing.T) {
	s := newStoreT(t)
	ctx := ctxT(t)

	if err := s.CreateRun(ctx, store.Run{
		ID:        "run-retry-cleanup",
		Pipeline:  "pipeline",
		Status:    "running",
		StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := s.CreateNode(ctx, store.Node{RunID: "run-retry-cleanup", NodeID: "pending-node", Status: "pending"}); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	if resp := acquireBareT(t, s, store.AcquireSlotRequest{
		Key: "stale-holder", HolderID: "run-retry-cleanup/pending-node", RunID: "run-retry-cleanup", NodeID: "pending-node",
		Capacity: 1, Policy: store.OnLimitQueue,
	}); resp.Kind != store.AcquireGranted {
		t.Fatalf("stale holder acquire = %s, want %s", resp.Kind, store.AcquireGranted)
	}
	if err := s.FinishRun(ctx, "run-retry-cleanup", "cancelled", "cancelled before cleanup"); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}

	if err := s.CancelRun(ctx, "run-retry-cleanup", "operator cancelled run"); err != nil {
		t.Fatalf("CancelRun retry: %v", err)
	}

	if holderExists(t, s, "stale-holder", "run-retry-cleanup/pending-node") {
		t.Fatalf("cancel retry left stale holder")
	}
	node, err := s.GetNode(ctx, "run-retry-cleanup", "pending-node")
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if node.Status != "done" || node.Outcome != "cancelled" {
		t.Fatalf("node status/outcome = %q/%q, want done/cancelled", node.Status, node.Outcome)
	}
}

func TestCancelRun_DoesNotOverwriteCompletedRun(t *testing.T) {
	s := newStoreT(t)
	ctx := ctxT(t)

	if err := s.CreateRun(ctx, store.Run{
		ID:        "run-complete-first",
		Pipeline:  "pipeline",
		Status:    "running",
		StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := s.FinishRun(ctx, "run-complete-first", "success", ""); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}
	if err := s.CancelRun(ctx, "run-complete-first", "operator cancelled run"); err == nil {
		t.Fatalf("CancelRun succeeded on completed run")
	}
	run, err := s.GetRun(ctx, "run-complete-first")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.Status != "success" {
		t.Fatalf("run status = %q, want success", run.Status)
	}
}

func TestFinishRun_DoesNotOverwriteCancelledRun(t *testing.T) {
	s := newStoreT(t)
	ctx := ctxT(t)

	if err := s.CreateRun(ctx, store.Run{
		ID:        "run-cancel-first",
		Pipeline:  "pipeline",
		Status:    "running",
		StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := s.CancelRun(ctx, "run-cancel-first", "operator cancelled run"); err != nil {
		t.Fatalf("CancelRun: %v", err)
	}
	if err := s.FinishRun(ctx, "run-cancel-first", "success", ""); err != nil {
		t.Fatalf("FinishRun after cancel: %v", err)
	}
	run, err := s.GetRun(ctx, "run-cancel-first")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.Status != "cancelled" {
		t.Fatalf("run status = %q, want cancelled", run.Status)
	}
}
