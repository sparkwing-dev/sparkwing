# Observable concurrency gates (proposal)

**Status:** design settled. Smaller than expected -- the synchronous
query and the queue/holder data already exist; this is mostly event
enrichment plus a surface to read it. Not yet implemented. Part of the
"make hidden runtime state explicit" release (v0.8).

## The gap

A node that declares `.Cache(CacheOptions{Namespace: ..., Max: N})` and
arrives at a full namespace under `OnLimit: Queue` blocks in a polling
loop until promoted (`internal/orchestrator/concurrency_dispatch.go:250`).
From the outside that wait is indistinguishable from a hang: you cannot
tell whether the node is running, queued, *behind whom*, or *how many
are ahead*. For an operator staring at a stalled run, "is this wedged or
just waiting its turn?" has no answer without digging.

A `concurrency_wait` event *is* already emitted the moment a node queues
(`concurrency_dispatch.go:259`), but its payload is thin:

```go
{ "key": "<namespace>", "kind": "queued",
  "leader_run_id": "...", "leader_node_id": "..." }
```

It carries the namespace and (for coalescing) the leader, but **not the
queue position, the queue length, or who currently holds the slots.**
That is the whole gap.

## What already exists (do not rebuild)

The data and most of the plumbing are already here:

- **Schema** (`pkg/store/store.go:287`): `concurrency_holders` (active
  holders, `claimed_at`) and `concurrency_waiters` (queued arrivals,
  ordered by `arrived_at`, indexed). No migration needed.
- **Events** (`concurrency_dispatch.go`): `concurrency_wait`,
  `concurrency_promoted`, `concurrency_queue_timeout`,
  `concurrency_cancelled`, `concurrency_force_release`.
- **Synchronous query** (`pkg/store/concurrency.go:1033`):
  `GetConcurrencyState(ctx, key)` returns `ConcurrencyState{Capacity,
  Holders, Waiters}` with holders and waiters both ordered oldest-first.
- **HTTP endpoint** (`pkg/controller/concurrency.go:208`):
  `GET /api/v1/concurrency/{key}/state` already exposes that state.

The small query keyed by namespace is, in effect, already shipped: the
waiters list is ordered, so a caller can already derive position by
index. What is missing is making it *effortless* (position computed for
you) and *push-based* (in the wait event, so a wrapper or the dashboard
need not poll).

## Design

There are two natural surfaces: the acquire response and the synchronous
state query. Both already have their plumbing; we finish both, cheaply.

### 1. Enrich the acquire response, then the wait event

In the `OnLimit: Queue` branch of `AcquireSlot`
(`pkg/store/concurrency.go:441`), the waiter row is inserted inside a
transaction that already sees the full table. In that same transaction,
compute and return:

- `Position` -- `COUNT(*) FROM concurrency_waiters WHERE key = ? AND
  arrived_at < ?` (FIFO rank, 0-based or 1-based; pick one and document).
- `Holders` -- the current `concurrency_holders` rows for the key
  (run/node, `claimed_at`), capped to a small N for payload sanity.

Add these to `AcquireSlotResponse` (today `{Kind, PreviousCapacity,
DriftNote}`). Computing them in the acquire transaction makes them
atomically consistent with the queue state that produced the wait -- no
follow-up read that could race.

`waitThenRun` then emits them in the `concurrency_wait` payload:

```go
{ "key": "<namespace>", "kind": "queued",
  "position": 2, "queue_length": 5,
  "holders": [ {"run_id": "...", "node_id": "deploy-prod"} ],
  "leader_run_id": "", "leader_node_id": "" }
```

Position applies to `Queue`-policy waiters; for `Coalesce` the existing
leader fields already describe the relationship, so position is omitted
there. Optionally emit a lightweight `concurrency_wait_update` when a
waiter's position advances, so a long wait visibly ticks down; if that
is too chatty, the dashboard can refresh via the query below instead.

### 2. A first-class position in the synchronous query

`GetConcurrencyState` already returns ordered waiters; add a derived
`Position` (and `Capacity`/`queue_length`) so callers do not recompute
it by hand, and surface a focused lookup for "where is *this*
run/node?":

```
GET /api/v1/concurrency/{key}/state            # existing: holders + waiters (+ derived position)
GET /api/v1/concurrency/{key}/waiter/{run}/{node}  # optional: just this waiter's position
```

### 3. A surface to read it

- **CLI:** `sparkwing concurrency status <namespace>`, backed by the
  existing endpoint, rendering holders + the ordered queue so an
  operator sees "queued for `<ns>`: 2 ahead, held by `<run>/<node>`"
  without hand-rolling.
- **Dashboard:** populate the node's `StatusDetail`
  (`pkg/store/store.go:1399`, "phase string for the dashboard") from the
  enriched `concurrency_wait` event, so a queued node renders its
  position inline instead of a featureless spinner.

## Non-goals (explicit)

- **No change to concurrency semantics.** FIFO ordering, `Max`, and the
  `OnLimit` policies are untouched. This is purely observability.
- **No schema migration.** Holders and waiters already carry everything
  needed; position is derived, not stored.
- **Not host admission.** That is a separate proposal about throttling
  total processes on a box; this is visibility into the existing
  namespace concurrency gate. They share the "surface queue state"
  spirit and event shape, nothing else.

## Decisions

- **Both push and pull.** Enrich the wait event (push, no polling) *and*
  add derived position to the query (pull, for on-demand checks). They
  serve different consumers (dashboard stream vs. a wrapper script).
- **Compute position in the acquire transaction**, not a follow-up read,
  so it is consistent with the queue state that caused the wait.
- **Position is for `Queue` waiters**; `Coalesce` keeps its existing
  leader fields.
- **`queue_length` and `holders` are capped** in the event payload to
  keep events small; the full list is available via the query.

## What "ship it" looks like

- `AcquireSlotResponse` gains `Position int` and a compact `Holders`
  slice; populated in the `OnLimit: Queue` branch within the existing
  transaction.
- `concurrency_wait` payload enriched with `position`, `queue_length`,
  `holders`.
- `GetConcurrencyState` response gains derived `Position` per waiter;
  optional per-waiter lookup endpoint.
- `sparkwing concurrency status <namespace>` CLI command.
- Dashboard renders queued position via `StatusDetail`.
- Tests: contention burst (N+1 racing acquires -> exactly N holders, 1
  queued at position 1; the event carries position + holders); position
  advances as holders release; coalescing waiters omit position.
- CHANGELOG entries: `**cli:**` for the command, `**controller:**` for
  the enriched event/query.
