package orchestrator

import (
	"context"
	"io"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/logbatch"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/sparkwinglogs"
)

// LiveLogSink accepts one batch of a running node's log lines for the
// controller's live view. The durable copy is the logs surface's job.
type LiveLogSink interface {
	AppendNodeLiveLog(ctx context.Context, runID, nodeID string, data []byte) error
}

// safety: far shorter than the durable batcher's interval, because a
// watcher notices a two-second gap and a request bill does not.
const liveLogFlushInterval = 250 * time.Millisecond

const liveLogFlushBytes = 32 << 10

const liveLogPostTimeout = 10 * time.Second

// safety: adapting the sink to storage.LogStore is what lets the
// durable path's batcher coalesce live posts too. Reads stay with the
// durable surface.
type liveLogStore struct {
	sink LiveLogSink

	// safety: the node's claim fence travels in this context, and the
	// batcher flushes on its own ticker rather than inside a caller's
	// call, so the fence has to be held here or the controller rejects
	// the post.
	ctx context.Context
}

func (l *liveLogStore) Append(_ context.Context, runID, nodeID string, data []byte) error {
	ctx, cancel := context.WithTimeout(l.ctx, liveLogPostTimeout)
	defer cancel()
	return l.sink.AppendNodeLiveLog(ctx, runID, nodeID, data)
}

func (l *liveLogStore) Read(context.Context, string, string, storage.ReadOpts) ([]byte, error) {
	return nil, storage.ErrNotSupported
}

func (l *liveLogStore) ReadRun(context.Context, string) ([]byte, error) {
	return nil, storage.ErrNotSupported
}

func (l *liveLogStore) Stream(context.Context, string, string) (io.ReadCloser, error) {
	return nil, nil
}

func (l *liveLogStore) DeleteRun(context.Context, string) error { return nil }

var _ storage.LogStore = (*liveLogStore)(nil)

// safety: mirroring buys nothing when the durable surface already
// serves a live read, and doubles that surface's write path.
func liveLogsRedundant(durable storage.LogStore) bool {
	switch s := durable.(type) {
	case *sparkwinglogs.Store:
		return true
	case *logbatch.Store:
		return liveLogsRedundant(s.Delegate())
	default:
		return false
	}
}
