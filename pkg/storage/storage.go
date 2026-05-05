// Package storage defines pluggable storage interfaces for the two
// kinds of data sparkwing pipelines persist: opaque blobs (compiled
// pipeline binaries, archived source trees) and per-node log streams.
//
// Three execution modes target different default backends:
//
//	local         filesystem            filesystem
//	ci-embedded   S3 (or compatible)    S3 (or compatible)
//	distributed   sparkwing-cache HTTP  sparkwing-logs HTTP
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

// ArtifactStore is a content-addressed blob store. Keys are
// caller-defined opaque strings. Implementations must tolerate
// concurrent Put on the same key (last write wins) without data
// corruption.
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
