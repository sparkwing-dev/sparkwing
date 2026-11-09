# Go SDK

The `sparkwing` package declares pipelines, jobs and steps. The engine hosts the
run through [protocol 2](node-protocol.md), schedules the graph and owns retries,
timeouts, admission, secrets, storage and masking. SDK processes execute the
callbacks the host selects.

```go
import sw "github.com/sparkwing-dev/sparkwing/sparkwing"
```

The Go SDK is its own module, `github.com/sparkwing-dev/sparkwing/sparkwing`,
with a Go 1.25 minimum. It has no host-module dependency. SDK releases use
`sparkwing/v0.x.y` tags and advance only when SDK source changes. Engine releases
have their own versions.

For source development, the repository workspace joins the host, SDK and pipeline
modules. Run SDK tests explicitly with `GOWORK=off go -C sparkwing test ./...`.
Root `go test ./...` does not traverse the nested module. The root manifest has
no replacement directives so versioned `go install` remains supported.

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
    sw.Register[Inputs]("Ship", func() sw.Pipeline[Inputs] { return Ship{} })
    sw.Main()
}
```

Registration is keyed by the `entrypoint` a `sparkwing.yaml` pipeline names
(`entrypoint: Ship` here), not by the pipeline name. `sw.RegisterEntrypoint[T]`
does the same as `sw.Register[T]`. The invoked pipeline name travels in
`RunContext.Pipeline`. Pipelines without
flags use `sw.NoInputs`.

Input structs support `flag`, `short`, `default`, `required`, `enum` and `desc`
tags. `secret:"true"` classifies a flag value for masking. A `flag:",extra"`
map receives extra flags. Typed job inputs use an embedded `sw.WithArgs[T]` and
share the pipeline's flag namespace. The host checks required values, types,
enums and name collisions before execution. An invalid public enum value lists
the allowed choices. Secret enum diagnostics omit both the supplied value and
the choices. Local discovery reads declarations before validating invocation
values, so required inputs can be supplied after `--`. Generated jobs carry declarations
with their job specification. The host prepares their values from the accepted
run snapshot after generation and sends them for typed decoding before execution.
Use the pipeline extra-flag map for supplied flags that only a generator declares.
Initial jobs must use argument declarations discovered by zero-input describe,
even when invocation inputs change job IDs. New declarations belong in generated
jobs, whose secrets the host registers before execution.

Secret flag values of 4 or more characters are masked in logs and diagnostics (see the secret values rule). Input masking does not
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
The host authorizes reads through declared edges in the same run and team.
Go `Ref.Get(ctx)` and TypeScript `ctx.output(job)` use this rule. A job cannot
read an unrelated job's output or select another run through an in-run read.

```go
build := sw.Job(plan, "build", Build{})
output := sw.RefTo[BuildOutput](build)
sw.Job(plan, "report", func(ctx context.Context) error {
    sw.Info(ctx, "built %s", output.Get(ctx).Digest)
    return nil
}).Needs(build)
```

`Ref.Get(ctx)` returns the decoded value. `Ref.TryGet(ctx)` also reports
presence. An upstream that was skipped, has not finished, or ran in a dry run
without a dry-run body is absent, as is an expired output; TypeScript `tryGet`
returns `undefined` for the same cases. Bytes missing behind a recorded output,
a transport failure or a decoding failure fail the callback. For a cross-run
reference, JSON null is absent. An in-run null output is present and decodes to
the type's zero value. Typed job and step outputs are bounded to 16 MiB each.

`sw.RefToLastRun[T](pipeline, jobID, sw.MaxAge(duration))` reads the latest
successful verified default-branch run without starting another run. A reference
to the reading run's own pipeline reads only runs of the reader's repository,
because repositories in one team can share a pipeline name. Store it in
a job field so planning can declare the read edge. Add `sw.FromAnyRun()` to accept
output from any successful run, including pull requests and uploads. The opt-out
must be declared in that job field. Runtime requests cannot widen it.
`sw.FromProvenRunAtCommit()` instead reads only a successful run at the reading
run's own commit, on any branch, that was proven for the reader's repository and
set no run option beyond those that only place, schedule or present it (so no
only, start-at, stop-at, dry run, pause or debug), sealed no environment variables, ran every
job on a trusted host, and in which the referenced job succeeded. A controller that predates it is refused.
It cannot be combined with `sw.FromAnyRun()`.
TypeScript uses `refToLastRun(pipeline, jobID, { fromAnyRun: true })`.

`sw.RunAndAwait[Out, In](ctx, pipeline, jobID, options...)` starts a child run and
waits for its output. Options include `sw.WithFreshInputs(in)`,
`sw.WithFreshArgs(args)` and `sw.WithFreshTimeout(duration)`. The host owns child
submission, authorization, cancellation and cycle checks. A child that is still
pending or running when its parent run is cancelled or ends, or when the job that
started it ends, is cancelled. Call it inside an execution callback. While it waits, the job does not count against its runner's
`max_concurrent`, so the child can run on the same runner; once the child
finishes, the resumed job can briefly put the runner over that limit. Compose
reusable job types into a plan when the work belongs
in the same run.
Declare every pipeline a job may start with
`sw.Job(plan, "release", &Release{}).StartsChildren("deploy-us", "deploy-eu")`
(TypeScript `job.startsChildren("deploy-us", "deploy-eu")`); the controller
refuses an undeclared child with `403` and names `StartsChildren` as the repair.
For another repository, declare it with `StartsChildrenIn` (TypeScript
`startsChildrenIn`). A child at its parent's exact revision
inherits the parent's identity, including its repository and pipeline for OIDC
claims and pipeline secrets. A child that names a branch the controller resolves
runs as an asserted manual run under its own pipeline name and reads only
team-wide shared secrets. Use `WithFreshRepo("owner/repo")` (TypeScript
`runAndAwait` option `repo`) for an explicit repository and `WithFreshBranch`
(option `branch`) to override its default `main` branch. The parent's live claim
must authorize that repository. A child in another repository always names a
branch, never a commit, and is always asserted.
Children nest at most 10 levels deep
counting the root, and one root starts at most the team's
`max_children_per_root` (50 unless a team admin changes it, at most 1,000); see
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
allowance. When all application attempts fail, the final job error reports the
attempt count and last error. Fix that error before retrying the run.

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
Local and runner hosts load this failure from the persisted source job.
Calling `OnFailure` on the returned job declares the next recovery in the chain.

## Core requests

Local admission charges each job one core unless the job declares its CPU.
`job.Cores(n)` declares `n` cores, fractions allowed down to `0.25`. Give a job
that mostly waits on git or the network a small request such as `0.25`. A
larger request reserves cores for a job that must not share them, at the cost
of fewer jobs running at once. `JobGroup.Cores` sets every member. A request
larger than the machine reserves the whole machine. Hosts without local
admission, and hosts older than the setting, keep the one-core default. See
[Local admission](admission.md#core-requests).

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

Any team member whose credential holds `approvals.write` can approve or deny:
editors and owners, including the member who started the run. Readers
cannot. An approval is a workflow step that pauses a run for a person's
decision. It is not a security control: it does not stop a run's author from
approving their own run, and it does not limit what the run's code or secrets
can do. See [security](security.md#approvals).

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
Admission and planning refuse unsupported Cloud size labels and list the valid
sizes, including when the label appears in a comma-separated alternative.
The `sparkwing-` prefix belongs to Cloud. Requiring a Cloud size label selects
a fresh, isolated Cloud pod. Self-hosted, personal and pool runners advertise
their own sizes with other labels. Each selector term must match;
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
See [secret values](secrets.md#values-that-can-be-secrets) for the masking
length rule. Empty supplied arguments and defaults carry no value.
Secrets containing `***` are accepted.
Defaults from unselected pipelines register when long enough.
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
stay private until more input or EOF. A stored secret read of 4 or more characters is masked even when
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
Runtime mask registrations apply only to the registering job's output on its host.
Declare a secret if its value may appear in another job or an identifier on another host.

Child-run arguments containing a registered secret are refused before they leave
the host. This uses the same secret check as job output, including encoded spellings.

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
Commands resolve executables using their `PATH` or an explicit path, with relative
paths resolved against the working directory. On Windows, `PATHEXT` supplies
executable extensions. The working directory is searched only when named in
`PATH` or the command path. Hosts resolve the executable to an absolute path before
constructing and validating argv. Registered cleanups use the same lookup and
report the argument position and remedy when refused. Windows `.cmd`/`.bat` targets run through the
system `cmd.exe` with arguments escaped as Rust's standard library escapes them, so
`& | < > ^ ( ) !` and `%VAR%` arrive literally and `"` arrives as `""`, which
`node.exe` and other Microsoft C runtime programs read as one quote. Arguments
containing CR, LF or NUL, and batch paths containing `"`, `^` or `%`, are refused;
use a non-batch tool, such as `node.exe` with its JavaScript entrypoint, for those.
Batch files that enable delayed expansion or expand arguments or pipeline-set
environment values without quoting are outside the host's control.
An empty `Bash` script succeeds.
On Windows, `Exec` refuses the System32 WSL `bash.exe` launcher; install Git Bash or call `wsl` explicitly.
Engine credentials and protocol settings
are removed from child environments, including values supplied through `Env`.
Terminators include `Run`, `Capture`, `String`, `Lines`, `JSON` and `MustBeEmpty`.
Handle returned errors to fail the step. `ExecError` carries exit status and
captured output. Its message repeats at most 512 bytes of the command and counts
the rest; the `Command` field keeps it whole. When a context deadline kills a command, `Terminated` is
`"timeout"` and the error names the deadline. `errors.Is(err, context.DeadlineExceeded)`
recognizes the cause. Increase the context timeout or make the command faster.
`WorkDir()` returns the repository working directory.
It reads the process working directory. In author tests, use `t.Chdir(root)`
to select a fixture repository. Go restores the directory after the test.

Generic file helpers are removed. Use `os.ReadFile`, `os.WriteFile` and
`filepath.Glob` in Go, or `node:fs/promises` in TypeScript. Resolve relative
paths with `filepath.Join(sw.WorkDir(), name)` or Node's `path.resolve(workDir(), name)`.
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
body. A run must allow, with `--sw-allow-risk`, every risk on a step its
selection runs; steps that `--sw-only`, `--sw-start-at` or `--sw-stop-at` leave
out need no allowance. `sw.IsDryRun(ctx)` reads the mode inside a callback.

`cleanup.Register(ctx, cleanup.Spec{Argv: argv, Description: text})` registers an
idempotent teardown command for a resource outside the process tree. It returns
a release function. Call release after normal teardown so the host can discard
the registration. Import the `sparkwing/cleanup` subpackage for this helper.

## Testing author code

Import `github.com/sparkwing-dev/sparkwing/sparkwingtest` to exercise
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

## Runnable staging integration gate

In the Sparkwing source checkout, `sparkwing run example` runs the SDK tour
and named integration assertions in parallel. The default local path needs no
Cloud or Docker and targets a wall time below two minutes. The tour includes
an approval that approves after one second and an optional failing canary.
Those canary failures demonstrate collected errors and do not fail the run.
The tour emits its simulated build and deploy logs without pacing delays so
idle waits do not hold integration capacity.

The assertions cover a two-axis matrix, job and predecessor step outputs,
generated jobs, a dynamic same-repository child, dependency cache save and
restore, memo reuse, artifacts, cancellation, and CLI reads of controller logs
and run history. `memoized-step` puts one step in a memoized job because memo
policies belong to jobs. Lifecycle assertions start the small `example-probe`
pipeline as separate runs through the CLI. Use `--api-profile NAME` to exercise
a configured controller instead of the local controller, and `--cli PATH` to
select its matching CLI. No profile means local. The host retains cancellation
cleanup for detached probes until the gate observes a terminal state, including
for the optional reference run. A failed or interrupted observation keeps that
cleanup armed. Cache and artifact assertions require the current run's marker
so older stored bytes cannot satisfy a failed save or delivery.

With a local SDK replacement, keep tool caches in Git-ignored paths such as
`.sparkwing/cache/golangci-lint`. The pipeline binary cache hashes non-ignored
replacement files, so an active tool cache inside the checkout can invalidate
binary reuse.

The dependency-cache path includes the run ID to force a cold seed, because
restored cache keys are not saved again.
Child waits use their job's 90-second deadline, including queue time, instead
of a shorter nested deadline. This leaves the job one budget for admission and
execution.
The cancellation probe waits for the long job's body log before requesting
cancellation, so it exercises running work rather than SDK startup.
Use `--sw-only dynamic-child` to focus that assertion. The child resolves its
own job and step selections, so its `stamp` job still runs.

```bash
sparkwing run example -- --fail-once
sparkwing run example -- --secret-name EXAMPLE_GATE_SECRET
sparkwing run example -- --smoke-profile staging --smoke-checkout /path/to/sparkwing-ci-smoke
```

`--fail-once` asks the probe to fail on its source run. The failed-only retry
must reuse its successful predecessors, read their outputs and finish green.
The source job returns its run identity, so the probe recognizes a retry from
that saved output and `SPARKWING_RUN_ID` without needing a CLI in its job host.
`--secret-name` selects the declared optional secret `EXAMPLE_GATE_SECRET`.
The secret job skips with a reason when no name or stored value is configured.
OIDC skips only when the run has no issuer. Otherwise it mints a token and
asserts its audience, run, pipeline, issuer, subject and validity window.
Docker skips with a reason unless the runner advertises Docker or has a usable
daemon. `--child-branch` selects another same-repository branch tip when source
provenance is available.

The final `reference-pipeline` job submits `reference` from the supplied
`sparkwing-ci-smoke` checkout through `pipeline trigger`, waits for the separate
run and requires successful `hello` and `docker-test` jobs. Set
`--smoke-pipeline` if that repository uses another pipeline name. It skips when
`--smoke-profile` is absent. Cross-repository child runs are refused, so this
job uses an independent trigger rather than `RunAndAwait`.

A TypeScript twin lives in `examples/staging-typescript` as a separate project
because SDK selection applies to the whole pipeline directory. Test its native
feature assertions through the SDK protocol with Node 22.18 or newer:

```bash
npm --prefix examples/staging-typescript/.sparkwing test
```

These checks cover output replay, skip reasons, OIDC signatures, stale artifacts
and memo reuse assertions with controlled host services. Running the twin
through the candidate CLI is blocked: its local-file SDK dependency resolves
the development version `0.0.0`, which requires a verified `v0.0.0` engine. The
Go local replacement path can use the candidate CLI, while TypeScript has no
corresponding path. The twin's README names the stamped SDK and matching engine
prerequisites. The Go example remains the executable candidate staging gate.

## Detached child runs

Go pipelines can start a child without holding the parent job open:

```go
sparkwing.Job(plan, "submit-scan", func(ctx context.Context) error {
    run, err := sparkwing.StartChild(ctx, "scan", map[string]string{"target": "staging"})
    if err == nil {
        sparkwing.Info(ctx, "scan run: %s", run)
    }
    return err
}).StartsChildren("scan")
```

`StartChild` returns the admitted run ID. The child keeps the parent's repository,
revision and verified provenance, and continues when the parent completes or is
cancelled. It uses its own pipeline's concurrency groups. Ordinary `RunAndAwait`
children remain attached and are cancelled when their creating job can no longer
await them. TypeScript does not expose detached child submission.

A job can observe a child it started with `status, err := sparkwing.AwaitChild(ctx, run)`.
The native wait releases runner capacity and returns the terminal status, including `failed`
or `cancelled`, without requiring a job output. A transport failure or cancelled wait returns
an error. This observes only that job's own children. It does not attach to unrelated runs.

## Protocol compatibility

The SDK announces its protocol version in the `hello` reply. The engine records
that version for the process and selects its engine-side adapter from
`internal/nodehost/protocol_adapter.go`. Protocol 2 uses the identity adapter.
The engine's hello request always uses bootstrap protocol 2, including when
its current message protocol changes. Published SDKs require that request
version and may consume even a refused hello. SDK replies announce their own
message protocol independently, so the engine can select the right adapter.
Keep the hello envelope stable across message protocol versions.
The adapter upgrades incoming frames before the current codec and host policy
see them. It also translates outgoing requests when an older SDK needs a
different request shape. SDK and engine release versions remain separate from
the protocol version.

Additive changes never bump the protocol version. Add optional fields or
capability flags, and let the engine supply defaults when fields are absent.
A breaking wire change bumps the protocol version and adds an engine-side
adapter from the old shape to the current shape. Keep engines as backwards
compatible as possible with the SDK protocols listed in the signed mapping.
Delete an adapter as soon as that mapping no longer lists any SDK version using
its protocol. Unknown protocols fail with `protocol_mismatch`.

Go and TypeScript conformance traffic is retained under
`internal/nodeproto/conformance/testdata/recordings/v<protocol>/<language>/`.
Each accepted session records engine requests, raw SDK frames and expected
replies separately, including
successes, body and hook failures, generated jobs and protocol refusals.
`TestRecordedSDKProtocols` replays every recorded version through the current
engine's adapter, frame codec, session rules and result validation. It checks outgoing request translation and compares
reply outcomes with the recording. Log frames pass through the engine codec.
The corpus does not exercise host command execution. The replay needs no historical SDK binary.
New adapters must preserve those outcomes. Retain published recordings when an
adapter is removed, and update the replay to assert the explicit refusal for
that retired protocol.

Record a new version from the SDK source being published with one command from
an Xwing worktree (the TypeScript compiler dependencies are installed by the
command refuses to overwrite an existing protocol directory):

```bash
xwing env exec --session <environment> --repo <worktree> --scratch -- bash bin/record-sdk-protocol.sh 2
```

Replace `2` with the SDK's announced protocol version. Review and commit the new
version directory with the protocol change. Keep published version fixtures
unchanged. Run `go test -p 2 ./internal/nodehost -run TestRecordedSDKProtocols`
to replay the fixtures. Full Go and TypeScript conformance runs also check the
live SDKs against the shared corpus and schemas.
