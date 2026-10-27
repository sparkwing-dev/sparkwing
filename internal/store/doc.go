// Package store owns persisted controller state, transactions and tenant authority.
// It is internal implementation code. HTTP contracts live in controllerwire,
// and pipeline authors use the sparkwing SDK.
//
// [Open] creates a SQLite or PostgreSQL store. [Store.ForTeam] supplies a
// tenant-scoped handle. [Store.AsOperator] supplies the deployment-wide handle
// used by controller composition and maintenance.
//
// A fresh database receives the translated schema and its version in one transaction.
// SQLite tables use STRICT storage. Audit events survive subject deletion, and
// database triggers reject every audit UPDATE and allow DELETE only after retention.
// Opening another schema version fails with recovery guidance. Recovery
// rechecks claim leases and selected work inside each transaction.
package store
