# Storage backends

There is no longer a `backends.yaml`. The three persistence surfaces --

- **state** -- run records, plan snapshots, status
- **cache** -- content-addressed artifacts and compiled pipeline binaries
- **logs** -- per-job log streams

-- are now declared per **profile** in `~/.config/sparkwing/profiles.yaml`.
A profile fully describes "where do my runs go and what auth do I need to
get there": the `state` / `cache` / `logs` triple plus any `controller` /
`token`. The same pipeline source runs on a laptop with the filesystem,
in GitHub Actions with S3, and against a self-hosted controller -- you
switch by selecting a profile, not by editing a backends file.

```yaml
# ~/.config/sparkwing/profiles.yaml
default: laptop
profiles:
  laptop:
    state: { type: sqlite }
    cache: { type: filesystem, path: ~/.cache/sparkwing }
    logs:  { type: filesystem, path: ~/.cache/sparkwing/logs }

  shared-team:
    state: { type: s3, bucket: team, prefix: state }
    cache: { type: s3, bucket: team, prefix: cache }
    logs:  { type: s3, bucket: team, prefix: logs }

  prod:
    controller: https://api.example.dev
    token: swu_xxx
    # state/cache/logs are implied by controller; reads/writes go through it.
```

Select a profile with `--profile NAME`; absent a flag, sparkwing resolves
one from the project hint, a matching per-profile `detect:` block, and the
`default:` entry. See `sparkwing profile` to print the resolved chain
without running anything.

## Backend types

| Surface | Types | Use |
| --- | --- | --- |
| `state` | `sqlite`, `postgres`, `s3`, `gcs`, `azure-blob`, `controller` | Run records, plan snapshots, status |
| `cache` | `filesystem`, `s3`, `gcs`, `azure-blob`, `controller` | Content-addressed artifact and compiled-binary store |
| `logs`  | `filesystem`, `s3`, `gcs`, `azure-blob`, `controller`, `stdout` | Per-job log stream persistence |

State backends correspond to deployment modes. See
[Deployment modes](deployment-modes.md) for when to pick each:

- `sqlite` -- laptop-local (Mode 1).
- `s3`, `gcs`, `azure-blob` -- per-run NDJSON state on a shared bucket
  (Mode 2). No coordinated cache or triggers.
- `postgres` -- shared database for cross-runner coordination
  (Mode 3). Triggers, approvals, debug pauses all work.
- `controller` -- runners talk to a hosted controller over HTTP
  (Mode 4). The controller owns the underlying database.

`mysql` is reserved in the schema but not implemented; declaring it
fails at run start with a clear error.

Required fields per type:

- `filesystem` -- `path`
- `s3`, `gcs`, `azure-blob` -- `bucket` (plus optional `prefix`)
- `postgres`, `mysql` -- exactly one of `url` or `url_source` (the
  latter names a secret in the resolved source)
- `controller`, `stdout`, `sqlite` -- no required fields

Recognized backend types that aren't implemented in the current
build surface a clear error at run start ("type X is recognized but
not implemented in this build") instead of silently falling back.

## Environment auto-detection

A profile can carry a `detect:` block; when its env condition matches,
that profile is auto-selected ahead of the project hint. This replaces
the old per-environment backend block:

```yaml
profiles:
  gha:
    detect: { env_var: GITHUB_ACTIONS, equals: "true" }
    state: { type: s3, bucket: team-ci, prefix: state }
    cache: { type: s3, bucket: team-ci, prefix: cache }
    logs:  { type: s3, bucket: team-ci, prefix: logs }
```

`gha` and `kubernetes` ship as built-in profiles that detect their
respective env vars; declaring a profile of the same name overrides it
per-field while keeping the built-in detect predicate.

## Per-target backend overrides

A pipeline target can still pin one surface, typically for an audit
requirement. The override lives under the pipeline definition in
`.sparkwing/sparkwing.yaml`:

```yaml
# .sparkwing/sparkwing.yaml
pipelines:
  - name: release-prod
    entrypoint: Release
    dispatch:
      runners: [prod-builders]
      backend:
        logs: { type: s3, bucket: prod-audit-logs, prefix: "${RUN_ID}/" }
```

Selection precedence per surface: the pipeline's `dispatch.backend`
overlay first, then the resolved profile's `state` / `cache` / `logs`.

## Pipeline binary distribution

Compiled pipeline binaries live in the cache surface under `bin/<hash>`.
On a cache hit, the orchestrator fetches and execs without recompiling.
An optional `cache.binaries` sub-spec isolates binaries to a separate
destination:

```yaml
profiles:
  shared-team:
    cache:
      type: filesystem
      path: ~/.cache/sparkwing
      binaries:
        type: s3
        bucket: sparkwing-binaries
        prefix: "${PIPELINE_NAME}/"
```

## Migrating from `backends.yaml`

For the before/after of moving `backends.yaml` `defaults:` and
`environments:` into per-profile specs, see the
[v0.5.0 migration guide](migrations/v0.5.0.md#profiles-absorb-all-backend-specs).
