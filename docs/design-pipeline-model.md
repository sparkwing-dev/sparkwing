# Pipeline Model Redesign

**Committed model (Model 1: Plan-then-Execute).** This is the authoritative
design doc for sparkwing's next-generation pipeline model. Implementation
decisions and checklists are in `IMPLEMENTATION.md`.

The alternative models were rejected on 2026-04-20 and are marked WONT DO:
- `DESIGN-pipeline-model-yaml.md` (YAML-contract) - REJECTED.
- `DESIGN-pipeline-model-generated.md` (generated-contract) - REJECTED.

Do not implement or mix in patterns from the rejected alternatives.

Captures the decisions from the 2026-04-19 / 2026-04-20 design sessions.

## Problem Statement

Sparkwing's current model has three friction points that have surfaced in use:

1. **Product identity is muddled.** README treats "general script runner" as an open
   question. This has leaked into design decisions: partial-support for scripts,
   ambiguity about what belongs in `.sparkwing/` vs elsewhere, unclear messaging.
2. **Pipelines cannot change shape based on runtime state.** A `pipelines.yaml`
   declares triggers plus a single root job. Everything else is emergent from Go code
   that spawns children at runtime. This gives Temporal-like flexibility but forfeits
   GHA-style upfront DAG visibility, and it interacts badly with conditional logic
   (e.g., "skip tests" still forces GHA-equivalent gymnastics because the DAG is not
   derivable before execution).
3. **No first-class work deduplication or output protocol.** Jobs run redundantly
   across pipelines; outputs are ad-hoc; downstream consumers cannot rely on typed,
   durable references to upstream artifacts.

The redesign targets all three.

## Product Identity (settled)

Sparkwing is a **CI/CD platform**, defined broadly: any repo-scoped work triggered by
git events, webhooks, schedules, or manual invocation. The `.sparkwing/` directory lives
in a repo and codifies that work.

Explicitly out of scope:
- Environment creation and management (deferred to xwing).
- Repo-less cron/job running (a separate product later if demand justifies it; do not
  retrofit sparkwing for it).
- Generic script discovery and invocation (use justfile/mise/make; sparkwing is not
  a replacement for those tools).

Shell scripts that want sparkwing ergonomics are wrapped in trivial Go jobs. The job
is the public interface; the script is an implementation detail.

## Design Goals

1. **Agent-readable.** A human or an LLM should be able to read the pipeline
   declaration and understand: what triggers it, what runs, in what order, with what
   inputs and outputs, and which branches of the DAG are dynamic. Minimal
   whole-program simulation required.
2. **Shape reflects the run.** The DAG rendered in the dashboard for a given run
   reflects what actually ran, not a static superset with gray nodes.
3. **Dedup cheaply.** Redundant work should be identified and short-circuited
   without spinning up a runner just to decide.
4. **Typed contracts between jobs.** Outputs are schemas, not strings.
5. **Self-hosted, no magic.** Behaviors are explicit and observable. No
   "skipped for unknown reason" states.

## Core Model

### Jobs are the only execution primitive

A job is a Go struct with a `Run` method. It executes on exactly one runner (one pod in
cluster mode, one local process in local mode).

### Pipelines are declarative entrypoints

A pipeline is a triggered, named entry point that returns a typed Plan. One
interface, one body:

```go
Plan(ctx context.Context, in Inputs, run RunContext) (*Plan, error)
```

No Execute alternative, no dual-mode dispatch. Commitment to a single model
eliminates decision fatigue and bifurcated feature support.

**Trivial pipelines** (one command, no DAG) implement Plan and return a
one-node plan whose Step does the work:

```go
type Lint struct{ sparkwing.Base }

func (p *Lint) Plan(_ context.Context, rc sparkwing.RunContext) (*sparkwing.Plan, error) {
    plan := sparkwing.NewPlan()
    plan.Step(rc.Pipeline, func(ctx context.Context) error {
        _, err := sparkwing.Sh(ctx, "go vet ./...")
        return err
    })
    return plan, nil
}
```

Every pipeline implements Plan -- one way to do things, even when there's
only one thing to do. Earlier drafts had a `SingleJobPipeline` shorthand for
the no-DAG case; it was removed in v0.46.0 because two shapes created exactly
the "which do I reach for?" decision the rest of this document avoids.

**Runtime-dynamic DAGs** (where shape genuinely emerges from job outputs) use
`ExpandFrom` or `dynamic: true` subtrees within Plan. Fully unstructured
"spawn whatever at runtime" from Go is not supported because the point of
Plan is inspectable structure; the escape hatches cover the legitimate cases.

### Steps are within-job structure

Steps are sequential (by default) chunks inside a job's Run method. They exist for log
grouping, progress display, and step-level retry hooks. They are not standalone-invocable
and never appear as independent nodes in the DAG.

### Plan-then-Execute

```go
func (p *DeployPipeline) Plan(ctx context.Context, in DeployInputs, run sparkwing.RunContext) (*sparkwing.Plan, error) {
    plan := sparkwing.NewPlan()
    plan.Add("build", &BuildJob{Component: in.Component, Commit: run.Git.Sha})

    if !in.SkipTests {
        plan.Add("test", &TestJob{}).Needs("build")
        plan.Add("deploy", &DeployJob{}).Needs("test")
    } else {
        plan.Add("deploy", &DeployJob{}).Needs("build")
    }
    return plan, nil
}
```

Key properties:
- Runs in-controller, not in a runner pod. Should be fast (milliseconds) and
  side-effect-light (read-only filesystem and API calls at most).
- Receives typed inputs and a `RunContext` (git state, trigger env, cluster info,
  invocation metadata). Emits a typed plan.
- The resulting DAG is rendered upfront in the dashboard and snapshotted into the
  run record; execution follows the snapshot.
- Skip semantics are trivial: if a job should not run, it does not appear in the plan.
  No edge-propagation rules.

### Five Dynamism Tiers

Most dynamism is handled declaratively. Tiers, from most-structured to most-emergent:

**1. Shape from inputs.** Plan reads pipeline inputs and branches to produce
different DAGs. Covers skip flags, branch logic, deploy-only-changed-services
from `git diff`. Handles the majority of real cases.

**2. Conditional skip on upstream output.** `SkipIf(ref, predicate)` modifier
marks a node as `Skipped` based on an upstream's output. Controller evaluates
the predicate after the upstream completes; no runner is spawned for the
skipped node.

```go
setup := plan.Add("setup", &SetupJob{})

plan.Step("tests", runTests).
    Needs(setup).
    SkipIf(setup.Output(), func(s SetupOut) bool { return s.SkipTests })

plan.Step("deploy", deployAll).
    Needs(setup).
    SkipIf(setup.Output(), func(s SetupOut) bool { return !s.Approved })
```

Use when setup produces flags that determine whether downstream work runs.
Predicates are pure Go, evaluated fast in the planner pod. Nodes flip to
"skipped" in the dashboard as soon as the decision is made.

`Skipped` is distinct from `Satisfied`: Skipped is a node never dispatched
(controller-side decision); Satisfied is a runner that ran but chose to do no
work. Both are terminal-green for dependency purposes; distinct in
observability.

**3. Shape from upstream output (fan-out).** `ExpandFrom` / `SpawnAll(ref)`
materializes children from an upstream's output list. One "discover" job
emits items; children fan out one per item.

```go
plan.Add("discover", &DiscoverJob{})
plan.ExpandFrom("discover", func(items []string) []sparkwing.Node {
    return lo.Map(items, func(i string, _ int) sparkwing.Node {
        return sparkwing.Job("test-"+i, &TestJob{Target: i}).Needs("discover")
    })
})
```

**4. Full re-plan after an upstream completes.** `ReplanAfter(node, fn)` lets
the planner re-enter Plan with the upstream's output in hand. New nodes
appear progressively. Heavier than SkipIf or ExpandFrom; use when the
structure of downstream work (not just inclusion) varies based on the
output.

```go
setup := plan.Add("setup", &SetupJob{})

plan.ReplanAfter(setup, func(out SetupOut, p *sparkwing.Plan) {
    if !out.SkipTests {
        p.Step("tests", runTests)
    }
    if out.DeployTarget != "" {
        p.Step("deploy", deployAll).Env("TARGET", out.DeployTarget)
    }
})
```

This is the Buildkite "pipeline upload" pattern in typed Go.

**5. Fully emergent** (`dynamic: true` on a job). A job spawns arbitrary
children at runtime. Dashboard shows the subtree materializing progressively.
Escape hatch for cases the prior tiers cannot express. Clearly marked so
agents know "read the code, the shape is emergent here."

Authors default to tiers 1 and 2. Tier 3 covers matrix-over-runtime-list.
Tier 4 covers "the shape genuinely varies by output." Tier 5 is reserved
for workloads no declarative form fits.

## Architecture: Orchestrator per Run

Three components, each with a distinct role. The design target is
**orchestrator as per-run coordinator, controller as global supervisor**.
First implementation will be controller-heavy (see "Implementation
phasing" below).

### Controller (control plane + authoritative read surface)

Long-lived service with two distinct responsibilities:

1. **Control plane** for orchestrator lifecycle. Spawns per trigger,
   monitors health, restarts crashed ones, tears down completed ones.
2. **Authoritative read surface** for pipeline state. Holds the
   complete, current view of every run's DAG in its DB. Dashboard,
   CLI, API, and agent consumers all read from here.

Does not compile or execute user Go. Does not create K8s Jobs for
pipeline nodes (orchestrators do that). Does not coordinate pipelines
(orchestrators do that). But owns the state store and the read API,
because users need rich live observability and that demands an
always-up-to-date authoritative view.

Owns:
- Trigger intake (webhooks, schedules, manual) and routing to orchestrators.
- Orchestrator lifecycle: spawn per trigger, monitor health via heartbeat,
  restart crashed ones, tear down completed ones.
- **Full pipeline state store**: every run's complete DAG including node
  status, timing, outputs, predicate evaluations, modifier state, edges.
  Updated by orchestrators via write-through on every meaningful
  transition. Source of truth for dashboard, CLI, historical queries,
  and crash recovery.
- Global coordination state that orchestrators query:
  - `Exclusive` lock registry (distributed mutexes across runs).
  - `CacheKey` lookup table (cross-pipeline dedup).
  - `PipelineRef` resolution (last successful run of another pipeline).
- Read surface: REST API and live-update protocol (SSE or websocket) for
  dashboard, CLI, MCP tools, agents. All consumers use the same API.

Controller scales by the number of orchestrators it supervises and the
write volume of their state transitions, not by the compute of the work
those pipelines do. At 100 concurrent runs with ~100 transitions each,
that's ~10k writes/minute; SQLite handles it, Postgres scales further.

### Observability data flow

- Orchestrator pushes every meaningful state transition to the controller
  as a sequenced event (monotonic seq per run).
- Controller persists the event and updates its in-memory view.
- Controller publishes live updates to subscribed dashboards / CLI clients.
- Dashboard / CLI render from the controller's view directly. No proxy
  to orchestrators on the read path.
- Historical runs render identically to live runs (same DB, same API).

Orchestrator batches closely-spaced transitions with a short flush
interval (~100ms) to keep controller write pressure reasonable and
observability lag sub-second.

Benefits:
- If an orchestrator dies, dashboard keeps showing the last-known state
  until replacement rehydrates. No visible gap.
- Cross-run queries (all failures on node X, slowest pipelines last week)
  are trivial on the controller's DB.
- One API for dashboard, CLI, MCP, agents. Equivalent feature set
  everywhere.

### Orchestrator pod (per run)

Pod spawned per pipeline invocation. Owns the in-flight coordination of a
single run end to end. Evolution of today's root-runner with scope both
narrowed (doesn't run the pipeline's work) and broadened (coordinates more
than just Plan).

Lifecycle:

1. **Spawn.** Controller dispatches an orchestrator when a pipeline triggers.
2. **Run Plan.** Orchestrator fetches the repo via gitcache, loads the
   pipeline binary (compiled once per commit, cached), executes `Plan()`,
   writes the resulting DAG to the controller for durability.
3. **Dispatch jobs.** Orchestrator creates K8s Jobs directly (using its
   scoped service account) for nodes whose dependencies are satisfied.
4. **Receive outputs.** Job runners stream outputs back to the
   orchestrator. Orchestrator persists them via write-through to the
   controller's DB (and blob storage for large artifacts).
5. **Evaluate decisions.** As upstreams complete, orchestrator evaluates
   `SkipIf` predicates, `ExpandFrom` generators, `ReplanAfter` closures,
   and hook closures. Updates the DAG accordingly. Dispatches next nodes.
6. **Report status.** Emits heartbeats and status transitions to the
   controller for the dashboard and crash recovery.
7. **Exit.** When the pipeline reaches a terminal state, orchestrator
   shuts down.

### The rule: authors write it, orchestrator runs it

All pipeline-author code runs in the orchestrator pod: `Plan`, Job `Run`
methods (via child runners), predicates, generators, closures, hooks.
Controller never compiles or executes user Go. This is the multi-tenant
safety boundary.

Predicates and closures must be **lightweight coordinator work**, not
compute. Per-call timeouts (default 1s) fail slow predicates with clear
errors; a predicate that needs heavy work should be split into a Job
whose output feeds a lightweight `SkipIf`. The orchestrator is a
coordinator, not a worker.

### Crash recovery

Orchestrator is stateful but not the durable store. Writes through to
controller's DB on every important transition. If the orchestrator
crashes:

1. Controller notices missed heartbeats.
2. Spawns a replacement orchestrator with the same run ID.
3. Replacement loads persisted state from the DB, rehydrates the DAG.
4. Resumes evaluating pending predicates and dispatching remaining nodes.
5. In-flight job runners reconnect to the replacement orchestrator.

Runs are never lost; worst case is seconds of coordination latency
during a recovery.

### Job runners

Spawned per job node in the DAG. Execute the job's `Run` method, stream
logs, report outcomes. Ephemeral. Connect to a single orchestrator, not
the controller.

### Heartbeat topology

Strict hierarchy: controller <> orchestrator <> runners. Controller never
talks to runners directly; orchestrator never involves controller in
per-runner health.

- **Controller <> orchestrator**: heartbeats + state write-through.
  Controller knows each orchestrator is alive; spawns a replacement if
  heartbeats miss.
- **Orchestrator <> runners**: heartbeats + control signals + status
  events (start, exit, result). Orchestrator notices runner failures and
  reschedules or fails the node; controller is not in the loop.

Runners discover their orchestrator via a stable Kubernetes Service name
derived from the run ID (e.g., `orchestrator-run-abc123`). When an
orchestrator is replaced after a crash, the replacement takes the same
Service name; runners reconnect transparently.

Controller learns about runners only through orchestrator state writes
(e.g., "node X dispatched to runner Y, running"). It can expose this via
the dashboard but holds no direct connection.

### Runner output split: structured vs unstructured

Job runners split their output into two distinct streams with different
destinations:

**Structured data -> orchestrator**:
- Typed `Output` struct values (the actual data flow for downstream jobs).
- Error details: code, message, stack trace, structured failure info.
- Status metadata: started, exited, retry attempt, exit code.
- BlobRef handles (not blob bytes; just references).

**Unstructured bulk -> sparkwing-logs service**:
- Stdout and stderr byte streams.
- Anything the job emits that isn't a structured output or error.

Rationale: orchestrator needs structured data to do its coordinator job
(evaluate predicates, route typed outputs, decide retries, trigger
OnFailure). It does not need "lint printed 10,000 lines of warnings." Bulk
logs belong on a data plane that scales independently.

Dashboard correlates by node ID: structured state comes from the
controller (which holds the view updated by orchestrator write-through),
logs come from the logs service. Both identified by the same node ID.

### Why this architecture

- **Controller doesn't compile or execute user Go.** Stable, small,
  upgradeable independently of pipelines.
- **Multi-tenant safe.** Each team's Plan, predicates, and closures run in
  an isolated pod. A bad predicate (infinite loop, heavy compute, panic)
  affects only that pipeline's planner, never other teams' runs or the
  controller itself.
- **Scalable.** Controller is IO-bound and stateful; scales to many
  concurrent pipelines before needing horizontal sharding.

### Safety boundaries on the planner pod

- Kubernetes resource limits (CPU, memory) cap blast radius.
- Network policies restrict outbound calls to the controller and gitcache.
- Per-decision timeouts fail slow or hung predicates with clear errors.
- Planner runs in a namespace scoped to the pipeline's team/project; no
  cross-tenant access.
- Crashes fail the single pipeline run loudly; other runs are unaffected.
  Controller can respawn a fresh planner from the snapshotted Plan if the
  run is recoverable.

### What the controller evaluates itself

Only sparkwing-framework-internal logic: dependency edges, `Exclusive` lock
contention, `CacheKey` lookups against its own DB, routing from `RunsOn`
labels. These are data-structure operations on trusted input, not user code.

**Rule**: if the pipeline author wrote it, it runs in the planner pod. If
the sparkwing team wrote it, it may run in the controller. Clean separation.

### Plan runs *before* any job dispatches

This is the key property that solves the GHA "spawn a runner to decide skip"
problem. Plan reads inputs, trigger env, git state, and cluster state, then
returns the DAG. Nodes that shouldn't run are simply absent. No runner tax for
deciding conditional shape.

GHA's model: to conditionally skip a job, add a "decide" job that runs on a
runner, emits a boolean, downstream gates on the boolean. N conditional jobs
means N decider pods.

Sparkwing's model: one planner pod per pipeline invocation answers all
conditional-shape questions at once. Actual work jobs dispatch only if their
node is in the returned DAG.

Caveat: Plan must be cheap and side-effect-light. Common decisions (skip
flags, branch checks, changed-files from `git diff`) fit. Heavy discovery
(scanning thousands of files, querying slow APIs) should be a dedicated
lightweight gate job whose output feeds `ExpandFrom`.

### Scale expectations

Because the controller is a thin supervisor rather than an active
orchestrator, it scales by the number of orchestrators (pipeline runs)
rather than the total work those pipelines do.

For a 4-core 8GB controller with SQLite:
- Supervising 1,000+ concurrent orchestrators comfortably.
- First bottleneck: DB writes from orchestrators' durability write-through.
  Postgres migration extends significantly.
- Dashboard aggregation is the second likely bottleneck at scale
  (multiplexing live state from many orchestrators); addressed via caching
  and incremental update protocols.

Orchestrator pods cost ~50-200MB idle memory each. 1,000 concurrent
runs ~= 100GB across the cluster, acceptable for most deployments and
cheap to shed via Kubernetes autoscaling.

For hyperscale (thousands of teams, hundreds of pipelines/second),
horizontal controllers sharded on pipeline name or team, shared Postgres
backend. Deferred; not a v1 problem.

### Implementation phasing

The target architecture above is not required from day one. Recommended
phasing:

**v1**: controller-heavy. Orchestrator is a short-lived "planner" that
runs Plan and evaluates predicates; controller handles dispatch, output
routing, and everything else. Fewer moving parts to implement correctly.

**v2**: extract orchestration into long-lived per-run orchestrators. Move
K8s Job creation to orchestrators (each with a scoped service account).
Move output routing from runners-through-controller to runners-direct-to-
orchestrator. Controller shrinks to control-plane-only.

The migration is internal architecture. The Pipeline interface, modifiers,
Refs, and authoring ergonomics are identical in both phases; pipeline
authors never notice the change.

## Local/Cluster Parity

The pipeline model, Plan execution, DAG rendering, state transitions,
and observability surfaces are **identical** in local and cluster
modes. The only differences are in the infrastructure backends used
underneath.

This is a non-negotiable property: if a pipeline works locally, it
should work in the cluster. If a pipeline fails locally, it should
fail the same way in the cluster. The "write local, push to cluster"
promise depends on it.

### Shared surface

Both modes run the same code paths for:

- `Plan` execution and DAG construction.
- All modifiers: `Needs`, `SkipIf`, `OnFailure`, `Retry`, `Timeout`,
  `CacheKey`, `Exclusive`, `BeforeRun`, `AfterRun`, `Webhook`.
- All dynamism tiers: inputs, SkipIf, ExpandFrom, ReplanAfter,
  dynamic:true.
- Typed Refs for data flow between jobs.
- Outcome states: Success, Failed, Satisfied, Cached, Skipped,
  Cancelled.
- Dashboard rendering (same React components, same data contract).
- CLI surface (`wing <pipeline>`, `sparkwing jobs ...`).

### Pluggable backends

The orchestrator is written against abstractions; local and cluster
modes provide different implementations:

| Concern | Local backend | Cluster backend |
|---|---|---|
| Job execution | goroutine in `wing` process | K8s Job pod |
| State store | SQLite at `~/.sparkwing/state.db` | Controller DB (SQLite or Postgres) |
| Blob storage | Content-addressed filesystem at `~/.sparkwing/blobs/` | sparkwing-cache service |
| Log sink | Files at `~/.sparkwing/runs/<id>/logs/` | sparkwing-logs service |
| Exclusive locks | `flock(2)` on `~/.sparkwing/locks/<key>.lock` | Controller DB lock table |
| CacheKey lookups | Local SQLite `cache` table | Controller DB `cache` table |
| PipelineRef resolution | `~/.sparkwing/pipelines/<name>/last-success` pointer | Controller DB `pipeline_refs` table |
| Orchestrator <> runner transport | In-process channel | gRPC over Service name |
| Secret resolution | Local env, `~/.sparkwing/secrets.yaml`, or OS keychain | Configured secret store |

Same orchestrator code consumes all of these through a single
`Backends` interface. Tests run against mocks of the same interface.

### Local mode architecture

`wing <pipeline>` in a repo with a `.sparkwing/` directory:

1. Loads the pipeline binary (compiles on demand, caches by source hash).
2. Starts an in-process orchestrator.
3. Runs `Plan()` to produce the DAG.
4. Dispatches jobs as goroutines. Each goroutine runs the Job's `Run`
   method directly in the process.
5. Routes typed outputs through the same Ref mechanism (in-memory
   resolution plus write-through to the local SQLite store for
   dashboard visibility).
6. Writes state transitions to local SQLite with the same schema the
   controller uses.
7. Streams logs to local files with the same format the logs service uses.
8. Exits when the run reaches a terminal state.

`sparkwing web` is a separate process that reads the local SQLite
store (and log files) and serves the same dashboard UI. It can render
live runs (while `wing` is active) and historical runs (from
persisted state), identically.

### What local mode can do (filesystem-backed)

Most cross-run state works naturally locally via `~/.sparkwing/`:

- **Cross-pipeline refs.** `~/.sparkwing/pipelines/<name>/last-success`
  is a pointer file updated on every successful run.
  `PipelineRef("lib-build", LastSuccess)` reads the pointer, loads the
  referenced run's outputs from `~/.sparkwing/runs/<id>/`, resolves
  the Ref. Works across separate `wing` process invocations.
- **CacheKey dedup.** Local SQLite has a `cache` table keyed on
  CacheKey. Plan checks it before dispatch. Hit returns Cached with
  replayed outputs; miss runs and inserts. Works cross-pipeline on
  the same laptop.
- **Exclusive locks.** `~/.sparkwing/locks/<lock-key>.lock` acquired
  via `flock(2)`. Works across separate processes. Second concurrent
  attempt waits or fails per configuration.
- **Blob dedup.** `~/.sparkwing/blobs/<sha256>` content-addressed
  filesystem storage. Identical content from two runs shares one
  on-disk file. Same semantics as cluster blob storage.

### What local mode cannot do

The real gaps, after filtering filesystem-solvable concerns:

- **Cross-laptop coordination.** A pipeline on laptop A cannot
  reference a run from laptop B. `PipelineRef` scope is one laptop.
  Non-issue for the "write local, push to cluster" workflow.
- **True CPU / memory / network isolation.** Local jobs share the
  host's resources; no K8s cgroups or network policies. A job that
  would hit its pod memory limit in cluster runs unbounded locally.
  Behavioral difference, rare to cause correctness bugs.
- **RunsOn routing.** One execution context (the host); labels are
  recorded but not acted on.
- **Fleet / multi-runner scheduling.** Unavailable; there's one
  runner (the local process).

Local mode is a faithful functional replica. Infrastructure-specific
behaviors (resource limits, node selection, fleet compute) differ.

### Why this parity is non-negotiable

- Authors iterate locally; if local lies, they ship broken pipelines.
- Agents use local mode for fast test cycles; divergent behavior
  confuses agent reasoning.
- Dashboard renders both modes; one set of UI components.
- New features (modifiers, outcome states, dynamism tiers) must land
  in both backends simultaneously. A feature that works only in the
  cluster is an incomplete feature.

### Implementation implication

The SDK's orchestrator core is written against `Backends` from day
one. Local and cluster backends ship together. CI runs the same
pipeline test suite against both backends to detect drift.

## Crash Recovery

The architecture supports full recovery from orchestrator death: controller
detects the failure, spawns a replacement, replacement rehydrates from the
controller's DB, heartbeats resume, in-flight runners reconnect. Runs do
not get lost. Dashboard keeps showing last-known state during the
transition.

### What the controller's DB must hold for rehydration

Beyond the observability state (node status, outputs, timing, predicate
results), the DB must capture enough for a replacement orchestrator to
answer "what do I need to do next?" without knowing anything that the
dead orchestrator held in memory:

- **Plan snapshot** at Plan time. Full DAG including any extensions
  materialized by `ReplanAfter` or `ExpandFrom`.
- **Dispatch intent**: "I am about to dispatch node X with inputs Y."
  Written *before* the K8s Job is created, so replacement can distinguish
  "nothing happened yet" from "dispatch in progress, check K8s."
- **Pending predicate evaluations**: which `SkipIf`, `ExpandFrom`,
  `ReplanAfter`, and hook closures are waiting on which upstream outputs.
- **Scheduled future work**: retry-after timestamps, timeout deadlines,
  any delayed pipelines. Never held in orchestrator memory only.
- **Exclusive lock holders by run ID**, not orchestrator identity.
  Replacement automatically inherits the lock.
- **Sequence numbers on all events** per run. Replay can detect gaps
  and request resends.

With this in place, a replacement orchestrator's first pipeline is to
read the DB and reconstruct the in-flight picture.

### The recovery sequence

1. Controller detects missed heartbeats (grace period ~30s to tolerate
   network blips and GC pauses).
2. Controller acquires a single-writer lease on the run ID in its DB.
3. Controller spawns replacement orchestrator pod with the same run ID.
4. Replacement loads Plan, state, dispatch intents, pending work, and
   scheduled events from controller's DB.
5. Replacement reconciles in-flight work:
   - For each "dispatched" node, check Kubernetes for the Job. Found:
     resume monitoring. Not found (dispatch died mid-air): recreate.
   - For each "running" node, runners reconnect via the stable Service
     name. Any output they buffered is re-sent with sequence numbers;
     the controller dedupes on seq.
   - For pending predicates, evaluate any whose upstreams completed
     during the transition.
6. Replacement registers its heartbeat and resumes normal operation.

Dashboard shows a brief "reconciling" badge on the affected run but no
data loss.

### Implementation details to get right

**Double-spawn protection**. If the "original is dead" decision is wrong
and the original is just slow, two orchestrators racing is a correctness
disaster. Prevent via:

- Single run-ID lease in the controller's DB. Old orchestrator loses the
  lease when its heartbeat expires; replacement acquires it on spawn. If
  the old one is still alive, it detects the lost lease and shuts down
  cleanly before acting.
- Deterministic K8s Job names keyed off `(run_id, node_id, dispatch_attempt)`.
  Duplicate dispatches fail with a K8s name collision rather than creating
  parallel runners.

**Runner output in flight**. Runner is streaming output when orchestrator
dies. The output may or may not have been persisted.

- Runners tag every output event with monotonic sequence numbers per node.
- On reconnect, replacement asks each live runner for everything since
  the last persisted seq.
- Controller's DB enforces uniqueness on `(run_id, node_id, seq)`, so
  retransmissions are idempotent.

**Partial dispatch**. Original wrote "dispatching node X" to the DB but
didn't actually create the K8s Job before crashing.

- Replacement reconciles by checking K8s for a Job matching the
  deterministic name.
- Exists: assume dispatch succeeded, attach monitoring.
- Doesn't exist: re-dispatch. Idempotent by name.

**Time-based state**. A retry was scheduled for `t + 30s`; original died
before firing it.

- Scheduled events live in a DB table keyed by (run_id, fire_at).
- Replacement queries for due events on startup and resumes them.
- No scheduler state is held in orchestrator memory only.

**Exclusive lock reclaim**. If a lock is released prematurely, another
pipeline may grab it and cause correctness bugs.

- Locks are keyed on run ID, not orchestrator identity.
- Replacement inherits held locks automatically by being the same run.
- If the run is permanently lost (no replacement spawns within a cutoff),
  controller releases held locks at that point.

### v1 vs v2 recovery targets

This is real engineering work; plan phased targets:

- **v1 target**: crash detection + replacement spawn + basic rehydration
  from the DB. Completed work is preserved; most in-flight work survives.
  Edge cases (runner outputs mid-flight, dispatch in-progress at moment
  of crash) may result in single-node re-execution or extended
  "reconciling" time. Acceptable for v1.
- **v2 target**: sequence-number gap detection, runner retransmission,
  K8s reconciliation, lease-based single-writer invariant. Crashes
  become invisible to users; no re-execution of completed work.

The v1 -> v2 migration adds resilience primitives without changing the
Pipeline interface or author-facing surface.

## Cancellation

Users can cancel in-flight work at two granularities:

- **Whole run**: cancels every non-terminal node in the pipeline.
- **Subtree**: cancels a specific node and everything downstream of it.
  Upstream nodes are unaffected and continue running. Useful for aborting
  a specific branch (e.g., cancel a long-running `integration-tests`
  without killing earlier `build` or parallel `security` work).

### What happens on cancel

Orchestrator-side sequence, identical for whole-run and subtree cases:

1. Controller receives the cancel request (`wing cancel <run-id>` or
   `wing cancel <run-id> --node <node-id>`).
2. Controller records the cancel intent and forwards to the orchestrator.
3. Orchestrator:
   - Stops dispatching new nodes in the cancelled scope.
   - Sends SIGTERM to runners for nodes in the scope that are currently
     running.
   - Waits a grace period (default 30s; configurable per-node via
     `CancelGrace` modifier) for each runner to exit cleanly.
   - Sends SIGKILL to any runner still alive past grace.
   - Marks each affected node `Cancelled` in the run state.
   - For downstream nodes that hadn't started yet, marks them `Cancelled`
     with reason "upstream-cancelled."
4. Modifier state cleanup:
   - Held `Exclusive` locks release as soon as their node reaches a
     terminal state (here, Cancelled). No stale locks blocking other
     pipelines.
   - Scheduled retries or timeouts for cancelled nodes are dropped.
   - OnFailure hooks **do not fire** for cancelled nodes. Cancel is
     distinct from failure; OnFailure is for genuine job-level failures.

### Job author responsibilities

Jobs that hold critical state (mid-deploy, database migration, etc.) must
handle SIGTERM cleanly or be written to be resumable. Sparkwing guarantees
predictable cancellation semantics (lock release, state transition, grace
period) but cannot guarantee cleanliness of a job that was interrupted
mid-operation.

For critical sections where cancellation is unsafe, the job can install a
SIGTERM handler that delays the exit until the critical section completes
(up to the grace period). Beyond grace, SIGKILL wins; design jobs for
resumability.

### Cancelled as a distinct terminal state

Node outcomes are now:

| Outcome | Set by | When |
|---|---|---|
| Success | job | Ran work, succeeded |
| Failed | job | Ran work, failed |
| Satisfied | job | Dispatched, no work needed (reason required) |
| Cached | controller | CacheKey matched pre-dispatch |
| Skipped | controller | Plan-time or SkipIf decision |
| Cancelled | orchestrator | User-initiated cancel |

Distinct from Failed. Dashboard styles them differently, downstream
semantics differ (Failed may trigger OnFailure; Cancelled does not).

## Triggers

Triggers live in a minimal `pipelines.yaml`, not in Go. This is the one piece of
declarative config sparkwing retains.

```yaml
pipelines:
  - name: build-test-deploy
    entrypoint: BuildTestDeploy       # Go type registered via RegisterPipeline
    on:
      deploy: {}
      push:
        branches: [main]
        env:
          TARGET: kikd-prod
      schedule: "0 */6 * * *"
      webhook: { path: /hooks/btd }
    secrets:
      - SPARKWING_ARGOCD_SERVER
      - SPARKWING_ARGOCD_TOKEN
    tags: [ci, deploy]
```

Rationale:
- Triggers are config, not code. Lifecycle and editors differ from pipeline logic.
- The controller needs to evaluate triggers without compiling or loading Go.
- Non-Go contributors (and agents) can add a webhook or cron schedule without a
  rebuild.

Discipline to hold: `pipelines.yaml` is a registry. No job definitions, no DAG,
no inputs schemas, no conditions. If you want any of those, they go in Plan.

## Configuration and Secrets

Four kinds of runtime config, each with a defined home.

| Kind | Home | Visible to Plan? | Example |
|---|---|---|---|
| Pipeline inputs | Typed `Inputs` struct | Yes (primary Plan input) | `SkipTests bool` |
| Run context | `RunContext` param | Yes | `run.Git.Sha`, `run.TriggerEnv("TARGET")` |
| Per-job env | Declared on the job in Plan | Yes | `.Env("GOFLAGS", "-mod=vendor")` |
| Secrets | Declared by reference in Plan | Reference only (not value) | `.WithSecret("AWS_KEY", "aws/prod/deploy")` |

**Secrets are never inlined.** Plan declares them by name; the controller
resolves them against its secret store at dispatch time and injects values into
the runner's environment. Plan-produced artifacts (the snapshot, logs of Plan
output) can be displayed safely because they only ever contain references.

```go
plan.Add("deploy", &DeployJob{Target: target}).
    Env("DOCKER_BUILDKIT", "1").
    WithSecret("AWS_ACCESS_KEY_ID", "aws/prod/deploy-key").
    WithSecret("AWS_SECRET_ACCESS_KEY", "aws/prod/deploy-secret").
    Needs("build")
```

**CacheKey interaction:** hash Plan-declared env vars (changing `GOFLAGS` must
invalidate the cache) but hash secret *references* only, not values (rotating a
secret should not spuriously invalidate cache). This is important and subtle;
the SDK should compute it automatically from the job spec rather than leaving
it to authors.

## SDK Helpers and Conventions

The first pipeline-conversion exercise surfaced these as essential primitives.
Ship them as part of the SDK; they make Plan code read cleanly and catch
common footguns.

### Plan construction (explicit form)
- `sparkwing.NewPlan()` - build a plan.
- `plan.Add(id, job) *Node` - register a job. Returns a node handle for further
  configuration.
- `node.Needs(ids ...string) *Node` - declare hard upstream dependencies.
- `node.NeedsIfPresent(ids ...string) *Node` - declare deps only on IDs that are
  actually in the plan. Essential for conditional upstreams like an
  optionally-present `aws-check` or `test` node.
- `node.Env(key, val) *Node` / `node.WithSecret(envKey, ref) *Node` - per-job
  configuration.
- `plan.ExpandFrom(id, func(out T) []Node)` - dynamic fan-out from an upstream
  output.
- `plan.Return(func(Results) Out) Out` - aggregate child outputs into the
  pipeline's typed output. (Name is placeholder; cleaner conventions welcome.)

### Plan construction (combinator sugar)

The explicit form is precise and handles complex DAGs well, but is verbose for
simple linear or fan-out pipelines. The SDK ships a combinator layer on top
that produces the same underlying graph with less ceremony. These are sugar
over `Add` + `Needs`; the two styles compose in the same plan.

- `plan.Parallel(nodes ...*Node) *Node` - run concurrently, no ordering between
  them. Returns a group handle that downstream `Needs` can reference.
- `plan.After(node *Node, deps ...*Node) *Node` - add edges without reordering
  calls; useful for cross-branch joins.
- `plan.SpawnAll(idPrefix string, items []T, job func(T) Job) *Group` - typed
  fan-out. Generates `idPrefix-<key>` nodes, one per item, returning a group
  handle. Downstream `Needs(group)` expands to all generated IDs.
- `plan.Skip(id, reason)` - declare a no-op node with a reason visible in the
  run record. Same observability as `Satisfied` but decided at Plan time (no
  runner spawned).

Before:

```go
build := plan.Add("build", &BuildJob{})
test  := plan.Add("test",  &TestJob{}).Needs("build")
lint  := plan.Add("lint",  &LintJob{}).Needs("build")
plan.Add("deploy", &DeployJob{}).Needs("test", "lint")
```

After (same graph):

```go
build := plan.Add("build", &BuildJob{})
checks := plan.Parallel(
    plan.Add("test", &TestJob{}).Needs(build),
    plan.Add("lint", &LintJob{}).Needs(build),
)
plan.Add("deploy", &DeployJob{}).Needs(checks)
```

Authors pick the style that fits the shape. The `Add` + `Needs` form is
authoritative for complex DAGs with cross-references; the combinator form is
the right default for linear and simple fan-out cases.

### Data flow: typed Refs on job input fields

Jobs declare their data dependencies as typed `Ref[T]` fields on their
struct. Plan wires them by threading `node.Output()` into downstream job
constructors. Data flow is visible at the declaration site, type-checked at
compile, and correctly wired across distributed runners.

```go
type BuildJob struct{ sparkwing.Base }

type BuildOut struct {
    Tag    string `json:"tag"`
    Digest string `json:"digest"`
}

func (j *BuildJob) Run(ctx context.Context) (BuildOut, error) {
    // do build
    return BuildOut{Tag: "v1.2.3", Digest: "sha256:abc"}, nil
}

type DeployJob struct {
    sparkwing.Base
    Build sparkwing.Ref[BuildOut]   // explicit typed input
}

func (j *DeployJob) Run(ctx context.Context) (DeployOut, error) {
    build := j.Build.Get()    // resolved at runtime, type-safe
    return deployImage(ctx, build.Tag, build.Digest)
}
```

In the Plan, wiring is a one-line declaration:

```go
build  := plan.Add("build",  &BuildJob{})
deploy := plan.Add("deploy", &DeployJob{
    Build: build.Output(),   // type-checked; compile breaks if BuildOut changes
})
```

Properties:

- **Visible at declaration.** Reader sees exactly which upstreams flow into
  which downstreams. No hidden `Upstream[T](ctx, "build")` ambient reads.
- **Typed end-to-end.** Changing BuildOut breaks every consumer at compile.
- **Implicit Needs.** The engine derives `deploy.Needs(build)` from the
  Ref. No duplicate declaration.
- **Correct under distributed execution.** Refs serialize through the
  controller; closure captures do not.

### Fan-out reads

SpawnAll exposes aggregated outputs for downstream consumers:

```go
builds := plan.SpawnAll("build", images, func(img string) sparkwing.Job {
    return &BuildImageJob{Image: img}
})

deploy := plan.Add("deploy", &DeployJob{
    Builds: builds.Outputs(),   // Ref[map[string]BuildOut], keyed by item
})
```

Typed, composable, correct under fan-in.

### Closure capture is forbidden

Capturing Go variables in Plan (`var tag string; ... tag = out.Tag; ...`) is
a compile-time footgun: it works locally (controller-local memory), breaks
silently when jobs run on separate runners. The SDK should make the pattern
a compile error; use Refs instead.

### RunContext
```go
type RunContext struct {
    Git           GitState          // Branch, Sha, ChangedFiles, ChangedSince
    TriggerEnv    func(string) string // env injected by the trigger
    Cluster       ClusterInfo       // name, labels, runner pool
    Invocation    InvocationInfo    // user, time, source (push, manual, schedule)
}
```
Typed, stable, agent-readable. Everything Plan needs to branch on lives here.

### Upstream access (inside Run)
- `sparkwing.Upstream[T](ctx, "build-controller") T` - typed fetch of a specific
  upstream output.
- `sparkwing.UpstreamByPrefix[T](ctx, "build-") map[string]T` - typed fetch of a
  set of upstream outputs, keyed by job ID. Essential for fan-out consumers
  (e.g., `DeployJob` consuming every `build-*` output).
- Access is enforced at dispatch: if a job reads an upstream that Plan did not
  connect, dispatch fails with a clear error.

### CacheKey helpers
- `sparkwing.Key(parts ...any) CacheKey` - compose a key from typed parts.
- `sparkwing.HashPath(path) string` - deterministic hash of a file or directory
  tree. Skips common garbage (`.git`, build output, `.sparkwing/`).
- `sparkwing.HashBlob(ref BlobRef) string` - use an upstream blob's digest as a
  key ingredient.
- Plan-level: auto-include declared `Env()` values, secret *references*, and
  input-struct fields via reflection. Reduces what authors write by hand and
  shrinks the drift surface.

### Outcomes
- `sparkwing.Success(out T)` - typed success.
- `sparkwing.Satisfied(out T, reason string)` - no-op success with audit
  breadcrumb.
- `sparkwing.Err(code, fmt, args...)` - structured error.

## Scaffolding and Authoring Ergonomics

The primary tension for adoption is authoring cost per pipeline. Solved by
tooling, not by model fragmentation.

### Design principle

One canonical code shape for all pipelines: a typed Go struct implementing
the Pipeline interface. No YAML-inline alternative. No fluent-builder
shortcut that produces anonymous pipelines. Verbosity at authoring time is a
tooling problem; solve it with scaffolding.

Rationale:
- Fragmented APIs create decision fatigue and two migration paths when
  pipelines grow.
- Anonymous-shortcut constructors (e.g. `Shell(name, cmd)`) break the
  "everything is a registered struct" mental model and make grep-finding
  a pipeline harder.
- Scaffolding can get the author's first keystroke down to "run one
  command" while keeping the canonical shape.

### Commands

- `sparkwing init` - scaffold a new `.sparkwing/` skeleton: `go.mod`,
  minimal `main.go`, empty `pipelines.yaml`. Run once per repo.
- `sparkwing job new <name>` - scaffold a trivial Execute-form pipeline.
  Prompts for description, optional output type, command. Generates the job
  file, updates `main.go` registration, adds the trigger entry to
  `pipelines.yaml`.
- `sparkwing pipeline new <name> --plan` - scaffold a DAG-form pipeline
  with a Plan stub. For multi-job pipelines.
- `sparkwing job add <pipeline> <job-name>` - inside a Plan-form pipeline,
  add a new Job struct file and wire it into the Plan.
- `sparkwing pipeline edit <name>` (future) - guided edits (add trigger,
  add secret, promote Execute to Plan). Lower priority; defer until
  scaffolding basics ship.

### Example: `sparkwing job new lint`

```
$ sparkwing job new lint
Description: Run go vet across all packages
Output type (optional, blank for none):
Command: go vet ./...
Trigger [push|pre-commit|manual|webhook]: pre-commit

Created .sparkwing/jobs/lint.go
Updated .sparkwing/main.go         (+1 line: registration)
Updated .sparkwing/pipelines.yaml  (+3 lines: trigger entry)

Try: wing lint
```

Generated `jobs/lint.go`:

```go
package jobs

import (
    "context"
    "github.com/sparkwing-dev/sparkwing/sparkwing"
)

type Lint struct{ sparkwing.BasePipeline }

func (p *Lint) Help() string { return "Run go vet across all packages" }

func (p *Lint) Execute(ctx context.Context) (any, error) {
    _, err := sparkwing.Sh(ctx, "go vet ./...")
    return nil, err
}
```

### Lines the author actually types

The right measuring stick for adoption cost is *how many lines does the
author write per pipeline?*, not *how many lines are in the repo?*. By that
measure:

- Trivial pipeline (lint): 1 line of Go thought (the Execute body), plus
  interactive scaffolding prompts. Competitive with Buildkite's 3-line
  YAML.
- Multi-step sequential pipeline: scaffolding generates the Plan stub;
  author fills in the Execute bodies. Each step is still 1-3 lines of
  thought.
- Rich pipeline (build-test-deploy): all the concept weight is real work:
  typed inputs, DAG shape, caching, deploy logic. No tooling shortcut will
  compress the essential complexity; scaffolding covers the boilerplate.

Greenfield cost (first pipeline in a new repo) is paid once via
`sparkwing init`, not per-pipeline. A repo adopting sparkwing pays the
~10-line scaffolding tax on day one; every subsequent pipeline is a single
scaffolding command.

### Why not YAML-inline

Considered and rejected. Losses:
- No compile-time checks on inline shell commands or references.
- `wing <pipeline>` invocation UX is weaker (no typed args, weaker help,
  weaker completion).
- Fragmented model: now there are two "kinds of pipelines" that behave
  differently for caching, outputs, composability.
- Migration path when outgrown: author has to rewrite from YAML to Go,
  paying the friction exactly when the pipeline is getting more complex.

Scaffolding achieves the same "first-pipeline-is-cheap" goal without any of
these costs.

### Why not `Shell()` constructor

Considered and rejected. An anonymous-pipeline constructor like
`sparkwing.Shell("lint", "go vet ./...")` saves a few lines but:
- Breaks "every pipeline is a findable struct in the jobs/ directory."
- Forces a migration (rewrite to struct) when the pipeline grows.
- Introduces two code shapes for no lasting benefit; scaffolding already
  gets to the same line count without the downsides.

## Node Modifiers

Every node in a Plan supports chained modifiers for cross-cutting concerns.
All modifiers are optional and independently composable. Common pipelines
use few; complex pipelines chain several.

### Control flow

- `Needs(nodes ...*Node)` - explicit upstream dependencies.
- `NeedsOptional(nodes ...*Node)` - depend only on nodes actually in the
  plan. Useful for conditionally-present upstreams.
- `SkipIf(ref, predicate)` - mark node as Skipped based on an upstream's
  output. Controller-evaluated after upstream completes; no runner spawns
  for skipped nodes. See the Dynamism Tiers section.
- `OnFailure(node *Node)` - run this node only if the parent fails.
- `OnSuccess(node *Node)` - run this node only if the parent succeeds.
- `ContinueOnError()` - downstream proceeds even if this node fails.
- `Optional()` - failure is logged as a warning, not a cascade.

### Resilience

- `Retry(n int, opts ...RetryOption)` - retry up to n times before
  failing. Options:
  - `RetryBackoff(d time.Duration)` - initial backoff between
    attempts; doubles each attempt (exponential).
  - `RetryAuto()` - re-dispatch the whole Node (a fresh runner
    invocation) instead of looping inside the runner. Right for
    infra-level flakes (spot preemption, transient network, OOM
    kills) where a clean runner boot is more likely to recover than
    a step-body re-run.
- `Timeout(d time.Duration)` - fail the node if it exceeds duration.

### Caching and concurrency

- `CacheKey(key sparkwing.CacheKey)` - content-addressed dedup. Cached
  result replaces execution.
- `Exclusive(lockKey string)` - only one node with this lock key runs at a
  time across the entire cluster, across pipelines. Serializes concurrent
  attempts. Distinct from CacheKey: Cache skips work, Exclusive queues it.

### Configuration

- `Env(key, value string)` - per-node environment variable.
- `WithSecret(envKey, ref string)` - inject a secret by reference at
  dispatch. Plan never sees the value.
- `RunsOn(labels map[string]string)` - route to runners matching labels.

### Lifecycle hooks

All hooks run as their own nodes for observability (visible in the DAG with
their own logs and status). Hook failures are distinguishable from body
failures.

- `BeforeRun(fn func(ctx) error)` - runs before the body; can fail the node.
- `AfterRun(fn func(ctx, result Result) error)` - runs after, regardless of
  outcome.
- `Webhook(url string, phase Phase)` - HTTP POST with the run record at a
  lifecycle event. Declarative alternative to a Go closure for common
  notification cases.

### Example: combining modifiers

```go
plan.Add("deploy", &DeployJob{Build: build.Output()}).
    Needs(test, security).
    CacheKey(sparkwing.Key("deploy", target, build.Output())).
    Exclusive("deploy-" + target.Name).
    BeforeRun(checkDeployWindow).
    AfterRun(notifySlack).
    OnFailure(rollbackStep).
    Webhook(datadogEventsURL, sparkwing.OnComplete).
    Retry(2).
    Timeout(10 * time.Minute)
```

Each modifier is optional. Trivial nodes have none; complex nodes chain
several. The DSL surface grows only when the node needs it.

## Cross-Pipeline References

Jobs in one pipeline can depend on outputs from another pipeline via typed
refs. Two patterns:

### Last-success reference (common)

```go
libBuild := sparkwing.PipelineRef("library-build", sparkwing.LastSuccess)
plan.Add("deploy", &DeployJob{
    Library: libBuild.Output[LibOut]("build"),
}).Needs(libBuild)
```

`PipelineRef` resolves to the most recent successful run of the named
pipeline. Optional `Within(duration)` restricts to recent runs. Fails at
dispatch if no eligible run exists.

Content-addressed blobs from that prior run are still resolvable via
`.Reader(ctx)` / `.LocalPath(ctx)`, enabling true cross-pipeline artifact
sharing.

### Synchronous await (less common, heavier)

```go
buildRun := plan.Add("await-build", &AwaitPipelineJob{
    Pipeline: "build-pipeline",
    Match:    sparkwing.TriggeredByCommit(run.Git.Sha),
    Timeout:  15 * time.Minute,
})
deploy := plan.Add("deploy", &DeployJob{
    Build: buildRun.Output[BuildOut]("build"),
}).Needs(buildRun)
```

Actively waits for another pipeline's run matching criteria. Slower than
last-success but enforces freshness ("wait for the build triggered by *this*
commit").

## Job Outcomes

Every job resolves to one of four outcomes. Three are returned by the job, one is set
by the controller.

| Outcome    | Set by     | When                                         | Downstream effect        |
|------------|------------|----------------------------------------------|--------------------------|
| Success    | job        | Ran work, succeeded                          | Downstream proceeds      |
| Failed     | job        | Ran work, failed                             | Downstream respects rule |
| Satisfied  | job        | Dispatched, self-determined no work needed   | Downstream proceeds      |
| Cached    | controller | CacheKey matched; dispatch skipped entirely  | Downstream proceeds      |

### Satisfied

```go
return sparkwing.Satisfied(outputs,
    "dedupe: matched completed run " + other.ID), nil
```

Requires a reason string. Carries outputs so downstream sees the job as if it
succeeded. Used when a runner started, did a cheap check, and decided the actual work
was unnecessary.

Distinct from Cached because the runner ran (even briefly) and the decision came from
user code, not the framework. Dashboard should style them differently so authors can
tell which layer saved them.

### Cached

Set by the controller via CacheKey lookup before any dispatch. No runner spawned.
Outputs are replayed from the cached prior run.

## CacheKey

Opt-in content-addressing per job.

```go
func (j *TestJob) CacheKey() sparkwing.CacheKey {
    return sparkwing.Key("test", j.Component, j.CommitSHA, j.GoModHash)
}
```

Controller behavior on dispatch:

1. **Completed match within TTL**: return Cached, replay outputs, do not dispatch.
2. **In-flight match**: coalesce. This job becomes an observer of the running
   instance; both resolve with the same outputs when it completes.
3. **No match**: dispatch normally, record the result under the key.

Properties:
- Deterministic: same key implies same decision.
- Composable across pipelines: a key hit in repo A's pipeline satisfies repo B's
  pipeline if the keys match. Large dedup win for monorepo-style setups.
- Author-declared: requires care. A wrong key is a cache-correctness bug. Document
  this as a footgun.

## Outputs: Values and Blobs

Two tiers of typed outputs.

### Values
Small, JSON-serializable, carried inline in the Result record. Stored directly in the
run database.

### Blobs
Anything large or binary. Stored via a pluggable blob backend. Jobs receive a typed
`BlobRef` and never handle bytes directly except at IO boundaries.

```go
type BuildOutputs struct {
    ImageTag    string             `json:"image_tag"`
    Binary      sparkwing.BlobRef  `json:"binary"`
    CoverageXML sparkwing.BlobRef  `json:"coverage_xml"`
}

func (j *BuildJob) Run(ctx context.Context) (sparkwing.Result, error) {
    binRef, err := sparkwing.PutBlob(ctx, "binary", reader)
    if err != nil { return nil, err }
    return sparkwing.Success(BuildOutputs{
        ImageTag: tag, Binary: binRef, CoverageXML: covRef,
    }), nil
}
```

Downstream access:

```go
build := upstream.BuildRef.Get(ctx)
r, err := build.Binary.Reader(ctx)        // stream
path, err := build.Binary.LocalPath(ctx)  // materialize to disk if a tool needs a path
```

### BlobRef properties

- Storage-agnostic. A BlobRef is `(backend-id, key, checksum, size, content-type)`.
- Pluggable backend. sparkwing-cache is the default; S3/GCS/Azure Blob are
  implementations of the same interface. Job code does not change across backends.
- Content-addressed. Key is a hash of contents. Two jobs producing the same bytes
  share one stored blob.
- Live-sharable within a pipeline run (normal job-to-job data flow).
- Cross-pipeline sharable via CacheKey matches (dedup).

### Retention / GC

Start simple:
- Default TTL (e.g., 7 days from last access).
- Explicit `sparkwing.PutBlob(ctx, name, r, sparkwing.Retain(sparkwing.Permanent))` for
  release artifacts.
- Refcounted GC deferred until blob volume justifies the complexity.

## Dashboard and UX Implications

- **Default view is pipelines (entrypoints), not jobs.** Drilling into a pipeline run
  expands its job tree. A jobs-only view exists for power-user debugging.
- **Run tree is the unit of grouping.** Every invocation has a root run ID; all
  spawned work (static, ExpandFrom, or fully emergent) hangs under it. Works for all
  three dynamism tiers uniformly.
- **Cached and Satisfied nodes render distinctly** from green-success, with the reason
  visible on hover.
- **ExpandFrom placeholders** render as "pending expansion" until the upstream job
  completes, then materialize their children in place.
- **Fully emergent subtrees** render progressively, like a live log stream but for
  DAG structure.

## Agent Reasoning

The explicit goal of this design is that an LLM reading a pipeline file can answer,
without running or simulating code:

- What triggers this pipeline?
- What jobs will run on trigger X with inputs Y?
- What outputs does each job produce (types and meanings)?
- Which subtrees are emergent and require reading job code?
- Where does this pipeline's work overlap with other pipelines (via CacheKey)?

Each layer of the model contributes:
- `Plan` is a pure-ish function returning a typed tree: readable like a recipe.
- Output structs are typed contracts: readable like an API.
- `CacheKey` is a declared equivalence: readable as "this work is fingerprintable by
  these inputs."
- `dynamic: true` is a clear flag that "here be dragons, read the code."

## Comparison to Other Systems

| System       | Static DAG   | Runtime shape          | Cross-run dedup        | Typed outputs | Agent-readable |
|--------------|--------------|------------------------|------------------------|---------------|----------------|
| GitHub Pipelines | Yes        | Skip-propagation only  | Manual (cache pipelines) | Strings        | Poor           |
| CircleCI     | Yes          | Similar to GHA         | Manual                 | Strings        | Poor           |
| Buildkite    | Yes + upload | Runtime YAML injection | Manual                 | Strings        | Moderate       |
| Airflow      | Yes          | Dynamic task mapping   | Datasets (limited)     | Typed (Python) | Moderate       |
| Dagster      | Yes          | Dynamic outputs        | Assets + materializations | Typed       | Good           |
| Temporal     | None         | Fully emergent         | None (app-level)       | Typed          | Poor for CI    |
| Bazel        | Yes          | No                     | Content-addressed      | Typed          | Poor for CI    |
| Sparkwing (proposed) | Per-run, from code | 3-tier | Content-addressed, opt-in | Typed with blobs | Primary goal |

Sparkwing's novel combination is: **code-first plans** (like Temporal/Dagster) that
**render statically per run** (like GHA), with **content-addressed cross-run dedup**
(like Bazel) and **typed blob outputs** (like Dagster), optimized for **agent
comprehension**. No existing system stacks all four.

## Preventing Plan / Execution Drift

The Plan declares a contract; Run implements it. Drift is any way Run can
violate the contract without loud failure. Defense is layered, roughly
compile-time through audit.

### Layer 1: Compile time (types)

- Job input fields: Plan constructs `&BuildImageJob{Image: ..., Registry: ...}`.
  Renames and type changes break Plan at compile.
- Job outputs: `func (j *BuildImageJob) Run(ctx) (sparkwing.Result[ImageRef], error)`
  with generics. Changing the output type breaks every
  `UpstreamByPrefix[ImageRef]` consumer at compile.
- Upstream accessors: typed by generic parameter. Can't silently drift.

### Layer 2: Registration (schema)

When sparkwing loads a binary, it walks all registered jobs and verifies:
- `Outputs()` schema matches the type Run actually returns.
- `Inputs()` schema matches the struct fields Plan populates.
- Declared errors (`Errors()`) are actually returned somewhere.

Mismatches fail at startup, not in production.

### Layer 3: Dispatch (wiring)

Before dispatching a job, the controller checks:
- Every upstream the job tries to read (via `Upstream[T]` / `UpstreamByPrefix[T]`)
  was declared in Plan via `.Needs(...)`.
- Declared upstreams actually exist in the plan graph.
- Secrets referenced by the job spec resolve in the secret store.

Violations fail fast with clear errors pointing to the Plan line and the Run
line that disagree.

### Layer 4: Plan snapshotting

The plan emitted for a given run is frozen into the run record. Execution
follows the snapshot, not re-invocations of Plan. Benefits:
- Deterministic execution even if Plan is re-entered.
- Divergent plans across runs are visible in run history as a diff.
- Partial reruns (resume from failed node) replay the frozen plan, not a
  newly-computed one.

### Layer 5: Runtime guards

- Secrets not declared in Plan are not in the runner's environment. Attempts
  to read them return empty, making coupling failures loud instead of silent.
- Jobs that attempt to spawn children without `dynamic: true` in their Plan
  declaration get rejected by the runner-to-controller protocol.

### Layer 6: CacheKey audit

CacheKey correctness is the one drift vector the type system cannot catch - an
author-declared key that fails to hash a real input produces wrong cache hits
silently. Three mitigations, in increasing strength:

1. **Discipline and docs.** Encourage the pattern of computing the hash set in a
   helper called by both CacheKey and Run. Warn that CacheKey is
   author-responsible.
2. **Auto-derived keys.** Where possible, derive CacheKey from declared inputs
   (struct fields, `Env()` values, secret references, explicit
   `InputPath("./src")` declarations). Authors write less key code; framework
   hashes correctly by default.
3. **Periodic re-run audits.** Sample a small percentage of cache hits and
   actually re-run them, comparing outputs. This is Bazel's approach. Detects
   CacheKey bugs in production without paying the full rebuild cost. Opt-in per
   pipeline, with alerting when divergence is found.

### What this does not catch

IO the framework cannot see: reading `/etc`, calling an external API whose
response affects outputs, time-dependent logic, clock skew, nondeterministic
compilers. Bazel solves this with hermetic sandboxing; sparkwing will not, at
least not initially. The honest advice is: declare inputs explicitly, avoid
ambient IO, and enable cache audits for pipelines where correctness bugs would
be expensive.

## Honest Tradeoffs

Where this design wins (vs incumbents):
- Conditional DAG shape without skip gymnastics.
- Cross-pipeline dedup via CacheKey + content-addressed blobs.
- Typed outputs as enforceable contracts.
- Satisfied as a first-class, reasoned outcome.
- Agent-readability as a primary design axis.

Where it is worse:
- No ecosystem. GHA's pipeline marketplace is a moat.
- Higher learning curve. Plan/CacheKey/Satisfied/BlobRef/ExpandFrom is a lot of
  concepts to learn.
- Go as the pipeline language is polarizing and narrows the audience.
- Self-hosted tax: ops cost vs GHA's hosted runners.
- No hermetic build guarantees: author-declared CacheKeys are a footgun.

Where it is unproven (only real use reveals this):
- Does Plan() feel good in practice, or annoying to iterate on?
- Do users conflate Satisfied and Cached?
- Do agents actually reason better about this vs a GHA workflow?
- Does content-addressed blob storage survive real workload non-determinism?

## Open Questions

1. **Plan inputs.** What types does Plan receive? Pipeline inputs (from trigger
   payload) seem obvious. What about git state (branch, sha, changed files)?
   Environmental context (cluster name, runner labels)? Define the input struct
   shape carefully; it is a versioned contract.
2. **Plan re-entry.** When ExpandFrom materializes, is that a full re-run of Plan
   with more info, or just a local expansion? Local expansion is cleaner; full
   re-run is more powerful. Probably local expansion for V1.
3. **Named references vs content hashes for blobs.** Do we want `latest-main-build`
   pointers alongside content-addressed refs? Git's model (objects immutable, refs
   mutable) maps naturally.
4. **CacheKey scope.** Is the cache global, per-repo, per-pipeline, or
   configurable? Global maximizes dedup but risks cross-team bleed. Per-repo is
   safer default.
5. **Plan determinism.** Do we require Plan to be pure? If Plan reads the
   filesystem, reruns may diverge. Options: forbid IO, allow IO with a warning,
   record Plan outputs as part of the run artifact for replay.
6. ~~**Trigger definitions.** Still YAML? Still Go? Tiny registration file just for
   triggers? Pick one and commit; triggers are the last bastion of config.~~
   **Resolved:** minimal YAML registry (`pipelines.yaml`) for triggers and name
   mapping only. See the Triggers section.
7. **Plan failure.** What if Plan itself errors? Pipeline never dispatches and
   fails loudly at the controller level. Surface this prominently.
8. **Satisfied + ExpandFrom interaction.** If an ExpandFrom parent returns
   Satisfied, do the children still materialize? Probably no (nothing to expand),
   but needs thought.
9. **Pipeline output aggregation.** The `plan.Return(func(Results) Out)`
   convention surfaced in the first conversion exercise and felt awkward.
   Alternatives: a dedicated aggregator job at the end of the plan; reflection
   on upstream outputs to build the Outputs struct; explicit `plan.Bind(...)`
   calls per output field. Needs a second pass.
10. **Auto-derived CacheKey.** How much of CacheKey can the framework infer from
    declared job spec (input struct, `Env()`, `WithSecret()`, `InputPath()`)
    without authors writing `Key(parts...)` manually? Target: `CacheKey()` is
    rarely needed; default behavior is correct. Investigate scope.
11. **Cache audit sampling.** What rate, what triggers an audit, and how is
    divergence reported? Bazel runs audits on CI; sparkwing probably wants
    per-pipeline opt-in with tunable rate.

## Migration Path (rough)

This is a big change. Proposed phasing:

1. **Land Plan() and typed outputs.** Keep existing YAML triggers as a compatibility
   layer that generates a one-node Plan. New pipelines opt into Plan directly.
2. **Add Satisfied outcome.** Additive; existing code unaffected.
3. **Add CacheKey.** Opt-in per job. Ship with opt-in mode until it proves stable.
4. **Introduce BlobRef.** Migrate cache service as the default backend; S3 as a
   pluggable alternative.
5. **Add ExpandFrom.** Covers the matrix-over-runtime-list use case.
6. **Deprecate root-job-spawns-jobs as the default pattern.** Keep it as
   `dynamic: true` escape hatch only.

Each phase should land with at least one real pipeline migrated to prove the
ergonomics. If a phase makes a real pipeline harder to read or longer to write, stop
and rethink.

## First Experiment

Before committing to the full design, do this:

1. Pick one existing sparkwing pipeline (ideally one with conditional logic).
2. Sketch it in the new model: Plan function, outputs, CacheKey where obvious.
3. Compare: is it shorter? more readable? easier for Claude to summarize?
4. Hand Claude the two versions and ask for the same modification. Measure which
   is faster and more accurate.

If step 3 is a clear win and step 4 is at least neutral, greenlight phase 1. If
not, the design needs more work before code does.

## What This Doc Is Not

- A spec. Interfaces shown are sketches; real signatures will evolve.
- A commitment. Every section is up for revision.
- A migration plan for production. The phasing above is rough sequencing, not a
  timeline.

## Related Work in Sparkwing

- `pkg/sparkwing/` - current SDK; Plan/Satisfied/CacheKey additions live here.
- `cmd/sparkwing-controller/` - CacheKey lookup, Plan evaluation, run-tree
  grouping.
- `sparkwing-cache` - default blob backend; needs a pluggable interface.
- `web/` - DAG rendering updates for Satisfied/Cached, ExpandFrom placeholders,
  progressive materialization.
- `FUTURE_PRODUCT.md` - product-level context; this doc is the technical
  realization of the CI/CD pillar described there.
