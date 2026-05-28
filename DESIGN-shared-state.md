# Shared state, caches, and dashboard across many runners

## Goal

Let N independent runners -- laptops, k8s workers, GitHub Actions
jobs -- share orchestrator state, job caches, binary caches, and
logs, with a hosted dashboard that reflects everyone's activity in
one place. Four deployment shapes are all valid and share the
same codebase:

1. **local-only** -- today's behavior. SQLite + on-disk caches + on-disk
   logs. Zero shared infra. Used when `backends.yaml` is absent or
   the `--sw-local-only` flag is set.
2. **S3-only shared** -- runners write their own run state, caches,
   and logs to a shared object store (S3 / GCS / Azure Blob). No
   database, no controller. The dashboard reads from the same
   bucket. Lowest-friction self-hosted setup; works for 10 laptops
   wanting cross-team visibility, GitHub Actions runners sharing a
   cache bucket, etc. Cross-runner cache *reservation* is skipped
   in this mode (see "Cache reservation across runners"); you keep
   cross-runner cache *reuse via content-addressed S3 keys*.
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

## What's already in place

Before listing work, here is the existing architecture this design
plugs into. Most of the shape is already correct.

### Orchestrator abstraction (done)

`internal/orchestrator/backends.go` defines:

- `StateBackend` -- ~40 methods covering runs, nodes, steps,
  events, dispatches, debug pauses, approvals, triggers, metrics.
- `LogBackend` -- opens per-node log sinks.
- `ConcurrencyBackend` -- atomic acquire / heartbeat / release /
  waiter resolution for the `.Cache()` DSL.
- `LocalBackends(paths, *store.Store)` constructs the bundle from a
  SQLite store + filesystem logs.

`internal/cluster/worker.go` already builds a *remote* bundle by
passing `*pkg/controller/client.Client` as the `StateBackend`,
`HTTPConcurrency` as the `ConcurrencyBackend`, and either a
`LogStore`-backed or HTTP-backed log backend. **The
hosted-controller path for the orchestrator is already wired.**
It just isn't exposed to `sparkwing run` on a laptop yet.

### Storage abstraction (done)

`pkg/storage` defines `ArtifactStore` and `LogStore`. Backends:
`fs`, `s3`, `sparkwingcache` (HTTP), `stdoutlogs`,
`sparkwinglogs`. `pkg/backends` defines the YAML configuration
surface -- including `state.type: postgres` and `state.type:
controller`, both whitelisted but not yet implemented behind
`pkg/storage/storeurl/spec.go:OpenStateStoreFromSpec`.

### Cache reservation primitive (done)

`pkg/store/concurrency.go` already implements exactly the
cross-laptop cache reservation behavior we want:

- `AcquireConcurrencySlot` atomically combines cache-lookup +
  capacity check + holder-count + policy branch.
- Returns `AcquireCached` with an `OutputRef` when a prior holder
  published a result for the same `CacheKeyHash`.
- `OnLimit:coalesce` lets followers wait on a leader and inherit
  its result.
- Holder leases with heartbeats; reaper sweeps expired holders.
- 35d TTL on the `concurrency_cache` rows.

The `.Cache()` DSL routes through this. Once everyone shares one
Postgres, cross-laptop reservation and result-borrowing fall out
for free -- no new code in this layer.

### Dashboard data abstraction (done)

`internal/backend/` defines a `Backend` interface with three
existing impls: `StoreBackend` (direct SQLite), `ClientBackend`
(HTTP-to-controller), `S3Backend` (reads `state.ndjson` dumps).
The web binary already adapts; once `*store.Store` works against
Postgres, the dashboard works against shared Postgres without
extra glue.

### Final-state S3 dump (done)

`orchestrator.DumpRunState` already writes
`runs/<id>/state.ndjson` to the artifact store at run completion.
`S3Backend` already reads it. Means a *completely
infrastructure-less* dashboard mode for **completed** runs
already works today. Extending this to live state (Mode 2) is one
of the work units below -- same file format, written incrementally
as the run progresses instead of only at the end.

## What is missing

In order of impact:

- **S3-only state backend** (Mode 2). A `StateBackend` impl that
  serializes run/node/event writes to per-run NDJSON in the
  artifact store, written incrementally as the run progresses
  (not just on completion). Plus a matching read path so the
  dashboard can serve live runs from S3.
- **Postgres impl of `pkg/store`** (Mode 3). The schema is
  SQLite-flavored but largely portable.
  `storeurl/spec.go:OpenStateStoreFromSpec` has a `TypePostgres`
  branch that returns "not implemented".
- **`RemoteBackends` constructor** (Mode 4). Symmetric to
  `LocalBackends`. Wraps a `*client.Client` + `HTTPConcurrency` plus
  an HTTP log backend into a `Backends` bundle. The pieces all
  exist; this is the assembly point. Naming a constructor and
  adding a `var _ StateBackend = (*client.Client)(nil)` assertion
  is most of the work.
- **`sparkwing run --sw-local-only` flag**. Forces SQLite + fs
  cache + fs logs regardless of `backends.yaml`. The opposite-end
  escape hatch from the shared-backends story.
- **Schema versioning** for direct-DB mode. A
  `sparkwing_schema_version` row that runners check on connect so
  N runner versions against one Postgres fail loudly on skew.
- **Dashboard wiring** against shared Postgres and against live
  S3 state. `cmd/sparkwing-web` needs to accept a state-backend
  spec; existing `Backend` impls (`StoreBackend`, `S3Backend`)
  cover both paths once the storage layer lands.
- **Cross-process integration tests** -- for both Mode 2 and Mode 3.
  Mode 2: two runs against the same bucket, second one sees the
  first's live progress in the dashboard. Mode 3: same plus
  verifying cache reservation produced an `AcquireCached` event.
- **Docs**. `docs/` page explaining the four modes, when to use
  each, what `backends.yaml` looks like for each, and the
  reservation-vs-thundering-herd tradeoff in Mode 2.

## Architecture by mode

### Mode 1: local-only

```
sparkwing run (laptop)
   └─ orchestrator
       └─ Backends{
            State:       localState{*store.Store(sqlite)}
            Logs:        localLogs{paths}
            Concurrency: localConcurrency{*store.Store(sqlite)}
          }
       └─ ArtifactStore: fs:///~/.sparkwing/cache
       └─ LogStore:      fs:///~/.sparkwing/logs
```

Selected when: no `backends.yaml`, no shared-backend env vars, OR
`--sw-local-only`.

### Mode 2: S3-only shared

```
sparkwing run (laptop or CI runner)
   └─ orchestrator
       └─ Backends{
            State:       s3State{S3 bucket}      // live NDJSON writes
            Logs:        s3LogBackend{S3 bucket}
            Concurrency: noopConcurrency{}       // skip the lease
          }
       └─ ArtifactStore: s3://shared-bucket/cache
       └─ LogStore:      s3://shared-bucket/logs

sparkwing-web (anywhere)
   └─ backend.S3Backend{S3 bucket}               // already exists
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
- **`noopConcurrency`** satisfies `ConcurrencyBackend` but always
  returns `AcquireGranted` -- no waiters, no leader election, no
  shared cache memo. Two runners computing the same key both run
  to completion and both upload to the same content-addressed
  cache path in S3. Last write wins on bytes that are identical
  by construction. Document this as the explicit tradeoff for
  Mode 2.
- **Cache reuse still works**: a runner checks
  `art.Has(ctx, cacheKey)` before computing; if it's already in
  S3, fetch and skip work. The mode you lose is *coordinated*
  reservation -- N runners arriving simultaneously will all
  compute. For cheap cacheable steps this is fine; for
  expensive ones, upgrade to Mode 3.
- **Triggers, debug pauses, approvals, runner pools** are not
  available in this mode. They require CAS over a shared key
  space, which we explicitly opted out of. The `StateBackend`
  methods that drive them return `ErrNotSupported`. The S3-only
  mode targets "run pipelines, see them in a dashboard" -- not
  the full controller-driven workflow.
- **Dashboard live updates**: `S3Backend` polls
  `runs/<id>/state.ndjson` for changes. Refresh latency = poll
  interval (default 2-5s). The existing
  `S3Backend.loadState` already does the read; cache invalidation
  on a per-run mtime check is the new piece.

Selected when: `backends.yaml` declares state as one of the
object-store types (`s3`, `gcs`, `azure-blob`). Today
`SurfaceState` only allows `sqlite`/`postgres`/`mysql`/
`controller`; we'd add `s3`, `gcs`, `azure-blob` to that
allow-list.

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
- The same orchestrator code runs in CI runners; only the
  `backends.yaml` differs.
- All runners need Postgres + S3 creds. Trust model: "anyone with
  DB creds is trusted." Fine for owned infra.

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

Selected when: `backends.yaml` declares
`state.type: controller, controller: <profile>` (or the env-var
shim resolves a controller profile).

Notes:

- The laptop in this mode is self-orchestrating, not a runner pod
  waiting on dispatches. It calls `client.CreateRun`,
  `client.CreateNode`, `client.StartNode`, etc. directly -- same
  HTTP surface the cluster worker uses, but driven by the
  laptop's own dispatch loop. No `dispatch` round trips.
- Auth uses existing tokens. Each laptop gets a runner-scoped
  token from the controller.

## Postgres schema

The SQLite schema in `pkg/store/store.go` (lines 70-380) ports
cleanly with these substitutions, applied at migration time:

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

Tables (15): `runs`, `nodes`, `events`, `triggers`,
`concurrency_entries`, `concurrency_holders`,
`concurrency_waiters`, `concurrency_cache`, `node_steps`,
`node_metrics`, `tokens`, `sessions`, `users`, `secrets`,
`debug_pauses`, `approvals`, `node_dispatches`.

Plus one new table for direct-DB mode:

```sql
CREATE TABLE sparkwing_schema_version (
    version    INTEGER NOT NULL,
    applied_at BIGINT NOT NULL,
    PRIMARY KEY (version)
);
```

On `store.Open`, the highest row's `version` is compared against
the binary's expected version. Skew handling:

- runner version > DB version: runner runs the missing migrations
  inside a single transaction. (Same behavior as today's SQLite
  `migrate()`.)
- runner version < DB version: refuse to start with a clear error
  ("DB is at schema v17; this runner expects v15. Upgrade
  sparkwing or downgrade the DB."). Fail loud; don't try to
  operate on a newer schema.

This is the cost of direct-DB mode and the reason hosted-controller
exists: the controller can run any version against any client
version of the same major; direct-DB couples runner version to
schema version.

### Locking semantics worth re-verifying

The SQLite implementation uses transaction-wrapped reads-then-writes
that are serialized by SQLite's writer-locks-database model. The
Postgres translations need explicit locks:

- `ClaimNextReadyNode` (store.go:1567) -- SQLite version does a
  `SELECT ... LIMIT 1` then `UPDATE ... WHERE claimed_by IS NULL`.
  In Postgres, use `SELECT ... FOR UPDATE SKIP LOCKED` to avoid
  thundering-herd waiters and let multiple claimants make
  progress in parallel.
- `AcquireConcurrencySlot` (concurrency.go:122) -- same pattern;
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

**In Mode 2 (S3-only):** the Postgres-mediated reservation is
deliberately omitted. The behavior degrades to:

1. Runners 1..N each `HEAD` the cache key in S3. If present,
   fetch and skip computation. **This is the common case** and
   is unchanged from Modes 3/4.
2. If absent, *every* runner computes and uploads. The
   content-addressed write target is identical, so last-write-
   wins is safe.
3. No leader election, no waiters, no shared "this run reused
   X" provenance row. The dashboard sees N independent runs that
   each happened to do the same work.

This is the explicit tradeoff for not needing a database. For
cacheable work where computation is cheap or rare, it's the
right call. For workloads where multiple runners regularly
contend on expensive uncached steps, move to Mode 3.

## --sw-local-only flag

Behavior on `sparkwing run`:

- When set: ignore `backends.yaml`, ignore env-var shim, force
  `Spec{Type: sqlite, Path: opts.DefaultStateDB}` for state,
  `fs://<paths.CacheDir>` for cache, `fs://<paths.LogsDir>` for
  logs. The orchestrator runs as if no shared config existed.
- When unset: existing precedence applies (target overlay →
  detected environment → defaults → legacy env-var shim).

Naming: `--sw-local-only` matches the existing `--sw-` namespace
convention for `sparkwing run` control flags. Help text: "Force
local state, cache, and logs for this run; ignore the configured
shared backends."

Implementation: a one-field option on `orchestrator.Options`. In
`ApplyBackendsConfig`, when set, return early after pinning the
three local specs. No changes to other call paths.

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

`.sparkwing/backends.yaml` already has the right shape. Example
for each mode:

```yaml
# local-only -- no file needed, or:
environments:
  default:
    cache:
      type: filesystem
      path: ~/.sparkwing/cache
    logs:
      type: filesystem
      path: ~/.sparkwing/logs
    state:
      type: sqlite
      path: ~/.sparkwing/state.db
```

```yaml
# S3-only shared
environments:
  shared:
    cache:
      type: s3
      bucket: my-org-sparkwing
      prefix: cache
    logs:
      type: s3
      bucket: my-org-sparkwing
      prefix: logs
    state:
      type: s3
      bucket: my-org-sparkwing
      prefix: state
```

```yaml
# direct-DB
environments:
  shared:
    cache:
      type: s3
      bucket: my-org-sparkwing
      prefix: cache
    logs:
      type: s3
      bucket: my-org-sparkwing
      prefix: logs
    state:
      type: postgres
      url_source: env:SPARKWING_PG_URL
```

```yaml
# hosted controller
environments:
  prod:
    cache:
      type: controller
      controller: prod
    logs:
      type: controller
      controller: prod
    state:
      type: controller
      controller: prod
```

`detect:` rules (already supported) auto-select environment based
on env vars -- `GITHUB_ACTIONS=true` selects the shared env, local
laptop falls through to default.

## Work breakdown

The pieces below are sized for parallel agent execution. Each
unit lists scope, files, acceptance, and dependencies.

### Unit A0 -- S3-only state backend (Mode 2)

**Scope**: add a `StateBackend` implementation that serializes
state writes to per-run NDJSON in the artifact store, plus a
`noopConcurrency` `ConcurrencyBackend`, plus a live-read path for
the dashboard. This is what enables Mode 2 with no database.

**Files**:

- `internal/orchestrator/s3state.go` (new) -- `S3StateBackend`
  implementing `StateBackend`. Each method appends a JSON envelope
  to `runs/<runID>/state.ndjson`. Writes are coalesced with a
  small in-memory buffer + periodic flush (e.g. 500ms or 16KB)
  to keep S3 PUT cost bounded; final flush on run completion.
  Methods that need CAS across runs (claim-trigger, ready-pool
  claim, debug-pause writes) return `ErrNotSupported`.
- `internal/orchestrator/noopconcurrency.go` (new) -- always
  returns `AcquireGranted`; release/heartbeat are no-ops.
- `internal/orchestrator/backends.go` -- new
  `S3Backends(art storage.ArtifactStore, log storage.LogStore)
  Backends` constructor.
- `pkg/backends/backends.go` -- extend `allowedTypes[SurfaceState]`
  to include `s3`, `gcs`, `azure-blob`.
- `pkg/storage/storeurl/spec.go` -- new `OpenStateStoreFromSpec`
  branches for the object-store types; return an opaque handle
  the orchestrator can adapt to its `StateBackend`. The current
  type alias `storage.StateStore = *store.Store` blocks this;
  see Unit B-2 (becomes a prerequisite -- or fold the alias swap
  into this unit).
- `internal/backend/s3_backend.go` -- extend the existing
  read-side `S3Backend` to:
  - serve **live** runs (today it only loads complete dumps),
  - return `ListEventsAfter` results parsed from the NDJSON tail
    instead of the current empty stub.
- `internal/orchestrator/s3state_test.go` -- unit tests covering
  the buffer/flush behavior, NDJSON round-trips, and
  `ErrNotSupported` for the CAS-requiring methods.
- `internal/orchestrator/s3state_outbox.go` (new) -- local SQLite
  outbox for offline operation. When an S3 write fails with a
  network error, stage the operation (key + bytes for state,
  artifact PUTs, log appends) in the outbox. A background
  replayer drains the outbox when connectivity returns.
- `internal/orchestrator/s3state_outbox_test.go` -- covers:
  network failure stages the write, reconnection drains in
  order, process restart resumes drain, idempotent replay
  (re-running a PUT against the same key is harmless because
  the key/bytes are unchanged for state, and content-addressed
  for cache).

**Acceptance**:

- `sparkwing run` against a backends.yaml declaring `state.type:
  s3` writes a live, growing `state.ndjson` for the run.
- The dashboard server pointed at the same bucket reflects each
  node's status changes within one poll interval.
- `.Cache()` calls on a key whose blob is already in S3 fetch
  without recomputing.
- `.Cache()` calls on an absent key compute, upload, and the
  next run (against the same bucket) hits the cache.
- Trigger-spawning pipelines fail with a clear "S3-only mode
  does not support triggers" error.
- With S3 unreachable (simulated via fault injection), the run
  completes successfully and writes accumulate in the outbox.
  After S3 becomes reachable again, the outbox drains and the
  bucket reflects the full run.

**Dependencies**: needs the `StateStore` alias to become an
interface (Unit B-2). Otherwise independent of Units A, B, C.

### Unit A -- Postgres backend for `pkg/store`

**Scope**: extend `pkg/store` to run against Postgres in addition to
SQLite. The struct stays `*store.Store`; the constructor branches
on a `Dialect` (sqlite/postgres) derived from the DSN. All query
strings stay in `pkg/store/*.go`, parameterized where the two
dialects diverge.

**Files**:

- `pkg/store/store.go` -- split the schema constant into
  `schemaSQLite` + `schemaPostgres`; add `Dialect` enum; new
  `OpenPostgres(dsn)` constructor; thread dialect through helpers.
- `pkg/store/concurrency.go`, `pkg/store/store.go`,
  `pkg/store/node_dispatches.go` -- dialect-aware locking clauses
  (`FOR UPDATE SKIP LOCKED` on the pg path).
- New `pkg/store/dialect.go` for the type + query-rewrite helpers.
- `pkg/storage/storeurl/spec.go` -- fill in the `TypePostgres`
  branch in `OpenStateStoreFromSpec`.
- `pkg/store/postgres_test.go` -- full conformance against a real
  Postgres (testcontainers-go or `PGURL` env var with skip).
- `go.mod` -- add `github.com/jackc/pgx/v5/stdlib` (recommended) or
  `github.com/lib/pq`.

**Acceptance**:

- All existing tests pass against SQLite.
- A new `TestStore_AgainstPostgres` subtest runs the same suite
  against a real Postgres and passes.
- `OpenStateStoreFromSpec` no longer returns "not implemented"
  for `TypePostgres`.

**Dependencies**: none. Largest unit; ~1-2 weeks of focused work.

### Unit B -- `RemoteBackends` constructor

**Scope**: expose the hosted-controller path to `sparkwing run`
the same way cluster workers already use it.

**Files**:

- `internal/orchestrator/backends.go` -- new
  `RemoteBackends(controllerURL, logsURL, token, *http.Client) Backends`.
  Asserts `var _ StateBackend = (*client.Client)(nil)` at package
  scope; fix any drift (the client has ~95% of `StateBackend` but
  spot-check coverage).
- `internal/orchestrator/orchestrator.go` -- when
  `opts.State` is a `*client.Client` (or a new state spec resolves
  to `type: controller`), build `RemoteBackends` instead of
  `LocalBackends` inside `RunLocal`.
- `pkg/storage/storeurl/spec.go` -- `TypeController` branch returns
  a `*client.Client` wrapped in something that satisfies
  `storage.StateStore`. (Note: `StateStore` is currently aliased
  to `*store.Store`; this alias may need to become an interface
  with both `*store.Store` and `*client.Client` as impls. The
  surface is broad -- see Unit B-2.)

**Unit B-2 -- Optional: introduce `storage.StateStore` interface**.
If Unit B finds that aliasing `StateStore = *store.Store` blocks
the controller spec, split it into an interface with the methods
the orchestrator actually needs. `internal/orchestrator/backends.go`
already enumerates them -- copy the StateBackend interface into
`pkg/storage` and use it.

**Acceptance**:

- `sparkwing run --sw-profile=prod foo` against a
  `state.type: controller` configured profile drives the run via
  HTTP, dashboard reflects it in real time.
- Cluster worker path unchanged (still passes `*client.Client` as
  State manually).

**Dependencies**: none, can run in parallel with Unit A.

### Unit C -- `--sw-local-only` flag

**Scope**: add the escape hatch.

**Files**:

- `cmd/sparkwing/action_run.go` (or wherever `sparkwing run`
  flags are parsed) -- parse `--sw-local-only`.
- `internal/orchestrator/orchestrator.go` -- new
  `Options.LocalOnly bool`.
- `internal/orchestrator/backends_apply.go` -- early-return in
  `ApplyBackendsConfig` when `LocalOnly` is true, pinning SQLite
  - filesystem.
- `docs/run.md` (or equivalent) -- document the flag.

**Acceptance**:

- `sparkwing run --sw-local-only pipeline-name` ignores
  `backends.yaml` and runs against local SQLite + fs.
- Tests verify backends.yaml that *would* fail to resolve (e.g.
  references a controller profile not present) succeeds anyway
  when the flag is set.

**Dependencies**: none.

### Unit D -- Schema versioning for direct-DB

**Scope**: refuse to operate on a schema newer than the binary
understands; auto-migrate when older.

**Files**:

- `pkg/store/store.go` -- add `expectedSchemaVersion` const;
  populate the new `sparkwing_schema_version` table; check on
  Open.
- `pkg/store/migrate_test.go` -- test forward and backward skew.

**Acceptance**:

- DB at v17, binary at v15 → `store.Open` returns a clear error
  naming both versions.
- DB at v15, binary at v17 → `store.Open` runs migrations 16 and
  17 atomically, then proceeds.
- Concurrent `store.Open` from two old binaries against a fresh
  DB → exactly one wins the migration; the other waits and
  proceeds.

**Dependencies**: Unit A (lands the schema-on-postgres concept).

### Unit E -- Dashboard against shared Postgres

**Scope**: let `sparkwing-web` serve from a Postgres-backed
`*store.Store` over a shared S3 log/artifact bucket.

**Files**:

- `cmd/sparkwing-web/*.go` -- accept a `--state-spec` flag (or
  `backends.yaml`-driven config) that resolves through
  `storeurl.OpenStateStoreFromSpec`.
- `internal/backend/store_backend.go` -- verify it works when the
  underlying `*store.Store` is Postgres-backed (should be
  no-op given Unit A).
- `internal/backend/capabilities.go` -- advertise `runs:
  "postgres"` instead of `"sqlite"` for the dashboard's frontend
  hints.

**Acceptance**:

- `sparkwing-web --state-spec=postgres://... --logs-spec=s3://...`
  starts and serves all runs the shared DB has.
- Live runs appear in the dashboard as their write transactions
  commit (no polling delay beyond browser refresh cadence).

**Dependencies**: Unit A.

### Unit F -- Cross-process integration tests

**Scope**: prove Mode 2 and Mode 3 work end-to-end.

**Files**:

- `internal/orchestrator/sharedstate_s3_integration_test.go` --
  minio/fakes3 via testcontainers, two `sparkwing run`
  invocations against the same bucket, asserts:
  1. Both runs visible in the dashboard-side S3 reader.
  2. Second run reuses the cache blob produced by the first
     (verify via `art.Has`-driven skip in the run's event log).
  3. Logs from both runs are readable from the shared bucket.
  4. A pipeline that uses `.Trigger()` fails with the expected
     `ErrNotSupported` from `S3StateBackend`.
- `internal/orchestrator/sharedstate_pg_integration_test.go` --
  Postgres + minio via testcontainers, same scenario, plus:
  1. Second run gets an `AcquireCached` outcome (not just
     a blob HEAD hit). Verify by inspecting the
     `concurrency_cache` row + captured event.
  2. Concurrent runs that race on the same uncached key:
     exactly one runs, the rest coalesce.

**Dependencies**:

- S3 test: Unit A0.
- Postgres test: Units A, B, E.

### Unit G -- Docs

**Scope**: explain the three modes; show example `backends.yaml`
for each; document `--sw-local-only`.

**Files**:

- `docs/backends.md` (or extend existing backend docs).
- `pkg/docs/content/...` if there's a generated docs flow.
- Update `README.md` if it overviews deployment modes.

**Dependencies**: Units A, B, C complete enough that examples are
real.

## Suggested execution order

```
B-2 ──┬─ A0 ─────────┐
      ├─ A ─┬─ D ────┤
      ├─ B ─┤        ├─ F ─ G
      │     ├─ E ────┤
      │     │        │
C ────┘     └────────┘  (independent, ship any time)
```

- **B-2** (`StateStore` alias → interface) blocks every backend
  implementation. Land it first; it's mostly mechanical.
- **A0** (S3-only state) and **A** (Postgres state) and
  **B** (RemoteBackends) can all proceed in parallel once B-2
  is in.
- **C** (`--sw-local-only`) is independent of everything; ship
  whenever.
- **D** (schema versioning) depends on A.
- **E** (dashboard wiring) is a thin layer over A and A0.
- **F** (integration tests) gates the release; needs A0 for the
  S3 scenario and A+B+E for the Postgres scenario.
- **G** (docs) lands last so examples are real.

## Open questions to resolve before implementation

1. **DSN style for Postgres**: do we accept a raw `postgres://`
   URL in `Spec.Path`, or invent `Spec.URL` / `Spec.URLSource`
   conventions consistent with cache and logs? Cache/Logs already
   have `URL` + `URLSource: env:VAR`; reuse those on State.
2. **pgx vs lib/pq**: pgx is the modern choice; lib/pq is in
   maintenance mode. Recommend pgx with `pgx/v5/stdlib` so we
   keep the `database/sql` interface.
3. **Migration coordination**: under direct-DB, N runners may
   open the store concurrently against a fresh DB. Pg advisory
   locks (`pg_advisory_lock(hash('sparkwing_migrate'))`) around
   the migrate path. SQLite path uses no lock today; behavior is
   unchanged there.
4. **Where does `cmd/sparkwing-web` live in the install story for
   direct-DB?** Today the web binary expects to find a controller
   or a local store. We need a clear "you can host the dashboard
   anywhere; here's how" doc + a sane default.
5. **Token model in direct-DB mode**: no controller means no
   `tokens` table consumer. The `tokens` / `sessions` / `users`
   tables are dead weight in direct-DB mode. Acceptable
   (they're tiny), but worth noting.
6. **S3 NDJSON write coalescing window**: short window means more
   PUTs (cost) and lower dashboard latency; long window means
   fewer PUTs but staler dashboards. Default 500ms / 16KB feels
   right but should be configurable per environment.
7. **What happens to in-process workflow features in Mode 2?**
   Plan-level concurrency (`.Cache().Namespace()` at plan scope)
   relies on a slot key shared across runs of the same pipeline.
   Without CAS, two concurrent pipeline runs both succeed the
   slot acquire. For most users this is invisible; for users
   relying on the namespace as a mutual-exclusion gate it's a
   silent semantics change. Document explicitly; consider a
   startup warning when a pipeline declares namespace-level
   concurrency and Mode 2 is selected.
