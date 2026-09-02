// Package store is the persisted run / node / event data model
// shared between the orchestrator engine, the controller HTTP
// surface, and dashboard readers. Stability promise: this is the
// public data model for sparkwing pipelines; types here are part of
// the SDK surface and version under module SemVer (see VERSIONING.md).
//
// # Opening a store
//
// [Open] returns a [*Store] backed by a SQLite database with WAL
// journaling. The store serializes writes via a single open
// connection; callers can hold one *Store for the process lifetime
// and share it across goroutines.
//
// # Migrations
//
// A schema version's statements and its sparkwing_schema_version row
// commit together, and the version that reaches the newest schema
// carries the min-binary-version stamp in that same transaction.
// SQLite wraps each version separately; Postgres wraps every pending
// version in one advisory-locked transaction and then, after
// releasing that lock, runs the run-annotation backfill row by row.
// A version that fails partway leaves neither its statements nor its
// version stamp, so the next open retries it against a clean schema.
// Migration code therefore takes the transaction it is handed rather
// than starting its own or reaching for the *Store query helpers: the
// SQLite handle allows one open connection, so a stray *Store query
// deadlocks against the migration's own transaction.
//
// # Primary records
//
// [Run] is the per-pipeline-invocation row (status, trigger, git
// snapshot, retry / replay lineage, invocation map). Nodes and
// events hang off it via the methods on *Store; concurrency
// admission lives in [ConcurrencyState], [ConcurrencyHolder], and
// [ConcurrencyWaiter]. [Secret] persists named secret material.
// [Session] / [User] back the dashboard's auth surface.
package store
