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

## Local restart secrets and backups

Requester secrets for a local run are encrypted in `state.db` with the existing
local secrets key before intake returns. Only that run's live execution award
can read them. Controller restarts retain the values; reaching a terminal run
state deletes them in the same transaction. A missing or unreadable value
refuses the read and asks the requester to submit a new run.

The filesystem object URL signing key is also encrypted in store settings.
Outstanding upload and download URLs keep working after a controller restart,
subject to their original expiry. Preserve the local secrets key with the
store; losing it prevents the controller from opening its signing key. If
`SPARKWING_HOME` selects an isolated home and no key exists, set
`SPARKWING_SECRETS_KEY_FILE` to a private file inside that home before starting
the controller. This keeps key creation inside the selected home.

SQLite backups contain encrypted requester secrets and signing material.
Deleting a run's live secrets does not erase earlier backups or SQLite's freed
pages. Retain and protect backups under the same policy as other encrypted
secrets. Restoring one requires the key that encrypted it. After key rotation,
older backups still require the retired key; live rows use the current key.

## Schema compatibility

Keep the binary matching a database backup. Before 1.0, schema changes may
require a reset after preserving configured data as described below. The binary
refuses a mismatched store and leaves it unchanged. For an older local store it
names the move-aside remedy. A newer database needs its matching binary, and
rollback needs a matching backup in a separate database: an older binary refuses
an upgraded store.

Before a binary upgrades a local SQLite store, it copies the store beside it as
`<store>.schema-<N>-<YYYYMMDDTHHMMSSZ>.bak`, where N is the schema version
before the upgrade. The copy is taken under the upgrade's write lock, so it
matches the state the upgrade started from. It has mode 0600 on Linux and macOS
and the store directory's ACL on Windows, holds the same encrypted secrets as
the store. After a successful upgrade, only the two newest schema backups are
kept, ordered by modification time. A failed upgrade preserves all backups.
A pruning failure is logged and does not prevent the upgraded store from opening.
When the copy fails,
the binary refuses the upgrade and leaves the store unchanged; free disk space
and start again. To return to the older binary, stop the controller, move the
store and its `-wal` and `-shm` files aside, and copy the newest backup for that
binary's schema to the store path. Runs recorded after the backup are lost.
PostgreSQL stores are not copied: take a snapshot before starting a release that
upgrades one.

The recorded baseline digest must also match the binary at the same schema
version. A differing digest refuses startup with both digests and the backup and
reset remedy below. Stores without a digest are stamped only when every table,
view and explicit index declared by the baseline exists; missing objects refuse
startup without changing the store.

Schemas 30 through 35 upgrade in place to schema 36 when their recorded baseline
digest matches. The open transaction holds PostgreSQL's migration advisory lock,
so concurrent controllers apply each step once. Schema 30 is the oldest accepted
baseline. Earlier versions are refused unchanged.

A database reset must preserve secrets and their encryption keys, teams and
memberships, tokens, runner registrations, GitHub App installations, schedules
and subscriptions. Run history, logs and caches may be discarded. Billing records
require separate review. Do not reset a configured database until that data has
a verified recovery path.

<!-- BEGIN GENERATED SCHEMA ERRORS -->
```text
sparkwing: store schema version 29 is older than this binary (expects 36); back up the store and use a binary matching it; this build cannot upgrade it; preserve secrets, configured identities and automation before any reset, and review billing records separately; to start fresh, move <path> and any <path>-wal and <path>-shm files aside and start again

sparkwing: store schema version 29 is older than this binary (expects 36); back up the store and use a binary matching it; this build cannot upgrade it; preserve secrets, configured identities and automation before any reset, and review billing records separately; to start fresh, point the server at an empty database and start again

sparkwing: store schema version 30 is older than this binary (expects 36); start the controller with this binary once to upgrade the v0.69.2 store in place, then run this again

sparkwing: store schema version 30 baseline digest "<recorded-digest>" is not the v0.69.2 baseline "d0f42d8e52dd4ce999ace44a3767b6302d87e54c3b79dd544dfc5cd1ff8c0cf4" this binary upgrades, so it left the store unchanged; open it once with Sparkwing v0.69.2 to record its digest, then start this binary; or back up the store and use a binary matching it; this build cannot upgrade it; preserve secrets, configured identities and automation before any reset, and review billing records separately; to start fresh, move <path> and any <path>-wal and <path>-shm files aside and start again

sparkwing: store schema version 30 baseline digest "<missing>" is not the v0.69.2 baseline "d0f42d8e52dd4ce999ace44a3767b6302d87e54c3b79dd544dfc5cd1ff8c0cf4" this binary upgrades, so it left the store unchanged; open it once with Sparkwing v0.69.2 to record its digest, then start this binary; or back up the store and use a binary matching it; this build cannot upgrade it; preserve secrets, configured identities and automation before any reset, and review billing records separately; to start fresh, point the server at an empty database and start again

sparkwing: store schema version 37 is newer than this binary (expects 36); start a binary matching the store, or restore a backup matching this binary: stop the controller, move <path> and any <path>-wal and <path>-shm files aside, and copy the newest <path>.schema-36-*.bak to <path>; runs recorded since that backup are lost

sparkwing: store schema version 37 is newer than this binary (expects 36); start a binary matching the store, or restore a backup matching this binary into a separate database

sparkwing: store schema version 36 baseline digest "<recorded-digest>" differs from this binary's "<binary-digest>"; back up the store and use a binary matching it; this build cannot upgrade it; preserve secrets, configured identities and automation before any reset, and review billing records separately; to start fresh, move <path> and any <path>-wal and <path>-shm files aside and start again

sparkwing: store schema version 36 baseline digest "<missing>" differs from this binary's "<binary-digest>"; back up the store and use a binary matching it; this build cannot upgrade it; preserve secrets, configured identities and automation before any reset, and review billing records separately; to start fresh, point the server at an empty database and start again
```
<!-- END GENERATED SCHEMA ERRORS -->
