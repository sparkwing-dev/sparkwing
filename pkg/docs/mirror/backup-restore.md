# Backup, restore, and upgrade

## Restore

Recovery is manual; follow the operator runbook kept outside this repository.

## Schema compatibility

Keep the binary matching a database backup. This build does not upgrade older
schema baselines. A newer database needs its matching binary; rollback needs
a matching backup in a separate database.

<!-- BEGIN GENERATED SCHEMA ERRORS -->
```text
sparkwing: store schema version 19 is older than this binary (expects 20); back up the store and use a binary matching it; this build cannot upgrade it; only for a disposable store, delete <path> (or drop the database) and start again

sparkwing: store schema version 21 is newer than this binary (expects 20); start a binary matching the store, or restore a backup matching this binary into a separate database
```
<!-- END GENERATED SCHEMA ERRORS -->
