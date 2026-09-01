# Deployment modes

Sparkwing runs in four distinct deployment shapes, sharing one
codebase and one configuration file. The shape you pick determines
who else can see your runs, whether cross-runner caching coordinates,
and what infrastructure you have to host.

| Mode | Infrastructure | Shared dashboard | Coordinated cache | Triggers / approvals / debug pauses | Auth surface |
| --- | --- | --- | --- | --- | --- |
| Local | none | -- | -- | -- | filesystem |
| Shared object storage | object store | yes (read-only) | with CAS¹ | with CAS¹ | bucket IAM |
| Postgres + object storage | object store + Postgres | yes | yes | yes | DB roles + bucket IAM |
| Hosted controller | controller + DB + object store | yes | yes | yes | tokens / sessions |

¹ Mode 2 coordinates cross-runner caching, triggers, approvals, and
debug pauses over object-store conditional-write CAS where the bucket
enforces write preconditions (S3 today). Where it does not, cache
reservation degrades to last-write-wins, while triggers, approvals,
and debug pauses report not-supported and need Mode 3. See
[Mode 2](#mode-2-shared-object-storage).

Pick the lowest row that meets your requirements. The selection lives in
the profile you run under -- each profile in
`~/.config/sparkwing/profiles.yaml` carries a `state` / `cache` / `logs`
triple (see [Storage backends](backends.md)) -- and applies uniformly to
`sparkwing run`, `sparkwing-web`, and any cluster-side binaries.

## Mode 1: Local

SQLite under `~/.sparkwing/state.db`, with per-run logs under
`~/.sparkwing/runs/<runID>/`. Zero shared
infrastructure. This is the default behavior -- no profile is selected and
the built-in local sqlite + filesystem defaults apply -- when no
`--profile` is given and the project sets no `defaults.profile`.

For: a developer working on pipelines on their own laptop.

Tradeoff: nobody else can see what you ran.

No configuration needed. `sparkwing run hello` and
`sparkwing dashboard start` work out of the box.

## Mode 2: Shared object storage

Runners write their run state, cache blobs, and log streams to a
shared object store. The dashboard reads from the same bucket. No
database and no controller: cross-runner coordination runs over the
object store itself, through conditional-write compare-and-swap.

For: a small team that wants cross-runner visibility (laptops, CI,
GitHub Actions) without hosting a database.

Where the bucket enforces write preconditions, Mode 2 coordinates
across runners with no database -- cache reservation, pipeline
triggers, approvals, and debug pauses all work. Each is an
object-store record mutated under compare-and-swap (S3
`If-None-Match` / `If-Match`); a contended `.Memoize()` key elects one
leader and the rest coalesce onto its output, the same
exactly-one-runs shape as Mode 3.

S3 is the object store that enforces these preconditions today. The
`gcs` and `azure-blob` state types are recognized in configuration
but not yet implemented. Some S3-compatible gateways accept the
precondition headers and silently ignore them; a runner probes the
endpoint once and, when it finds the guarantee missing, falls back to
last-write-wins -- cache reservation degrades to "every runner
computes and uploads to the same content-addressed key" (safe by
construction), and triggers, approvals, and debug pauses report
not-supported, so reach for Mode 3 when you need them.

Tradeoff: coordination over one object is slower at the tail than a
database row lock. A heavily-contended key serializes its acquires
and releases as compare-and-swap retries against a single object; an
uncontended key touches it once. When that tail latency matters,
Mode 3's Postgres row locks are the upgrade.

If a runner's object store is briefly unreachable, run state writes
stage to a local SQLite outbox (`~/.sparkwing/outbox.db`, one per host,
honoring `SPARKWING_HOME`) and replay in order when connectivity
returns, so a transient blip neither fails the run nor loses state.
Cache and log writes are not buffered this way: a cache write that
can't reach the bucket surfaces the error, and the step recomputes on
a later run rather than reading a half-written result.

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

### What each runner needs beyond the profile

The profile carries two strings per surface -- `bucket` and `prefix`.
Everything else is environment, and every runner needs it:

| Variable | Required | What it does |
|---|---|---|
| `AWS_REGION` | Yes | The SDK resolves no endpoint without it. Unset is the default state of a runner that has static credentials and no `~/.aws/config`. |
| `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY`, or any other link in the standard AWS credential chain | Yes | Authenticates every read and write. Profiles hold no credential fields. |
| `SPARKWING_S3_ENDPOINT` | Only for non-AWS S3 | Points the SDK at MinIO, R2, or another S3-compatible endpoint. Setting it also forces path-style addressing, and it applies process-wide: every surface -- state, cache, logs, and the binaries cache -- goes to that endpoint, so one profile cannot mix a MinIO cache with a real-AWS state. |

Secrets are the exception to "no controller": the secrets surface
takes `controller`, `filesystem`, `env`, or `none`, and has no
object-store option. A Mode 2 team whose pipelines call `Secret()`
provisions those per host, or runs a controller for that surface
alone.

Run against it with `sparkwing run <pipeline> --profile shared`, then
point `sparkwing-web` at the same bucket:

```sh
sparkwing-web --state-spec=s3://my-org-sparkwing/state \
              --logs-spec=s3://my-org-sparkwing/logs \
              --artifacts-spec=s3://my-org-sparkwing/cache
```

This controller-free dashboard has no browser login backend. Keep it on a
trusted network. `--require-login` fails startup unless you also provide
`--controller URL` or select a profile with `controller.url`.

See [local-execution.md](local-execution.md#per-host-concurrency)
for the host-local concurrency gate that caps how many `sparkwing run`
processes a single machine admits at once. The gate is mode-agnostic
but matters most in Mode 2, where the state backend doesn't
incidentally serialize overlapping invocations the way Mode 1's SQLite
does.

## Mode 3: Postgres + object storage

Runners write run state to a shared Postgres database and caches /
logs to a shared object store. The `.Memoize()` DSL routes through
Postgres `concurrency_*` tables, so cross-runner reservation works
properly: N runners arriving at the same key elect one leader, the
rest coalesce and inherit the leader's output. Triggers, approvals,
and debug pauses all work.

For: a team that wants cross-runner reservation guaranteed by a
database row lock -- rather than dependent on the bucket's CAS support
and its tail latency under heavy contention -- but doesn't want to
host a controller process.

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

The Postgres state database is not a browser session backend. Keep this
controller-free dashboard on a trusted network, or provide a controller URL or
controller-bearing profile before enabling `--require-login`.

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

### One-click provisioning

A Terraform module under `install/terraform/mode3-postgres` stands up the
Postgres this mode needs in one `terraform apply`: the database (a single
RDS instance or an Aurora Serverless v2 cluster, picked by one knob), its
security group, and its subnet group across the private subnets you give
it. It writes the connection string to AWS Secrets Manager, so each runner
reads one secret into `SPARKWING_PG_URL` rather than hand-rolling a DSN.
You supply the VPC and private subnets; the module places the database
into networking you already run. Its README covers the variables and how
to point a runner at the result.

## Mode 4: Hosted controller

A central controller process owns Postgres + object-store credentials
and serves the dashboard. Runners (including laptops) talk to it
over HTTP and never see the underlying database. The controller
handles version translation; clients only need to match the
controller's API major version.

For: a team with untrusted CI, public webhooks, or a need to
decouple client and schema versions.

Tradeoff: you have to host the controller. Deploy the complete OSS stack with
the `sparkwing-full` Helm chart. Teams that do not need a shared controller can
keep pipeline execution and its dashboard local instead.

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
    controller:
      url: https://api.example.dev
      token: swu_xxx
    # state/cache/logs are implied by controller; reads/writes go through it.
```

A profile with a `controller:` block routes state and cache through that
controller over HTTP; the nested `token:` authenticates. Register or edit
profiles with `sparkwing configure profiles`. See
[Self-hosting](self-hosting.md) for the supported local and controller
deployment paths.

Logs are the exception. `sparkwing-controller` serves no `/api/v1/logs`
route -- `sparkwing-logs` is a separate binary on its own port -- so a
deployment that runs them as two processes must tell the controller
where the logs service is:

```sh
sparkwing-controller --logs-url https://sparkwing-logs.example.dev
```

The controller announces that URL on `GET /api/v1/services`, and a
runner with no `logs:` surface of its own posts there. Without the
announcement a runner falls back to the controller's own URL, which is
correct only when one process serves both -- the local dashboard mounts
the controller and the logs service on a single mux. When it is wrong,
every append gets a 404 and the run fails naming the missing service,
rather than losing the lines silently.

## Forcing local mode for a single run

`sparkwing run --sw-local-only <pipeline>` ignores any resolved profile
and pins state, cache, and logs to the local SQLite + filesystem layout,
regardless of which profile would otherwise apply. Useful for ad-hoc work
that shouldn't appear in the team dashboard, or for reproducing an issue
against a known-clean local state.

The flag only affects the one run; subsequent runs without the flag
resolve a profile normally again.

## Selecting a profile

Profile selection is explicit: pass `--profile NAME`, or set
`defaults.profile` in `.sparkwing/sparkwing.yaml` for the project's
default. With neither, no profile is active and the built-in local
defaults (Mode 1) apply.
There is no environment-based auto-selection -- a CI job picks its
profile by passing `--profile` in the run command (see
[ci-embedded.md](ci-embedded.md)).

## Choosing a mode

A practical decision order:

1. **One person, one laptop?** Mode 1.
2. **Multiple people on S3, fine with bucket-dependent
   coordination?** Mode 2 -- a bucket, a shared profile, and the
   per-runner environment that profile does not carry: `AWS_REGION`,
   credentials, and `SPARKWING_S3_ENDPOINT` for a non-AWS store. See
   [what each runner needs](#what-each-runner-needs-beyond-the-profile).
3. **Expensive cacheable steps where you want reservation guaranteed
   regardless of bucket CAS support, or low tail latency under heavy
   contention?** Mode 3 -- add a Postgres on top of Mode 2.
4. **Untrusted runners (public CI, customer pipelines) or you don't
   want every runner holding DB credentials?** Mode 4 -- host a
   controller.

You can move between modes by editing a profile (or selecting a
different one with `--profile`); pipeline code doesn't change.
