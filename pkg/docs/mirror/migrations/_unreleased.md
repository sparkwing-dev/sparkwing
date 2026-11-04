# Migrating to the next release

This release upgrades the store to schema baseline 30 in place, and removes the
retired `warm_cpu_class_cores` credit setting. Take a store snapshot before the
first start; the sections below cover the other changes.

## Upgrade the store in place to schema baseline 30

Schema baseline 30 replaces the `warm_pool_desired` view. Auto warm capacity now
adds a node for slow starts only while a job is waiting for a fresh node. Before,
any slow start in the last hour kept adding a node every scaling cycle until the
pool reached its maximum, even with nobody waiting.

The default Auto start-time target becomes 20 seconds. New stores seed every
class at 20 seconds, and the upgrade moves each class still at the old
10-second default to 20. A class set to another target keeps it; set it back
with `sparkwing operator warm-capacity set --start-time-target 10s` if you
want the old behavior.

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

## Cloud nodes retire after an idle grace

The Cloud launcher retires a used node once it has seen the node unreserved and
empty for 60 seconds, instead of waiting for the node's run to end. It no longer
sends the `retire_run` query on launcher sync, and the controller no longer
accepts it or returns `run_terminal`.

Deploy the controller and launcher from the same release. An older launcher
against a newer controller has its retirement query refused, logs a retirement
warning at most once a minute, and leaves spent nodes to Karpenter
consolidation until it is upgraded. A newer launcher works against an older
controller, because it sends nothing new.

Jobs that become ready more than about a minute after their run's last node
emptied, such as a deploy after an approval, now boot a fresh node. To keep
nodes longer, pass `sparkwing-runner launch --idle-grace <duration>`; it must be
at least `--poll` plus 30 seconds.

## runners add --trust verified is gone

`sparkwing runners add --trust verified` was removed with no alias. A shared
runner enrolled with `--repos` already takes only work whose repository GitHub
or the controller's own fetch proved, and is now a trusted host whose jobs can
produce a pass that `sw.FromProvenRunAtCommit()` counts. A runner registered
with `--trust verified` keeps claiming only verified default-branch work, so
remove it with `sparkwing runners remove` and enroll it again with `--repos`
and no `--trust`, or `--trust any`.

## Team launcher pods are not fresh hosts

Pods a team's own launcher starts now take the trust of the launcher's
credential. Their jobs mint OIDC tokens with `runner_environment: persistent`,
so a cloud trust policy that requires `fresh` stops accepting them: allow
`persistent` for those subjects, or run the jobs on Sparkwing Cloud. Their
verified runs write the launcher's own cache scope, which other launchers and
Cloud do not restore, unless the team turns on `high_trust`.
