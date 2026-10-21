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
// database triggers refuse mutation of committed audit rows.
// Opening another schema version fails with recovery guidance. Elected recovery
// rechecks leases and selected work inside each transaction.
package store
