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

`sparkwing run` executes on this machine and records to the local SQLite
store, logs, cache, and secrets; it takes no `--profile`. A profile in
`~/.config/sparkwing/config.yaml` names a controller (see
[Storage backends](backends.md)).
`sparkwing pipeline trigger <pipeline> --profile NAME` runs a pipeline on
that profile's controller, and read verbs such as
`sparkwing runs list --profile NAME` read its history. Shared run history
lives on a controller: runners never write a shared bucket or database
themselves.

## Local

SQLite under `~/.sparkwing/state.db`, with per-run logs under
`~/.sparkwing/runs/<runID>/`. Zero shared
infrastructure. Every `sparkwing run` records here.

For: a developer working on pipelines on their own laptop.

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
dashboard URL. `sparkwing pipeline trigger <pipeline> --profile NAME` then
runs a pipeline there. See
[connecting to Sparkwing Cloud](getting-started.md#sparkwing-cloud) for
the flags, and [hosted controller](#hosted-controller) below for
what the controller process does and how to run one.

## Advanced shapes

The rest of this page is for a team that runs its own controller. Each
shape is a profile away: pipeline code does not change.

### Hosted controller

A central controller process owns Postgres + object-store credentials
and serves the dashboard. Clients reach it over HTTP and never see the
underlying database: `sparkwing pipeline trigger` submits a run, the
controller dispatches it to its runners, and the dashboard and read verbs
read the results. The controller handles version translation; clients
only need to match the controller's API major version.

For: a team that wants shared run history on infrastructure it runs,
including untrusted CI, public webhooks, or a need to decouple client and
schema versions.

Tradeoff: you have to host the controller. Deploy the complete OSS stack with
the `sparkwing-full` Helm chart. Teams that do not need a shared controller can
keep pipeline execution and its dashboard local instead.

The "owns Postgres" framing above describes the multi-tenant case; the
controller's state backend is pluggable. See
[one of your own machines as the controller](#one-of-your-own-machines-as-the-controller)
for the single-box shape.

```yaml
# ~/.config/sparkwing/config.yaml
profiles:
  prod:
    controller:
      url: https://api.example.dev
      token: swu_xxx
    # state/cache/logs are implied by controller; reads/writes go through it.
```

A profile with a `controller:` block points commands at that controller
over HTTP; the nested `token:` authenticates. `sparkwing pipeline trigger
<pipeline> --profile prod` runs a pipeline there, and `--working-tree` runs
uncommitted changes from an uploaded snapshot. Register or edit
profiles with `sparkwing configure profiles`. See
[Self-hosting](self-hosting.md) for the supported local and controller
deployment paths.

Node logs go through the controller too. A runner uploads each node's log
as gzip segments through URLs the controller issues, into the controller's cache
bucket (`--cache-blob-store`) or, without one, its output directory. The
dashboard, `sparkwing runs logs --profile` and `sparkwing pipeline trigger`
read them back through the controller's log routes. A controller with
neither a cache bucket nor an output directory keeps no node logs. See
[observability.md](observability.md#node-logs-kept-as-segments).

#### Watching a run live

A node's segments land every 10 seconds or 1 MiB, so the controller
closes the gap while someone watches. The dashboard's log stream and
`sparkwing runs logs --follow` hold a 30-second watch on the node; while
it is held, the node's runner pushes its lines to the controller, which
holds the last 512 KiB per node in memory, and the reader follows that
ring after the segments uploaded so far. An unwatched node sends nothing
live. A read after the node finishes comes from the segments.

The ring is memory, bounded per node (512 KiB), across all nodes
together (64 MiB), and by ring count (1024 nodes holding one at once).
Past the byte bounds the oldest bytes of the widest ring go first; past
the node bound the ring of the node that wrote least recently is
released. A node's ring is released shortly after the node finishes, or
after ten minutes of silence from a node that never reported finishing.
`sparkwing-controller` takes each bound as `--live-log-node-kb`,
`--live-log-total-mb`, `--live-log-max-nodes` and `--live-log-idle`, and
refuses a non-positive value.

A reader is told when it misses bytes: a stream of the ring alone
(`GET /api/v1/runs/{id}/nodes/{nodeID}/logs/stream`) whose buffer dropped
lines before the reader got to them emits one marker line naming the
dropped byte count, and a reader still attached when the buffer is
released gets a marker pointing it at the durable copy. The merged read
(`GET /api/v1/runs/{id}/logs/{node}/stream`) instead waits for the segment
that covers dropped bytes. Only a node the run actually has can write to a ring.

#### Schema versioning

A state database, SQLite or Postgres (see
[PostgreSQL state](self-hosting.md#postgresql-state)), records its schema
version in `sparkwing_schema_version`. Until paid launch there is one
baseline schema, version 1, and no upgrade path. On startup:

- Empty database: the binary creates the baseline in one transaction.
  Concurrent controller instances against a fresh Postgres database
  coordinate via an advisory lock; exactly one creates it.
- Database at the binary's version: nothing to do.
- Database at any other version, including every database a release
  before the baseline wrote: the binary refuses to start with
  `sparkwing: this store predates the baseline ...; delete <path> (or drop
  the database) and start again`. Delete the SQLite file or drop the
  Postgres database, then start the binary again.

Every binary that opens one database must be built at the same schema
version. Clients that reach the database through a controller never meet
the schema.

#### Testing against Postgres

The store's own test suite runs against either dialect.
`SPARKWING_TEST_STORE=postgres` points every test that opens through the
store's test helper at Postgres instead of a local SQLite file, and
`SPARKWING_TEST_PG_URL` names the server each test creates a schema of its
own on. With the dialect selected and no URL the suite fails rather than
skipping, so a misconfigured run cannot look like a pass. `sparkwing run
store-postgres` sets both against an embedded Postgres it starts and stops
itself, so proving a store change on the controller's Postgres dialect
needs neither Docker nor a database of your own.

#### One-click provisioning

A Terraform module under `install/terraform/mode3-postgres` stands up the
Postgres a self-hosted controller uses in one `terraform apply`: the
database (a single RDS instance or an Aurora Serverless v2 cluster, picked
by one knob), its security group, and its subnet group across the private
subnets you give it. It writes the connection string to AWS Secrets
Manager for the controller's `pg-url` credential. You supply the VPC and
private subnets; the module places the database into networking you
already run. Its README covers the variables and how to point a
controller at the result.

### One of your own machines as the controller

A single-instance controller on one box can back its state with SQLite
(`~/.sparkwing/state.db`) and keep caches and logs on local disk -- the
same storage layout as the local path, but fronted by the HTTP
controller so untrusted clients still never touch the store directly. A
laptop that triggers pipelines on a desktop you own is this shape: one
person's own machines, coordinated without an account. Solo operators
and small teams don't need to stand up Postgres to run it. Back the
controller with Postgres and object storage when you outgrow a single box
-- more than one controller instance, or state and caches that must survive that box. A
team's fleet coordinates through Sparkwing Cloud, or through a controller
the team runs.

## Forcing local mode for a single run

Every `sparkwing run` records to this machine. `sparkwing run <pipeline>
--sw-local-only` also keeps its node processes off a shared cache named by
`SPARKWING_CACHE_URL`, pinning secrets, state, cache, and logs to the local
secret store, SQLite, and filesystem layout. Git hooks run their pipelines
this way.

## Selecting a profile

Profile selection is explicit: pass `--profile NAME` on the command that
should reach the profile. Neither the environment nor a project-wide
default selects one. `sparkwing run` takes no `--profile`; it refuses one
with a message naming `sparkwing pipeline trigger <pipeline> --profile
NAME`, the command that runs a pipeline on a controller. A pipeline whose
`.sparkwing/sparkwing.yaml` entry names `profile:` is pinned to that
controller profile; see
[pinning a pipeline to a profile](backends.md#pinning-a-pipeline-to-a-profile).
A CI job names its profile on its own command line (see
[ci-embedded.md](ci-embedded.md)).

## Choosing a shape

A practical decision order:

1. **One person on their own machine?** Local.
2. **A team that wants one dashboard and one run history?**
   [Sparkwing Cloud](#sparkwing-cloud), and `sparkwing pipeline trigger`
   from laptops and CI.
3. **The same on infrastructure you run, with untrusted runners
   (public CI, customer pipelines) kept away from the database?**
   [Hosted controller](#hosted-controller), or
   [one of your own machines](#one-of-your-own-machines-as-the-controller)
   for a single box.

You can move between shapes by editing a profile; pipeline code doesn't
change.
