package storage

import (
	"context"
	"errors"
	"io"
	"time"
)

// ErrNotSupported reports an operation unavailable on the selected backend.
var ErrNotSupported = errors.New("storage: operation not supported")

// ErrBadCursor is returned by a log stream whose resume cursor is not one
// it issued for that run and node.
var ErrBadCursor = errors.New("storage: log stream cursor is not valid here")

type logCursorKey struct{}

// WithLogCursor returns ctx carrying the cursor a log stream resumes
// after: the last SSE event id a reader received from it, as the
// stream's Last-Event-ID.
func WithLogCursor(ctx context.Context, cursor string) context.Context {
	return context.WithValue(ctx, logCursorKey{}, cursor)
}

// LogCursor returns the cursor [WithLogCursor] put on ctx, or "".
func LogCursor(ctx context.Context) string {
	c, _ := ctx.Value(logCursorKey{}).(string)
	return c
}

// StoreUsage is how much a store holds: its total stored bytes and the
// number of objects those bytes sit in, measured at ObservedAt.
type StoreUsage struct {
	Bytes      int64
	Objects    int64
	ObservedAt time.Time
	// Partial marks a total the store stopped measuring early, because
	// it hit its page cap or the caller's deadline. A partial total is
	// short of the truth, so a caller bounding storage discards it
	// rather than acting on it.
	Partial bool
}

// UsageReporter exposes read-only stored-byte and object totals.
type UsageReporter interface {
	Usage(ctx context.Context) (StoreUsage, error)
}

// ReadOpts narrows a log read server-side. Zero values disable
// individual filters; an empty ReadOpts returns the full log.
type ReadOpts struct {
	Tail  int    // last N lines; 0 disables
	Head  int    // first N lines; 0 disables
	Lines string // "A:B" inclusive 1-indexed range
	Grep  string // substring filter (case-sensitive)
}

// LogStore reads and streams run and job logs through the controller.
type LogStore interface {
	// Read returns the (filtered) contents of one node's log.
	// Returns nil bytes (no error) if the node has no log yet.
	Read(ctx context.Context, runID, nodeID string, opts ReadOpts) ([]byte, error)

	// ReadRun returns concatenated banners + content for every node
	// in the run.
	ReadRun(ctx context.Context, runID string) ([]byte, error)

	// Stream opens a live tail using the cursor from WithLogCursor.
	Stream(ctx context.Context, runID, nodeID string) (io.ReadCloser, error)
}
