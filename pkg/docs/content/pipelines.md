# Pipelines

Pipelines are the core of sparkwing. They define what happens when you run
`wing <name>` (or `sparkwing run <name>`). This page is the user-facing
tour; for the full Go SDK reference see [sdk.md](sdk.md).

> **Host requirements.** Pipelines that call `sparkwing.Bash` shell
> out to `bash` on the runner host. macOS and Linux have this by
> default. On Windows, install
> [Git for Windows](https://git-scm.com/download/win) and run pipelines
> from the Git Bash terminal it ships -- `sparkwing.Exec` (no-shell,
> arg-vector form) works without it. The cluster-mode `sparkwing-runner`
> Service is Linux/macOS only; Windows users dispatch pipelines to
> remote runners or to a remote cluster.

## Pipeline registry

`.sparkwing/pipelines.yaml` is the registry of every pipeline in the repo
(pipelines plus commands). It is named `pipelines.yaml` for historical
reasons; the file holds both kinds.

```yaml
build-deploy:
  description: Build and deploy the app
  on:
    push:
      branches: [main]
  tags: [ci, deploy]

release:
  description: Cut a release
  # no on: -> command, manual-only
```

Each entry has:

- **name** - the pipeline name (`wing build-deploy`), from the YAML map key
- **description** - one-line summary surfaced by `sparkwing pipeline list`
- **on** - declarative trigger block. Absent means "manual only" (a command).
- **tags** - labels for filtering and grouping
- **env** - environment variables passed to the pipeline
- **secrets** - cluster-stored secrets to surface
- **runs_on** - scheduling constraints for runner selection
  (see [scheduling](scheduling.md))
- **group** - section header for `sparkwing pipeline list` and tab complete
- **hidden** - omit from `pipelines list` (still invocable by exact name)

## Triggers

Trigger types live under `on:`:

```yaml
# Run on git push to main
build:
  on:
    push:
      branches: [main]
      branches_ignore: [dependabot/*]  # optional exclusion
      paths: ["*.go", "go.mod"]        # optional path filter
      paths_ignore: ["docs/*"]         # optional path exclusion

# Run on pull requests
review:
  on:
    pull_request:
      branches: [main]
      types: [opened, synchronize]
      labels: [deploy]                 # optional label filter
      paths_ignore: ["*.md"]

# Scheduled (cron)
nightly:
  on:
    schedule: "0 2 * * *"
```

Webhook delivery is handled by the controller - see
`POST /webhooks/github/{pipeline}` in [api](api.md). Git hooks are
**not** installed by sparkwing; see [hooks](hooks.md) for context.

## The two-layer model

Sparkwing has two DAG layers, and almost every pipeline-authoring choice
is a layer choice. Internalize this before reading the recipes below.

- **Plan / Node** is the *outer* DAG - units of dispatch. Each Node runs
  on its own runner: a separate pod in cluster mode, a separate
  goroutine slot in local mode. Nodes carry the dispatch envelope -
  `Retry`, `Timeout`, `OnFailure`, `Cache`, `RunsOn`, `BeforeRun` /
  `AfterRun`, `Approval` gating - because each Node *is* the unit the
  scheduler can retry, time out, or route to a labeled runner.
- **Work / WorkStep** is the *inner* DAG - units of work *within* one
  Node's runner. Steps share the Node's runner, filesystem, environment,
  and ctx. They have `Needs` for ordering and `SkipIf` for predicates;
  they do **not** carry Node-only modifiers (Retry, Timeout, ...).
  Promote a step to a Node via `SpawnNode` if it needs one.

Each pipeline implements `Plan(ctx, plan *sw.Plan, in T, rc sw.RunContext) error`
which registers nodes on the passed-in `*Plan` (the outer DAG). Each Node carries a `Job` whose `Work()`
method returns the inner DAG. Both DAGs are materialized at Plan-time
- the orchestrator walks the entire reachable tree (including spawn
targets) before any dispatch begins, so `pipeline explain` and the
dashboard render the full structure before the run starts.

### Cost grid

| API | Layer | Cardinality | Cost |
|---|---|---|---|
| `sw.Job(plan, id, job)` | Plan | one, declared at Plan-time | normal node |
| `sw.JobFanOut(plan, name, items, fn)` | Plan | many, items in hand at Plan-time | normal nodes; one per element |
| `sw.JobFanOutDynamic(plan, name, source, fn)` | Plan | many, source's runtime output | source runner exits before fan-out - no stranded compute |
| `w.Step(id, fn)` | Work | one, in-process unit of work | one logging frame, ordered/parallel via Needs |
| `w.SpawnNode(id, job)` | Work | one, decided mid-Work | spawning runner stays suspended until child completes |
| `w.SpawnNodeForEach(items, fn)` | Work | many, mid-Work fan-out | spawning runner stays suspended across all children |

The verb tells you the cost. `Node*` is cheap; `SpawnNode*` flags the
layer jump and the suspended-runner cost. Reach for `SpawnNode` when
you genuinely need Node-only modifiers (Retry, RunsOn, distinct
runner) on a unit decided mid-execution; otherwise stay inside Work.

## Trivial single-step jobs

For pipelines that are one closure with no DAG, register a `JobFn`:

```go
type Lint struct{ sparkwing.Base }

func (p *Lint) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
    sw.Job(plan, rc.Pipeline, sw.JobFn(p.run))
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

For typed-output Jobs (downstream nodes read the value via `Ref[T]` /
`Output[T]`), define a struct that embeds `sparkwing.Produces[T]` and
have its `Work()` set a typed result step:

```go
type Build struct {
    sparkwing.Base
    sparkwing.Produces[BuildOut]
}

func (j *Build) Work() *sparkwing.Work {
    w := sparkwing.NewWork()
    sparkwing.Result(w, "run", j.run)
    return w
}

func (j *Build) run(ctx context.Context) (BuildOut, error) {
    return BuildOut{Tag: "app:sha-abc"}, nil
}

build := sw.Job(plan, "build", &Build{})
buildRef := sparkwing.Output[BuildOut](build)
sw.Job(plan, "deploy", &DeployJob{Build: buildRef}).Needs(build)
```

## Multi-step jobs

For jobs whose body is more than one logical phase, implement `Job`
yourself. The struct's `Work()` method declares the inner DAG: each
`Step` is a unit of work, `Needs` declares ordering, `Sequence` and
`Parallel` are convenience combinators.

```go
type BuildJob struct{ sparkwing.Base }

func (j *BuildJob) Work() *sparkwing.Work {
    w := sparkwing.NewWork()
    fetch := w.Step("fetch", j.fetch)
    validate := w.Step("validate", j.validate)
    w.Step("compile", j.compile).Needs(fetch, validate)
    return w
}

func (j *BuildJob) fetch(ctx context.Context) error    { return j.gitFetch(ctx) }
func (j *BuildJob) validate(ctx context.Context) error { return j.checkGoMod(ctx) }
func (j *BuildJob) compile(ctx context.Context) error  { return j.goBuild(ctx) }
```

`Sequence` wires `Needs` between consecutive steps; `Parallel` groups
steps for downstream fan-in:

```go
func (j *DeployJob) Work() *sparkwing.Work {
    w := sparkwing.NewWork()
    a := w.Step("render-manifests", j.render)
    b := w.Step("argo-sync", j.sync)
    c := w.Step("verify", j.verify)
    w.Sequence(a, b, c)  // a -> b -> c
    return w
}
```

### Typed step output

For the common case -- a Job with a single typed step whose return
value IS the Job's output -- use `sparkwing.Result[T](w, id, fn)`:

```go
func (j *BuildJob) Work() *sparkwing.Work {
    w := sparkwing.NewWork()
    sparkwing.Result(w, "compile", j.compile)
    return w
}
```

For Works with multiple typed steps where downstream steps inside the
same Work read intermediate values via `.Get(ctx)`, use `Out[T]`
directly; pair it with `w.SetResult(step)` to designate which (if
any) is the Node's typed output:

```go
func (j *DeployJob) Work() *sparkwing.Work {
    w := sparkwing.NewWork()
    tags := sparkwing.Out(w, "compute-tags", func(ctx context.Context) (Tags, error) {
        return loadTags(ctx)
    })
    w.Step("publish", func(ctx context.Context) error {
        return publish(ctx, tags.Get(ctx))
    }).Needs(tags.WorkStep)
    return w
}
```

### Inner step skip

`step.SkipIf(predicate)` skips a single step without aborting the
Work. Multiple `SkipIf` calls accumulate with OR semantics.

```go
w.Step("publish", j.publish).
    Needs(buildOut).
    SkipIf(func(ctx context.Context) bool { return os.Getenv("DRY_RUN") == "1" })
```

## Plan-layer fan-out

Two type-safe verbs cover the Plan-layer fan-out cases. Both return a
`*Group` whose `name` becomes a collapsible cluster in the dashboard
and a single `Needs(group)` target downstream.

### Static: `JobFanOut` (slice in hand at Plan-time)

`sw.JobFanOut[T]` registers one Node per element of a slice
already known when `Plan()` runs:

```go
images := sw.JobFanOut(plan, "image-builds", Images, func(img imageSpec) (string, sw.Workable) {
    return "build-" + img.Name, &BuildImage{Image: img}
}).Needs(webBuild, discover).Retry(2)

sw.Job(plan, "artifact", &ArtifactJob{}).Needs(images)
```

The chained `.Needs(...)` / `.Retry(...)` apply to every member; see
*Group modifiers* below.

### Runtime: `JobFanOutDynamic` (slice produced by an upstream Node)

`sw.JobFanOutDynamic[T]` materializes one Plan-level Node per
element of an upstream typed Node's output slice. Each fan-out child is
a fresh Node with its own dispatch envelope:

```go
type ListShards struct {
    sparkwing.Base
    sparkwing.Produces[[]string]
}

func (j *ListShards) Work() *sparkwing.Work {
    w := sparkwing.NewWork()
    sparkwing.Result(w, "run", j.run)
    return w
}

func (j *ListShards) run(ctx context.Context) ([]string, error) {
    return loadShards(ctx)
}

shards := sw.Job(plan, "list-shards", &ListShards{})

sw.JobFanOutDynamic(plan, "shard-work", shards, func(shard string) (string, sw.Workable) {
    return "process-" + shard, &ProcessShard{Shard: shard}
})
```

`JobFanOutDynamic` runs at Plan-time-after-source: the source Node runs
and exits, *then* the orchestrator builds children from the resolved
output. The source runner is not held during the fan-out - no
stranded compute.

### Group modifiers

`*Group` mirrors the chainable surface of `*Node` (Needs, Retry,
Timeout, RunsOn, SkipIf, Env, Inline, ContinueOnError, Optional,
BeforeRun, AfterRun, Cache, NeedsOptional). Each call delegates to
every member and returns the same `*Group` for chaining. `OnFailure`
is intentionally per-Node; group-level recovery has unclear semantics.

## Layer escape: SpawnNode

When a unit of work decided *mid-Work* needs a Node-only modifier
(Retry, RunsOn, distinct runner, separate cache key), promote it via
`SpawnNode`. The spawning runner suspends until the spawned Node
completes:

```go
func (j *ScanJob) Work() *sparkwing.Work {
    w := sparkwing.NewWork()
    analyze := w.Step("analyze", j.analyze)
    scan := w.SpawnNode("compliance", &ComplianceJob{}).Needs(analyze)
    w.Step("publish", func(ctx context.Context) error {
        return publish(ctx, scan)
    }).Needs(scan)
    return w
}
```

The spawned Node id is namespaced as `parent/spawnID`
(e.g. `scan/compliance`) so logs and the run history don't collide.

`w.SpawnNodeForEach(items, fn)` is the cardinality-many variant. The
generator runs once Needs are satisfied; each returned `(id, Job)`
pair becomes a fresh Plan node. The spawning runner stays suspended
across the entire fan-out:

```go
w.SpawnNodeForEach(targets, func(target string) (string, sw.Workable) {
    return "deploy-" + target, &DeployJob{Target: target}
}).Needs(buildStep)
```

Reach for spawn primitives sparingly. Each call holds a runner slot
during the child's lifetime; a deeply nested spawn chain pins one slot
per layer. The verb is intentionally distinct from `Step` to flag the
cost at the call site.

## Modifier scope discipline

| Modifier | Layer | Notes |
|---|---|---|
| `Retry(n, opts...)` | Plan only | `RetryBackoff(d)` + `RetryAuto()` options; `RetryAuto` re-dispatches the whole Node |
| `Timeout` | Plan only | per-attempt cap |
| `OnFailure(id, job)` | Plan only | constructs a detached recovery node fired on parent failure |
| `Cache` | Plan only | content-addressed result memoization |
| `RunsOn(labels...)` | Plan only | scheduler routes by runner label |
| `BeforeRun` / `AfterRun` | Plan only | runner lifecycle hooks |
| `Approval` | Plan only | gates dispatch on a human decision |
| `Inline()` | Plan only | bypass the runner entirely |
| `Group("name")` | Plan only | UI grouping |
| `Dynamic()` | Plan only | flag for renderers |
| `Needs` | both | ordering inside its layer |
| `SkipIf` | both | skip predicate |
| typed output | both | `Ref[T]` (Node) / `TypedStep[T]` (Work) |

A Step that needs Retry / Timeout / RunsOn is the canonical signal
to promote it to a Node via `SpawnNode`.

## Scheduling modifiers

### `.Inline()`

Marks a Node for in-process execution on the dispatcher (the
controller in cluster mode, the laptop binary in local mode). Bypasses
the configured Runner so no pod / warm-runner spin-up cost is paid.

```go
sw.Job(plan, "setup", &SetupJob{}).Inline()
sw.Job(plan, "summarize", &SummaryJob{}).Needs(deploys).Inline()
```

Reach for it on genuinely lightweight glue (setup checks, fan-in
summaries) that would otherwise burn seconds of runner boot for a few
hundred ms of work. It is **not** a general "faster" knob: inline
nodes share the dispatcher's goroutine pool, so a long inline job
delays every other node's scheduling. Keep inline work under a second
or two. `.Inline()` on an approval gate panics. `.RunsOn` labels are
ignored for inline nodes.

### `.Dynamic()`

Annotates a Node whose downstream work is runtime-variable - the
common case is `AwaitPipelineJob` or external task enqueueing. Purely
a signal to readers: the plan preview shows `[dynamic]` so reviewers
know to inspect the run for the actual child nodes. `JobFanOutDynamic`
source nodes are auto-marked dynamic at plan finalization, so you only
need `.Dynamic()` for the non-fan-out case.

### `.Group("name")`

Pure UI annotation. The dashboard folds nodes that share a group under
one collapsible header; the scheduler, cache, retry, and dependency
semantics are unchanged.

```go
sw.Job(plan, "schema-check",  &SchemaCheckJob{}).Group("safety")
sw.Job(plan, "security-scan", &SecurityScanJob{}).Group("safety")
```

## Eager Plan-time materialization

Every Job's `Work()` runs during the Pipeline's `Plan()`, not at
runner dispatch. The orchestrator walks the entire reachable nested
DAG - including transitive `SpawnNode` targets - before any node runs.
What stays runtime-dynamic is bounded:

- Which Nodes execute (Plan-time branching on `in`, Node `SkipIf`).
- Which Steps execute (intra-Work `SkipIf`).
- Whether each `SpawnNode` fires and with what arguments.
- `JobFanOutDynamic` cardinality (count and keys come from the source's
  runtime output; the per-item shape is known).

Because the structure is reachable from source, `sparkwing pipeline
explain --name X` and the dashboard render the full Plan -> Node ->
Work -> Step tree before anything runs. The dashboard's per-Node card
exposes a collapsible **Work** section showing inner steps and spawn
declarations as placeholders (filled in once spawned children
appear).

The cost-grid table above is the load-bearing artifact for an agent
reader - load it before designing a multi-Node pipeline.

## Cache

`.Cache(CacheOptions{...})` turns a Node into a content-addressed
cache entry plus a coordination primitive. The orchestrator computes
the key after upstream deps complete, looks it up across runs, and
short-circuits the job on a hit, replaying the cached output without
running. Misses execute normally and record `(key -> output)` on
success.

```go
build := sw.Job[BuildOut](plan, "build", &BuildJob{}).
    Cache(sparkwing.CacheOptions{
        Key:     "build",
        OnLimit: sparkwing.Coalesce,
        CacheKey: func(ctx context.Context) sparkwing.CacheKey {
            return sparkwing.Key("build", run.Git.SHA)
        },
        CacheTTL: 24 * time.Hour,
    })
```

`sparkwing.Key(parts...)` hashes arbitrary parts into a stable string
- use it rather than hand-concatenating. Return the empty string from
`CacheKey` to opt out for a particular invocation - useful when inputs
are non-deterministic.

`Cache` is also the coordination primitive: omit `CacheKey` and you
get mutex (`Max=0|1`) or semaphore (`Max>1`) gating without the
memoization. See [scheduling](scheduling.md) for the full coordination
model.

Do not cache nodes whose effect is the side effect itself (deploys,
notifications, gitops commits). Caching replays the return value, not
the external world - a "cached" deploy did not actually deploy
anything. Cache pure builds, test runs against content-addressed
sources, and artifact packaging; gate external side effects with
`.Needs` on the cached Node.

## Approval gates

Pause a run and wait for a human decision by registering an
`*sw.Approval` job with `sw.Job`. The orchestrator detects the
*Approval value, flips the Node to `approval_pending`, writes an
approvals row, and blocks until the dashboard, CLI, or the configured
timeout resolves it.

```go
approve := sw.Job(plan, "approve-prod", &sw.Approval{
    Message:   fmt.Sprintf("Promote %s to prod?", git.SHA),
    Timeout:   2 * time.Hour,
    OnTimeout: sw.ApprovalFail,
}).Needs(integStg)
sw.Job(plan, "deploy-prod", &DeployJob{Env: "prod"}).Needs(approve)
```

Fields:

- `Message` - operator-facing prompt shown in the dashboard banner
  and CLI list output. Compose with `fmt.Sprintf` if you need to
  weave in run-time values.
- `Timeout` - maximum wait before the waiter writes a `timed_out`
  resolution itself. Zero (the default) means never time out.
- `OnTimeout` - one of `sw.ApprovalFail` (default), `sw.ApprovalDeny`,
  or `sw.ApprovalApprove`. Unrecognised values panic at plan time.

Resolution paths:

- Dashboard: any node in `approval_pending` renders an indigo banner
  with a comment textarea and Approve / Deny buttons.
- CLI: `sparkwing runs approvals approve --run ID --node ID`,
  `sparkwing runs approvals deny ...`.
- Programmatic: `POST /api/v1/runs/{run}/approvals/{node}` with
  `{"resolution":"approved","comment":"..."}`. The approver is recorded
  from the authenticated principal.

**Limitation - `wing` runs cannot survive a terminal close mid-approval.**
In local (`wing <pipeline>`) mode the orchestrator lives in the same
process as the CLI invocation. Close the terminal while a gate is
waiting and the waiter goroutine dies with it: the approvals row
stays on disk and can still be resolved from the dashboard, but
nothing transitions the Node out of `approval_pending` and the run
stays `running` forever. Workaround: re-run, or keep `sparkwing
dashboard start` up so the dispatcher lives in the long-lived local
web server. Cluster mode has the same property via the controller
pod.

## Migration from the pre-rewrite SDK

If you're reading old jobs that don't compile, here's the rename map:

| Old | New |
|---|---|
| `plan.Add(id, &J{})` | `sw.Job(plan, id, &J{})` (`J` must implement `Work()`) |
| `plan.Step(id, fn)` | `sw.Job(plan, id, sw.JobFn(fn))` |
| `plan.ExpandFrom(source, gen)` | `sw.JobFanOutDynamic(plan, name, source, fn)` |
| `sparkwing.NodeForEach(plan, source, fn)` | `sw.JobFanOutDynamic(plan, name, source, fn)` |
| `Run(ctx) error` on a Job | `Work() *Work` returning a one-step Work |
| `Run(ctx) (T, error)` on a Job | `Work()` with `sparkwing.Result(w, "run", j.run)` |
| `sparkwing.Step(ctx, name)` | structured `step_start` / `step_end` events emitted automatically by each `WorkStep` |
| `sparkwing.StepErr(ctx, err)` | the same step events; for best-effort work, return nil from the step and `sparkwing.Error` if needed |
| `InvokeJob(ctx, name, &J{})` | compose as a `Step` (or another sub-`Job` via `SpawnNode`) inside the parent's `Work` |
| `InvokeJobsParallel(ctx, NamedJob...)` | declare each as its own Step in the same Work; the runner runs them in parallel by default |
| `sparkwing.NamedJob` | gone - the `Step` id is the name |
| `node.CacheKey(fn)` | `node.Cache(sparkwing.CacheOptions{Key: ..., CacheKey: fn})` |
| `node.Exclusive(group)` | `node.Cache(sparkwing.CacheOptions{Key: group})` |

See `CHANGELOG.md` for the full per-release migration notes.
