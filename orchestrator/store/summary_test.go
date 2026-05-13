package store_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
)

func TestSetNodeSummary_OverwritesLatestValue(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()

	mustCreateRunWithNode(t, s, "run-1", "node-a")

	if err := s.SetNodeSummary(ctx, "run-1", "node-a", "## first"); err != nil {
		t.Fatalf("SetNodeSummary first: %v", err)
	}
	if err := s.SetNodeSummary(ctx, "run-1", "node-a", "## second"); err != nil {
		t.Fatalf("SetNodeSummary second: %v", err)
	}

	got, err := s.GetNode(ctx, "run-1", "node-a")
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if got.Summary != "## second" {
		t.Fatalf("Summary = %q, want %q", got.Summary, "## second")
	}
}

func TestSetNodeSummary_IdempotentRepeat(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	mustCreateRunWithNode(t, s, "run-1", "node-a")

	const md = "## ready"
	for range 3 {
		if err := s.SetNodeSummary(ctx, "run-1", "node-a", md); err != nil {
			t.Fatalf("SetNodeSummary: %v", err)
		}
	}
	got, err := s.GetNode(ctx, "run-1", "node-a")
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if got.Summary != md {
		t.Fatalf("Summary = %q, want %q", got.Summary, md)
	}
}

func TestSetNodeSummary_MissingNodeReturnsNotFound(t *testing.T) {
	s := openStore(t)
	err := s.SetNodeSummary(context.Background(), "run-x", "node-x", "## hi")
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestSetNodeSummary_SurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.db")
	ctx := context.Background()

	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	if err := s.CreateRun(ctx, store.Run{ID: "run-1", Pipeline: "p", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := s.CreateNode(ctx, store.Node{RunID: "run-1", NodeID: "node-a", Status: "pending"}); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	if err := s.SetNodeSummary(ctx, "run-1", "node-a", "## persisted"); err != nil {
		t.Fatalf("SetNodeSummary: %v", err)
	}
	_ = s.Close()

	s2, err := store.Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()

	got, err := s2.GetNode(ctx, "run-1", "node-a")
	if err != nil {
		t.Fatalf("GetNode after reopen: %v", err)
	}
	if got.Summary != "## persisted" {
		t.Fatalf("Summary = %q, want %q", got.Summary, "## persisted")
	}
}

func TestSetStepSummary_OverwritesLatestValue(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	mustCreateRunWithNode(t, s, "run-1", "node-a")
	if err := s.StartNodeStep(ctx, "run-1", "node-a", "step-x"); err != nil {
		t.Fatalf("StartNodeStep: %v", err)
	}

	if err := s.SetStepSummary(ctx, "run-1", "node-a", "step-x", "## first"); err != nil {
		t.Fatalf("SetStepSummary first: %v", err)
	}
	if err := s.SetStepSummary(ctx, "run-1", "node-a", "step-x", "## second"); err != nil {
		t.Fatalf("SetStepSummary second: %v", err)
	}

	steps, err := s.ListNodeSteps(ctx, "run-1")
	if err != nil {
		t.Fatalf("ListNodeSteps: %v", err)
	}
	if len(steps) != 1 {
		t.Fatalf("got %d steps, want 1", len(steps))
	}
	if steps[0].Summary != "## second" {
		t.Fatalf("Summary = %q, want %q", steps[0].Summary, "## second")
	}
}

func TestSetStepSummary_InsertsPlaceholderRowBeforeStart(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	mustCreateRunWithNode(t, s, "run-1", "node-a")

	if err := s.SetStepSummary(ctx, "run-1", "node-a", "step-x", "## early"); err != nil {
		t.Fatalf("SetStepSummary: %v", err)
	}
	steps, err := s.ListNodeSteps(ctx, "run-1")
	if err != nil {
		t.Fatalf("ListNodeSteps: %v", err)
	}
	if len(steps) != 1 {
		t.Fatalf("got %d steps, want 1", len(steps))
	}
	if steps[0].StepID != "step-x" {
		t.Fatalf("StepID = %q, want step-x", steps[0].StepID)
	}
	if steps[0].Summary != "## early" {
		t.Fatalf("Summary = %q, want %q", steps[0].Summary, "## early")
	}
}

func TestSetStepSummary_IdempotentRepeat(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	mustCreateRunWithNode(t, s, "run-1", "node-a")
	if err := s.StartNodeStep(ctx, "run-1", "node-a", "step-x"); err != nil {
		t.Fatalf("StartNodeStep: %v", err)
	}
	const md = "## stable"
	for range 3 {
		if err := s.SetStepSummary(ctx, "run-1", "node-a", "step-x", md); err != nil {
			t.Fatalf("SetStepSummary: %v", err)
		}
	}
	steps, err := s.ListNodeSteps(ctx, "run-1")
	if err != nil {
		t.Fatalf("ListNodeSteps: %v", err)
	}
	if len(steps) != 1 || steps[0].Summary != md {
		t.Fatalf("steps = %+v, want one row with Summary=%q", steps, md)
	}
}

func TestSetStepSummary_SurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.db")
	ctx := context.Background()

	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	if err := s.CreateRun(ctx, store.Run{ID: "run-1", Pipeline: "p", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := s.CreateNode(ctx, store.Node{RunID: "run-1", NodeID: "node-a", Status: "pending"}); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	if err := s.StartNodeStep(ctx, "run-1", "node-a", "step-x"); err != nil {
		t.Fatalf("StartNodeStep: %v", err)
	}
	if err := s.SetStepSummary(ctx, "run-1", "node-a", "step-x", "## kept"); err != nil {
		t.Fatalf("SetStepSummary: %v", err)
	}
	_ = s.Close()

	s2, err := store.Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()

	steps, err := s2.ListNodeSteps(ctx, "run-1")
	if err != nil {
		t.Fatalf("ListNodeSteps after reopen: %v", err)
	}
	if len(steps) != 1 || steps[0].Summary != "## kept" {
		t.Fatalf("steps = %+v, want one row with Summary=%q", steps, "## kept")
	}
}

func mustCreateRunWithNode(t *testing.T, s *store.Store, runID, nodeID string) {
	t.Helper()
	ctx := context.Background()
	if err := s.CreateRun(ctx, store.Run{ID: runID, Pipeline: "p", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := s.CreateNode(ctx, store.Node{RunID: runID, NodeID: nodeID, Status: "pending"}); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
}
