# Migrating to the next release

## Node logs reach the controller as segments

- **Before:** a Kubernetes Job, warm runner, self-hosted agent, GitHub
  Actions runner, launcher Job or the trigger runner's in-process nodes
  appended node logs to the logs service at their logs URL, as did a laptop
  run against a remote profile, and without one
  wrote them to a disk nobody could read and that went away with the pod.
  `sparkwing runs logs --profile` and `runs logs --grep --profile` read the
  logs service the profile or the controller named.
- **After:** against a controller that keeps segments, every one of those
  runners uploads each node's log as gzip segments plus a sealing manifest
  through URLs the controller issues, into the controller's cache bucket
  (`--cache-blob-store`) or, without one, its output directory, whether or
  not it has a logs URL. The dashboard and the CLI read every node through
  the controller, which serves segments first and the logs service for a
  node that wrote none, so logs the service already holds stay readable.
  Deleting a run or a team deletes its segments.
- **Operator steps:** keep the logs service running so its existing logs
  stay readable. On an S3 cache bucket, grant the controller
  `s3:GetLifecycleConfiguration` and add enabled lifecycle rules expiring
  `<cache prefix>/logs/r90/` after 90 days and `<cache prefix>/logs/r30/`
  after 30, plus `<cache prefix>/logs/r<days>/` after `<days>` for each
  team `--log-retention team=days` names; [self-hosting.md](../self-hosting.md#node-log-retention)
  has the command. Until a class has its rule, the controller issues no
  upload URLs into it and those nodes' runners keep writing to their logs
  URL, or their own disk without one. A controller that keeps segments on
  its own disk needs no rule and keeps them until their run is deleted.
  On a multi-team controller segment bytes count against the team's log
  storage.
- **Logs the service already holds:** run `sparkwing-controller migrate-logs`
  once against the service's root and archive and the controller's cache
  bucket and state database (see [self-hosting.md](../self-hosting.md#moving-the-logs-services-logs-to-segments))
  before you retire the service; logs not converted become unreadable.
- **Mixed versions:** upgrade the controller first. A runner that predates
  segments keeps writing to its logs URL, which the controller still reads
  through. A runner of this release against an older controller finds no
  segments advertised and writes to its logs URL, or locally without one,
  as before. Rolling the runners back returns their new logs to the logs
  service.

## The logs service is removed

This release ships no `sparkwing-logs`. Its steps replace the ones in
[Node logs reach the controller as segments](#node-logs-reach-the-controller-as-segments)
that keep the logs service running.

- **Before:** `sparkwing-logs` held node logs that a runner without
  segments, or a node from before segments, wrote; the controller read
  through it and announced it as `logs` in `GET /api/v1/services`. The
  runner-bundle chart deployed it, and runners, agents and profiles named
  its URL.
- **After:** the controller is the only place node logs go. It keeps them
  as segments in its cache bucket (`--cache-blob-store`) or, without one,
  its output directory; a controller with neither keeps no node logs, so a
  runner keeps the log on its own disk. Pipeline binaries built against any SDK
  keep logging unchanged: the runner serves their appends on loopback.
- **Before you upgrade:** scale the logs service and the controller to
  zero and run this release's `sparkwing-controller migrate-logs` once,
  `--dry-run` first, as a Job that mounts the logs PVC and the controller's
  data volume with the controller's credentials, as
  [self-hosting.md](../self-hosting.md#moving-the-logs-services-logs-to-segments)
  describes in order. It reads the service's volume and archive directly,
  records each converted log in the controller's state database, skips
  what it already converted, and can run again. It names and leaves
  unconverted any run it cannot convert whole, such as one the controller's
  database does not hold. Logs not converted become unreadable.
- **Helm:** delete `sparkwing-runner-bundle.logs` (or `logs` for a
  standalone runner bundle) and `controller.logs` from your values; a
  values file that still sets them fails to render. The upgrade removes the
  logs Deployment, Service and ServiceAccount; the logs PVC is kept by its
  resource policy, so delete it once the export is done:

  ```sh
  kubectl -n <namespace> delete pvc <release>-sparkwing-runner-bundle-logs
  ```

- **Controller:** drop `--logs-url` and `--logs-archive-store`, and delete
  the `logs-delete-token` file from `--credentials-dir` (or the
  `controller.credentialsSecret` key); nothing reads it. The storage pass
  counts and prunes logs from the segments, and deleting a team deletes its
  segments.
- **Runners and agents:** drop `--logs` from `sparkwing-runner runner` and
  `launch`, `--trigger-runner-logs-url` from `runner`, and the `logs:` key
  from an agent's `config.yaml`, which now fails to load with
  `field logs not found`. `sparkwing cluster runners add` has no `--logs`.
  Drop any `SPARKWING_LOGS_EGRESS_*` variables; nothing reads them.
- **Profiles:** a `logs: {type: controller}` surface takes no `url`; remove
  it.
- **Credentials and API:** the `logs.write` and `logs.delete` scopes are
  gone, and minting a token with either is refused; runner tokens carry
  `nodes.claim`, `triggers.claim`, `runs.state` and `secrets.read`.
  Existing tokens that list them keep working with the rest of their
  scopes. `GET /api/v1/runs/{id}/log-access`,
  `POST /api/v1/runs/{id}/nodes/{nodeID}/claim/validate`, the `logs` field
  of `GET /api/v1/services` and signed `kind=log` downloads are removed;
  read logs through `GET /api/v1/runs/{id}/logs/{node}`. The storage
  counter routes answer only the cache's operator token.
- **Laptop:** `sparkwing serve` runs no logs service and writes only
  `SPARKWING_CONTROLLER_URL` to `dev.env`; the dashboard reads node logs
  from the run directories, and `sparkwing runs prune` removes them through
  the dashboard.
- **Go packages:** `pkg/logs` and `pkg/storage/sparkwinglogs` are removed.
  Read node logs with `client.NodeLogs` from
  `pkg/controller/client`, and their completeness with `pkg/storage/logseg`.
- **Mixed versions:** upgrade the runners and the controller together. A
  runner from before segments has nowhere to send its logs once the
  service is gone, and keeps them on its own disk.

## S3 and Postgres state profiles are removed

`sparkwing run` executes on this machine and records to this machine's
store. It no longer takes `--profile`, and a profile's `state:` is
`sqlite` or `controller`. Shared run history lives on a controller:
`sparkwing pipeline trigger <pipeline> --profile P` runs a pipeline there.
This ends the temporary exception
[v0.67.0](v0.67.0.md#sparkwing-run-takes---profile-only-for-s3-or-postgres-state)
kept for S3 and Postgres state profiles.

| Before | After |
|---|---|
| `sparkwing run build --profile shared` with an S3 or Postgres `state:` | `sparkwing pipeline trigger build --profile prod` to run on a controller, or `sparkwing run build` to run here |
| `state: {type: s3, ...}` or `state: {type: postgres, ...}` in a profile | Delete the `state:` key, or replace the profile with a controller profile (`sparkwing cloud connect`). The config fails to load with `state.type: s3 is removed` (or `postgres`) and a pointer to this section. |
| `url_source: env:VAR` on any surface | Delete it; it named the Postgres state DSN. The config fails to load with `<surface>.url_source is removed`. |
| `mirror_local:` on a profile | Delete it. The config fails to load with `field mirror_local not found`. |
| `cache: {binaries: {...}}` | Delete the `binaries:` block. The config fails to load with `field binaries not found`. Runs fetch prebuilt binaries from the git cache `SPARKWING_GITCACHE_URL` names. |
| `sparkwing run X --sw-artifact-digest-backfill` | Delete the flag; it applied only to binaries fetched from a profile's cache. `run` refuses it as an unknown runner flag. |
| `sparkwing serve start --profile P [--no-local-store]` | `sparkwing serve start` for this machine's runs, or the controller's dashboard for shared ones. `--log-store` and `--artifact-store` remain. |
| `guards: {require: [profile:name=prod]}` | `profile:local`, `profile:controller` or an `arg:` token. A run never has a named profile, so the token never matched; a config naming it now fails to load. |
| `sparkwing runs list --profile shared` against an S3 or Postgres state profile | Nothing reads that history any more. The bucket or database is yours to keep or delete. |

- **Refusal:** `sparkwing run deploy --profile prod` fails before it
  compiles anything: `run: --profile is removed; sparkwing run executes on
  this machine against its own store, so run on the profile's controller
  with sparkwing pipeline trigger deploy --profile prod`. `--profile`
  before the verb fails the same way.
- **Hooks:** a hook installed before v0.67.0 with `--profile 'P'` on its
  `sparkwing run` lines fails until you reinstall it with `sparkwing
  pipeline hooks install`.
- **Mixed versions:** a pipeline binary built against this SDK refuses a
  run that an older CLI started with `--profile`, naming `sparkwing
  pipeline trigger`, rather than recording it locally. This CLI still
  drops an inherited `SPARKWING_PROFILE` and `SPARKWING_SECRETS_PROFILE`,
  so a pipeline binary from an older SDK cannot be pointed at a remote
  store either.
- **Controllers:** a controller on PostgreSQL is unchanged; only a laptop
  or CI run opening a shared database or bucket itself is removed. The
  `install/terraform/mode3-postgres` module now documents the controller's
  database alone.
- **Output:** a run no longer writes `runs/<id>/state.ndjson` into this
  machine's cache directory. `run_start` reports a `backends` block only
  for `--sw-local-only`, and never a `profile` block.
  `sparkwing configure profiles show -o json` drops `mirror_local`. The
  controller's health no longer reports `object_store.stalled`.
- **Go packages:** `pkg/storage/s3state` is removed. `pkg/controller`
  drops `Loopback`, `LoopbackState`, `LoopbackConcurrency` and
  `NewLoopback`; a local run's node processes reach the full controller.
  `pkg/backends` drops `TypePostgres`, `Spec.Binaries` and
  `Surfaces.BinaryCache` and adds `MigrationGuideURL`.
  `storeurl.OpenStateStoreFromSpec` opens `sqlite` and `controller` only.
  `pkg/localws` drops `Options.NoLocalStore`. `pkg/pipelines` drops
  `KindProfileName` and `GuardContext.ProfileName`.
