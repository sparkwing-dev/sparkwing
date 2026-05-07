package local_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/controller/client"
	controller "github.com/sparkwing-dev/sparkwing/internal/local"
	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
)

// seedFinishedRun inserts a run in a terminal state with a specific
// finished_at. Centralized so tests read as data, not setup.
func seedFinishedRun(t *testing.T, st *store.Store, id, pipeline, status string, startedAgo, finishedAgo time.Duration) {
	t.Helper()
	ctx := context.Background()
	if err := st.CreateRun(ctx, store.Run{
		ID: id, Pipeline: pipeline, Status: "running",
		StartedAt: time.Now().Add(-startedAgo),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	_, err := st.DB().ExecContext(ctx,
		`UPDATE runs SET status = ?, finished_at = ? WHERE id = ?`,
		status, time.Now().Add(-finishedAgo).UnixNano(), id)
	if err != nil {
		t.Fatalf("update run: %v", err)
	}
}

// TestPipelineLatest_HTTPRoundTrip exercises the full read path:
// seed, query via Go client, decode.
func TestPipelineLatest_HTTPRoundTrip(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	seedFinishedRun(t, st, "older", "build", "success", 1*time.Hour, 45*time.Minute)
	seedFinishedRun(t, st, "newer", "build", "success", 10*time.Minute, 5*time.Minute)
	seedFinishedRun(t, st, "other-pipeline", "deploy", "success", 10*time.Minute, 5*time.Minute)

	srv := httptest.NewServer(controller.New(st, nil).Handler())
	defer srv.Close()
	c := client.New(srv.URL, nil)

	got, err := c.GetLatestRun(context.Background(), "build", []string{"success"}, 0)
	if err != nil {
		t.Fatalf("GetLatestRun: %v", err)
	}
	if got.ID != "newer" {
		t.Fatalf("want newer, got %q", got.ID)
	}
}

// TestPipelineLatest_NotFound404 proves missing data surfaces as
// ErrNotFound through the client so SDK callers can switch on it.
func TestPipelineLatest_NotFound404(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	srv := httptest.NewServer(controller.New(st, nil).Handler())
	defer srv.Close()
	c := client.New(srv.URL, nil)

	_, err = c.GetLatestRun(context.Background(), "never-ran", []string{"success"}, 0)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

// TestPipelineLatest_MaxAgeFilter verifies the max_age query param
// is plumbed through from client to store.
func TestPipelineLatest_MaxAgeFilter(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	seedFinishedRun(t, st, "stale", "build", "success", 2*time.Hour, 90*time.Minute)

	srv := httptest.NewServer(controller.New(st, nil).Handler())
	defer srv.Close()
	c := client.New(srv.URL, nil)

	_, err = c.GetLatestRun(context.Background(), "build", []string{"success"}, 1*time.Hour)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("1h bound should exclude 90m-old run, got %v", err)
	}
	got, err := c.GetLatestRun(context.Background(), "build", []string{"success"}, 2*time.Hour)
	if err != nil {
		t.Fatalf("2h bound should include 90m-old run: %v", err)
	}
	if got.ID != "stale" {
		t.Fatalf("want stale, got %q", got.ID)
	}
}
