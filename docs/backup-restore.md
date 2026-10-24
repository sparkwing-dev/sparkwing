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

## Schema compatibility

Keep the binary matching a database backup. This build does not upgrade older
schema baselines. A newer database needs its matching binary; rollback needs
a matching backup in a separate database.

<!-- BEGIN GENERATED SCHEMA ERRORS -->
```text
sparkwing: store schema version 23 is older than this binary (expects 24); back up the store and use a binary matching it; this build cannot upgrade it; only for a disposable store, delete <path> (or drop the database) and start again

sparkwing: store schema version 25 is newer than this binary (expects 24); start a binary matching the store, or restore a backup matching this binary into a separate database
```
<!-- END GENERATED SCHEMA ERRORS -->
