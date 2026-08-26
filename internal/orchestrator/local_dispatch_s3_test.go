package orchestrator

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/s3state"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// memBucket is an in-memory ArtifactStore, so an object-store-backed
// run assembles without an AWS endpoint.
type memBucket struct {
	mu   sync.Mutex
	data map[string][]byte
}

func newMemBucket() *memBucket { return &memBucket{data: map[string][]byte{}} }

func (m *memBucket) Get(_ context.Context, key string) (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.data[key]
	if !ok {
		return nil, storage.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

func (m *memBucket) Put(_ context.Context, key string, r io.Reader) error {
	body, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[key] = body
	return nil
}

func (m *memBucket) Has(_ context.Context, key string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.data[key]
	return ok, nil
}

func (m *memBucket) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, key)
	return nil
}

func (m *memBucket) List(_ context.Context, prefix string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for k := range m.data {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	return out, nil
}

// TestSetupLocalExecution_S3StateNoLongerFallsBackInProcess is the
// gate on the second execution model being gone.
//
// An object-store-backed local run used to get (nil, nil) here, which
// left opts.Runner unset and its nodes running in the dispatcher's own
// goroutines -- process-per-node everywhere except the one shape the
// CI story documents. It must now come back with a runner pointed at a
// loopback of its own.
func TestSetupLocalExecution_S3StateNoLongerFallsBackInProcess(t *testing.T) {
	art := newMemBucket()
	state := s3state.New(art, s3state.WithFlushInterval(10*time.Millisecond))
	t.Cleanup(func() { _ = state.Close() })

	backends := S3Backends(nil, state, art)
	opts := &Options{State: state, ArtifactStore: art, RunID: "run-s3-setup"}

	exec, err := setupLocalExecution(newInternalPaths(t), opts, backends, t.TempDir(), quietTestLogger())
	if err != nil {
		t.Fatalf("setupLocalExecution: %v", err)
	}
	if exec == nil {
		t.Fatal("object-store state still falls back to in-process execution")
	}
	t.Cleanup(exec.cleanup)
	if exec.runner == nil {
		t.Fatal("no node runner wired for an object-store run")
	}
}

// TestSetupLocalExecution_S3LoopbackServesTheRunsState checks that the
// URL and token handed to a node process reach this run's state, and
// that nothing else does.
func TestSetupLocalExecution_S3LoopbackServesTheRunsState(t *testing.T) {
	art := newMemBucket()
	state := s3state.New(art, s3state.WithFlushInterval(10*time.Millisecond))
	t.Cleanup(func() { _ = state.Close() })

	ctx := context.Background()
	const runID = "run-s3-loopback"
	if err := state.CreateRun(ctx, store.Run{ID: runID, Pipeline: "modetwo", Status: "running"}); err != nil {
		t.Fatalf("seed run: %v", err)
	}

	backends := S3Backends(nil, state, art)
	loopback, err := startRunLoopback(&Options{State: state, ArtifactStore: art, RunID: runID}, backends, quietTestLogger())
	if err != nil {
		t.Fatalf("startRunLoopback: %v", err)
	}
	t.Cleanup(loopback.Close)

	c := client.NewWithToken(loopback.url, nil, loopback.token)
	got, err := c.GetRunForExecution(ctx, runID)
	if err != nil {
		t.Fatalf("child read of the run: %v", err)
	}
	if got.Pipeline != "modetwo" {
		t.Errorf("pipeline = %q, want modetwo", got.Pipeline)
	}

	// safety: the child writes its terminal row through the same URL, and it
	// has to land in the run's object-store state, not in a second copy.
	if err := c.CreateNode(ctx, store.Node{RunID: runID, NodeID: "n", Status: "pending"}); err != nil {
		t.Fatalf("child CreateNode: %v", err)
	}
	if err := c.FinishNode(ctx, runID, "n", "success", "", []byte(`{"ok":true}`)); err != nil {
		t.Fatalf("child FinishNode: %v", err)
	}
	n, err := state.GetNode(ctx, runID, "n")
	if err != nil || n.Outcome != "success" {
		t.Fatalf("node row in the run's own state = %+v (err=%v)", n, err)
	}

	// safety: the token is the only boundary; a request without it must not
	// reach the run's state.
	req, _ := http.NewRequest(http.MethodGet, loopback.url+"/api/v1/runs/"+runID, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("unauthenticated probe: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("unauthenticated read = %d, want 401", resp.StatusCode)
	}
}

// TestStartRunLoopback_SQLiteStillGetsTheRealController pins the split:
// a run whose state is a local database keeps the full controller,
// which is the surface the dashboard shares with it.
func TestStartRunLoopback_SQLiteStillGetsTheRealController(t *testing.T) {
	paths := newInternalPaths(t)
	st, err := store.Open(paths.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	backends := LocalBackends(paths, st, nil)
	loopback, err := startRunLoopback(&Options{State: st, RunID: "run-sqlite"}, backends, quietTestLogger())
	if err != nil {
		t.Fatalf("startRunLoopback: %v", err)
	}
	t.Cleanup(loopback.Close)

	// safety: /api/v1/trends is a controller route the shim does not serve,
	// so answering it proves the real controller is mounted here.
	req, _ := http.NewRequest(http.MethodGet, loopback.url+"/api/v1/trends", nil)
	req.Header.Set("Authorization", "Bearer "+loopback.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("trends probe: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		t.Error("a SQLite-backed run got the node-facing shim, not the real controller")
	}
}

// newInternalPaths returns a Paths under t.TempDir() with the root
// created, for the tests inside the package (newPaths lives in the
// external test package).
func newInternalPaths(t *testing.T) Paths {
	t.Helper()
	root := t.TempDir()
	p := PathsAt(root)
	if err := p.EnsureRoot(); err != nil {
		t.Fatalf("ensure root: %v", err)
	}
	return p
}

// quietTestLogger keeps the loopback controller silent in tests.
func quietTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}
