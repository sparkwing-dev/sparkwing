package store_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
)

// TestFinishNodeWithReason_PersistsStructuredMetadata verifies that
// the reason + exit code make it onto the node row and round-trip
// through GetNode.
func TestFinishNodeWithReason_PersistsStructuredMetadata(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()
	seedRunAndNode(t, s, "run-1", "node-a")

	code := 137
	if err := s.FinishNodeWithReason(ctx, "run-1", "node-a",
		"failed", "pod OOMKilled", nil, store.FailureOOMKilled, &code); err != nil {
		t.Fatalf("FinishNodeWithReason: %v", err)
	}

	n, err := s.GetNode(ctx, "run-1", "node-a")
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if n.FailureReason != store.FailureOOMKilled {
		t.Fatalf("failure_reason: %q", n.FailureReason)
	}
	if n.ExitCode == nil || *n.ExitCode != 137 {
		t.Fatalf("exit_code: %v", n.ExitCode)
	}
	if n.Outcome != "failed" || n.Status != "done" {
		t.Fatalf("outcome/status: %q %q", n.Outcome, n.Status)
	}
}

// TestFinishNode_LeavesReasonEmpty is the backwards-compat check:
// callers that never opt into the new signature get the same row
// shape as before (empty reason, NULL exit_code).
func TestFinishNode_LeavesReasonEmpty(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()
	seedRunAndNode(t, s, "run-1", "node-a")

	if err := s.FinishNode(ctx, "run-1", "node-a", "success", "", []byte(`"ok"`)); err != nil {
		t.Fatalf("FinishNode: %v", err)
	}
	n, _ := s.GetNode(ctx, "run-1", "node-a")
	if n.FailureReason != store.FailureUnknown {
		t.Fatalf("expected empty failure_reason, got %q", n.FailureReason)
	}
	if n.ExitCode != nil {
		t.Fatalf("expected nil exit_code, got %v", *n.ExitCode)
	}
}

// TestFailExpiredNodeClaims_TerminatesWithAgentLost registers a
// claim, lets the lease elapse, and asserts the reaper flips the
// node to failed with reason=agent_lost instead of simply clearing
// the claim.
func TestFailExpiredNodeClaims_TerminatesWithAgentLost(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()
	seedRunAndNode(t, s, "run-1", "node-a")
	if err := s.MarkNodeReady(ctx, "run-1", "node-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimNextReadyNode(ctx, "pod-dead", 1*time.Millisecond, nil); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)

	pairs, err := s.FailExpiredNodeClaims(ctx)
	if err != nil {
		t.Fatalf("FailExpiredNodeClaims: %v", err)
	}
	if len(pairs) != 1 || pairs[0] != [2]string{"run-1", "node-a"} {
		t.Fatalf("unexpected pairs: %v", pairs)
	}

	n, _ := s.GetNode(ctx, "run-1", "node-a")
	if n.Status != "done" || n.Outcome != "failed" {
		t.Fatalf("expected terminal failed; got status=%q outcome=%q", n.Status, n.Outcome)
	}
	if n.FailureReason != store.FailureAgentLost {
		t.Fatalf("expected agent_lost, got %q", n.FailureReason)
	}
	if n.ClaimedBy != "" || n.LeaseExpiresAt != nil {
		t.Fatalf("claim not cleared after termination: claimed_by=%q lease=%v", n.ClaimedBy, n.LeaseExpiresAt)
	}
}

// TestFailStaleQueuedNodes_TerminatesWithQueueTimeout inserts a
// ready-but-unclaimed node whose ready_at is older than the
// threshold, runs the sweep, and asserts queue_timeout.
func TestFailStaleQueuedNodes_TerminatesWithQueueTimeout(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()
	seedRunAndNode(t, s, "run-1", "node-a")
	if err := s.MarkNodeReady(ctx, "run-1", "node-a"); err != nil {
		t.Fatal(err)
	}
	// Back-date ready_at so the sweep sees a stale entry. The column
	// is INTEGER unix-nanos; write directly since MarkNodeReady
	// stamps "now".
	past := time.Now().Add(-1 * time.Hour).UnixNano()
	if _, err := s.DB().ExecContext(ctx,
		`UPDATE nodes SET ready_at = ? WHERE run_id = ? AND node_id = ?`,
		past, "run-1", "node-a"); err != nil {
		t.Fatal(err)
	}

	pairs, err := s.FailStaleQueuedNodes(ctx, 15*time.Minute)
	if err != nil {
		t.Fatalf("FailStaleQueuedNodes: %v", err)
	}
	if len(pairs) != 1 || pairs[0] != [2]string{"run-1", "node-a"} {
		t.Fatalf("unexpected pairs: %v", pairs)
	}
	n, _ := s.GetNode(ctx, "run-1", "node-a")
	if n.FailureReason != store.FailureQueueTimeout {
		t.Fatalf("expected queue_timeout, got %q", n.FailureReason)
	}
	if n.Outcome != "failed" || n.Status != "done" {
		t.Fatalf("expected terminal failed; got status=%q outcome=%q", n.Status, n.Outcome)
	}
	if n.ReadyAt != nil {
		t.Fatalf("ready_at should be cleared: %v", *n.ReadyAt)
	}
}

// TestFailStaleQueuedNodes_SkipsClaimedAndFresh ensures the sweep
// leaves alone nodes that are either already claimed (a runner has
// picked them up) or whose ready_at is fresher than the threshold.
func TestFailStaleQueuedNodes_SkipsClaimedAndFresh(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()
	if err := s.CreateRun(ctx, store.Run{
		ID: "run-1", Pipeline: "demo", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateNode(ctx, store.Node{RunID: "run-1", NodeID: "fresh", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateNode(ctx, store.Node{RunID: "run-1", NodeID: "claimed", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkNodeReady(ctx, "run-1", "fresh"); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkNodeReady(ctx, "run-1", "claimed"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimNextReadyNode(ctx, "pod-1", 30*time.Second, nil); err != nil {
		t.Fatal(err)
	}

	pairs, err := s.FailStaleQueuedNodes(ctx, 15*time.Minute)
	if err != nil {
		t.Fatalf("FailStaleQueuedNodes: %v", err)
	}
	if len(pairs) != 0 {
		t.Fatalf("expected no terminations, got %v", pairs)
	}
}

// compile-time guard: make sure the nil-exit-code path doesn't
// accidentally write 0 as a concrete NULL vs INTEGER mismatch.
var _ sql.NullInt64 = sql.NullInt64{}
