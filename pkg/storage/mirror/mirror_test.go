package mirror_test

import (
	"bytes"
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/storage/mirror"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
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

func sampleRun(id string) store.Run {
	return store.Run{ID: id, Pipeline: "demo", Status: "running", StartedAt: time.Now()}
}

func TestNew_TeesWriteToBothStores(t *testing.T) {
	ctx := context.Background()
	canon := openStore(t)
	local := openStore(t)
	w := mirror.New(canon, local, nil)

	if err := w.CreateRun(ctx, sampleRun("run-1")); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	for name, s := range map[string]*store.Store{"canonical": canon, "local": local} {
		got, err := s.GetRun(ctx, "run-1")
		if err != nil {
			t.Fatalf("%s GetRun: %v", name, err)
		}
		if got == nil || got.ID != "run-1" {
			t.Fatalf("%s did not receive the write: %#v", name, got)
		}
	}
}

func TestLocalFailureToleratedAndLogged(t *testing.T) {
	ctx := context.Background()
	canon := openStore(t)
	local := openStore(t)
	// Close local so its writes fail; canonical stays healthy.
	if err := local.Close(); err != nil {
		t.Fatalf("close local: %v", err)
	}

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	w := mirror.New(canon, local, logger)

	if err := w.CreateRun(ctx, sampleRun("run-1")); err != nil {
		t.Fatalf("local failure should not surface; got %v", err)
	}
	if got, _ := canon.GetRun(ctx, "run-1"); got == nil {
		t.Fatal("canonical write should have succeeded")
	}
	if !strings.Contains(buf.String(), "mirror: local write failed") || !strings.Contains(buf.String(), "CreateRun") {
		t.Fatalf("expected a warn for the failed local write, got log:\n%s", buf.String())
	}
}

func TestCanonicalFailureSurfaces(t *testing.T) {
	ctx := context.Background()
	canon := openStore(t)
	local := openStore(t)
	if err := canon.Close(); err != nil {
		t.Fatalf("close canonical: %v", err)
	}
	w := mirror.New(canon, local, nil)

	err := w.CreateRun(ctx, sampleRun("run-1"))
	if err == nil {
		t.Fatal("canonical error must surface to the caller")
	}
	// Local still wrote successfully despite canonical's failure.
	if got, gerr := local.GetRun(ctx, "run-1"); gerr != nil || got == nil {
		t.Fatalf("local should have written despite canonical failure: %v %#v", gerr, got)
	}
}

func TestReadsDelegateToCanonical(t *testing.T) {
	ctx := context.Background()
	canon := openStore(t)
	local := openStore(t)
	// Seed canonical only; leave local empty.
	if err := canon.CreateRun(ctx, sampleRun("run-1")); err != nil {
		t.Fatalf("seed canonical: %v", err)
	}
	w := mirror.New(canon, local, nil)

	got, err := w.GetRun(ctx, "run-1")
	if err != nil || got == nil || got.ID != "run-1" {
		t.Fatalf("read should return canonical's value: %v %#v", err, got)
	}
	if local0, _ := local.GetRun(ctx, "run-1"); local0 != nil {
		t.Fatal("read should not have touched local")
	}
}

// TestMethodCoverage_AllCategories exercises one mutating method per
// category through the wrapper and confirms it landed in both stores.
func TestMethodCoverage_AllCategories(t *testing.T) {
	ctx := context.Background()
	canon := openStore(t)
	local := openStore(t)
	w := mirror.New(canon, local, nil)
	stores := map[string]*store.Store{"canonical": canon, "local": local}

	mustNoErr := func(label string, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
	}

	// runs + nodes (FK parents for everything below).
	mustNoErr("CreateRun", w.CreateRun(ctx, sampleRun("run-1")))
	mustNoErr("CreateNode", w.CreateNode(ctx, store.Node{RunID: "run-1", NodeID: "node-1", Status: "pending"}))
	// steps
	mustNoErr("StartNodeStep", w.StartNodeStep(ctx, "run-1", "node-1", "step-1"))
	// dispatches
	mustNoErr("WriteNodeDispatch", w.WriteNodeDispatch(ctx, store.NodeDispatch{RunID: "run-1", NodeID: "node-1", Seq: 1}))
	// debug pauses
	mustNoErr("CreateDebugPause", w.CreateDebugPause(ctx, store.DebugPause{
		RunID: "run-1", NodeID: "node-1", Reason: "manual",
		PausedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
	}))
	// approvals
	mustNoErr("CreateApproval", w.CreateApproval(ctx, store.Approval{RunID: "run-1", NodeID: "node-1", RequestedAt: time.Now()}))
	// metric samples (advisory; no direct read-back in the interface)
	mustNoErr("AddNodeMetricSample", w.AddNodeMetricSample(ctx, "run-1", "node-1", store.MetricSample{TS: time.Now(), CPUMillicores: 1, MemoryBytes: 2}))
	// heartbeats
	mustNoErr("TouchRunHeartbeat", w.TouchRunHeartbeat(ctx, "run-1"))
	mustNoErr("TouchNodeHeartbeat", w.TouchNodeHeartbeat(ctx, "run-1", "node-1"))

	// Verify each category persisted to BOTH stores.
	for name, s := range stores {
		if n, err := s.GetNode(ctx, "run-1", "node-1"); err != nil || n == nil {
			t.Fatalf("%s: node missing: %v %#v", name, err, n)
		}
		if steps, err := s.ListNodeSteps(ctx, "run-1"); err != nil || len(steps) != 1 {
			t.Fatalf("%s: step missing: %v %#v", name, err, steps)
		}
		if d, err := s.GetNodeDispatch(ctx, "run-1", "node-1", -1); err != nil || d == nil {
			t.Fatalf("%s: dispatch missing: %v %#v", name, err, d)
		}
		if p, err := s.GetActiveDebugPause(ctx, "run-1", "node-1"); err != nil || p == nil {
			t.Fatalf("%s: debug pause missing: %v %#v", name, err, p)
		}
		if a, err := s.GetApproval(ctx, "run-1", "node-1"); err != nil || a == nil {
			t.Fatalf("%s: approval missing: %v %#v", name, err, a)
		}
	}

	// triggers: the sole trigger method here is a read; confirm it
	// delegates to canonical without error.
	if _, err := w.FindSpawnedChildTriggerID(ctx, "run-1", "node-1", "child"); err != nil {
		t.Fatalf("FindSpawnedChildTriggerID delegate: %v", err)
	}
}

// blockingCanonical wraps a real store but blocks inside CreateRun until
// release is closed, after signaling that it has entered. It lets the
// test observe whether the local write proceeds while canonical is
// blocked — i.e. whether the two run in parallel.
type blockingCanonical struct {
	*store.Store
	entered chan struct{}
	release chan struct{}
}

func (c *blockingCanonical) CreateRun(ctx context.Context, r store.Run) error {
	close(c.entered)
	<-c.release
	return c.Store.CreateRun(ctx, r)
}

// closeRecorder wraps a real store and records whether Close was called,
// so the cascade test can confirm canonical was closed too.
type closeRecorder struct {
	*store.Store
	closed bool
}

func (c *closeRecorder) Close() error {
	c.closed = true
	return c.Store.Close()
}

func TestClose_CascadesToBoth(t *testing.T) {
	ctx := context.Background()
	canon := &closeRecorder{Store: openStore(t)}
	local, err := store.Open(filepath.Join(t.TempDir(), "local.db"))
	if err != nil {
		t.Fatalf("open local: %v", err)
	}
	w := mirror.New(canon, local, nil)

	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !canon.closed {
		t.Error("canonical was not closed")
	}
	// A write against local now fails, proving it was closed too.
	if err := local.CreateRun(ctx, sampleRun("after-close")); err == nil {
		t.Error("local store was not closed (write unexpectedly succeeded)")
	}
}

func TestConcurrentFanout(t *testing.T) {
	ctx := context.Background()
	canon := &blockingCanonical{
		Store:   openStore(t),
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	local := openStore(t)
	w := mirror.New(canon, local, nil)

	done := make(chan error, 1)
	go func() { done <- w.CreateRun(ctx, sampleRun("run-1")) }()

	<-canon.entered // canonical goroutine is now parked inside CreateRun.

	// If the fanout is parallel, the local goroutine writes independently
	// while canonical is still blocked. If it were serial, local would
	// never run until we release canonical, and this poll would time out.
	deadline := time.After(2 * time.Second)
	for {
		if got, _ := local.GetRun(ctx, "run-1"); got != nil {
			break
		}
		select {
		case <-deadline:
			close(canon.release)
			t.Fatal("local write did not proceed while canonical was blocked → not parallel")
		case <-time.After(time.Millisecond):
		}
	}

	close(canon.release)
	if err := <-done; err != nil {
		t.Fatalf("CreateRun returned: %v", err)
	}
}
