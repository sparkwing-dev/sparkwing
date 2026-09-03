# Scheduling

How sparkwing decides which runner executes a job. The model is simple:
**runners advertise labels, jobs declare the labels they need, and the
controller hands each job to a runner whose labels satisfy it.**

## The model in one paragraph

Each controller-connected runner advertises a set of **labels** (opaque equality
strings like `arm64`, `os=linux`, `gpu`). A job declares the labels it
needs -- per node via the Go SDK (`.Requires(...)`) or for the whole
pipeline via `requires:` in `sparkwing.yaml`. The controller's claim
query matches a job to a runner when the runner's advertised labels
satisfy the job's needed labels. A node whose labels no eligible polling
runner advertises is never claimed: it waits in the queue and the controller
fails it with `queue_timeout` once the queue deadline (default 15m)
passes.

## Label-match semantics

Labels are compared as **literal equality strings** -- the matcher does
no parsing of `key=value`, so `os=linux` is one opaque token that must
appear verbatim in the runner's advertised set. Within a single term,
commas are **alternatives (OR)**; across separate terms, matches compose
with **AND**:

```
needs ["linux"]            have ["linux"]          -> match
needs ["linux"]            have ["macos"]          -> no match
needs ["linux,macos"]      have ["macos"]          -> match   (OR within a term)
needs ["linux","amd64"]    have ["linux"]          -> no match (AND across terms)
needs ["linux,macos","amd64"] have ["macos","amd64"] -> match
```

Empty / no needed labels match any runner.

## Per-node modifiers (Go SDK)

Three chainable modifiers on `*sparkwing.JobNode` (and the same names on
`*sparkwing.JobGroup`) control placement. All take the comma-OR / AND
term syntax above.

```go
// Hard filter on the claim queue: no runner advertising these labels
// means the node waits, then fails with queue_timeout.
sw.Job(plan, "train", &Train{}).Requires("gpu")
sw.Job(plan, "package", &Package{}).Requires("arch=arm64", "trusted")
sw.Job(plan, "build", &Build{}).Requires("os=linux,macos", "amd64") // (linux OR macos) AND amd64

// Placement preference recorded on the plan; the claim queue does not
// rank on it.
sw.Job(plan, "integration", &Integration{}).
    Requires("os=linux").
    Prefers("cloud-linux")

// Conditional: silently skip the node when the dispatching runner does
// not advertise the labels (downstream Needs treats it as satisfied).
preflight := sw.Job(plan, "preflight-sso", &CheckSSO{}).WhenRunner("local")
sw.Job(plan, "deploy", &Deploy{}).Needs(preflight)
```

- **`Requires`** -- hard constraint. Only a runner advertising the
  labels can claim the job. While none does, the warm pool logs a hint
  naming the missing labels and the node waits; the controller's sweep
  fails it with `queue_timeout` at the queue deadline (default 15m).
- **`Prefers`** -- runner-label preferences recorded in plan-snapshot
  metadata. Preferences do not affect runner selection: the claim queue
  is FIFO by readiness and filters only on the labels a job requires, so
  the first eligible runner to poll claims the job.
- **`WhenRunner`** -- conditional execution. A runner that advertises
  labels evaluates the terms up front and skips the node when they are
  not satisfied (downstream `Needs` treats a skip as satisfied); a runner
  that advertises no labels matches anything. On a controller-dispatched
  run the terms are folded into the node's claim labels instead, so the
  node waits for a runner advertising them.

## Pipeline-level `requires` (`sparkwing.yaml`)

A pipeline entry can require labels of **every** job it contains, on top
of each node's own `.Requires()`:

```yaml
pipelines:
  - name: deploy-prod
    entrypoint: DeployProd
    requires: [warm-runner]
```

`requires` is a flat list of label terms. When set it wholesale replaces
the project `defaults.requires`. The reserved label **`local`** pins
execution to this machine -- the same effect as the `--sw-local-only`
flag:

```yaml
pipelines:
  - name: seed-local-db
    entrypoint: SeedLocalDB
    requires: [local]
```

## How runners advertise labels

Cluster-mode runners (`sparkwing-runner`) advertise labels with the
repeatable `--label` flag:

```bash
sparkwing-runner runner --label arm64 --label os=linux --label gpu
```

The controller's claim query keeps only runners whose advertised set
satisfies a job's needed labels. When no eligible polling runner advertises the
required labels, the warm pool logs a hint once and the node waits:

```
no warm runner advertises these labels; start a runner with
--label matching or remove .Requires()
```

`sparkwing cluster worker` is a laptop-side loop that claims triggers
from a profile's controller and dispatches each one to `handle-trigger`,
a child process that compiles and runs the pipeline. It works at the
trigger layer, unlike the cluster runner (`sparkwing-runner runner`),
which claims nodes.

## Agent-first execution with Kubernetes overflow

The `warm` trigger runner still sends nodes through the legacy name-less
`sparkwing-runner agent` FIFO claim loop. That process opens no listener and
polls the controller over outbound HTTP(S). A LAN, VPN, or tailnet may provide
a direct cache path, but discovery grants no execution trust. Legacy `labels`
are self-asserted placement terms. `Prefers` does not affect claim order, and
local admission may happen after a claim.

Schema 28 adds a separate administrator-owned executor enrollment for the
assisted scheduler. Enrollment binds an exact runner or service token prefix
to trusted capabilities, a priority range, concurrency and resource ceilings,
and a display-only location. Authenticated worker heartbeats can update only
liveness and finite nonnegative headroom. Idle enrollments remain visible;
stale ones appear offline. Each enrollment also reports the exact number of
live node claims as `active_slots`; legacy inferred rows omit that field because
their slot count is unknown. Rotating an enrollment to a different credential
marks it offline until that exact credential sends a heartbeat. The
scheduling-summary and membership primitives
apply hard capability, slot, headroom, and resource filters before bounded
priority.

This release does not send enrolled executors an offer or let them claim
through the legacy route. A named or plural agent configuration therefore
reports liveness only. The nonblocking reservation and exact-node claim
primitives are the seam for later offer persistence and arbitration.
After a short internal window, an unclaimed unlabeled node is
atomically removed from the agent queue and sent to the configured Kubernetes
runner. A claim that wins that handoff owns the node, so the Kubernetes
fallback cannot execute it a second time. Labeled nodes never use this
fallback because Kubernetes Jobs do not advertise the agent labels; they wait
for a compatible agent and remain subject to the normal queue timeout.

In the runner-bundle chart, set `runner.triggerRunner.kind: warm` and
`runner.automountServiceAccountToken: true`. In the full chart, prefix both
paths with `sparkwing-runner-bundle.`. The default remains `inprocess`. Warm
mode reuses the chart's existing Kubernetes runner settings and grants only
the namespace-scoped Job lifecycle and pod-read access overflow needs.

## Direct (`sparkwing run`) vs dispatched (`trigger`)

`sparkwing run <pipeline>` executes the pipeline **on this machine**,
each job in a process this binary spawns -- there is no controller and
no claim step, so label matching against remote runners does not apply
(`.Requires()` is not enforced here -- there is no claim step to filter
on -- while `WhenRunner` evaluates against the local runner, which
advertises `local` unless the caller overrides its label set). Use
`requires: [local]` or `--sw-local-only` to force local execution
explicitly.

`sparkwing pipeline trigger <pipeline> --profile prod` hands the run to a
controller, which schedules each node onto a runner whose labels satisfy
its needed labels.

## Schedule triggers (cron)

A pipeline records its intended cadence with the `schedule` trigger in
`sparkwing.yaml`:

```yaml
pipelines:
  - name: nightly-rebuild
    entrypoint: NightlyRebuild
    on:
      schedule: "0 3 * * *"   # 03:00 daily
```

`schedule:` records the cadence the pipeline is meant to run at, and
`sparkwing pipeline explain` echoes it. sparkwing does not evaluate the
expression -- triggers are created from delivered events and explicit
calls, never from a clock -- so a pipeline whose only trigger is
`schedule:` does not fire on its own. Drive the cadence from an external
timer that calls
`sparkwing pipeline trigger <pipeline> --profile <profile>` (a systemd
timer, a Kubernetes CronJob, or whatever scheduler the platform already
runs); the run then schedules onto a runner by the same label rules as
any other dispatched run.

## Worked examples

### Run only on the warm-runner pool

```go
sw.Job(plan, "deploy", &Deploy{}).Requires("warm-runner")
```

### Prefer ARM but accept anything

```go
sw.Job(plan, "build-image", &BuildImage{}).Prefers("arch=arm64")
```

Any runner whose labels satisfy the job's `Requires` can claim it -- the
preference is visible on the plan, not applied at claim time.

### A local-only preflight before a remote build

```go
preflight := sw.Job(plan, "check-sso", &CheckSSO{}).WhenRunner("local")
sw.Job(plan, "build", &Build{}).Needs(preflight)
```

The preflight runs when you `sparkwing run` locally, because the local
runner advertises `local`. Dispatched to a controller, the
term becomes a claim-queue label rather than an up-front skip: unless a
connected runner advertises `local`, the node waits unclaimed and fails
with `queue_timeout`. Keep environment-scoped preflights on
locally-executed pipelines, or run a runner that advertises the label.
