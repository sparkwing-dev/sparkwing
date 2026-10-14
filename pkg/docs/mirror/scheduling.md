# Scheduling

How sparkwing decides which runner executes a job. The model is simple:
**runners advertise labels, jobs declare the labels they need, and the
controller hands each job to a runner whose labels satisfy it.**

## The model in one paragraph

Each job declares the labels it needs, per node through the Go SDK
(`.Requires(...)`) or for the whole pipeline through `requires:` in
`sparkwing.yaml`. Runners self-report labels when they poll. The controller
filters incompatible runners before claim ordering. A node
with no eligible runner waits, and its run reports why in `needs_attention`,
until its claim wait runs out (24 hours unless the plan sets
`ClaimWait`); it then fails as unclaimable with `queue_timeout`.

The trigger queue deadline (default 15m) covers the step before any node exists, for a trigger a
claimant released. A claim whose runner dies puts the trigger back in the queue
and leaves the run pending for the next claimant, which is what lets the first
run submitted during a runner-pool rollout wait out the rollout. The controller
fails such a run once it passes the queue deadline, and finishes its trigger in
the same sweep so no late runner executes a child for it. A run still stamping
its heartbeat survives that sweep, because the claimant that spawned the work can
die while the work runs on.

A trigger no runner has ever claimed is outside that sweep and nothing reaps it
today: it stays queued until a runner with matching labels appears, and the node
claim wait above takes over once the run has nodes.

## Label-match semantics

Labels are compared as **literal equality strings**, so `gpu` or
`tier=fast` must appear verbatim in the runner's advertised set. Within a
single term, commas are **alternatives (OR)**; across separate terms,
matches compose with **AND**:

```
needs ["linux"]            have ["linux"]          -> match
needs ["linux"]            have ["macos"]          -> no match
needs ["linux,macos"]      have ["macos"]          -> match   (OR within a term)
needs ["linux","amd64"]    have ["linux"]          -> no match (AND across terms)
needs ["linux,macos","amd64"] have ["macos","amd64"] -> match
```

Empty / no needed labels match any runner.

A few keys are answered from facts the runner cannot choose for itself.
Every claim route, and the dispatcher's own `WhenRunner` check, uses the one
matcher in `pkg/match`, so they agree:

- `name=<agent>` matches only the runner whose token was minted for that
  agent name. A runner asserting `name=...` as a label matches nothing, and a
  team holds one live runner or machine token per agent name.
- `os=` and `arch=` match the platform the runner detects and reports with
  every claim. A runner that reports none falls back to an `os=` or `arch=`
  label.
- `local`, `location=`, `class=` and `team=` labels a runner asserts are
  dropped.

A runner also reports its capacity: the smallest CPU and memory limit along
its cgroup ancestry, or the host's totals. A node whose resource request
exceeds that capacity is never handed to it. What the runner has free right
now only decides whether it holds a node back from the cloud.

## Tools

An agent looks up a fixed list of tools on its `PATH` when it starts (aws,
buildx, crane, docker, git, go, golangci-lint, helm, kubectl, node, npm,
shellcheck, terraform) and advertises each one it finds as the label
`tool:<name>`, with no configuration. Like other labels an agent sends, these
steer scheduling and grant nothing. `buildx` also counts when installed as a
docker CLI plugin.

A node names the tools it needs with `NeedsTools`, or with a `tool:<name>`
term in `Requires` or `requires:`. The matcher never hands such a node to an
agent that lacks the tool, on any claim route. Sparkwing Cloud runners have
the tools `build/runner-tools` declares for the runner image, currently git
and go, whatever tools their claims send; the image build fails when the image
lacks one it declares.

```go
sw.Job(plan, "apply", &Apply{}).NeedsTools("terraform")
```

`JobGroup.NeedsTools` applies to every member, including those a
`JobFanOutDynamic` group generates after its source finishes.

## Waiting nodes and `needs_attention`

A node no agent can claim does not fail fast. Once it has waited 30 seconds
since it first became ready, the controller checks every agent registered to
the node's team, online or offline, plus Sparkwing Cloud on a metered
controller. Agents are judged as their claims are, on the selector, the run's
repository and the node's resource request: each agent on what its latest
claim sent, and online while it polls. When no online agent and no Cloud
runner could run the node, the run's `needs_attention` field (on the run and
in the run list) says why:

- `node apply needs tool:terraform; no agent in team acme has it and Sparkwing
  Cloud runners don't provide it` when nothing registered satisfies the
  selector; it names only the terms nothing satisfies.
- `node apply needs tool:terraform; eligible agent pi is offline (last seen
  2026-09-28T10:00:00Z)` when an agent that could run it is offline, matched
  on the labels and platform it advertised on its last claim.
- `node apply needs tool:terraform; every agent in team acme that matches
  refuses it: pi (shape)` when the matching agents refuse the node's
  repository (`repo`) or cannot hold its request (`shape`).

The sweep runs every 10 seconds and judges at most 1,000 waiting nodes per
tick, resuming after the last one on the next tick. A pass covers the nodes
that were waiting when it began, so every waiting node is judged within
ceil(N/1,000) ticks however many arrive meanwhile. On Postgres with 5,000
waiting nodes a tick that rewrites the reason of every node in its batch took
about 350 ms and a tick that changes none about 10 ms.

The field clears when an agent claims the node, and on the next sweep once an
eligible agent is online. The node waits up to its claim wait, 24 hours by
default or what `plan.ClaimWait(d)` sets for the pipeline, then fails as
unclaimable with `queue_timeout` and the reason as its error. Both the
30 seconds and the claim wait run from when the node first became ready, so an
agent that polls and cannot take the node does not postpone either. A missing
tool is never retried automatically.

## Per-node modifiers (Go SDK)

Three chainable modifiers on `*sparkwing.JobNode` (and the same names on
`*sparkwing.JobGroup`) control placement. All take the comma-OR / AND
term syntax above.

```go
// Hard filter on the claim queue: no runner advertising these labels
// means the node waits, then fails as unclaimable at its claim wait.
sw.Job(plan, "train", &Train{}).Requires("gpu")
sw.Job(plan, "package", &Package{}).Requires("arch=arm64", "trusted")
sw.Job(plan, "build", &Build{}).Requires("os=linux,macos", "amd64") // (linux OR macos) AND amd64

// Holds the node for a runner advertising cloud-linux, for a short window.
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
  fails it with `queue_timeout` at its claim wait (default 24h).
- **`Prefers`** -- runner-label preferences recorded in plan-snapshot
  metadata. A preference holds the node back from a runner that does not
  advertise it, for the controller's hold window; see
  [Local-first placement in claim mode](#local-first-placement-in-claim-mode).
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
the project `defaults.requires`. The reserved label **`local`** is the
compatibility spelling of `location=coordinator`: no runner can claim that
node, because a runner cannot advertise either term for itself.

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
from a profile's controller and hosts each run itself: it builds the
pipeline from the checkout it runs in, plans the run, and runs each node in
a process of its own under a claim it takes for that node. It works at the
trigger layer, unlike the cluster runner (`sparkwing-runner runner`),
which claims nodes.

While runners work on its nodes, the warm runner reads their
statuses with one `GET /api/v1/runs/{id}/nodes` per run. An unchanged run
backs off from 500 ms to 12 seconds with jitter; a status change resets the
interval. A `429` response with `Retry-After` or
`X-Sparkwing-Poll-After` sets a minimum wait before the next read.

## Local-first placement in claim mode

A warm cloud pool polls every 500ms, so first-in-first-out hands it every node
before a developer's own machine asks. The controller holds a node back from a
claim-mode runner whose labels do not satisfy the node's `Prefers` while another
runner is worth waiting for. A runner is worth waiting for when all four hold:
it advertises the preference, it satisfies the node's `Requires` as well, it
polled inside `--placement-liveness` (default 30s), and its last poll advertised
a slot it has not since spent. The hold runs `--placement-hold` (default 20s)
from the node's hold-from time, which is when it became claimable; after that,
the next eligible runner takes it. A node with no `Prefers` falls back to
`--default-prefer-labels` (comma-separated terms, empty by default), and a node
with neither is claimed first-in-first-out as before.

The rule reorders the queue and never widens it: `Requires` stays a hard filter,
and a held node blocks nothing behind it, because one poll reads past held
nodes in ready order, up to 512 of them, to find the first it may take. Three cases deliberately hold nothing
back, because each is a runner that cannot be counted on to take the node:

- a runner whose claim carries no `capacity` block, which said nothing about
  having room and is what a runner from before this protocol sends;
- a runner at its advertised ceiling, counting the nodes this controller has
  handed it since its last poll;
- a runner this controller has never awarded a node to. Labels are
  self-asserted, so polling alone would let any claim credential reserve the
  queue for a machine that never executes anything. Hand claim tokens only to
  machines you trust to poll honestly, and watch them in `GET /api/v1/agents`,
  which reports the labels and ceiling each one asserted.

A runner's hold-from time survives the 1us `ready_at` bump a label-mismatched
claim applies, so a mixed fleet polling constantly cannot restart the window.

Each claim records on the node which runner took it and why, as the
`placement_reason` field (`preference`, `fallback`, or `none`) and a
`node_placed` event. `sparkwing runs status` prints that line under a node the
preference decided:

```
    build:
      placement: runner:laptop-alice:1 (preferred)
```

Run the developer's own runner with the label its pipelines prefer:

```bash
sparkwing-runner runner --label location=local
```

## Agent-first execution with Kubernetes overflow

The `warm` trigger runner sends nodes through the `sparkwing-runner agent`
FIFO claim loop. That process opens no listener and polls the controller over
outbound HTTP(S). A LAN, VPN, or tailnet may provide a direct cache path, but
discovery grants no execution trust. Runner `labels` are self-asserted
placement terms, so they order the claim queue and never satisfy a hard
requirement. Local admission may happen after a claim.

When a node is ready, the dispatcher opens a claim round of at most five
seconds in which any eligible runner may claim it with
`POST /api/v1/nodes/claim`. Requirements and resource limits filter which
runner may claim; among eligible runners the queue is first-in-first-out,
subject to [local-first placement](#local-first-placement-in-claim-mode).

If the runner holding a node stops renewing its claim, that node ends as
`agent_lost`; the controller never reuses its row. When `.Retry(n)` still has
budget, the controller creates a fresh linked run for the lost node and its
descendants, reusing successful unrelated nodes and artifacts. Loss before the
job-body acknowledgement is free. Each acknowledged body invocation spends one
of the `n + 1` total invocations, including in-process and `RetryAuto` attempts.
The replacement keeps the original coordinator and location, waits on durable
bounded backoff, and briefly prefers another eligible runner. Coordinator
fallback cannot relax that placement. This is bounded at-least-once execution,
not exactly-once external effects.

After the claim round, an unclaimed unlabeled node is
atomically removed from the runner queue and sent to the configured Kubernetes
runner. A claim that wins that handoff owns the node, so the Kubernetes
fallback cannot execute it a second time. Labeled nodes never use this
fallback by default because Kubernetes Jobs advertise no capabilities. Set
`runner.triggerRunner.labels` to capabilities every spawned Job can honor; a
labeled node uses fallback only when those labels satisfy all of its
requirements. The fallback does not inherit `runner.labels`, because those
describe the outer pool process and may include architecture or hardware the
spawned Job does not have. Each Job reports the same configured labels through
its runtime runner metadata.

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
`requires: [local]` keeps any runner from claiming a dispatched node.

`sparkwing pipeline trigger <pipeline> --profile prod` hands the run to a
controller, which schedules each node onto a runner whose labels satisfy
its needed labels.

## Schedule triggers (cron)

A pipeline declares its cadences with the `schedule` trigger in
`sparkwing.yaml`, and each entry names the side that fires it with
`where: local` or `where: controller`. A scheduled run places its nodes
by the same label rules as any other run. The [crons page](crons.md)
covers every field, arming a host, and controller schedules.

## Worked examples

### Run only on the warm-runner pool

```go
sw.Job(plan, "deploy", &Deploy{}).Requires("warm-runner")
```

### Prefer ARM but accept anything

```go
sw.Job(plan, "build-image", &BuildImage{}).Prefers("arch=arm64")
```

Any runner whose labels satisfy the job's `Requires` can claim it. The
controller holds the node for a runner advertising `arch=arm64` while one is
worth waiting for, up to the placement hold; see
[Local-first placement in claim mode](#local-first-placement-in-claim-mode).

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
