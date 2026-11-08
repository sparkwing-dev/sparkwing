# Placement preferences for repository runs

Status: slices 1-5 implemented; slice 6 (docs and live acceptance) remains. Date: 2026-11-07.

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
automation pins `runner: github-actions` or a pool. Explicit repository tiers dispatch on demand through approved workflows and
GitHub-hosted images. The App requires installation approval of `actions: write`;
source tokens retain Contents read only.

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
| SDK `Prefers` | Orders claimants inside an open tier (decided question 2). |
| Controller `--placement-hold`, `--default-prefer-labels` | Not applied to that job. |
| Team claim ranking | Still filters which claimant classes are allowed; adds no hold. A listed tier the ranking excludes is a lint error. |
| Automation `runner: github-actions` or `runner: pool=` | Conflict. Lint names both settings; neither silently wins. |
| Pipeline `run.route: local` | Conflict with any `cloud` or `github-actions` tier. Lint error. |
| `sparkwing run`, `--sw-route cloud`, machine burst | Unchanged. Asserted runs never read repository placement. |

Which runs read it: App push, pull request and release deliveries, App-armed
schedules, and Run now on a commit Sparkwing fetched, whether or not intake
could verify them; the list only narrows placement, so an unverified run seals
it too. The policy comes from the verified default-branch config, sealed with
its revision next to the event's execution revision, so a pull request cannot
add a paid tier or pick a workflow (decided question 3). An override for a job
the plan does not contain is inert. `sparkwing pipeline lint` prints the
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
(decided question 5).

**Cancellation.** Cancelling the run or job removes it from ready work,
invalidates its dispatch intent and cancels the known GitHub run through the
same API permission. A late workflow runner finds nothing to claim and exits
idle. A placement hold never preempts an awarded job.

**No tier available.** The job stays queued under today's queue policy with a
reason per tier. No new timeout or cap is added (decided question 4). A tier that
definitely cannot serve (missing workflow, Actions disabled, App permission not
approved, no Cloud credits) is marked unavailable with its repair, and the
remaining tiers keep working. A config that can never place, such as
`Requires("os=darwin")` with only a Linux image listed, fails at validation.

**Cloud-hosted controllers.** Today Cloud intake sends every unpinned App run
to the launcher. With an explicit list, job units follow the list instead.
Planning and evaluation keep today's placement (decided question 1).

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
   visibly (decided question 6).
5. **On-demand GitHub Actions (L).** App `actions: write` and approval flow,
   owner-approved workflow and images, dispatch intents and sender lease,
   receipt-bound OIDC exchange for tag and PR refs, cancellation, fallback
   workflow with no event triggers. Explicit Actions tiers accept only the
   receipt's job through the approved workflow revision and hosted image.
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

## Implementation notes (slices 1-4)

- The only Cloud-only job path on Cloud-hosted controllers is the GitHub App
  runner setting `cloud`, a hard `class=cloud` requirement. Any runner setting
  other than `anywhere` therefore conflicts with an explicit list and refuses
  the run with both settings named, rather than letting the setting win.
- Funding needs no new check: an unfunded Cloud claimant is ineligible in the
  claim predicate, so it never holds a later tier and never wins.
- The list only narrows, so intake seals it whatever the run's provenance,
  including App runs whose compare was unavailable. Any runner pin (App runner
  setting, `--runner`, burst routing) conflicts and refuses the run.
- A child run takes the resolved list of the job that started it, unchanged,
  and never reads its own pipeline's list; a child of a job without a list has
  none. Lists are a cost and preference policy, not a confinement boundary, so
  a child of an unbound job, or a child whose pipeline lives in a repository
  with its own list, is not confined by any list. A child's runner selection
  conflicts with an inherited list.
- App-armed schedules read the default-branch list each time they fire, in
  the controller's schedule source resolution.
- An override for a job a plan lacks is inert: the list binds job IDs, and a
  pipeline flag can leave a declared job out. Plan and expansion acceptance
  share one per-job check that refuses an override on an inline job and a job
  whose operating-system requirement no listed tier can meet.
- Holds run from `queue_started_at`, the time a job first became ready since
  its last started attempt, so an award returned before it starts cannot renew
  them. Ranking still orders by the latest ready time.
- A listed job keeps the team ranking's claimant filter even when its
  requirements pin a class, pool or name; unlisted pinned jobs still skip it.
- A configuration reader older than these keys refuses a file that uses them.
  Pre-launch, readers get no tolerance for unknown keys (Korey, 2026-11-07):
  upgrade the controller and CLIs before adding the keys. The repository's own
  pre-release YAML moves to a separate configuration change after the installed
  CLI is rebuilt.
- An explicit `github-actions` tier requires a dispatch receipt. Approval binds
  the workflow path, ref and image. Registration checks signed OIDC and provider
  run/job metadata; the receipt credential has only node-claim authority for its
  job. The fallback workflow runs on its approved ref and preserves the workload's
  original source and trust. Sender leases fence concurrent replicas and a lost
  outcome remains uncertain until an audited operator retry.
- `pipeline lint` prints the preview in table and plain output; JSON output
  keeps findings only.

## Decided questions

Korey decided all eight on 2026-11-07 as recommended; the options and
competitor comparisons are kept in the
[proposal](proposals/repository-run-preferences.md).

1. **Planning and evaluation placement is unchanged.** Cloud launcher on
   Cloud-hosted controllers, the controller owner when self-hosted. Revisit
   with measured Cloud planning cost.
2. **SDK `Prefers` only breaks ties inside an open repository tier.** It adds
   no hold; the controller's 20s `--placement-hold` does not apply to a job
   with a repository list.
3. **Policy comes from the verified default-branch config**, sealed with its
   revision next to the event's execution revision.
4. **No placement deadline.** A job with no available tier waits under the
   existing queue policy and shows a reason per tier.
5. **A retry after a started attempt gets a fresh ready time.** A returned
   award that never started keeps the original one.
6. **Team-runner cells ship first.** Slices 1-4 land before slice 5; a cell
   whose machine is off waits visibly.
7. **The existing App gains `actions: write`** (slice 5); each installation
   approves it, and the tier shows an approval link until then.
8. **Uncertain dispatches are never redispatched automatically** (slice 5); an
   audited operator retry opens a new generation.

The design agent's calls are ratified too: OS requirements stay in SDK and
pipeline `requires`; `here`, `machines` and `pool=` stay burst-only; Cloud
funding is checked when the Cloud tier opens; this note wins over the proposal.

Competitor summary: GitHub Actions routes by `runs-on` labels and runner
groups and waits for a match; ordered self-hosted-then-hosted fallback needs a
hand-written pre-job that queries runner availability. CircleCI selects a
self-hosted runner or a cloud executor statically per job through
`resource_class`. Buildkite targets agents by queue tags and waits for a
matching agent; autoscaling a queue is the usual answer rather than falling
through to another queue. None offers ordered tiers with holds, which is the
feature's reason to exist.
