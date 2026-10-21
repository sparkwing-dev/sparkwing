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

## Reserved pools

A team administrator reserves a runner for a pool and allows repositories into
that pool. A reserved runner accepts work only when the resolved demand both
explicitly requires one of its granted pools and includes it in intake's allowed
pools. Satisfying a different alternative in a selector does not replace that
pool requirement. An unreserved runner cannot claim work requiring a pool.

```go
sw.Job(plan, "deploy", &Deploy{}).Requires("pool=production")
```

A pipeline's `requires` is inherited by its jobs. Allowed pools are sealed
server facts. Neither a pipeline declaration nor a caller's label grants them.
Pool-restricted secrets check the reservation at acceptance and release. Here
execution cannot use those secrets. See [authentication](auth.md).

An access rule may name a branch with `sparkwing pools allow --branch`. The
branch is stored without `refs/heads/`, so `main` and `refs/heads/main` are the
same rule, and it matches as a Go `path.Match` glob, the matcher App
subscriptions use. A run a signed GitHub App delivery started on that branch,
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
restriction, reservation or resource bound. A preference cannot authorize work
on a runner that fails a hard filter.

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
