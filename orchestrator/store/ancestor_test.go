package store_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/v2/orchestrator/store"
)

func openAncestorStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// insertChain creates runs in a linear parent chain. Each id's parent
// is the previous entry; the first entry has no parent.
func insertChain(t *testing.T, s *store.Store, entries []struct{ id, pipeline string }) {
	t.Helper()
	ctx := context.Background()
	var parent string
	for _, e := range entries {
		if err := s.CreateRun(ctx, store.Run{
			ID:          e.id,
			Pipeline:    e.pipeline,
			Status:      "success",
			StartedAt:   time.Now(),
			ParentRunID: parent,
		}); err != nil {
			t.Fatalf("CreateRun %s: %v", e.id, err)
		}
		parent = e.id
	}
}

// TestAncestor_EmptyWhenNoParent returns an empty slice when the run
// has no parent chain. Important: no error, no nil-pointer surprise.
func TestAncestor_EmptyWhenNoParent(t *testing.T) {
	s := openAncestorStore(t)
	insertChain(t, s, []struct{ id, pipeline string }{
		{"only", "build"},
	})
	got, err := s.GetRunAncestorPipelines(context.Background(), "only")
	if err != nil {
		t.Fatalf("GetRunAncestorPipelines: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("want empty, got %v", got)
	}
}

// TestAncestor_ReturnsChain walks a 3-deep chain and checks the order
// (parent-first).
func TestAncestor_ReturnsChain(t *testing.T) {
	s := openAncestorStore(t)
	insertChain(t, s, []struct{ id, pipeline string }{
		{"root", "A"},
		{"mid", "B"},
		{"leaf", "C"},
	})
	got, err := s.GetRunAncestorPipelines(context.Background(), "leaf")
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	// Direct parent's pipeline first, then root.
	want := []string{"B", "A"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("got[%d]=%q want %q", i, got[i], want[i])
		}
	}
}

// TestAncestor_TerminatesOnMissingParent: a broken chain (parent_run_id
// points at a deleted run) returns what was reachable without errors.
func TestAncestor_TerminatesOnMissingParent(t *testing.T) {
	s := openAncestorStore(t)
	insertChain(t, s, []struct{ id, pipeline string }{
		{"root", "A"},
		{"mid", "B"},
	})
	// Delete root to break the chain.
	if err := s.DeleteRun(context.Background(), "root"); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetRunAncestorPipelines(context.Background(), "mid")
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	// "mid"'s own pipeline is not returned; the walk hits missing
	// "root" and stops cleanly.
	if len(got) != 0 {
		t.Fatalf("want empty after broken chain, got %v", got)
	}
}

// TestAncestor_RunIDNotFound handles an invalid seed id gracefully
// rather than erroring.
func TestAncestor_RunIDNotFound(t *testing.T) {
	s := openAncestorStore(t)
	got, err := s.GetRunAncestorPipelines(context.Background(), "does-not-exist")
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("want empty, got %v", got)
	}
}
