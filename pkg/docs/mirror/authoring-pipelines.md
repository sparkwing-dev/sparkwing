# Authoring idiomatic pipelines

A pipeline's `Plan` method and each job's `Work` method build the DAG.
Plan inspection and execution must produce the same structure. Put I/O
and host-dependent decisions in registered job or step callbacks, which
execute after dispatch and may repeat on retry.

`sparkwing pipeline lint` checks each `Plan` body and the `guards:` blocks in
`.sparkwing/sparkwing.yaml`, reports each violation by rule name, and exits
non-zero so it can gate a push or a CI job. `sparkwing pipeline lint
--rules` prints the live rule set.

## Adding a pipeline

Start with the scaffold so the Go registration and YAML catalog entry are
created together:

```sh
sparkwing pipeline new --name deploy --template minimal
```

`sparkwing.Register` connects a name to its Go implementation in the
pipeline binary. `.sparkwing/sparkwing.yaml` defines the repository catalog
and its triggers, defaults, and guards. `sparkwing pipeline list` reads that
catalog without compiling Go, so a Go registration alone will not appear.

When adding a pipeline by hand, pair its registration in `.sparkwing/jobs/`:

```go
func init() {
    sparkwing.Register("deploy", func() sparkwing.Pipeline[sparkwing.NoInputs] {
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

The `name` matches the registration, and `entrypoint` names the Go type.
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
    sha, _ := sparkwing.Bash(ctx, "git rev-parse HEAD").Lines() // runs on every plan read
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

`Plan` renders the same DAG wherever it runs, so `explain` and dispatch
agree on the shape. Reading `os.Getenv`, switching on `runtime.GOOS`, or
calling `IsLocal()` in the body branches the structure on the host that
happens to read it. Express the condition where it belongs: a job-level
`SkipIf`, evaluated at dispatch, or a pipeline guard that gates the whole
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

Declare the job unconditionally. Its `SkipIf` callback runs on the
coordinator after dependencies complete; an environment read there sees
the coordinator's environment:

```go
func (p *Deploy) Plan(ctx context.Context, plan *sparkwing.Plan, in sparkwing.NoInputs, rc sparkwing.RunContext) error {
    sparkwing.Job(plan, "deploy-prod", p.deployProd).
        SkipIf(func(ctx context.Context) bool { return os.Getenv("ENV") != "prod" })
    return nil
}
```

To gate the whole pipeline instead of one job, use a `guards:` block (see
below).

## Runner labels (`runner-label`)

The linter rejects blank runner labels on `Requires`, `Prefers`, and
`WhenRunner`. An empty string is dropped when labels are normalized, so the
term vanishes; a whitespace label survives and matches no runner, so the term
can never be satisfied. An `Inline()` job executes on the dispatcher's host,
where `Requires` and `Prefers` select no runner; `WhenRunner` still applies
there, matched against the inline runner.

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

`JobGroup.Memoize` applies one key function to every member. A constant
key makes every member share one result. Give members doing different
work distinct keys.

Don't cache the group:

```go
sparkwing.JobFanOut(plan, "matrix", goVersions, func(v string) (string, any) {
    return v, &Test{GoVersion: v}
}).Memoize(func(ctx context.Context) (sparkwing.CacheKey, error) {
    return sparkwing.Key("tests"), nil // one key for every Go version
})
```

Do key each member:

```go
matrix := sparkwing.JobFanOut(plan, "matrix", goVersions, func(v string) (string, any) {
    return v, &Test{GoVersion: v}
})
for _, member := range matrix.Members() {
    version := member.ID()
    member.Memoize(func(ctx context.Context) (sparkwing.CacheKey, error) {
        return sparkwing.Key("tests", version), nil
    })
}
```

A key callback returns `(CacheKey, error)`. Return errors when inputs
cannot be read; return `sparkwing.NoCache, nil` to bypass memoization.
Errors, panics, empty keys, and resolution deadlines fail before dispatch.
Use `.CacheDir()` to restore dependency directories before executing a job.

## Configuring a dynamic fan-out (`dynamic-group-inert`)

`JobFanOutDynamic` builds its members from an upstream job's output, so the
group is empty while `Plan()` runs. Every `JobGroup` setter applies to the
members present when it is called, which on a dynamic group is none of them:
the call compiles, reads as configuration, and is dropped.

Don't configure the group:

```go
shards := sparkwing.Job(plan, "discover", &Discover{})
sparkwing.JobFanOutDynamic(plan, "bench", shards, func(s Shard) (string, any) {
    return s.Name, &Bench{Shard: s}
}).Requires("gpu").Retry(2) // neither reaches a generated job
```

Do configure the jobs the callback returns. `Requires`, `Prefers`, and
`WhenRunner` have provider interfaces a `Workable` implements, and each
generated instance can answer from its own data:

```go
type Bench struct {
    sparkwing.Base
    Shard Shard
}

func (b Bench) Requires() []string { return []string{b.Shard.Runner} }
```

The group itself stays useful as a dependency target: `Needs(group)` on a
downstream job waits for every generated member.

## Declarative trigger filters and run guards

The `branches`, `paths`, and `actions` fields under `on.push` and
`on.pull_request` record intent. The controller dispatches the pipeline
named by the webhook URL without reading those fields.

Use a pipeline guard when the policy depends on the branch Sparkwing checks
out. `require: [git:branch=main]` blocks a run from any other checked-out
branch before a step starts. The literal name matches the head branch; it does
not match a pull request's base branch. `git:branch=default` matches only when
the dispatch supplies default-branch metadata, which controller webhook and
local trigger claims do not. These guards do not implement path filters,
custom pull-request actions, or pull-request base-branch matching;
[Triggers](hooks.md) describes that boundary.

## Unsatisfiable guards (`guard-misuse`)

A pipeline's `guards:` block gates dispatch on the resolved profile,
args, and git branch -- `profile:local` / `profile:controller` /
`profile:name=NAME`, `arg:FLAG=VALUE`, and `git:branch=NAME` /
`git:branch=default`. `require` blocks the run when not every token
matches; `reject` blocks it when any token matches. A token in both
lists or a `require` naming two mutually exclusive profiles prevents
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
      require: [profile:local, profile:controller] # mutually exclusive
      reject:  [profile:controller]                # also rejected -> contradiction
```

Do pick tokens that can be satisfied together:

```yaml
# .sparkwing/sparkwing.yaml
pipelines:
  - name: deploy
    entrypoint: Deploy
    guards:
      require: [profile:controller]  # run only against a controller profile
      reject:  [git:branch=main]     # never from the checked-out main branch
```

## Running the linter

```
sparkwing pipeline lint --all            # every pipeline in the repo
sparkwing pipeline lint --name deploy    # one pipeline by name
sparkwing pipeline lint --rules          # print the rule charters
```

Add `-o json` for machine-readable findings. Point `--dir` at a source tree
other than the convention (`.sparkwing/jobs`). A non-zero exit on any
finding makes the command a drop-in gate for a pre-push hook or a CI job.
