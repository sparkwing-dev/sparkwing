package storage

import (
	"context"
	"errors"
	"io"
	"time"
)

// ErrNotFound is returned by Get / Has-style reads when the requested
// key (or run/node pair) does not exist.
var ErrNotFound = errors.New("storage: not found")

// ErrListNotSupported is returned by ArtifactStore.List on backends
// without a native enumeration primitive.
var ErrListNotSupported = errors.New("storage: list not supported")

// ErrNotSupported is returned by any operation a partial
// implementation deliberately doesn't perform (e.g. Read on a
// write-only LogStore). Conformance suites under
// pkg/storage/conformance use errors.Is on this sentinel to skip
// subtests for operations the implementation has opted out of,
// rather than failing them.
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

// ErrPreconditionFailed reports an object that already exists.
var ErrPreconditionFailed = errors.New("storage: conditional write precondition failed")

// ArtifactStore reads and creates immutable raw objects for log storage and measurement.
type ArtifactStore interface {
	// Get returns a reader for the blob at key. The caller closes it.
	// Returns ErrNotFound if the key has never been written.
	Get(ctx context.Context, key string) (io.ReadCloser, error)

	// PutIfAbsent refuses an existing key with ErrPreconditionFailed.
	PutIfAbsent(ctx context.Context, key string, r io.Reader) error

	// Delete removes the blob at key. Idempotent: missing keys are
	// not an error.
	Delete(ctx context.Context, key string) error

	// List returns every key under prefix (inclusive). Order is
	// implementation-defined. Backends without a native enumeration
	// primitive return ErrListNotSupported.
	List(ctx context.Context, prefix string) ([]string, error)
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

// UsageReporter is the optional capability an [ArtifactStore] exposes
// when it can total its own contents. Measuring means enumerating the
// store, which object stores bill per thousand keys, so a caller
// measures on a schedule and never per request.
//
// Reach it through [Usage], which reports false for a store that
// cannot total itself.
type UsageReporter interface {
	// Usage totals everything the store holds under its configured
	// prefix.
	Usage(ctx context.Context) (StoreUsage, error)
}

// Usage totals what store holds, and reports false for a backend that
// cannot measure itself.
func Usage(ctx context.Context, store ArtifactStore) (StoreUsage, bool, error) {
	r, ok := store.(UsageReporter)
	if !ok {
		return StoreUsage{}, false, nil
	}
	u, err := r.Usage(ctx)
	return u, true, err
}

// ReadOpts narrows a log read server-side. Zero values disable
// individual filters; an empty ReadOpts returns the full log.
type ReadOpts struct {
	Tail  int    // last N lines; 0 disables
	Head  int    // first N lines; 0 disables
	Lines string // "A:B" inclusive 1-indexed range
	Grep  string // substring filter (case-sensitive)
}

// LogStore persists per-node log streams keyed by (runID, nodeID).
// Implementations store opaque bytes; callers control marshaling.
// Implementations live in the [fs] and [logseg] subpackages. [ReadOpts]
// narrows the slice returned by Read.
type LogStore interface {
	// Append writes data verbatim to the (runID, nodeID) log.
	Append(ctx context.Context, runID, nodeID string, data []byte) error

	// Read returns the (filtered) contents of one node's log.
	// Returns nil bytes (no error) if the node has no log yet.
	Read(ctx context.Context, runID, nodeID string, opts ReadOpts) ([]byte, error)

	// ReadRun returns concatenated banners + content for every node
	// in the run.
	ReadRun(ctx context.Context, runID string) ([]byte, error)

	// Stream opens a live-tail stream for one node's log, resuming after
	// the cursor [WithLogCursor] put on ctx, if any. Implementations
	// that don't support streaming return (nil, nil); the caller falls
	// back to polling Read.
	Stream(ctx context.Context, runID, nodeID string) (io.ReadCloser, error)

	// DeleteRun removes every log file for the run. Idempotent.
	DeleteRun(ctx context.Context, runID string) error
}
