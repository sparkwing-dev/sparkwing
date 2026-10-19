# Run anywhere: ranked claims for unselected runs

Source: Korey's decisions 2 and 13-16 of 2026-10-18 (`contracts/handoffs.md`). It replaces
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

**B. One eligibility result per (unit, claimant).** `unitEligibilityTx`
returns the claimant's matching inputs, tier rank, kind, liveness, capacity
fit and billing (for every metered claimant) in one value. Queue scans, named
claims, ranking holds and notices all derive from it. A claimant-specific
refusal, an unpriced Cloud class included, is an ineligible result, never an
error that aborts another claimant's check. Notices evaluate the live
observations the ranking scans (capped per claim), including the local
queue, and report the first failing step.

**C. Asks and awards serialize under one ranking lock per unit.** A held
claimant's ask is written in its claim transaction, whether that scan awards
another unit or nothing. Every ask and award takes the unit's ranking lock
before reading readiness; scans take these locks in one global unit order.
`RankingAward` (2 s after the window, worst case 7 s) is the lead amendment to
decision 2.

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
is a ready node that any matching claimant takes through `match.Evaluate`.
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

- Inside the window, a claim waits while a better-ranked claimant is live and
  fully eligible for the unit (the shared eligibility below, including its
  advertised kinds, capacity and billing). The best-ranked claimant present
  wins immediately. A waiting claim records an **ask** (`unit_asks`:
  claimant, rank, time) under the unit's claim lock, in the claim
  transaction itself, whether that transaction awards another unit or none.
- From `ready+W` to `ready+W+A` (`A = limits.RankingAward`, 2 s, the lead
  amendment to decision 2), a claim waits only while a better-ranked claimant
  recorded an ask inside the window. The best asker has `A` to come back.
- After that, the first eligible asker wins. An award clears the unit's asks.

Claimants keep their normal poll cadence. A ranking check evaluates at most
`limits.RankingObservations` (64) of the most recent live observations.

## Eligibility

`unitEligibilityTx` is the one judgment of a claimant for a unit: claim
kinds and advertised kinds, authority and placement (repository, push and
personal-machine scope, tier inside the ranking), matching, advertised
capacity, and billing for every metered claimant. Queue scans, named claims,
ranking holds and notices all call it. Named claims leave billing to award
issuance, which reports the exact credit standing. The wingd queue is an
observed claimant like any runner.

## Notices

Notices judge the live observations the ranking scans, plus each Cloud
launcher for credit standing. Any live eligible claimant leaves only the
generic `unclaimed_work` notice. Otherwise `credit_hold` appears when a
claimant failed only on billing. A run-anywhere unit then gets
`no_eligible_runner`, naming the first cause: dropped tiers ("Cloud is off in
this team's runner ranking"), the pin ("this job targets pool=gpu only"), or
"no runner of the allowed tiers (...) is online". A live claimant that is only
short of capacity keeps the generic notice.

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
3. A runner's job claims are bounded by its measured headroom. Plans and
   evaluations carry no core demand, so a runner on a busy machine can take a
   plan and then not fit its one-core jobs until the machine has headroom
   again. Ranking records no ask for a claimant that does not fit.
