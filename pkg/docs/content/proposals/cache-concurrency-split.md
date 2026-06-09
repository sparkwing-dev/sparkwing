# Proposal: split caching from concurrency control

Status: draft

## Problem

`CacheOptions.Namespace` does double duty. It is at once:

1. the **memoization identity** -- what work this is, so its result can be
   reused, and
2. the **concurrency scope** -- which group shares a limit, and how many
   may run at once.

These are orthogonal concerns keyed off one field, and they collide.
Reusing a result keys on `(namespace, content-hash)`. Throttling a set of
*distinct* nodes together requires giving them one shared namespace --
which makes their identical content hashes collide, so one node replays
another's stored result. The only workaround is to fold a per-node
discriminator into the content hash: perturbing the *memoization key* to
express a *scheduling constraint*. That is the smell this proposal removes.

Two further gaps motivated it:

- The only concurrency lever inside a run is `--sw-workers`, a global node
  cap -- too blunt to throttle a heavy subset (e.g. database-backed test
  shards on a laptop) without slowing every cheap node too.
- `OnLimit` lists `Coalesce` (share one result) next to `Queue` (each
  runs) as if they were settings of one knob, though they imply opposite
  things about whether replaying another node's output is correct.

## The split

Two independent concerns, each keyed on the thing that defines it:

- **Cache** -- keyed on **content**. "Same work: compute once, reuse the
  result." Covers both an already-finished result (a hit) and identical
  work running right now (one computes, the rest wait and take its
  result). No scope, no group.
- **Concurrency** -- a named **group** with a budget. "Different work
  competing for a shared budget: everyone runs, we only bound how many at
  once." Never shares results.

Splitting by that question -- "is this the *same work* (share the answer)?"
vs "is this *different work* taking turns?" -- makes the collision
impossible by construction and makes each concept legible on its own.

## API

A concurrency group is defined **once** and referenced by each member node.
Both `Cache` and `Concurrency` are ordinary node modifiers, peers to
`Verify` / `Needs` / `Retry`; neither depends on the other.

```go
// Cache: content-keyed result reuse (replaces the old overloaded Cache).
func (n *JobNode) Cache(key CacheKeyFn, opts ...CacheOption) *JobNode
func TTL(d time.Duration) CacheOption

// Concurrency: a group defined once, referenced by a handle per node.
func NewConcurrencyGroup(name string, limit ConcurrencyLimit) *ConcurrencyGroup
type ConcurrencyLimit struct {
    Capacity int     // total budget in units; with the usual cost 1, "max running at once"
    Scope    Scope   // Run | Box | Global -- what the budget spans (see Scope below)
    OnLimit  OnLimit
}
func (n *JobNode) Concurrency(g *ConcurrencyGroup, cost ...int) *JobNode  // cost defaults to 1

type Scope string
const (
    ScopeRun    Scope = "run"     // key name@<runID>:   only this run's nodes
    ScopeBox    Scope = "box"     // key name@<hostID>:  per machine, even under a controller
    ScopeGlobal Scope = "global"  // key name:           across the whole fleet
)

type OnLimit string
const (
    Queue        OnLimit = "queue"          // wait for room, then run (FIFO; optional timeout)
    Fail         OnLimit = "fail"           // error now
    Skip         OnLimit = "skip"           // don't run this node
    CancelOthers OnLimit = "cancel_others"  // evict running members so this fits
)
```

`Capacity` and `cost` are plain integers in **author-defined units** -- a
slot, a gigabyte, a database container, whatever the author means by one
unit. Count-limiting is the degenerate case: capacity `N`, every member
cost `1`.

## Examples

Count limit -- "at most 2 of these at once":

```go
// defined once, above the pipeline
var dbGroup = sparkwing.NewConcurrencyGroup("db", sparkwing.ConcurrencyLimit{
    Capacity: 2,
    OnLimit:  sparkwing.Queue,
})

sparkwing.Job(plan, "shard-1", run).Concurrency(dbGroup)  // cost defaults to 1
sparkwing.Job(plan, "shard-2", run).Concurrency(dbGroup)
```

Budgeted admission plus caching, fully independent (the case that surfaced
this). When capacity comes from a per-box arg, define the group inside
`Plan()` instead of package-level -- still define-once:

```go
func (DBShards) Plan(ctx context.Context, plan *sparkwing.Plan, in Inputs, run sparkwing.RunContext) error {
    dbGroup := sparkwing.NewConcurrencyGroup("db", sparkwing.ConcurrencyLimit{
        Capacity: in.BoxUnits,           // author-supplied per machine
        OnLimit:  sparkwing.Queue,
    })

    shard := sparkwing.Job(plan, "shard-1", run)
    shard.Concurrency(dbGroup, 4)        // 2 shards fit a budget of 8
    shard.Cache(func(ctx context.Context) sparkwing.CacheKey {
        return sparkwing.Key("coverage", "shard-1")   // content key only: no scope, no collision
    }, sparkwing.TTL(7*24*time.Hour))
    return nil
}
```

## Scope

The limit is enforced by the run's existing coordination backend, so the
name is scope-agnostic on purpose: on a single machine it bounds that box
(SQLite-backed locally), and when a controller / Postgres backend
coordinates, the same named group pools across runs. "Concurrency" reads
correctly in both; "pool" would imply a machine-local thing it is not.

## Decisions settled

- **Define-once handle, not a per-node restatement.** A
  `ConcurrencyGroup` is constructed once and passed to each member. Because
  capacity lives in exactly one place, member nodes cannot accidentally
  disagree on it -- the drift the old per-node `Max` allowed is gone within
  a pipeline. (See the residual below.)
- **Budget is author-supplied, not auto-detected.** `Capacity` is a value
  the author sets -- a literal package-level group, or a group built inside
  `Plan()` from a pipeline arg/env per machine class. Sparkwing does not
  probe the box.
- **Single dimension.** Capacity and cost are one integer in
  author-defined units. No multi-field resource struct in v1.
- **In-flight dedupe is part of Cache.** "The same content is being
  computed right now" is handled by Cache -- one runs, the rest take its
  result -- the same rule as a hit, so it is not a separate policy. The old
  `Coalesce` is therefore removed from `OnLimit`. As a bonus, dedupe now
  keys on content instead of the group, fixing today's hash-blind
  coalescing.
- **Hard removal, no deprecation.** The old
  `Cache(CacheOptions{Namespace, Max, OnLimit, ...})` is removed. `Cache`
  keeps its name but takes a content key plus options; the scheduling
  fields move to `NewConcurrencyGroup` / `Concurrency`. The signature
  change is a compile error at every call site, which walks the small,
  known set of users through the migration.

## Cross-run version skew: most-restrictive wins

Two different pipeline *versions* running against the same controller can
declare group `"db"` with different `Capacity` values. Separate processes
cannot share an in-memory group, so they coordinate by the group's name in
the store. The store resolves a conflict by **most-restrictive-wins**, not
latest-wins: a concurrency cap is a safety constraint, and the only value
that honors every live participant at once is the minimum. Latest-wins
would let a stale or mistaken higher value override a deliberately low one
and overcommit the resource -- for a limit, "too permissive" is an
incident, "too restrictive" is an annoyance, so it fails safe.

Mechanics: each live holder/waiter records the capacity *it* declared, and
the effective capacity is `min` over the currently-live participants,
recomputed as they come and go (the same self-correcting recompute used for
queue positions). The consequence is an intended asymmetry -- **lowering
takes effect immediately; raising is delayed** until the last participant
still declaring the lower value drains. A drift warning still fires so the
skew is visible; it simply enforces the minimum rather than the last
writer. Local single-box runs rarely hit this; it is intrinsic to
distributed coordination by a shared name.

Policy disagreement (one member declares `Queue`, another `Fail`) is rarer
and has no obvious "safest" ordering; it stays warn-and-pick-one rather than
attempting to rank policies.

## Non-goals

- **Measured enforcement.** `cost` is a declared figure for *admission
  control* (don't start what won't fit), not a cgroup limit. A node that
  under-declares can still overcommit; this is cooperative budgeting.
  Admission alone solves the laptop-out-of-memory case and needs no
  cgroups -- it works on a plain laptop today.
- **An optional later tier: enforcement.** Translating a declared cost into
  a real container limit (`docker --memory` / `--cpus`, or cgroups v2)
  where the platform allows is hardening, not a prerequisite. Out of scope
  here.
- **Multi-dimensional / bin-packing admission.** One dimension, FIFO queue.
  Letting a cheap node skip a queued heavy one to fill spare budget is a
  possible future option, not v1.

## Open (non-blocking)

- Whether `Concurrency`'s cost is variadic-defaulting-to-1 (shown above) or
  always explicit.

## Versioning

Breaking SDK change (removes the old `Cache` shape) plus a store adjustment
(the concurrency group is keyed independently of the memo key, and
admission sums costs against capacity). Target a v0.9.0 minor release.
