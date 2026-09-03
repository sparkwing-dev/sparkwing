package s3state_test

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/storage/s3state"
)

func TestOutbox_StageCollapsesSupersededRowsForTheSameKey(t *testing.T) {
	art := newMemArt()
	outbox, err := s3state.OpenOutbox(filepath.Join(t.TempDir(), "outbox.db"), art, time.Hour)
	if err != nil {
		t.Fatalf("OpenOutbox: %v", err)
	}
	t.Cleanup(func() { _ = outbox.Close() })
	ctx := context.Background()

	key := "runs/r/state.ndjson"
	newest := []byte("first-second-third")
	for _, body := range [][]byte{[]byte("first"), []byte("first-second"), newest} {
		if err := outbox.Stage(ctx, s3state.OutboxKindState, key, body); err != nil {
			t.Fatalf("Stage: %v", err)
		}
	}
	if n, err := outbox.Pending(ctx); err != nil || n != 1 {
		t.Fatalf("Pending = %d, %v; want 1, nil (each blob replaces the last)", n, err)
	}

	if err := outbox.Drain(ctx); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	rc, err := art.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	got, _ := io.ReadAll(rc)
	_ = rc.Close()
	if string(got) != string(newest) {
		t.Errorf("store = %q, want the newest blob %q", got, newest)
	}
}

func TestOutbox_StageRefusesWhenTheQueueIsFull(t *testing.T) {
	art := newMemArt()
	outbox, err := s3state.OpenOutbox(filepath.Join(t.TempDir(), "outbox.db"), art, time.Hour)
	if err != nil {
		t.Fatalf("OpenOutbox: %v", err)
	}
	t.Cleanup(func() { _ = outbox.Close() })
	ctx := context.Background()

	for i := 0; i < s3state.DefaultOutboxMaxRows; i++ {
		key := fmt.Sprintf("runs/r%d/state.ndjson", i)
		if err := outbox.Stage(ctx, s3state.OutboxKindState, key, []byte("body")); err != nil {
			t.Fatalf("Stage %s: %v", key, err)
		}
	}
	if err := outbox.Stage(ctx, s3state.OutboxKindState, "runs/over/state.ndjson", []byte("body")); err == nil {
		t.Fatal("Stage queued a write past the row cap")
	}
	if err := outbox.Stage(ctx, s3state.OutboxKindState, "runs/r0/state.ndjson", []byte("newer")); err != nil {
		t.Fatalf("Stage of an already-queued key at the cap: %v", err)
	}
	if n, err := outbox.Pending(ctx); err != nil || n != s3state.DefaultOutboxMaxRows {
		t.Fatalf("Pending = %d, %v; want %d, nil", n, err, s3state.DefaultOutboxMaxRows)
	}
}
