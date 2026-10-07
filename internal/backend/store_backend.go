package backend

import (
	"context"
	"io"
	"os"

	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type StoreBackend struct {
	st       *store.Store
	paths    paths.Paths
	logStore storage.LogStore
	runs     RunReader
	live     LiveLogReader

	caps Capabilities
}

// RunReader answers run reads from a view narrower than the whole store,
// such as one team's runs.
type RunReader interface {
	ListRuns(ctx context.Context, f store.RunFilter) ([]*store.Run, error)
	GetRun(ctx context.Context, runID string) (*store.Run, error)
}

// Scoped returns a copy of b that lists and reads runs through runs and, when
// live is non-nil, answers a running node's log from it. A caller that serves
// one team builds one per request from that team's view.
func (b *StoreBackend) Scoped(runs RunReader, live LiveLogReader) *StoreBackend {
	c := *b
	c.runs, c.live = runs, live
	return &c
}

// StreamNodeLiveLog opens the live view Scoped attached, or answers (nil, nil)
// when it attached none.
func (b *StoreBackend) StreamNodeLiveLog(ctx context.Context, runID, nodeID string, since int64) (io.ReadCloser, error) {
	if b.live == nil {
		return nil, nil
	}
	return b.live.StreamNodeLiveLog(ctx, runID, nodeID, since)
}

// ReadNodeLiveLog reads the live view Scoped attached, or answers ok false when
// it attached none.
func (b *StoreBackend) ReadNodeLiveLog(ctx context.Context, runID, nodeID string, since int64) ([]byte, int64, bool, bool, error) {
	if b.live == nil {
		return nil, 0, false, false, nil
	}
	return b.live.ReadNodeLiveLog(ctx, runID, nodeID, since)
}

func NewStoreBackend(st *store.Store, paths paths.Paths, logStore storage.LogStore) *StoreBackend {
	return &StoreBackend{st: st, paths: paths, logStore: logStore}
}

var _ Backend = (*StoreBackend)(nil)

func (b *StoreBackend) SetCapabilities(c Capabilities) { b.caps = c }

func (b *StoreBackend) Store() *store.Store { return b.st }

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
	if b.runs != nil {
		return b.runs.ListRuns(ctx, f)
	}
	return b.st.ListRuns(ctx, f)
}

func (b *StoreBackend) GetRun(ctx context.Context, runID string) (*store.Run, error) {
	if b.runs != nil {
		return b.runs.GetRun(ctx, runID)
	}
	return b.st.GetRun(ctx, runID)
}

func (b *StoreBackend) ListNodes(ctx context.Context, runID string) ([]*store.Node, error) {
	return b.st.ListNodes(ctx, runID)
}

// GetNodeOutput reads a node's output from the store's output directory.
func (b *StoreBackend) GetNodeOutput(ctx context.Context, runID, nodeID string) ([]byte, error) {
	return b.st.GetNodeOutput(ctx, runID, nodeID)
}

func (b *StoreBackend) ListEventsAfter(ctx context.Context, runID string, afterSeq int64, limit int) ([]store.Event, error) {
	return b.st.ListEventsAfter(ctx, runID, afterSeq, limit)
}

func (b *StoreBackend) ReadNodeLog(ctx context.Context, runID, nodeID string, opts ReadOpts) ([]byte, error) {
	if b.logStore != nil {
		return b.logStore.Read(ctx, runID, nodeID, toStorageReadOpts(opts))
	}
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
