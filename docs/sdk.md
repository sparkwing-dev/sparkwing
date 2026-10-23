# Go SDK

The `sparkwing` package declares pipelines, jobs and steps. The engine hosts the
run through [protocol 2](node-protocol.md), schedules the graph and owns retries,
timeouts, admission, secrets, storage and masking. SDK processes execute the
callbacks the host selects.

```go
import sw "github.com/sparkwing-dev/sparkwing/sparkwing"
```

## Pipeline registration

Implement `Pipeline[T]`, register a factory and call `sw.Main()` from the pipeline
entry's `main` function. The factory creates a fresh pipeline for each invocation.
The host supplies typed inputs and a `RunContext` to `Plan`.

```go
package main

import (
    "context"

    sw "github.com/sparkwing-dev/sparkwing/sparkwing"
)

type Inputs struct {
    Target string `flag:"target" default:"staging" enum:"staging,prod"`
}

type Ship struct{ sw.Base }

func (Ship) Plan(_ context.Context, plan *sw.Plan, in Inputs, _ sw.RunContext) error {
    build := sw.Job(plan, "build", func(ctx context.Context) error {
        _, err := sw.Exec(ctx, "go", "build", "./...").Run()
        return err
    })
    sw.Job(plan, "deploy", func(ctx context.Context) error {
        sw.Info(ctx, "deploying to %s", in.Target)
        return nil
    }).Needs(build)
    return nil
}

func main() {
    sw.Register[Inputs]("ship", func() sw.Pipeline[Inputs] { return Ship{} })
    sw.Main()
}
```

`sw.RegisterEntrypoint[T]` registers a named SDK entrypoint for a project alias.
The invoked pipeline name travels in `RunContext.Pipeline`. Pipelines without
flags use `sw.NoInputs`.

Input structs support `flag`, `short`, `default`, `required`, `enum` and `desc`
tags. `secret:"true"` classifies a flag value for masking. A `flag:",extra"`
map receives extra flags. Typed job inputs use an embedded `sw.WithArgs[T]` and
share the pipeline's flag namespace. The host checks required values, types,
enums and name collisions before execution. Generated jobs carry declarations
with their job specification. The host prepares their values from the accepted
run snapshot after generation and sends them for typed decoding before execution.
Use the pipeline extra-flag map for supplied flags that only a generator declares.
Initial jobs must use argument declarations discovered by zero-input describe,
even when invocation inputs change job IDs. New declarations belong in generated
jobs, whose secrets the host registers before execution.

Secret flag values are masked in logs and diagnostics. Input masking does not
remove those values from stored invocation arguments, so use declared secrets
for credentials.

The SDK describes registered inputs, defaults, help, risks and secret names to
the host. A discovery error refuses only that pipeline and reports its sanitized
cause when invoked. Other pipelines retain their own complete secret declarations.
A pipeline cannot run when its declarations are incomplete. Help and describe
validate declarations and defaults without requiring invocation values. Describe
uses zero values and does not execute generators.

The host validates arguments after describe and before planning. Empty defaults mean
no default; a required argument cannot also have a non-empty default. Numeric and
duration values use Go parsing rules, and floating-point NaN and infinity are
refused. Lists split on commas, trim Unicode whitespace from each item and drop empty items; both the raw
argument map and typed list receive that normalized value. The host trims enum
choices and checks each list item. Scalar string values keep their whitespace.
Invalid arguments and declarations fail during host preparation
with `invalid_args`. SDKs project declarations and decode the host's prepared
values. Plan graph and secret declaration validation also belong to the host.
Annotation locations, including an end line preceding the start, are checked by
the host service when the annotation is written.

## Plan() must be pure

`Plan(ctx, plan, inputs, runContext)` builds declarations. Put commands, file
writes, network calls and secret reads inside execution callbacks. SDK helpers
reject side effects on the planning context. `Work` methods also construct
declarations and must follow the same rule.

The host replans in execution processes to reconstruct code and compares the
result with the accepted job declaration. The same inputs and context must
produce the same graph. Read clocks, random values and mutable environment
inside callbacks. A reconstruction mismatch fails with `plan_drift` before the
body starts.

`RunContext` contains `RunID`, `Pipeline`, `Git`, `Trigger`, `StartedAt`, `DryRun`
and `NoMemo`. These are the host's run snapshot. `sw.Inputs[T](ctx)` reads typed
pipeline inputs from an execution callback. `sw.Runner(ctx)` returns the granted
runner identity, including its name, type and labels.

## Jobs and steps

A `Plan` holds the job graph. Each executable job contains a `Work` graph of
steps running within its SDK process. `sw.Job(plan, id, body)` accepts a
`Workable` value or a `func(context.Context) error`. A plain function becomes a
single step named `run`.

A `Workable` implements `Work(*sw.Work) (*sw.WorkStep, error)`. Its returned step
selects the job's typed result. Return nil when the job has no output.

```go
type BuildOutput struct {
    Digest string
}

type Build struct {
    sw.Base
    sw.Produces[BuildOutput]
}

func (Build) Work(work *sw.Work) (*sw.WorkStep, error) {
    compile := sw.Step(work, "compile", func(ctx context.Context) error {
        _, err := sw.Exec(ctx, "go", "build", "./...").Run()
        return err
    })
    result := sw.Step(work, "result", func(context.Context) (BuildOutput, error) {
        return BuildOutput{Digest: "sha256:example"}, nil
    }).Needs(compile)
    return result, nil
}
```

The host schedules steps when their dependencies settle. `sw.StepGet[T](ctx,
step)` reads a completed step's local output. A new attempt resets those outputs.
The protocol carries an explicit attempt ordinal and terminal-step snapshot for
both step bodies and step conditions.

`sw.GroupJobs(plan, name, jobs...)` groups jobs and provides a dependency target.
`sw.JobFanOut(plan, name, items, fn)` declares a static fan-out.
`sw.GroupSteps(work, name, steps...)` groups steps within a job.

| Declaration | Behavior |
|---|---|
| `job.Needs(deps...)` | Wait for required jobs |
| `job.NeedsIfPresent(deps...)` | Wait for dependencies that exist in this plan |
| `job.NeedsGenerated(generators...)` | Wait for generator settlement and generated jobs |
| `step.Needs(deps...)` | Wait for steps in the same job |
| `SkipIf(predicate)` | Skip when the predicate returns true |
| `ContinueOnError()` | Allow dependents after failure |
| `Optional()` | Exclude this job or step's failure from required success |
| `step.Finally()` | Run cleanup work after dependencies settle, including failed dependencies |
| `work.FailurePolicy(sw.FailFast)` | Cancel independent work after a step failure |
| `work.FailurePolicy(sw.CollectAll)` | Let independent steps finish |

Final outcomes are `sw.Success`, `sw.Failed`, `sw.Cancelled`, `sw.Skipped`,
`sw.Superseded` and `sw.Memoized`. Their wire values use snake_case. A separate
reason distinguishes conditions such as a concurrency skip.

## Typed outputs and references

A job embedding `sw.Produces[T]` exposes its selected result as `T`.
`sw.RefTo[T](job)` creates a typed reference. Declare its dependency with `Needs`.
The host authorizes reads through declared edges.

```go
build := sw.Job(plan, "build", Build{})
output := sw.RefTo[BuildOutput](build)
sw.Job(plan, "report", func(ctx context.Context) error {
    sw.Info(ctx, "built %s", output.Get(ctx).Digest)
    return nil
}).Needs(build)
```

`Ref.Get(ctx)` returns the decoded value. `Ref.TryGet(ctx)` also reports
presence. A transport or decoding failure fails the callback. For a cross-run
reference, JSON null is absent. An in-run null output is present and decodes to
the type's zero value. Typed job and step outputs are bounded to 16 MiB each.

`sw.RefToLastRun[T](pipeline, jobID, sw.MaxAge(duration))` reads the latest
successful run without starting another run. Store it in a job field so planning
can declare the read edge.

`sw.RunAndAwait[Out, In](ctx, pipeline, jobID, options...)` starts a child run and
waits for its output. Options include `sw.WithFreshInputs(in)`,
`sw.WithFreshArgs(args)` and `sw.WithFreshTimeout(duration)`. The host owns child
submission, authorization, cancellation and cycle checks. Call it inside an
execution callback. Compose reusable job types into a plan when the work belongs
in the same run.
Declare every pipeline a job may start with
`sw.Job(plan, "release", &Release{}).StartsChildren("deploy-us", "deploy-eu")`
(TypeScript `job.startsChildren("deploy-us", "deploy-eu")`); the controller
refuses an undeclared child with `403`. A child at its parent's exact revision
inherits the parent's identity, including its pipeline for OIDC claims and
pipeline-scoped secrets. A child that names a branch the controller resolves
runs as an asserted manual run under its own pipeline name and reads only
team-wide shared secrets. Use `WithFreshRepo("owner/repo")` (TypeScript
`runAndAwait` option `repo`) for an explicit repository and `WithFreshBranch`
(option `branch`) to override its default `main` branch. The parent's live claim
must authorize that repository. A child in another repository is always asserted.
Children nest at most 10 levels deep
counting the root, and one root starts at most 50; see
[limits](limits.md#execution-and-records).
The creating job reads the child's committed output under its live claim.
Missing output, missing bytes and failed digest verification fail the await.
An explicit JSON `null` remains a valid output.

## Generated jobs

`job.Generate` registers a callback that declares jobs after a successful or
memoized source result. Read that result through a typed reference. A source
without a typed result gives its generators no result, locally and on any
runner. The returned
`*sw.Generator` is a dependency target for `NeedsGenerated`.

```go
source := sw.Job(plan, "build", Build{})
output := sw.RefTo[BuildOutput](source)
generated := source.Generate(func(ctx context.Context, expansion *sw.Plan) error {
    digest := output.Get(ctx).Digest
    sw.Job(expansion, "inspect", func(ctx context.Context) error {
        sw.Info(ctx, "inspecting %s", digest)
        return nil
    })
    return nil
})
sw.Job(plan, "finish", func(context.Context) error { return nil }).
    NeedsGenerated(generated)
```

Each registration has a stable ID. The host persists the accepted jobs and the
barrier even when the callback generates zero jobs. The limit is 256 jobs per
generator. Expansions declare jobs, while plan settings remain with the initial
plan. Execution replay reconstructs code and must match the accepted declaration.
Generator contexts exclude the source's body environment because it must not
change reconstructed declarations. Failed, skipped, cancelled and superseded
sources settle the barrier without running the generator.

## Retry, timeouts and hooks

`job.Retry(n, sw.Backoff(duration))` permits `n` additional application attempts.
The host rejects retry declarations above its ceiling of 10. Backoff doubles
between retries, up to five minutes. Application retries keep the claim and
advance the attempt ordinal. Lease-loss recovery has a separate disruption
allowance.

`Timeout(duration)` bounds each attempt. `NoProgressTimeout(duration)` bounds
inactivity during the body and verification. Observable log records reset the
inactivity window. Streaming command output reports progress, while a captured
command can remain silent until it finishes. Use a longer absolute timeout to
bound work that keeps producing output.

`BeforeRun` registers a callback before execution. `Verify` checks the successful
body and fails the job when it returns an error. `AfterRun` receives the final
error, with nil for success. `OnFailure(id, body)` declares a separate recovery
job and returns that job, so chained modifiers configure the recovery.
Recovery callbacks can accept `sw.Failure` to inspect `Stage` and `Err`.
Calling `OnFailure` on the returned job declares the next recovery in the chain.

## Approvals

Use `plan.Approval(id, sw.ApprovalConfig{...})` for a manual decision.

```go
gate := plan.Approval("promote", sw.ApprovalConfig{
    Message:   "Promote this build?",
    Timeout:   2 * time.Hour,
    OnTimeout: sw.ApprovalDeny,
}).Needs(build)
sw.Job(plan, "release", func(context.Context) error { return nil }).Needs(gate)
```

`OnTimeout` accepts `sw.ApprovalFail`, `sw.ApprovalDeny` or
`sw.ApprovalApprove`. Zero timeout waits indefinitely within the owner's
deadline. Approvals support `Needs`, `NeedsIfPresent`, `NeedsGenerated`, `SkipIf`,
`Optional`, `ContinueOnError` and `OnFailure`. The recovery job is executable.
The gate itself only declares a decision.

## Memo, caches and artifacts

`Memo` reuses a successful job result. Its callback returns `sw.MemoValue` and an
error. The host hashes that JSON value. Collect parts with `sw.MemoKey(parts...)`, set retention with
`sw.MemoTTL(duration)`, and return `sw.NoMemo` to bypass reuse. Empty keys fail.
The key callback runs after SkipIf on the executing host under the job's actual
award, inside its concurrency slot. A memo hit releases the job's groups without
running its body. The maximum memo TTL is 30 days. Missing or corrupt objects
produce a miss.

```go
sw.Job(plan, "build", Build{}).
    Memo(func(context.Context) (sw.MemoValue, error) {
        return sw.MemoKey("build", "toolchain-v1"), nil
    }, sw.MemoTTL(24*time.Hour))
```

The `sparkwing/inputs` package supplies `RepoFiles`, `Files`, `Tree`, `Env`,
`Parts`, `Const` and `Compose` input recipes, evaluated by the host against its
checkout and effective job environment. Pass a recipe directly to `Memo`.
Include every input that affects the
result. The host scopes reuse by the accepted repository and trust policy.

`Cache` restores and saves dependency directories. `sw.GoModules()` and
`sw.Npm()` declare standard caches. `sw.Dir(path, sw.KeyFromFile(lockfile))`
declares a custom directory cache. `DirCache.Key(workdir)` computes its input key
without granting storage access. A dependency-cache failure runs the job cold.
Relative cache paths and key files use the repository working directory, as do
package-manager cache queries.

```go
sw.Job(plan, "test", func(ctx context.Context) error {
    _, err := sw.Exec(ctx, "go", "test", "./...").Run()
    return err
}).Cache(sw.GoModules())
```

`Artifacts(globs...)` publishes files. `Consumes(producer, sw.Into(prefix))`
stages the producer's artifacts and adds its dependency. Patterns and staging
destinations use the repository working directory and stay inside that workspace.
Artifact staging failure fails the job.

```go
build := sw.Job(plan, "build", Build{}).Artifacts("dist/**")
sw.Job(plan, "package", func(context.Context) error { return nil }).
    Consumes(build, sw.Into("input/build"))
```

## Environment and placement

`job.Env(name, value)` applies body environment after planning. Use
`sw.Startup()` for interpreter settings that must be present when the SDK starts.
The engine also recognizes its fixed interpreter startup names. Startup values
must not alter declarations. Commands and cleanups inherit the attempt's body
environment.

`Requires(labels...)` declares required runner labels. `Prefers(labels...)` holds
remote work for a runner advertising those preferred labels when applicable.
`NeedsTools(names...)` declares known tool requirements. `Inline()` selects the
run host for execution. `sw.Runner(ctx)` reports the host-granted identity.

Runner size is a required label, for example `job.Requires("sparkwing-4cpu")`.
Cloud offers `sparkwing-2cpu`, `sparkwing-4cpu`, `sparkwing-8cpu`,
`sparkwing-16cpu`, `sparkwing-32cpu` and `sparkwing-64cpu`; an installation must
price a size before it can award it. Billing uses that label's exact credit class.
Without a size requirement, Cloud uses `sparkwing-2cpu`.
Self-hosted runners advertise their own labels. Each selector term must match;
comma-separated alternatives within a term are ORed. CPU and memory resource
declarations are removed. `plan.Priority(value)` sets admission priority.
`plan.ClaimWait(duration)` declares a positive maximum wait for a runner claim.
`plan.Checkout(sw.Checkout{...})` declares source options with pointer fields for
`Depth`, `FullHistory`, `Tags`, `Submodules` and `LFS`. Explicit values remain
distinct from omission. The host resolves settings and refuses declarations over
operator ceilings when it accepts the plan.

## Concurrency

Create a shared budget with `sw.NewConcurrencyGroup` and attach it to a job or
the plan. Membership cost defaults to one.

```go
deploys := sw.NewConcurrencyGroup("deploys", sw.ConcurrencyLimit{
    Scope:    sw.ScopeGlobal,
    Capacity: 1,
    OnLimit:  sw.Queue,
})
sw.Job(plan, "deploy", func(context.Context) error { return nil }).
    Concurrency(deploys)
```

Scopes are `sw.ScopeGlobal`, `sw.ScopeRun` and `sw.ScopeBox`. Policies are
`sw.Queue`, `sw.Fail`, `sw.Skip` and `sw.CancelOthers`. Set a positive `Capacity`
in every Go `ConcurrencyLimit`. Its zero value is an explicit zero and fails
validation. Omitted scope, membership cost and policy resolve to global scope,
cost one and queueing. An explicit zero cost also fails validation.
Reused group definitions at the same level must agree on scope, capacity and
policy. Plan and job groups have separate namespaces. The same name at both
levels identifies two budgets. The store derives keys from accepted declarations
and the actual execution award. Each complete group set grants atomically. A
queued job holds none of its groups. `QueueTimeout` bounds initial waiting.
Replaying an already granted admission after that timeout retains its groups.

After released work loses plan capacity, it keeps running. New releases queue
without a timeout until the plan reacquires capacity. `CancelOthers` replaces
logical capacity immediately and requests cooperative cancellation. Disconnected
work can overlap until cleanup or the five-minute drain deadline.

`plan.Concurrency(group, cost...)` holds a budget for the whole run. Plan groups
accept global or run scope. Pipeline acceptance refuses a box-scoped plan group
with an error naming the group and pointing at job-level box scope, because a
run can execute across several machines. Attach a box-scoped group to jobs to
limit work on each machine. Job memberships consume capacity around the job.
Resource admission remains a separate host responsibility.

## Secrets and masking

Masking is provided by the Go host. Neither SDK masks output or sends automatic
mask registrations. Pipeline code run without a Sparkwing host is unmasked.
The host registers accepted secret arguments, defaults and normalized list items
before execution. Stored secrets and OIDC tokens are registered before release.
Stored secrets, supplied secret arguments and nonempty secret defaults of the
selected pipeline need at least four characters. Secrets containing `***` are accepted.
This includes each nonempty list item. An empty supplied argument or default
is no value.
Use a plain config variable for values that fail this rule. Defaults belonging
to unselected pipelines do not refuse a run. Valid values and list items still
register, while short items are skipped. Derived proxy credential parts
that fail the rule are skipped.
Stdout and stderr are masked before line splitting. Describe and generator output
waits for declarations. Failed discovery withholds that output, including CLI
help diagnostics and test-host planning output. Generator failures persist a
fixed error with details withheld. Controller comments and run displays include
accepted generator classifications. CLI lookup, flag parsing and routing use
private declarations. Help and the describe cache use a separate presentation
type, with all unclassified text withheld after incomplete discovery.

The stream matcher publishes complete safe lines within the current write.
It holds only suffixes that could complete a registered spelling. Safe partial
lines flush after 25 ms of idle output. Prefixes that could complete a secret
stay private until more input or EOF. A stored secret read is masked even when
its value occurs in an accepted job identifier, including subsequent steps.
Size and idle flushes preserve complete UTF-8 boundaries. Masking and protocol
validation refusals stop the session, including during a timer flush. Log
persistence failures preserve the execution result, report incomplete logs and
alert the operator.

A pipeline implements `Secrets() any` to declare a typed secrets struct. Fields
use `sw:"NAME,required"` or `sw:"NAME,optional"`. Required is the default.
Declarations appear in describe. The host checks required names, and execution
callbacks resolve values lazily.

```go
type ShipSecrets struct {
    Token string `sw:"DEPLOY_TOKEN,required"`
}

func (Ship) Secrets() any { return &ShipSecrets{} }
```

`sw.PipelineSecrets[ShipSecrets](ctx)` returns the resolved struct.
`sw.Secret(ctx, name)` reads a masked value. `sw.Variable(ctx, name)` reads an
unmasked value. A classification mismatch fails the read. `MustSecret` and
`MustVariable` panic on errors. Planning has no execution binding for these
services.

Call `sw.Mask(ctx, value)` before logging a dynamically obtained secret. It
returns after the host acknowledges registration, and returns an error if that
registration fails. Registered secret values cannot occur in job, step, group or
approval IDs, including generated declarations. Validation reports the field
path without echoing the value. Registration also refuses a value contained in
an already accepted identifier.
Runtime mask registrations stay on the registering host, so declare a secret if its value may appear in an identifier on another host.

A job output that holds a registered secret value, plainly or base64-, URL- or
JSON-escaped, is withheld. The job still passes, gets a `warn` annotation
reading `output withheld: contains a secret`, and logs the same warning.
Downstream jobs see no output, as if the job returned none.

`sw.OIDCToken(ctx, audience)` requests an identity token through the host.
Credentials remain in the private process binding rather than the environment
of commands a step starts.

## Exec and Bash step commands

`sw.Exec(ctx, program, args...)` runs argv directly. `sw.Bash(ctx, script)` runs a
shell script for a step. Use Exec to run a command such as `go test ./...`.
Both return a command builder with `Dir`, `Env` and `EnvMap`.
Terminators include `Run`, `Capture`, `String`, `Lines`, `JSON` and `MustBeEmpty`.
Handle returned errors to fail the step. `ExecError` carries exit status and
captured output. `WorkDir()` returns the repository working directory.

File helpers include `ReadFile`, `WriteFile`, `Glob` and similar operations.
Call them inside execution callbacks when their result depends on workspace
state. Subpackages provide Docker, git and service helpers.

`sw.Info`, `sw.Warn` and `sw.Error` emit structured text records.
`sw.Annotate(ctx, sw.Annotation{...})` writes a diagnostic with level, message,
file and optional line, end-line and column. Positions are one-based when
supplied. Levels are `debug`, `info`, `warn` and `error`, with `info` as the
default. `sw.Summary(ctx, markdown)` replaces the job or step summary. The host
assigns the job, filters debug records and masks text before applying limits.
Annotation and summary write failures fail the callback.

A step can declare `Risk(labels...)`, `DryRun(callback)` and
`SafeWithoutDryRun()`. The host enforces approved risks and chooses the dry-run
body. `sw.IsDryRun(ctx)` reads the mode inside a callback.

`cleanup.Register(ctx, cleanup.Spec{Argv: argv, Description: text})` registers an
idempotent teardown command for a resource outside the process tree. It returns
a release function. Call release after normal teardown so the host can discard
the registration. Import the `sparkwing/cleanup` subpackage for this helper.

## Testing author code

Import `github.com/sparkwing-dev/sparkwing/sparkwing/sparkwingtest` to exercise
author code with an isolated fake host. `Options` supplies runner and run
context, classified secrets, job outputs and callbacks for OIDC, latest output
and child runs.

```go
func TestJob(t *testing.T) {
    host := sparkwingtest.New(sparkwingtest.Options{
        Runner: sw.RunnerInfo{Name: "fixture", Type: "local"},
        Secrets: map[string]sparkwingtest.Secret{
            "TOKEN": {Value: "test-token", Masked: true},
        },
    })
    t.Cleanup(func() {
        if err := host.Close(); err != nil {
            t.Error(err)
        }
    })
    plan := sw.NewPlan()
    sw.Job(plan, "check", func(ctx context.Context) error {
        _, err := sw.Secret(ctx, "TOKEN")
        return err
    })
    if _, err := host.RunStep(t.Context(), plan, "check", "run"); err != nil {
        t.Fatal(err)
    }
}
```

`host.Plan(ctx, name, args)` invokes a registered planner and returns its wire
plan. `RunStep` executes a declared step and returns JSON output. `Eval` invokes
a declared closure with JSON input. `Context` installs the fixture's services.
`Records` and `Masks` return snapshots for assertions. `Close` cancels and joins
active operations and callbacks. Each fixture owns its state.

Planning cannot read service answers because it has no execution binding.
Execution uses the same SDK declarations and operation rules as protocol
serving. The fake host provides focused author tests. Real-host acceptance tests
cover scheduling, storage and authorization integration.
