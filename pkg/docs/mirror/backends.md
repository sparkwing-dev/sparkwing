# Storage backends

Backends are configured per profile, not in a separate file. A profile
declares two surfaces plus how to reach a controller:

- **state** -- run records, plan snapshots, status
- **secrets** -- where `sparkwing.Secret` values resolve from

Node logs and artifacts follow the state: a sqlite profile reads them from
this machine's run directories and cache, and a controller profile reads
them from the controller. A profile that names `logs` or `cache` fails to
load with `field logs not found` or `field cache not found`.

A profile describes where a command reads and what auth it needs to get
there. `sparkwing pipeline trigger <pipeline> --profile NAME` runs a
pipeline on the profile's controller, and read verbs such as
`sparkwing runs list --profile NAME` read the profile's history.
`sparkwing run` executes on this machine against its local surfaces and
takes no `--profile`. Laptop
profiles live in the `profiles` section of `~/.config/sparkwing/config.yaml`
(see [Machine settings](machine-config.md)); project profiles
in `.sparkwing/sparkwing.yaml` (see [config-reference.md](config-reference.md)).

```yaml
# ~/.config/sparkwing/config.yaml
profiles:
  laptop:
    state: { type: sqlite }

  prod:
    controller: { url: https://api.example.dev, token: swu_xxx }
    # state is implied by the controller; reads go through it.
```

Select a profile with `--profile NAME`; it applies wholesale. Without
`--profile`, a command uses this machine's local store; no environment variable or project default selects a profile. `sparkwing configure profiles show` prints which profile resolved and why.

## Backend types

| Surface | Types | Use |
| --- | --- | --- |
| `state` | `sqlite`, `controller` | Run records, plan snapshots, status |
| `secrets` | `controller`, `filesystem`, `env`, `none` | Where `sparkwing.Secret` values resolve from |

Each state backend is one deployment shape. See
[Deployment modes](deployment-modes.md) for when to pick each:

- `sqlite` -- the local path; the default when no profile is selected.
- `controller` -- commands reach a hosted controller over HTTP,
  Sparkwing Cloud included. The controller owns the underlying database,
  SQLite or PostgreSQL.

`s3` and `postgres` state are removed: a profile that declares either
fails to load, naming the key. Shared run history lives on a controller;
the migration guide's "S3 and Postgres state profiles are removed" section
has the steps. Any other type fails with an error naming the types its surface accepts.

Local execution is process-per-node. A node body runs in its own process
of the pipeline binary and reaches run state through the controller the
dispatcher mounts on loopback for the run, over this machine's SQLite
store.

Required fields per type:

- `filesystem` -- `path`
- `controller` requires `controller: <profile-name>` or `url:`. A profile with a sibling `controller:` block inherits that profile name.
- `sqlite` -- no required fields

Recognized backend types that aren't implemented in the current
build surface a clear error at run start ("type X is recognized but
not implemented in this build") instead of silently falling back.

The fourth surface, `secrets`, names where `sparkwing.Secret` values
resolve from (this machine's local secret store or controller-stored); see
[security.md](security.md). A local run reads this machine's secret store
(`sparkwing secrets set`); controller secrets reach only runs executing on
the controller, which select that pipeline's scoped values. A standalone
resolver call without a run binding retains unscoped lookup behavior.

## Pinning a pipeline to a profile

`profile:` on a pipeline entry pins that pipeline to a controller
profile:

```yaml
# .sparkwing/sparkwing.yaml
pipelines:
  - name: release-prod
    entrypoint: Release
    profile: prod
```

A pinned pipeline runs only on that profile's controller.
`sparkwing run release-prod` refuses it (`pipeline release-prod is pinned
to profile prod; use sparkwing pipeline trigger release-prod --profile
prod`), and so do detached runs, crons, and local child pipelines.
`sparkwing pipeline trigger` requires `--profile` and refuses any profile
other than `prod`. `sparkwing pipeline describe`
reports the pin as `pinned_profile` in JSON and a `pinned:` line in text,
and `sparkwing pipeline plan` prints a `Pinned:` line.

The pin guards against sending a pipeline to the wrong place by mistake.
It is not a security boundary: the controller decides who may run a
pipeline and which secrets its runs see.

## Pipeline binary distribution

Local runs compile into this machine's binary cache and reuse those bytes
until the source inputs change. Remote binary transfers use the controller's
signed data path. See [Storage](storage.md).

## Migrating from `backends.yaml`

For the before/after of moving `backends.yaml` `defaults:` and
`environments:` into per-profile specs, see the
[v0.5.0 migration guide](migrations/v0.5.0.md#profiles-absorb-all-backend-specs).
