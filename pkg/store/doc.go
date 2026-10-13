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
// # Tenancy
//
// Every row that belongs to a team carries a team column; the tables
// that belong to the deployment -- the schema version, the meta
// bag and the executor fleet -- do not. The
// classification is the tenantTables and operatorTables lists in
// tenant.go, and a test fails on a table missing from both.
//
// [Store.ForTeam] returns a [*Tenant], the handle a request-serving
// caller uses: its methods take no team, every statement they issue
// carries team = ?, and the *Store inside it is unexported so no other
// package can widen it back. [Store.AsOperator] returns [*Operator],
// the named unscoped handle for reaping, dispatch and
// fleet-wide listing.
//
// The port is in progress. Every method already on Tenant still has a
// byte-identical twin on *Store that writes [DefaultTeam], because
// pkg/storage.StateStore and internal/backend.Backend name those
// methods; those twins are deleted in one change once every family has
// moved. Until then the compiler does not refuse an unscoped call --
// the doc comment on [Tenant] says what does, and how to port a family.
//
// # Schema
//
// A fresh database gets the baseline in schema_sqlite.sql or
// schema_postgres.sql, its sparkwing_schema_version row and its seed rows
// in one transaction; Postgres takes an advisory lock first so two openers
// cannot both create it. Open refuses a database that records any other
// schema version and tells the operator to delete it: no older store is
// upgraded. The two files describe one schema, and a test compares what
// each builds.
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
