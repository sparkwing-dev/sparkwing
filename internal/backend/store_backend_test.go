package backend_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/backend"
	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func newTestStore(t *testing.T) (*store.Store, paths.Paths) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st, paths.PathsAt(dir)
}

func TestStoreBackend_Capabilities(t *testing.T) {
	t.Parallel()
	st, _ := newTestStore(t)
	b := backend.NewStoreBackend(st, nil)
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
	st, _ := newTestStore(t)
	b := backend.NewStoreBackend(st, nil)
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

func TestStoreBackendReadRequiresBoundLogs(t *testing.T) {
	st, _ := newTestStore(t)
	_, err := backend.NewStoreBackend(st, nil).ReadNodeLog(t.Context(), "r1", "n1", backend.ReadOpts{})
	if !errors.Is(err, storage.ErrNotSupported) {
		t.Fatalf("unbound log read = %v", err)
	}
}
