# Backup, restore, and upgrade

## Restore

Stop every controller before replacing the database. Preserve the latest external
`credential-epoch` file, then run `sparkwing-controller restore prepare` against
the restored database with the same store and credential-directory flags as startup.
Preparation cancels restored unfinished scheduled runs and holds new schedule
admissions. Sessions, API tokens, runner and machine credentials, and API/local
schedule consent remain valid. `restore acknowledge` discards schedule catch-up
through its timestamp and lifts the hold without requiring new consent.

For a security incident, such as a stolen database or leaked keys, use
`restore prepare --sign-out-everyone`. It rotates the external epoch and
invalidates all earlier credentials and API/local schedule consent. Supply a
fresh bootstrap seed, reissue credentials, and rearm those schedules.

A backup can revive credentials revoked after it was taken. Keep the controller's
audit log stream outside the database and reapply revocations, member removals
and demotions since the backup using its token prefixes, account IDs, machine
IDs and teams. Session logout and replacement records include `session_digest`,
`revoked_account_id` and `revoked_team` for the session that ended, so another
session for the same account can stay valid. Identity unlink adds
`kept_session_digest` and records each ended session on its own audit line.
Payment recovery and targeted revocation recovery are manual. Follow the
operator runbook kept outside this repository.

After a restore, Cloud nodes may still hold orphan reservations or bindings for
awards the restored controller no longer recognizes. The launcher excludes those
nodes and quarantines them when their protected team identity matches: it cordons
the node and adds the `sparkwing.dev/quarantined` taint and annotation. Healthy
nodes can still take work. Inspect the launcher logs and the node's quarantine
annotation, then terminate the node so fresh capacity replaces it. To reuse a node,
first remove the orphan workload and its stale reservation or binding, then remove
the quarantine taint and annotation and uncordon it. Uncordoning alone does not
make it eligible again. Do not clear a live award's reservation or binding.

## Schema compatibility

Keep the binary matching a database backup. This build does not upgrade older
schema baselines. A newer database needs its matching binary; rollback needs
a matching backup in a separate database.

v0.68.0 replaced the store. It cannot read or import a store written by v0.67
or earlier, refuses one with the move-aside remedy below, and leaves it
unchanged. See
[Upgrading from v0.67](migrations/_unreleased.md#upgrading-from-v067-v068-starts-a-new-store).
Releases after v0.68.0 ship schema changes as forward migrations that upgrade
the store in place.

<!-- BEGIN GENERATED SCHEMA ERRORS -->
```text
sparkwing: store schema version 24 is older than this binary (expects 25); back up the store and use a binary matching it; this build cannot upgrade it; only for a disposable store, delete <path> and start again

sparkwing: store schema version 24 is older than this binary (expects 25); back up the store and use a binary matching it; this build cannot upgrade it; only for a disposable store, drop the database and start again

sparkwing: store schema version 26 is newer than this binary (expects 25); start a binary matching the store, or restore a backup matching this binary into a separate database

sparkwing: ~/.sparkwing/state.db is a store from Sparkwing v0.67 or earlier (schema version 94); this release starts a new store and cannot read or import it, and left the old one unchanged; move it aside and run again: mv ~/.sparkwing/state.db ~/.sparkwing/state.db.v067 (move ~/.sparkwing/state.db-wal and ~/.sparkwing/state.db-shm with it if they exist)

sparkwing: this database is a store from Sparkwing v0.67 or earlier (schema version 94); this release starts a new store and cannot read or import it, and left the old one unchanged; back up this database, then point the server at an empty database
```
<!-- END GENERATED SCHEMA ERRORS -->
