package backend

import (
	"context"
	"io"

	"github.com/sparkwing-dev/sparkwing/internal/storage"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

type StoreBackend struct {
	tenant   *store.Tenant
	logStore storage.LogStore
	caps     Capabilities
}

func NewStoreBackend(tenant *store.Tenant, logStore storage.LogStore) *StoreBackend {
	return &StoreBackend{tenant: tenant, logStore: logStore}
}

var _ Backend = (*StoreBackend)(nil)

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
	return b.tenant.ListRuns(ctx, f)
}

func (b *StoreBackend) GetRun(ctx context.Context, runID string) (*store.Run, error) {
	return b.tenant.GetRun(ctx, runID)
}

func (b *StoreBackend) ListNodes(ctx context.Context, runID string) ([]*store.Node, error) {
	return b.tenant.ListNodes(ctx, runID)
}

func (b *StoreBackend) ListEventsAfter(ctx context.Context, runID string, afterSeq int64, limit int) ([]store.Event, error) {
	return b.tenant.ListEventsAfter(ctx, runID, afterSeq, limit)
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
