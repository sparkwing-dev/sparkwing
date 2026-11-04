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
10-second default to 20. A class set to another target keeps it. To keep the
old behavior, pass `--start-time-target 10s` to `sparkwing operator
warm-capacity set` along with the class's other settings.

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
In the API document this removes
`components.schemas.internal.controllerwire.LauncherRun`,
`components.schemas.internal.controllerwire.LauncherSyncRequest.properties.retire_run`
and `components.schemas.internal.controllerwire.LauncherSyncResponse.properties.run_terminal`.

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

The `verified` value of the `--trust` flag on `sparkwing runners add` was removed with no alias. A shared
runner enrolled with `--repos` already takes only work whose repository GitHub
or the controller's own fetch proved, and is now a trusted host whose jobs can
produce a pass that `sw.FromProvenRunAtCommit()` counts. A runner registered
with `--trust verified` keeps claiming only verified default-branch work, so
remove it with `sparkwing runners remove` and enroll it again with `--repos`
and no `--trust`, or `--trust any`.

## Team launcher pods are not fresh hosts

Pods a team's own launcher starts, a runner enrolled with `--launcher`, now
take the trust of the launcher's credential instead of counting
as fresh hosts. Jobs on them mint OIDC tokens whose `runner_environment` claim
is `persistent`, and the subject's `runner_environment` segment changes with
it. A push to `main` run on such a pod was

```text
team:acme:repository_id:123456789:pipeline:deploy:trigger:push:runner:runner:runner_environment:fresh:ref:refs/heads/main
```

and is now

```text
team:acme:repository_id:123456789:pipeline:deploy:trigger:push:runner:runner:runner_environment:persistent:ref:refs/heads/main
```

Every other segment stays the same, `runner:runner` included. Jobs on Sparkwing
Cloud nodes and GitHub-hosted Actions runners, which their provider attests no
other run touched, keep `runner_environment:fresh`.

This affects a cloud trust policy that requires `runner_environment:fresh` (an
AWS `sub` condition, a GCP attribute condition on
`assertion.runner_environment`, a Vault `bound_claims` entry, and similar) for
jobs that run on a team's own launcher. After the upgrade those roles refuse
the token. Pick one:

- Admit the launcher's subjects: replace `runner_environment:fresh` with
  `runner_environment:persistent` in the subject, or with
  `runner_environment:*` to admit both, and in claim-based conditions accept
  `persistent`. Keep the repository, trigger and ref pinned.
- Move the deploying jobs to Sparkwing Cloud, whose tokens stay `fresh`, and
  leave the policy unchanged.

A policy that already uses `runner_environment:*` needs no change. Verified
runs on a team launcher's pods also write that launcher's own cache scope,
which other launchers and Cloud do not restore, unless the team turns on
`high_trust`.

## sparkwing run --sw-output is removed

`sparkwing run` no longer accepts `--sw-output`. Use the global output flag:

- Before: `sparkwing run build --sw-output json`
- After: `sparkwing run build -o json`

`-o` now drives a run's output, help and errors, ahead of
`SPARKWING_LOG_FORMAT` and terminal detection.

## doctor and info no longer report other installs

`sparkwing doctor -o json` drops the `install_conflict` check and its
`executable` and `competing_install` lines, and `sparkwing info -o json` drops
`executable.other_installs`. A script that read those fields should check the
shell instead: `which -a sparkwing` lists every copy on `PATH`, and
`sparkwing version` names the one that runs.

## Admission requests no longer attach to a parent lease

The daemon's `admission_request` message no longer carries
`parent_lease_token`, `parent_run_id` or `parent_node_id`, and the daemon no
longer attaches a child run to its parent's lease. No Sparkwing client sent
these fields, so nothing changes for the CLI or SDKs. A custom client that set
them should drop them; a child run is admitted on its own.
