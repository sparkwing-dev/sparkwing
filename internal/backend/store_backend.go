package backend

import (
	"context"
	"io"
	"os"

	"github.com/sparkwing-dev/sparkwing/v2/orchestrator"
	"github.com/sparkwing-dev/sparkwing/v2/orchestrator/store"
	"github.com/sparkwing-dev/sparkwing/v2/pkg/storage"
)

// StoreBackend wraps a *store.Store for state, with either a
// storage.LogStore or the on-disk laptop layout for log reads.
type StoreBackend struct {
	st       *store.Store
	paths    orchestrator.Paths
	logStore storage.LogStore // when nil, fall back to disk reads under paths

	caps Capabilities
}

// NewStoreBackend constructs a StoreBackend bound to st. paths is used
// for the disk log fallback when logStore is nil. Pass a non-nil
// logStore to route log reads through that backend instead.
func NewStoreBackend(st *store.Store, paths orchestrator.Paths, logStore storage.LogStore) *StoreBackend {
	return &StoreBackend{st: st, paths: paths, logStore: logStore}
}

var _ Backend = (*StoreBackend)(nil)

// SetCapabilities binds the static capabilities body.
func (b *StoreBackend) SetCapabilities(c Capabilities) { b.caps = c }

func (b *StoreBackend) Capabilities(context.Context) (Capabilities, error) {
	if b.caps.Mode == "" {
		return Capabilities{
			Mode:     "local",
			Storage:  CapabilitiesStorage{Artifacts: "fs", Logs: "fs", Runs: "sqlite"},
			Features: []string{"pipelines", "runs", "logs", "secrets", "approvals", "cross-pipeline-refs"},
		}, nil
	}
	return b.caps, nil
}

func (b *StoreBackend) ListRuns(ctx context.Context, f store.RunFilter) ([]*store.Run, error) {
	return b.st.ListRuns(ctx, f)
}

func (b *StoreBackend) GetRun(ctx context.Context, runID string) (*store.Run, error) {
	return b.st.GetRun(ctx, runID)
}

func (b *StoreBackend) ListNodes(ctx context.Context, runID string) ([]*store.Node, error) {
	return b.st.ListNodes(ctx, runID)
}

func (b *StoreBackend) ListEventsAfter(ctx context.Context, runID string, afterSeq int64, limit int) ([]store.Event, error) {
	return b.st.ListEventsAfter(ctx, runID, afterSeq, limit)
}

func (b *StoreBackend) ReadNodeLog(ctx context.Context, runID, nodeID string, opts ReadOpts) ([]byte, error) {
	if b.logStore != nil {
		return b.logStore.Read(ctx, runID, nodeID, toStorageReadOpts(opts))
	}
	// Disk fallback: missing files render as empty (no logs yet).
	f, err := os.Open(b.paths.NodeLog(runID, nodeID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

func (b *StoreBackend) StreamNodeLog(ctx context.Context, runID, nodeID string) (io.ReadCloser, error) {
	if b.logStore != nil {
		return b.logStore.Stream(ctx, runID, nodeID)
	}
	// Disk fallback has no streaming; the dashboard polls.
	return nil, nil
}

func toStorageReadOpts(o ReadOpts) storage.ReadOpts {
	return storage.ReadOpts{
		Tail:  o.Tail,
		Head:  o.Head,
		Lines: o.Lines,
		Grep:  o.Grep,
	}
}
