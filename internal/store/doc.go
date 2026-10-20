// Package store owns persisted controller state, transactions and tenant authority.
// It is internal implementation code. HTTP contracts live in controllerwire,
// and pipeline authors use the sparkwing SDK.
//
// [Open] creates a SQLite or PostgreSQL store. [Store.ForTeam] supplies a
// tenant-scoped handle. [Store.AsOperator] supplies the deployment-wide handle
// used by controller composition and maintenance.
//
// A fresh database receives schema.sql and its schema version in one transaction.
// Opening another schema version fails with recovery guidance. Elected recovery
// rechecks leases and selected work inside each transaction.
package store
