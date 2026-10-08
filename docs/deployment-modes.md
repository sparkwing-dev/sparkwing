# Deployment modes

Two paths carry a reader: **Local** and **Sparkwing Cloud**.
[Getting started](getting-started.md) walks both. This page is the
storage-and-coordination taxonomy underneath them, for a team that hosts
its own controller.

| Shape | Infrastructure | Shared dashboard | Coordinated cache | Triggers / approvals / debug pauses | Auth surface |
| --- | --- | --- | --- | --- | --- |
| Local | none | -- | -- | -- | filesystem |
| Sparkwing Cloud | none (hosted) | yes | yes | yes | tokens / sessions |
| Self-hosted controller | controller + DB + object store | yes | yes | yes | tokens / sessions |

`sparkwing run` executes on this machine against its local SQLite store,
logs, cache, and secrets. A profile in `~/.config/sparkwing/config.yaml`
names a controller or a `state` / `cache` / `logs` triple (see
[Storage backends](backends.md)). `sparkwing pipeline trigger --profile
NAME <pipeline>` runs a pipeline on that profile's controller, and read
verbs such as `sparkwing runs list --profile NAME` and `sparkwing serve
start --profile NAME --read-only` read the profile's backends.

## Local

SQLite under `~/.sparkwing/state.db`, with per-run logs under
`~/.sparkwing/runs/<runID>/`. Zero shared
infrastructure. Every `sparkwing run` records here.

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
dashboard URL. `sparkwing pipeline trigger --profile NAME <pipeline>` then
runs a pipeline there. See
[connecting to Sparkwing Cloud](getting-started.md#sparkwing-cloud) for
the flags, and [hosted controller](#hosted-controller) below for
what the controller process does and how to run one.

## Advanced shapes

The rest of this page is for a team that runs its own controller. A
controller is a profile away: pipeline code does not change.

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
--profile prod <pipeline>` runs a pipeline there, and `--working-tree` runs
uncommitted changes from an uploaded snapshot. Register or edit
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
controller-dispatched runner with no `logs:` surface of its own posts
there. The local
dashboard, which mounts the controller and the logs service on one mux,
announces its own URL. Without an announcement a dispatched run refuses to start,
`sparkwing cluster worker` refuses to start, and `sparkwing pipeline
trigger` and `sparkwing runs logs --grep` refuse to read logs, each with an error
naming the controller and the missing logs service.

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
`sparkwing-controller` takes each bound as `--live-log-node-kb`,
`--live-log-total-mb`, `--live-log-max-nodes` and `--live-log-idle`, and
refuses a non-positive value.

A reader is told when it misses bytes: a stream whose buffer dropped
lines before the reader got to them emits one marker line naming the
dropped byte count, and a reader still attached when the buffer is
released gets a marker pointing it at the durable copy. Only a node the
run actually has can write to a ring.

A deployment that runs `sparkwing-logs` keeps its own live stream and
mirrors nothing.

#### Schema versioning

A controller whose state is Postgres (see
[PostgreSQL state](self-hosting.md#postgresql-state)) records two things
in that database: the migrations applied, one
`sparkwing_schema_version` row each, and the features its schema relies
on, one `sparkwing_requirements` row each. A binary opens the database
when it knows every requirement listed, whatever version number the
schema table holds. On startup:

- Database at a *lower* version than the binary: the binary runs the
  missing migrations atomically inside one transaction and stamps the
  requirements those versions declare. Concurrent controller instances
  against a fresh database coordinate via a Postgres advisory lock;
  exactly one runs the migration.
- Database at the *same* version: nothing to do.
- Database at a *higher* version than the binary, listing only
  requirements the binary knows: the binary opens it read/write,
  migrates nothing, and stamps nothing. Every additive migration lands
  here, so it never freezes a rollout.
- Database listing a requirement the binary does not know: the binary
  refuses to start, naming the requirements and the release that
  introduced them (`sparkwing: this state database uses
  unique-token-prefix, which needs sparkwing >= v0.40.0; you have
  v0.38.2. Run sparkwing update to upgrade.`). A database an older
  binary migrated carries no requirement rows; the first
  requirements-aware binary to open it stamps the ones its applied
  versions declare.

Only a breaking migration couples a binary's version to the schema
version, and `sparkwing_requirements` names exactly which one. Stagger
those upgrades: upgrade every controller instance *before* you upgrade
the database, or run mixed versions briefly during a rollout. Clients
that reach the database through a controller never meet the schema.

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
Manager, and the controller reads that DSN as `SPARKWING_PG_URL`. You
supply the VPC and private subnets; the module places the database into
networking you already run. Its README covers the variables and how to
point the controller at the result.

### One of your own machines as the controller

A single-instance controller on one box can back its state with SQLite
(`~/.sparkwing/state.db`) and keep caches and logs on local disk -- the
same storage layout as the local path, but fronted by the HTTP
controller so untrusted clients still never touch the store directly. A
laptop that triggers pipelines on a desktop you own is this shape: one
person's own machines, coordinated without an account. Solo operators
and small teams don't need to stand up Postgres to run it. Reach for
Postgres + object storage when you outgrow a single box -- more than one
controller instance, or state and caches that must survive that box. A
team's fleet coordinates through Sparkwing Cloud, or through a controller
the team runs.

## Selecting a profile

Profile selection is explicit: pass `--profile NAME` on the command that
should reach the profile. Neither the environment nor a project-wide
default selects one. `sparkwing run` takes no `--profile` and refuses one,
naming `sparkwing pipeline trigger --profile NAME <pipeline>`, the command
that runs a pipeline on a controller. A pipeline whose
`.sparkwing/sparkwing.yaml` entry names `profile:` is pinned to that
controller profile; see
[pinning a pipeline to a profile](backends.md#pinning-a-pipeline-to-a-profile).

A profile can name an S3 bucket or a Postgres database as its `state` or
`logs` surface directly. Read verbs read those backends, so run history
kept there stays reachable through `sparkwing runs list --profile NAME`
and `sparkwing serve start --profile NAME --read-only`; no client command
writes runs to them.

## Choosing a shape

A practical decision order:

1. **One person, on the machines they own?** Local, plus `--sw-fleet`
   when a run should spread across them.
2. **A team that wants one dashboard and one run history?**
   [Sparkwing Cloud](#sparkwing-cloud), and `sparkwing pipeline trigger`
   from laptops and CI.
3. **The same on infrastructure you run, with untrusted runners
   (public CI, customer pipelines) kept away from the database?**
   [Hosted controller](#hosted-controller), or
   [one of your own machines](#one-of-your-own-machines-as-the-controller)
   for a single box.

The command picks the shape: `sparkwing run` executes locally, and
`sparkwing pipeline trigger --profile NAME` executes on a controller.
Pipeline code doesn't change.
