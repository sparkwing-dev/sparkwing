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

## I/O in `Plan` (`plan-io`)

A `Plan` body that shells out, touches the filesystem, or makes an HTTP
call runs that I/O every time the plan is read. The runtime plan-guard
panics on it. Move the call into a job or step body, which runs at dispatch
on the runner.

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
