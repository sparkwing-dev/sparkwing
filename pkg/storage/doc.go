// Package storage defines pluggable storage interfaces for the
// three kinds of data sparkwing pipelines persist: opaque blobs
// (compiled pipeline binaries, archived source trees, cache entries),
// per-node log streams, and run records.
//
// # Interfaces
//
// [ArtifactStore] is a content-addressed blob store. [LogStore]
// persists per-node log streams keyed by (runID, nodeID). [StateStore]
// is the run-record handle (runs, nodes, steps, annotations,
// approvals, dispatches, debug pauses). The SQLite-backed
// *store.Store satisfies it today; Postgres, HTTP (controller), and
// object-store NDJSON backends slot in behind the same interface.
//
// # Implementations
//
// Concrete backends live as sibling subpackages, each suited to one
// execution mode:
//
//   - [fs] -- filesystem (laptop / local dev)
//   - [s3] -- any S3-compatible object store (ci-embedded, distributed)
//   - [sparkwingcache] -- the sparkwing-cache HTTP service (distributed)
//   - [sparkwinglogs] -- the sparkwing-logs HTTP service (distributed)
//   - [stdoutlogs] -- write-only stream to process stdout (ephemeral CI)
//
// Default backend per execution mode:
//
//	local         filesystem            filesystem
//	ci-embedded   S3 (or compatible)    S3 (or compatible)
//	distributed   sparkwing-cache HTTP  sparkwing-logs HTTP
//
// # Opening backends
//
// Production code does not instantiate implementations directly.
// Use [storeurl.OpenArtifactStore] / [storeurl.OpenLogStore], which
// parse a backend URL (`fs:///...`, `s3://bucket/prefix`, ...) and
// return the matching implementation behind the interface. State is
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
