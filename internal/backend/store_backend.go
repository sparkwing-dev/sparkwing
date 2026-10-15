package backend

import (
	"context"
	"io"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type StoreBackend struct {
	st       *store.Store
	logStore storage.LogStore
	runs     RunReader

	caps Capabilities
}

// RunReader answers run reads from a view narrower than the whole store,
// such as one team's runs.
type RunReader interface {
	ListRuns(ctx context.Context, f store.RunFilter) ([]*store.Run, error)
	GetRun(ctx context.Context, runID string) (*store.Run, error)
}

// Scoped returns a backend whose run reads use the supplied tenant.
func (b *StoreBackend) Scoped(runs RunReader) *StoreBackend {
	c := *b
	c.runs = runs
	return &c
}

func NewStoreBackend(st *store.Store, logStore storage.LogStore) *StoreBackend {
	return &StoreBackend{st: st, logStore: logStore}
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

func (b *StoreBackend) ListEventsAfter(ctx context.Context, runID string, afterSeq int64, limit int) ([]store.Event, error) {
	return b.st.ListEventsAfter(ctx, runID, afterSeq, limit)
}

func (b *StoreBackend) ReadNodeLog(ctx context.Context, runID, nodeID string, opts ReadOpts) ([]byte, error) {
	if b.logStore != nil {
		return b.logStore.Read(ctx, runID, nodeID, toStorageReadOpts(opts))
	}
	return nil, storage.ErrNotSupported
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
