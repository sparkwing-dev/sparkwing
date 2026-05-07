package store_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// seedRun inserts a run with the given pipeline + status + started_at,
// optionally finished at a relative offset. Centralizes the boilerplate
// so each test case reads as data, not setup.
func seedRun(t *testing.T, s *store.Store, id, pipeline, status string, startedAgo time.Duration, finishedAgo time.Duration) {
	t.Helper()
	ctx := context.Background()
	started := time.Now().Add(-startedAgo)
	if err := s.CreateRun(ctx, store.Run{
		ID: id, Pipeline: pipeline, Status: "running", StartedAt: started,
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if status != "running" {
		// FinishRun writes finished_at = now, but we want a
		// controllable offset for maxAge testing. Poke the raw DB via
		// the store's query path.
		_, err := s.DB().ExecContext(ctx, `
UPDATE runs SET status = ?, error = '', finished_at = ? WHERE id = ?`,
			status, time.Now().Add(-finishedAgo).UnixNano(), id)
		if err != nil {
			t.Fatalf("set finish: %v", err)
		}
	}
}

// TestGetLatestRun_ReturnsNewest verifies that multiple successful runs
// of the same pipeline resolve to the newest one.
func TestGetLatestRun_ReturnsNewest(t *testing.T) {
	s := openStore(t)
	seedRun(t, s, "old", "build", "success", 2*time.Hour, 90*time.Minute)
	seedRun(t, s, "mid", "build", "success", 1*time.Hour, 45*time.Minute)
	seedRun(t, s, "new", "build", "success", 10*time.Minute, 5*time.Minute)

	got, err := s.GetLatestRun(context.Background(), "build", []string{"success"}, 0)
	if err != nil {
		t.Fatalf("GetLatestRun: %v", err)
	}
	if got.ID != "new" {
		t.Fatalf("latest: got %q, want new", got.ID)
	}
}

// TestGetLatestRun_FiltersStatus proves a failing run doesn't count
// when the caller asks for {"success"}.
func TestGetLatestRun_FiltersStatus(t *testing.T) {
	s := openStore(t)
	seedRun(t, s, "older-success", "deploy", "success", 1*time.Hour, 45*time.Minute)
	seedRun(t, s, "newer-failure", "deploy", "failed", 10*time.Minute, 5*time.Minute)

	got, err := s.GetLatestRun(context.Background(), "deploy", []string{"success"}, 0)
	if err != nil {
		t.Fatalf("GetLatestRun: %v", err)
	}
	if got.ID != "older-success" {
		t.Fatalf("want older-success, got %q", got.ID)
	}
}

// TestGetLatestRun_NotFound returns ErrNotFound when the pipeline has
// no runs at all.
func TestGetLatestRun_NotFound(t *testing.T) {
	s := openStore(t)
	_, err := s.GetLatestRun(context.Background(), "never-ran", []string{"success"}, 0)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

// TestGetLatestRun_MaxAgeRespected proves the freshness bound filters
// stale runs. Seeded run finished 90 minutes ago; a 1h maxAge excludes
// it. A 2h maxAge includes it.
func TestGetLatestRun_MaxAgeRespected(t *testing.T) {
	s := openStore(t)
	seedRun(t, s, "stale", "build", "success", 2*time.Hour, 90*time.Minute)

	_, err := s.GetLatestRun(context.Background(), "build", []string{"success"}, 1*time.Hour)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("1h bound should exclude 90m-old run, got %v", err)
	}
	got, err := s.GetLatestRun(context.Background(), "build", []string{"success"}, 2*time.Hour)
	if err != nil {
		t.Fatalf("2h bound should include 90m-old run: %v", err)
	}
	if got.ID != "stale" {
		t.Fatalf("want stale, got %q", got.ID)
	}
}

// TestGetLatestRun_EmptyStatusesMatchesAny proves that passing nil
// statuses returns the newest regardless of outcome.
func TestGetLatestRun_EmptyStatusesMatchesAny(t *testing.T) {
	s := openStore(t)
	seedRun(t, s, "older-success", "mix", "success", 1*time.Hour, 45*time.Minute)
	seedRun(t, s, "newer-failed", "mix", "failed", 10*time.Minute, 5*time.Minute)

	got, err := s.GetLatestRun(context.Background(), "mix", nil, 0)
	if err != nil {
		t.Fatalf("GetLatestRun: %v", err)
	}
	if got.ID != "newer-failed" {
		t.Fatalf("empty statuses: want newer-failed, got %q", got.ID)
	}
}

// TestGetLatestRun_RejectsEmptyPipeline guards against accidental
// "match anything" queries from callers that forgot to pass a name.
func TestGetLatestRun_RejectsEmptyPipeline(t *testing.T) {
	s := openStore(t)
	if _, err := s.GetLatestRun(context.Background(), "", nil, 0); err == nil {
		t.Fatal("expected error for empty pipeline")
	}
}
