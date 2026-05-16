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

- **Plan / Job** is the *outer* DAG - units of dispatch. Each Job runs
  on its own runner: a separate pod in cluster mode, a separate
  goroutine slot in local mode. Nodes carry the dispatch envelope -
  `Retry`, `Timeout`, `OnFailure`, `Cache`, `Requires`, `BeforeRun` /
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
`*Group` whose `name` becomes a collapsible cluster in the dashboard
and a single `Needs(group)` target downstream.

### Static: `JobFanOut` (slice in hand at Plan-time)

`sw.JobFanOut[T]` registers one Job per element of a slice
already known when `Plan()` runs:

```go
images := sw.JobFanOut(plan, "image-builds", Images, func(img imageSpec) (string, sw.Workable) {
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

sw.JobFanOutDynamic(plan, "shard-work", shards, func(shard string) (string, sw.Workable) {
    return "process-" + shard, &ProcessShard{Shard: shard}
})
```

`JobFanOutDynamic` runs at Plan-time-after-source: the source Job runs
and exits, *then* the orchestrator builds children from the resolved
output. The source runner is not held during the fan-out - no
stranded compute.

### Group modifiers

`*Group` mirrors the chainable surface of `*Job` (Needs, Retry,
Timeout, Requires, SkipIf, Env, Inline, ContinueOnError, Optional,
BeforeRun, AfterRun, Cache, NeedsOptional). Each call delegates to
every member and returns the same `*Group` for chaining. `OnFailure`
is intentionally per-Job; group-level recovery has unclear semantics.

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

`sw.JobSpawnEach(w, items, fn)` is the cardinality-many variant. The
generator runs once Needs are satisfied; each returned `(id, Job)`
pair becomes a fresh Plan node. The spawning runner stays suspended
across the entire fan-out:

```go
sw.JobSpawnEach(w, targets, func(target string) (string, sw.Workable) {
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
| `Cache` | Plan only | content-addressed result memoization |
| `Requires(labels...)` | Plan only | scheduler routes by runner label |
| `BeforeRun` / `AfterRun` | Plan only | runner lifecycle hooks |
| `Approval` | Plan only | gates dispatch on a human decision |
| `Inline()` | Plan only | bypass the runner entirely |
| `Group("name")` | Plan only | UI grouping (free-function form: `sw.GroupJobs`) |
| `Dynamic()` | Plan only | flag for renderers |
| `Needs` | both | ordering inside its layer |
| `SkipIf` | both | skip predicate |
| typed output | both | `Ref[T]` (Job) / `*WorkStep` returned from `Work` (Work) |

A Step that needs Retry / Timeout / Requires is the canonical signal
to promote it to a Job via `sw.JobSpawn`.

## Scheduling modifiers

### `.Inline()`

Marks a Job for in-process execution on the dispatcher (the
controller in cluster mode, the laptop binary in local mode). Bypasses
the configured Runner so no pod / warm-runner spin-up cost is paid.

```go
sw.Job(plan, "setup", &Setup{}).Inline()
sw.Job(plan, "summarize", &Summarize{}).Needs(deploys).Inline()
```

Reach for it on genuinely lightweight glue (setup checks, fan-in
summaries) that would otherwise burn seconds of runner boot for a few
hundred ms of work. It is **not** a general "faster" knob: inline
nodes share the dispatcher's goroutine pool, so a long inline job
delays every other node's scheduling. Keep inline work under a second
or two. `.Inline()` on an approval gate panics. `.Requires` labels are
ignored for inline nodes.

### `.Dynamic()`

Annotates a Job whose downstream work is runtime-variable - the
common case is `RunAndAwait` or external task enqueueing. Purely
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

`.Cache(CacheOptions{...})` turns a Job into a content-addressed
cache entry plus a coordination primitive. The orchestrator computes
the key after upstream deps complete, looks it up across runs, and
short-circuits the job on a hit, replaying the cached output without
running. Misses execute normally and record `(key -> output)` on
success.

```go
build := sw.Job[BuildOut](plan, "build", &Build{}).
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
that don't apply to gates -- `Retry`, `Timeout`, `Cache`, `Requires`,
`Inline` -- are physically absent, so misuse is a compile error
rather than a runtime panic / silent no-op.

`ApprovalConfig` fields:

- `Message` - operator-facing prompt shown in the dashboard banner
  and CLI list output. Compose with `fmt.Sprintf` if you need to
  weave in run-time values.
- `Timeout` - maximum wait before the waiter writes a `timed_out`
  resolution itself. Zero (the default) means never time out.
- `OnExpiry` - one of `sw.ApprovalFail` (default), `sw.ApprovalDeny`,
  or `sw.ApprovalApprove`. Unrecognised values panic at plan time.
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

**Limitation - `wing` runs cannot survive a terminal close mid-approval.**
In local (`wing <pipeline>`) mode the orchestrator lives in the same
process as the CLI invocation. Close the terminal while a gate is
waiting and the waiter goroutine dies with it: the approvals row
stays on disk and can still be resolved from the dashboard, but
nothing transitions the Job out of `approval_pending` and the run
stays `running` forever. Workaround: re-run, or keep `sparkwing
dashboard start` up so the dispatcher lives in the long-lived local
web server. Cluster mode has the same property via the controller
pod.

## Migration from the pre-rewrite SDK

If you're reading old jobs that don't compile, here's the rename map:

| Old | New |
|---|---|
| `plan.Add(id, &J{})` | `sw.Job(plan, id, &J{})` (`J` must implement `Work(w *Work) (*WorkStep, error)`) |
| `plan.Step(id, fn)` | `sw.Job(plan, id, fn)` (closure passes through directly) |
| `plan.ExpandFrom(source, gen)` | `sw.JobFanOutDynamic(plan, name, source, fn)` |
| `sparkwing.NodeForEach(plan, source, fn)` | `sw.JobFanOutDynamic(plan, name, source, fn)` |
| `Work() *Work` on a Job | `Work(w *Work) (*WorkStep, error)` (return the result step, or nil) |
| `w.Step(id, fn)` | `sw.Step(w, id, fn)` |
| `w.Sequence(a, b, c)` | `b.Needs(a); c.Needs(b)` -- pure `.Needs` chain |
| `w.Parallel(x, y)` for fan-in | `next.Needs(x, y)` directly |
| `w.Parallel(x, y)` for UI cluster | `sw.GroupSteps(w, "name", x, y)` |
| `sparkwing.Out(w, id, fn) + .Get(ctx)` | `sw.Step(w, id, fn) + sw.StepGet[T](ctx, step)` |
| `sparkwing.Result(w, id, fn) + return w` | `return sw.Step(w, id, fn), nil` |
| `w.SetResult(step)` | return `step` from `Work` |
| `w.JobSpawn(id, &J{})` | `sw.JobSpawn(w, id, &J{})` |
| `w.JobSpawnEach(items, fn)` | `sw.JobSpawnEach(w, items, fn)` |
| `sw.Approval(plan, id, cfg)` | `sw.JobApproval(plan, id, cfg)` |
| `sw.Group(plan, name, ...)` | `sw.GroupJobs(plan, name, ...)` |
| `sw.JobFn(fn)` | (gone) -- pass `fn` directly to `sw.Job` |
| `*TypedStep[T]` | (gone) -- only `*WorkStep` remains; `outType` is set by reflection |
| `sparkwing.Step(ctx, name)` | structured `step_start` / `step_end` events emitted automatically by each `WorkStep` |
| `sparkwing.StepErr(ctx, err)` | the same step events; for best-effort work, return nil from the step and `sparkwing.Error` if needed |
| `InvokeJob(ctx, name, &J{})` | compose as a `Step` (or another sub-`Job` via `JobSpawn`) inside the parent's `Work` |
| `InvokeJobsParallel(ctx, NamedJob...)` | declare each as its own Step in the same Work; the runner runs them in parallel by default |
| `sparkwing.NamedJob` | gone - the `Step` id is the name |
| `node.CacheKey(fn)` | `node.Cache(sparkwing.CacheOptions{Key: ..., CacheKey: fn})` |
| `node.Exclusive(group)` | `node.Cache(sparkwing.CacheOptions{Key: group})` |

See `CHANGELOG.md` for the full per-release migration notes.
