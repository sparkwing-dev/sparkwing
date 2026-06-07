# Scheduling

How sparkwing decides which runner executes a job. The model is simple:
**runners advertise labels, jobs declare the labels they need, and the
controller hands each job to a runner whose labels satisfy it.**

## The model in one paragraph

Each cluster runner advertises a set of **labels** (opaque equality
strings like `arm64`, `os=linux`, `gpu`). A job declares the labels it
needs -- per node via the Go SDK (`.Requires(...)`) or for the whole
pipeline via `requires:` in `sparkwing.yaml`. The controller's claim
query matches a job to a runner when the runner's advertised labels
satisfy the job's needed labels. A node that no runner can satisfy
fails the run at validation (the hard rail).

## Label-match semantics

Labels are compared as **literal equality strings** -- the matcher does
no parsing of `key=value`, so `os=linux` is one opaque token that must
appear verbatim in the runner's advertised set. Within a single term,
commas are **alternatives (OR)**; across separate terms, matches compose
with **AND**:

```
needs ["linux"]            have ["linux"]          -> match
needs ["linux"]            have ["macos"]          -> no match
needs ["linux,macos"]      have ["macos"]          -> match   (OR within a term)
needs ["linux","amd64"]    have ["linux"]          -> no match (AND across terms)
needs ["linux,macos","amd64"] have ["macos","amd64"] -> match
```

Empty / no needed labels match any runner.

## Per-node modifiers (Go SDK)

Three chainable modifiers on `*sparkwing.JobNode` (and the same names on
`*sparkwing.JobGroup`) control placement. All take the comma-OR / AND
term syntax above.

```go
// Hard filter: the run fails at validation if no runner can satisfy.
sw.Job(plan, "train", &Train{}).Requires("gpu")
sw.Job(plan, "package", &Package{}).Requires("arch=arm64", "trusted")
sw.Job(plan, "build", &Build{}).Requires("os=linux,macos", "amd64") // (linux OR macos) AND amd64

// Soft bias: never fails on its own. When more than one runner can
// satisfy Requires, pick the first matching a preference term (in order).
sw.Job(plan, "integration", &Integration{}).
    Requires("os=linux").
    Prefers("cloud-linux")

// Conditional: silently skip the node when the dispatching runner does
// not advertise the labels (downstream Needs treats it as satisfied).
preflight := sw.Job(plan, "preflight-sso", &CheckSSO{}).WhenRunner("local")
sw.Job(plan, "deploy", &Deploy{}).Needs(preflight)
```

- **`Requires`** -- hard constraint. A job no runner can satisfy fails
  the run at validation.
- **`Prefers`** -- soft ordering. Never fails; if nothing matches, the
  job dispatches via the default selection. Meaningful once more than one
  runner can claim the job.
- **`WhenRunner`** -- conditional execution. Skipped at dispatch when the
  active runner can't satisfy the terms; a runner that advertises no
  labels matches anything, so pipelines stay portable.

## Pipeline-level `requires` (`sparkwing.yaml`)

A pipeline entry can require labels of **every** job it contains, on top
of each node's own `.Requires()`:

```yaml
pipelines:
  - name: deploy-prod
    entrypoint: DeployProd
    requires: [warm-runner]
```

`requires` is a flat list of label terms. When set it wholesale replaces
the project `defaults.requires`. The reserved label **`local`** pins
execution to the in-process runner -- the same effect as the
`--sw-local-only` flag:

```yaml
pipelines:
  - name: seed-local-db
    entrypoint: SeedLocalDB
    requires: [local]
```

## How runners advertise labels

Cluster-mode runners (`sparkwing-runner`) advertise labels with the
repeatable `--label` flag:

```bash
sparkwing-runner --label arm64 --label os=linux --label gpu
```

The controller's claim query keeps only runners whose advertised set
satisfies a job's needed labels. When no connected runner advertises the
required labels, the warm pool logs a hint once and the node waits:

```
no warm runner advertises these labels; start a runner with
--label matching or remove .Requires()
```

The laptop-side drainer `sparkwing cluster worker` runs claimed jobs
in-process and is the local counterpart to the cluster runner.

## Direct (`sparkwing run`) vs dispatched (`trigger`)

`sparkwing run <pipeline>` executes the pipeline **locally, in this
process, on this machine** -- there is no controller and no claim step,
so label matching against remote runners does not apply (a node's
`.Requires()` is still validated, and `WhenRunner` evaluates against the
local runner). Use `requires: [local]` or `--sw-local-only` to force
in-process execution explicitly.

`sparkwing pipeline trigger <pipeline> --profile prod` hands the run to a
controller, which schedules each node onto a runner whose labels satisfy
its needed labels.

## Schedule triggers (cron)

A pipeline fires on a cron schedule via the `schedule` trigger in
`sparkwing.yaml`:

```yaml
pipelines:
  - name: nightly-rebuild
    entrypoint: NightlyRebuild
    on:
      schedule: "0 3 * * *"   # 03:00 daily
```

The controller evaluates schedules and enqueues a run when the cron
expression fires; the run then schedules onto a runner by the same label
rules as any other dispatched run.

## Worked examples

### Run only on the warm-runner pool

```go
sw.Job(plan, "deploy", &Deploy{}).Requires("warm-runner")
```

### Prefer ARM but accept anything

```go
sw.Job(plan, "build-image", &BuildImage{}).Prefers("arch=arm64")
```

If an ARM and an AMD runner are both idle, ARM wins by preference. If
only AMD is idle, the job runs there.

### A local-only preflight before a remote build

```go
preflight := sw.Job(plan, "check-sso", &CheckSSO{}).WhenRunner("local")
sw.Job(plan, "build", &Build{}).Needs(preflight)
```

The preflight runs when you `sparkwing run` locally and is skipped when
the same pipeline is dispatched to a cluster runner.
