# Deployment modes

Sparkwing runs in four distinct deployment shapes, sharing one
codebase and one configuration file. The shape you pick determines
who else can see your runs, whether cross-runner caching coordinates,
and what infrastructure you have to host.

| Mode | Infrastructure | Shared dashboard | Coordinated cache | Triggers / approvals / debug pauses | Auth surface |
| --- | --- | --- | --- | --- | --- |
| Local | none | -- | -- | -- | filesystem |
| Shared object storage | object store | yes (read-only) | -- | -- | bucket IAM |
| Postgres + object storage | object store + Postgres | yes | yes | yes | DB roles + bucket IAM |
| Hosted controller | controller + DB + object store | yes | yes | yes | tokens / sessions |

Pick the lowest row that meets your requirements. The selection lives in
the profile you run under -- each profile in
`~/.config/sparkwing/profiles.yaml` carries a `state` / `cache` / `logs`
triple (see [Storage backends](backends.md)) -- and applies uniformly to
`sparkwing run`, `sparkwing-web`, and any cluster-side binaries.

## Mode 1: Local

SQLite under `~/.sparkwing/state.db`, with per-run logs under
`~/.sparkwing/runs/<runID>/`. Zero shared
infrastructure. This is the default behavior -- the built-in `laptop`
profile -- when no `--profile` is given and no profile auto-detects.

For: a developer working on pipelines on their own laptop.

Tradeoff: nobody else can see what you ran.

No configuration needed. `sparkwing run hello` and
`sparkwing dashboard start` work out of the box.

## Mode 2: Shared object storage

Runners write their run state, cache blobs, and log streams to a
shared object store (S3, GCS, or Azure Blob). The dashboard reads
from the same bucket. No database, no controller, no shared
coordination.

For: a small team that wants cross-runner visibility (laptops, CI,
GitHub Actions) without hosting a database.

Tradeoff: cross-runner cache *reservation* is skipped. If two
runners arrive at the same uncached `.Cache()` key simultaneously,
both compute and both upload to the same content-addressed key. The
bytes are identical by construction, so last-write-wins is safe, but
the dashboard sees two independent runs that each did the same work.
Triggers, approvals, and debug pauses are unavailable in this mode --
they require cross-runner CAS that this mode deliberately omits.

If a runner's object store is briefly unreachable, state writes,
cache PUTs, and log appends stage to a local SQLite outbox
(`~/.sparkwing/outbox.db`) and replay when connectivity returns.

```yaml
# ~/.config/sparkwing/profiles.yaml
profiles:
  shared:
    state:
      type: s3
      bucket: my-org-sparkwing
      prefix: state
    cache:
      type: s3
      bucket: my-org-sparkwing
      prefix: cache
    logs:
      type: s3
      bucket: my-org-sparkwing
      prefix: logs
```

Run against it with `sparkwing run <pipeline> --profile shared`, then
point `sparkwing-web` at the same bucket:

```sh
sparkwing-web --state-spec=s3://my-org-sparkwing/state \
              --logs-spec=s3://my-org-sparkwing/logs \
              --artifacts-spec=s3://my-org-sparkwing/cache
```

See [local-execution.md](local-execution.md#per-host-concurrency)
for the host-local concurrency gate that caps how many `sparkwing run`
processes a single machine admits at once. The gate is mode-agnostic
but matters most in Mode 2, where the state backend no longer
incidentally serializes overlapping invocations the way SQLite does in
Mode 1.

## Mode 3: Postgres + object storage

Runners write run state to a shared Postgres database and caches /
logs to a shared object store. The `.Cache()` DSL routes through
Postgres `concurrency_*` tables, so cross-runner reservation works
properly: N runners arriving at the same key elect one leader, the
rest coalesce and inherit the leader's output. Triggers, approvals,
and debug pauses all work.

For: a team that has outgrown Mode 2's "everyone computes" semantics
on expensive cacheable steps, but doesn't want to host a controller
process.

Tradeoff: every runner needs Postgres credentials. The trust model
is "anyone with DB creds can write run state." Suitable for owned
infrastructure; not suitable for untrusted CI against shared infra
(use Mode 4 for that).

```yaml
# ~/.config/sparkwing/profiles.yaml
profiles:
  shared:
    state:
      type: postgres
      url_source: env:SPARKWING_PG_URL
    cache:
      type: s3
      bucket: my-org-sparkwing
      prefix: cache
    logs:
      type: s3
      bucket: my-org-sparkwing
      prefix: logs
```

`url_source: env:SPARKWING_PG_URL` reads the DSN from the named
environment variable so the literal connection string stays out of
yaml.

```sh
export SPARKWING_PG_URL="postgres://user:pass@db.example/sparkwing?sslmode=require"
sparkwing run hello
sparkwing-web --state-spec=postgres://...  # same DSN
```

### Schema versioning

Every runner records the schema version it operates against in a
`sparkwing_schema_version` row. On startup:

- Database at a *lower* version than the binary: the binary runs the
  missing migrations atomically inside one transaction. Concurrent
  runners against a fresh database coordinate via a Postgres
  advisory lock; exactly one runs the migration.
- Database at the *same* version: nothing to do.
- Database at a *higher* version than the binary: the binary refuses
  to start with a clear error naming both versions
  (`sparkwing: database is at schema version N; this binary expects
  M. Upgrade sparkwing or restore the database to a matching
  version.`).

This couples runner version to schema version. Stagger upgrades:
upgrade every runner *before* you upgrade the database, or run
mixed-version fleets briefly during a rollout. Mode 4 (hosted
controller) is the alternative that decouples client and schema
versions.

## Mode 4: Hosted controller

A central controller process owns Postgres + object-store credentials
and serves the dashboard. Runners (including laptops) talk to it
over HTTP and never see the underlying database. The controller
handles version translation; clients only need to match the
controller's API major version.

For: a team with untrusted CI, public webhooks, or a need to
decouple client and schema versions.

Tradeoff: you have to host the controller. The `self-hosting`
section covers a small VPS + docker-compose setup that fits most
teams.

The "owns Postgres" framing above describes the multi-tenant case;
the controller's state backend is pluggable. A single-instance
controller on one box can back its state with SQLite
(`~/.sparkwing/state.db`) and keep caches and logs on local disk --
the same storage layout as Mode 1, but fronted by the HTTP controller
so untrusted clients still never touch the store directly. Solo
operators and small teams don't need to stand up Postgres to run this
mode. Reach for Postgres + object storage when you outgrow a single
box -- more than one controller instance, or state and caches that
must survive that box.

```yaml
# ~/.config/sparkwing/profiles.yaml
profiles:
  prod:
    controller: https://api.example.dev
    token: swu_xxx
    # state/cache/logs are implied by controller; reads/writes go through it.
```

A profile with a `controller:` set routes state, cache, and logs through
that controller over HTTP; the `token:` authenticates. Register or edit
profiles with `sparkwing configure profiles`. See
[Self-hosting](self-hosting.md) for the controller deployment.

## Forcing local mode for a single run

`sparkwing run --sw-local-only <pipeline>` ignores any resolved profile
and pins state, cache, and logs to the local SQLite + filesystem layout,
regardless of which profile would otherwise apply. Useful for ad-hoc work
that shouldn't appear in the team dashboard, or for reproducing an issue
against a known-clean local state.

The flag only affects the one run; subsequent runs without the flag
resolve a profile normally again.

## Environment auto-detection

A profile can carry a `detect:` block so the same configuration covers
laptops, CI, and cluster contexts. When a profile's env condition matches,
it is auto-selected ahead of the project hint. The built-in `gha` profile
fires when `GITHUB_ACTIONS=true`; `kubernetes` fires when
`KUBERNETES_SERVICE_HOST` is set. Declaring a profile of the same name
overrides it per-field while preserving the built-in detect predicate.

```yaml
# ~/.config/sparkwing/profiles.yaml
default: laptop
profiles:
  laptop:
    state: { type: sqlite,     path: ~/.cache/sparkwing/state.db }
    cache: { type: filesystem, path: ~/.cache/sparkwing }
    logs:  { type: filesystem, path: ~/.cache/sparkwing/logs }

  gha:
    detect: { env_var: GITHUB_ACTIONS, equals: "true" }
    state: { type: s3, bucket: my-team-sparkwing, prefix: state/ }
    cache: { type: s3, bucket: my-team-sparkwing, prefix: cache/ }
    logs:  { type: s3, bucket: my-team-sparkwing, prefix: logs/  }
```

A laptop with no `GITHUB_ACTIONS` set falls through to the `default:`
profile (Mode 1). A GitHub Actions job auto-selects the `gha` profile and
picks up the shared S3 backends (Mode 2).

## Choosing a mode

A practical decision order:

1. **One person, one laptop?** Mode 1.
2. **Multiple people, no expensive cacheable steps?** Mode 2 -- a
   bucket and a shared profile is the entire setup.
3. **Multiple people, expensive cacheable steps where you want
   exactly-one-runs semantics?** Mode 3 -- add a Postgres on top of
   Mode 2.
4. **Untrusted runners (public CI, customer pipelines) or you don't
   want every runner holding DB credentials?** Mode 4 -- host a
   controller.

You can move between modes by editing a profile (or selecting a
different one with `--profile`); pipeline code doesn't change.
