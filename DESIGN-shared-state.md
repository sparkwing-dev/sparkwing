# Shared state, caches, and dashboard across many runners

## Goal

Let N independent runners -- laptops, k8s workers, GitHub Actions
jobs -- share orchestrator state, job caches, binary caches, and
logs, with a hosted dashboard that reflects everyone's activity in
one place. Four deployment shapes are all valid and share the
same codebase:

1. **local-only** -- SQLite + on-disk caches + on-disk logs. Zero shared
   infra. Used when the resolved profile declares no shared surfaces or
   the `--sw-local-only` flag is set.
2. **S3-only shared** -- runners write their own run state, caches,
   and logs to a shared object store. No database, no controller.
   The dashboard reads from the same bucket. Lowest-friction
   self-hosted setup; works for 10 laptops wanting cross-team
   visibility, GitHub Actions runners sharing a cache bucket, etc.
   Cross-runner coordination -- cache reservation, triggers,
   approvals, debug pauses -- runs over object-store conditional-write
   CAS where the endpoint enforces write preconditions (see "Cache
   reservation across runners"). Where it does not, cache reservation
   degrades to last-write-wins (you keep cross-runner cache *reuse via
   content-addressed keys*), while triggers, approvals, and debug
   pauses are not-supported and need Mode 3 or Mode 4.
3. **direct-DB** -- runners write straight to a shared Postgres for
   state and a shared object store for caches/logs. Adds proper
   cross-runner cache reservation (no thundering herd) and the
   full live state surface for the dashboard. The upgrade path
   from S3-only when reservation matters but you still don't want
   to host a controller.
4. **hosted controller** -- runners (including laptops) talk to a
   hosted controller over HTTP. Controller owns Postgres + object
   store credentials + serves the dashboard. The model the cluster
   worker already runs in; we're just exposing it to laptops too.

The unifying decomposition: the orchestrator already runs against
abstract `StateBackend` + `LogBackend` + `ConcurrencyBackend`
interfaces (`internal/orchestrator/backends.go`). Mode selection
is just *which implementations* fill those slots.

## How the pieces fit

The architecture the four modes share.

### Orchestrator abstraction

`internal/orchestrator/backends.go` defines:

- `StateBackend` -- the run/node/step/event/dispatch/debug-pause/
  approval/trigger/metrics surface. It embeds `storage.StateStore`
  and adds the wrapper-shaped methods (output extraction, trigger
  cycle detection, simplified-error `AppendEvent`).
- `LogBackend` -- opens per-node log sinks.
- `ConcurrencyBackend` -- atomic acquire / heartbeat / release /
  waiter resolution for the `.Cache()` DSL.

Three constructors fill those slots, one per shared-infra shape:
`LocalBackends` (SQLite store + filesystem logs), `S3Backends`
(NDJSON-over-object-store state + CAS concurrency), and
`RemoteBackends` (a `*controller/client.Client` for state, HTTP
concurrency, HTTP logs). The laptop path, the cluster worker, and the
single-node runner all assemble Mode 4 through `RemoteBackends`.

### Storage abstraction

`pkg/storage` defines `ArtifactStore`, `LogStore`, and `StateStore`.
`storage.StateStore` (`pkg/storage/state.go`) is the interface every
state backend implements; `storeurl.OpenStateStoreFromSpec` dispatches
`sqlite`, `s3`, `postgres`, and `controller` specs through it,
returning `*store.Store`, `*s3state.Backend`, or `*client.Client` as
the spec demands. `gcs`, `azure-blob`, and `mysql` are recognized and
report unimplemented at run start rather than falling back silently.

### Cache reservation primitive

`pkg/store/concurrency.go` implements the cross-laptop cache
reservation behavior:

- `AcquireConcurrencySlot` atomically combines cache-lookup +
  capacity check + holder-count + policy branch.
- Returns `AcquireCached` with an `OutputRef` when a prior holder
  published a result for the same `CacheKeyHash`.
- `OnLimit:coalesce` lets followers wait on a leader and inherit
  its result.
- Holder leases with heartbeats; reaper sweeps expired holders.
- 35d TTL on the `concurrency_cache` rows.

The `.Cache()` DSL routes through this. When everyone shares one
Postgres, cross-laptop reservation and result-borrowing fall out
for free -- no extra code in this layer.

### Dashboard data abstraction

`internal/backend/` defines a `Backend` interface with three impls:
`StoreBackend` (direct `*store.Store`, SQLite or Postgres),
`ClientBackend` (HTTP-to-controller), and `S3Backend` (reads
`state.ndjson`). `cmd/sparkwing-web` takes `--state-spec`,
`--logs-spec`, and `--artifacts-spec` and resolves them through the
same `storeurl` factories the orchestrator uses, so the dashboard
serves any of the four modes without extra glue.

### S3 state dump

`orchestrator.DumpRunState` writes `runs/<id>/state.ndjson` to the
artifact store at run completion, and `S3Backend` reads it -- a
completely infrastructure-less dashboard mode for finished runs. Mode 2
uses the same file format, written incrementally as the run progresses
instead of only at the end.

The operator-facing view of all of this is
[docs/deployment-modes.md](docs/deployment-modes.md) and
[docs/backends.md](docs/backends.md).

## Architecture by mode

### Mode 1: local-only

```
sparkwing run (laptop)
   └─ orchestrator
       └─ Backends{
            State:       client.Client -> unix api.sock -> wingd's *store.Store(sqlite)
            Logs:        localLogs{paths}
            Concurrency: HTTPConcurrency -> the same socket
          }
       └─ ArtifactStore: fs:///~/.cache/sparkwing
       └─ LogStore:      per-run files under ~/.sparkwing/runs/<runID>/
```

Selected when: the resolved profile's `state:` surface is `sqlite` (or
the profile declares no surfaces), or `--sw-local-only` is set.

The admission daemon owns `~/.sparkwing/state.db`. A run that reaches it
opens no store: its state and concurrency calls travel over the daemon's
`api.sock` with no bearer token, and each `run-node` subprocess is handed
that socket instead of a loopback controller's URL and token. Logs and
artifacts stay on this machine's own files either way.

A run that cannot reach a daemon serving that socket keeps the direct path
instead -- `localState` and `localConcurrency` over its own
`*store.Store`, plus an in-process loopback controller for its
subprocesses -- so a machine with no daemon still runs pipelines.

### Mode 2: S3-only shared

```
sparkwing run (laptop or CI runner)
   └─ orchestrator
       └─ Backends{
            State:       s3State{bucket}         // live NDJSON + CAS records
            Logs:        s3LogBackend{bucket}
            Concurrency: s3Concurrency{bucket}   // If-Match CAS semaphore
          }
       └─ ArtifactStore: s3://shared-bucket/cache
       └─ LogStore:      s3://shared-bucket/logs

sparkwing-web (anywhere)
   └─ backend.S3Backend{S3 bucket}
```

Notes:

- **State writes** are incremental updates to
  `runs/<runID>/state.ndjson` -- same format `DumpRunState` writes
  today, but appended throughout the run rather than dumped at
  the end. Each runner only writes its own run paths; no
  cross-runner contention on a single key.
- **Offline buffering** is supported. When the object store is
  unreachable, state writes, cache PUTs, and log appends stage to
  a local SQLite buffer (`~/.sparkwing/outbox.db`) and replay
  when connectivity returns. Safe because all keys are
  per-runner (`runs/<runID>/...`) or content-addressed; no
  conflicts on replay. No schema or API negotiation needed.
- **`s3Concurrency`** coordinates cross-runner reservation over the
  object store's conditional-write CAS, no database. It holds each
  concurrency key's full state -- holders, waiters, memoized output --
  in one versioned slot object and mutates it under a
  read-modify-`PutIfMatch` retry loop, so N runners on one key elect
  a leader and the rest coalesce onto its output. The deliberate
  tradeoff is tail latency, not correctness: a heavily-contended key
  serializes its acquires and releases as retries against a single
  object, slower at the tail than a Postgres row lock; an uncontended
  key touches the object once. When the endpoint does not enforce
  preconditions the backend degrades to granting every slot, so two
  runners may both compute and both upload to the same
  content-addressed key -- safe because the bytes are identical by
  construction.
- **Cache reuse still works** unconditionally: a runner checks
  `art.Has(ctx, cacheKey)` before computing and skips the work if the
  blob is already in the store. On top of that, coordinated
  *reservation* works wherever the endpoint enforces preconditions --
  the `.Cache()` slot acquire coalesces followers onto one leader
  instead of every runner computing. Where it cannot, you keep bare
  content-addressed reuse and lose only the reservation; Mode 3 is the
  upgrade when you need it guaranteed.
- **Triggers, debug pauses, and approvals** work in this mode where
  the endpoint enforces write preconditions. Each is a discrete
  object-store record mutated under CAS -- `PutIfAbsent` for
  create-once, a `GetWithETag`/`PutIfMatch` loop for resolve -- and
  pipeline-trigger enqueue carries child-trigger idempotency so a
  retried parent does not double-spawn. Where the endpoint ignores
  preconditions the driving `StateBackend` methods return
  `ErrNotSupported` and the caller falls back to Mode 3 or Mode 4
  rather than coordinate unsafely.
- **Provider support and capability detection**: S3 is the object
  store that enforces these preconditions today (`If-None-Match: *`
  for create-once, `If-Match: <etag>` for compare-and-swap); the
  filesystem backend enforces them locally for single-host runs. The
  `ConditionalWriter` contract is provider-agnostic and names the GCS
  generation-match and Azure ETag equivalents, but those backends are
  not yet implemented -- declaring `gcs` or `azure-blob` surfaces an
  unimplemented error at run start. Two checks gate every coordinated
  operation: a static type check that the backend exposes
  `ConditionalWriter`, and a one-time live probe
  (`ConditionalWritesSupported`) that catches S3-compatible gateways
  which accept precondition headers and silently ignore them. Either
  check failing routes the operation to the last-write-wins fallback.
- **Dashboard live updates**: `S3Backend` polls
  `runs/<id>/state.ndjson` for changes. Refresh latency = poll
  interval (default 2-5s), with cache invalidation on a per-run
  mtime check.

Selected when: the resolved profile's `state:` surface is `s3` (`gcs`
and `azure-blob` are recognized and report unimplemented).

### Mode 3: direct-DB

```
sparkwing run (laptop or CI runner)
   └─ orchestrator
       └─ Backends{
            State:       localState{*store.Store(postgres)}
            Logs:        s3LogBackend{S3 bucket}
            Concurrency: localConcurrency{*store.Store(postgres)}
          }
       └─ ArtifactStore: s3://shared-bucket/cache
       └─ LogStore:      s3://shared-bucket/logs

sparkwing-web (anywhere)
   └─ backend.StoreBackend{*store.Store(postgres), s3LogStore}
```

Notes:

- `localState` and `localConcurrency` wrap a `*store.Store`. The
  type is the same whether the underlying driver is SQLite or
  Postgres -- this is the central reason a `Store` interface
  refactor is *not* required. The Store stays a concrete type with
  a dialect-aware backing `*sql.DB`.
- The same orchestrator code runs in CI runners; only the selected
  profile differs.
- All runners need Postgres + S3 creds. Trust model: "anyone with
  DB creds is trusted." Fine for owned infra.

Selected when: the resolved profile's `state:` surface is `postgres`.

### Mode 4: hosted controller

```
sparkwing run (laptop)
   └─ orchestrator
       └─ Backends{
            State:       *client.Client(controller URL, token)
            Logs:        HTTPLogs{logs URL, token}
            Concurrency: HTTPConcurrency{controller URL, token}
          }
       └─ ArtifactStore: sparkwingcache HTTP backend → controller
       └─ LogStore:      sparkwinglogs HTTP backend → controller

controller (k8s)
   └─ controller.Server{*store.Store(postgres)}
   └─ ArtifactStore: s3://shared-bucket/cache
   └─ LogStore:      s3://shared-bucket/logs
   └─ serves the dashboard
```

Selected when: the resolved profile's `state:` surface is `controller`
(or the profile carries only a `controller:` block, which routes every
surface through it).

Notes:

- The laptop in this mode is self-orchestrating, not a runner pod
  waiting on dispatches. It calls `client.CreateRun`,
  `client.CreateNode`, `client.StartNode`, etc. directly -- same
  HTTP surface the cluster worker uses, but driven by the
  laptop's own dispatch loop. No `dispatch` round trips.
- Auth uses existing tokens. Each laptop gets a runner-scoped
  token from the controller.

## Postgres schema

The SQLite schema (the `schemaSQLite` constant in
`pkg/store/store.go`) ports cleanly with these substitutions, applied
at migration time:

| SQLite | Postgres |
|---|---|
| `INTEGER` (for IDs, counters) | `BIGINT` |
| `INTEGER` (for unix-time) | `BIGINT` |
| `BLOB` | `BYTEA` |
| `TEXT` | `TEXT` |
| `INSERT OR REPLACE INTO` | `INSERT ... ON CONFLICT ... DO UPDATE` |
| `INSERT OR IGNORE INTO` | `INSERT ... ON CONFLICT DO NOTHING` |
| Partial indexes (`WHERE`) | identical syntax -- supported |
| `RETURNING` | identical -- supported |
| `strftime`, `unixepoch` | none used; all times stored as `BIGINT` |
| `PRAGMA journal_mode(WAL)` | drop |
| `PRAGMA foreign_keys(on)` | drop (always on in pg) |
| `PRAGMA busy_timeout(5000)` | drop (no equivalent needed) |

Tables: `runs`, `nodes`, `events`, `triggers`,
`concurrency_entries`, `concurrency_holders`,
`concurrency_waiters`, `concurrency_cache`, `node_steps`,
`node_metrics`, `tokens`, `sessions`, `users`, `secrets`,
`debug_pauses`, `approvals`, `node_dispatches`, plus
`sparkwing_schema_version` and `sparkwing_requirements` (both created
separately on every Open):

```sql
CREATE TABLE sparkwing_schema_version (
    version    INTEGER NOT NULL,
    applied_at BIGINT NOT NULL,
    PRIMARY KEY (version)
);

CREATE TABLE sparkwing_requirements (
    name             TEXT NOT NULL,
    added_at         BIGINT NOT NULL,
    added_by_version TEXT NOT NULL,
    PRIMARY KEY (name)
);
```

Neither table is a numbered migration, so neither leaves a row in
`sparkwing_schema_version`. That is deliberate for the requirements
table: a schema bump to introduce it would have stranded every older
binary in order to ship the mechanism whose purpose is to stop
stranding them.

`sparkwing_schema_version` orders migrations. `sparkwing_requirements`
decides who may open the database: each migration declares whether it is
additive or adds a named requirement, and `store.Open` admits a runner
that knows every requirement listed, whatever version number the schema
table holds. Skew handling:

- runner version > DB version: runner runs the missing migrations
  inside a single transaction, stamping the requirements each applied
  version declares. A database migrated before requirements shipped is
  backfilled with the requirements of its applied versions on first
  open.
- runner version < DB version, every listed requirement known: open
  read/write, migrate nothing, stamp nothing. Every additive migration
  lands here.
- any listed requirement unknown: refuse to start, naming the
  requirements and the release that introduced them ("this state
  database uses unique-token-prefix, which needs sparkwing >= v0.40.0").
  Fail loud; don't operate on a schema whose shape this binary cannot
  model.

Only a breaking migration couples runner version to schema version, and
the requirements table names exactly which one. Hosted-controller still
decouples the two entirely: the controller can run any version against
any client version of the same major.

### Locking semantics worth re-verifying

The SQLite implementation uses transaction-wrapped reads-then-writes
that are serialized by SQLite's writer-locks-database model. The
Postgres translations need explicit locks:

- `Store.ClaimNextReadyNode` -- SQLite version does a
  `SELECT ... LIMIT 1` then `UPDATE ... WHERE claimed_by IS NULL`.
  In Postgres, use `SELECT ... FOR UPDATE SKIP LOCKED` to avoid
  thundering-herd waiters and let multiple claimants make
  progress in parallel.
- `Store.AcquireConcurrencySlot` (`pkg/store/concurrency.go`) -- same pattern;
  the inner transaction reads holders/waiters/cache, then writes.
  Use `SELECT FOR UPDATE` on the `concurrency_entries` row keyed
  by the slot key; pg row-level lock is the natural serialization
  point.
- `ClaimNextTrigger`, `ClaimSpecificTrigger`, `ReapExpiredNodeClaims`
  -- all variants of the same pattern; `FOR UPDATE SKIP LOCKED`.

These changes don't affect the SQLite path. The two dialects can
share most query strings and branch only on the locking clauses.

## Cache reservation across runners

**In Modes 3 and 4 (Postgres / hosted controller):** no new
primitive. The flow when N runners independently want to run a
cached step:

1. Laptops 1..N all hit `acquireConcurrencySlot(key, holder_id_n)`.
2. Postgres serializes on `concurrency_entries.key`.
3. Laptop 1 wins, gets `Granted` + a holder row + a lease.
4. Laptops 2..N either:
   - **Coalesce** (default for `.Cache()`): wait on leader, get
     `Coalesced` + a waiter row. When laptop 1 calls
     `ReleaseSlot(outcome="success", outputRef=s3://...)`, the
     waiters resolve to `Cached` with the same `outputRef`. All
     N laptops fetch from S3 -- only laptop 1 computed.
   - **Skip**: get `Skipped` immediately and proceed past.
   - **Queue**: get `Queued`, poll until promoted.
5. The `concurrency_cache` row laptop 1 wrote on release serves
   future cache lookups for 35d.

Heartbeat-based reaping (`reapStaleConcurrencyHolders`) handles
the "laptop 1 crashed mid-job" case: lease expires, another
laptop's next acquire attempt picks up the lock.

The cache *blob* lives in S3 at a content-addressed path. Even
without the Postgres row, a runner can `HEAD` the S3 key directly
and short-circuit. The Postgres row buys atomicity (no thundering
herd) and dashboard visibility (provenance: "this run reused
output from run X on date Y").

**In Mode 2 (S3-only):** reservation runs over the object store's
conditional-write CAS instead of a Postgres row, wherever the
endpoint enforces preconditions:

1. Runners 1..N each `HEAD` the cache key. If present, fetch and
   skip computation. **This is the common case** and is unchanged
   from Modes 3/4.
2. If absent, the runners contend on one versioned slot object via
   `PutIfMatch`. One wins the leader role and computes; the rest
   record waiter entries in the same object and coalesce, inheriting
   the leader's output when it releases -- the same exactly-one-runs
   shape as Modes 3/4, serialized through one object instead of a
   row lock.
3. The leader's terminal outcome lingers in the slot for a short
   retention window so a slow follower can still inherit it.

The tradeoff is tail latency, not correctness: a heavily-contended
key serializes its acquires and releases as `PutIfMatch` retries
against a single object, slower at the tail than a Postgres row lock.
Where the endpoint does not enforce preconditions the backend
degrades to the original "every runner computes" behavior (safe by
content-addressing); Mode 3 is the upgrade when you need guaranteed
reservation under heavy contention.

## --sw-local-only flag

Behavior on `sparkwing run`:

- When set: ignore the resolved profile's surfaces and pin
  `Spec{Type: sqlite, Path: opts.DefaultStateDB}` for state, with no
  shared cache or log store. The orchestrator runs as if no shared
  infrastructure were configured.
- When unset: the resolved profile's `state:` / `cache:` / `logs:`
  surfaces apply, falling back to the built-in local defaults.

`--sw-local-only` matches the `--sw-` namespace convention for
`sparkwing run` control flags. It is one field on
`orchestrator.Options` (`LocalOnly`); `ApplyProfileBackends`
short-circuits on it before any profile surface is opened, so an
operator facing a stale or unreachable shared store can bypass the
resolver entirely. `SPARKWING_LOCAL_ONLY=1` sets the same field.

## Trust and auth

| Mode | Who can write | Auth surface |
|---|---|---|
| local-only | one user, one machine | filesystem perms |
| S3-only shared | anyone with object-store creds | IAM / bucket policies |
| direct-DB | anyone with DB + object-store creds | pg roles + IAM |
| hosted controller | anyone with a controller token | existing token/session system |

Direct-DB and S3-only modes do **not** add row-level security.
Runners are fully trusted; both modes are intended for owned
infrastructure. If the threat model includes "untrusted CI runs
against shared infra," use hosted-controller mode instead.

## Configuration surface

Each mode is one profile. Laptop profiles live in
`~/.config/sparkwing/profiles.yaml`; project profiles in the
`profiles:` map of `.sparkwing/sparkwing.yaml`, where all four
surfaces are required. Selecting a profile selects a mode -- see
[docs/backends.md](docs/backends.md).

```yaml
# ~/.config/sparkwing/profiles.yaml
profiles:
  # Mode 1: local-only. Also the built-in default with no profile at all.
  laptop:
    secrets: { type: env }
    state:   { type: sqlite }
    cache:   { type: filesystem, path: ~/.cache/sparkwing }
    logs:    { type: filesystem, path: ~/.cache/sparkwing/logs }

  # Mode 2: S3-only shared.
  shared:
    secrets: { type: env }
    state:   { type: s3, bucket: my-org-sparkwing, prefix: state }
    cache:   { type: s3, bucket: my-org-sparkwing, prefix: cache }
    logs:    { type: s3, bucket: my-org-sparkwing, prefix: logs }

  # Mode 3: direct-DB.
  shared-db:
    secrets: { type: env }
    state:   { type: postgres, url_source: env:SPARKWING_PG_URL }
    cache:   { type: s3, bucket: my-org-sparkwing, prefix: cache }
    logs:    { type: s3, bucket: my-org-sparkwing, prefix: logs }

  # Mode 4: hosted controller.
  prod:
    controller: { url: https://prod.example.com, token: swu_xxx }
    secrets: { type: controller, url: https://prod.example.com }
    state:   { type: controller }
    cache:   { type: controller }
    logs:    { type: controller }
```

The profile applies wholesale: `--profile <name>`, else the pipeline's
own `profile:`, else the project's `defaults.profile`, else the
built-in local defaults. Surfaces are not layered in one at a time,
and there is no environment auto-detection -- CI selects its mode by
naming a profile like any other caller.

## Operational notes

- **Postgres DSN style.** `state:` takes `url:` directly or
  `url_source: env:VAR`, matching the cache and logs surfaces. The
  driver is `pgx/v5`, so the store keeps the `database/sql` interface.
- **Migration coordination.** Under direct-DB, N runners may open the
  store concurrently against a fresh database. The migrate path runs
  inside a transaction guarded by
  `pg_advisory_xact_lock(hashtext('sparkwing_migrate'))`, so exactly
  one runner applies the schema and the rest wait. The SQLite path
  takes no such lock.
- **Token tables in direct-DB mode.** With no controller there is no
  consumer for `tokens` / `sessions` / `users`. They stay in the
  schema (they are tiny) and sit empty.
- **S3 NDJSON write coalescing.** A short window means more PUTs and a
  fresher dashboard; a long one means fewer PUTs and staler reads.
  `s3state.DefaultFlushInterval` is 500ms and
  `s3state.DefaultBufferThreshold` is 16KiB; both are `Option`s on the
  backend.
- **Plan-level concurrency in Mode 2.** `.Cache().Namespace()` at plan
  scope relies on a slot key shared across runs of the same pipeline.
  `s3Concurrency` holds that slot in one versioned object, so
  concurrent runs serialize on it wherever the endpoint enforces write
  preconditions. Where it cannot, the backend degrades to granting
  every slot and a namespace used as a hard gate loses its guarantee.
