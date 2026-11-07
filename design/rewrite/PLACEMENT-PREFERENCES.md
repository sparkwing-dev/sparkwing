# Placement preferences for repository runs

Status: design, not implemented. Date: 2026-11-07. Implement after local
controller step 3 (local jobs through the runner path, see
[local controller restart](proposals/local-controller-restart.md)).

This note is the short version Korey reviews. The longer
[repository run preferences proposal](proposals/repository-run-preferences.md)
keeps the detailed GitHub dispatch, receipt and OIDC contract and its failure
table. Where the two disagree, this note wins.

## Decided

| Decision | Source |
| --- | --- |
| Ordered `prefer` tiers with a `hold` each apply to push, pull request, release and schedule runs, not only personal burst runs | Korey, 2026-11-05 |
| Tiers are team runners selected by labels or name, Sparkwing Cloud, and GitHub Actions | Korey, 2026-11-05 |
| GitHub Actions is dispatched on demand, only after earlier tiers' holds stop blocking | Korey, 2026-11-05 |
| One GitHub workflow run per job. Batch only with a measured need | Korey, 2026-11-06 |
| An omitted `hold` is `0s` | Korey, 2026-11-06 |
| The suggested pre-release setup holds `5s` on each team OS tier | Korey, 2026-11-06 |
| First use: pre-release macOS and Windows cells, Korey's machines first and GitHub-hosted runners second | Korey, 2026-11-05 |

## What exists today

Three hold mechanisms already live in the store's claim query
(`internal/store/claim_query.go`, `placementCTEs`). All three measure from the
job's ready time on the store clock, and all three hold only while a better
claimant is live, matching and has a free slot. An absent tier never delays
anything.

| Mechanism | Who sets it | Default | Applies to |
| --- | --- | --- | --- |
| SDK `Prefers` hold | Controller `--placement-hold`, `--default-prefer-labels` | 20s | Any job with `Prefers`, or the controller default labels |
| Team claim ranking | `sparkwing team settings set --claim-ranking` over `here`, `personal`, `github-actions`, `self-hosted`, `cloud` | `limits.RankingWindow`, 5s | Run-anywhere jobs that pin no runner, pool or class |
| Burst preferences | Machine `run.routing.prefer`, sealed per request | `here` 5s, `machines` 5s, then `cloud` | Personal asserted runs only |

Repository preferences become a fourth source, and the only one a repository
can declare. The engine stays one: repository tiers are another `steps` source
in `placementCTEs`, sealed into the run request like burst preferences. No
ranking moves into the runner, the console, the Cloud launcher or a workflow.

GitHub Actions work today comes from an eager workflow (`on: push`) running
`sparkwing-runner warm --github-actions`, bound to the push's repository, ref
and commit. On Sparkwing Cloud, App runs go to the Cloud launcher unless the
automation pins `runner: github-actions` or a pool. Nothing dispatches a
workflow on demand yet, and the App has no `actions: write` permission.

## Config shape

Placement lives in `.sparkwing/sparkwing.yaml` on the pipeline entry, with
optional per-job overrides keyed by job ID. No SDK change and no pin bump.

```yaml
pipelines:
  - name: pre-release
    entrypoint: PreRelease
    placement:
      prefer:
        - target: team
          hold: 5s
        - target: cloud
    jobs:
      macos:
        placement:
          prefer:
            - target: team
              labels: [os=darwin]
              hold: 5s
            - target: github-actions
              workflow: .github/workflows/sparkwing-fallback.yaml
              runner: macos-latest
      windows:
        placement:
          prefer:
            - target: team
              labels: [os=windows]
              hold: 5s
            - target: github-actions
              workflow: .github/workflows/sparkwing-fallback.yaml
              runner: windows-latest
```

Rules:

- Targets: `team` (optionally narrowed by `labels`, same term syntax as
  `requires`), `name=<runner>`, `cloud`, `github-actions`. `here`, `machines`
  and `pool=` stay burst-only: personal machines never take repository work,
  and reserved pools already need an explicit `requires`.
- `hold` omitted means `0s`. Holds sum along the list; tier N opens at ready
  time plus the holds of tiers before it.
- The list is the whole allowed set. Expiry changes priority, never membership.
  A pipeline with no `cloud` entry never spends Cloud credits on those jobs.
- `github-actions` must be last and names an approved workflow and hosted image.
  `runner` picks from images the team owner approved; it is not a free `runs-on`.
- A job block replaces the pipeline list; it never appends. An explicit empty
  list restores today's routing for that job.
- Omitting `placement` entirely keeps today's behavior. No repository-wide
  default is added.
- Job overrides carry only `placement`. Requirements stay where they are today:
  SDK `Requires` and pipeline `requires`. The cells declare
  `Requires("os=darwin")` in Go.

How it composes with what exists:

| Existing surface | With an explicit repository list |
| --- | --- |
| SDK `Requires`, pipeline `requires` | Hard filter in every tier. A `darwin` job can never fall to a Linux image. |
| Tier `labels` | Narrow that tier only, never later tiers. |
| SDK `Prefers` | Orders claimants inside an open tier (open question 2). |
| Controller `--placement-hold`, `--default-prefer-labels` | Not applied to that job. |
| Team claim ranking | Still filters which claimant classes are allowed; adds no hold. A listed tier the ranking excludes is a lint error. |
| Automation `runner: github-actions` or `runner: pool=` | Conflict. Lint names both settings; neither silently wins. |
| Pipeline `run.route: local` | Conflict with any `cloud` or `github-actions` tier. Lint error. |
| `sparkwing run`, `--sw-route cloud`, machine burst | Unchanged. Asserted runs never read repository placement. |

Which runs read it: runs whose intake seals repository proof. That is App
push, pull request and release deliveries, App-armed schedules, and Run now on
a commit Sparkwing fetched. The policy comes from the verified default-branch
config, sealed with its revision next to the event's execution revision, so a
pull request cannot add a paid tier or pick a workflow (open question 3).
Unknown job IDs fail at plan acceptance with a located error; generated jobs
are checked when expansion closes. `sparkwing pipeline lint` prints the
resolved list for every known job.

Inline jobs stay on their run host and cannot carry a placement override; lint
says so. Each pre-release cell must be its own dispatched job.

## Controller behavior

**Where holds are evaluated.** In the store, in the one claim predicate used
by queue polls, named claims, owner self-execution and Cloud launcher demand.
The run request carries the sealed list. A claimant's tier is the earliest
listed tier it matches. Later tiers are excluded while an earlier tier has a
live, admissible claimant and its deadline has not passed. Fair share and
arrival order across jobs are unchanged.

**Opening GitHub Actions.** There is no idle claimant to wait for, so the
controller acts. A leased dispatcher loop asks the store for jobs whose
`github-actions` tier is open, unclaimed and without a dispatch intent. Under
the same job lock as claim selection it writes one intent per job and
placement generation, then sends `workflow_dispatch` outside the transaction.
The intent reserves nothing: a team runner that comes online keeps claiming
until some claimant wins the award. The workflow's runner proves itself by
OIDC bound to that intent and can claim only that job at that job's trust.
A loser exits idle and costs its GitHub startup minute.

**Restart.** Everything is in the store: the sealed list, the job's ready
time, the store clock (now monotonic within a process), and dispatch intents.
A restart neither resets a hold nor resends a dispatch. An intent caught in
`sending` becomes `uncertain`; GitHub's `204` carries no run ID, so the
controller cannot tell a lost request from a slow runner. Uncertain intents
are shown and never resent automatically; earlier tiers stay claimable.

**Retries and returned awards.** An award returned before start (busy machine,
failed Cloud capacity) keeps the original ready time, so holds cannot renew
forever. A new attempt after a started attempt failed gets a fresh ready time
(open question 5).

**Cancellation.** Cancelling the run or job removes it from ready work,
invalidates its dispatch intent and cancels the known GitHub run through the
same API permission. A late workflow runner finds nothing to claim and exits
idle. A placement hold never preempts an awarded job.

**No tier available.** The job stays queued under today's queue policy with a
reason per tier. No new timeout or cap is added (open question 4). A tier that
definitely cannot serve (missing workflow, Actions disabled, App permission not
approved, no Cloud credits) is marked unavailable with its repair, and the
remaining tiers keep working. A config that can never place, such as
`Requires("os=darwin")` with only a Linux image listed, fails at validation.

**Cloud-hosted controllers.** Today Cloud intake sends every unpinned App run
to the launcher. With an explicit list, job units follow the list instead.
Planning and evaluation keep today's placement (open question 1).

## What the dashboard and CLI show while a job is held

The controller computes the projection; the browser and CLI only render it, so
both agree and neither uses its own clock. `sparkwing runs status --run <id>`
and the run page show, per waiting job:

```text
macos   waiting   team os=darwin (build-mac live, 1 slot)   GitHub Actions opens in 3s
windows waiting   team os=windows: no live runner           GitHub Actions windows-latest accepted 4s ago
```

The job panel lists the resolved tiers, the policy revision, the required
labels, the active tier, its deadline, and why each later tier is waiting or
unavailable. It separates offline, busy, label mismatch, funding refusal and
provider queue. Dispatch states are `requested`, `accepted`, `refused` with
the masked reason, and `uncertain`. A GitHub run links once registration binds
its ID. After award, the existing `placement_reason` shows the winning tier,
and a losing fallback worker shows as "exited idle, lost to build-mac". This
extends `unclaimedNotice` in `internal/controller/unclaimed_notice.go` with a
placement-hold notice kind; it is not a new view.

## Billing

- **Holds cost nothing.** A held job has no Cloud award, credit hold or
  standing use.
- **Cloud tier.** Unchanged award-based billing: credit reservation when the
  Cloud tier wins, renewal, settlement, and one refund of an unstarted hold.
  Funding is checked when the Cloud tier opens, not at intake. A pipeline
  whose list starts with `team` must not be refused at intake for zero credits
  when team runners can serve it; an unfunded Cloud tier reports why it cannot
  win while other tiers stay open. Planning that runs on Cloud keeps its charge
  even when every job runs elsewhere, so such a run is not shown as free.
- **GitHub Actions tier.** GitHub bills the repository's account. Sparkwing
  adds no fee for it in this design; a fee would be a separate pricing
  decision. GitHub rounds each job up to a whole minute, and macOS and Windows
  hosted minutes cost more than Linux on private repositories, so the run page
  says plainly that fallback can spend Actions minutes and counts dispatches
  that lost the race.
- **Team runners.** Unmetered, as today.

## Implementation slices

Sizes: S half a day, M one to two days, L three to five days, tests included.

1. **Config, validation and sealing (M).** Parse `placement` and `jobs`,
   default-branch sourcing, sealing with revision, conflict lint, `pipeline lint`
   preview. No runtime effect yet.
2. **Store placement for team, name and Cloud tiers (L).** Repository steps in
   `placementCTEs`; suppress the controller and ranking holds for explicit
   lists; job units leave the Cloud-only launcher path on Cloud-hosted
   controllers; funding checked at tier open. Usable alone: team first, Cloud
   second.
3. **Held-job projection (S).** Notice kind, `runs status` lines and run page.
   Lands with or right after slice 2 so holds are never invisible.
4. **Pre-release cells on team runners (M).** Split the macOS and Windows checks
   into dispatched jobs with `Requires`, enroll Korey's machines as team
   runners, add the YAML. No fallback yet: if a machine is off, the cell waits
   visibly (open question 6).
5. **On-demand GitHub Actions (L).** App `actions: write` and approval flow,
   owner-approved workflow and images, dispatch intents and sender lease,
   receipt-bound OIDC exchange for tag and PR refs, cancellation, fallback
   workflow with no event triggers.
6. **Docs and live acceptance (S).** Config reference, scheduling and Actions
   docs with their drift checks, then the live scenario below.

## Test plan

- **Config (unit).** Parsing, replace versus inherit, empty list, unknown and
  generated job IDs, label alternatives, duration bounds, Actions-last rule,
  every conflict row in the composition table.
- **Store (SQLite and PostgreSQL, controlled clock).** Holds open on time;
  an absent, stale, saturated or wrong-scope claimant never holds; fair share
  and arrival order unchanged; controller and ranking holds suppressed for
  explicit lists and intact without them; restart and returned awards keep the
  ready time. Race a team claim, a Cloud launch and an Actions intent on two
  controller replicas and assert one award.
- **Dispatch (fake GitHub at the HTTP boundary).** Exactly one request per job
  generation, including after a lost `204` and a restart mid-send; refusal,
  missing workflow, disabled Actions, missing permission, rate limiting;
  cancellation cancels the provider run; a team claim during an outstanding
  dispatch wins and the late worker exits idle.
- **OIDC receipt.** Wrong repository, workflow or attempt; spoofed receipt
  input; replayed exchange; PR code minting the job's token gets only that
  job's trust.
- **Billing.** No Cloud charge during holds; one settlement per Cloud award;
  zero credits do not refuse a team-first run; Cloud planning still charged.
- **Projection.** CLI and console text against committed store state.
- **End-to-end scenario (real time, second actor).** A pre-release run with
  Korey's Mac live claims the macOS cell inside its hold and starts no workflow.
  With the Windows machine off, the Windows cell dispatches one
  `windows-latest` workflow at once. Bring the Mac online during an outstanding
  dispatch and confirm it can still win. Cancel during dispatch; restart the
  controller between intent and response. Repeat on a Cloud-hosted controller.
  Keep provider run IDs, dispatch counts and timings.

## Open questions

1. **Where do planning and evaluation run when jobs have repository tiers?**
   Options: (a) unchanged, Cloud launcher on Cloud-hosted controllers and the
   controller owner when self-hosted; (b) follow the pipeline list minus
   `github-actions`; (c) a separate `planning` list. Recommendation: (a) for
   now. Planning is short, the first use needs only the cells, and (b) makes a
   Mac's absence delay every job's plan. Revisit with measured Cloud planning
   cost. GitHub Actions, CircleCI and Buildkite have no planning unit to place:
   a dynamic Buildkite pipeline upload is itself an ordinary job on an agent.
2. **Does SDK `Prefers` hold inside an open repository tier?** Options: (a) it
   only breaks ties, no hold; (b) it keeps the controller's 20s
   `--placement-hold` inside each tier. Recommendation: (a). The repository
   list already states how long to wait; a hidden 20s inside a 5s tier defeats
   it. None of the three competitors has preferences, only hard selectors.
3. **Which revision supplies the policy?** Options: (a) verified default-branch
   config, sealed alongside the event revision; (b) the event's own revision.
   Recommendation: (a), so a branch or PR cannot add a paid tier or choose a
   privileged workflow; the run page shows both revisions. GitHub Actions takes
   `runs-on` from the event's own workflow file, so a PR can target any runner
   label the repository can reach; CircleCI and Buildkite also take runner
   selection from the build's own config.
4. **What happens when no tier is available for a long time?** Options: (a) the
   job waits under the existing queue policy with per-tier reasons; (b) add a
   placement deadline that fails the job. Recommendation: (a). A deadline is a
   new customer-visible limit, so it would need your call. GitHub Actions fails
   a job queued for 24 hours with no matching runner; CircleCI and Buildkite
   also queue until a matching runner or agent appears; neither falls back to
   other capacity.
5. **Does a retry after a started attempt restart the holds?** Options: (a)
   fresh ready time for the new attempt; (b) keep the first ready time, so a
   retry goes straight to later tiers. Recommendation: (a). The crashed runner
   is offline, so it does not hold anyway, and a healthy preferred runner should
   get the retry. Unstarted returned awards keep the first ready time either
   way.
6. **Ship team-only cells before GitHub fallback?** Options: (a) slices 1-4
   first, so pre-release runs macOS and Windows on your machines and waits if
   one is off, then slice 5; (b) hold the cells until slice 5 lands.
   Recommendation: (a). Pre-release is operator-run, a visible wait is
   acceptable, and it proves the store engine on real machines before the
   largest slice.
7. **How does the App get `actions: write`?** Options: (a) add it to the
   existing App, so each installation must approve the updated permissions and
   the tier stays unavailable with an approval link until then; (b) a second
   App only for dispatch. Recommendation: (a). One App is simpler, existing
   installations keep working without approval, and only teams that list the
   tier are prompted. Source tokens stay Contents read.
8. **Automatic redispatch after an uncertain dispatch?** Options: (a) never
   automatic; show it and offer an audited operator retry on a new generation;
   (b) redispatch after a fixed wait. Recommendation: (a). A lost `204` is rare
   and earlier tiers stay claimable, so a manual retry fits; (b) adds a timeout
   that needs your approval and can double-bill minutes. GitHub Actions,
   CircleCI and Buildkite have no equivalent, because none of them dispatches
   fallback compute on another provider.

Competitor summary: GitHub Actions routes by `runs-on` labels and runner
groups and waits for a match; ordered self-hosted-then-hosted fallback needs a
hand-written pre-job that queries runner availability. CircleCI selects a
self-hosted runner or a cloud executor statically per job through
`resource_class`. Buildkite targets agents by queue tags and waits for a
matching agent; autoscaling a queue is the usual answer rather than falling
through to another queue. None offers ordered tiers with holds, which is the
feature's reason to exist.
