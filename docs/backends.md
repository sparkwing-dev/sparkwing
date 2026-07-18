# Storage backends

Backends are configured per profile, not in a separate file. A profile
declares four persistence surfaces plus how to reach a controller:

- **state** -- run records, plan snapshots, status
- **cache** -- content-addressed artifacts and compiled pipeline binaries
- **logs** -- per-job log streams
- **secrets** -- where `sparkwing.Secret` values resolve from

A profile fully describes "where do my runs go and what auth do I need
to get there." The same pipeline source runs on a laptop with the
filesystem, in CI with S3, or against a self-hosted controller -- you
switch by selecting a profile, not by editing a backends file. Laptop
profiles live in `~/.config/sparkwing/profiles.yaml`; project profiles
in `.sparkwing/sparkwing.yaml` (see [config-reference.md](config-reference.md)).

```yaml
# ~/.config/sparkwing/profiles.yaml
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
    controller: { url: https://api.example.dev, token: swu_xxx }
    # state/cache/logs are implied by the controller; reads/writes go through it.
```

Select a profile with `--profile NAME`; it applies wholesale. Without
`--profile`, the project's `defaults.profile` in `.sparkwing/sparkwing.yaml`
applies, falling back to the built-in local (sqlite + filesystem)
defaults. `sparkwing profile` prints which profile resolved and why.

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
  (Mode 2). Cache reservation, triggers, approvals, and debug pauses
  coordinate over object-store CAS where the bucket enforces write
  preconditions (S3 today; `gcs`/`azure-blob` recognized but not yet
  implemented). Where it does not, cache reservation degrades to
  last-write-wins, while triggers, approvals, and debug pauses report
  not-supported and need Mode 3.
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

The fourth surface, `secrets`, names where `sparkwing.Secret` values
resolve from (laptop dotenv or controller-stored); see
[security.md](security.md).

## Per-pipeline backend selection

A pipeline pins its backends by pointing at a profile that declares
them. Put the profile in a `profiles:` entry and set `profile:` on the
pipeline; that profile then applies to its runs (typically for an audit
requirement). Project profiles in `.sparkwing/sparkwing.yaml` are
validated on load and must declare all four surfaces -- secrets, state,
cache, and logs -- even when only one differs from the shared backends
(laptop `profiles.yaml` entries are not validated this way):

```yaml
# .sparkwing/sparkwing.yaml
profiles:
  prod-audit:
    secrets: { type: env }
    state:   { type: s3, bucket: prod, prefix: state }
    cache:   { type: s3, bucket: prod, prefix: cache }
    logs:    { type: s3, bucket: prod-audit-logs, prefix: "${RUN_ID}/" }
pipelines:
  - name: release-prod
    entrypoint: Release
    profile: prod-audit
```

The selected profile applies wholesale -- the pipeline's `profile:` when
set, otherwise the project's `defaults.profile`. Project defaults are
not layered in per surface; the chosen profile's own surfaces are what
apply. Any surface the chosen profile leaves unset falls back to the
built-in local default (sqlite state, no shared cache or logs), not to
another profile.

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
