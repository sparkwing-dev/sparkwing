package fs

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"testing/quick"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
)

func newStoreT(t *testing.T, dir string) *ArtifactStore {
	t.Helper()
	s, err := NewArtifactStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestPutIfAbsentGivesUpOnTheDeadlineWhileTheLockIsHeld(t *testing.T) {
	dir := t.TempDir()
	held, err := newStoreT(t, dir).lockKey(t.Context(), "k")
	if err != nil {
		t.Fatal(err)
	}
	defer releaseKey(held)
	blocked := newStoreT(t, dir)
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
		defer cancel()
		err := blocked.PutIfAbsent(ctx, "k", bytes.NewReader([]byte("v")))
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("blocked create = %v", err)
		}
	})
	if _, err := blocked.Get(t.Context(), "k"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("cancelled create published an object: %v", err)
	}
}

func TestPutIfAbsentAcrossHandles(t *testing.T) {
	property := func(left, right [64]byte) bool {
		dir := t.TempDir()
		stores := []*ArtifactStore{newStoreT(t, dir), newStoreT(t, dir)}
		bodies := [][]byte{left[:], right[:]}
		start := make(chan struct{})
		var wg sync.WaitGroup
		var results [2]error
		for i := range stores {
			wg.Go(func() {
				<-start
				results[i] = stores[i].PutIfAbsent(t.Context(), "key", bytes.NewReader(bodies[i]))
			})
		}
		close(start)
		wg.Wait()
		winner := 0
		if results[0] != nil {
			winner = 1
		}
		if results[winner] != nil || !errors.Is(results[1-winner], storage.ErrPreconditionFailed) {
			return false
		}
		reader, err := stores[0].Get(t.Context(), "key")
		if err != nil {
			t.Fatal(err)
		}
		body, readErr := io.ReadAll(reader)
		return errors.Join(readErr, reader.Close()) == nil && bytes.Equal(body, bodies[winner])
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 32}); err != nil {
		t.Fatal(err)
	}
}
