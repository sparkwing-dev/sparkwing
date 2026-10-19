# Run anywhere: ranked claims for unselected runs

Source: Korey's decisions 2 and 13-16 of 2026-10-18 (`contracts/handoffs.md`). It replaces
the launcher-only rule for unselected runs from the earlier lead ruling "Cloud
triggers use controller dispatch". Frozen §2.3, §3.2 and §3.5 still hold.

## Restructure plan (2026-10-19, Sol re-review and Opus re-review 2)

Three shared boundaries replace the per-path patches.

**A. Automation identity, sealed at intake.** `runspec.Request.Automation` is
server-derived and client-cleared. It holds the delivery's `SenderID` and the
code's `AuthorID` (linked GitHub IDs, App runs), or `ArmedBy` (the schedule
armer account), plus the immutable `Repos` (`gh:<id>` for the primary and each
extra repository). Its presence is the automated flag. Children copy the
parent's, and retries keep it because they keep the sealed request. Release
events take the commit author, never the publisher; branch create/delete stay
unowned. Personal eligibility, the tier, repository access and the access
cache all read it: an App run belongs to a member only when the sender equals
the author on that member's linked account. Access rows are keyed by `gh:<id>`,
names are resolved from the ID only for the collaborator call, entries live
60 s, and the controller re-checks access live when it awards a unit to a
personal machine, releasing the unit on a denial. Needs come from the units the
machine could claim, not a scan of recent runs. Intake refuses a client
`schedule` origin. Approval revocation deletes every live generation of the
machine. `here` uses a short-lived proof the runner derives from its
credential, never the credential.

**B. One eligibility result per (unit, claimant).** `unitEligibilityTx`
returns the claimant's matching inputs, tier rank, kind, liveness, capacity
fit and billing (for every metered claimant) in one value. Queue scans, named
claims, ranking holds and notices all derive from it. The notice survey is
deleted: notices evaluate the same live observations the ranking scans
(capped per claim), including the local queue and the controller host, and
report the first failing step (tier dropped, pin, nobody online, capacity,
credits).

**C. Asks inside the ranking transaction.** A held claimant's ask is written,
under the unit's claim lock, in the same transaction that scans the queue,
whether that scan awards another unit or nothing. Awards read asks under the
same lock. `RankingAward` (2 s after the window, worst case 7 s) is the lead
amendment to decision 2.

The non-Cloud `sparkwing-controller` composes the same host claimant runtime
as wingd, so every self-hosted controller can host unselected runs.

## Which runs

`SelectDispatch` is the one intake rule on every controller, Cloud and
self-hosted alike (decision 13). An executing run that selects no runner, no
`pool=` requirement and no burst preferences seals `dispatch=controller`.
Local `sparkwing run` (origin `local`), burst, publish and explicitly placed
runs keep host dispatch. Intake chooses only when the request leaves dispatch
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

The controller host is a self-hosted claimant (decision 13). wingd's embedded
queue holds `controller.owner`, renews plan and eval units it owns, and claims
plan, eval and job units like any runner.

## Tiers and the ranking

The claimant's tier comes from its authenticated chain, never from labels:

| Tier | Claimant |
|---|---|
| `here` | the personal machine the submitting CLI ran on |
| `personal` | a personal machine root of the run's owner |
| `github-actions` | an Actions push-bound machine root |
| `self-hosted` | any other registered machine root, the local queue, and a personal machine approved for automated runs when it takes one |
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
issuance, which reports the exact credit standing. The wingd queue and the
controller host are observed claimants like any runner.

## Notices

Notices judge the live observations the ranking scans, plus each Cloud
launcher for credit standing. Any live eligible claimant leaves only the
generic `unclaimed_work` notice. Otherwise `credit_hold` appears when a
claimant failed only on billing. A run-anywhere unit then gets
`no_eligible_runner`, naming the first cause: dropped tiers ("Cloud is off in
this team's runner ranking"), the pin ("this job targets pool=gpu only"), or
"no runner of the allowed tiers (...) is online". A live claimant that is only
short of capacity keeps the generic notice.

## Automated runs on personal machines

Intake seals `Automation` on runs from a verified App delivery, from a
schedule firing, and on their children; retries keep it. Clients cannot set
it, and intake refuses a client `schedule` origin. A personal machine takes
an automated run only when all of these hold (decisions 15 and 16):

- its owner requested automated runs for it on a direct sign-in, and a team
  admin approved (`PUT /api/v1/runners/{runner}/automation` and
  `.../automation/approval`). Both are audited and cover every live credential
  generation of the machine, so revocation also ends a rotation predecessor's
  approval. Machine revocation ends it too.
- the sealed owner is the machine's owner: the arming member of a schedule, or
  for an App run the member linked to the author when the author is also the
  event's sender. A release or branch event is unowned. A commit whose email
  matches a member never routes to that member's machine unless that member
  pushed it.
- the owner's linked GitHub login can read every sealed repository, by
  immutable ID. The controller resolves each name from its ID under a token
  restricted to those IDs, checks the collaborator permission, caches the
  answer for `limits.PersonalRepoAccess` (60 s) for the units the machine
  could claim, and rechecks live when it awards a unit, releasing it on a
  denial. The claimant never supplies it.

An approved machine ranks as `self-hosted` for automated runs. Its OIDC
tokens carry `runner_kind=personal`. It never receives team git credentials
or App installation tokens, and fetches with its owner's own git access.

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
