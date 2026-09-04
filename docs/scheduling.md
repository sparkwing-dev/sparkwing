# Scheduling

How sparkwing decides which runner executes a job. The model is simple:
**runners advertise labels, jobs declare the labels they need, and the
controller hands each job to a runner whose labels satisfy it.**

## The model in one paragraph

Each job declares the labels it needs, per node through the Go SDK
(`.Requires(...)`) or for the whole pipeline through `requires:` in
`sparkwing.yaml`. Legacy runners self-report labels when they poll. Enrolled
executors use only administrator-owned capabilities; agent traffic cannot add
them. The controller filters incompatible runners before claim ordering. A node
with no eligible runner waits until the queue deadline, then fails with
`queue_timeout` (default 15m).

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

// Enrolled executors use this to adjust priority within their operator ceiling.
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
  metadata. They raise an eligible enrolled-executor offer's effective
  priority within its administrator-owned priority ceiling during one
  controller's offer round.
  The first matching term adds `len(Prefers) - index`, so preferences break
  nearby scores rather than overriding administrator-owned base priority. For
  example, a base-50 generic executor still outranks a base-0 executor that
  matches one preference. Legacy name-less claims remain FIFO and do not rank
  on preferences.
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
compatibility spelling of `location=coordinator`: enrolled and legacy fleet
helpers cannot claim that node. `--sw-local-only` is different; it selects
local secrets, state, cache, and log backends, not fleet placement.

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

Schema 30 adds administrator-owned executor enrollment for the assisted
scheduler. Enrollment binds an exact runner or service token prefix
to trusted capabilities, a priority range, concurrency and resource ceilings,
and a trusted placement location. `location=local` and `location=cloud` hard
requirements match only that enrollment field. `unknown` fails either selector,
while `location=coordinator` and its compatibility alias `local` cannot be
granted to any helper. Authenticated worker heartbeats can update only
liveness and finite nonnegative headroom. Idle enrollments remain visible;
stale ones appear offline. Each enrollment also reports the exact number of
live node claims as `active_slots`; legacy inferred rows omit that field because
their slot count is unknown. Rotating an enrollment to a different credential
marks it offline until that exact credential sends a heartbeat. The scheduling
summary and membership check apply hard capability, slot, headroom, and
resource filters before bounded priority. Each controller accepts at most 256
enrolled executors. The fixed safety bound keeps one scheduling snapshot and
offer round finite; adding the 257th returns
`executor enrollment limit reached: maximum 256 per controller`.
At schema 31 heartbeat persists the supervisor's body-protocol range, named
supervisor and body-host capability sets, and observational runner build
identity. Those fields gate protocol hosting; they do not attest a particular
compiled pipeline binary, and Sparkwing never requires exact runner-version
equality. Observed OS, architecture, and environment are not trusted placement
facts. Use explicit non-reserved trusted capabilities for admission tests
rather than treating an `os=`, `arch=`, or `environment=` selector as automatic
helper placement.
Once an executor wins a node, its coordinator and location become hard
requirements for any agent-loss retry.

Schema 31 admits only nodes carrying an immutable controller-derived execution
policy. Migrated schema-30 and other unsealed nodes stay coordinator-only.
Prepare checks the indexed policy and placement metadata, then requires the
helper's supervisor protocol and capability sets before decoding the bounded
policy document or reserving capacity. Missing host capabilities return
`upgrade_required`; a helper whose minimum protocol is newer than the sealed
body returns `protocol_incompatible`. A compatible helper currently stops at
`body_attestation_required`: exact compiled-body attestation and policy
enforcement are not implemented yet, so it cannot reserve, offer, or execute.
The upgrade response names the highest stable release that introduced every
known missing floor. An unknown capability or protocol reports `safe_hold`
with no minimum release, so a future updater cannot guess a download target.
Eligible nodes fall back once to the coordinator; helper-only nodes remain
pending. Automatic runner update is not implemented.

Schema 30 added the durable offer records and arbitration rules retained by
assisted scheduling. Schema 31 keeps that data model, but public helpers cannot
submit an offer while compiled-body attestation is absent. The reservation
contract binds one exact resource digest and physical slot through wingd. One
shared local slot ledger covers every configured coordinator, so two
controllers cannot award the same physical capacity. wingd also enforces
machine-wide and per-membership CPU and memory contribution ceilings. A
gateway must make an equivalent downstream admission reservation before
offering. Schema 30 also persists separate random internal identities for the
controller and each enrollment. Membership IDs derive from those two values,
so credential rotation does not split execution history and the same display
name on another controller cannot merge it. A membership ID is a non-secret
journal identity, not proof that a network endpoint is trusted.

The round lasts at most five seconds. On PostgreSQL, opening the round takes an
exclusive eligibility fence while ordinary allocation, release, expiry,
claim-heartbeat, and plan mutations take a shared fence. Its recorded highest
eligible effective priority therefore describes one exact eligibility instant,
while a deadline award cannot stall an unrelated claim heartbeat. Target and
award paths load active executor occupancy once; award locks candidate executor
rows in one canonical batch. A late heartbeat cannot revive an expired claim
after its capacity is reusable. An offer at priority 100 or at that priority wins
immediately.
Otherwise the deadline winner is the highest effective priority, then the
earliest offer, executor name, physical slot, and holder. Effective priority
starts at the enrolled base. Run priority and the first matching `Prefers` term
are added, then clamped to zero and the administrator-owned ceiling.
Requirements and resource limits always filter before ranking. The winner
consumes the same reservation; losers release theirs. Repeating an offer after
a lost response recovers the same fenced claim.

Each controller owns its own offer rounds. Multiple configured controller
memberships share the machine's physical slot ledger, but the offer protocol does not compare
their run priorities before one membership reserves a free slot. Simultaneous
work from different controllers is therefore selected by which membership
prepares and reserves first, not by a global cross-controller priority order.

If the winning agent or gateway stops heartbeating, that node ends as
`agent_lost`; the controller never reuses its row. When `.Retry(n)` still has
budget, the controller creates a fresh linked run for the lost node and its
descendants, reusing successful unrelated nodes and artifacts. Loss before the
job-body acknowledgement is free. Each acknowledged body invocation spends one
of the `n + 1` total invocations, including in-process and `RetryAuto` attempts.
The replacement keeps the original coordinator and location, waits on durable
bounded backoff, and briefly prefers another eligible executor. Coordinator
fallback cannot relax that placement. This is bounded at-least-once execution,
not exactly-once external effects.

After the offer window, an unclaimed unlabeled node is
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
`requires: [local]` keeps a fleet helper from claiming a dispatched node.
`--sw-local-only` selects local backends but does not add a placement rule.

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

Any runner whose labels satisfy the job's `Requires` can claim it. An enrolled
executor applies the preference inside its trusted priority range; a legacy
runner ignores it.

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
