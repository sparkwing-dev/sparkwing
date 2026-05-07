package backend_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/backend"
	"github.com/sparkwing-dev/sparkwing/orchestrator"
	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
)

func newTestStore(t *testing.T) (*store.Store, orchestrator.Paths) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st, orchestrator.PathsAt(dir)
}

func TestStoreBackend_Capabilities(t *testing.T) {
	t.Parallel()
	st, paths := newTestStore(t)
	b := backend.NewStoreBackend(st, paths, nil)
	got, err := b.Capabilities(context.Background())
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	if got.Mode != "local" {
		t.Errorf("Mode = %q, want local", got.Mode)
	}
	if got.Storage.Logs != "fs" || got.Storage.Artifacts != "fs" {
		t.Errorf("Storage = %+v, want fs/fs", got.Storage)
	}
	if got.Storage.Runs != "sqlite" {
		t.Errorf("Storage.Runs = %q, want sqlite", got.Storage.Runs)
	}
	if got.ReadOnly {
		t.Errorf("ReadOnly = true, want false")
	}
}

func TestStoreBackend_RunsRoundTrip(t *testing.T) {
	t.Parallel()
	st, paths := newTestStore(t)
	b := backend.NewStoreBackend(st, paths, nil)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Second)
	if err := st.CreateRun(ctx, store.Run{
		ID: "r1", Pipeline: "demo", Status: "running", StartedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "r1", NodeID: "n1", Status: "pending"}); err != nil {
		t.Fatal(err)
	}

	got, err := b.GetRun(ctx, "r1")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.ID != "r1" || got.Pipeline != "demo" {
		t.Errorf("GetRun = %+v", got)
	}
	nodes, err := b.ListNodes(ctx, "r1")
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	if len(nodes) != 1 || nodes[0].NodeID != "n1" {
		t.Errorf("ListNodes = %+v", nodes)
	}
	runs, err := b.ListRuns(ctx, store.RunFilter{})
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Errorf("ListRuns len = %d, want 1", len(runs))
	}
}

func TestStoreBackend_ReadNodeLog_DiskFallback(t *testing.T) {
	t.Parallel()
	st, paths := newTestStore(t)
	b := backend.NewStoreBackend(st, paths, nil)
	ctx := context.Background()

	// Missing file: render as empty.
	got, err := b.ReadNodeLog(ctx, "r1", "n1", backend.ReadOpts{})
	if err != nil {
		t.Fatalf("ReadNodeLog miss: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ReadNodeLog miss = %q, want empty", got)
	}

	if err := paths.EnsureRunDir("r1"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.NodeLog("r1", "n1"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err = b.ReadNodeLog(ctx, "r1", "n1", backend.ReadOpts{})
	if err != nil {
		t.Fatalf("ReadNodeLog hit: %v", err)
	}
	if string(got) != "hello\n" {
		t.Errorf("ReadNodeLog = %q, want %q", got, "hello\n")
	}
}
