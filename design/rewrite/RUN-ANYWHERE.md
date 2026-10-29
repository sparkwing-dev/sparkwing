# Run anywhere: ranked claims for unselected runs

Source: Korey's decisions 2, 13-16, 20 and 44 (`contracts/handoffs.md`). It replaces
the launcher-only rule for unselected runs from the earlier lead ruling "Cloud
triggers use controller dispatch". Frozen §2.3, §3.2 and §3.5 still hold.

## Shared boundaries (2026-10-19)

**A. Personal machines take only their owner's manual runs** (decision 20).
`automatedLineageTx` follows a retry or child back to its original request
and refuses any App or schedule origin. Intake refuses a client `schedule`
origin. `here` uses a short-lived proof the runner derives from its
credential, never the credential. Admin-approved automated runs on personal
machines (decisions 15 and 16) and the in-process controller host (decision
13's host claimant) were built, reviewed and deleted; they return as their
own reviewed work.

**B. One SQL eligibility predicate per (unit, claimant).** Every queue,
named and self-claim uses `claimSQL` for readiness, grants, bindings, repository
access, trust, labels, exclusions, placement and funding. Work is ordered by
each team's last committed award ordinal, then arrival and stable identity.
Only a successful award advances the ordinal. Claimant refusals leave other
eligible work selectable. There are no scan frontiers or demand hashes.
Private owner projections use the same predicate before job readiness, without
reserving work. Award rechecks readiness and eligibility in its
transaction. Notices explain refusals without selecting or awarding work.

**C. Awards serialize under queue arbitration.** Claims take queue
arbitration, authority and unit rows, then the ledger, as defined in
`lock_order.go`. Taking queue arbitration after a ledger lock poisons the
transaction. Ranking writes nothing: a claim is one indexed lookup.

A self-hosted `sparkwing-controller` runs no pipelines itself: without a
registered runner, unselected work shows `no_eligible_runner`.

## Which runs

`SelectDispatch` is the one intake rule on every controller, Cloud and
self-hosted alike (decision 13). Publish runs, local `sparkwing run` (origin
`local`), burst runs, a runner selection other than `any`, and a `pool=`
requirement keep host dispatch. Every other executing run seals
`dispatch=controller`, including `name=` and `class=` requirements, whose
units skip ranking for the claimants they name. Intake chooses only when the request leaves dispatch
empty, and the public API clears client-supplied dispatch. CLI triggers,
webhooks and schedules, including local schedules, all send it empty.

A unit is **ranked** when its run is run anywhere (controller dispatch,
executes, no burst, no runner selection, no pinned run requirement) and the
unit's own resolved requirements pin no `pool=`, `name=` or `class=`
alternative. Placement is resolved once, after the unit's requirements are
known. A pinned job inside a run-anywhere run skips ranking, so a dropped tier
never strands it. A ranked unit has one policy: the ranking. Burst-style
preference holds do not apply to it.

## Sealing and the plan unit (placement D, any executor)

Under §2.3 the store schedules a controller-owned run (stage 7), so every job
is a ready node selected through the shared SQL claim predicate.
Only the plan step and generator evaluation change hands. Registered machines,
personal machines and Actions runners claim `plan` (grant `triggers.claim`)
and `eval` (grant `nodes.claim`) units on `/api/v1/runners/claim`. The runner
loop asks for trigger, plan, eval and job units by default, and
`runners.Hosting` runs plan and eval awards in-process with the native owner. A plan
award ends at acceptance and the store schedules from there. A direct eval
award reads its run's sealed source bundle and its source job's output. A
generated job's snapshot carries its generator sources, so a remote node host
can replay the generator ancestry.

wingd's embedded queue holds `controller.owner`, renews plan and eval units it
owns, and claims plan, eval and job units like any runner, at the self-hosted
tier.

## Tiers and the ranking

The claimant's tier comes from its authenticated chain, never from labels:

| Tier | Claimant |
|---|---|
| `here` | the personal machine the submitting CLI ran on |
| `personal` | a personal machine root of the run's owner |
| `github-actions` | an Actions push-bound machine root |
| `self-hosted` | any other registered machine root and the local queue |
| `cloud` | the launcher service |

`here` is sealed at intake. When the local runner is enrolled with the same
controller, `pipeline trigger` sends a proof of its credential, never the
credential: an HMAC keyed by the credential's stored digest over its prefix,
the request's idempotency key and a 5-minute expiry. Intake records the
machine only when the proof verifies against a live personal machine
credential of this team owned by the submitting account.

The ranking is the team setting `claim_ranking`, stored on `teams`. Unset
means the default `here, personal, github-actions, self-hosted, cloud`. An
admin sets any ordered subset with `PUT /api/v1/team/settings` or
`sparkwing team settings set --claim-ranking`. `none` (an empty list) means
first come among all tiers. A tier left out of a non-empty ranking cannot
claim ranked work. Matching, repository scope, push scope, pool reservations
and billing checks still apply to every claim. A self-hosted controller has no
launcher, so the Cloud tier is never present there.

## The window

Each plan, eval and job unit has its own window `W = limits.RankingWindow`
(5 s) from its readiness.

- Inside the window, a unit that names no runner is held for the best-ranked
  tier that has a live claimant of the same team which is fully eligible for
  it (the shared eligibility below, including its advertised kinds, labels and
  billing) and has a free slot. Lower tiers skip the unit until that claimant
  takes it. A busy or offline better tier causes no wait.
- After the window, any eligible claimant may take the unit, by fair share and
  arrival. A claimant takes the next eligible unit with no per-runner
  preference scoring; `Prefers` orders placement for that job only.

Claimants keep their normal poll cadence. SQL selection considers live claimant
observations. Notices explain up to `limits.RankingObservations` (64) of the
most recent observations.

## Eligibility

`claimSQL` owns eligibility for every award and private owner projection.
It checks claim kinds, authority, placement, repository and push scope,
personal-machine ownership, tier, labels and funding for metered claimants.
Every requested selector term must match. Alternatives within a term use OR.
Runner size is a label. The fixed Cloud size catalog binds labels to physical
allocations and credit classes. Self-hosted runners advertise their own labels.
CPU and memory requests and free-resource observations do not select work.

Named claims use the same predicate and preserve the exact funding refusal.
The wingd queue is an observed claimant like any runner. Local foreground work
is reserved for its here owner, so the background queue cannot take its trigger.

## Notices

Notices explain recent live observations, plus each Cloud
launcher for credit standing. Any live eligible claimant leaves only the
generic `unclaimed_work` notice. Otherwise `credit_hold` appears when a
claimant failed only on billing. A run-anywhere unit then gets
`no_eligible_runner`, naming the first cause: dropped tiers ("Cloud is off in
this team's runner ranking"), the pin ("this job targets pool=gpu only"), or
"no runner of the allowed tiers (...) is online". A live claimant that is only
waiting on temporary Cloud standing capacity keeps the generic notice.

## Personal machines

A personal machine takes only its owner's manual runs, ranked `here` when its
CLI started the run and `personal` otherwise. App and schedule runs, and their
retries and children, never reach it. Its OIDC tokens carry
`runner_kind=personal`. It never receives team git credentials or App
installation tokens, and fetches with its owner's own git access.

## Explicit placements

`--runner NAME|label=|here|github-actions`, `pool=` requirements, burst
preferences and App `runner: cloud|github-actions|pool=<name>` take a run out
of run anywhere. App `runner:` defaults to `anywhere`. `cloud` is an explicit
pin that requires `class=cloud`, so only the launcher matches.

## Settled follow-ups

1. A self-hosted controller refuses `cloud` in a team ranking with 400,
   because it has no Cloud launcher.
2. A generator on a job without a typed result evaluates with no result
   (`null`) on every host. Both local and remote evaluation failed with "blob
   not found" before.
3. Job placement uses required labels. Plans and evaluations use their run's
   labels. Cloud standing capacity and funding are checked and reserved at
   award. Private snapshots check funding through the shared predicate and
   leave readiness, reservations and fairness accounting unchanged.
