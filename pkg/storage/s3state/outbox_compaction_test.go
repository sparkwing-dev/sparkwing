package s3state_test

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"sync"
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

type holdFirstPutArt struct {
	*memArt
	mu       sync.Mutex
	holdKey  string
	entered  chan struct{}
	release  chan struct{}
	held     bool
	putOrder []string
}

func newHoldFirstPutArt(key string) *holdFirstPutArt {
	return &holdFirstPutArt{
		memArt:  newMemArt(),
		holdKey: key,
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
}

// waitForCompletedPut gives a replay that is free to run a chance to finish
// before the held one is released, bounded so a build that makes the two
// serialize does not wait for something that will never happen.
func (h *holdFirstPutArt) waitForCompletedPut(d time.Duration) {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		h.mu.Lock()
		done := len(h.putOrder) > 0
		h.mu.Unlock()
		if done {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func (h *holdFirstPutArt) Put(ctx context.Context, key string, r io.Reader) error {
	h.mu.Lock()
	hold := key == h.holdKey && !h.held
	if hold {
		h.held = true
	}
	h.mu.Unlock()
	if hold {
		close(h.entered)
		<-h.release
	}
	err := h.memArt.Put(ctx, key, r)
	if err == nil {
		h.mu.Lock()
		h.putOrder = append(h.putOrder, key)
		h.mu.Unlock()
	}
	return err
}

func TestOutbox_StageDoesNotLetAnInFlightReplayOvertakeTheNewestBlob(t *testing.T) {
	key := "runs/r/state.ndjson"
	art := newHoldFirstPutArt(key)
	outbox, err := s3state.OpenOutbox(filepath.Join(t.TempDir(), "outbox.db"), art, time.Hour)
	if err != nil {
		t.Fatalf("OpenOutbox: %v", err)
	}
	t.Cleanup(func() { _ = outbox.Close() })
	ctx := context.Background()

	older := []byte("run-still-running")
	newest := []byte("run-still-running-then-finished")
	if err := outbox.Stage(ctx, s3state.OutboxKindState, key, older); err != nil {
		t.Fatalf("Stage older: %v", err)
	}

	firstDrain := make(chan error, 1)
	go func() { firstDrain <- outbox.Drain(ctx) }()
	<-art.entered

	if err := outbox.Stage(ctx, s3state.OutboxKindState, key, newest); err != nil {
		t.Fatalf("Stage newest: %v", err)
	}
	secondDrain := make(chan error, 1)
	go func() { secondDrain <- outbox.Drain(ctx) }()
	art.waitForCompletedPut(300 * time.Millisecond)

	close(art.release)
	if err := <-firstDrain; err != nil {
		t.Fatalf("first Drain: %v", err)
	}
	if err := <-secondDrain; err != nil {
		t.Fatalf("second Drain: %v", err)
	}

	rc, err := art.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	got, _ := io.ReadAll(rc)
	_ = rc.Close()
	if string(got) != string(newest) {
		t.Errorf("store = %q, want the newest blob %q; a replay of a superseded row landed last", got, newest)
	}
	if n, err := outbox.Pending(ctx); err != nil || n != 0 {
		t.Fatalf("Pending = %d, %v; want 0, nil", n, err)
	}
}
