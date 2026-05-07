package backend

import (
	"context"
	"io"

	"github.com/sparkwing-dev/sparkwing/v2/controller/client"
	"github.com/sparkwing-dev/sparkwing/v2/orchestrator/store"
	"github.com/sparkwing-dev/sparkwing/v2/pkg/storage"
)

// ClientBackend wraps a *client.Client (controller HTTP) for state
// and an optional storage.LogStore for log reads. Log reads bypass the
// controller and go straight to logStore.
type ClientBackend struct {
	c        *client.Client
	logStore storage.LogStore // nil means log read/stream return empty

	caps Capabilities
}

// NewClientBackend constructs a ClientBackend. logStore may be nil,
// in which case log endpoints return empty content.
func NewClientBackend(c *client.Client, logStore storage.LogStore) *ClientBackend {
	return &ClientBackend{c: c, logStore: logStore}
}

var _ Backend = (*ClientBackend)(nil)

// SetCapabilities binds the static capabilities body.
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
