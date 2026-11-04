# Migrating to the next release

This release upgrades the store to schema baseline 30 in place, and removes the
retired `warm_cpu_class_cores` credit setting. Take a store snapshot before the
first start; nothing else needs to change unless a script still sends the
removed setting.

## Upgrade the store in place to schema baseline 30

Schema baseline 30 replaces the `warm_pool_desired` view. Auto warm capacity now
adds a node for slow starts only while a job is waiting for a fresh node. Before,
any slow start in the last hour kept adding a node every scaling cycle until the
pool reached its maximum, even with nobody waiting.

A v0.68.7 store (schema 29 with the v0.68.7 baseline digest) upgrades in place
the first time this release opens it, on SQLite and PostgreSQL. A v0.68.3 to
v0.68.6 store (schema 28) takes both steps in the same transaction. The upgrade
replaces the view and records version 30 and its digest together; on PostgreSQL
the migration advisory lock lets one of several controllers starting together
run it. PostgreSQL replaces the view in place, so the
`GRANT SELECT ON warm_pool_desired TO sparkwing_warm_reader` that KEDA relies on
survives and needs no reapplying.

An older binary refuses a schema 30 store, so take a snapshot first:

1. Stop the local daemon with `sparkwing daemon stop`, or stop the controllers
   using a server database.
2. Back up the store: copy `state.db` and any `state.db-wal` and `state.db-shm`
   files from the Sparkwing home, or take a PostgreSQL dump or snapshot.
3. Start this release. The first open upgrades the store.

To roll back, restore the snapshot and start the previous release.

Every other store is refused unchanged with a remedy, as for schema 29. See
[schema compatibility](../backup-restore.md#schema-compatibility).

## The warm_cpu_class_cores credit setting is removed

`GET /api/v1/credits` and `GET /api/v1/credits/settings` no longer report
`warm_cpu_class_cores`, and `PUT /api/v1/operator/credits/settings` refuses a
body that names it, as it refuses any unknown field. Nothing scaled from this
setting: per-class warm capacity (`sparkwing operator warm-capacity set` or the
console's **Warm capacity** page) is the only warm policy. Drop the field from
any script that sends it. A stored value is left in place and nothing reads it.
