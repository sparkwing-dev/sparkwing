package backend

import (
	"context"
	"io"
)

// LiveLogReader is the optional capability a backend exposes when a
// running node's log is readable from the controller's in-memory ring.
// It is the live view for deployments whose durable logs surface has no
// live read of its own, such as an object store.
type LiveLogReader interface {
	// StreamNodeLiveLog opens a server-sent-events stream of the node's
	// live log from the given byte offset. A node with no live buffer
	// yields (nil, nil).
	StreamNodeLiveLog(ctx context.Context, runID, nodeID string, since int64) (io.ReadCloser, error)

	// ReadNodeLiveLog returns the live log bytes after since, the offset
	// to ask from next, and whether the node has finished writing. A
	// node with no live buffer yields ok false.
	ReadNodeLiveLog(ctx context.Context, runID, nodeID string, since int64) (data []byte, next int64, done, ok bool, err error)
}

// StreamLiveLog opens b's live stream for one node, and returns
// (nil, nil) when b has no live view or the node has no live buffer.
func StreamLiveLog(ctx context.Context, b Backend, runID, nodeID string, since int64) (io.ReadCloser, error) {
	lr, ok := b.(LiveLogReader)
	if !ok {
		return nil, nil
	}
	return lr.StreamNodeLiveLog(ctx, runID, nodeID, since)
}

// ReadLiveLog reads b's live buffer for one node, and reports ok false
// when b has no live view or the node has no live buffer.
func ReadLiveLog(ctx context.Context, b Backend, runID, nodeID string, since int64) (data []byte, next int64, done, ok bool, err error) {
	lr, isLive := b.(LiveLogReader)
	if !isLive {
		return nil, 0, false, false, nil
	}
	return lr.ReadNodeLiveLog(ctx, runID, nodeID, since)
}
