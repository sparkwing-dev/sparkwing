package s3state_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/storage/s3state"
)

func outageBackend(t *testing.T, art *memArt) (*s3state.Backend, *s3state.Outbox) {
	t.Helper()
	outbox, err := s3state.OpenOutbox(filepath.Join(t.TempDir(), "outbox.db"), art, time.Hour)
	if err != nil {
		t.Fatalf("OpenOutbox: %v", err)
	}
	b := s3state.New(art,
		s3state.WithOutbox(outbox),
		s3state.WithFlushInterval(time.Hour),
		s3state.WithBufferThreshold(1),
	)
	return b, outbox
}

func TestBackend_FinishRunFailsWhenTheTerminalStateOnlyReachedTheOutbox(t *testing.T) {
	art := newMemArt()
	b, _ := outageBackend(t, art)
	t.Cleanup(func() { _ = b.Close() })
	ctx := context.Background()

	art.setPutErr(errors.New("connection refused"))
	if err := b.CreateRun(ctx, runningRun("r")); err != nil {
		t.Fatalf("CreateRun during the outage: %v", err)
	}

	err := b.FinishRun(ctx, "r", "success", "")
	if err == nil {
		t.Fatal("FinishRun reported success with the run's terminal state only in the local outbox")
	}
	if !strings.Contains(err.Error(), "outbox") {
		t.Errorf("FinishRun error = %v, want it to name the local outbox", err)
	}
	if ok, _ := art.Has(ctx, "runs/r/state.ndjson"); ok {
		t.Fatal("state reached the store during the simulated outage")
	}

	art.setPutErr(nil)
	if err := b.FinishRun(ctx, "r", "success", ""); err != nil {
		t.Fatalf("FinishRun after the store recovered: %v", err)
	}
	if ok, _ := art.Has(ctx, "runs/r/state.ndjson"); !ok {
		t.Fatal("FinishRun returned nil with nothing in the store")
	}
}

func TestBackend_CloseReportsStateLeftInTheLocalOutbox(t *testing.T) {
	art := newMemArt()
	b, outbox := outageBackend(t, art)
	t.Cleanup(func() { _ = outbox.Close() })
	ctx := context.Background()

	art.setPutErr(errors.New("connection refused"))
	if err := b.CreateRun(ctx, runningRun("r")); err != nil {
		t.Fatalf("CreateRun during the outage: %v", err)
	}
	if n, _ := outbox.Pending(ctx); n == 0 {
		t.Fatal("write did not stage to the outbox during the outage")
	}

	if err := b.Close(); err == nil {
		t.Fatal("Close reported success with the run's state only in the local outbox")
	}
}

func TestBackend_CloseDrainsTheOutboxIntoTheStore(t *testing.T) {
	art := newMemArt()
	b, _ := outageBackend(t, art)
	ctx := context.Background()

	art.setPutErr(errors.New("connection refused"))
	if err := b.CreateRun(ctx, runningRun("r")); err != nil {
		t.Fatalf("CreateRun during the outage: %v", err)
	}

	art.setPutErr(nil)
	if err := b.Close(); err != nil {
		t.Fatalf("Close after the store recovered: %v", err)
	}
	if ok, _ := art.Has(ctx, "runs/r/state.ndjson"); !ok {
		t.Fatal("Close left the run's state in the local outbox")
	}
}

func TestOutbox_StageRefusesAKindItCannotReplay(t *testing.T) {
	art := newMemArt()
	outbox, err := s3state.OpenOutbox(filepath.Join(t.TempDir(), "outbox.db"), art, time.Hour)
	if err != nil {
		t.Fatalf("OpenOutbox: %v", err)
	}
	t.Cleanup(func() { _ = outbox.Close() })
	ctx := context.Background()

	if err := outbox.Stage(ctx, s3state.OutboxKindLog, "logs/r/n.log", []byte("line")); err == nil {
		t.Fatal("Stage queued a log append the drainer would delete without replaying it")
	}
	if n, err := outbox.Pending(ctx); err != nil || n != 0 {
		t.Fatalf("Pending = %d, %v; want 0, nil", n, err)
	}
}
