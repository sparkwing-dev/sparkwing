# Placement preferences for repository runs

Decision date: 2026-11-05. Status: design for implementation.

Runs started by pushes, pull requests, releases and schedules can prefer team
runners before using paid or hosted fallback capacity. These are
repository-proven runs. They use the same ordered `prefer` and `hold` model as
personal burst runs. The controller owns placement on both self-hosted and
Cloud-hosted controllers.

The first use is the pre-release macOS and Windows cells. Each cell prefers
team runners with `os=darwin` or `os=windows`. GitHub-hosted runners provide the
last tier. When a team runner takes the cell, no fallback workflow starts and
that cell spends no GitHub Actions minutes.

Implementation starts after step 3, **Local jobs through the runner path**, in
[local controller restart](local-controller-restart.md#implementation-order).
That step puts local execution behind the common runner contract. This design
extends that contract instead of adding another execution path.

## Existing contracts

[Burst and overflow](../../../docs/local-execution.md#burst-and-overflow) and
[CLI run](../../../docs/cli-run.md) describe personal asserted routing. A
machine file authorizes that routing. Its sealed preference set contains the
requester's machines and optional Cloud capacity. Team runners are excluded.
Repository preferences add a separate policy for repository-proven work.
Personal machines remain ineligible for automated repository work.

The store already owns claim matching and holds. The relevant sources are:

- `internal/runspec/burst.go` defines burst targets and hold validation.
- `internal/store/burst_placement.go` seals the eligible burst set and checks
  personal ownership.
- `internal/store/claim_query.go` owns eligibility, preference ranks,
  `placementCTEs`, `firstPreferenceHold` and `rankingHold`.
- `internal/store/claim_ranking.go` owns team ranking and pinned selectors.
- `internal/store/burst_placement_test.go` and `claim_ranking_test.go` guard
  holds, absent capacity and relinquishment.

[Admission](../ADMISSION.md) keeps placement, logical concurrency and machine
capacity separate. A placement wait holds no compute. A provisional award that
cannot obtain compute relinquishes its permit and job reservations. Its
original readiness window survives.

Go already has `JobNode.Requires` and `JobNode.Prefers` in `sparkwing/plan.go`.
The SDK declares selectors. The host interprets policy. Follow the
[thin SDK rule](thin-sdk.md): this feature needs host configuration and runner
protocol support, without a new SDK API or a repository SDK pin bump.

## Repository declarations

Declare placement in `.sparkwing/sparkwing.yaml`. Add `placement.prefer` to a
pipeline entry. Add `jobs` keyed by accepted job ID for job overrides. These
keys are proposed syntax, not an implemented configuration surface.

```yaml
pipelines:
  - name: pre-release
    entrypoint: PreRelease
    placement:
      prefer:
        - target: team
          hold: 5s
        - target: cloud
          hold: 10s
        - target: github-actions
          workflow: .github/workflows/sparkwing-fallback.yaml
          runner: ubuntu-latest
    jobs:
      macos:
        requires: [os=darwin]
        placement:
          prefer:
            - target: team
              labels: [os=darwin]
              hold: 5s
            - target: github-actions
              workflow: .github/workflows/sparkwing-fallback.yaml
              runner: macos-latest
      windows:
        requires: [os=windows]
        placement:
          prefer:
            - target: team
              labels: [os=windows]
              hold: 5s
            - target: github-actions
              workflow: .github/workflows/sparkwing-fallback.yaml
              runner: windows-latest
```

The durations are examples, not defaults. The job IDs are illustrative. The
implementation must use the accepted pre-release cell IDs. Each cell must be
a dispatched job. Inline work stays on its owner's host and cannot move tiers.
Lint must explain that conflict when an inline job receives a placement override.

Supported targets are `team`, `name=<runner-name>`, `cloud` and `github-actions`.
`name=` selects an enrolled shared team runner, as burst already selects named
personal machines. A team or named tier can add `labels`. Terms use existing
selector syntax. Terms combine with AND. Comma-separated alternatives within a
term combine with OR. A claimant matching several tiers takes its earliest rank.
Provider identity comes from authenticated capability facts, never an advertised
`class` label. `runner` selects an approved GitHub-hosted image in the workflow.
It does not grant authority or let repository input supply arbitrary `runs-on`.

A job block replaces the pipeline's whole preference list. It never appends an
unexpected paid fallback. Omitted job placement inherits the pipeline list.
An explicit empty list clears inheritance and restores existing placement.
Omitted pipeline placement preserves existing routing and ranking. This change
adds no repository-wide placement default.

For explicit lists, the listed tiers are the entire allowed placement set.
Expiry changes priority, not membership. Cloud requires a `cloud` entry.
GitHub Actions is the tail tier. A list can omit either provider. Existing hard
runner or pool selections still constrain it. A legacy automation setting that
pins `github-actions` must be changed before that pipeline can prefer team
runners. Report the conflicting settings rather than silently overriding one.

Required labels from accepted run settings, the SDK job and the repository job
block all remain hard constraints. A preference cannot relax them. For example,
`Requires("os=darwin")` rules out Linux in every tier. Adding `name=build-mac`
as a requirement pins the job and prevents a GitHub fallback. Put that name in
a preference to allow fallback. Tier labels narrow that tier only. They do not
become requirements for later tiers.

An explicit repository list supplies tier order and holds. Existing SDK
`Prefers` breaks ties among eligible claimants within an open tier. With no
repository list, existing SDK preference behavior stays intact. Team policy
continues to restrict eligible classes and pools. Its default ranking must not
add a second hold that reverses an explicit repository order.

## Policy source and sealing

Read repository placement from the verified default-branch configuration used
for repository automation consent. Apply that policy to subscribed pipelines
and App-armed schedules. Seal its revision and digest alongside the event's
execution revision. A feature branch or PR head cannot add paid fallback or
choose a privileged workflow. Release code still executes at the sealed tag
commit. A scheduled run still executes at its resolved commit.

The controller resolves pipeline policy at intake and job overrides when the
plan is accepted. Generated jobs use the same resolver and exact job IDs.
Unknown static IDs produce a located error at plan acceptance. Generated IDs
are checked when expansion closes. Lint should preview resolved policy for
every known job. Retries keep the sealed policy and get fresh attempt fences.
Child pipelines resolve their own repository and pipeline policy. They cannot
inherit a parent's repository dispatch authority.

An event name alone does not prove a repository. Intake must seal repository
proof before enabling this policy. A proof failure remains visible and uses
the existing asserted-work rules. It cannot gain team runner, source, secret
or dispatch authority through these declarations. Local schedules without
repository proof follow the same rule.

## Holds and claim selection

Resolve policy once in the controller store. Reuse the common claim predicate
for queue polls, named claims, owner execution and provider launch requests.
Keep team fair share and durable arrival order across jobs. Within a job,
apply hard eligibility first, then its open tiers and preference rank.

Use the burst readiness clock. A tier's nominal deadline is the job's ready
time plus holds through that tier. Deadlines use the store clock. Polling,
restart and an unstarted award being returned do not restart them. Later tiers
open when earlier applicable holds expire. Earlier tiers stay eligible after
expiry until another claimant owns the job. A placement preference never
preempts a live execution award.

A hold blocks later capacity only while an earlier matching claimant is live
and admissible under the shared predicate. An enrolled but offline runner is
absent. A stale observation, wrong repository scope, unsatisfied label, revoked
credential or missing job slot cannot hold the queue. Reuse the existing runner
observation lifetime. A saturated runner withdraws its job offer. Its planning
offer does not count as job capacity. An absent tier cannot delay dispatch
just because its configured hold is positive.

For a team tier followed by Actions, a live eligible team offer gets its hold.
If the offer remains admissible but never wins, expiry opens Actions. If no
eligible team offer exists, Actions can open immediately. Returning a provisional
award because machine capacity is busy keeps the original ready time. It cannot
renew the hold forever.

Cloud launch demand must pass the same placement check before reserving paid
capacity. The controller uses authenticated launcher capacity facts, rather
than requiring a running Cloud executor as evidence. An on-demand Actions tier
needs no idle workflow as a claimant. It becomes launchable when the shared
placement decision opens the tail. This distinction prevents a circular wait.

The store atomically awards one unit and advances its generation. Reporting,
source, secrets, permits and settlement require that live award. Persist the
resolved policy with ready work so every controller replica sees one decision.
Extend `placementCTEs` and the sealed claimant-set checks. Do not add independent
ranking in the console, runner, Cloud launcher or GitHub workflow.

Cloud-hosted controllers must allow this mixed job placement instead of sealing
all App work to Cloud execution. Keep the existing run scheduler and verdict
owner independent of each job's executor. Planning and evaluation units retain
their own placement and billing. A macOS cell must not force a Linux planning
owner onto macOS or require Actions just to discover the plan.

## On-demand GitHub Actions

Use `workflow_dispatch` to start the repository's approved fallback workflow.
That workflow starts `sparkwing-runner warm --github-actions`, using the existing
OIDC exchange and runner hosting path. The command is implemented through
`internal/cluster/runner_cli.go` and `production_runtime.go`. The existing
workflow and registration contract is in
[GitHub Actions runners](../../../docs/github-actions-runners.md).

The fallback workflow has no push, PR, release or schedule trigger. The App
creates the Sparkwing run. The controller starts a workflow only for ready work
whose earlier holds no longer block. Split fallback cells from any eager
pre-release matrix so an unconditional matrix does not spend their minutes.
An existing matching Actions runner can take open work through the same claim
path. It must not cause the controller to dispatch another workflow for that
work. External workflows that run eagerly incur their own GitHub costs.

The team owner approves the numeric repository ID, installation, workflow path,
workflow ref and allowed hosted images. The default branch must contain the
dispatchable workflow. Dispatch uses that approved branch or tag. The controller
records its resolved workflow revision. GitHub's API requires `actions: write`
on the repository. Mint an installation token for that repository ID only.
Keep it in the controller. The existing App requests Contents, Checks, Metadata
and Pull requests permissions. Adding Actions write needs installation approval.
Source tokens remain restricted to Contents read. See
[GitHub App](../../../docs/github-app.md) and
[GitHub dispatch authorization](https://docs.github.com/en/rest/actions/workflows#create-a-workflow-dispatch-event).

Recheck installation ownership, repository coverage, automation consent and
workflow approval before dispatch. Uninstall, suspension, repository transfer
and withdrawn consent fence pending dispatches and later exchanges. The workflow
gets `id-token: write` and `contents: read`. It receives no App token or reusable
team credential. Workflow inputs contain a dispatch receipt ID and approved
image selection. Inputs contain no secrets or bearer credentials.

The existing exchange accepts branch refs and binds repository, ref and SHA to
the workflow's identity. That is insufficient for release tags, PR refs and a
trusted workflow running separately from the workload revision. Add a receipt
binding at the host boundary. Verify GitHub OIDC issuer, audience, numeric
repository and owner IDs, workflow identity, workflow run ID and attempt.
Match the workflow revision to the intent. Associate the provider run with the
recorded App dispatch through the API response or verified provider metadata.
Then resolve the receipt in the controller. An input naming a run is not proof.
Keep the existing once-per-workflow-attempt exchange rule.

The receipt authorizes only its original run, job and placement generation.
The execution award retains the original repository, event, ref, SHA and trust.
A default-branch dispatcher does not upgrade PR code to default-branch trust.
Use the existing source and secret checks for the awarded job. The new receipt
path does not broaden ordinary Actions push credentials to arbitrary refs.
Keep the existing fork PR refusal.

### One dispatch decision

Commit a durable dispatch intent under the same job lock as claim selection.
Key it by team, numeric repository ID, run, job, source generation and placement
generation. Store the workflow, image, policy digest, receipt ID and provider
status. A second replica or wake reads that intent instead of sending a second
request. An execution award that wins first prevents intent creation.

The intent reserves delivery to Actions, not compute or logical concurrency.
Once committed for sending, it fences competing job claims until delivery is
resolved or explicitly abandoned. Send outside the database transaction under
one leased sender. Recheck cancellation and the generation before sending.
Record the provider run ID when returned, and adopt it through authenticated
workflow registration when needed. Provider events and registration wake the
same controller state machine. The workflow cannot create another Sparkwing
trigger. This also prevents a dispatch webhook loop.

Do not claim exactly-once HTTP delivery. Persist a sending state before the HTTP
request. A lost response or sender crash can mean GitHub accepted the request.
An expired sender lease makes that state uncertain. Reconcile by receipt and
provider run identity before any resend. A controller restart cannot blindly
resend it.
Duplicate workflow invocations must compete for one receipt binding. Only one
can obtain the job award. Workflow concurrency may reduce wasted starts, but
the controller fence owns execution deduplication. An upstream duplicate may
still incur GitHub startup minutes.

Cancellation invalidates the receipt and cancels a known provider run. Late
registration cannot obtain work. Before releasing a failed dispatch back to
other tiers, invalidate its generation so a delayed workflow cannot also
execute. Retry delivery after a definite refusal or an operator decision uses
a new dispatch generation. A lost claim or exchange response keeps the existing
one-time-response rules. Receipt reconciliation must not replay bearer issuance.

## Console and costs

Show the resolved tier list on each job, its policy revision and required labels.
Show the active tier, hold deadline, remaining time and the reason later tiers
are waiting. Distinguish an offline runner from a busy runner, a label mismatch,
a funding refusal and a provider queue. Example messages are:

- `Waiting for team runners with os=darwin. GitHub Actions opens in 3s.`
- `No live matching team offer. Requesting GitHub Actions macos-latest.`
- `GitHub Actions accepted the workflow. Waiting for its runner.`
- `Dispatch outcome unknown. Reconciling before another request.`

Link a dispatched provider run and show the awarded runner after claim. Show
failed tiers with a repair action. Keep these explanations in the controller's
run detail projection so the CLI and console agree. The browser does not
compute eligibility from its own clock.

Cloud hosting does not make team or Actions execution Cloud compute. Preserve
the existing immutable billing award's payer, executor class, size and start
rule. Placement waiting and dispatch intents create no Cloud compute award.
Cloud execution still uses credit reservation, renewal and settlement. Release
an unstarted Cloud reservation once when its award is relinquished or fails.
Planning or evaluation that actually runs on Cloud keeps its existing charges,
even when every work job uses a team runner. Do not describe such a run as free.

GitHub bills workflow execution against the repository's GitHub account and
plan. Sparkwing must show that fallback can spend Actions minutes. Existing
team runner charges and controller charges keep their current contracts. This
proposal adds no surcharge or personal overflow budget to repository runs.
Funding checks must consider every authorized execution path. Zero Cloud
credits must not reject a run that existing admission can serve entirely on
unmetered capacity. A Cloud fallback without funds reports why it cannot win.
An eligible Actions tail remains available.

## Failures and recovery

| Failure | Controller behavior and remedy |
| --- | --- |
| Dispatch API rejects the request | Persist the masked reason. Release the delivery reservation after fencing it. Keep authorized team or Cloud work claimable. Retry a transient refusal only when it is known not to have been accepted. |
| Workflow file or dispatch trigger is missing | Mark that tier unavailable. Name the expected path and `workflow_dispatch`. Ask the repository owner to commit it. Do not silently create or edit a workflow. |
| Actions or the workflow is disabled | Show the provider refusal and repository Actions settings. Keep other eligible tiers usable. Do not silently enable Actions. |
| App lacks Actions write or consent was revoked | Refuse dispatch and explain the approval or reconnection needed. Already admitted work on other authorized runners can continue under existing authority rules. |
| GitHub accepts but no runner starts | Show provider queue state and a link. Reconcile workflow completion and cancellation. Use the existing job queue deadline. Do not start duplicate workflows on each poll. |
| Dispatch outcome is uncertain | Preserve the intent across restart. Reconcile before retry. Offer an audited operator retry that first fences the old receipt and warns about possible extra GitHub minutes. |
| Hosted image cannot satisfy required labels | Keep the requirement. Show the image and missing labels. A workflow cannot relabel itself to bypass the match. |
| Every tier is unavailable | Keep the job visibly queued under its existing queue policy, with reasons for each tier. A provable configuration error fails validation. No hidden new queue timeout applies. |
| Runner disconnects after award | Use existing lease expiry, cleanup, retry and generation fencing. Expiring a preference hold does not move an executing job. |

## Defaults and approval

No new customer-visible default is approved by this proposal. Preserve existing
behavior when repository placement is absent. Preserve existing queue deadlines,
runner liveness, concurrency and credential limits. Add no tier-count cap,
dispatch quota, monthly budget or separate fallback timeout.

**Pending maintainer approval:** recommend an omitted hold of `0s` for this new
repository surface. Require examples and setup previews to show explicit holds.
Do not silently inherit the personal burst or team ranking five-second default.
Also pending maintainer approval is using the example `5s` hold in the generated
pre-release setup. It trades a short wait for a chance to avoid hosted minutes.

GitHub Actions selects a runner with matching `runs-on` labels and groups. It
requeues an uncollected assignment after 60 seconds and fails a job queued for
more than 24 hours. Its documented routing waits for a matching runner rather
than specifying an ordered self-hosted-to-hosted fallback. See
[GitHub runner routing](https://docs.github.com/en/actions/reference/runners/self-hosted-runners#routing-precedence-for-self-hosted-runners).
CircleCI selects a self-hosted pool through a job's `resource_class`. Its
documented selector does not specify this ordered cross-provider hold model.
See [CircleCI runner concepts](https://circleci.com/docs/runner/runner-concepts/).
These are comparison points, not proposed Sparkwing defaults.

Any implementation proposal for a new retry cap, dispatch-start deadline,
hold ceiling or queue limit is **pending maintainer approval**. It must state
the customer impact and compare GitHub Actions and CircleCI for that limit.
Operational backoff may pace retries. It cannot silently end a job or disable
a configured execution path.

## Tests and delivery evidence

Use controlled clocks and real store transactions on SQLite and PostgreSQL.
Extend burst and claim-ranking fixtures so both policy surfaces exercise the
same placement engine. Cover these contracts:

- Parse pipeline and job lists. Check replacement, clearing, names, label
  alternatives, invalid durations, duration overflow and the Actions tail.
  Keep old configuration and SDK pins working. Test exact generated job IDs.
- Seal default-branch policy independently of event SHA. Test pushes, same-repo
  PRs, releases and schedules. A PR policy edit cannot authorize dispatch.
  Proof failure, personal machines and fork PRs retain their existing boundaries.
- Race eligible team claims, Cloud launch requests and Actions dispatch intent
  on two controller replicas. Assert one winner and no dispatch when an earlier
  tier wins. Test named claims and owner execution through the same predicate.
- Advance through live, absent, stale, full and revoked claimant observations.
  Assert immediate fallback without an eligible earlier offer, fixed deadlines,
  unchanged fair share and no hold reset on restart or relinquishment.
- Check required OS, architecture, pool and trust constraints in every tier.
  Test explicit SDK runner pins and SDK preferences with and without YAML policy.
  Inline jobs and approvals must not acquire independent placement awards.
- Fake the GitHub API at the HTTP boundary. Cover acceptance, definite refusal,
  missing workflow, disabled Actions, permission loss, rate limiting, lost
  responses, cancellation and controller restart. Count outbound dispatches.
- Verify OIDC against the receipt and original award. Test PR and tag source
  revisions, wrong repository or workflow, spoofed receipt input, replayed
  exchange, duplicate workflow attempts, revoked consent and late registration.
  Share fixtures between controller exchange and runner startup.
- Assert zero Cloud compute charges during placement holds. Test Cloud award
  settlement once, actual planning charges, no-credit unmetered execution and
  Actions fallback without spending personal overflow allowance.
- Test console and CLI reasons against committed store state. Keep workflow
  generation, source docs, embedded docs and their drift checks aligned.

Before enabling the first use, run a real pre-release cell on native macOS and
Windows team runners. Then remove matching offers and prove the controller
dispatches the correct GitHub-hosted image. Repeat on a Cloud-hosted controller.
Cancel during dispatch and restart a controller between intent and response.
Retain provider run IDs and dispatch counts. Confirm that the team-runner case
starts no fallback workflow. Live provider tests belong to implementation
acceptance and require an authorized test repository and account.

## Implementation order

S means half a day. M means 1-2 days. L means 3-5 days. Sizes include focused
tests. Begin after local-controller restart step 3 is complete.

1. **Configuration and sealed policy, M.** Add pipeline and job declarations,
   located validation, inheritance and policy revision capture. Keep routing
   authorization separate from personal asserted settings. Preview effective
   job policy without executing it.
2. **Shared claim placement, L.** Extend the existing matcher and ready-work
   projection. Handle team labels, names, Cloud and the Actions tail. Remove
   Cloud-only job dispatch assumptions for explicit policies. Prove holds,
   fairness, admission and fencing on both store dialects.
3. **Authorized dispatch and receipt binding, L.** Extend the App client and
   installation permission flow. Add durable intents and reconciliation.
   Extend host OIDC exchange and runner startup for receipt-bound PR and tag
   work. Preserve one-time exchange and claim semantics.
4. **Console and billing evidence, M.** Expose waiting tiers, deadlines,
   unavailable reasons and provider links. Verify billing by actual executor
   and preserve the existing cost rules.
5. **Pre-release declarations and workflow, M.** Make macOS and Windows cells
   dispatched jobs. Add team OS preferences and approved hosted fallback
   images. Remove eager fallback cells from the existing Actions matrix.
   Exercise team-first and fallback paths on Cloud-hosted controllers.
6. **Docs and acceptance review, S.** Update source references, configuration
   help, generated workflow and drift guards. Run the relevant delivery gates
   and review the placement, consent and dispatch races before enabling it.

## Open questions

| Question | Recommendation |
| --- | --- |
| What hold applies when omitted? | Use `0s`, pending maintainer approval. Keep setup holds explicit. The provider comparison is above. |
| What should pre-release setup suggest? | Suggest `5s` for each team OS tier, pending maintainer approval. Measure dispatch avoidance and queue delay before changing it. |
| Should Actions receipt scope cover one job or a batch? | Start with one job and placement generation. It makes cost and cancellation traceable. Reuse an existing eligible runner through normal claims. Add batching only with measured need and explicit job bindings. |
| Which revision supplies policy and workflow authority? | Use verified default-branch policy and an owner-approved workflow ref. Preserve the event SHA as execution source. Show both revisions. |
| Should SDK `Prefers` override repository tier order? | Keep repository tiers outermost. Use SDK selectors within an open tier because they cannot authorize a new provider. |
| How should uncertain dispatch recover? | Reconcile the same receipt before resending. Offer an audited retry after fencing the old generation. Do not impose a new automatic deadline or retry cap without approval. |
| Should a failed Actions tail fail the whole run immediately? | Keep other listed tiers usable under the existing queue policy. Show the unavailable tail and repair action. Fail a provably invalid declaration during validation. |
| Does Cloud hosting change prices for team or Actions jobs? | Keep existing award-based charging. Show any Cloud planning charges separately. Any new controller fee or fallback surcharge needs a separate pricing decision. |

This proposal changes no runtime behavior. Its validation is source review and
design consistency. Implementation acceptance must supply the runtime evidence.
