package store_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestFinishNodeWithReason_PersistsStructuredMetadata(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	seedRunAndNode(t, s, "run-1", "node-a")

	code := 137
	if err := s.FinishNodeWithReason(ctx, "run-1", "node-a",
		"failed", "pod OOMKilled", store.FailureOOMKilled, &code); err != nil {
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
	s := storetest.Open(t)
	ctx := context.Background()
	seedRunAndNode(t, s, "run-1", "node-a")

	if err := s.FinishNode(ctx, "run-1", "node-a", "success", ""); err != nil {
		t.Fatalf("FinishNode: %v", err)
	}
	code := 137
	if err := s.FinishNodeWithReason(ctx, "run-1", "node-a",
		"failed", "late infrastructure failure", store.FailureOOMKilled, &code); err != nil {
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

	if n.FailureReason != store.FailureUnknown {
		t.Fatalf("failure_reason = %q, want original empty reason", n.FailureReason)
	}
	if n.ExitCode != nil {
		t.Fatalf("exit_code = %v, want nil", *n.ExitCode)
	}
}

func TestFinishNodeWithReason_FinalizesDoneNodeWithEmptyOutcome(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	if err := s.CreateRun(ctx, store.Run{
		ID: "run-1", Pipeline: "demo", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := s.CreateNode(ctx, store.Node{RunID: "run-1", NodeID: "node-a", Status: "done"}); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}

	if err := s.FinishNode(ctx, "run-1", "node-a", "success", ""); err != nil {
		t.Fatalf("FinishNode: %v", err)
	}
	n, err := s.GetNode(ctx, "run-1", "node-a")
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if n.Status != "done" || n.Outcome != "success" {
		t.Fatalf("status/outcome: %q %q, want done/success", n.Status, n.Outcome)
	}

}

func TestFinishNode_LeavesReasonEmpty(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	seedRunAndNode(t, s, "run-1", "node-a")

	if err := s.FinishNode(ctx, "run-1", "node-a", "success", ""); err != nil {
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

var _ sql.NullInt64 = sql.NullInt64{}
