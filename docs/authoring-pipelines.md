# Authoring idiomatic pipelines

A pipeline's `Plan` method builds the DAG and returns. The orchestrator
reads it on every `sparkwing pipeline explain`, every plan preview, and
every dispatch, and each read must produce the same shape. So `Plan` stays
pure and deterministic: shelling out, reading files, and branching on the
host all belong in a job or step body, which runs once, on the runner, at
dispatch.

`sparkwing pipeline lint` is the machine-checkable definition of
"idiomatic". It parses each `Plan` body and the `guards:` blocks in
`.sparkwing/sparkwing.yaml`, reports each violation by rule name, and exits
non-zero so it can gate a push or a CI job. `sparkwing pipeline lint
--rules` prints the live rule set, each with the charter of what it forbids
and why. Every rule has a section below with a do/don't pair.

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
type deployJob struct{ sparkwing.Base }

func (j *deployJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
    sparkwing.Step(w, "apply", j.apply)
    return nil, nil
}

func (j *deployJob) apply(ctx context.Context) error { return nil }
```

The two return values are the job's typed output step and a Plan-time
materialization error. An untyped job -- one that does not embed
`Produces[T]` -- has no output to designate, so it returns `nil, nil` once
its steps are registered; this is not an error case, it is the normal
return for the common case. A typed job returns the step whose value
becomes the `Produces[T]` output that `RefTo` exposes downstream:

```go
func (j *buildJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
    compile := sparkwing.Step(w, "compile", j.compile)
    publish := sparkwing.Step(w, "publish", j.publish)
    publish.Needs(compile)
    return publish, nil // this step's return value becomes the Job's Produces[T] output
}
```

## I/O in `Plan` (`plan-io`)

A `Plan` body that shells out, touches the filesystem, or makes an HTTP
call runs that I/O every time the plan is read. The runtime plan-guard
panics on it. Move the call into a job or step body, which runs at dispatch
on the runner. That includes reading configuration: calling `os.Getenv`
inside a job or step body is sanctioned, since the body runs once, at
dispatch, on the runner -- `Plan` is the only place it is forbidden.

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

Do the I/O inside the job, where it runs once at dispatch:

```go
type Release struct{ sparkwing.Base }

func (p *Release) Plan(ctx context.Context, plan *sparkwing.Plan, in sparkwing.NoInputs, rc sparkwing.RunContext) error {
    sparkwing.Job(plan, "publish", p.publish)
    return nil
}

func (p *Release) publish(ctx context.Context) error {
    sha, err := sparkwing.Bash(ctx, "git rev-parse HEAD").Lines() // runs at dispatch, on the runner
    if err != nil {
        return err
    }
    return sparkwing.Bash(ctx, "publish "+sha[0]).MustBeEmpty("publish failed")
}
```

## Choosing `Bash` versus `Exec`

Both run inside a job or step body; neither is a lint rule, so nothing
flags a wrong choice. Pick by where the values in the command come from.

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

Do declare the job unconditionally and let it decide at dispatch. The
`SkipIf` closure runs on the runner, so an environment read there is fine:

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

A blank runner label matches no runner, so the job strands forever. An
`Inline()` job runs in-process on the dispatcher, so a `Requires` or
`Prefers` label on it can never be honored -- declaring both signals
confused placement.

Don't strand a job on a blank or unhonored label:

```go
// blank label matches no runner
sparkwing.Job(plan, "build", func(ctx context.Context) error { return nil }).Requires("")

// inline runs in-process, so the label is never honored
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

## Unsatisfiable guards (`guard-misuse`)

A pipeline's `guards:` block gates dispatch on the resolved profile and
args. `require` blocks the run when not every token matches; `reject`
blocks it when any token matches. A token in both lists, a `require` that
names two mutually exclusive profiles, or a duplicate token describes a
pipeline that can never dispatch. The config parser accepts the syntax; the
linter catches the contradiction.

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
      reject:  [git:branch=default]  # never from the default branch
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
