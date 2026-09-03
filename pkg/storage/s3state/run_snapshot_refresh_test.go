package s3state_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/storage/s3state"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// A parent run polls for a child another process creates, which is the
// shape of the orchestrator's await-pipeline wait loop.
func TestBackend_GetRunSeesARunCreatedAfterAFirstReadFoundNothing(t *testing.T) {
	art := newMemArt()
	parent := s3state.New(art, s3state.WithFlushInterval(time.Hour))
	t.Cleanup(func() { _ = parent.Close() })
	ctx := context.Background()

	if _, err := parent.GetRun(ctx, "child-1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetRun before the child exists = %v, want ErrNotFound", err)
	}

	child := s3state.New(art, s3state.WithFlushInterval(time.Hour))
	if err := child.CreateRun(ctx, runningRun("child-1")); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := child.FinishRun(ctx, "child-1", "success", ""); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}
	if err := child.Close(); err != nil {
		t.Fatalf("child Close: %v", err)
	}

	got, err := parent.GetRun(ctx, "child-1")
	if err != nil {
		t.Fatalf("GetRun after another process finished the child: %v", err)
	}
	if got.Status != "success" {
		t.Errorf("status = %q, want success", got.Status)
	}
}

func TestBackend_GetRunRefreshesAnotherProcessesRunAfterTheReadTTL(t *testing.T) {
	art := newMemArt()
	ctx := context.Background()

	writer := s3state.New(art, s3state.WithFlushInterval(time.Hour))
	if err := writer.CreateRun(ctx, runningRun("r")); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := writer.FinishRun(ctx, "r", "running", ""); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}

	reader := s3state.New(art,
		s3state.WithFlushInterval(time.Hour),
		s3state.WithReadCacheTTL(time.Millisecond),
	)
	t.Cleanup(func() { _ = reader.Close() })
	got, err := reader.GetRun(ctx, "r")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.Status != "running" {
		t.Fatalf("status = %q, want running", got.Status)
	}

	if err := writer.FinishRun(ctx, "r", "success", ""); err != nil {
		t.Fatalf("FinishRun terminal: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("writer Close: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		got, err = reader.GetRun(ctx, "r")
		if err != nil {
			t.Fatalf("GetRun after the writer finished the run: %v", err)
		}
		if got.Status == "success" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("status = %q after the read TTL elapsed, want success", got.Status)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestBackend_GetLatestRunDoesNotPinTheRunsItScans(t *testing.T) {
	art := newMemArt()
	ctx := context.Background()

	seed := s3state.New(art, s3state.WithFlushInterval(time.Hour))
	for _, id := range []string{"r1", "r2"} {
		if err := seed.CreateRun(ctx, runningRun(id)); err != nil {
			t.Fatalf("CreateRun %s: %v", id, err)
		}
		if err := seed.FinishRun(ctx, id, "success", ""); err != nil {
			t.Fatalf("FinishRun %s: %v", id, err)
		}
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("seed Close: %v", err)
	}

	// A TTL long enough that only a scan that retains nothing lets the
	// later read below observe the other process's write.
	reader := s3state.New(art,
		s3state.WithFlushInterval(time.Hour),
		s3state.WithReadCacheTTL(time.Hour),
	)
	t.Cleanup(func() { _ = reader.Close() })
	if _, err := reader.GetLatestRun(ctx, "p", []string{"success"}, time.Hour); err != nil {
		t.Fatalf("GetLatestRun: %v", err)
	}

	other := s3state.New(art, s3state.WithFlushInterval(time.Hour))
	if err := other.FinishRun(ctx, "r1", "failed", "boom"); err != nil {
		t.Fatalf("FinishRun from another process: %v", err)
	}
	if err := other.Close(); err != nil {
		t.Fatalf("other Close: %v", err)
	}

	got, err := reader.GetRun(ctx, "r1")
	if err != nil {
		t.Fatalf("GetRun after the scan: %v", err)
	}
	if got.Status != "failed" {
		t.Errorf("status = %q, want failed; the scan retained the run it read", got.Status)
	}
}
