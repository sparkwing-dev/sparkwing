# SDK Reference

A curated tour of the `sparkwing` package helpers you call from
`.sparkwing/jobs/*.go`, plus the SDK-authoring concepts worth loading
at the start of a task. The **complete** API -- every exported symbol
with its signature -- is generated from source in
[sdk-reference.md](sdk-reference.md) (offline:
`sparkwing docs read --topic sdk-reference`), and browsable with
cross-links on pkg.go.dev:
<https://pkg.go.dev/github.com/sparkwing-dev/sparkwing/sparkwing>.

When a signature shown here disagrees with the generated reference, the
generated reference wins. For the Plan/Work model and the
`sparkwing.yaml` shape, see [pipelines](pipelines.md).

The convention is to import the SDK under the alias `sw`:

```go
import sw "github.com/sparkwing-dev/sparkwing/sparkwing"
```

Every example below uses that alias. The package itself is named
`sparkwing` -- the alias just keeps the call sites short.

## Read/write split

Operations that mutate a DAG (Plan or Work) are **free functions on
`sparkwing`**; operations that read a DAG are **methods on the
container** (`*Plan` / `*Work`). Go forbids generic methods, so the
typed adders (`RefTo[T]`, `JobFanOut[T]`, `StepGet[T]`) must be free
functions; for symmetry every adder lives there. Reads stay on the
container because they don't have the same constraint and the
`plan.X()` / `w.X()` shape reads naturally for accessors.

The same grammar applies at both layers: `sw.<Verb>(<container>,
...args).<modifier>(...)`. Tab-completing `sw.` shows every adder;
tab-completing `Job` shows every way to put a Job into the run
(`Job`, `JobFanOut`, `JobFanOutDynamic`, `JobApproval`, `JobSpawn`,
`JobSpawnEach`) regardless of layer.

| Layer | Mutate (free funcs) | Read (methods) |
|---|---|---|
| Plan | `sw.Job(plan, id, x)` | `plan.Nodes()` |
| Plan | `sw.JobFanOut(plan, name, items, fn)` | `plan.Job(id)` |
| Plan | `sw.JobFanOutDynamic(plan, name, source, fn)` | `plan.LintWarnings()` |
| Plan | `sw.JobApproval(plan, id, cfg)` | `plan.Expansions()` |
| Plan | `sw.GroupJobs(plan, name, members...)` | `plan.IsDynamicNode(id)` / `plan.GroupSourceIDs(id)` |
| Plan | `sw.RefTo[T](node)` | |
| Work | `sw.Step(w, id, fn)` | `w.Steps()` / `w.StepByID(id)` |
| Work | `sw.JobSpawn(w, id, job)` | `w.Spawns()` / `w.SpawnGens()` |
| Work | `sw.JobSpawnEach(w, items, fn)` | |
| Work | `sw.GroupSteps(w, name, steps...)` | |
| Work | `sw.StepGet[T](ctx, step)` | |

## The two-layer model

Plan/Job (the outer DAG, units of dispatch) versus Work/WorkStep (the
inner DAG, units of work inside one Job's runner) -- the Plan-only
modifier set, the Plan-time materialization, and the per-adder cost
grid -- is the canonical conceptual tour in
[pipelines](pipelines.md); the rest of this page assumes it. The
read/write split above is the SDK-specific corollary: mutating adders
are free functions, reads are container methods.

## Plan() must be pure

`Pipeline.Plan(ctx, plan, in, rc)` declares the DAG by registering
nodes on the passed-in `*Plan` and returns `error`. The SDK
constructs the `*Plan` and hands it in -- authors don't call
`NewPlan()`. Plan() must not run work: calling `sparkwing.Bash` /
`Exec`, anything in `sparkwing/docker`, anything in `sparkwing/git`,
or any other helper that touches state inside `Plan()` panics at
runtime with a message naming the helper and pointing back here.

Why: `pipeline explain`, the dashboard's pipeline view, the MCP
tool-definition path, and the describe-cache all call `Plan()`
multiple times for read-only purposes. If `Plan()` shells out, those
flows break outside a working repo / docker daemon, and the
invariant that "the reachable graph derives from source without
running anything" no longer holds.

Move the work into a Job's `Work()` body and surface the result as a
typed output the rest of the plan consumes via `Ref[T]`:

```go
// Wrong: shells out from Plan()
func (b *Build) Plan(ctx context.Context, plan *sw.Plan, args BuildArgs, run sw.RunContext) error {
    tags, err := docker.ComputeTags(ctx)              // panics: Plan-time guard
    platforms, err := docker.FilterBuildxPlatforms(ctx, ...) // panics
    sw.Job(plan, "build", &BuildImageJob{Tags: tags.All(), Platforms: platforms})
    return nil
}

// Right: discover Job with typed output, downstream Ref[BuildContext]
type BuildContext struct {
    TagList   []string
    Platforms []string
    // ...
}

type DiscoverBuildContextJob struct {
    sw.Base
    sw.Produces[BuildContext]
}

func (j *DiscoverBuildContextJob) Work(w *sw.Work) (*sw.WorkStep, error) {
    return sw.Step(w, "run", j.run), nil
}

func (j *DiscoverBuildContextJob) run(ctx context.Context) (BuildContext, error) {
    tags, _ := docker.ComputeTags(ctx)              // ok: inside a Job
    platforms, _ := docker.FilterBuildxPlatforms(ctx, ...)
    return BuildContext{TagList: tags.All(), Platforms: platforms}, nil
}

func (b *Build) Plan(ctx context.Context, plan *sw.Plan, args BuildArgs, run sw.RunContext) error {
    discover := sw.Job(plan, "discover", &DiscoverBuildContextJob{}).Inline()
    discoverRef := sw.RefTo[BuildContext](discover)
    sw.Job(plan, "build", &BuildImageJob{Discover: discoverRef}).Needs(discover)
    return nil
}
```

`.Inline()` keeps a tiny discover Job from paying dispatch overhead
while still living in the DAG (so explain renders it, retry/cache
apply, the dashboard shows it). Inline is the explicit "run in the
orchestrator process" annotation -- it's not a way to opt back into
Plan-time side effects.

Consumer-side helper packages (sparks-core libraries, custom
pipeline libs) can opt their own ctx-taking entry points into the
guard by calling `planguard.Guard(ctx, "yourpkg.Helper")` at the top
(import `github.com/sparkwing-dev/sparkwing/sparkwing/planguard`).

## Exec - shelling out

Two entry points pick the kind of execution. Each returns a `*Cmd`
builder you chain modifiers onto, then terminate with one verb that
decides what to do with the output.

```
Bash(ctx, line)               *Cmd  // bash -c, no formatting; line is verbatim
Exec(ctx, name, args...)      *Cmd  // no shell; arg-vector form
WorkDir() string                    // pipeline working directory (repo root)
```

`Bash` shells out to the host's `bash`. macOS and Linux have it by
default. **On Windows, install [Git for Windows](https://git-scm.com/download/win)
and run `sparkwing` from the Git Bash terminal it ships** -- the same dep
pipelines.md flags. `Exec` doesn't need a shell, so it works regardless;
prefer it when the command is a clean arg vector (no pipes, redirects,
or `&&`).

`Bash` takes the shell program verbatim - there's no printf-style
formatting. Splice dynamic *values* into a shell command by
passing them through `.Env("KEY", value)` and referencing `"$KEY"`
inside the line; the shell expands the variable safely. Splice dynamic
*argv* through `Exec(ctx, name, args...)` instead. This makes shell
injection unspellable: there is no signature that takes a shell string
and a dynamic value together.

Modifiers (chain freely; each returns the same `*Cmd`):

```
.Dir(path)                          // run in path; relative resolves vs WorkDir()
.Env(key, val)                      // add one env var
.EnvMap(map)                        // merge a map of env vars
```

Terminators (one per call; pick the shape that matches the post-exec
work):

```
.Run() (ExecResult, error)          // stream stdout/stderr to the run logger
.Capture() (ExecResult, error)      // silent; full output in ExecResult
.String() (string, error)           // captured + TrimSpace(stdout)
.Lines() ([]string, error)          // captured stdout, split + trimmed, blanks dropped
.JSON(out any) error                // captured stdout decoded via json.Unmarshal
.MustBeEmpty(reason) error          // non-empty stdout becomes "<reason>:\n<stdout>"
```

Common shapes:

```go
sparkwing.Bash(ctx, "go test ./...").Run()
sparkwing.Bash(ctx, `git -C "$R" diff --name-only`).Env("R", repo).MustBeEmpty("uncommitted changes")
sha, _ := sparkwing.Exec(ctx, "git", "rev-parse", "HEAD").String()
pkgs, _ := sparkwing.Bash(ctx, "go list ./...").Lines()
var pods PodList
sparkwing.Exec(ctx, "kubectl", "get", "pods", "-o", "json").JSON(&pods)
sparkwing.Exec(ctx, "go", "test", "./...").Dir("internal").Env("CGO_ENABLED", "0").Run()
```

`ExecError` carries `Command`, `Stdout`, `Stderr`, `ExitCode`, and a
wrapped `Cause`. `errors.As(err, &ee)` works through every terminator
(including `JSON` and `MustBeEmpty`).

## Files

```
Path(parts...) string                       // join onto WorkDir(); abs first part wins
ReadFile(path) ([]byte, error)              // os.ReadFile, relative -> WorkDir()
WriteFile(path, data) error                 // os.WriteFile, perm 0o644
Glob(pattern) ([]string, error)             // filepath.Glob, returns absolute paths
```

When invoked outside any sparkwing project (no `.sparkwing/`
discoverable above cwd), the relative-path forms return
`sparkwing.ErrNoProject`. Absolute inputs work without a project.

## Logging

```
Info(ctx, format, args...)                // info-level log on the current node
Warn(ctx, format, args...)                // warn-level log
Error(ctx, format, args...)               // error-level log
Debug(ctx, format, args...)               // only when SPARKWING_DEBUG=1
Annotate(ctx, msg)                        // persistent node-level summary
```

`Annotate` differs from the four log helpers: the message is appended
to a persistent `annotations` list on the Job row instead of (only)
appearing in the run log. The dashboard surfaces these summaries
next to the node so operators see "processed 1,234 records · 12
failed" without opening the log view. Multiple calls per node
accumulate; calls outside a node context are a silent no-op.

Per-level methods only -- the level lives in the verb name, no
level-as-string arg. Same printf-style format-args contract across
all four. Each call goes through the `Logger` installed in ctx and
is stamped with the current Job and Job-stack envelope.

Step boundaries are emitted automatically by `RunWork` as structured
`step_start` / `step_end` events; the renderer surfaces them as a
collapsible bucket in the CLI and dashboard.

These four helpers are sparkwing's pipeline-observability channel,
not a general-purpose logger -- they exist so node output, run
records, and the dashboard see the same stream. The `Logger`
interface is pluggable: install your own backend (slog, zerolog,
zap, OTel) via `sparkwing.WithLogger(ctx, impl)` and the call sites
stay the same.

## Plan - the outer DAG

Every pipeline implements
`Plan(ctx context.Context, plan *sw.Plan, in T, rc sw.RunContext) error`
where `T` is the pipeline's typed Inputs struct. The SDK constructs
the `*Plan` and passes it to the user's Plan(); authors register
nodes on it via the free-function adders below.

`in` carries the typed flag values (see "Typed Inputs" below). `rc`
is a `sw.RunContext` - the run-time environment Plan branches on.
Useful fields:

```
rc.RunID    string         // unique run identifier
rc.Pipeline string         // registered pipeline name
rc.Git      *Git           // repo state at the trigger SHA
rc.Trigger  TriggerInfo    // {Source: "manual|push|schedule|webhook", User}
```

Most one-step Plans don't need `rc` at all - the parameter is named
for the moment a Plan starts branching on trigger source / SHA.

Free-function adders (writes; mutate the Plan):

```
sw.Job(plan, id, x any) *JobNode                                             // register a Job: x is sw.Workable or func(ctx) error
sw.JobFanOut[T](plan, name, items, fn) *JobGroup                             // Plan-time static fan-out
sw.JobFanOutDynamic[T](plan, name, source, fn) *JobGroup                     // runtime fan-out after source completes
sw.JobApproval(plan, id, cfg) *ApprovalGate                                   // human-decision gate (see "Approval gates")
sw.GroupJobs(plan, name, members...) *JobGroup                               // named cluster + Needs target (name="" = unnamed)
sw.RefTo[T](node) sw.Ref[T]                                                   // typed Ref into node's typed output
```

`sw.Job`'s third argument is `any`: pass either an `sw.Workable`
implementation (struct with `Work(w *Work) (*WorkStep, error)`) or a
plain `func(ctx context.Context) error` for the trivial single-closure
case. Reflection at register time accepts either form. Anything else
panics at materialize time.

Approval gates register through `sw.JobApproval` and return an
`*ApprovalGate` -- a narrower modifier surface than `*JobNode` so the
modifiers that don't apply to gates (`Retry`, `Timeout`, `Cache`,
`Requires`, `Inline`) are physically absent and a misuse is a compile
error rather than a runtime surprise:

```go
approve := sw.JobApproval(plan, "approve-prod", sw.ApprovalConfig{
    Message:  fmt.Sprintf("Promote %s to prod?", git.SHA),
    Timeout:  2 * time.Hour,
    OnExpiry: sw.ApprovalFail,
}).Needs(integStg)

sw.Job(plan, "deploy-prod", &Deploy{}).Needs(approve)
```

Available modifiers on `*ApprovalGate`: `Needs`, `NeedsOptional`,
`OnFailure`, `BeforeRun`, `AfterRun`, `SkipIf`, `Optional`,
`ContinueOnError`. Plus `Job()` as the escape hatch when an author
genuinely needs the underlying `*JobNode`.

`OnExpiry` defaults to fail; valid values are `sw.ApprovalFail`,
`sw.ApprovalDeny`, `sw.ApprovalApprove`. Unknown values panic at
plan time. (Named `OnExpiry` rather than `OnTimeout` to keep it
distinct from `Job.Timeout()`, which bounds per-attempt execution.)

Plan accessors (reads; methods on `*Plan`):

```
plan.Nodes() []*JobNode                           // all registered nodes, in declaration order
plan.Job(id) *JobNode                             // lookup by id, nil if absent
plan.LintWarnings() []sw.LintWarning               // non-fatal Plan-time advisories
plan.Expansions() []sw.Expansion                   // dynamic fan-out generators
plan.IsDynamicNode(id) bool                        // node sources runtime-variable downstream work
plan.GroupSourceIDs(id) []string                   // ExpandFrom group's source nodes
```

Job modifiers (chainable on `*JobNode`):

```
node.Needs(deps...) *JobNode                       // dependency edges
node.Env(key, value) *JobNode                      // per-node env var
group.Needs(deps...) *JobGroup                     // every member depends on deps; same chainable surface as *JobNode
```

`sw.GroupJobs(plan, name, members...)` returns a `*JobGroup` that is
both a `Needs` target (a downstream `Needs(group)` depends on every
member) and a dashboard cluster (members fold under the name; one
arrow draws into the cluster instead of one-per-member). An empty
name means "structural collection only" -- still a Needs target,
but no UI cluster. The Work-layer twin is `sw.GroupSteps(w, name,
steps...)`.

Common Plan-layer modifiers (chainable on `*JobNode`):

```
.Retry(n, opts...)                 // retry n times on failure; RetryBackoff(d) and RetryAuto() compose
.Timeout(d)                        // execution budget; child plan-admission queue wait is excluded
.Verify(fn)                        // postcondition checked after the action succeeds; non-nil fails at StageVerify
.OnFailure(id, job)                // recovery node if this node fails; job may be func(ctx, sparkwing.Failure) error to branch on stage
.SkipIf(pred, opts...)             // skip when pred(ctx) returns true; SkipBudget(d) overrides budget
.Requires(labels...)                 // require runner labels (AND semantics)
.Cache(key, TTL(d))                // content-addressed result memoization (+ in-flight dedupe)
.Concurrency(group, cost...)       // join a shared concurrency budget (count-limit, gate, throttle)
.BeforeRun(fn) / .AfterRun(fn)     // hooks
.Inline()                          // bypass the runner entirely
.ContinueOnError() / .Optional()   // failure-propagation knobs
.NeedsOptional(deps...)            // soft upstream dep
```

## Workable - the Work-bearing interface

```go
type Workable interface {
    Work(w *sw.Work) (*sw.WorkStep, error)
}
```

Every Job carries a Workable (a struct that exposes its inner DAG
via `Work`). The orchestrator constructs the `*Work` and passes it
in -- authors don't call `NewWork()`. The returned `*WorkStep` (or
`nil` for an untyped Job) is the Job's typed output: the
result-step contract is enforced on Work's return value, not on a
separate `SetResult` call.

For Jobs with no typed output, return `nil`:

```go
type Build struct{ sw.Base }

func (j *Build) Work(w *sw.Work) (*sw.WorkStep, error) {
    fetch := sw.Step(w, "fetch", j.fetch)
    sw.Step(w, "compile", j.compile).Needs(fetch)
    return nil, nil
}
```

For typed-output Jobs the contract is **strict**: the job struct
must embed `sw.Produces[T]` AND its `Work` must return a step whose
output type is `T`. Either alone is a Plan-time panic. The marker
lives on the struct, where the typed contract belongs; `sw.RefTo[T]
(node)` validates against the marker and never falls back to
inferring the type from the returned step.

For trivial single-closure Jobs (one function, no inner DAG, no
struct), pass the closure directly to `sw.Job` and skip the
Workable entirely:

```go
sw.Job(plan, "lint", p.run)   // p.run is func(ctx context.Context) error
```

The SDK wraps the closure into an internal Workable; no `JobFn`
wrapper is needed.

## Work - the inner DAG

The Work layer mirrors Plan's free-function grammar. Four adders
plus one typed reader:

```
sw.Step(w, id, fn any) *WorkStep                          // register a step (untyped or typed; see below)
sw.GroupSteps(w, name, steps...) *StepGroup               // named cluster + Needs target
sw.JobSpawn(w, id, job) *SpawnSpec                        // spawn one Plan node from inside Work
sw.JobSpawnEach(w, items, fn) *SpawnGenSpec               // spawn many Plan nodes (per-item template)
sw.StepGet[T](ctx, step) T                                // typed-read accessor for use inside step bodies
```

`sw.Step`'s `fn` is either a `func(ctx context.Context) error`
(untyped) or a `func(ctx context.Context) (T, error)` (typed). The
SDK validates the signature via reflection at register time and
stores the step's `outType` (nil for untyped, T for typed). A
wrong-shape `fn` panics at materialize time with a typed message.
A single verb covers both shapes -- the function signature is the
only declaration site for typing.

Step modifiers (chainable on `*WorkStep`):

```
step.Needs(deps...) *WorkStep                             // accepts *WorkStep, *StepGroup, *SpawnSpec, *SpawnGenSpec, []*WorkStep, string
step.SkipIf(predicate) *WorkStep                          // OR-accumulating skip predicate
step.DryRun(fn func(ctx) error) *WorkStep                 // no-mutation body run instead of the apply Fn under sparkwing X --dry-run
step.SafeWithoutDryRun() *WorkStep                        // mark the apply Fn as side-effect-free; runs unmodified under --dry-run
```

### Dry-run contract

`sparkwing X --dry-run` (and `pipeline plan --dry-run`) installs
`sparkwing.WithDryRun(ctx)` on the run-wide ctx. Each step's
dispatch then picks one of three paths:

- `step.DryRun(fn)` declared -> `fn` runs in place of the apply Fn.
  The closure must NEVER mutate state; it answers "what *would* the
  apply do" the way `terraform plan`, `kubectl apply --dry-run=server`,
  and `helm upgrade --dry-run` do for their tools.
- `step.SafeWithoutDryRun()` declared -> the apply Fn runs unchanged,
  on the author's signed contract that it has no side effects.
  Use for read-only steps (cluster discovery, fetch-only, validation)
  where authoring a separate dry-run shim would be redundant.
- Neither declared -> the step soft-skips with `step_skipped` /
  `skip_reason: no_dry_run_defined`. Existing pipelines keep working
  under `--dry-run` while the contract gap is visible in run logs.
  When paired with risk labels (`step.Risk("destructive", "prod", ...)`),
  this soft-skip tightens to a hard refusal.

For step bodies that need to branch on the mode (e.g. emit a
structured "would do X" log line for an op without a native
dry-run flag), read `sparkwing.IsDryRun(ctx)` directly -- the public
way to detect dry-run from inside a step.

`PreviewPlan` (the pipeline-binary helper behind
`sparkwing pipeline plan`) renders one of three decisions per step
under `--dry-run`: `would_dry_run` (DryRunFn defined),
`would_run` (SafeWithoutDryRun marker), or `would_skip` with
`skip_reason: no_dry_run_defined` (neither contract). Runtime
and preview always agree.

Do NOT add a `flag:"dry-run"` field to your pipeline's typed
Inputs as a roll-your-own preview mode. Declare `step.DryRun(fn)`
on the steps that mutate, and the runner-level `--sw-dry-run`
dispatches your DryRun bodies for free (see *Flag namespace* below).

`*StepGroup` (returned by `sw.GroupSteps`) is both a `Needs` target
(a downstream `step.Needs(group)` depends on every member) and a
dashboard cluster (members fold under the name in the Work view).
Initial modifiers mirror what `*WorkStep` has today:

```
group.Needs(deps...) *StepGroup                           // applies to every member
group.SkipIf(predicate) *StepGroup                        // applies to every member
```

Reads on `*Work` stay methods: `w.Steps()`, `w.StepByID(id)`,
`w.Spawns()`, `w.SpawnGens()`.

Spawn handles:

```
spawn.Needs(deps...)                                     // declare upstream Steps / Spawns
spawn.SkipIf(predicate)                                  // skip predicate before firing
```

The spawned Plan node's id is namespaced as `parent/spawnID` so logs
and the run history are unambiguous.

## Typed step composition

Inside a step body, read another step's typed output via
`sw.StepGet[T](ctx, step)`. It mirrors Plan's `Ref[T].Get(ctx)` and
exists as a free function because Go forbids generic methods.

Reach for it when a step needs to compose values from multiple
typed steps into a single returned result:

```go
type BuildOut struct {
    Tag, Platform, Hash string
}

type Build struct {
    sw.Base
    sw.Produces[BuildOut]
}

func (j *Build) Work(w *sw.Work) (*sw.WorkStep, error) {
    tag      := sw.Step(w, "tag",      j.computeTag)        // (string, error)
    platform := sw.Step(w, "platform", j.detectPlatform)    // (string, error)
    hash     := sw.Step(w, "hash",     j.computeHash)       // (string, error)

    return sw.Step(w, "compose", func(ctx context.Context) (BuildOut, error) {
        return BuildOut{
            Tag:      sw.StepGet[string](ctx, tag),
            Platform: sw.StepGet[string](ctx, platform),
            Hash:     sw.StepGet[string](ctx, hash),
        }, nil
    }).Needs(tag, platform, hash), nil
}
```

`StepGet` blocks until the upstream step's terminal completion
fires, panics on missing or mismatched type. For the common case
where the Work is one typed step whose return value IS the Job's
output, you don't need `StepGet` at all -- just return the step
from `Work`:

```go
func (j *Build) Work(w *sw.Work) (*sw.WorkStep, error) {
    return sw.Step(w, "run", j.run), nil
}
```

## Typed outputs (single field type for every routing)

Every typed dependency on another node's output is a `sw.Ref[T]`
field. The constructor in `Plan()` carries the routing detail:

| Routing | Constructor | What it does |
|---|---|---|
| In-run sibling | `sw.RefTo[T](node)` | Read a `*JobNode` in the same DAG. Implies a `Needs()` edge. |
| Cross-pipeline, passive | `sw.RefToLastRun[T](pipeline, nodeID, opts...)` | Read another pipeline's latest successful run. Does not trigger. |
| Cross-pipeline, active | `sw.RunAndAwait[Out, In](ctx, ...)` (free fn) | Trigger a fresh run of another pipeline, wait, return its output. |

```go
type Build struct {
    sw.Base
    sw.Produces[BuildOut]      // declares the contract on the struct
}

func (j *Build) Work(w *sw.Work) (*sw.WorkStep, error) {
    return sw.Step(w, "run", j.run), nil  // returned step IS the Job's typed output
}

type Deploy struct {
    sw.Base
    Build    sw.Ref[BuildOut]   // in-run
    Manifest sw.Ref[Manifest]   // cross-pipeline, same field type
}

build := sw.Job(plan, "build", &Build{})
sw.Job(plan, "deploy", &Deploy{
    Build:    sw.RefTo[BuildOut](build),                                 // wires the Needs edge
    Manifest: sw.RefToLastRun[Manifest]("manifest-pipe", "out",
                  sw.MaxAge(24*time.Hour)),                              // staleness guard
}).Needs(build)

// In step body:
b := j.Build.Get(ctx)
m := j.Manifest.Get(ctx)
```

`sw.RefTo[T]` is strict: the node's job MUST embed `sw.Produces[T]`.
Without the marker -- even if the Work returns a step of the right
type -- `sw.RefTo[T]` panics. This forces the contract to be visible
at the type level so readers and agents see it on the struct
definition alone.

Untyped pipelines (no typed output) skip both `sw.Produces[T]` and
`sw.RefTo[T]`; pass plain bytes via env vars or sibling steps.

### Imperative cross-pipeline trigger

```go
out, err := sparkwing.RunAndAwait[Out, In](ctx, "build", "artifact",
    sparkwing.WithFreshInputs(In{Service: "api"}),  // typed flag struct
    sparkwing.WithFreshTimeout(10*time.Minute),
)
```

Use `sparkwing.NoInputs` as the second type parameter when the target
pipeline takes no flags. Cross-repo callers without import access to
the target's Inputs type pass `sparkwing.NoInputs` and use the escape
hatch `sparkwing.WithFreshArgs(map[string]string{...})`.

When `RunAndAwait` inherits the caller job's `.Timeout(d)`, child
plan-admission queue wait is excluded from that execution budget. An
explicit `WithFreshTimeout(d)` is different: it bounds the total wait
for the child run, including admission queue time.

## Secrets and config

```
Secret(ctx, name) (string, error)        // resolve a cluster secret; auto-masked in logs
MustSecret(ctx, name) string             // panic on miss
Config(ctx, name) (string, error)        // unmasked config value
MustConfig(ctx, name) string             // panic on miss
```

**Call from step bodies or CacheKey functions, not from `Plan()`.**
The orchestrator installs the resolver on the run ctx at dispatch
time, *after* every pipeline's `Plan()` has returned. Calling
`Secret`/`Config` (or their `Must*` forms) from inside `Plan()`
returns `no resolver installed` / panics. This is consistent with the
"Plan() must be pure" rule above: `Plan()` declares the graph; values
are resolved when the graph runs.

```go
// Wrong: reads config at Plan time -- no resolver installed yet.
func (b *Build) Plan(ctx context.Context, plan *sw.Plan, _ sw.NoInputs, rc sw.RunContext) error {
    region := sw.MustConfig(ctx, "REGION") // panics
    sw.Job(plan, "build", func(_ context.Context) error { return doBuild(region) })
    return nil
}

// Right: defer the lookup into the step body.
func (b *Build) Plan(_ context.Context, plan *sw.Plan, _ sw.NoInputs, rc sw.RunContext) error {
    sw.Job(plan, "build", func(ctx context.Context) error {
        region, err := sw.Config(ctx, "REGION")
        if err != nil { return err }
        return doBuild(region)
    })
    return nil
}
```

CacheKey functions also run at dispatch time, so they may call
`Secret`/`Config` directly.

Register a custom resolver for tests:
`WithSecretResolver(ctx, SecretResolverFunc(...))`.

## Trigger inputs from step bodies

The pipeline's `Plan(ctx, plan, in T, rc)` method receives the typed
Inputs once. To read the same value from a step body deep in the
DAG without threading it through closures or job-struct fields,
call `sw.Inputs[T](ctx)`:

```go
type DeployArgs struct {
    Service string `flag:"service"`
    Env     string `flag:"env" default:"staging"`
}

func (Deploy) Plan(ctx context.Context, plan *sw.Plan, _ DeployArgs, rc sw.RunContext) error {
    sw.Job(plan, "deploy", func(ctx context.Context) error {
        args := sw.Inputs[DeployArgs](ctx)
        return runDeploy(ctx, args.Service, args.Env)
    })
    return nil
}
```

Panics outside a dispatch ctx (no installer) or on a wrong concrete
type. The orchestrator installs the parsed Inputs on every node's
runner ctx automatically.

For tests outside the orchestrator boundary:
`WithInputs(ctx, args) context.Context`.

## Pipeline registration

In `.sparkwing/jobs/<name>.go`:

```go
import sw "github.com/sparkwing-dev/sparkwing/sparkwing"

type Inputs struct {
    SkipTests bool   `flag:"skip-tests" desc:"skip the test suite"`
    Target    string `flag:"target" default:"local" enum:"local,staging,prod"`
}

type MyPipeline struct{ sw.Base }

func (MyPipeline) Plan(ctx context.Context, plan *sw.Plan, in Inputs, rc sw.RunContext) error {
    sw.Job(plan, "test", func(ctx context.Context) error {
        if in.SkipTests { return nil }
        _, err := sw.Bash(ctx, "go test ./...").Run()
        return err
    })
    return nil
}

func init() {
    sw.Register[Inputs]("my-pipeline", func() sw.Pipeline[Inputs] {
        return MyPipeline{}
    })
}
```

For pipelines that take no flags, use `sw.NoInputs`:

```go
sw.Register[sw.NoInputs]("lint", func() sw.Pipeline[sw.NoInputs] {
    return Lint{}
})
```

The pipeline struct embeds `sw.Base` and optionally exposes
`ShortHelp() / Help() / Examples()` for the `sparkwing run <name> --help`
screen.

## Typed Inputs

Each pipeline declares exactly one Inputs type. Field tags drive CLI
parsing, `--help`, schema introspection (`sparkwing pipeline describe
--name X -o json`), shell completion, dashboard run-form, and MCP
tool definitions.

```
`flag:"name"`            // Required on every input field. Uses dash-case.
`short:"x"`              // Optional one-letter alias (e.g. -v alongside --verbose)
`desc:"text"`            // Human description shown in --help
`default:"value"`        // Default when flag is not provided
`required:"true"`        // Errors when missing (mutex with default)
`enum:"a,b,c"`           // Allowed values; requires default-or-required
`secret:"true"`          // Mask in logs and dashboard
`flag:",extra"`          // Catch-all for unknown flags; map[string]string only
```

Supported field types: `string`, `bool`, `int`, `int64`, `float64`,
`time.Duration`, `[]string` (comma-separated on the wire), and
`map[string]string` (only with `,extra`).

Unknown flags are an error by default. To opt into forwarding (e.g.
to wrap an inner tool), declare a single `map[string]string` field
with `flag:",extra"`:

```go
type WrapperInputs struct {
    Image string            `flag:"image" required:"true"`
    Extra map[string]string `flag:",extra"`
}
```

### Flag namespace: `--sw-*` vs your flags

`sparkwing run` keeps its own control flags out of your way by
prefixing every one of them with `sw-`:

```
-C, --sw-cd PATH          // re-anchor .sparkwing/ discovery
    --sw-ref REF          // compile the pipeline at a git ref
-v, --sw-verbose          // debug logging
    --sw-start-at STEP    // start the run at STEP
    --sw-stop-at STEP     // stop the run after STEP
    --sw-only GLOB        // run only matching jobs (+ their Needs)
    --sw-no-cache         // ignore cached per-node results
    --sw-local-only       // force local state/cache/logs
    --sw-dry-run          // run each step's dry-run probe
    --sw-allow LABEL,...  // authorize risk-labeled steps
    --sw-no-update        // skip the sparks auto-resolve step
```

Because the runner owns the `sw-` prefix, your pipeline `flag:"..."`
tags have the entire unprefixed namespace to themselves -- there is no
reserved-name collision check, and a field named `flag:"ref"` or
`flag:"verbose"` resolves to *your* flag, not the runner's. Any flag
`run` doesn't recognize is forwarded to the pipeline binary as a typed
Arg.

The only non-`sw-` flags `run` consumes itself are `--profile` and
`--target` (storage / deployment-target selection); avoid those two
names and the `sw-` prefix for pipeline inputs.

For a `--dry-run`-style flag, prefer `step.DryRun(fn)` on each mutating
step (see *Work - the inner DAG > Dry-run contract*) over a
`flag:"dry-run"` input; the runner-level `--sw-dry-run` then dispatches
your DryRun bodies for free.

## Cache

`.Cache(key, opts...)` is content-addressed result memoization: same
content, compute once, reuse the result. It carries no scope and no
group -- that is [Concurrency](#concurrency)'s job.

```go
sw.Key("go-mod", "1.26", "abc123") // a CacheKey from any parts

node := sw.Job(plan, "build", func(ctx context.Context) error { return nil })
node.Cache(func(ctx context.Context) sw.CacheKey {
    return sw.Key("build", "linux", "amd64")
}, sw.TTL(24*time.Hour))
```

- `key` is a `CacheKeyFn` -- `func(ctx) CacheKey`. It runs at dispatch
  time, after upstream deps resolve, so it can read `Ref[T]` output.
- `TTL(d)` bounds retention; omit for `DefaultCacheTTL` (7d), capped at
  `MaxCacheTTL` (35d).
- Return `sw.NoCache` from the key fn to run uncached for that
  invocation.
- Identical content that is in flight dedupes automatically: one
  computes, the rest wait and replay. No policy needed.

See [caching.md](caching.md) for the full model. The `JobGroup` mirror
is `group.Cache(key, opts...)`.

## Concurrency

`.Concurrency(group, cost...)` enrolls a node in a named budget shared
by its members: different work taking turns under a cap. Define the
group once and pass the handle to each member.

```go
dbGroup := sw.NewConcurrencyGroup("db", sw.ConcurrencyLimit{
    Capacity:     8,
    Scope:        sw.ScopeBox,
    OnLimit:      sw.Queue,
    QueueTimeout: 30 * time.Second,
})
sw.Job(plan, "shard-1", func(ctx context.Context) error { return nil }).Concurrency(dbGroup, 4)
sw.Job(plan, "shard-2", func(ctx context.Context) error { return nil }).Concurrency(dbGroup, 4)
```

`Capacity` and `cost` are integers in author-defined units (a slot, a
gigabyte, a database container). Admission compares the summed `cost` of
live members in the scope plus this member's cost against `Capacity`.
Count-limiting ("at most N at once") is the degenerate case: capacity
`N`, every member the default `cost` of 1.

```go
deployGate := sw.NewConcurrencyGroup("deploy-prod", sw.ConcurrencyLimit{
    Capacity: 1,
    OnLimit:  sw.Queue,
})
sw.Job(plan, "deploy", func(ctx context.Context) error { return nil }).Concurrency(deployGate)
```

### OnLimit

What a member does when its group is at capacity:

- `Queue` (default) -- wait FIFO for room, then run.
- `Fail` -- error immediately.
- `Skip` -- resolve as a no-op without running.
- `CancelOthers` -- evict running members oldest-first until this one
  fits (best-effort; side effects already committed are not rolled
  back).

Sharing another member's result is not an option here -- a group is
different work taking turns, never the same work. Result reuse is
[Cache](#cache).

### Scope

`Scope` selects how far the budget reaches; it folds into the
coordination key as `name@<id>`:

- `ScopeRun` -- key `name@<runID>`: only this run's nodes share the
  budget.
- `ScopeBox` -- key `name@<hostID>`: every run on one machine shares it,
  even under a controller.
- `ScopeGlobal` (the zero value) -- key `name`: the whole fleet shares
  it through the coordination backend.

`hostID` for `ScopeBox` is `os.Hostname()`, overridable via
`SPARKWING_BOX_ID`. Inside a container the hostname is per-container, so
two containers on one physical host would each get their own box budget;
set `SPARKWING_BOX_ID` to the physical host identity when you want
per-machine budgeting across containers.

### Capacity skew: most-restrictive wins

Two pipeline versions running against one controller can declare the
same group with different `Capacity`. The store enforces the **minimum**
over live participants, not the last writer -- a cap is a safety
constraint, so the only value that honors every live participant is the
smallest. Lowering takes effect immediately; raising waits for the
last participant declaring the lower value to drain. A drift warning
fires so the skew is visible.

### Timeouts

- `QueueTimeout` (with `Queue`) bounds the wait; on expiry the node
  fails with `failure_reason: queue_timeout` and the waiter leaves the
  queue, so a later release won't hand the slot to a run that gave up.
  Zero waits indefinitely.
- `CancelTimeout` (with `CancelOthers`) bounds how long the arrival
  waits for evicted holders to release before the slot is force-freed.

### Gate-shaped pipelines: queue, don't fail

When several runs contend for one shared resource -- a deploy slot, a
migration lock, a single-writer index -- reach for a capacity-1 group
with `OnLimit: Queue`, not `Fail`. `Fail` pushes a poll-and-retry loop
onto every caller and aborts the loser with "slot full". `Queue` lines
arrivals up FIFO and runs them one at a time, with `QueueTimeout` as the
bounded way out.

### Whole-run coordination

A plan can take one unit of a group before any node dispatches and
release it when the run reaches a terminal status. A plan never
memoizes, so this is concurrency only:

```go
plan.Concurrency(sw.NewConcurrencyGroup("whole-run-prod", sw.ConcurrencyLimit{
    Capacity: 1,
    OnLimit:  sw.Fail,
}))
```

The `JobGroup` mirror is `group.Concurrency(handle, cost...)`.

## Discovery

- `sparkwing docs read --topic pipelines` - conceptual tour
- `sparkwing docs read --topic sdk` - this page
- `sparkwing docs all` - every doc concatenated (one stdout dump for agents)
- `sparkwing pipeline explain --name X [-o json]` - render the full
  Plan -> Job -> Work -> Step tree before running
- [`pipelines.md`](pipelines.md) - the conceptual Plan/Work tour
