package fs

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"
)

func newStoreT(t *testing.T, dir string) *ArtifactStore {
	t.Helper()
	s, err := NewArtifactStore(dir)
	if err != nil {
		t.Fatalf("NewArtifactStore: %v", err)
	}
	return s
}

func TestPutIfAbsentGivesUpOnTheDeadlineWhileTheLockIsHeld(t *testing.T) {
	dir := t.TempDir()
	held, err := newStoreT(t, dir).lockKey(context.Background(), "k")
	if err != nil {
		t.Fatalf("lockKey: %v", err)
	}
	defer releaseKey(held)

	blocked := newStoreT(t, dir)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = blocked.PutIfAbsent(ctx, "k", bytes.NewReader([]byte("v")))
	elapsed := time.Since(start)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("PutIfAbsent against a held lock: err = %v, want context.DeadlineExceeded", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("PutIfAbsent returned after %s, want near its 50ms deadline", elapsed)
	}
	has, err := blocked.Has(context.Background(), "k")
	if err != nil {
		t.Fatalf("Has: %v", err)
	}
	if has {
		t.Fatal("PutIfAbsent wrote the object it reported it could not take the lock for")
	}
}

func TestConditionalWritesSupportedDoesNotWaitOnAHeldLock(t *testing.T) {
	dir := t.TempDir()
	held, err := newStoreT(t, dir).lockKey(context.Background(), casProbeKey)
	if err != nil {
		t.Fatalf("lockKey: %v", err)
	}
	defer releaseKey(held)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ok, err := newStoreT(t, dir).ConditionalWritesSupported(ctx)
	if err != nil {
		t.Fatalf("ConditionalWritesSupported: %v", err)
	}
	if !ok {
		t.Fatal("a lock another holder is already holding reported the filesystem as unable to lock")
	}
}
