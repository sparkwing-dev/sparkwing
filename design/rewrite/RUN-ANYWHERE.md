# Run anywhere: ranked claims for unselected runs

Source: Korey's decision 2 of 2026-10-18 (`contracts/handoffs.md`). It replaces
the launcher-only rule for unselected runs from the earlier lead ruling "Cloud
triggers use controller dispatch". Frozen §2.3, §3.2 and §3.5 still hold.

## Which runs

A run is **run anywhere** when its sealed request has controller dispatch,
executes, and selects no placement: no `--runner` (any kind other than `any`),
no `pool=` requirement, no `class=cloud` pin, and no burst preferences. The
predicate reads only the sealed request, so retries keep it.

`SelectDispatch` keeps its rule. On Cloud an unselected execute run seals
`dispatch=controller`. Schedules already seal controller dispatch on every
controller. A self-hosted controller keeps the request's own dispatch for CLI
triggers (open question 2).

## Sealing and the plan unit (placement D, any executor)

Controller dispatch is kept rather than host dispatch. Under §2.3 the store
schedules a controller-owned run (stage 7), so every job is a ready node that
any matching claimant takes through `match.Evaluate`. Only the plan step and
generator evaluation have to change hands. Host dispatch would make the first
claimant the run host for stages 4-8, but the launcher never runs code, so it
could never hold that unit.

Registered machines, personal machines and Actions runners may now claim
`plan` (grant `triggers.claim`) and `eval` (grant `nodes.claim`) units on
`/api/v1/runners/claim`. The runner loop asks for trigger, plan, eval and job
units by default. `runners.Hosting` already runs plan and eval awards
in-process with the native owner (`owner.Plan`, `owner.Evaluate`), as a plan
pod does. A plan award ends at acceptance and the store schedules from there.
§3.2 placement D now reads "plan executor (4-6); store (7); any claimant (9)".

## Tiers and the ranking

The claimant's tier comes from its authenticated chain, never from labels:

| Tier | Claimant |
|---|---|
| `here` | a here capability bound to the run |
| `personal` | a personal machine root (owner checks below) |
| `github-actions` | an Actions push-bound machine root |
| `self-hosted` | any other registered machine root |
| `cloud` | the launcher service |

The ranking is the team setting `claim_ranking`, stored on `teams`. Unset
means the default `here, personal, github-actions, self-hosted, cloud`. An
admin sets any ordered subset with `PUT /api/v1/team/settings` or
`sparkwing team settings set --claim-ranking`. `none` (an empty list) means
first come among all tiers, with no window. A tier left out of a non-empty
ranking cannot claim run-anywhere work. Matching, repository scope, push scope,
pool reservations and billing checks still apply to every claim.

## The window

Each plan, eval and job unit has its own window `W = limits.RankingWindow`
(5 s), measured from its readiness: `triggers.available_at` for the plan,
`nodes.ready_at` for a job, and the source job's `finished_at` for an eval.
The rule reuses the burst hold machinery: `placementRunnersTx` (polls observed
within the liveness horizon) and `placementRunnerTx` (each observed claimant's
matching inputs, rechecked in the claim transaction).

- Before `ready+W`, a claim waits while a better-ranked claimant that is
  eligible for the unit, wants its kind and fits is live. The best-ranked
  claimant present therefore wins immediately.
- From `ready+W` to `ready+2W`, a claim waits only for a better-ranked eligible
  claimant whose last poll fell inside the window. That window winner has one
  more window to take its award.
- From `ready+2W`, the first eligible asker wins.

Every claim transaction locks the unit's owner, run and node rows before it
evaluates holds, so simultaneous claims serialize and one award wins. Each
poll is recorded in its own transaction before the claim. Launcher and Actions
polls are recorded too, so every tier can be observed.

## Cloud and billing

The launcher counts toward a unit only when the team can pay for it:
`eligibleUnitBillingTx`, which accepts prepaid funds, operator grants or a
card's headroom. An unfunded launcher never holds a lower tier, and it still
cannot claim. Units taken by unmetered runners create no Cloud charge, so
under the default ranking a team with its own capacity stops paying for
unselected runs. `credit_hold` stays as it is: the notice appears only when no
unmetered claimant of the team is eligible, and eligibility now includes the
tier checks. The `unclaimed_work` notice is unchanged.

## Automated runs

Runs with origin `github_app` (push, pull request, release) or `schedule` are
automated. Tier 1 never applies to them. A personal machine takes an automated
run only when its owner has opted that machine in (a per-machine setting, off
by default) and the owner is the run's owner:

- for a schedule, the account that armed it;
- for an App run, the team member whose linked GitHub identity matches the
  verified delivery's sender ID, now sealed in `AppFacts.SenderID`.

The opt-in sits in `machine_automation`, keyed by team and machine name and
recording the owner. Only the machine's owner changes it, with
`PUT /api/v1/runners/{runner}/automation` or
`sparkwing runners automation --runner NAME --automated-runs true`.

## Explicit placements

`--runner NAME|label=|here|github-actions`, `pool=` requirements, burst
preferences and App `runner: cloud|github-actions|pool=<name>` take a run out
of run anywhere. App `runner:` now defaults to `anywhere`. `cloud` becomes an
explicit pin that requires `class=cloud`, so only the launcher matches.

## Burst

Burst runs keep their sealed per-run preferences and holds, and stay outside
the team ranking. Ranking adds a tier rank beside burst's selector rank and
shares its observation scan, live-runner matching and fit checks. It does not
add a second hold engine.

## Open questions

1. A plain `pipeline trigger` has no here claimant, so tier 1 never applies to
   it. Should the submitting CLI (or the personal runner on that machine)
   become the here claimant?
2. Self-hosted controllers still seal host dispatch for CLI triggers. Should
   run anywhere apply there too?
3. In this note, "tier present" means a live eligible claimant is present.
   The `ready+2W` grace for the window winner was added here. Both need
   ratification.
4. In this note, a tier dropped from the ranking cannot claim unselected work,
   and an empty ranking means first come. This also needs ratification.
