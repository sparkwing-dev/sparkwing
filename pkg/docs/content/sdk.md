# SDK Reference

Flat reference of every helper in the `sparkwing` package an agent
or human is likely to call from a `.sparkwing/jobs/*.go` file. Type
signatures and one-line summaries - designed to be loaded once at the
start of a pipeline-authoring task.

For the conceptual tour (Plan, Node, Job, Work, modifiers,
`pipelines.yaml` shape), read [pipelines](pipelines.md). This page is
the authoritative SDK reference for the `sparkwing` Go package.

The convention is to import the SDK under the alias `sw`:

```go
import sw "github.com/sparkwing-dev/sparkwing/sparkwing"
```

Every example below uses that alias. The package itself is named
`sparkwing` -- the alias just keeps the call sites short.

## Read/write split

Operations that mutate the Plan are **free functions on `sparkwing`**;
operations that read the Plan are **methods on `*Plan`**. Go forbids
generic methods, so the typed adders (RefTo[T], JobFanOut[T]) must
be free functions; for symmetry every adder lives there. Reads stay
on `*Plan` because they don't have the same constraint and the
`plan.X()` shape reads naturally for accessors.

| Mutate (free funcs) | Read (methods) |
|---|---|
| `sw.Job(plan, id, job)` | `plan.Nodes()` |
| `sw.JobFanOut(plan, name, items, fn)` | `plan.Node(id)` |
| `sw.JobFanOutDynamic(plan, name, source, fn)` | `plan.LintWarnings()` |
| `sw.Group(plan, name, members...)` | `plan.Expansions()` |
| `sw.RefTo[T](node)` | `plan.IsDynamicNode(id)` / `plan.GroupSourceIDs(id)` |

## The two-layer model

Sparkwing has two DAGs: **Plan / Node** (the outer DAG, units of
dispatch) and **Work / WorkStep** (the inner DAG, units of work
within one Node's runner). Plan-only modifiers - Retry, Timeout,
OnFailure, Cache, RunsOn, BeforeRun / AfterRun, Approval, Inline -
live on `*Node`. The inner DAG carries `Needs` and `SkipIf` only.

Every Job's `Work()` runs at Plan-time, so renderers
(`sparkwing pipeline explain`, the dashboard) walk the full reachable
nested DAG before any dispatch.

The non-typed step type is named **`WorkStep`** (rather than `Step`)
because the historical `sparkwing.Step` package-level call was a log
breadcrumb that has been replaced with structured `step_start` /
`step_end` events. The inner-DAG entity carries the suffix to keep
the rename auditable.

### Cost grid

| API | Layer | Cardinality | Cost |
|---|---|---|---|
| `sw.Job(plan, id, job)` | Plan | one, declared at Plan-time | normal node |
| `sw.JobFanOut(plan, name, items, fn)` | Plan | many, items in hand at Plan-time | normal nodes; one per element |
| `sw.JobFanOutDynamic(plan, name, source, fn)` | Plan | many, source's runtime output | source runner exits before fan-out - no stranded compute |
| `w.Step(id, fn)` | Work | one, in-process unit of work | one logging frame, ordered/parallel via Needs |
| `w.SpawnNode(id, job)` | Work | one, decided mid-Work | spawning runner stays suspended until child completes |
| `w.SpawnNodeForEach(items, fn)` | Work | many, mid-Work fan-out | spawning runner stays suspended across all children |

The verb tells you the cost: `Node*` is cheap; `SpawnNode*` flags
the layer jump and the suspended-runner cost. Reach for `SpawnNode`
when you genuinely need Plan-only modifiers on a unit decided
mid-execution.

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

func (j *DiscoverBuildContextJob) Work() *sw.Work {
    w := sw.NewWork()
    sw.Result(w, "run", j.run)
    return w
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
guard by calling `sparkwing.GuardPlanTime(ctx, "yourpkg.Helper")`
at the top.

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
and run `wing` from the Git Bash terminal it ships** -- the same dep
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
```

Per-level methods only -- the level lives in the verb name, no
level-as-string arg. Same printf-style format-args contract across
all four. Each call goes through the `Logger` installed in ctx and
is stamped with the current Node and Job-stack envelope.

Step boundaries are emitted automatically by `RunWork` as structured
`step_start` / `step_end` events; the renderer surfaces them as a
collapsible bucket in the CLI and dashboard. The pre-rewrite
package-level `sparkwing.Step` / `sparkwing.StepErr` log breadcrumbs
are gone.

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
rc.Trigger  TriggerInfo    // {Source: "push|manual|schedule|webhook", User, Env}
```

Most one-step Plans don't need `rc` at all - the parameter is named
for the moment a Plan starts branching on trigger source / SHA.

Free-function adders (writes; mutate the Plan):

```
sw.Job(plan, id, job sw.Workable) *Node                                       // register a Job
sw.JobFanOut[T](plan, name, items, fn) *NodeGroup                             // Plan-time static fan-out
sw.JobFanOutDynamic[T](plan, name, source, fn) *NodeGroup                     // runtime fan-out after source completes
sw.Group(plan, name, members...) *NodeGroup                                   // named cluster + Needs target (name="" = unnamed)
sw.RefTo[T](node) sw.Ref[T]                                                  // typed Ref into node's typed output
```

Approval gates register through `sw.Job` with the built-in
`*sw.Approval` job type:

```go
approve := sw.Job(plan, "approve-prod", &sw.Approval{
    Message:  fmt.Sprintf("Promote %s to prod?", git.SHA),
    Timeout:  2 * time.Hour,
    OnExpiry: sw.ApprovalFail,
}).Needs(integStg)
```

`OnExpiry` defaults to fail; valid values are `sw.ApprovalFail`,
`sw.ApprovalDeny`, `sw.ApprovalApprove`. Unknown values panic at
plan time. (Named `OnExpiry` rather than `OnTimeout` to keep it
distinct from `Node.Timeout()`, which bounds per-attempt execution.)

Plan accessors (reads; methods on `*Plan`):

```
plan.Nodes() []*Node                               // all registered nodes, in declaration order
plan.Node(id) *Node                                // lookup by id, nil if absent
plan.LintWarnings() []sw.LintWarning               // non-fatal Plan-time advisories
plan.Expansions() []sw.Expansion                   // dynamic fan-out generators
plan.IsDynamicNode(id) bool                        // dynamic source or .Dynamic()
plan.GroupSourceIDs(id) []string                   // ExpandFrom group's source nodes
```

Node modifiers (chainable on `*Node`):

```
node.Needs(deps...) *Node                          // dependency edges
node.Env(key, value) *Node                         // per-node env var
group.Needs(deps...) *NodeGroup                    // every member depends on deps; same chainable surface as *Node
```

`sw.Group(plan, name, members...)` returns a `*NodeGroup` that is
both a `Needs` target (a downstream `Needs(group)` depends on every
member) and a dashboard cluster (members fold under the name; one
arrow draws into the cluster instead of one-per-member). An empty
name means "structural collection only" -- still a Needs target,
but no UI cluster.

Common Plan-layer modifiers (chainable on `*Node`):

```
.Retry(n, opts...)                 // retry n times on failure; RetryBackoff(d) and RetryAuto() compose
.Timeout(d)                        // hard kill after d
.OnFailure(id, job)                // run a recovery node if this node fails
.SkipIf(pred, opts...)             // skip when pred(ctx) returns true; SkipBudget(d) overrides budget
.RunsOn(labels...)                 // require runner labels (AND semantics)
.Cache(CacheOptions{...})          // coordination + memoization
.BeforeRun(fn) / .AfterRun(fn)     // hooks
.Inline()                          // bypass the runner entirely
.Dynamic()                         // mark runtime-variable downstream shape
.ContinueOnError() / .Optional()   // failure-propagation knobs
.NeedsOptional(deps...)            // soft upstream dep
```

## Workable - the Work-bearing interface

```go
type Workable interface {
    Work() *Work
}
```

Every Node carries a Workable (a struct that exposes its inner DAG
via `Work()`). `Work()` runs at Plan-time and returns the Node's
inner DAG. The trivial untyped case has a sugar constructor:

```
sw.JobFn(fn func(ctx) error) sw.Workable             // single-step Workable
```

For typed-output Jobs the contract is **strict**: the job
struct must embed `sw.Produces[T]` AND its `Work().SetResult` must
land on a step of type `T`. Either alone is a Plan-time panic. The
marker lives on the struct, where the typed contract belongs;
`sw.RefTo[T](node)` validates against the marker and never falls
back to inferring the type from `Work.SetResult`.

For multi-step Jobs, define a struct with a `Work()` method:

```go
type BuildJob struct{ sw.Base }

func (j *BuildJob) Work() *sw.Work {
    w := sw.NewWork()
    fetch := w.Step("fetch", j.fetch)
    w.Step("compile", j.compile).Needs(fetch)
    return w
}
```

## Work - the inner DAG

```
NewWork() *Work

// Step registration
w.Step(id, fn func(ctx) error) *WorkStep                // basic step
sparkwing.Result[T](w, id, fn func(ctx) (T, error)) *TypedStep[T]  // typed step + Work result (the common case)
sparkwing.Out[T](w, id, fn func(ctx) (T, error)) *TypedStep[T]     // typed step alone (multi-typed-step Works)

// Result wiring
w.SetResult(step *WorkStep) *Work                        // mark step as the Node's typed output (paired with Out for multi-typed-step Works)

// Combinators
w.Sequence(steps...) *WorkStep                           // wires Needs between consecutive steps
w.Parallel(steps...) *StepGroup                          // groups steps for downstream fan-in

// Layer escape
w.SpawnNode(id, job) *SpawnHandle                        // spawn one Plan node from inside Work
w.SpawnNodeForEach(items, fn) *SpawnGroup                // spawn many Plan nodes (per-item template)
```

Step modifiers:

```
step.Needs(deps...) *WorkStep                            // accepts *WorkStep, *TypedStep[T], *StepGroup, *SpawnHandle, *SpawnGroup, []*WorkStep, string
step.SkipIf(predicate) *WorkStep                         // OR-accumulating skip predicate
```

`*TypedStep[T]` (returned by `sparkwing.Out`) embeds `*WorkStep` and
adds `.Get(ctx) T` for downstream Steps to read the typed result.

Spawn handles:

```
spawn.Needs(deps...)                                     // declare upstream Steps / Spawns
spawn.SkipIf(predicate)                                  // skip predicate before firing
```

The spawned Plan node's id is namespaced as `parent/spawnID` so logs
and the run history are unambiguous.

## Typed outputs (single field type for every routing)

Every typed dependency on another node's output is a `sw.Ref[T]`
field. The constructor in `Plan()` carries the routing detail:

| Routing | Constructor | What it does |
|---|---|---|
| In-run sibling | `sw.RefTo[T](node)` | Read a `*Node` in the same DAG. Implies a `Needs()` edge. |
| Cross-pipeline, passive | `sw.RefToLastRun[T](pipeline, nodeID, opts...)` | Read another pipeline's latest successful run. Does not trigger. |
| Cross-pipeline, active | `sw.RunAndAwait[Out, In](ctx, ...)` (free fn) | Trigger a fresh run of another pipeline, wait, return its output. |

```go
type BuildJob struct {
    sw.Base
    sw.Produces[BuildOut]      // declares the contract on the struct
}

func (j *BuildJob) Work() *sw.Work {
    w := sw.NewWork()
    sw.Result(w, "run", j.run) // runs `func(ctx) (BuildOut, error)` and SetResults it
    return w
}

type DeployJob struct {
    sw.Base
    Build    sw.Ref[BuildOut]   // in-run
    Manifest sw.Ref[Manifest]   // cross-pipeline, same field type
}

build := sw.Job(plan, "build", &BuildJob{})
sw.Job(plan, "deploy", &DeployJob{
    Build:    sw.RefTo[BuildOut](build),                                 // wires the Needs edge
    Manifest: sw.RefToLastRun[Manifest]("manifest-pipe", "out",
                  sw.MaxAge(24*time.Hour)),                              // staleness guard
}).Needs(build)

// In step body:
b := j.Build.Get(ctx)
m := j.Manifest.Get(ctx)
```

`sw.RefTo[T]` is strict: the node's job MUST embed `sw.Produces[T]`.
Without the marker -- even if the Work has a SetResult of the right
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

## Secrets and config

```
Secret(ctx, name) (string, error)        // resolve a cluster secret; auto-masked in logs
MustSecret(ctx, name) string             // panic on miss
Config(ctx, name) (string, error)        // unmasked config value
MustConfig(ctx, name) string             // panic on miss
```

Register a custom resolver for tests:
`WithSecretResolver(ctx, SecretResolverFunc(...))`.

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
    sw.Job(plan, "test", sw.JobFn(func(ctx context.Context) error {
        if in.SkipTests { return nil }
        _, err := sw.Bash(ctx, "go test ./...").Run()
        return err
    }))
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
`ShortHelp() / Help() / Examples()` for the `wing <name> --help`
screen.

## Typed Inputs

Each pipeline declares exactly one Inputs type. Field tags drive CLI
parsing, `--help`, schema introspection (`sparkwing pipeline describe
--pipeline X --json`), shell completion, dashboard run-form, and MCP
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

## Cache

```
sw.Key("go-mod", goVersion, fileHash)             // CacheKey from any parts
node.Cache(sw.CacheOptions{
    Key:      "build",                                // required: coordination key
    Max:      3,                                      // optional: semaphore (default 1 = mutex)
    OnLimit:  sw.Queue,                               // Queue (default), Coalesce (node-only), CancelOthers
    CacheKey: func(ctx) sw.CacheKey { return ... },   // optional: result memoization
    CacheTTL: 24*time.Hour,                           // optional: cache lifetime
    CancelTimeout: 60*time.Second,                    // CancelOthers wait budget
})
```

`.Cache()` is the unified coordination + memoization primitive (it
replaces the pre-rewrite `.Exclusive(group)` and `.CacheKey(fn)`).
Empty `Key` is a no-op.

## Discovery

- `sparkwing docs read --topic pipelines` - conceptual tour
- `sparkwing docs read --topic sdk` - this page
- `sparkwing docs all` - every doc concatenated (one stdout dump for agents)
- `sparkwing pipeline explain --name X [--json]` - render the full
  Plan -> Node -> Work -> Step tree before running
- [`pipelines.md`](pipelines.md) - the conceptual Plan/Work tour
