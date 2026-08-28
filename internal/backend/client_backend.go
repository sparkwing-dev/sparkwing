package backend

import (
	"context"
	"io"

	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type ClientBackend struct {
	c        *client.Client
	logStore storage.LogStore

	caps Capabilities
}

func NewClientBackend(c *client.Client, logStore storage.LogStore) *ClientBackend {
	return &ClientBackend{c: c, logStore: logStore}
}

var _ Backend = (*ClientBackend)(nil)

func (b *ClientBackend) SetCapabilities(c Capabilities) { b.caps = c }

func (b *ClientBackend) Capabilities(context.Context) (Capabilities, error) {
	if b.caps.Mode == "" {
		return Capabilities{
			Mode:     "cluster",
			Storage:  CapabilitiesStorage{Artifacts: "custom", Logs: "sparkwinglogs", Runs: "controller"},
			Features: []string{"pipelines", "runs", "logs", "secrets", "approvals", "cross-pipeline-refs"},
		}, nil
	}
	return b.caps, nil
}

func (b *ClientBackend) ListRuns(ctx context.Context, f store.RunFilter) ([]*store.Run, error) {
	return b.c.ListRuns(ctx, f)
}

func (b *ClientBackend) GetRun(ctx context.Context, runID string) (*store.Run, error) {
	return b.c.GetRun(ctx, runID)
}

func (b *ClientBackend) ListNodes(ctx context.Context, runID string) ([]*store.Node, error) {
	return b.c.ListNodes(ctx, runID)
}

func (b *ClientBackend) ListEventsAfter(ctx context.Context, runID string, afterSeq int64, limit int) ([]store.Event, error) {
	return b.c.ListEventsAfter(ctx, runID, afterSeq, limit)
}

func (b *ClientBackend) ReadNodeLog(ctx context.Context, runID, nodeID string, opts ReadOpts) ([]byte, error) {
	if b.logStore == nil {
		return nil, nil
	}
	return b.logStore.Read(ctx, runID, nodeID, toStorageReadOpts(opts))
}

func (b *ClientBackend) StreamNodeLog(ctx context.Context, runID, nodeID string) (io.ReadCloser, error) {
	if b.logStore == nil {
		return nil, nil
	}
	return b.logStore.Stream(ctx, runID, nodeID)
}
