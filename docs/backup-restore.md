# Backup, restore, and upgrade

## Restore

Stop every controller before replacing the database. Preserve the latest external
`credential-epoch` file, then run `sparkwing-controller restore prepare` against
the restored database with the same store and credential-directory flags as startup.
Preparation cancels restored unfinished scheduled runs and holds new schedule
admissions. Sessions, API tokens, runner and machine credentials, and API/local
schedule consent remain valid. `restore acknowledge` discards schedule catch-up
through its timestamp and lifts the hold without requiring new consent.
Preparation and startup refuse a database recorded under a different epoch,
such as one taken before an earlier sign-out or one served with a lost epoch
file, and name the file to restore.

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

Keep the binary matching a database backup. Before 1.0, schema changes may
require moving the local store aside or recreating a server database. The binary
refuses a mismatched store and leaves it unchanged. For an older local store it
names the move-aside remedy. A newer database needs its matching binary, and
rollback needs a matching backup in a separate database: an older binary refuses
an upgraded store, so take a snapshot before starting a release that upgrades it.

The recorded baseline digest must also match the binary at the same schema
version. A differing digest refuses startup with both digests and the backup and
reset remedy below. Stores without a digest are stamped only when every table,
view and explicit index declared by the baseline exists; missing objects refuse
startup without changing the store.

v0.68.3 ships schema baseline 28 on SQLite and PostgreSQL as a store reset, with
no forward migration. Back up and recreate stores at every other baseline
before starting it, following
[the v0.68.3 reset steps](migrations/v0.68.3.md#recreate-the-store-for-schema-baseline-28).

Schema 29 is the first forward migration. On SQLite and PostgreSQL, the first
open of a v0.68.3 or later 0.68 store (schema 28 with the v0.68.3 baseline
digest) adds `nodes.trusted_host` and records version 29 and its digest in one
transaction; existing jobs read as untrusted. On PostgreSQL the migration
advisory lock lets one of several controllers starting together run it. Every
other store is refused unchanged: a version older than 28, schema 28 with any
other digest, or a version newer than the binary. Take a snapshot first; rolling
back to 0.68 means restoring it. Staging and production upgrade the same way;
follow
[the in-place upgrade steps](migrations/v0.68.7.md#upgrade-the-store-in-place-to-schema-baseline-29).

Schema 30 replaces the `warm_pool_desired` view in the same way: the first open
of a v0.68.7 store (schema 29 with its baseline digest) replaces the view and
records version 30 and its digest in one transaction, and a schema 28 store
takes both steps. PostgreSQL replaces the view in place, so the SELECT grant for
KEDA's warm reader role survives. An older binary refuses a schema 30 store;
follow
[the schema 30 upgrade steps](migrations/_unreleased.md#upgrade-the-store-in-place-to-schema-baseline-30).

v0.68.0 replaced the store. It cannot read or import a store written by v0.67
or earlier, refuses one with the move-aside remedy below, and leaves it
unchanged. See
[Upgrading from v0.67](migrations/v0.68.0.md#upgrading-from-v067-v068-starts-a-new-store).

<!-- BEGIN GENERATED SCHEMA ERRORS -->
```text
sparkwing: store schema version 27 is older than this binary (expects 31); back up the store and use a binary matching it; this build cannot upgrade it; to start fresh, move <path> and any <path>-wal and <path>-shm files aside and start again

sparkwing: store schema version 27 is older than this binary (expects 31); back up the store and use a binary matching it; this build cannot upgrade it; to start fresh, point the server at an empty database and start again

sparkwing: store schema version 28 is older than this binary (expects 31); start the controller with this binary once to upgrade the v0.68.3 store in place, then run this again

sparkwing: store schema version 28 baseline digest "<recorded-digest>" is not the v0.68.3 baseline "75de685ed349678b0fa36694978890a2ddd491851d7a63c4c6740e81408b166a" this binary upgrades, so it left the store unchanged; open it once with Sparkwing v0.68.3 to record its digest, then start this binary; or back up the store and use a binary matching it; this build cannot upgrade it; to start fresh, move <path> and any <path>-wal and <path>-shm files aside and start again

sparkwing: store schema version 28 baseline digest "<missing>" is not the v0.68.3 baseline "75de685ed349678b0fa36694978890a2ddd491851d7a63c4c6740e81408b166a" this binary upgrades, so it left the store unchanged; open it once with Sparkwing v0.68.3 to record its digest, then start this binary; or back up the store and use a binary matching it; this build cannot upgrade it; to start fresh, point the server at an empty database and start again

sparkwing: store schema version 32 is newer than this binary (expects 31); start a binary matching the store, or restore a backup matching this binary into a separate database

sparkwing: store schema version 31 baseline digest "<recorded-digest>" differs from this binary's "<binary-digest>"; back up the store and use a binary matching it; this build cannot upgrade it; to start fresh, move <path> and any <path>-wal and <path>-shm files aside and start again

sparkwing: store schema version 31 baseline digest "<missing>" differs from this binary's "<binary-digest>"; back up the store and use a binary matching it; this build cannot upgrade it; to start fresh, point the server at an empty database and start again

sparkwing: ~/.sparkwing/state.db is a store from Sparkwing v0.67 or earlier (schema version 94); this release starts a new store and cannot read or import it, and left the old one unchanged; stop every older Sparkwing process still using it, such as a v0.67 dashboard or daemon, then move it aside and run again: mv ~/.sparkwing/state.db ~/.sparkwing/state.db.v067 (move ~/.sparkwing/state.db-wal and ~/.sparkwing/state.db-shm with it if they exist)

sparkwing: this database is a store from Sparkwing v0.67 or earlier (schema version 94); this release starts a new store and cannot read or import it, and left the old one unchanged; back up this database, then point the server at an empty database
```
<!-- END GENERATED SCHEMA ERRORS -->
