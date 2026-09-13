package backend

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"

	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type ClientBackend struct {
	c        *client.Client
	logStore storage.LogStore

	caps Capabilities

	liveLogWarn sync.Once
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

var _ LiveLogReader = (*ClientBackend)(nil)

// safety: a node with no buffer is the ordinary case once it finishes, so
// only that answer is silent; any other failure is reported once and the
// caller still falls back to the durable copy rather than failing the read.
func (b *ClientBackend) StreamNodeLiveLog(ctx context.Context, runID, nodeID string, since int64) (io.ReadCloser, error) {
	rc, err := b.c.StreamNodeLiveLog(ctx, runID, nodeID, since)
	if err != nil {
		b.noteLiveLogFailure("stream", runID, nodeID, err)
		return nil, nil
	}
	return rc, nil
}

func (b *ClientBackend) ReadNodeLiveLog(ctx context.Context, runID, nodeID string, since int64) ([]byte, int64, bool, bool, error) {
	chunk, err := b.c.ReadNodeLiveLog(ctx, runID, nodeID, since)
	if err != nil {
		b.noteLiveLogFailure("read", runID, nodeID, err)
		return nil, 0, false, false, nil
	}
	if chunk == nil {
		return nil, 0, false, false, nil
	}
	return []byte(chunk.Data), chunk.Next, chunk.Done, true, nil
}

func (b *ClientBackend) noteLiveLogFailure(op, runID, nodeID string, err error) {
	if errors.Is(err, client.ErrNoLiveLog) || errors.Is(err, context.Canceled) {
		return
	}
	b.liveLogWarn.Do(func() {
		slog.Warn("live log unavailable; reading the durable copy instead",
			"op", op, "run_id", runID, "node_id", nodeID, "error", err)
	})
}
