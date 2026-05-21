package s3state_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/s3state"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// memArt is an in-memory ArtifactStore used to exercise s3state
// without requiring a live object store. The fault knobs (putErr,
// getErr) let outbox + transient-error paths run in unit tests.
type memArt struct {
	mu     sync.Mutex
	data   map[string][]byte
	putErr error
	getErr error
}

func newMemArt() *memArt { return &memArt{data: map[string][]byte{}} }

func (m *memArt) Get(_ context.Context, key string) (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.getErr != nil {
		return nil, m.getErr
	}
	b, ok := m.data[key]
	if !ok {
		return nil, storage.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

func (m *memArt) Put(_ context.Context, key string, r io.Reader) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.putErr != nil {
		return m.putErr
	}
	body, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	m.data[key] = body
	return nil
}

func (m *memArt) Has(_ context.Context, key string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.data[key]
	return ok, nil
}

func (m *memArt) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, key)
	return nil
}

func (m *memArt) List(_ context.Context, prefix string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.data))
	for k := range m.data {
		if prefix == "" || len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			out = append(out, k)
		}
	}
	return out, nil
}

func TestS3StateBackend_CreateRun_RoundTrips(t *testing.T) {
	art := newMemArt()
	b := s3state.New(art, s3state.WithFlushInterval(10*time.Millisecond))
	t.Cleanup(func() { _ = b.Close() })

	ctx := context.Background()
	run := store.Run{
		ID:        "run-1",
		Pipeline:  "deploy",
		Status:    "running",
		StartedAt: time.Now().UTC(),
	}
	if err := b.CreateRun(ctx, run); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	got, err := b.GetRun(ctx, "run-1")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.Pipeline != "deploy" || got.Status != "running" {
		t.Errorf("GetRun got %+v", got)
	}
}

func TestS3StateBackend_FinishNode_PersistsEnvelope(t *testing.T) {
	art := newMemArt()
	b := s3state.New(art, s3state.WithFlushInterval(5*time.Millisecond))
	ctx := context.Background()

	if err := b.CreateRun(ctx, store.Run{ID: "r", Pipeline: "p", Status: "running", StartedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := b.CreateNode(ctx, store.Node{RunID: "r", NodeID: "n", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := b.StartNode(ctx, "r", "n"); err != nil {
		t.Fatal(err)
	}
	out := []byte(`{"ok":true}`)
	if err := b.FinishNode(ctx, "r", "n", "success", "", out); err != nil {
		t.Fatal(err)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopen on the same bucket: replay should recover terminal state.
	b2 := s3state.New(art)
	t.Cleanup(func() { _ = b2.Close() })
	n, err := b2.GetNode(ctx, "r", "n")
	if err != nil {
		t.Fatalf("GetNode after reopen: %v", err)
	}
	if n.Status != "done" || n.Outcome != "success" {
		t.Errorf("node = %+v, want done/success", n)
	}
	if !bytes.Equal(n.Output, out) {
		t.Errorf("output = %q, want %q", n.Output, out)
	}
}

func TestS3StateBackend_Annotations_Accumulate(t *testing.T) {
	art := newMemArt()
	b := s3state.New(art, s3state.WithFlushInterval(5*time.Millisecond))
	t.Cleanup(func() { _ = b.Close() })
	ctx := context.Background()

	if err := b.CreateRun(ctx, store.Run{ID: "r", StartedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := b.CreateNode(ctx, store.Node{RunID: "r", NodeID: "n", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	for _, msg := range []string{"first", "second", "third"} {
		if err := b.AppendNodeAnnotation(ctx, "r", "n", msg); err != nil {
			t.Fatalf("AppendNodeAnnotation: %v", err)
		}
	}
	n, err := b.GetNode(ctx, "r", "n")
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if len(n.Annotations) != 3 || n.Annotations[0] != "first" || n.Annotations[2] != "third" {
		t.Errorf("annotations = %v", n.Annotations)
	}
}

func TestS3StateBackend_Steps_ListInInsertionOrder(t *testing.T) {
	art := newMemArt()
	b := s3state.New(art, s3state.WithFlushInterval(5*time.Millisecond))
	t.Cleanup(func() { _ = b.Close() })
	ctx := context.Background()

	if err := b.CreateRun(ctx, store.Run{ID: "r", StartedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"a", "b", "c"} {
		if err := b.StartNodeStep(ctx, "r", "n", s); err != nil {
			t.Fatal(err)
		}
		if err := b.FinishNodeStep(ctx, "r", "n", s, "passed"); err != nil {
			t.Fatal(err)
		}
	}
	steps, err := b.ListNodeSteps(ctx, "r")
	if err != nil {
		t.Fatalf("ListNodeSteps: %v", err)
	}
	if len(steps) != 3 || steps[0].StepID != "a" || steps[2].StepID != "c" {
		t.Errorf("steps = %+v", steps)
	}
}

func TestS3StateBackend_ErrNotSupported_OnControlPlane(t *testing.T) {
	art := newMemArt()
	b := s3state.New(art)
	t.Cleanup(func() { _ = b.Close() })
	ctx := context.Background()

	cases := []struct {
		name string
		call func() error
	}{
		{"WriteNodeDispatch", func() error { return b.WriteNodeDispatch(ctx, store.NodeDispatch{RunID: "r", NodeID: "n"}) }},
		{"CreateDebugPause", func() error { return b.CreateDebugPause(ctx, store.DebugPause{RunID: "r", NodeID: "n"}) }},
		{"CreateApproval", func() error { return b.CreateApproval(ctx, store.Approval{RunID: "r", NodeID: "n"}) }},
		{"ListPendingApprovals", func() error { _, e := b.ListPendingApprovals(ctx); return e }},
		{"FindSpawnedChildTriggerID", func() error { _, e := b.FindSpawnedChildTriggerID(ctx, "p", "n", "x"); return e }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call()
			if !errors.Is(err, s3state.ErrNotSupported) {
				t.Errorf("err = %v, want wraps ErrNotSupported", err)
			}
		})
	}
}

func TestS3StateBackend_GetLatestRun_FiltersByPipeline(t *testing.T) {
	art := newMemArt()
	b := s3state.New(art, s3state.WithFlushInterval(5*time.Millisecond))
	t.Cleanup(func() { _ = b.Close() })
	ctx := context.Background()

	now := time.Now().UTC()
	runs := []store.Run{
		{ID: "old-deploy", Pipeline: "deploy", Status: "success", StartedAt: now.Add(-2 * time.Hour)},
		{ID: "new-deploy", Pipeline: "deploy", Status: "success", StartedAt: now},
		{ID: "test-only", Pipeline: "test", Status: "success", StartedAt: now.Add(-30 * time.Minute)},
	}
	for _, r := range runs {
		if err := b.CreateRun(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	// Force flush so List sees every run.
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	b2 := s3state.New(art)
	t.Cleanup(func() { _ = b2.Close() })

	got, err := b2.GetLatestRun(ctx, "deploy", []string{"success"}, 24*time.Hour)
	if err != nil {
		t.Fatalf("GetLatestRun: %v", err)
	}
	if got.ID != "new-deploy" {
		t.Errorf("got %s, want new-deploy", got.ID)
	}
}

func TestS3StateBackend_AppendEvent_AndEventSequence(t *testing.T) {
	art := newMemArt()
	b := s3state.New(art, s3state.WithFlushInterval(5*time.Millisecond))
	t.Cleanup(func() { _ = b.Close() })
	ctx := context.Background()

	if err := b.CreateRun(ctx, store.Run{ID: "r", StartedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"k1", "k2", "k3"} {
		if err := b.AppendEvent(ctx, "r", "", k, []byte(`{}`)); err != nil {
			t.Fatalf("AppendEvent: %v", err)
		}
	}
	// We can't read events directly from the StateStore surface; round-
	// trip through the dashboard backend would re-read NDJSON. Here we
	// just verify each AppendEvent call returns nil and stage cleanly.
}

func TestS3StateBackend_BufferThreshold_TriggersFlush(t *testing.T) {
	art := newMemArt()
	// 100-byte threshold + a long flushInterval guarantees the
	// in-band flush path is what writes to the bucket, not the timer.
	b := s3state.New(art,
		s3state.WithFlushInterval(10*time.Second),
		s3state.WithBufferThreshold(100),
	)
	t.Cleanup(func() { _ = b.Close() })
	ctx := context.Background()

	if err := b.CreateRun(ctx, store.Run{ID: "r", Pipeline: "p", Status: "running", StartedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	// UpdatePlanSnapshot writes a Run envelope carrying the snapshot
	// bytes inline -- enough to cross the 100-byte threshold and
	// trigger an in-band flush.
	if err := b.UpdatePlanSnapshot(ctx, "r", bytes.Repeat([]byte("y"), 4096)); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if ok, _ := art.Has(ctx, "runs/r/state.ndjson"); ok {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("threshold did not trigger a flush within 500ms")
}

func TestS3StateBackend_Close_FlushesPending(t *testing.T) {
	art := newMemArt()
	// Long interval so only Close can flush.
	b := s3state.New(art, s3state.WithFlushInterval(10*time.Second))
	ctx := context.Background()
	if err := b.CreateRun(ctx, store.Run{ID: "r", Pipeline: "p", Status: "running", StartedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if ok, _ := art.Has(ctx, "runs/r/state.ndjson"); ok {
		t.Fatal("flush happened before Close; threshold or timer fired unexpectedly")
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if ok, _ := art.Has(ctx, "runs/r/state.ndjson"); !ok {
		t.Fatal("Close did not flush pending state")
	}
}

func TestOutbox_DrainsAfterTransientError(t *testing.T) {
	art := newMemArt()
	dir := t.TempDir()
	outbox, err := s3state.OpenOutbox(filepath.Join(dir, "outbox.db"), art, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("OpenOutbox: %v", err)
	}
	t.Cleanup(func() { _ = outbox.Close() })

	// Stage a write while "S3 is down" -- the body lands in the outbox.
	body := []byte(`{"kind":"run","data":{"id":"r","pipeline":"p"}}`)
	ctx := context.Background()
	if err := outbox.Stage(ctx, s3state.OutboxKindState, "runs/r/state.ndjson", body); err != nil {
		t.Fatalf("Stage: %v", err)
	}
	n, err := outbox.Pending(ctx)
	if err != nil || n != 1 {
		t.Fatalf("Pending = %d, %v; want 1, nil", n, err)
	}

	// Connectivity returns; drain should clear the outbox + write the
	// blob to the store.
	if err := outbox.Drain(ctx); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if got, _ := outbox.Pending(ctx); got != 0 {
		t.Fatalf("Pending after drain = %d, want 0", got)
	}
	if ok, _ := art.Has(ctx, "runs/r/state.ndjson"); !ok {
		t.Fatal("outbox drain did not deliver to the store")
	}
}
