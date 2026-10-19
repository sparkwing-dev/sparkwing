# Run anywhere: ranked claims for unselected runs

Source: Korey's decisions 2 and 13-16 of 2026-10-18 (`contracts/handoffs.md`). It replaces
the launcher-only rule for unselected runs from the earlier lead ruling "Cloud
triggers use controller dispatch". Frozen §2.3, §3.2 and §3.5 still hold.

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

`here` is sealed at intake. `pipeline trigger` sends the local runner's
credential as a proof when that runner is enrolled with the same controller.
Intake records the machine only when the proof is a live personal machine
credential of this team owned by the submitting account. Any other proof seals
nothing.

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

- Inside the window, a claim waits while a better-ranked claimant that is
  eligible for the unit, wants its kind and fits is live. The best-ranked
  claimant present wins immediately. A waiting claim records an **ask** for
  the unit (`unit_asks`: claimant, rank, time), written after the refused
  claim's transaction rolls back.
- From `ready+W` to `ready+W+A` (`A = limits.RankingAward`, 2 s), a claim
  waits only while a better-ranked claimant recorded an ask inside the window.
  Later polls cannot erase that ask. The best asker has `A` to come back.
- After that, the first eligible asker wins. An award clears the unit's asks.

Claimants keep their normal poll cadence: they do not long-poll. Every claim
transaction locks the unit's owner, run and node rows first, so simultaneous
claims serialize and one award wins.

## Cloud and billing

The launcher counts toward a unit only when the team can pay for it
(`eligibleUnitBillingTx`). An unfunded launcher never holds a lower tier and
cannot claim. Units taken by unmetered runners create no Cloud charge.

## Notices

`unclaimed_work`, `credit_hold` and `no_eligible_runner` judge claimants with
the same credentials, matching and ranking checks that claims use, Actions
runners included. `credit_hold` appears only when no unmetered claimant is
eligible. When a ranked unit has no eligible claimant online,
`no_eligible_runner` names the cause: the dropped tiers ("Cloud is off in this
team's runner ranking"), the pin ("this job targets pool=gpu only"), or "no
runner of the allowed tiers (...) is online".

## Automated runs on personal machines

Runs with origin `github_app` or `schedule` are automated. A personal machine
takes one only when all of these hold (decisions 15 and 16):

- its owner requested automated runs for it, logged in directly
  (`PUT /api/v1/runners/{runner}/automation`), and a team admin approved the
  request (`PUT /api/v1/runners/{runner}/automation/approval`). Both steps are
  audited. Admin revocation or machine revocation ends the approval, which is
  keyed by the machine's root prefix.
- the owner is the run's owner. For a schedule, that is the team account that
  armed it. For an App run, it is the team member whose linked GitHub identity
  is the commit or pull request author (`AppFacts.AuthorID`), never the
  delivery's sender. Retries keep the sealed author.
- the owner's linked GitHub login can read the run's repository and every
  extra repository. The controller checks this through the App installation's
  collaborator-permission API before the claim and caches the result for
  `limits.PersonalRepoAccess` (10 min). The claimant never supplies it.

An approved machine ranks as `self-hosted` for automated runs. Its OIDC
tokens carry `runner_kind=personal`. It never receives team git credentials
or App installation tokens.

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
