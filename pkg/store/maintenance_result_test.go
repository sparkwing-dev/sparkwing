package store_test

import (
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestPruneRetainsRecentlyFinishedRun(t *testing.T) {
	s := storetest.New(t).Open(t)
	ctx := t.Context()
	now := time.Now()
	if err := s.CreateRun(ctx, store.Run{ID: "recent", Pipeline: "test", Status: "running", StartedAt: now.Add(-48 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishRun(ctx, "recent", "success", ""); err != nil {
		t.Fatal(err)
	}
	ids, err := s.PruneRunsOlderThan(ctx, now.Add(-24*time.Hour))
	if err != nil || len(ids) != 0 {
		t.Fatalf("pruned recently finished run: %v %v", ids, err)
	}
	if _, err := s.GetRun(ctx, "recent"); err != nil {
		t.Fatal(err)
	}
}

func TestPruneReturnsOnlyDeletedIDsOnFailure(t *testing.T) {
	s := storetest.OpenSQLite(t)
	ctx := t.Context()
	if err := s.CreateRun(ctx, store.Run{ID: "kept", Pipeline: "test", Status: "success", StartedAt: time.Now().Add(-48 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`CREATE TRIGGER deny_run_delete BEFORE DELETE ON runs BEGIN SELECT RAISE(ABORT, 'retained'); END`); err != nil {
		t.Fatal(err)
	}
	ids, err := s.PruneRunsOlderThan(ctx, time.Now().Add(time.Hour))
	if err == nil {
		t.Fatal("expected delete failure")
	}
	if len(ids) != 0 {
		t.Fatalf("reported undeleted ids: %v", ids)
	}
	if _, err := s.GetRun(ctx, "kept"); err != nil {
		t.Fatal(err)
	}
}

func TestDuplicateTriggerIDIsNotIdempotencyConflict(t *testing.T) {
	for _, key := range []string{"", "new-key"} {
		t.Run(key, func(t *testing.T) {
			s := storetest.New(t).Open(t)
			first := store.Trigger{ID: "same-id", Pipeline: "test", CreatedAt: time.Now()}
			if err := s.CreateTrigger(t.Context(), first); err != nil {
				t.Fatal(err)
			}
			first.IdempotencyKey = key
			err := s.CreateTrigger(t.Context(), first)
			if err == nil || errors.Is(err, store.ErrDuplicateIdempotencyKey) {
				t.Fatalf("duplicate primary id misclassified: %v", err)
			}
		})
	}
}
