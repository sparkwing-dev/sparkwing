# Scheduling

Runners advertise capacity and free labels. Jobs declare requirements and
preferences. The controller grants repository and reserved-pool access. One
matcher decides eligibility for named, queued and self claims.

This page describes the rewrite contract. Native owner, runner and launcher
composition still needs acceptance evidence. The pure matcher and CLI transport
fixtures do not establish placement on a running deployment.

## Label-match semantics

Label terms use literal equality. Commas express alternatives within one term.
Separate terms must all match:

| Requires | Runner labels | Eligible |
| --- | --- | --- |
| `linux` | `linux` | yes |
| `linux` | `macos` | no |
| `linux,macos` | `macos` | yes |
| `linux` and `amd64` | `linux` | no |
| `linux,macos` and `amd64` | `macos` and `amd64` | yes |

An empty requirement list adds no label filter. Repository restrictions, granted
reservations, trusted-placement facts and resource shape still apply.

Name, class, location and reserved pools come from granted facts. A free label
cannot impersonate them. A `pool=NAME` term matches a granted reservation, rather
than a label the runner chose. Platform observations supply OS and architecture.
Free labels, including discovered `tool:NAME` labels, steer placement without
granting authority.

The `sparkwing-` label prefix belongs to Sparkwing Cloud. Self-hosted, personal
and pool runners cannot configure it, and claims strip such self-reported
labels. Use another prefix for your own runner sizes. Requiring a Cloud size
label such as `sparkwing-16cpu` selects a fresh, isolated Cloud pod. Intake checks
its funding as Cloud-only placement. A comma alternative such as
`sparkwing-16cpu,gpu` still permits a self-hosted runner advertising `gpu`.

## Reserved pools

A team administrator reserves a runner for a pool and allows repositories into
that pool. A reserved runner accepts work only when the resolved demand both
explicitly requires one of its granted pools and an access rule allows the run
into it. Satisfying a different alternative in a selector does not replace that
pool requirement. An unreserved runner cannot claim work requiring a pool.

```go
sw.Job(plan, "deploy", &Deploy{}).Requires("pool=production")
```

A pipeline's `requires` is inherited by its jobs. Allowed pools come only from
access rules, read against the run's sealed identity each time the run is
placed, releases a reserved secret or mints an OIDC token, so removing a rule
cuts off runs already admitted, retries included. Neither a pipeline declaration
nor a caller's label grants them.
Pool-restricted secrets check the reservation at acceptance and release. Here
execution cannot use those secrets. See [authentication](auth.md).

An access rule may name a branch with `sparkwing pools allow --branch`. The
branch is stored without `refs/heads/`, so `main` and `refs/heads/main` are the
same rule, and it matches as a Go `path.Match` glob limited to the `*` and `?`
wildcards; `[` classes and `\` escapes are refused. A run a signed GitHub App delivery started on that branch,
a child or retry that inherits that run's identity, and a fire of an
admin-armed default-branch schedule (see [schedules](schedules.md#pause-edit-and-run))
when the rule names the default branch match a branch rule. Pools are granted at
all only to those runs; a rule with source `github` also requires a run a
signed App delivery started. A typed repository grants nothing, so CLI, API,
GitHub Actions runs and schedules a non-admin armed, or that are locked,
overridden or off the default branch, never receive a reserved pool.

## Job modifiers

```go
sw.Job(plan, "train", &Train{}).Requires("gpu")
sw.Job(plan, "package", &Package{}).Requires("os=linux", "arch=arm64")
sw.Job(plan, "apply", &Apply{}).NeedsTools("terraform")
sw.Job(plan, "build", &Build{}).Prefers("arch=arm64")

preflight := sw.Job(plan, "check-sso", &CheckSSO{}).Inline()
sw.Job(plan, "deploy", &Deploy{}).Needs(preflight)
```

`Requires` filters eligibility. `NeedsTools` adds `tool:NAME` requirements.
`Prefers` ranks eligible candidates and never relaxes a requirement, repository
restriction, reservation or placement policy. A preference cannot authorize work
on a runner that fails a hard filter.
An award matching a Cloud size preference records `placement_reason=preference`.

`Inline` keeps a job with its run host. Use it for a local preflight or other work
that must run with the owner. A `local` or `location=coordinator` requirement is
not an alias for Inline placement.

### Pipeline-level requirements

```yaml
pipelines:
  - name: deploy-prod
    entrypoint: DeployProd
    requires: [pool=production]
```

Pipeline YAML replaces the defaults layer's requirements when explicitly set.
An explicit empty list clears that layer. The accepted resolved settings carry
the winning requirements to claims and retries. Executors do not reread their
own project or machine preferences to change them.

### Repository placement

A pipeline entry can order where its jobs run for GitHub App push, pull
request and release deliveries, App-armed schedules (read when each fires) and
Run now on a commit the controller fetched, including runs whose verification
was unavailable. Each tier names a target and an optional hold:

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
```

Targets are `team` (shared team runners, optionally narrowed by `labels`),
`name=<runner>`, `cloud` and `github-actions` (last only, with an approved
`workflow` and hosted `runner` image). A hold defaults to `0s`. Tier N opens
when every earlier tier with a live runner that has a free slot has passed
its deadline, which is the job's ready time plus the holds through that tier.
An offline, busy, stale or mismatched runner never holds a later tier. Holds
run from the time the job first became ready since its last started attempt:
a returned award that never started keeps that time, and a retry after a
started attempt starts a fresh one.

The list is the whole allowed set: a target that is not listed never runs the
job, so a list without `cloud` never spends Cloud credits on it. The list binds
job IDs, not code: a job renamed to `macos` takes the `macos` override, and in a
pipeline with only `jobs` overrides every other job keeps today's routing. A
child run stays inside the list of the job that started it; the child
pipeline's own default-branch list can only narrow it, and a child whose list
shares no tier with its parent's is refused. Requirements
from the SDK and the pipeline's `requires` still apply in every tier, and tier
labels narrow only their own tier. A job block replaces the pipeline list; an
empty `prefer: []` gives that job the routing it would have without a list.
With a list, the controller's preference hold and the team ranking's
five-second window do not apply; the ranking still filters which claimant
classes may take the job. SDK `Prefers` adds no hold inside a tier.

Only the copy of `.sparkwing/sparkwing.yaml` on the default branch's head
applies, sealed with that commit next to the run's own revision, so a branch
or pull request cannot add a paid tier. An override for a job a plan does
not contain is ignored. Plan and expansion acceptance refuse a job whose
override names it while it is inline, and a job whose operating-system
requirement no tier can meet, such as `os=darwin` with only `cloud` listed. Listing
`cloud` or `github-actions` under a `run.route: local` pipeline is an error,
and a GitHub App runner setting other than `anywhere`, an explicit `--runner`
or burst routing refuses the run with both settings named. When the
default-branch file cannot be read or parsed, that pipeline's run is refused
with the reason, and the rest of the delivery proceeds. `sparkwing pipeline lint` prints the resolved list for
every declared job. `sparkwing runs status` and the run page show each held
job's tiers, the runners live in each, and when the next tier opens. A job
with no available tier waits under the existing queue policy. On-demand
GitHub Actions dispatch is not available yet; a `github-actions` tier takes
only an already running Actions runner.

## How runners advertise labels

Runner enrollment accepts repeatable labels and an owner repository allow-list:

```bash
sparkwing runners add --runner build-box --profile prod --label gpu --repos 'github.com/acme/*'
sparkwing runners add --runner deploy-box --profile prod --repos 'github.com/acme/*' --reserved-for prod
sparkwing runners list --profile prod -o json
```

The controller returns the granted runner identity, class, location and
reservations separately from free labels and measured headroom. An absent
headroom sample is unavailable data. It is not zero capacity.
`--reserved-for` submits a pool proposal under team administration. Repeating
the flag or separating pools with commas preserves every requested pool. The
controller grants the reservation through enrollment. Enrollment never retries
a response that could have contained the one-time credential.

The unified runner uses one ordered claim loop for trigger and job units.
The launcher claims plan, eval and work units and creates their Kubernetes Jobs.
There is no warm-pool dispatcher, five-second fallback race or separate laptop
worker loop. All claim paths apply the same matcher before awarding authority.

## Waiting jobs

Runners ask for work when they can execute it. A job requires every selector
term to match the runner's labels. Within a term, comma-separated alternatives
are ORed. Claims order eligible jobs by the team's last award, then arrival and
identity. Preference holds can delay a later placement without widening eligibility. Queue observations retain
holders, ordered waiters, pressure, estimates and missing measurements:

```bash
sparkwing queue list --profile prod -o json
sparkwing queue list --profile prod --group deploy -o json
sparkwing runs status --run RUN_ID --profile prod
```

A waiting job's notice lists its required runner labels. For example, a job
requiring `gpu-x` needs an online runner with that label. Ask a team
administrator to enroll one for the repository:

```bash
sparkwing runners add --runner gpu-box --profile prod --label gpu-x --repos github.com/acme/repo --session-stdin
```

Provide an independent account session on stdin. Start an existing matching
runner if one is already enrolled.

A local `sparkwing run` executes its jobs only on the machine that started it.
List a job's custom labels under `runner.labels` in that machine's
[configuration](machine-config.md#runner-labels) and run
`sparkwing daemon restart --force` so its local runs report them.

New metered claims account for billable time that begins during issuance when
computing the funded lease bound.

Queue wait defaults to 24 hours and also ends at a finite owner deadline.
Execution and approval wait are separate budgets. A local run has no default
execution deadline. Operator ceilings refuse excessive explicit values, naming
both the setting and source. See [limits](limits.md).

## Recovery and retries

Application retries run under the current holder's claim. The attempt ordinal
increases for each execution and spends the job's declared retry budget. There
is no automatic retry mode or replacement run for an application retry.

A lease lapse releases the unit to its owner and clears its claim. Before
execution starts, the lapse consumes no disruption allowance. After start, it
increments disruptions. The cap is three, separate from application retries.
The same run continues. The owner decides whether released work becomes ready
again. Concurrency holders and controller ownership retain their store contracts.

Deleting a finished run created by someone else requires `team.admin`.
Ask a team administrator to delete it. The run stays when deletion is refused.

Cancellation of held work records a request. The holder keeps the authority
needed to drain and report its result. Clients branch on canonical error codes
rather than treating every `409` as cancellation or lease loss.

## Direct runs and triggers

`sparkwing run` hosts its run in the CLI. Runner-hosted runs have a run-host
owner. Launcher placement has a controller owner and separate plan/eval/work
executors. On Sparkwing Cloud, intake selects launcher placement for every
executable run that names no runner, label, `here` or routed pool; any of those
selects a runner-hosted or here owner. The local controller keeps the
submitted dispatch. Owners decide graph readiness. Executors only execute their awards.
Inline work stays with its run host. Other ready work is self-claimed or awarded
to a matching runner or launcher.

The server-granted runner identity and verified selected engine reach each SDK
process in hello. Ambient environment values and runner image labels cannot
select either identity. Resume and retry consume persisted request and plan
seals. See [local execution](local-execution.md).

## Schedule triggers

Schedules declare `where: local` or `where: controller`. Their runs use the same
intake, matcher and lifecycle. Scheduled work infers batch admission only when
no explicit class wins. Machine environment is captured at installation, rather
than replaced with the timer or daemon's ambient environment.
See [schedules](schedules.md) for arming and management.
