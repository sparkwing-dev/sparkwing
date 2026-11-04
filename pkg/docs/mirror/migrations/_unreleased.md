# Migrating to the next release

## Upgrade the store in place to schema baseline 29

Schema baseline 29 is the first forward migration. A store at schema 28 with
the v0.68.3 baseline digest, written by v0.68.3 or a later 0.68 release,
upgrades in place the first time this release opens it, on SQLite and
PostgreSQL. The upgrade adds `nodes.trusted_host`, with existing jobs read as
untrusted, and records version 29 and its digest in the same transaction. On
PostgreSQL the migration advisory lock lets one of several controllers starting
together run it.

An older binary refuses the upgraded store, so take a snapshot first:

1. Stop the local daemon with `sparkwing daemon stop`, or stop the controllers
   using a server database.
2. Back up the store: copy `state.db` and any `state.db-wal` and `state.db-shm`
   files from the Sparkwing home, or take a PostgreSQL dump or snapshot.
3. Start this release. The first open upgrades the store.

To roll back, restore the snapshot and start the previous release.

Every other store is refused unchanged with a remedy: a version older than 28,
schema 28 with any other digest (open it once with v0.68.3 so it records its
digest, or recreate it), and a version newer than the binary. See
[schema compatibility](../backup-restore.md#schema-compatibility).

## Run handle files carry no status

`sparkwing run PIPELINE --sw-run-handle-file PATH` writes the handle once at intake. It
no longer includes `status`, which stayed `pending` after the run finished. The
file now holds only `schema_version`, `run_id` and `pipeline`.

A script that read `status` from the handle reads `run_id` and asks for the
current state instead:

```bash
run_id="$(jq -r .run_id /tmp/build-run.json)"
sparkwing runs status --run "$run_id" -o json
```
