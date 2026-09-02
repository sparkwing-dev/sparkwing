package store_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

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

func TestFinishNodeWithReason_DoesNotOverwriteTerminalNode(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()
	seedRunAndNode(t, s, "run-1", "node-a")

	if err := s.FinishNode(ctx, "run-1", "node-a", "success", "", []byte(`"ok"`)); err != nil {
		t.Fatalf("FinishNode: %v", err)
	}
	code := 137
	if err := s.FinishNodeWithReason(ctx, "run-1", "node-a",
		"failed", "late infrastructure failure", nil, store.FailureOOMKilled, &code); err != nil {
		t.Fatalf("FinishNodeWithReason: %v", err)
	}

	n, err := s.GetNode(ctx, "run-1", "node-a")
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if n.Status != "done" || n.Outcome != "success" {
		t.Fatalf("status/outcome: %q %q, want done/success", n.Status, n.Outcome)
	}
	if n.Error != "" {
		t.Fatalf("error = %q, want empty", n.Error)
	}
	if string(n.Output) != `"ok"` {
		t.Fatalf("output = %s, want original output", n.Output)
	}
	if n.FailureReason != store.FailureUnknown {
		t.Fatalf("failure_reason = %q, want original empty reason", n.FailureReason)
	}
	if n.ExitCode != nil {
		t.Fatalf("exit_code = %v, want nil", *n.ExitCode)
	}
}

func TestFinishNodeWithReason_FinalizesDoneNodeWithEmptyOutcome(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()
	if err := s.CreateRun(ctx, store.Run{
		ID: "run-1", Pipeline: "demo", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := s.CreateNode(ctx, store.Node{RunID: "run-1", NodeID: "node-a", Status: "done"}); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}

	if err := s.FinishNode(ctx, "run-1", "node-a", "success", "", []byte(`"ok"`)); err != nil {
		t.Fatalf("FinishNode: %v", err)
	}
	n, err := s.GetNode(ctx, "run-1", "node-a")
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if n.Status != "done" || n.Outcome != "success" {
		t.Fatalf("status/outcome: %q %q, want done/success", n.Status, n.Outcome)
	}
	if string(n.Output) != `"ok"` {
		t.Fatalf("output = %s, want finalized output", n.Output)
	}
}

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

func TestFailExpiredNodeClaims_TerminatesWithAgentLost(t *testing.T) {
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

	pairs, err := store.Maintenance.FailExpiredNodeClaims(s, ctx)
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

func TestFailStaleQueuedNodes_TerminatesWithQueueTimeout(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()
	seedRunAndNode(t, s, "run-1", "node-a")
	if err := s.MarkNodeReady(ctx, "run-1", "node-a"); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-1 * time.Hour).UnixNano()
	if _, err := s.DB().ExecContext(ctx,
		`UPDATE nodes SET ready_at = ? WHERE run_id = ? AND node_id = ?`,
		past, "run-1", "node-a"); err != nil {
		t.Fatal(err)
	}

	pairs, err := store.Maintenance.FailStaleQueuedNodes(s, ctx, 15*time.Minute)
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
	if _, err := s.ClaimNextReadyNode(ctx, store.ClaimIdentity{Principal: "runner-principal", TokenPrefix: "swr_runner-principal"}, "pod-1", 30*time.Second, nil); err != nil {
		t.Fatal(err)
	}

	pairs, err := store.Maintenance.FailStaleQueuedNodes(s, ctx, 15*time.Minute)
	if err != nil {
		t.Fatalf("FailStaleQueuedNodes: %v", err)
	}
	if len(pairs) != 0 {
		t.Fatalf("expected no terminations, got %v", pairs)
	}
}

var _ sql.NullInt64 = sql.NullInt64{}
