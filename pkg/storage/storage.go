package storage

import (
	"context"
	"errors"
	"io"
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

// ErrPreconditionFailed is returned by [ConditionalWriter] writes when
// the required precondition does not hold: the object already exists
// for PutIfAbsent, or its current ETag differs from (or the object is
// absent for) PutIfMatch. It signals a lost compare-and-swap race, not
// a transport fault; the caller re-reads the current state and retries.
var ErrPreconditionFailed = errors.New("storage: conditional write precondition failed")

// ArtifactStore is a content-addressed blob store. Keys are
// caller-defined opaque strings. Implementations must tolerate
// concurrent Put on the same key (last write wins) without data
// corruption. Open via [storeurl.OpenArtifactStore]; implementations
// live in the [fs], [s3], and [sparkwingcache] subpackages.
type ArtifactStore interface {
	// Get returns a reader for the blob at key. The caller closes it.
	// Returns ErrNotFound if the key has never been written.
	Get(ctx context.Context, key string) (io.ReadCloser, error)

	// Put stores the bytes from r under key. r is read to EOF.
	Put(ctx context.Context, key string, r io.Reader) error

	// Has reports whether a blob exists at key without transferring
	// its contents.
	Has(ctx context.Context, key string) (bool, error)

	// Delete removes the blob at key. Idempotent: missing keys are
	// not an error.
	Delete(ctx context.Context, key string) error

	// List returns every key under prefix (inclusive). Order is
	// implementation-defined. Backends without a native enumeration
	// primitive return ErrListNotSupported.
	List(ctx context.Context, prefix string) ([]string, error)
}

// ETag is an opaque per-object version token. A successful
// [ConditionalWriter] write returns the new ETag; the same value,
// fed back to PutIfMatch, gates the next write on the object not
// having changed in between. ETags are only comparable within one
// backend instance; never persist one as cross-backend identity.
type ETag string

// ConditionalWriter is the optional compare-and-swap capability an
// [ArtifactStore] exposes when its provider honors object-store write
// preconditions (S3 If-None-Match/If-Match, GCS generation-match,
// Azure Blob ETag preconditions). It turns a shared bucket into a
// coordination primitive: many writers serialize on one key through
// read-modify-CAS retry loops, with no database or lock service.
//
// Detect the capability before relying on it. Not every backend
// implements the interface, and not every endpoint that does actually
// enforces the preconditions -- some S3-compatible gateways accept the
// headers and silently ignore them. [Conditional] reports the static
// type capability; ConditionalWritesSupported probes the live
// endpoint. When either reports false, fall back to last-write-wins
// [ArtifactStore.Put]; a CAS loop against an endpoint that ignores
// preconditions would hand out unsafe locks.
type ConditionalWriter interface {
	// GetWithETag returns a reader for the object at key together with
	// its current ETag. The caller closes the reader. Returns
	// ErrNotFound if the key has never been written.
	GetWithETag(ctx context.Context, key string) (io.ReadCloser, ETag, error)

	// PutIfAbsent writes r at key only when no object exists there,
	// returning the new ETag. It returns ErrPreconditionFailed,
	// without writing, when an object already exists.
	PutIfAbsent(ctx context.Context, key string, r io.Reader) (ETag, error)

	// PutIfMatch writes r at key only when the current object's ETag
	// equals expect, returning the new ETag. It returns
	// ErrPreconditionFailed, without writing, when the ETag differs
	// (a concurrent writer won the race) or the object is absent.
	PutIfMatch(ctx context.Context, key string, r io.Reader, expect ETag) (ETag, error)

	// ConditionalWritesSupported reports whether the configured
	// endpoint actually enforces write preconditions. Implementations
	// probe once and memoize. A false result means the caller must
	// fall back to last-write-wins.
	ConditionalWritesSupported(ctx context.Context) (bool, error)
}

// Conditional returns the [ConditionalWriter] view of store and true
// when the backend type supports compare-and-swap writes. A false
// result means the caller must fall back to last-write-wins. Callers
// that must also tolerate endpoints which advertise but ignore
// preconditions follow up with
// [ConditionalWriter.ConditionalWritesSupported].
func Conditional(store ArtifactStore) (ConditionalWriter, bool) {
	cw, ok := store.(ConditionalWriter)
	return cw, ok
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
// Open via [storeurl.OpenLogStore]; implementations live in the
// [fs], [s3], [sparkwinglogs], and [stdoutlogs] subpackages. [ReadOpts]
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

	// Stream opens a live-tail stream for one node's log.
	// Implementations that don't support streaming return (nil, nil);
	// the caller falls back to polling Read.
	Stream(ctx context.Context, runID, nodeID string) (io.ReadCloser, error)

	// DeleteRun removes every log file for the run. Idempotent.
	DeleteRun(ctx context.Context, runID string) error
}
