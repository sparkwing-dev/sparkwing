# Pipelines

Pipelines are the core of sparkwing. They define what happens when you run
`sparkwing run <name>` (or `sparkwing pipeline run <name>`). This page is the user-facing
tour; for the full Go SDK reference see [sdk.md](sdk.md), and for the rules
that keep a `Plan` pure and deterministic -- enforced by `sparkwing pipeline
lint` -- see [authoring-pipelines.md](authoring-pipelines.md).

> **Host requirements.** Pipelines that call `sparkwing.Bash` shell
> out to `bash` on the runner host. macOS and Linux have this by
> default. On Windows, install
> [Git for Windows](https://git-scm.com/download/win) and run pipelines
> from the Git Bash terminal it ships -- `sparkwing.Exec` (no-shell,
> arg-vector form) works without it. The cluster-mode `sparkwing-runner`
> Service is Linux/macOS only; Windows users dispatch pipelines to
> remote runners or to a remote cluster.

## Pipeline registry

The `pipelines:` block in `.sparkwing/sparkwing.yaml` is the registry of
every pipeline in the repo (pipelines plus commands); the block holds both
kinds. Each entry is a list item with a `name:`.

```yaml
# .sparkwing/sparkwing.yaml
pipelines:
  - name: build-deploy
    entrypoint: BuildDeploy
    description: Build and deploy the app
    on:
      push:
        branches: [main]

  - name: release
    entrypoint: Release
    description: Cut a release
    # no on: -> command, manual-only
```

Each entry has (these are the only valid keys; an unknown field is a
hard parse error):

- **name** - the pipeline name (`sparkwing run build-deploy`); must equal the `Register("name", ...)` string
- **entrypoint** - the Go pipeline struct type implementing it (required); equals the struct name
- **description** - one-line summary surfaced by `sparkwing pipeline list`
- **on** - declarative trigger block: `push` (branches/paths), `pull_request` (actions/branches), `schedule` (cron expression, UTC; declarative -- recorded, not evaluated), `webhook`, `pre_commit`, `pre_push`, `post_commit`. Absent means "manual only" (a command).
- **guards** - gate dispatch on profile, args, and git branch (`reject` / `require` token lists)
- **args** - per-arg default values, keyed by CLI flag name
- **profile** - the project profile this pipeline uses (from the `profiles:` map)
- **requires** - runner-label requirements for every job (e.g. `[local]` pins execution to this machine)
- **hidden** - omit from `pipeline list` (still invocable by exact name)

For the complete schema -- every top-level key, pipeline field, and
trigger field with types -- see the generated [config-reference.md](config-reference.md).

## Triggers

Trigger types live under `on:`:

```yaml
# .sparkwing/sparkwing.yaml
pipelines:
  # Fires on a git push the controller receives via webhook
  - name: build
    entrypoint: Build
    on:
      push:
        branches: [main]                 # declarative: records intent, not gated on
        paths: ["*.go", "go.mod"]        # declarative: records intent, not gated on

  # Run on every pull request (checks out the PR head)
  - name: pr-gate
    entrypoint: PRGate
    on:
      pull_request:
        branches: [main]                 # declarative: records intent, not gated on

  # Declarative path: the controller exposes POST /webhooks/github/{pipeline};
  # this path is recorded, not routed
  - name: review
    entrypoint: Review
    on:
      webhook:
        path: /review

  # Cron in UTC, declarative -- nothing evaluates it; invoke with
  # `sparkwing run nightly` or an external scheduler
  - name: nightly
    entrypoint: Nightly
    on:
      schedule: "0 2 * * *"
```

`branches` / `paths` / `actions` record intent: the controller does not
read your `sparkwing.yaml`, so it dispatches whichever pipeline the
webhook URL names. Scope a trigger by pointing its GitHub webhook only
at that pipeline's URL -- see [hooks](hooks.md).

Webhook delivery is handled by the controller - see
`POST /webhooks/github/{pipeline}` in [api](api.md). Git hooks are not
installed automatically -- declaring an `on: pre_commit` / `pre_push` /
`post_commit` trigger does nothing until you run `sparkwing pipeline hooks
install`, which writes the hook files into `.git/hooks/`; see
[hooks](hooks.md) for context.

## The two-layer model

Sparkwing has two DAG layers, and almost every pipeline-authoring choice
is a layer choice. Internalize this before reading the recipes below.

- **Plan / Job** is the *outer* DAG - units of dispatch. Each Job runs
  in its own process: a separate pod in cluster mode, a separate
  invocation of the pipeline binary in local mode. Nodes carry the
  dispatch envelope -
  `Retry`, `Timeout`, `OnFailure`, `Memoize`, `Requires`, `BeforeRun` /
  `AfterRun`, `Approval` gating - because each Job *is* the unit the
  scheduler can retry, time out, or route to a labeled runner.
- **Work / WorkStep** is the *inner* DAG - units of work *within* one
  Job's runner. Steps share the Job's runner, filesystem, environment,
  and ctx. They have `Needs` for ordering and `SkipIf` for predicates;
  they do **not** carry Job-only modifiers (Retry, Timeout, ...).
  Promote a step to a Job via `JobSpawn` if it needs one.

Each pipeline implements `Plan(ctx, plan *sw.Plan, in T, rc sw.RunContext) error`
which registers nodes on the passed-in `*Plan` (the outer DAG). Each Job carries a `Job` whose `Work()`
method returns the inner DAG. Both DAGs are materialized at Plan-time

- the orchestrator walks the entire reachable tree (including spawn
targets) before any dispatch begins, so `pipeline explain` and the
dashboard render the full structure before the run starts.

### Cost grid

| API | Layer | Cardinality | Cost |
|---|---|---|---|
| `sw.Job(plan, id, x)` | Plan | one, declared at Plan-time | normal node |
| `sw.JobFanOut(plan, name, items, fn)` | Plan | many, items in hand at Plan-time | normal nodes; one per element |
| `sw.JobFanOutDynamic(plan, name, source, fn)` | Plan | many, source's runtime output | source runner exits before fan-out - no stranded compute |
| `sw.Step(w, id, fn)` | Work | one, in-process unit of work | one logging frame, ordered/parallel via Needs |
| `sw.JobSpawn(w, id, job)` | Work | one, decided mid-Work | spawning runner stays suspended until child completes |
| `sw.JobSpawnEach(w, items, fn)` | Work | many, mid-Work fan-out | spawning runner stays suspended across all children |

The verb tells you the cost. The Plan-layer `Job*` adders are cheap;
the Work-layer `JobSpawn*` adders flag the layer jump and the
suspended-runner cost. Reach for `JobSpawn` when you genuinely need
Job-only modifiers (Retry, Requires, distinct runner) on a unit
decided mid-execution; otherwise stay inside Work.

## Trivial single-step jobs

For pipelines that are one closure with no DAG, pass the function
directly to `sw.Job` -- no struct, no wrapper:

```go
type Lint struct{ sparkwing.Base }

func (p *Lint) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
    sw.Job(plan, rc.Pipeline, p.run)
    return nil
}

func (p *Lint) run(ctx context.Context) error {
    if err := sparkwing.Bash(ctx, "gofmt -l .").MustBeEmpty("formatting drift"); err != nil {
        return err
    }
    _, err := sparkwing.Bash(ctx, "go vet ./...").Run()
    return err
}

// In .sparkwing/main.go:
//     sparkwing.Register[sparkwing.NoInputs]("lint", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &Lint{} })
```

`sw.Job`'s third argument is `any`: a `func(ctx context.Context)
error` is wrapped into an internal Workable, while a struct
implementing `Work(w *Work) (*WorkStep, error)` registers as a
multi-step Job. Reflection picks the right form at register time.

For typed-output Jobs (downstream nodes read the value via `Ref[T]` /
`RefTo[T]`), define a struct that embeds `sparkwing.Produces[T]` and
return the typed step from `Work`:

```go
type Build struct {
    sparkwing.Base
    sparkwing.Produces[BuildOut]
}

func (j *Build) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
    return sw.Step(w, "run", j.run), nil
}

func (j *Build) run(ctx context.Context) (BuildOut, error) {
    return BuildOut{Tag: "app:sha-abc"}, nil
}

build := sw.Job(plan, "build", &Build{})
buildRef := sparkwing.RefTo[BuildOut](build)
sw.Job(plan, "deploy", &Deploy{Build: buildRef}).Needs(build)
```

## Multi-step jobs

For jobs whose body is more than one logical phase, implement
`Workable` yourself. The struct's `Work(w *Work) (*WorkStep, error)`
method registers steps onto the passed-in `*Work` and returns the
result step (or `nil` for an untyped Job). Each `sw.Step` is a unit
of work; `Needs` declares ordering.

```go
type Build struct{ sparkwing.Base }

func (j *Build) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
    fetch    := sw.Step(w, "fetch",    j.fetch)
    validate := sw.Step(w, "validate", j.validate)
    sw.Step(w, "compile", j.compile).Needs(fetch, validate)
    return nil, nil  // untyped Job; no result step
}

func (j *Build) fetch(ctx context.Context) error    { return j.gitFetch(ctx) }
func (j *Build) validate(ctx context.Context) error { return j.checkGoMod(ctx) }
func (j *Build) compile(ctx context.Context) error  { return j.goBuild(ctx) }
```

The DAG is built entirely from `.Needs()` chains. For sequential
steps, chain Needs directly; there is no separate `Sequence`
combinator:

```go
func (j *Deploy) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
    a := sw.Step(w, "render-manifests", j.render)
    b := sw.Step(w, "argo-sync",        j.sync).Needs(a)
    sw.Step(w, "verify",                j.verify).Needs(b)
    return nil, nil
}
```

For named clustering of related steps -- the dashboard's Work view
folds members under one collapsible header -- use `sw.GroupSteps`:

```go
func (j *Deploy) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
    fetch := sw.Step(w, "fetch", j.fetch)

    safety := sw.GroupSteps(w, "safety",
        sw.Step(w, "lint",    j.lint).Needs(fetch),
        sw.Step(w, "secscan", j.secscan).Needs(fetch),
        sw.Step(w, "vet",     j.vet).Needs(fetch),
    )

    return sw.Step(w, "deploy", j.deploy).Needs(safety), nil
}
```

`*StepGroup` is both a `Needs` target (downstream steps that
`Needs(group)` depend on every member) and a UI cluster. Initial
modifiers mirror what `*WorkStep` has today (`Needs`, `SkipIf`); each
applies to every member.

### Typed step output

For the common case -- a Job with a single typed step whose return
value IS the Job's output -- declare the step with a typed signature
and return it from `Work`:

```go
func (j *Build) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
    return sw.Step(w, "compile", j.compile), nil
}
```

`sw.Step`'s third argument is `any`: pass either a
`func(ctx context.Context) error` (untyped) or a
`func(ctx context.Context) (T, error)` (typed). Reflection at
register time stores the step's output type.

For Works with multiple typed steps where downstream steps inside the
same Work read intermediate values, use `sw.StepGet[T](ctx, step)`
inside the consuming step's body:

```go
func (j *Deploy) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
    tags := sw.Step(w, "compute-tags", func(ctx context.Context) (Tags, error) {
        return loadTags(ctx)
    })
    return sw.Step(w, "publish", func(ctx context.Context) error {
        return publish(ctx, sw.StepGet[Tags](ctx, tags))
    }).Needs(tags), nil
}
```

`StepGet` mirrors Plan's `Ref[T].Get(ctx)`. It exists as a free
function because Go forbids generic methods.

### Inner step skip

`step.SkipIf(predicate)` skips a single step without aborting the
Work. Multiple `SkipIf` calls accumulate with OR semantics.

```go
sw.Step(w, "publish", j.publish).
    Needs(buildOut).
    SkipIf(func(ctx context.Context) bool { return os.Getenv("DRY_RUN") == "1" })
```

## Plan-layer fan-out

Two type-safe verbs cover the Plan-layer fan-out cases. Both return a
`*JobGroup` whose `name` becomes a collapsible cluster in the dashboard
and a single `Needs(group)` target downstream.

### Static: `JobFanOut` (slice in hand at Plan-time)

`sw.JobFanOut[T]` registers one Job per element of a slice
already known when `Plan()` runs:

```go
images := sw.JobFanOut(plan, "image-builds", Images, func(img imageSpec) (string, any) {
    return "build-" + img.Name, &BuildImage{Image: img}
}).Needs(webBuild, discover).Retry(2)

sw.Job(plan, "artifact", &Artifact{}).Needs(images)
```

The chained `.Needs(...)` / `.Retry(...)` apply to every member; see
*Group modifiers* below.

### Runtime: `JobFanOutDynamic` (slice produced by an upstream Job)

`sw.JobFanOutDynamic[T]` materializes one Plan-level Job per
element of an upstream typed Job's output slice. Each fan-out child is
a fresh Job with its own dispatch envelope:

```go
type ListShards struct {
    sparkwing.Base
    sparkwing.Produces[[]string]
}

func (j *ListShards) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
    return sw.Step(w, "run", j.run), nil
}

func (j *ListShards) run(ctx context.Context) ([]string, error) {
    return loadShards(ctx)
}

shards := sw.Job(plan, "list-shards", &ListShards{})

sw.JobFanOutDynamic(plan, "shard-work", shards, func(shard string) (string, any) {
    return "process-" + shard, &ProcessShard{Shard: shard}
})
```

`JobFanOutDynamic` runs at Plan-time-after-source: the source Job runs
and exits, *then* the orchestrator builds children from the resolved
output. The source runner is not held during the fan-out - no
stranded compute.

### Group modifiers

Every chainable `*JobNode` modifier has a `*JobGroup` twin: the call
delegates to each member and returns the same `*JobGroup` for chaining.
The generated [sdk-reference.md](sdk-reference.md) lists the current
set. Two carve-outs: `OnFailure` is intentionally per-Job, since
group-level recovery has unclear semantics; and `Memoize` on a group is
what the `group-cache-shared` lint rule rejects -- one key function
across N members makes them share a single cache entry and replay each
other's results. Key each member instead, by ranging over
`group.Members()`; see [authoring-pipelines.md](authoring-pipelines.md).

## Layer escape: JobSpawn

When a unit of work decided *mid-Work* needs a Job-only modifier
(Retry, Requires, distinct runner, separate cache key), promote it via
`sw.JobSpawn`. The spawning runner suspends until the spawned Job
completes:

```go
func (j *ScanJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
    analyze := sw.Step(w, "analyze", j.analyze)
    scan := sw.JobSpawn(w, "compliance", &ComplianceJob{}).Needs(analyze)
    sw.Step(w, "publish", func(ctx context.Context) error {
        return publish(ctx, scan)
    }).Needs(scan)
    return nil, nil
}
```

The spawned Job id is namespaced as `parent/spawnID`
(e.g. `scan/compliance`) so logs and the run history don't collide.

A spawned Job runs inside its parent node's own process -- a local node
process, or a pod. It runs under the admission lease its parent already
holds, so it is not charged against host capacity a second time, and
concurrent children are capped the way the dispatcher caps nodes. It
still records its own node row under `parent/spawnID`, its own logs,
metrics, and output, and a `spawn_dispatched` event on the parent. A
child whose `WhenRunner` labels the executing runner does not advertise
is skipped, exactly as a planned node would be.

`sw.JobSpawnEach(w, items, fn)` is the cardinality-many variant. The
generator runs once Needs are satisfied; each returned `(id, Job)`
pair becomes a fresh Plan node. The spawning runner stays suspended
across the entire fan-out:

```go
sw.JobSpawnEach(w, targets, func(target string) (string, any) {
    return "deploy-" + target, &Deploy{Target: target}
}).Needs(buildStep)
```

Reach for spawn primitives sparingly. Each call holds a runner slot
during the child's lifetime; a deeply nested spawn chain pins one slot
per layer. The `JobSpawn*` prefix flags the layer jump (and the
suspended-runner cost) at the call site.

## Modifier scope discipline

| Modifier | Layer | Notes |
|---|---|---|
| `Retry(n, opts...)` | Plan only | `RetryBackoff(d)` + `RetryAuto()` options; `RetryAuto` re-dispatches the whole Job |
| `Timeout` | Plan only | per-attempt cap |
| `OnFailure(id, job)` | Plan only | constructs a detached recovery node fired on parent failure |
| `Memoize` | Plan only | content-addressed result memoization |
| `Requires(labels...)` | Plan only | scheduler routes by runner label |
| `BeforeRun` / `AfterRun` | Plan only | runner lifecycle hooks |
| `Approval` | Plan only | gates dispatch on a human decision |
| `Inline()` | Plan only | bypass the runner entirely |
| grouping | Plan only | free function `sw.GroupJobs(plan, "name", nodes...)`; a named cluster in the DAG view and a `Needs` target -- there is no `.Group()` modifier |
| `Needs` | both | ordering inside its layer |
| `SkipIf` | both | skip predicate |
| parallel failures | Work only | `w.ParallelFailures(sw.FailFast)` or `sw.CollectAll` |
| `Finally()` | Work only | cleanup after declared dependencies terminate, including failed or cancelled ones |
| typed output | both | `Ref[T]` (Job) / `*WorkStep` returned from `Work` (Work) |

A Step that needs Retry / Timeout / Requires is the canonical signal
to promote it to a Job via `sw.JobSpawn`.

## Scheduling modifiers

### `.Inline()`

Marks a Job to run on the dispatcher's own host instead of being
handed to the configured Runner, so no pod / warm-runner spin-up cost
is paid.

```go
sw.Job(plan, "setup", &Setup{}).Inline()
sw.Job(plan, "summarize", &Summarize{}).Needs(deploys).Inline()
```

Reach for it on genuinely lightweight glue (setup checks, fan-in
summaries) that would otherwise burn seconds of runner boot for a few
hundred ms of work.

`Inline()` says where the job runs, not what it shares. In a local run
it is still its own process, like every other job -- what it skips is
the cluster, not the process boundary. Dispatched to a cluster runner
it runs inside the dispatcher itself, and there it is **not** a general
"faster" knob: it shares the dispatcher's goroutine pool, so a long
inline job delays every other node's scheduling. Keep such jobs under a
second or two.

`.Inline()` on an approval gate panics. `.Requires` labels are ignored
for inline nodes.

### Dynamic nodes

A node whose downstream work is runtime-variable is *dynamic*:
`JobFanOutDynamic` source nodes are auto-marked dynamic at plan
finalization. `plan.IsDynamicNode(id)` reports it and the plan preview
shows `[dynamic]`, so reviewers know to inspect the run for the actual
child nodes rather than expecting the full shape at plan time.

### `GroupJobs(plan, "name", ...)`

Pure UI annotation. The dashboard folds nodes that share a group under
one collapsible header; the scheduler, cache, retry, and dependency
semantics are unchanged.

```go
sw.GroupJobs(plan, "safety",
    sw.Job(plan, "schema-check", &SchemaCheckJob{}),
    sw.Job(plan, "security-scan", &SecurityScanJob{}),
)
```

## Eager Plan-time materialization

Every Job's `Work()` runs during the Pipeline's `Plan()`, not at
runner dispatch. The orchestrator walks the entire reachable nested
DAG - including transitive `JobSpawn` targets - before any node runs.
What stays runtime-dynamic is bounded:

- Which Nodes execute (Plan-time branching on `in`, Job `SkipIf`).
- Which Steps execute (intra-Work `SkipIf`).
- Whether each `JobSpawn` fires and with what arguments.
- `JobFanOutDynamic` cardinality (count and keys come from the source's
  runtime output; the per-item shape is known).

Because the structure is reachable from source, `sparkwing pipeline
explain --name X` and the dashboard render the full Plan -> Job ->
Work -> Step tree before anything runs. The dashboard's per-Job card
exposes a collapsible **Work** section showing inner steps and spawn
declarations as placeholders (filled in once spawned children
appear).

The cost-grid table above is the load-bearing artifact for an agent
reader - load it before designing a multi-Job pipeline.

## Cache

`.Memoize(key, TTL(...))` turns a Job into a content-addressed cache
entry. The orchestrator computes the key after upstream deps complete,
looks it up across runs, and short-circuits the job on a hit, replaying
the cached output without running. Misses execute normally and record
`(key -> output)` on success. Identical content computing at the same
time dedupes automatically.

```go
sw.Job(plan, "build", &Build{}).Memoize(
    func(ctx context.Context) sparkwing.CacheKey {
        return sparkwing.Key("build", "v1")
    },
    sparkwing.TTL(24*time.Hour),
)
```

`sparkwing.Key(parts...)` hashes arbitrary parts into a stable string --
use it rather than hand-concatenating. Return `sparkwing.NoCache` from
the key fn to opt out for a particular invocation, useful when inputs
are non-deterministic. See [caching.md](caching.md) for the full model.

Caching is content only. To bound how many nodes run at once -- a mutex,
a semaphore, a deploy gate -- use `.Concurrency(group)`; see
[sdk.md](sdk.md#concurrency) and [scheduling](scheduling.md).

Do not cache nodes whose effect is the side effect itself (deploys,
notifications, gitops commits). Caching replays the return value, not
the external world - a "cached" deploy did not actually deploy
anything. Cache pure builds, test runs against content-addressed
sources, and artifact packaging; gate external side effects with
`.Needs` on the cached Job.

## Approval gates

Pause a run and wait for a human decision by registering a gate via
`sw.JobApproval`. The orchestrator routes approval nodes through the
approval-waiter flow, flipping the Job to `approval_pending`,
writing an approvals row, and blocking until the dashboard, CLI, or
the configured timeout resolves it.

```go
approve := sw.JobApproval(plan, "approve-prod", sw.ApprovalConfig{
    Message:  fmt.Sprintf("Promote %s to prod?", git.SHA),
    Timeout:  2 * time.Hour,
    OnExpiry: sw.ApprovalFail,
}).Needs(integStg)
sw.Job(plan, "deploy-prod", &Deploy{Env: "prod"}).Needs(approve)
```

`sw.JobApproval` returns `*ApprovalGate`, a narrower handle than
`*Job` -- only the modifiers that make sense for a human gate are
methods on it (`Needs`, `NeedsOptional`, `OnFailure`, `BeforeRun`,
`AfterRun`, `SkipIf`, `Optional`, `ContinueOnError`). Modifiers
that don't apply to gates -- `Retry`, `Timeout`, `Memoize`, `Requires`,
`Inline` -- are physically absent, so misuse is a compile error
rather than a runtime panic / silent no-op.

`ApprovalConfig` fields:

- `Message` - operator-facing prompt shown in the dashboard banner
  and CLI list output. Compose with `fmt.Sprintf` if you need to
  weave in run-time values.
- `Timeout` - maximum wait before the waiter writes a `timed_out`
  resolution itself. Zero (the default) means never time out.
- `OnExpiry` - one of `sw.ApprovalFail` (default), `sw.ApprovalDeny`,
  or `sw.ApprovalApprove`. Unrecognized values panic at plan time.
  Named `OnExpiry` (not `OnTimeout`) so it doesn't read like
  `Job.Timeout()`, which is unrelated.

Resolution paths:

- Dashboard: any node in `approval_pending` renders an indigo banner
  with a comment textarea and Approve / Deny buttons.
- CLI: `sparkwing runs approvals approve --run ID --node ID`,
  `sparkwing runs approvals deny ...`.
- Programmatic: `POST /api/v1/runs/{run}/approvals/{node}` with
  `{"resolution":"approved","comment":"..."}`. The approver is recorded
  from the authenticated principal.

**Limitation - `sparkwing` runs cannot survive a terminal close mid-approval.**
In local (`sparkwing run <pipeline>`) mode the orchestrator lives in the same
process as the CLI invocation. Close the terminal while a gate is
waiting and the waiter goroutine dies with it: the approvals row
stays on disk and can still be resolved from the dashboard, but
nothing transitions the Job out of `approval_pending` and the run
stays `running` forever. Workaround: re-run, or keep `sparkwing
dashboard start` up so the dispatcher lives in the long-lived local
web server. Cluster mode has the same property via the controller
pod.
