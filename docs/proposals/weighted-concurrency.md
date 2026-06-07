# Proposal: weighted concurrency (resource-cost admission)

Status: draft

## Problem

The concurrency gate today is **count-based**. `Cache(CacheOptions{Namespace,
Max, OnLimit})` admits up to `Max` concurrent holders of a namespace, and
every holder costs exactly one slot. That is the right model when the
gated nodes are interchangeable (one deploy slot, one migration lock).

It breaks down when the gated nodes are **resource-heterogeneous on one
box**. The motivating case: a pre-merge pipeline runs ~5 database-backed
test shards, each spinning its own DB container (several GB of RAM), plus
many cheap unit-test and lint nodes. On a 16 GB laptop the heavy shards
pile onto the same machine and exhaust it.

The existing levers don't fit:

- `--sw-workers` (a.k.a. `SPARKWING_WORKERS`) caps *total* concurrent nodes
  for the whole run. Setting it low enough to tame the heavy shards also
  throttles every cheap node, so the whole DAG slows down.
- A per-group `Cache(Namespace, Max)` on just the heavy shards is better --
  it caps the shards without touching the cheap nodes -- but it's still a
  flat count. You can't pool a shared budget across nodes of different
  cost: "this box affords 8 units; a DB shard costs 4, a build costs 2, a
  unit test costs 1; admit until the budget is spent."
- There is no per-node CPU/memory limit in local execution (that exists
  only for the Kubernetes runner, as pod requests/limits).

So an author who wants "no more than the box can hold, weighted by how
heavy each node is" has no primitive for it. They approximate with several
hand-tuned count namespaces and still can't share one budget.

## Goal

A node declares a **cost**; a namespace declares a **budget**; the gate
admits arriving nodes until the budget is exhausted, queuing (or
coalescing, per `OnLimit`) the rest. Count-based behavior is the special
case where every cost is 1.

## Non-goals

- **Measured enforcement.** Weight is a *declared* cost for admission
  control, not a cgroup/limit. Sparkwing does not measure or cap a node's
  actual CPU/RAM. A node that under-declares its cost can still overcommit
  the box; this is cooperative budgeting, not isolation.
- **Auto-derived budgets.** v1 budgets are author-set integers. Deriving a
  budget from the machine (NumCPU, total RAM) is a possible later layer,
  out of scope here.
- **Bin-packing / backfill scheduler.** v1 keeps the existing FIFO queue
  semantics. Smarter packing (let a cheap node skip ahead of a queued
  heavy one to fill spare budget) is a future optimization.

## Design

Add a per-acquisition weight and reinterpret the namespace's `Max` as a
budget expressed in those weight units.

```go
// Box budget of 8 units, shared across heterogeneous nodes.
// A DB-backed shard costs 4:
dbShard.Cache(sparkwing.CacheOptions{
    Namespace: "box-load",
    Max:       8, // budget, in weight units
    Weight:    4, // this node's cost
    OnLimit:   sparkwing.Queue,
})

// A heavy build costs 2:
build.Cache(sparkwing.CacheOptions{Namespace: "box-load", Max: 8, Weight: 2})

// A cheap unit-test node costs 1 (the default):
test.Cache(sparkwing.CacheOptions{Namespace: "box-load", Max: 8})
```

With this, the namespace admits any mix whose summed weight is `<= 8`: two
DB shards (4+4), or one shard plus two builds (4+2+2), or eight unit
tests, etc. The heavy shards self-limit against the same budget the cheap
work draws from.

### API

`CacheOptions` gains one field:

```go
// Weight is this node's cost against the namespace budget (Max).
// Zero or unset = 1, so a namespace where every node omits Weight
// behaves exactly like today's count-based Max. Only meaningful when
// Namespace is set.
Weight int
```

`Max` keeps its name but is documented as "the namespace budget, in weight
units." With uniform `Weight: 1` it is identically today's max-concurrent
count, so existing pipelines are unchanged with zero edits.

### Mechanism

The store's slot model already carries `Capacity` (the `Max`) and tracks
active holders. The change is to sum weights instead of counting rows:

- `AcquireSlotRequest` gains `Weight int` (default 1).
- A holder row records its weight.
- Admission changes from `activeCount < Capacity` to
  `activeWeight + reqWeight <= Capacity`; `openSlots = Capacity -
  activeCount` becomes `openBudget = Capacity - activeWeight`.
- Queue / coalesce / cancel-others policies and the lease/heartbeat
  machinery are unchanged.

Because the default weight is 1, `activeWeight == activeCount` for every
existing namespace, so the behavior is bit-for-bit the same until someone
sets a weight. It is a store-level change, so it applies uniformly to the
SQLite (local), Postgres, and controller backends.

## Backward compatibility

Fully backward compatible. `Weight` defaults to 1; the admission math
reduces to the current count check; no migration of existing pipelines or
data. The only schema addition is a weight column on the concurrency
holder row, defaulting to 1 for existing rows.

## Open decisions

1. **A node whose `Weight` exceeds `Max`.** It can never fit, so the
   choices are: (a) reject at plan validation; (b) clamp to "runs only
   when the budget is fully free" (admit when `activeWeight == 0`) with a
   warning. Lean: (b) -- a misconfigured weight degrades to "runs alone"
   rather than deadlocking the run.
2. **Head-of-line blocking vs. backfill.** With strict FIFO, a queued
   heavy node (needs 4, only 2 free) blocks cheaper nodes behind it,
   wasting budget until it can run. Strict FIFO matches existing queue
   semantics and is simplest; backfill (let cheap nodes skip ahead) is
   better utilization but risks starving the heavy node and is harder to
   reason about. Lean: strict FIFO for v1, document the tradeoff, leave
   backfill as a future opt-in.
3. **`Max` naming.** Reinterpreting `Max` as a budget is backward
   compatible but the name reads as "max count." Options: keep `Max`
   (documented as budget; count is the weight-1 case) or add a `Budget`
   alias. Lean: keep `Max`, minimal surface.

## Observability

The existing observable-concurrency surfaces (`sparkwing cluster
concurrency`, the `concurrency_wait` event and `runs status` detail) extend
naturally: report budget used / available instead of slot count, and the
queued-node detail already shows position and holders. Queue depth on a
weighted namespace remains the signal for "this box is saturated" -- the
data-driven input to a decision about whether to add machines.

## Relationship to other knobs

- `--sw-workers` stays the orthogonal global cap on total concurrent nodes.
- `--sw-box-slots` stays the orthogonal cap on concurrent *runs* per box.
- This proposal refines the per-namespace `Max` from a count into a
  weighted budget. None of the three measures actual resource use; that
  remains a non-goal locally and a Kubernetes-runner feature otherwise.

## Versioning

SDK field addition plus a store column, backward compatible. Target a
v0.9.0 minor release.
