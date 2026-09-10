# SDK Reference

This guide covers pipeline authoring with the `sparkwing` package.
The generated [API reference](sdk-reference.md) lists exported signatures;
`sdk-<name>.md` pages cover subpackages. Read them offline with
`sparkwing docs read --topic sdk-reference` or `--topic sdk-<name>`.
See [Pipelines](pipelines.md) for the Plan/Work model and project YAML.

Examples import `sparkwing` by its package name or the alias `sw`:

```go
import sw "github.com/sparkwing-dev/sparkwing/sparkwing"
```

## Read/write split

DAG adders are package functions; container methods read the graph.
Both layers use `sw.<Verb>(<container>, ...args).<modifier>(...)`.

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

A Plan holds jobs, which are dispatch units. Each job's Work graph holds
steps that execute in the job's process. See [Pipelines](pipelines.md).

## Plan() must be pure

`Pipeline.Plan(ctx, plan, in, rc)` declares the DAG by registering
nodes on the passed-in `*Plan` and returns `error`. The SDK
constructs the `*Plan` and hands it in -- authors don't call
`NewPlan()`. Plan() must not run work: calling `sparkwing.Bash` /
`Exec`, anything in `sparkwing/docker`, anything in `sparkwing/git`,
or any other helper that touches state inside `Plan()` panics at
runtime with a message naming the helper and pointing back here.

`Plan` and `Work` construct the graph during inspection as well as
execution. Put side effects in registered job or step callbacks, and
return typed outputs for downstream `Ref[T]` consumers.

`.Inline()` selects the dispatcher's host. A local inline job still runs
in its own process, and its `Work` method still constructs the graph
before dispatch.

Consumer-side helper packages can opt their own ctx-taking entry points into the
guard by calling `planguard.Guard(ctx, "yourpkg.Helper")` at the top
(import `github.com/sparkwing-dev/sparkwing/sparkwing/planguard`).

## Exec and Bash - running a shell command in a step

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
and run `sparkwing` from its Git Bash terminal**. `Exec` runs an argument
vector directly; use it for commands that need no shell features.

`Bash` takes the shell program verbatim - there's no printf-style
formatting. Splice dynamic *values* into a shell command by
passing them through `.Env("KEY", value)` and referencing `"$KEY"`
inside the line; the shell expands the variable safely. Splice dynamic
*argv* through `Exec(ctx, name, args...)`.

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
packages, _ := sparkwing.Bash(ctx, "go list ./...").Lines()
var pods PodList
sparkwing.Exec(ctx, "kubectl", "get", "pods", "-o", "json").JSON(&pods)
sparkwing.Exec(ctx, "go", "test", "./...").Dir("internal").Env("CGO_ENABLED", "0").Run()
```

`ExecError` carries `Command`, `Stdout`, `Stderr`, `ExitCode`, and a
wrapped `Cause`. `errors.As(err, &ee)` works through every terminator
(including `JSON` and `MustBeEmpty`).

### Tool caches

```
ToolCacheDir(tool) string              // cache dir for an external tool, scoped to this worktree
```

A tool that keys its cache on file content alone - golangci-lint among
them - replays a stored result for identical input no matter which
checkout produced it. Two worktrees of one repo sharing that tool's
default cache therefore see each other's results: a run in one reports
the other's file paths, including paths from a worktree that has since
been deleted. Hand the tool a cache scoped to the worktree instead:

```go
sparkwing.Bash(ctx, "golangci-lint run ./...").
    Env("GOLANGCI_LINT_CACHE", sparkwing.ToolCacheDir("golangci-lint")).
    Run()
```

The path derives from `WorkDir()`. Runs in one worktree share a cache;
each worktree has its own cache.

`SaveLintCache` and `RestoreLintCache` operate on
`ToolCacheDir("golangci-lint")` for the current `WorkDir()` and take no
directory argument, so a fixed-workdir runner seeds the same cache it
lints with.

Each worktree keeps its own cache, and pays a cold first lint for it.
A stored issue carries the absolute path of the tree that produced it,
so a cache shared between worktrees replays paths that belong to
another tree. Lending the run a stable alias path resolves those paths
and breaks the report instead: git resolves the alias to the real
worktree and calls that the repository root, so every replayed finding
sits outside the diff and a baseline such as golangci-lint's
`new-from-merge-base` drops it -- a tree with eight findings lints
clean in three seconds.

Running two lint jobs at once is a different problem, and a scoped
cache does not touch it. golangci-lint takes its parallel-runner lock
on `golangci-lint.lock` in the OS temp directory, so every run on the
box contends on one file no matter where `GOLANGCI_LINT_CACHE` points -
only `TMPDIR` moves it. By default a run that cannot take the lock
retries for 5s and then exits `parallel golangci-lint is running`,
which a gate reports as a lint failure against a tree that is fine.

Pass `--allow-serial-runners` so the run waits its turn instead:

```go
sparkwing.Bash(ctx, "golangci-lint run --allow-serial-runners ./...").
    Env("GOLANGCI_LINT_CACHE", sparkwing.ToolCacheDir("golangci-lint")).
    Run()
```

It waits on golangci-lint's own lock, so it queues behind every other
golangci-lint on the box, including runs that know nothing about
sparkwing - one pipeline adopting it stops failing straight away, with
no fleet-wide agreement needed. The flag waits indefinitely, so bound
it with a context deadline, or one wedged linter pins every gate on the
machine. A deadline that fires establishes that the step did not
finish, never why: report it as could-not-run with the time waited, and
keep the word contention for a run that printed `parallel golangci-lint
is running`.

`--allow-parallel-runners` drops the lock instead of waiting on it,
which lets N linters share the box's CPU and memory at once. Prefer it
only where the machine has headroom to spare.

## Files

```
Path(parts...) string                       // join onto WorkDir(); abs first part wins
ReadFile(path) ([]byte, error)              // os.ReadFile, relative -> WorkDir()
WriteFile(path, data) error                 // os.WriteFile, perm 0o644
Glob(pattern) ([]string, error)             // filepath.Glob, returns absolute paths
```

When invoked outside any sparkwing project (no `.sparkwing/`
discoverable above cwd), the relative-path forms of `ReadFile` /
`WriteFile` / `Glob` return `sparkwing.ErrNoProject` (wrapped). `Path`
has no error return, so it panics with `ErrNoProject` instead.
Absolute inputs work without a project.

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

These helpers send node output to run records and the dashboard.

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
plain `func(ctx context.Context) error` for the single-closure
case. Reflection at register time accepts either form. Anything else
panics at materialize time.

Approval gates register through `sw.JobApproval` and return an
`*ApprovalGate` -- a narrower modifier surface than `*JobNode` so the
modifiers that don't apply to gates (`Retry`, `Timeout`, `Memoize`,
`Requires`, `Inline`) are physically absent and a misuse is a compile
error:

```go
approve := sw.JobApproval(plan, "approve-prod", sw.ApprovalConfig{
    Message:  fmt.Sprintf("Promote %s to prod?", git.SHA),
    Timeout:  2 * time.Hour,
    OnExpiry: sw.ApprovalFail,
}).Needs(stagingChecks)

sw.Job(plan, "fictional-deploy-prod", &Deploy{}).Needs(approve)
```

Available modifiers on `*ApprovalGate`: `Needs`, `NeedsOptional`,
`OnFailure`, `BeforeRun`, `AfterRun`, `SkipIf`, `Optional`,
`ContinueOnError`. Plus `Job()` as the accessor when an author
needs the underlying `*JobNode`.

`OnExpiry` defaults to fail; valid values are `sw.ApprovalFail`,
`sw.ApprovalDeny`, `sw.ApprovalApprove`. Unknown values panic at
plan time. `Job.Timeout()` bounds per-attempt execution.

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
.Timeout(d)                        // absolute per-attempt execution budget
.NoProgressTimeout(d)              // per-attempt inactivity budget; node log records reset it
.Verify(fn)                        // postcondition checked after the action succeeds; non-nil fails at StageVerify
.OnFailure(id, job)                // recovery node if this node fails; job may be func(ctx, sparkwing.Failure) error to branch on stage
.SkipIf(pred, opts...)             // skip when pred(ctx) returns true; SkipBudget(d) overrides budget
.Requires(labels...)               // require runner labels (comma = OR within a term, AND across terms)
.Memoize(key, TTL(d))                // content-addressed result memoization (+ in-flight dedupe)
.Concurrency(group, cost...)       // join a shared concurrency budget (count-limit, gate, throttle)
.BeforeRun(fn) / .AfterRun(fn)     // hooks
.Inline()                          // run on the dispatcher's host, not a runner
.ContinueOnError() / .Optional()   // failure-propagation knobs
.NeedsOptional(deps...)            // soft upstream dep
```

Use `NoProgressTimeout` to stop attempts that cease producing observable
progress. Each node log record resets the inactivity window, including step
transitions, `Info` / `Warn` / `Error`, command starts, and complete output
lines from `Exec(...).Run()`. The timer covers the action and its `Verify`
postcondition. It starts fresh for each retry.

Node admission, hooks, retry backoff, delegated child execution, and tool-slot
admission do not consume the inactivity budget. Cached nodes do not start it.
Captured commands are silent after their command-start record, so a long
`Capture`, `String`, `Lines`, `JSON`, or `MustBeEmpty` call can exceed the
budget even while its subprocess is healthy. Use streaming `Run`, report
progress from the job, or omit `NoProgressTimeout` when silence is expected.

Pair it with a longer `Timeout` when continuing progress must not make an
attempt unbounded:

```go
sw.Job(plan, "fictional-index", &Index{}).
    NoProgressTimeout(2 * time.Minute).
    Timeout(30 * time.Minute)
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
`nil` for an untyped job) selects the job's typed output.

For Jobs with no typed output, return `nil`:

```go
type Build struct{ sw.Base }

func (j *Build) Work(w *sw.Work) (*sw.WorkStep, error) {
    fetch := sw.Step(w, "fetch", j.fetch)
    sw.Step(w, "compile", j.compile).Needs(fetch)
    return nil, nil
}
```

A typed-output job embeds `sw.Produces[T]` and returns a step producing
`T` from `Work`. A mismatch panics during materialization.

For single-closure Jobs (one function, no inner DAG, no
struct), pass the closure directly to `sw.Job` and skip the
Workable entirely:

```go
sw.Job(plan, "fictional-lint", p.run)
```

The SDK wraps the closure into a Workable.

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
step.Needs(deps...) *WorkStep                             // accepts *WorkStep, *StepGroup, *SpawnSpec, *SpawnGenSpec; splat a slice: s.Needs(steps...)
step.SkipIf(predicate) *WorkStep                          // OR-accumulating skip predicate
step.Finally() *WorkStep                                 // cleanup after declared deps terminate, even after sibling failure
step.DryRun(fn func(ctx) error) *WorkStep                 // no-mutation body run instead of the apply Fn under --sw-dry-run
step.SafeWithoutDryRun() *WorkStep                        // mark the apply Fn as side-effect-free; runs unmodified under --sw-dry-run
```

Choose the parallel failure policy once per Work:

```go
w.ParallelFailures(sw.FailFast)
w.ParallelFailures(sw.CollectAll)
```

Fail-fast records the triggering step, cancelled sibling count, and
cancellation latency in `work_fail_fast` telemetry. Cancelled steps end with
`outcome=cancelled`, never `failed`. Collect-all never turns a failed `.Needs()`
prerequisite into success; a dependent dispatches only after success or when
the prerequisite explicitly uses `.ContinueOnError()`. Cleanup steps should
declare every item they clean up with `.Needs(...)` and add `.Finally()`; they
run through the Job's parent context, so sibling failure does not cancel them
while an operator cancellation still does.

### Dry-run contract

`sparkwing run <pipeline> --sw-dry-run` installs a dry-run flag on the
run-wide ctx -- detect it with `IsDryRun(ctx)`. Each step's dispatch
then picks one of three paths:

- `step.DryRun(fn)` declared -> `fn` runs in place of the apply Fn.
  The closure reports proposed changes without mutating state; it answers "what *would* the
  apply do" the way `terraform plan`, `kubectl apply --dry-run=server`,
  `helm upgrade --dry-run`, and similar tools do.
- `step.SafeWithoutDryRun()` declared -> the apply Fn runs unchanged,
  for steps whose execution has no side effects.
- Neither declared -> the step soft-skips with `step_skipped` /
  `skip_reason: no_dry_run_defined`.
  Risk labels (`step.Risk("destructive", "prod", ...)`) are a separate
  gate: they refuse a normal run unless every label is authorized with
  `--sw-allow`, and `--sw-dry-run` bypasses that gate, so a
  risk-labeled step with no dry-run contract still soft-skips.

For step bodies that need to branch on the mode (e.g. emit a
structured "would do X" log line for an op without a native
dry-run flag), read `sparkwing.IsDryRun(ctx)` directly -- the public
way to detect dry-run from inside a step.

`PreviewPlan` (the pipeline-binary helper behind
`sparkwing pipeline plan`) renders one of three decisions per step
under dry-run: `would_dry_run` (DryRunFn defined),
`would_run` (SafeWithoutDryRun marker), or `would_skip` with
`skip_reason: no_dry_run_defined` (neither contract). Runtime
and preview always agree. `sparkwing pipeline plan` has no dry-run
flag of its own; the preview reads the dry-run mode from the
environment `sparkwing run --sw-dry-run` sets.

Declare `step.DryRun(fn)` on mutating steps; `--sw-dry-run` selects
those callbacks (see *Flag namespace* below).

`*StepGroup` (returned by `sw.GroupSteps`) is both a `Needs` target
(a downstream `step.Needs(group)` depends on every member) and a
dashboard cluster (members fold under the name in the Work view).
Its modifiers are:

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
    tag      := sw.Step(w, "tag",      j.computeTag)
    platform := sw.Step(w, "platform", j.detectPlatform)
    hash     := sw.Step(w, "hash",     j.computeHash)

    return sw.Step(w, "compose", func(ctx context.Context) (BuildOut, error) {
        return BuildOut{
            Tag:      sw.StepGet[string](ctx, tag),
            Platform: sw.StepGet[string](ctx, platform),
            Hash:     sw.StepGet[string](ctx, hash),
        }, nil
    }).Needs(tag, platform, hash), nil
}
```

`StepGet` waits for the upstream step to finish and panics on a missing
or mismatched type. When one step produces the job output, return that
step from `Work`:

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
| In-run sibling | `sw.RefTo[T](node)` | Read a `*JobNode` in the same DAG. A typed handle only: it does NOT create a dependency edge, so pair it with `.Needs(node)` on the consumer. |
| Cross-pipeline, passive | `sw.RefToLastRun[T](pipeline, nodeID, opts...)` | Read another pipeline's latest successful run. Does not trigger. |
| Cross-pipeline, active | `sw.RunAndAwait[Out, In](ctx, ...)` (free fn) | Trigger a fresh run of another pipeline, wait, return its output. |

```go
type Build struct {
    sw.Base
    sw.Produces[BuildOut]
}

func (j *Build) Work(w *sw.Work) (*sw.WorkStep, error) {
    return sw.Step(w, "run", j.run), nil
}

type Deploy struct {
    sw.Base
    Build    sw.Ref[BuildOut]
    Manifest sw.Ref[Manifest]
}

build := sw.Job(plan, "fictional-build", &Build{})
sw.Job(plan, "fictional-deploy", &Deploy{
    Build:    sw.RefTo[BuildOut](build),
    Manifest: sw.RefToLastRun[Manifest]("manifest-pipe", "out",
                  sw.MaxAge(24*time.Hour)),
}).Needs(build)
b := j.Build.Get(ctx)
m := j.Manifest.Get(ctx)
```

`sw.RefTo[T]` requires a job struct to embed `sw.Produces[T]`. It panics
if that marker is missing or declares another output type.

`Get` panics when the reference cannot be resolved. For a
compare-to-last-run pipeline that is the normal first state --
`sw.RefToLastRun` has no successful run to read on the pipeline's first
run -- so read those refs with `TryGet`, which reports the miss instead:

```go
prev, ok := j.Prev.TryGet(ctx)
if !ok {
    return j.buildEverything(ctx) // bootstrap run: nothing to compare against
}
```

`ok` is false when the upstream node has not completed, when the run
that was found stored no output, and on any cross-pipeline resolver
failure; the value is the zero `T`. The SDK cannot tell an unreachable
store from a genuine absence -- a resolver returns a bare error -- and
reports both as absence. For the compare-to-last-run shape that errs
toward doing the whole job; a step that uses `TryGet` to *skip* work
turns a store outage into a silent skip, so read the warn log before
relying on that shape. Every miss is logged at warn naming the pipeline
and node, because "no matching run" is also what a misspelled pipeline
name and an unreachable store produce.

One input divides the two accessors: a cross-pipeline run that stored
empty or null output. `Get` renders that as the zero `T` and carries on;
`TryGet` reports it as a miss. Swapping one for the other on such a ref
changes which branch runs.

`TryGet` panics for the failures a pipeline author cannot handle at
runtime: no resolver in context, which happens only outside a dispatched
step; stored output that does not fit `T`; and a cancelled or expired
context, which is the step being torn down rather than an upstream that
is absent. Keep `Get` where a missing output is itself a programmer
mistake.

Untyped pipelines (no typed output) skip both `sw.Produces[T]` and
`sw.RefTo[T]`; pass plain bytes via env vars or sibling steps.

### Imperative cross-pipeline trigger

```go
out, err := sparkwing.RunAndAwait[Out, In](ctx, "fictional-build", "fictional-artifact",
    sparkwing.WithFreshInputs(In{Service: "api"}),
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
func (b *Build) Plan(_ context.Context, plan *sw.Plan, _ sw.NoInputs, rc sw.RunContext) error {
    sw.Job(plan, "fictional-build", func(ctx context.Context) error {
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
    sw.Job(plan, "fictional-deploy", func(ctx context.Context) error {
        args := sw.Inputs[DeployArgs](ctx)
        return runDeploy(ctx, args.Service, args.Env)
    })
    return nil
}
```

Panics outside a dispatch ctx (no installer) or on a wrong concrete
type. The orchestrator installs the parsed Inputs on every node's
runner ctx automatically.

There is no public helper for installing inputs on a ctx from
external test code; inputs reach a node's ctx only through the
orchestrator.

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
sw.Register[sw.NoInputs]("fictional-lint", func() sw.Pipeline[sw.NoInputs] {
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
`secret:"true"`          // Redact in logs, run views, receipts, and dashboard
`flag:",extra"`          // Catch-all for unknown flags; map[string]string only
```

### What `secret:"true"` covers

A secret-marked input is redacted to `***` on every read surface: the
`run_start` setup block `sparkwing run` prints, `runs list`, `runs get`,
`runs status`, `runs find`, `runs tree`, `runs wait`, `runs receipt`
(including the `rerun` reproducer command), the controller's run API,
and the dashboard's Setup panel. Node log bodies are masked separately
by the run's masker, which replaces the value anywhere it appears in
emitted text.

Redaction is applied when a run is read, not when it is written. The
run row keeps the plaintext argument, because the masker derives its
redaction set from it and retrying or replaying a run re-executes with
it. Treat the state database, its backups, and the `state.ndjson` run
dumps as holding the value in the clear, and protect them accordingly.

Limits worth knowing:

- A `flag:",extra"` bag is never redacted -- its keys are arbitrary and
  carry no per-key opt-in.
- Runs recorded before a pipeline declared the input secret render as
  they always did. The classification is stamped on the run at start,
  and there is no schema to reclassify an old row from.
- A value that arrives from `sparkwing.yaml` -- the project's
  `defaults.args` block or a pipeline entry's `args:` block -- is masked
  in logs but does not appear on the run row at all, redacted or
  otherwise. The row records the arguments the caller passed, and the
  yaml layers are re-read from the checkout each run, so a retry picks
  up the project's current value instead of a copy of the old one.
- Trigger rows are not redacted. `sparkwing runs triggers get`,
  `sparkwing runs triggers list`, and `GET /api/v1/triggers` show
  argument values, because the same endpoint hands them to the runner
  claiming the work.
- A run pre-allocated by a fresh trigger shows its arguments while it
  sits in `pending`, until the worker that starts it stamps the
  classification on. Retries and replays inherit their source run's
  classification and are covered for that window.
- Redaction of the `rerun` reproducer is anchored on the argument name,
  not the value. Pass the same value to a non-secret argument as well
  and it is masked in logs -- where masking is value-anchored -- but
  shown under that other argument's name.
- `inputs_hash` is omitted when the caller supplied any `secret:"true"`
  argument. This keeps run metadata, logs, receipts, and state dumps from
  becoming an offline oracle for low-entropy values. The SQL migration also
  removes hashes from classified rows written by older versions. Built-in
  state backends reject an unsafe write; custom `storage.StateStore`
  implementations should call `store.ValidateRunInvocation` in `CreateRun`.
- Audit events are masked when they are written, not when they are
  read. Events recorded before this behavior existed, and any future
  event type that carries arguments without going through the masker,
  are served as-is by `GET /api/v1/runs/{id}/events` and the dashboard
  SSE stream.
- Dispatch envelopes are masked only when a masker is present on the
  context. A dispatch written outside a run's execution context is
  stored as-is.

Cluster executors are the one caller that needs the real values: a pod
fetches the arguments it runs with from `GET /api/v1/runs/{id}`. They
pass `?include=secret_values`, which the controller honors for an
`admin` token and for a `nodes.claim` token holding an unexpired claim
on one of the run's nodes. A controller running with authentication
disabled honors it for everyone, because the whole API is open there
and a redacted argument would execute as the literal `***`. Every other
caller gets the redacted view whether or not it asks.

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
using the `sw-` prefix for its long control options:

```
-C, --sw-cd PATH          // re-anchor .sparkwing/ discovery
    --sw-ref REF          // compile the pipeline at a git ref
-v, --sw-verbose          // debug logging
    --sw-start-at STEP    // start the run at STEP
    --sw-stop-at STEP     // stop the run after STEP
    --sw-only GLOB        // run only matching jobs (+ their Needs)
    --sw-no-cache         // ignore cached per-node results
    --sw-priority VALUE   // local admission priority: N, front, back
    --sw-local-only       // force local secrets/state/cache/logs
    --sw-dry-run          // run each step's dry-run probe
    --sw-allow LABEL,...  // authorize risk-labeled steps
    --sw-no-update        // skip the sparks auto-resolve step
```

The runner reserves `--sw-*` for control options. Unknown options with that
prefix fail before execution setup. Other arguments pass to the pipeline.
Put `--` before pipeline arguments that resemble runner options; every argument after
the separator passes unchanged to the pipeline binary.

Before the separator, the runner also consumes `--profile`, `-C`, `-v`, and
`--dry-run=true` / `--dry-run=false`. The pipeline owns `--target`.

For a `--dry-run`-style flag, prefer `step.DryRun(fn)` on each mutating
step (see *Work - the inner DAG > Dry-run contract*) over a
`flag:"dry-run"` input; the runner-level `--sw-dry-run` then dispatches
your DryRun callbacks.

## Cache

`.Memoize(key, opts...)` is content-addressed result memoization: same
content, compute once, reuse the result. It carries no scope and no
group -- that is [Concurrency](#concurrency)'s job.

```go
sw.Key("go-mod", "1.26", "abc123")

node := sw.Job(plan, "fictional-build", func(ctx context.Context) error { return nil })
node.Memoize(func(ctx context.Context) (sw.CacheKey, error) {
    return sw.Key("fictional-build", "linux", "amd64"), nil
}, sw.TTL(24*time.Hour))
```

- `CacheKeyFn` has signature `func(context.Context) (CacheKey, error)`.
  It resolves after upstream dependencies complete and before dispatch,
  so it can read `Ref[T]` output. Errors, panics, empty keys, and expired
  resolution deadlines fail the node before its work starts.
- `TTL(d)` bounds retention; omit for `DefaultCacheTTL` (7d), capped at
  `MaxCacheTTL` (35d).
- Return `sw.NoCache, nil` to run uncached for one invocation.
- Identical content that is in flight dedupes automatically: one
  computes, the rest wait and replay a successful result.

See [caching.md](caching.md) for the full model. The `JobGroup` mirror
is `group.Memoize(key, opts...)`.

## Concurrency

`.Concurrency(group, cost...)` enrolls a node in a named budget shared
by its members: different work taking turns under a cap. Define the
group once and pass the handle to each member.

```go
databaseGroup := sw.NewConcurrencyGroup("db", sw.ConcurrencyLimit{
    Capacity:     8,
    Scope:        sw.ScopeBox,
    OnLimit:      sw.Queue,
    QueueTimeout: 30 * time.Second,
})
sw.Job(plan, "fictional-shard-1", func(ctx context.Context) error { return nil }).Concurrency(databaseGroup, 4)
sw.Job(plan, "fictional-shard-2", func(ctx context.Context) error { return nil }).Concurrency(databaseGroup, 4)
```

`Capacity` and `cost` are integers in author-defined units (a slot, a
gigabyte, a database container, and similar quantities). Admission compares the summed `cost` of
live members in the scope plus this member's cost against `Capacity`.
For at most `N` concurrent members, set capacity to `N` and use each
member's default cost of 1.

```go
deployGate := sw.NewConcurrencyGroup("fictional-deploy-prod", sw.ConcurrencyLimit{
    Capacity: 1,
    OnLimit:  sw.Queue,
})
sw.Job(plan, "fictional-deploy", func(ctx context.Context) error { return nil }).Concurrency(deployGate)
```

### OnLimit

What a member does when its group is at capacity:

- `Queue` (default) -- wait for room, then run. Waiters that fit run
  oldest-first; a waiter that cannot fit in the available
  weighted budget does not block later waiters that do fit unless younger
  backfilled holders are what keep the older waiter from fitting. This
  ordering is scoped to the `Concurrency()` group; local admission priority
  is a separate run-level order.
- `Fail` -- error immediately.
- `Skip` -- resolve as a no-op without running.
- `CancelOthers` -- evict running members oldest-first until this one
  fits (best-effort; side effects already committed are not rolled
  back).

Concurrency groups schedule distinct work. Use [Cache](#cache) to reuse results.

### Scope

`Scope` selects how far the budget reaches; it folds into the
coordination key as a scope tag plus a length-prefixed qualifier, so
two scopes (or two qualifiers) can never fold onto one key:

- `ScopeRun` -- key `r:<len>:<runID><name>`: only this run's nodes
  share the budget.
- `ScopeBox` -- key `b:<len>:<hostID><name>`: every run on one machine
  shares it, even under a controller.
- `ScopeGlobal` (the zero value) -- key `g:<name>`: the whole fleet
  shares it through the coordination backend.

`<len>` is the byte length of the qualifier that follows.

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
onto every caller and aborts the loser with "slot full". With capacity 1,
`Queue` lines arrivals up FIFO and runs them one at a time, with `QueueTimeout`
as the bounded way out. With weighted capacities, it can grant one later
request when the head waiter cannot fit. After that backfill, the
older waiter's full resource set is protected until it runs, even if external
pressure still keeps it from fitting when the younger holder exits. Queue state
reports the backfill count and protection reason; the rolling event summary
retains younger-backfill and protected-waiter counts after the waiter departs.

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

### Run priority

Local admission orders queued runs by an integer priority: higher
admits first, equal priorities keep FIFO order. A plan can declare its
own:

```go
plan.Priority(10)
```

An operator can override that default per run with `sparkwing run --sw-priority VALUE`,
where VALUE is an integer or one of `front` / `back`:

```sh
sparkwing run fictional-deploy --sw-priority 100   # explicit number
sparkwing run fictional-deploy --sw-priority front # one past the highest queued
sparkwing run fictional-deploy --sw-priority back  # one below the lowest queued
```

`front` and `back` are resolved once, when the run starts, against the
queue as it stands then -- an empty queue answers 1 and -1. The
resolved number is fixed for the life of the run, so the run's own
admission and every node it later dispatches queue at the same place,
and the run record carries both the number and where it came from.
`sparkwing run --sw-detached --sw-priority` carries the request on the
trigger and resolves it when the consumer launches the run, so `front`
means ahead of the queue the run joins. The flag reaches the
pipeline program as `SPARKWING_PRIORITY`.

Priority is a whole-run order, separate from `Concurrency()`: a group's
members still take turns oldest-first inside the group.

## Discovery

- `sparkwing docs read --topic pipelines` - conceptual tour
- `sparkwing docs read --topic sdk` - this page
- `sparkwing docs all` - every doc concatenated (one stdout dump for agents)
- `sparkwing pipeline explain --name X [-o json]` - render the full
  Plan -> Job -> Work -> Step tree before running
- [`pipelines.md`](pipelines.md) - the conceptual Plan/Work tour
