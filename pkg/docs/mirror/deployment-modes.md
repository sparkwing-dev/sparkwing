# Deployment modes

Two paths carry a reader: **Local** and **Sparkwing Cloud**.
[Getting started](getting-started.md) walks both. This page is the
storage-and-coordination taxonomy underneath them, for a team that hosts
its own state, cache, or controller.

| Shape | Infrastructure | Shared dashboard | Coordinated cache | Triggers / approvals / debug pauses | Auth surface |
| --- | --- | --- | --- | --- | --- |
| Local | none | -- | -- | -- | filesystem |
| Sparkwing Cloud | none (hosted) | yes | yes | yes | tokens / sessions |
| Self-hosted controller | controller + DB + object store | yes | yes | yes | tokens / sessions |
| Shared object storage | object store | yes (read-only) | with CAS¹ | approvals + pauses, with CAS¹ | bucket IAM |
| Postgres + object storage | object store + Postgres | yes | yes | yes | DB roles + bucket IAM |

¹ Shared object storage coordinates cross-runner caching, approvals, and
debug pauses over object-store conditional-write CAS where the bucket
enforces write preconditions (S3 today). Where it does not, cache
reservation degrades to last-write-wins, while approvals and debug pauses
report not-supported and need Postgres. Pipeline triggers report
not-supported on a bucket whatever it supports, and need Postgres or a
controller. See [shared object storage](#shared-object-storage).

The selection lives in the profile you run under -- each profile in
`~/.config/sparkwing/profiles.yaml` carries a `state` / `cache` / `logs`
triple (see [Storage backends](backends.md)) -- and applies uniformly to
`sparkwing run`, `sparkwing-web`, and any cluster-side binaries.

## Local

SQLite under `~/.sparkwing/state.db`, with per-run logs under
`~/.sparkwing/runs/<runID>/`. Zero shared
infrastructure. This is the default behavior -- no profile is selected and
the built-in local sqlite + filesystem defaults apply -- when no
`--profile` is given and the project sets no `defaults.profile`.

For: a developer working on pipelines on their own laptop, and the other
machines that developer owns. `sparkwing run <pipeline> --sw-fleet` hands
nodes of one foreground run to helpers provisioned with
[`sparkwing fleet`](cli-fleet.md), which keeps the run local to your own
hardware and needs no controller.

Tradeoff: nobody else can see what you ran.

No configuration needed. `sparkwing run hello` and
`sparkwing serve start` work out of the box. The path stays open with no
network once the first build has fetched its modules; see
[offline after the first build](getting-started.md#offline-after-the-first-build).

## Sparkwing Cloud

Sparkwing Cloud is the hosted controller. A controller owns the shared
dashboard, run history, scheduling, webhooks, and tokens; machines reach
it over outbound HTTPS and hold no database credential. The same command
reaches any controller you can reach, including one your team runs:

```bash
sparkwing cloud connect --controller https://api.sparkwing.example --token-stdin
```

It stores the token you pass on stdin, writes the profile, and prints the
dashboard URL. See
[connecting to Sparkwing Cloud](getting-started.md#sparkwing-cloud) for
the flags, and [hosted controller](#hosted-controller) below for
what the controller process does and how to run one.

## Advanced shapes

The rest of this page is for a team that runs its own bucket, database,
or controller. Each shape is a profile away: pipeline code does not
change.

### Shared object storage

Runners write their run state, cache blobs, and log streams to a
shared object store. The dashboard reads from the same bucket. No
database and no controller: cross-runner coordination runs over the
object store itself, through conditional-write compare-and-swap.

For: a small team that wants cross-runner visibility (laptops, CI,
GitHub Actions) without hosting a database.

Where the bucket enforces write preconditions, a bucket coordinates
across runners with no database -- cache reservation, approvals, and
debug pauses all work. Each is an object-store record mutated under
compare-and-swap (S3
`If-None-Match` / `If-Match`); a contended `.Memoize()` key elects one
leader and the rest coalesce onto its output, the same
exactly-one-runs shape Postgres gives.

Pipeline triggers are the exception, and CAS does not change it. The
object-store backend enqueues a trigger and has no path that claims
one, so `sparkwing.RunAndAwait` refuses with a not-supported error
naming Postgres rather than waiting on a run that nothing starts. Run
pipelines that spawn other pipelines on Postgres or a controller.

S3 is the object store that enforces these preconditions today. The
`gcs` and `azure-blob` state types are recognized in configuration
but not yet implemented. Some S3-compatible gateways accept the
precondition headers and silently ignore them; a runner probes the
endpoint once and, when it finds the guarantee missing, falls back to
last-write-wins -- cache reservation degrades to "every runner
computes and uploads to the same content-addressed key" (safe by
construction), and approvals and debug pauses report not-supported,
so reach for Postgres when you need them.

Tradeoff: coordination over one object is slower at the tail than a
database row lock. A heavily-contended key serializes its acquires
and releases as compare-and-swap retries against a single object; an
uncontended key touches it once. When that tail latency matters,
Postgres row locks are the upgrade.

If a runner's object store is briefly unreachable, run state writes
stage to a local SQLite outbox (`~/.sparkwing/outbox.db`, one per host,
honoring `SPARKWING_HOME`) and replay in order when connectivity
returns, so a transient blip neither fails the run nor loses state. A
blip that outlasts the run does fail it: finishing a run, and closing
the state backend, report an error when the run's terminal state is
still queued locally, and `sparkwing run` prints that error and exits
non-zero, because the bucket is the run's only copy and the outbox
sits on a disk a CI runner discards at job end.
Replay is bounded: the drainer waits an exponentially growing,
fully jittered interval between cycles that made no progress, and after
twelve consecutive failures it declares the replay stalled, logs `s3
state outbox replay gave up`, and drops to one attempt every five
minutes. The stall reaches an operator through the controller's health
route, and the drainer clears it the moment a write lands, so a bucket
that returns hours later still delivers. A bucket that refuses every
write costs a dozen requests and then twelve an hour, rather than one
every five seconds for as long as the host lives.
A write the object-store request budget refuses is never staged: the
outbox absorbs an outage, and a spent budget is a guard against a
runaway loop, not an outage to buffer.
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

#### What each runner needs beyond the profile

The profile carries two strings per surface -- `bucket` and `prefix`.
Everything else is environment, and every runner needs it:

| Variable | Required | What it does |
|---|---|---|
| `AWS_REGION` | Yes | The SDK resolves no endpoint without it. Unset is the default state of a runner that has static credentials and no `~/.aws/config`. |
| `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY`, or any other link in the standard AWS credential chain | Yes | Authenticates every read and write. Profiles hold no credential fields. |
| `SPARKWING_S3_ENDPOINT` | Only for non-AWS S3 | Points the SDK at MinIO, R2, or another S3-compatible endpoint. Setting it also forces path-style addressing, and it applies process-wide: every surface -- state, cache, logs, and the binaries cache -- goes to that endpoint, so one profile cannot mix a MinIO cache with a real-AWS state. |

Secrets are the exception to "no controller": the secrets surface
takes `controller`, `filesystem`, `env`, or `none`, and has no
object-store option. A bucket-only team whose pipelines call `Secret()`
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
but matters most on a shared bucket, where the state backend doesn't
incidentally serialize overlapping invocations the way the local path's
SQLite does.

### Postgres and object storage

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
(use a controller for that).

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

#### Schema versioning

The database records two things: the migrations applied, one
`sparkwing_schema_version` row each, and the features its schema relies
on, one `sparkwing_requirements` row each. A binary opens the database
when it knows every requirement listed, whatever version number the
schema table holds. On startup:

- Database at a *lower* version than the binary: the binary runs the
  missing migrations atomically inside one transaction and stamps the
  requirements those versions declare. Concurrent runners against a
  fresh database coordinate via a Postgres advisory lock; exactly one
  runs the migration.
- Database at the *same* version: nothing to do.
- Database at a *higher* version than the binary, listing only
  requirements the binary knows: the binary opens it read/write,
  migrates nothing, and stamps nothing. Every additive migration lands
  here, so it never freezes a fleet.
- Database listing a requirement the binary does not know: the binary
  refuses to start, naming the requirements and the release that
  introduced them (`sparkwing: this state database uses
  unique-token-prefix, which needs sparkwing >= v0.40.0; you have
  v0.38.2. Run sparkwing update to upgrade.`). A database an older
  binary migrated carries no requirement rows; the first
  requirements-aware binary to open it stamps the ones its applied
  versions declare.

Only a breaking migration couples runner version to schema version, and
`sparkwing_requirements` names exactly which one. Stagger those
upgrades: upgrade every runner *before* you upgrade the database, or run
mixed-version fleets briefly during a rollout. A hosted controller is
the alternative that decouples client and schema versions entirely.

#### Testing against Postgres

The store's own test suite runs against either dialect.
`SPARKWING_TEST_STORE=postgres` points every test that opens through the
store's test helper at Postgres instead of a local SQLite file, and
`SPARKWING_TEST_PG_URL` names the server each test creates a schema of its
own on. With the dialect selected and no URL the suite fails rather than
skipping, so a misconfigured run cannot look like a pass. `sparkwing run
store-postgres` sets both against an embedded Postgres it starts and stops
itself, so proving a store change on this mode's dialect needs neither
Docker nor a database of your own.

#### One-click provisioning

A Terraform module under `install/terraform/mode3-postgres` stands up the
Postgres this mode needs in one `terraform apply`: the database (a single
RDS instance or an Aurora Serverless v2 cluster, picked by one knob), its
security group, and its subnet group across the private subnets you give
it. It writes the connection string to AWS Secrets Manager, so each runner
reads one secret into `SPARKWING_PG_URL` rather than hand-rolling a DSN.
You supply the VPC and private subnets; the module places the database
into networking you already run. Its README covers the variables and how
to point a runner at the result.

### Hosted controller

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

The "owns Postgres" framing above describes the multi-tenant case; the
controller's state backend is pluggable. See
[one of your own machines as the controller](#one-of-your-own-machines-as-the-controller)
for the single-box shape.

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

#### Watching a run live without the logs service

A deployment whose `logs:` surface is an object store has no live read:
an object appears only once a batch is flushed, and the store serves no
tail. The controller closes that gap. A node's runner mirrors its lines
to the controller as it writes them, the controller holds the last
512 KiB per running node in memory, and the dashboard and `sparkwing
runs logs --follow` read that ring while the node runs. The durable
copy still goes to the bucket, and a read after the node finishes comes
from there.

The ring is memory, bounded per node (512 KiB), across all nodes
together (64 MiB), and by ring count (1024 nodes holding one at once).
Past the byte bounds the oldest bytes of the widest ring go first; past
the node bound the ring of the node that wrote least recently is
released. A node's ring is released shortly after the node finishes, or
after ten minutes of silence from a node that never reported finishing.
`sparkwing-controller` takes all four bounds as `--live-log-node-kb`,
`--live-log-total-mb`, `--live-log-max-nodes` and `--live-log-idle`, and
refuses a non-positive value.

A reader is told when it misses bytes: a stream whose buffer dropped
lines before the reader got to them emits one marker line naming the
dropped byte count, and a reader still attached when the buffer is
released gets a marker pointing it at the durable copy. Only a node the
run actually has can write to a ring.

A deployment that runs `sparkwing-logs` keeps its own live stream and
mirrors nothing.

### One of your own machines as the controller

A single-instance controller on one box can back its state with SQLite
(`~/.sparkwing/state.db`) and keep caches and logs on local disk -- the
same storage layout as the local path, but fronted by the HTTP
controller so untrusted clients still never touch the store directly. A
laptop that points its profile at a desktop you own is this shape: one
person's own machines, coordinated without an account. Solo operators
and small teams don't need to stand up Postgres to run it. Reach for
Postgres + object storage when you outgrow a single box -- more than one
controller instance, or state and caches that must survive that box. A
team's fleet coordinates through Sparkwing Cloud, or through a controller
the team runs.

## Forcing local mode for a single run

`sparkwing run <pipeline> --sw-local-only` ignores the shared surfaces in any
resolved profile and pins secrets, state, cache, and logs to the local dotenv,
SQLite, and filesystem layout. Useful for ad-hoc work that shouldn't appear in
the team dashboard, or for reproducing an issue against known-clean local
state.

The flag only affects the one run; subsequent runs without the flag
resolve a profile normally again.

## Selecting a profile

Profile selection is explicit: pass `--profile NAME`, or set
`defaults.profile` in `.sparkwing/sparkwing.yaml` for the project's
default. With neither, no profile is active and the built-in local
defaults (the local path) apply.
There is no environment-based auto-selection -- a CI job picks its
profile by passing `--profile` in the run command (see
[ci-embedded.md](ci-embedded.md)).

## Choosing a shape

A practical decision order:

1. **One person, on the machines they own?** Local, plus `--sw-fleet`
   when a run should spread across them.
2. **A team that wants one dashboard and one run history?**
   [Sparkwing Cloud](#sparkwing-cloud), or a controller your team runs.
3. **Multiple people on S3, fine with bucket-dependent coordination?**
   [Shared object storage](#shared-object-storage) -- a bucket, a
   shared profile, and the per-runner environment that profile does not
   carry: `AWS_REGION`, credentials, and `SPARKWING_S3_ENDPOINT` for a
   non-AWS store. See
   [what each runner needs](#what-each-runner-needs-beyond-the-profile).
4. **Expensive cacheable steps where you want reservation guaranteed
   regardless of bucket CAS support, or low tail latency under heavy
   contention?**
   [Postgres and object storage](#postgres-and-object-storage) --
   add a database on top of the bucket.
5. **Untrusted runners (public CI, customer pipelines) or you don't
   want every runner holding DB credentials, and you want to host the
   controller yourself?**
   [Hosted controller](#hosted-controller).

You can move between shapes by editing a profile (or selecting a
different one with `--profile`); pipeline code doesn't change.
