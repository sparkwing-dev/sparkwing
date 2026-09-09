package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func localStart(ordinal int) store.ExecutionStart {
	return store.ExecutionStart{
		AttemptOrdinal: ordinal,
		ExecutorKind:   store.ExecutorKindLocal,
		ExecutorID:     "workstation-1",
	}
}

func localFinish(ordinal int, outcome, reason string) store.ExecutionAttemptFinish {
	return store.ExecutionAttemptFinish{
		AttemptOrdinal: ordinal,
		Outcome:        outcome,
		FailureReason:  reason,
		ExecutorKind:   store.ExecutorKindLocal,
	}
}

func TestLocalExecutionAttempt_RecordsHostAndOutcome(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	seedRunAndNode(t, s, "run-1", "build")

	if err := s.AcknowledgeNodeExecutionStart(ctx, "run-1", "build", store.ClaimIdentity{}, localStart(1)); err != nil {
		t.Fatalf("AcknowledgeNodeExecutionStart: %v", err)
	}
	if err := s.FinishNodeExecutionAttempt(ctx, "run-1", "build", store.ClaimIdentity{},
		localFinish(1, "success", "")); err != nil {
		t.Fatalf("FinishNodeExecutionAttempt: %v", err)
	}

	attempts, err := s.ListNodeExecutionAttempts(ctx, "run-1", "build")
	if err != nil {
		t.Fatalf("ListNodeExecutionAttempts: %v", err)
	}
	if len(attempts) != 1 {
		t.Fatalf("attempts = %d, want 1", len(attempts))
	}
	got := attempts[0]
	if got.Attempt != 1 || got.ExecutorKind != store.ExecutorKindLocal ||
		got.ExecutorName != "workstation-1" || got.ExecutorID != "workstation-1" ||
		got.ExecutorLocation != "local" {
		t.Fatalf("attempt attribution = %+v, want attempt 1 on local/workstation-1", got)
	}
	if got.Outcome != "success" || got.FinishedAt == nil {
		t.Fatalf("attempt outcome = %q finished = %v, want a closed success", got.Outcome, got.FinishedAt)
	}
	node, err := s.GetNode(ctx, "run-1", "build")
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if node.AttemptsConsumed != 0 {
		t.Fatalf("attempts_consumed = %d, want 0; a local attempt spends no retry budget", node.AttemptsConsumed)
	}
	if node.ExecutionStartedAt == nil {
		t.Fatal("execution_started_at was not stamped by the local attempt")
	}
}

func TestLocalExecutionAttempt_SequencesRetriedNode(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	seedRunAndNode(t, s, "run-1", "build")

	if err := s.AcknowledgeNodeExecutionStart(ctx, "run-1", "build", store.ClaimIdentity{}, localStart(1)); err != nil {
		t.Fatalf("open attempt 1: %v", err)
	}
	if err := s.FinishNodeExecutionAttempt(ctx, "run-1", "build", store.ClaimIdentity{},
		localFinish(1, "failed", store.FailureUnknown)); err != nil {
		t.Fatalf("close attempt 1: %v", err)
	}
	if err := s.AcknowledgeNodeExecutionStart(ctx, "run-1", "build", store.ClaimIdentity{}, localStart(2)); err != nil {
		t.Fatalf("open attempt 2: %v", err)
	}
	if err := s.FinishNodeExecutionAttempt(ctx, "run-1", "build", store.ClaimIdentity{},
		localFinish(2, "success", "")); err != nil {
		t.Fatalf("close attempt 2: %v", err)
	}

	attempts, err := s.ListNodeExecutionAttempts(ctx, "run-1", "build")
	if err != nil {
		t.Fatalf("ListNodeExecutionAttempts: %v", err)
	}
	if len(attempts) != 2 || attempts[0].Attempt != 1 || attempts[1].Attempt != 2 {
		t.Fatalf("attempt ordinals = %+v, want 1 then 2", attempts)
	}
	if attempts[0].Outcome != "failed" || attempts[1].Outcome != "success" {
		t.Fatalf("attempt outcomes = %q, %q, want failed then success",
			attempts[0].Outcome, attempts[1].Outcome)
	}

	// safety: an ordinal numbered against a part-spent retry budget is
	// accepted when it comes after every recorded attempt, never behind one.
	if err := s.AcknowledgeNodeExecutionStart(ctx, "run-1", "build", store.ClaimIdentity{},
		localStart(5)); err != nil {
		t.Fatalf("open attempt 5 after a part-spent budget: %v", err)
	}
	other := localStart(2)
	other.ExecutorID = "workstation-2"
	if err := s.AcknowledgeNodeExecutionStart(ctx, "run-1", "build", store.ClaimIdentity{},
		other); !errors.Is(err, store.ErrLockHeld) {
		t.Fatalf("another host reopening a recorded attempt: err = %v, want ErrLockHeld", err)
	}
}

func TestLocalExecutionAttempt_ReopeningTheSameAttemptIsANoOp(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	seedRunAndNode(t, s, "run-1", "build")

	if err := s.AcknowledgeNodeExecutionStart(ctx, "run-1", "build", store.ClaimIdentity{}, localStart(1)); err != nil {
		t.Fatalf("open attempt 1: %v", err)
	}
	if err := s.AcknowledgeNodeExecutionStart(ctx, "run-1", "build", store.ClaimIdentity{}, localStart(1)); err != nil {
		t.Fatalf("reopen attempt 1: %v", err)
	}
	if err := s.FinishNodeExecutionAttempt(ctx, "run-1", "build", store.ClaimIdentity{},
		localFinish(1, "success", "")); err != nil {
		t.Fatalf("close attempt 1: %v", err)
	}
	if err := s.FinishNodeExecutionAttempt(ctx, "run-1", "build", store.ClaimIdentity{},
		localFinish(1, "success", "")); err != nil {
		t.Fatalf("reclose attempt 1 with the same verdict: %v", err)
	}
	if err := s.FinishNodeExecutionAttempt(ctx, "run-1", "build", store.ClaimIdentity{},
		localFinish(1, "failed", store.FailureUnknown)); !errors.Is(err, store.ErrLockHeld) {
		t.Fatalf("reclose attempt 1 with another verdict: err = %v, want ErrLockHeld", err)
	}

	attempts, err := s.ListNodeExecutionAttempts(ctx, "run-1", "build")
	if err != nil {
		t.Fatalf("ListNodeExecutionAttempts: %v", err)
	}
	if len(attempts) != 1 || attempts[0].Outcome != "success" {
		t.Fatalf("attempts = %+v, want one success", attempts)
	}
}

func TestLocalExecutionAttempt_RefusesAClaimedNode(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	seedRunAndNode(t, s, "run-1", "build")
	setNodeReadyAt(t, s, "run-1", "build", time.Now())
	if _, err := s.ClaimNextReadyNode(ctx, store.ClaimIdentity{}, "agent:one", time.Minute, nil); err != nil {
		t.Fatalf("ClaimNextReadyNode: %v", err)
	}

	if err := s.AcknowledgeNodeExecutionStart(ctx, "run-1", "build", store.ClaimIdentity{},
		localStart(1)); !errors.Is(err, store.ErrLockHeld) {
		t.Fatalf("local start on a claimed node: err = %v, want ErrLockHeld", err)
	}
}

func TestLocalExecutionAttempt_RefusesAnUnnamedExecutor(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	seedRunAndNode(t, s, "run-1", "build")

	start := localStart(1)
	start.ExecutorID = ""
	if err := s.AcknowledgeNodeExecutionStart(ctx, "run-1", "build", store.ClaimIdentity{},
		start); !errors.Is(err, store.ErrLockHeld) {
		t.Fatalf("local start with no executor id: err = %v, want ErrLockHeld", err)
	}
}
