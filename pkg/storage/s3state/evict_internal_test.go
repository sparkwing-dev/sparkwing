package s3state

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type stubArt struct {
	mu     sync.Mutex
	data   map[string][]byte
	getErr error
}

func (s *stubArt) setGetErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.getErr = err
}

func newStubArt() *stubArt { return &stubArt{data: map[string][]byte{}} }

func (s *stubArt) Get(_ context.Context, key string) (io.ReadCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.getErr != nil {
		return nil, s.getErr
	}
	b, ok := s.data[key]
	if !ok {
		return nil, storage.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

func (s *stubArt) Put(_ context.Context, key string, r io.Reader) error {
	body, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[key] = body
	return nil
}

func (s *stubArt) Has(_ context.Context, key string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.data[key]
	return ok, nil
}

func (s *stubArt) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, key)
	return nil
}

func (s *stubArt) List(_ context.Context, prefix string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.data))
	for k := range s.data {
		if prefix == "" || (len(k) >= len(prefix) && k[:len(prefix)] == prefix) {
			out = append(out, k)
		}
	}
	return out, nil
}

func TestEvictIdleReadsKeepsAnEntryACallerIsStillAcquiring(t *testing.T) {
	b := New(newStubArt(), WithFlushInterval(time.Hour))
	t.Cleanup(func() { _ = b.Close() })

	b.mu.Lock()
	rs := newRunState()
	b.runs["r"] = rs
	b.mu.Unlock()

	b.evictIdleReads(time.Now())

	if b.lookupRun("r") != rs {
		t.Fatal("the sweep dropped an entry a caller had not finished acquiring; a writer would append to a detached run whose envelopes are never flushed")
	}
}

func TestEvictIdleReadsKeepsAPinnedSnapshotAndDropsItOnceReleased(t *testing.T) {
	art := newStubArt()
	ctx := context.Background()
	writer := New(art, WithFlushInterval(time.Hour))
	if err := writer.CreateRun(ctx, store.Run{ID: "r", Pipeline: "p", Status: "running", StartedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("writer Close: %v", err)
	}

	b := New(art, WithFlushInterval(time.Hour))
	t.Cleanup(func() { _ = b.Close() })
	rs, err := b.getRunState(ctx, "r", accessRead)
	if err != nil {
		t.Fatalf("getRunState: %v", err)
	}

	b.mu.Lock()
	rs.pins++
	b.mu.Unlock()
	b.evictIdleReads(time.Now())
	if b.lookupRun("r") != rs {
		t.Fatal("the sweep dropped a snapshot a caller is holding")
	}

	b.mu.Lock()
	rs.pins--
	b.mu.Unlock()
	b.evictIdleReads(time.Now())
	if b.lookupRun("r") != nil {
		t.Fatal("a released snapshot of another process's run was not swept")
	}
}

func TestEvictIdleReadsKeepsARunThisProcessWrites(t *testing.T) {
	b := New(newStubArt(), WithFlushInterval(time.Hour))
	t.Cleanup(func() { _ = b.Close() })
	ctx := context.Background()

	if err := b.CreateRun(ctx, store.Run{ID: "r", Pipeline: "p", Status: "running", StartedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := b.flushRun(ctx, "r", true); err != nil {
		t.Fatalf("flushRun: %v", err)
	}

	b.evictIdleReads(time.Now())

	if b.lookupRun("r") == nil {
		t.Fatal("the sweep dropped a run this process writes")
	}
}

func TestEvictIdleReadsSweepsASnapshotWhoseLoadFailed(t *testing.T) {
	art := newStubArt()
	art.setGetErr(errors.New("s3: 503 slow down"))
	b := New(art, WithFlushInterval(time.Hour))
	t.Cleanup(func() { _ = b.Close() })
	ctx := context.Background()

	if _, err := b.GetRun(ctx, "r"); err == nil {
		t.Fatal("GetRun reported success while the object store was failing")
	}
	if b.lookupRun("r") == nil {
		t.Fatal("no entry to sweep")
	}

	b.evictIdleReads(time.Now())

	if b.lookupRun("r") != nil {
		t.Fatal("an entry whose load failed is never swept, so a failing read pins one entry per run id")
	}

	art.setGetErr(nil)
	if _, err := b.GetRun(ctx, "r"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetRun after recovery = %v, want ErrNotFound from a fresh read", err)
	}
}

func TestGetRunRetriesAfterAFailedLoad(t *testing.T) {
	art := newStubArt()
	ctx := context.Background()
	writer := New(art, WithFlushInterval(time.Hour))
	if err := writer.CreateRun(ctx, store.Run{ID: "r", Pipeline: "p", Status: "running", StartedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("writer Close: %v", err)
	}

	art.setGetErr(errors.New("s3: 503 slow down"))
	b := New(art, WithFlushInterval(time.Hour), WithReadCacheTTL(time.Hour))
	t.Cleanup(func() { _ = b.Close() })
	if _, err := b.GetRun(ctx, "r"); err == nil {
		t.Fatal("GetRun reported success while the object store was failing")
	}

	art.setGetErr(nil)
	got, err := b.GetRun(ctx, "r")
	if err != nil {
		t.Fatalf("GetRun after recovery: %v", err)
	}
	if got.Pipeline != "p" {
		t.Errorf("run = %+v, want the stored run", got)
	}
}
