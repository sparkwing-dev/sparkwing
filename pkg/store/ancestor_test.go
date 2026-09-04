package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/storetest"
)

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

func TestAncestor_EmptyWhenNoParent(t *testing.T) {
	s := storetest.Open(t)
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

func TestAncestor_ReturnsChain(t *testing.T) {
	s := storetest.Open(t)
	insertChain(t, s, []struct{ id, pipeline string }{
		{"root", "A"},
		{"mid", "B"},
		{"leaf", "C"},
	})
	got, err := s.GetRunAncestorPipelines(context.Background(), "leaf")
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
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

func TestAncestor_TerminatesOnMissingParent(t *testing.T) {
	s := storetest.Open(t)
	insertChain(t, s, []struct{ id, pipeline string }{
		{"root", "A"},
		{"mid", "B"},
	})
	if err := s.DeleteRun(context.Background(), "root"); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetRunAncestorPipelines(context.Background(), "mid")
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("want empty after broken chain, got %v", got)
	}
}

func TestAncestor_RunIDNotFound(t *testing.T) {
	s := storetest.Open(t)
	got, err := s.GetRunAncestorPipelines(context.Background(), "does-not-exist")
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("want empty, got %v", got)
	}
}
