# Concurrency participant lifecycle as one state machine (proposal)

**Status:** transition verbs implemented; full re-sequencing of the
acquire switch deliberately deferred. Each lifecycle edge now has one
writer -- `txInsertHolder` (admit), `txPark` (wait), `txSupersede`
(evict), `txDeleteHolder`/`txDeleteWaiter` (reap), `txReleaseHolder`
(release), `txDrainCoalesceFollowers` (resolve) -- enforced by the
source-guard tests, and every budget-mutating path serializes on the
key's entries row. The acquire entry point still selects among the
verbs with its eight-way policy switch; collapsing that selection
further remains future work, guarded by the property suite.

## The remaining structural gap

A participant in a concurrency key is conceptually one entity moving
through states:

```
arriving -> holder -> released
         -> waiter -> promoted (holder) | cancelled | timed out
holder   -> superseded -> reaped | reclaimed
holder   -> lease-expired -> reaped | reclaimed
```

The store does not model this; it stores two row kinds (holder, waiter)
and every transition is a hand-written SQL sequence inside one of nine
mutating entry points. The hardening pass made the *invariants* single-
definition and added runtime checks, but the *transitions* are still
written per-entry-point. That is where the residual twin risk lives:

- Admission-on-acquire and admission-on-promotion are two code paths
  that must agree on what "admit" means (budget check, holder mint,
  waiter cleanup). They now share `txConcurrencyAccounting` and
  `txInsertHolder`, but the sequencing around them is still duplicated.
- The acquire entry point branches eight ways (cached, idempotent
  re-acquire, grant, skip, fail, coalesce, cancel-others, queue) with
  per-branch commit handling. A new policy means editing this switch
  and remembering every interaction with the others.

## Proposed shape

Introduce an internal transition layer in `pkg/store` so each lifecycle
edge has exactly one function, and entry points only *select*
transitions, never write rows:

```go
txAdmit(tx, participant, budget)      // waiter/arrival -> holder
txPark(tx, participant, policy)       // arrival -> waiter
txSupersede(tx, holder)               // holder -> superseded
txReap(tx, holder|waiter)             // terminal cleanup
txResolve(tx, waiter, resolution)     // waiter -> cached/leader/cancelled
```

`AcquireConcurrencySlot`, `ReleaseAndNotify`, `CancelWaiter`, the
reapers, and `txPromoteWaiters` become orchestrations of those five
verbs. The invariant checker stays at the commit boundary as the
backstop; the verbs make the per-transition bookkeeping (waiter-row
cleanup on admit, cache write on release) impossible to forget rather
than merely checked.

## Why not now

The verbs cut across the acquire switch, which is the most behavior-
dense code in the store and freshly stabilized by the v0.9.1 fixes and
the new property suite. The mechanical risk of re-sequencing it
outweighs the marginal safety gain while the helper + checker layer is
new. Revisit once the property suite has soaked (it is seedable; widen
its op mix first), and do the migration one verb at a time, keeping the
suite green after each.

## Smaller siblings worth folding in

- The two slot-heartbeat loops
  (`internal/orchestrator/concurrency_dispatch.go` and
  `internal/orchestrator/plan_cache.go`) share cadence, lease window,
  and contact-loss bookkeeping but differ deliberately in what they do
  on supersede (node: cancel execution; plan: record only). A shared
  loop taking an on-supersede callback would keep the fail-closed
  arithmetic in one place without flattening that policy difference.
- Run, node, and trigger status strings (`'pending'`, `'claimed'`,
  `'running'`, `'done'`) are SQL literals spread across `pkg/store`
  with the transition rules implicit. The same treatment applied here
  (named fragment constants plus a source-guard test counting them)
  would make a status added in one reaper unmissable in its siblings.
- The outcome and policy switches in
  `internal/orchestrator/concurrency_dispatch.go` (`storeOutcome`,
  `followerOutcomeFromLeader`) are sound today but silently absorb new
  enum values into their default arms. An exhaustiveness lint over
  `sparkwing.Outcome` and the store policy constants would turn a new
  value into a build-time error instead of a default-arm surprise.
