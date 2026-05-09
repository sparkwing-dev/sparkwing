package orchestrator

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
)

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// TestReconcileOrphanedLocalRuns_StaleRunFlipsToFailed verifies the
// core promise: a "running" run whose orchestrator process is dead
// (no heartbeat past the threshold, started long ago) gets
// transitioned to "failed" with an "orphaned" reason. This is what
// closes the "dashboard says still running but it isn't" gap that
// motivated the helper.
func TestReconcileOrphanedLocalRuns_StaleRunFlipsToFailed(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	old := time.Now().Add(-10 * time.Minute)
	if err := st.CreateRun(ctx, store.Run{
		ID: "run-stale", Pipeline: "p", Status: "running", StartedAt: old,
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := st.CreateNode(ctx, store.Node{
		RunID: "run-stale", NodeID: "build", Status: "running",
	}); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}

	n, err := reconcileOrphanedLocalRuns(ctx, st, 60*time.Second)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if n != 1 {
		t.Errorf("reconciled count: got %d, want 1", n)
	}

	got, err := st.GetRun(ctx, "run-stale")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.Status != "failed" {
		t.Errorf("run status: got %q, want failed", got.Status)
	}
	if got.Error == "" {
		t.Errorf("run error: expected non-empty 'orphaned' message")
	}
	nodes, err := st.ListNodes(ctx, "run-stale")
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	if nodes[0].Outcome != "failed" {
		t.Errorf("node outcome: got %q, want failed", nodes[0].Outcome)
	}
	if nodes[0].FailureReason != "orphaned" {
		t.Errorf("node failure_reason: got %q, want orphaned", nodes[0].FailureReason)
	}
}

// TestReconcileOrphanedLocalRuns_FreshRunUntouched guards against the
// false-positive case: a "running" run that started a moment ago has
// no heartbeat YET (the orchestrator hasn't had time to stamp one),
// but it shouldn't get marked failed -- the cutoff filter sees
// started_at after the threshold and skips it.
func TestReconcileOrphanedLocalRuns_FreshRunUntouched(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	if err := st.CreateRun(ctx, store.Run{
		ID: "run-fresh", Pipeline: "p", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	n, err := reconcileOrphanedLocalRuns(ctx, st, 60*time.Second)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if n != 0 {
		t.Errorf("reconciled count: got %d, want 0", n)
	}

	got, err := st.GetRun(ctx, "run-fresh")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.Status != "running" {
		t.Errorf("run status: got %q, want running", got.Status)
	}
}

// TestReconcileOrphanedLocalRuns_RecentHeartbeatUntouched: a run
// started long ago whose nodes are still heartbeating should NOT be
// reconciled. The MAX(last_heartbeat) clause covers this case.
func TestReconcileOrphanedLocalRuns_RecentHeartbeatUntouched(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	old := time.Now().Add(-10 * time.Minute)
	if err := st.CreateRun(ctx, store.Run{
		ID: "run-live", Pipeline: "p", Status: "running", StartedAt: old,
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := st.CreateNode(ctx, store.Node{
		RunID: "run-live", NodeID: "build", Status: "running",
	}); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	// Stamp a current heartbeat so the run looks live.
	if _, err := st.DB().ExecContext(ctx,
		`UPDATE nodes SET last_heartbeat = ? WHERE run_id = ?`,
		time.Now().UnixNano(), "run-live"); err != nil {
		t.Fatalf("stamp heartbeat: %v", err)
	}

	n, err := reconcileOrphanedLocalRuns(ctx, st, 60*time.Second)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if n != 0 {
		t.Errorf("reconciled count: got %d, want 0", n)
	}
}
