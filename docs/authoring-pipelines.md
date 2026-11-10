# Authoring idiomatic pipelines

## A merge queue without pull requests

Create a landing pipeline in a repository whose policy permits direct pushes
to its default branch:

```bash
sparkwing pipeline new --pipeline land --template merge-queue
# In a TypeScript pipeline project, use this instead:
sparkwing pipeline new --pipeline land --template merge-queue-typescript
```

The TypeScript scaffold prints the npm lockfile setup command. Run it and
commit the lockfile before planning or running the pipeline. Both templates
use Bash and Git on the local machine. Add `bin/check.sh` with your repository's
required checks, including review if your policy requires it. A missing script
fails the entry. Commit the pipeline and check script before submitting.
The template discovers `origin`'s default branch. Edit `remote=origin` in the
generated job if your repository uses another remote.

Submit each clean, committed work branch from its own retained worktree:

```bash
sparkwing run land --sw-detached --sw-route local --sw-allow-risk destructive,prod -- --brief 'Explain the change and its intended result'
sparkwing queue list --group plan:global:merge-queue
sparkwing runs status --run <returned-run-id> --follow -o json
sparkwing runs cancel --run <returned-run-id>
```

Keep the worktree unchanged until the run finishes. The inline job edits that
checkout, so it must remain available on the submitting machine. Configure Git
authentication there with permission to push to the default branch. The remote's
branch protection still applies. All submitters must use the same controller
and concurrency group for serialization. A separate controller has a separate
queue. Group names share a team namespace, so give unrelated repositories
different group names and keep all landers for one repository on the same name.

Each run requires a non-empty brief and records it with its arguments and logs.
The capacity-one run group admits one entry at a time. The job refuses a dirty
checkout or a changed submission, merges the fetched default branch, runs
`bash bin/check.sh`, checks that branch, HEAD and checkout still match, and pushes
the exact checked commit without force. Checks must leave tracked and untracked
files clean. Configure generated output in `.gitignore` where appropriate.

A conflict aborts the merge. A failed check or rejected push fails only that
entry and releases its slot. Inspect the failed run, repair the worktree, and
submit again with a brief. If another writer moves the default branch during
checks, the push fails rather than checking a different merge implicitly.
The successful log records the landed commit. `runs status --follow` exits zero
for success and one for failure or cancellation. Runs and their logs also appear
in the dashboard.

Ordering follows concurrency admission, rather than a strict submission FIFO.
`queue list --group plan:global:merge-queue` reports group waiters and their arrival positions.
The dashboard queue page shows machine admission positions, which can differ
from group positions. Use a distinct group such as `deploy-<environment>` for
serialized deploys, release trains, migrations, and similar work. A concurrency
limit bounds simultaneous work. External API request rates still need pacing
in the job or the provider's own rate limiter.

## Pipeline structure

A pipeline's `Plan` method and each job's `Work` method build the DAG.
Plan inspection and execution must produce the same structure. Put I/O
and host-dependent decisions in registered job or step callbacks, which
execute after dispatch and may repeat on retry.

The Go examples below use the Go SDK. [TypeScript authoring](#typescript-authoring)
covers the same topics with its API and validation differences.

`sparkwing pipeline lint` checks each `Plan` body and the `guards:` blocks in
`.sparkwing/sparkwing.yaml` in Go projects, reports each violation by rule name, and exits
non-zero so it can gate a push or a CI job. `sparkwing pipeline lint
--rules` prints the live rule set.

## Adding a pipeline

Start with the scaffold so the Go registration and YAML catalog entry are
created together:

```sh
sparkwing pipeline new --pipeline deploy --template minimal
```

`sparkwing.Register` connects a name to its Go implementation in the
pipeline binary. `.sparkwing/sparkwing.yaml` defines the repository catalog
and its triggers, defaults, and guards. `sparkwing pipeline list` reads that
catalog without compiling Go, so a Go registration alone will not appear.

When adding a pipeline by hand, pair its registration in `.sparkwing/jobs/`:

<!-- registration-example -->
```go
func init() {
    sparkwing.Register("Deploy", func() sparkwing.Pipeline[sparkwing.NoInputs] {
        return &Deploy{}
    })
}
```

with an entry under `pipelines:` in `.sparkwing/sparkwing.yaml`:

```yaml
pipelines:
  - name: deploy
    entrypoint: Deploy
```

`entrypoint` names the registration key, and `name` is what
`sparkwing run` invokes. The two may differ, and several names may share
one entrypoint. The pipeline reads the invoked name from `RunContext.Pipeline`.
Run `sparkwing pipeline list` to confirm the catalog entry, then
`sparkwing pipeline lint` to check the pipeline source and guards.

## Sequencing jobs with `Needs`

A multi-job pipeline dispatches in the order its edges require, not the
order `Plan` calls `Job`. `Needs` declares that ordering: a job never
dispatches until every job it needs has succeeded.

```go
type Deploy struct{ sparkwing.Base }

func (p *Deploy) Plan(ctx context.Context, plan *sparkwing.Plan, in sparkwing.NoInputs, rc sparkwing.RunContext) error {
    build := sparkwing.Job(plan, "build", p.build)
    test := sparkwing.Job(plan, "test", p.test).Needs(build)
    sparkwing.Job(plan, "deploy", p.deploy).Needs(test)
    return nil
}

func (p *Deploy) build(ctx context.Context) error {
    _, err := sparkwing.Bash(ctx, "go build ./...").Run()
    return err
}

func (p *Deploy) test(ctx context.Context) error {
    _, err := sparkwing.Bash(ctx, "go test ./...").Run()
    return err
}

func (p *Deploy) deploy(ctx context.Context) error {
    return sparkwing.Bash(ctx, "./deploy.sh").MustBeEmpty("deploy failed")
}
```

`test` will not dispatch until `build` succeeds, and `deploy` waits on
`test` in turn. A job can chain any number of upstream `Needs`; a job
with none dispatches as soon as the runner has a slot. When a downstream
job needs an upstream job's typed output rather than just its completion,
wire a `Ref` and still add the `Needs` edge explicitly (see "Discarded
`Ref` results" below) -- `RefTo` does not add the edge for you.

## The `Work` return contract

A job with more than one step implements `Workable` instead of passing a
plain func to `Job`: it declares a `Work(w *sparkwing.Work) (*sparkwing.WorkStep, error)`
method, registers its steps onto `w` via `Step`, and returns.

```go
type ExampleDeploy struct{ sparkwing.Base }

func (j *ExampleDeploy) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
    sparkwing.Step(w, "apply", j.apply)
    return nil, nil
}

func (j *ExampleDeploy) apply(ctx context.Context) error { return nil }
```

The two return values are the job's typed output step and a Plan-time
materialization error. An untyped job -- one that does not embed
`Produces[T]` -- has no output to designate, so it returns `nil, nil` once
its steps are registered. A typed job returns the step whose value
becomes the `Produces[T]` output that `RefTo` exposes downstream:

```go
func (j *ExampleBuild) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
    compile := sparkwing.Step(w, "compile", j.compile)
    publish := sparkwing.Step(w, "publish", j.publish)
    publish.Needs(compile)
    return publish, nil
}
```

## I/O in `Plan` (`plan-io`)

A `Plan` body that shells out, touches the filesystem, or makes an HTTP
call runs that I/O every time the plan is read. The SDK's side-effect
helpers refuse outright: `sparkwing.Bash` / `Exec` / `Shell` and
anything in `sparkwing/docker`, `sparkwing/git`, or `sparkwing/services`
panic through the runtime plan-guard, naming the call. Plain `os`,
`os/exec`, and `net/http` calls have no such guard -- they run silently
on every read, and this lint rule catches them. Move I/O, including
configuration reads, into a registered job or step callback. `Work` also
constructs the graph before dispatch and must remain pure.

Don't shell out while the DAG is built:

```go
type Release struct{ sparkwing.Base }

func (p *Release) Plan(ctx context.Context, plan *sparkwing.Plan, in sparkwing.NoInputs, rc sparkwing.RunContext) error {
    sha, _ := sparkwing.Bash(ctx, "git rev-parse HEAD").Lines() // refused during plan construction
    sparkwing.Job(plan, "publish-"+sha[0], p.publish)
    return nil
}

func (p *Release) publish(ctx context.Context) error { return nil }
```

Do the I/O inside the registered job callback:

```go
type Release struct{ sparkwing.Base }

func (p *Release) Plan(ctx context.Context, plan *sparkwing.Plan, in sparkwing.NoInputs, rc sparkwing.RunContext) error {
    sparkwing.Job(plan, "publish", p.publish)
    return nil
}

func (p *Release) publish(ctx context.Context) error {
    sha, err := sparkwing.Bash(ctx, "git rev-parse HEAD").Lines()
    if err != nil {
        return err
    }
    return sparkwing.Exec(ctx, "publish", sha[0]).MustBeEmpty("publish failed")
}
```

## Choosing `Bash` versus `Exec` to run a shell command

Choose by where the values in the command come from.

Use `Exec` whenever an argument is dynamic -- a branch name, an image tag,
anything built from a variable. `Exec` runs the argv directly with no
shell, so there is no quoting to get wrong and no way for a value
containing `$`, backticks, or `;` to be read as shell syntax:

```go
tag := "app:" + sha
_, err := sparkwing.Exec(ctx, "docker", "push", tag).Run()
```

Reserve `Bash` for a command line that itself needs shell features -- a
pipe, a redirect, a glob, a conditional. Pass any dynamic value in through
`.Env()` instead of interpolating it into the line, so it never reaches
the shell parser:

```go
sparkwing.Bash(ctx, `git -C "$R" status --porcelain`).Env("R", repo).MustBeEmpty("dirty tree")
```

Interpolating an untrusted value straight into a `Bash` line is a
shell-injection risk; `Exec`, or `Bash` with `.Env()`, avoids it.

## Branching on the runtime environment (`plan-runtime-branch`)

`Plan` renders the same DAG wherever it runs, so plan inspection and dispatch
agree on the shape. Reading `os.Getenv`, switching on `runtime.GOOS`, or
calling `IsLocal()` in the body branches the structure on the host that
happens to read it. Express the condition where it belongs: a job-level
`SkipIf`, evaluated on the executing host, or a pipeline guard that gates the whole
run.

Don't branch the DAG on the host environment:

```go
func (p *Deploy) Plan(ctx context.Context, plan *sparkwing.Plan, in sparkwing.NoInputs, rc sparkwing.RunContext) error {
    if os.Getenv("ENV") == "prod" { // a different DAG depending on where Plan runs
        sparkwing.Job(plan, "deploy-prod", p.deployProd)
    }
    return nil
}
```

Declare the job unconditionally. After dependencies complete, its `SkipIf`
callback runs on the executing host under the job's award, inside its concurrency
slot. An environment read there sees that host's environment:

```go
func (p *Deploy) Plan(ctx context.Context, plan *sparkwing.Plan, in sparkwing.NoInputs, rc sparkwing.RunContext) error {
    sparkwing.Job(plan, "deploy-prod", p.deployProd).
        SkipIf(func(ctx context.Context) bool { return os.Getenv("ENV") != "prod" })
    return nil
}
```

A skipped job satisfies `Needs`, so dependent jobs can still run. If skipping
preparation must also skip its dependents, apply the predicate to those jobs
or to a `JobGroup` containing them.

An approval's `SkipIf` runs in a separate evaluation job before requesting approval,
holding no capacity. A true predicate marks the approval skipped and satisfies
its dependencies. Approvals cannot declare concurrency groups. Put the group on
the job after the approval so that job takes its slot on its executing host.
The predicate can read its declared `Needs`, secrets and pipeline references on
both run-host and controller-owned runs.

To gate the whole pipeline instead of one job, use a `guards:` block (see
below).

## Runner labels (`runner-label`)

A registered secret cannot appear in a runner requirement, preference or profile.
This applies to checkout defaults and pipeline entries, including declarations
that lose precedence, as well as SDK declarations. The host refuses the plan
before accepting its settings or jobs. Retrying a previously accepted run also
refuses secret-bearing identifiers before copying the sealed spec.

The linter flags blank runner labels on `Requires` and `Prefers`. The host trims
outer whitespace, drops empty labels and keeps the first copy of each label
in declaration order. Both SDKs preserve the declarations for host processing.
Alternatives within a selector term keep their existing OR semantics. An
`Inline()` job executes on the dispatcher's host, where `Requires` and `Prefers`
select no runner.

Avoid blank labels and labels on inline jobs:

```go
sparkwing.Job(plan, "build", func(ctx context.Context) error { return nil }).Requires("")

sparkwing.Job(plan, "setup", func(ctx context.Context) error { return nil }).Inline().Requires("linux")
```

Do label the job that needs a runner, and leave the inline job to the
dispatcher:

```go
sparkwing.Job(plan, "build", func(ctx context.Context) error { return nil }).Requires("linux")
sparkwing.Job(plan, "setup", func(ctx context.Context) error { return nil }).Inline()
```

## Discarded `Ref` results (`unused-ref`)

A `Ref` is the typed handle a downstream job reads an upstream job's output
through. Creating one with `RefTo` and discarding it -- into `_` or as a
bare statement -- is dead code: either wire it into a job or drop the
producing edge.

Don't throw the `Ref` away:

```go
build := sparkwing.Job(plan, "build", &Build{})
_ = sparkwing.RefTo[BuildOut](build) // nothing reads this Ref
```

Do wire it into the job that consumes the output:

```go
build := sparkwing.Job(plan, "build", &Build{})
out := sparkwing.RefTo[BuildOut](build)
sparkwing.Job(plan, "deploy", &Deploy{Build: out}).Needs(build)
```

## Shared cache across a group (`group-cache-shared`)

`JobGroup.Memo` applies one key function to every member. A constant
key makes every member share one result. Give members doing different
work distinct keys.

Don't cache the group:

```go
sparkwing.JobFanOut(plan, "matrix", goVersions, func(v string) (string, any) {
    return v, &Test{GoVersion: v}
}).Memo(func(ctx context.Context) (sparkwing.MemoValue, error) {
    return sparkwing.MemoKey("tests"), nil // one key for every Go version
})
```

Do key each member:

```go
matrix := sparkwing.JobFanOut(plan, "matrix", goVersions, func(v string) (string, any) {
    return v, &Test{GoVersion: v}
})
for _, member := range matrix.Members() {
    version := member.ID()
    member.Memo(func(ctx context.Context) (sparkwing.MemoValue, error) {
        return sparkwing.MemoKey("tests", version), nil
    })
}
```

A key callback returns `(MemoValue, error)`. Return errors when inputs
cannot be read; return `sparkwing.NoMemo, nil` to bypass memoization.
The executing host hashes the returned JSON value. Errors, panics, empty
callback values, and resolution deadlines fail before the body runs.
Use `.Cache()` to restore dependency directories before executing a job.

## Configuring generated jobs

`source.Generate` receives a plan for jobs created after the source completes.
Apply placement, retry and memo declarations to each generated job inside that
callback. `NeedsGenerated` waits for the generator's settlement barrier, including
an empty expansion.

```go
source := sparkwing.Job(plan, "discover", &Discover{})
shards := sparkwing.RefTo[[]Shard](source)
generated := source.Generate(func(ctx context.Context, expansion *sparkwing.Plan) error {
    for _, shard := range shards.Get(ctx) {
        sparkwing.Job(expansion, shard.Name, &Bench{Shard: shard}).
            Requires("gpu").Retry(2)
    }
    return nil
})
sparkwing.Job(plan, "finish", func(context.Context) error { return nil }).
    NeedsGenerated(generated)
```

The host validates the complete expansion before accepting it. Each generator
can declare at most 256 jobs. A failed source settles the barrier without
executing the callback. See [Generated jobs](sdk.md#generated-jobs).

## Declarative trigger filters and run guards

The `branches`, `paths`, and `actions` fields under `on.push` and
`on.pull_request` record intent. The controller dispatches the pipeline
named by the webhook URL without reading those fields.

Use a pipeline guard when the policy depends on the branch Sparkwing checks
out. `when: [git:branch=main]` blocks a run from any other checked-out
branch before a step starts. The literal name matches the head branch; it does
not match a pull request's base branch. `git:branch=default` matches the
repository's default branch: a GitHub App trigger records it, and any other
run reads origin's HEAD from its checkout, asking origin when the clone did
not record it. A run whose default branch cannot be resolved is refused
rather than judged without it. These guards do not implement path filters,
custom pull-request actions, or pull-request base-branch matching;
[Triggers](hooks.md) describes that boundary.

## Unsatisfiable guards (`guard-misuse`)

A pipeline's `guards:` block gates dispatch on where the run is
dispatched, args, and git branch -- `where:local` / `where:controller`,
`arg:FLAG=VALUE`, and `git:branch=NAME` /
`git:branch=default`. `when` blocks the run when not every token
matches; `unless` blocks it when any token matches. A token in both
lists or a `when` naming two mutually exclusive execution locations prevents
dispatch. A duplicate token is redundant. The linter reports both kinds
of defect. The
`default` token matches only when the dispatch supplies default-branch
metadata; use a literal branch for controller webhook and local trigger
claims.

Don't write guards that can never all hold:

```yaml
# .sparkwing/sparkwing.yaml
pipelines:
  - name: deploy
    entrypoint: Deploy
    guards:
      when: [where:local, where:controller] # mutually exclusive
      unless:  [where:controller]                # also rejected -> contradiction
```

Do pick tokens that can be satisfied together:

```yaml
# .sparkwing/sparkwing.yaml
pipelines:
  - name: deploy
    entrypoint: Deploy
    guards:
      when: [where:controller]  # run only on a controller
      unless:  [git:branch=main]     # never from the checked-out main branch
```

## Running the linter

```
sparkwing pipeline lint            # every pipeline in the repo
sparkwing pipeline lint --pipeline deploy    # one pipeline by name
sparkwing pipeline lint --rules          # print the rule charters
```

Add `-o json` for machine-readable findings. Point `--dir` at a source tree
other than the convention (`.sparkwing/jobs`). A non-zero exit on any
finding makes the command a drop-in gate for a pre-push hook or a CI job.

## TypeScript authoring

TypeScript uses the same catalog, controller and job graph as Go. Start with
`--template typescript` and the npm setup command in the
[quick start](getting-started.md#quick-start). Node.js 22.18 or later and npm
must be on PATH. Commit the exact SDK pin and native npm lockfile in
`.sparkwing/`. Sparkwing installs dependencies with `npm ci` and runs the
TypeScript entry through Node, rather than compiling a standalone executable.

### Registration and dependencies

`definePipeline` registers an implementation. Import its module from the
package's `main` entry and call `await main()` there. The YAML `entrypoint`
names that registration, while YAML `name` is the name passed to `sparkwing run`.
`pipeline list` reads YAML. `pipeline describe` reads declarations and
`pipeline plan` renders the resolved DAG without dispatching jobs.

```ts
import { definePipeline, main } from "@sparkwing/sdk";

definePipeline({
  name: "Deploy",
  plan(plan) {
    const build = plan.job("build", async (ctx) => {
      await ctx.exec(["npm", "run", "build"]).run();
      return { image: "app:checked" };
    });
    const test = plan.job("test", async (ctx) => {
      await ctx.exec(["npm", "test"]).run();
    }).needs(build);
    plan.job("deploy", async (ctx) => {
      const output = await ctx.output(build);
      if (!output) throw new Error("build output is unavailable");
      await ctx.exec(["./deploy.sh", output.image]).run();
    }).needs(build, test);
  },
});

await main();
```

`.needs(...)` supplies ordering. A job handle retains its output type, so
`ctx.output(build)` replaces Go's `RefTo` and `Ref.Get`. Reading an output does
not add a dependency. Declare the producer in `.needs(...)` as well. Keep and
use handles whose results you need, rather than constructing unused references.

### Steps and output returns

TypeScript has no Go `Workable` method or `(*WorkStep, error)` return pair.
Register steps with `job.step(id, callback)` during planning and connect them
with `.needs(...)`. A job or step callback returns its JSON-serializable
output, returns nothing for no output, and throws or rejects to fail.
`ctx.stepOutput(step)` reads a predecessor step's output within the same attempt.
A skipped predecessor has no output. Mark one step with `.result()` to make its returned value the job output,
replacing Go's returned output step. A job with a body cannot also register
steps. Use `plan.job(id)` without a body for a multi-step job.

### Pure planning and runtime conditions

Keep `plan` and step registration free of filesystem, network and process I/O.
They reconstruct the DAG for inspection and execution. Put I/O in job, step,
memo or other registered runtime callbacks. SDK side-effect helpers refuse
planning calls. Direct Node APIs such as `node:fs` and `fetch` do not become
safe merely because they bypass those helpers.

Do not branch the graph on `process.env`, `process.platform` or the machine
reading the plan. Declare the job and use `.skipIf(async (ctx) => ...)` for a
runtime condition. A skipped job satisfies dependencies. Apply the condition
to dependents too when they must skip together. Approval predicates run before
requesting approval and hold no capacity. Put concurrency on the job after an
approval, since approvals cannot declare concurrency groups.

### Commands and placement

Use `ctx.exec([program, ...args])` for dynamic arguments. Use `ctx.bash(script)`
for pipes, redirects or other shell syntax, passing dynamic values through
`.env(name, value)` instead of interpolating them. Bash requires Bash on the
executing host. Both helpers run commands through the host and propagate
command failures to the callback.

`.requires(...)` and `.prefers(...)` declare runner labels. Avoid blank labels.
The host trims whitespace, removes empty labels and keeps the first copy of
each label. `.inline()` runs on the dispatcher's host, where runner selectors
have no placement effect. These semantics match Go even though TypeScript
source receives no `runner-label` lint findings.

### Memoization, caches and generated jobs

`.memo(...)` declares a host-evaluated result key. Give jobs doing different
work distinct keys, including each matrix member's identity. Return `noMemo`
to bypass reuse. Key callbacks can reject when inputs cannot be read.
`.cache(...)` restores dependency directories before the body runs. Memoized
results and dependency caches serve different purposes.

Use `source.generate((generated, output, ctx) => { ... })` to declare jobs from
a source job's recorded output. Apply `.requires(...)`, `.retry(...)`,
`.memo(...)` and other modifiers to each job inside that callback. A dependent
uses `.needsGenerated(generator)` to wait for the expansion barrier, including
an empty expansion. The host validates the expansion before accepting it and
limits each generator to 256 jobs. See [Generated jobs](sdk.md#generated-jobs).

### Triggers, guards and validation

The YAML trigger filters and run guards described above apply to both SDKs.
Use satisfiable guard tokens and check branch semantics in
[Triggers](hooks.md). Trigger filters do not implement dispatch filtering.
TypeScript `pipeline lint` prints an unsupported notice and exits zero. JSON
reports `status: "unsupported"`. It analyzes neither TypeScript source nor its
guards, so it cannot serve as an authoring gate. Run your TypeScript checker,
`pipeline describe`, `pipeline plan` and focused execution tests. The host
validates declarations and graphs when they are submitted.

### Local runs, schedules and child runs

`sparkwing run <name>` runs TypeScript locally. `--sw-detached` submits a local
run that can continue after the CLI exits. Local schedules use the same
pipeline entry. Consult [Schedules](schedules.md) for installation, proof and
pinning behavior. TypeScript's Node-plus-script entry cannot use the standalone
executable proof/pinning path. Use `schedules install --no-prove` for a
following-source local schedule. `--unpinned` alone still attempts proof.
Validate the pipeline separately before installing without proof.

Child-run declarations use `.startsChildren(...)` and `.startsChildrenIn(...)`.
The TypeScript context supports awaited child runs, but exposes no detached
child-submission API. This differs from detaching the CLI invocation. See
[Detached child runs](sdk.md#detached-child-runs) for the Go API and its boundary.
