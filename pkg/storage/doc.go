// Package storage defines pluggable storage interfaces for the
// three kinds of data sparkwing pipelines persist: opaque blobs
// (compiled pipeline binaries, archived source trees, cache entries),
// per-node log streams, and run records.
//
// # Interfaces
//
// [ArtifactStore] is a content-addressed blob store. [LogStore]
// persists per-node log streams keyed by (runID, nodeID). [StateStoreOld]
// is the run-record handle (runs, nodes, steps, annotations,
// approvals, dispatches, debug pauses). The SQLite- and
// Postgres-backed *store.Store and the controller HTTP client satisfy it.
//
// # Implementations
//
// Concrete backends live as sibling subpackages, each suited to one
// execution mode:
//
//   - [fs] -- filesystem (laptop / local dev)
//   - [s3] -- any S3-compatible object store (artifacts only)
//
// Distributed byte clients use internal/blob through the controller signed data path.
//
// # Opening backends
//
// Production code does not instantiate implementations directly.
// Use [storeurl.OpenArtifactStore], which parses a backend URL
// (`fs:///...`, `s3://bucket/prefix`, ...) and returns the matching
// implementation behind the interface. State is
// opened from a backends.Spec via [storeurl.OpenStateStoreFromSpec].
//
// # Adding a backend
//
// Implement [ArtifactStore] and / or [LogStore], honoring the
// contracts documented on each interface (notably: idempotent
// Delete, ErrNotFound on missing Get, concurrent Put tolerated).
// External consumers can supply their own implementations without
// patching this module; the interfaces are the only stability promise.
package storage
