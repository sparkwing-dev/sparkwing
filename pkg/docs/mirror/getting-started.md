# Getting Started

## Install

### macOS / Linux

Pre-built binaries are published to GitHub Releases. Pick the one for
your platform and drop it on PATH:

```bash
# macOS Apple Silicon
curl -fsSL https://github.com/sparkwing-dev/sparkwing/releases/latest/download/sparkwing-darwin-arm64 \
    -o /usr/local/bin/sparkwing && chmod +x /usr/local/bin/sparkwing

# macOS Intel
curl -fsSL https://github.com/sparkwing-dev/sparkwing/releases/latest/download/sparkwing-darwin-amd64 \
    -o /usr/local/bin/sparkwing && chmod +x /usr/local/bin/sparkwing

# Linux x86_64
curl -fsSL https://github.com/sparkwing-dev/sparkwing/releases/latest/download/sparkwing-linux-amd64 \
    -o /usr/local/bin/sparkwing && chmod +x /usr/local/bin/sparkwing

# Linux ARM64
curl -fsSL https://github.com/sparkwing-dev/sparkwing/releases/latest/download/sparkwing-linux-arm64 \
    -o /usr/local/bin/sparkwing && chmod +x /usr/local/bin/sparkwing
```

Or, if Go is on PATH, build from source:

```bash
go install github.com/sparkwing-dev/sparkwing/cmd/sparkwing@latest
```

This installs the `sparkwing` binary, which is the single CLI for
both admin / inspection (`sparkwing dashboard start`,
`sparkwing pipeline list`) and pipeline invocation
(`sparkwing run <pipeline>`).

> **Note:** `go install` does not include the Next.js dashboard
> bundle, which is a generated artifact and not checked into the
> repository. A source-built binary will refuse to start
> `sparkwing dashboard` with a clear message pointing back to the
> release binary. CLI-only commands (`run`, `pipeline`, `runs`, etc.)
> work fine. If you want the dashboard from a source checkout, run
> `bash bin/build-web.sh` first to generate the bundle, then
> `go install ./cmd/sparkwing` from the repo root.

### Windows

Prebuilt Windows binaries are published to GitHub Releases:
`sparkwing-windows-amd64.exe` and `sparkwing-windows-arm64.exe`. They
embed the dashboard bundle, so `sparkwing dashboard start` runs locally
just as it does on macOS/Linux. Download the one for your architecture,
rename it to `sparkwing.exe`, and put it on PATH. Install
[Git for Windows](https://git-scm.com/download/win) as well -- pipelines
call out to `sparkwing.Bash` / `sparkwing.Exec` at runtime.

To build from source instead, with a Go toolchain on PATH, in a Git Bash
terminal:

```bash
go install github.com/sparkwing-dev/sparkwing/cmd/sparkwing@latest
```

A source build does not include the Next.js dashboard bundle (it is a
generated artifact, not checked into the repository), so
`sparkwing dashboard start` on a source-built CLI refuses to start unless
you run `bash bin/build-web.sh` from a repo checkout first. The prebuilt
release binary has no such limitation.

The cluster-mode runner Service (`sparkwing-runner`) is Linux/macOS
only; Windows users dispatch pipelines to remote Linux/macOS runners
or to a remote cluster.

## Quick Start

```bash
# 1. Set up your repo
cd your-project
sparkwing pipeline new --name release   # single-node minimal template by default

# 2. Run your first pipeline
sparkwing run release

# 3. (Optional) Watch runs in the browser
sparkwing dashboard start    # detached local dashboard + API on :4343
```

For a build/test/deploy DAG instead of a single node, pass
`--template build-test-deploy`:

```bash
sparkwing pipeline new --name release --template build-test-deploy
```

Beyond the built-in stubs, `sparkwing pipeline templates` lists a
registry of curated, real-world starters (static-site deploys,
containerized deploys to Kubernetes, migrate+deploy, CI-hygiene gates,
and more). Filter with `--category` / `--cloud`, inspect one with
`--name <template>` (add `--body` to preview the rendered pipeline), then
scaffold with `--template <name> --param k=v`. See the
[template catalog](sparks.md#the-template-catalog) for the full workflow.

If you want to own and edit a spark library's helper code directly rather
than importing it, `sparkwing pipeline sparks vendor --module <name>`
copies its source into your repo. See
[vendoring](sparks.md#vendoring-a-spark-module).

Each `sparkwing` invocation compiles `.sparkwing/` and runs the pipeline as a host
subprocess. Run state lives under `~/.sparkwing/` (SQLite + log files).
`sparkwing dashboard start` spawns a detached local web server (`pkg/localws`,
embedded in the CLI) against the same SQLite store, exposing the dashboard
plus the JSON / logs APIs on one port - useful when several runs are going
in parallel and the terminal gets crowded. `sparkwing dashboard status` /
`kill` manage its lifecycle.

If you want a local Kubernetes cluster as a deploy target for user apps
(not for sparkwing itself), bring your own - any local Kubernetes setup
works. Sparkwing does not run in-cluster locally; the controller is a
prod-only component.

### Storage class

When you deploy sparkwing in-cluster (Helm chart at `charts/sparkwing-full`),
the controller provisions a PersistentVolumeClaim for its state DB. A PVC
that omits `storageClassName` falls back to the cluster's default
StorageClass; on clusters without one (some bare-metal kubeadm installs,
fresh kind clusters with the local-path provisioner not installed, etc.)
the PVC sits `Pending` indefinitely with no clear error.

Set the class explicitly via the chart value:

```bash
helm install sparkwing charts/sparkwing-full \
    --set controller.storage.pvc.storageClassName=gp3
```

Common values: `gp3` (EKS), `standard-rwo` (GKE), `managed-csi` (AKS),
`standard` (kind/minikube with the default local-path provisioner).

The controller logs a `WARNING` at startup when no PVC declares a class
and the cluster has no default StorageClass.

## What `sparkwing pipeline new` Creates

```
.sparkwing/
  sparkwing.yaml    # registry of every pipeline this repo defines
  main.go           # registers Go jobs
  README.md         # generated package README
  go.mod            # Go module for pipeline code
  go.sum            # dependency checksums (from go mod tidy)
  jobs/             # pipeline implementations
```

## The Model

A **pipeline** is anything `sparkwing` (or `sparkwing run`) can invoke. Two
shapes share the same surface:

- **triggered pipeline** - a YAML entry with an `on:` trigger; runs itself on push / webhook / schedule. Implemented as a Go type whose factory is registered via `sparkwing.Register`.
- **manual pipeline** - a YAML entry with no `on:` trigger. Runs only when explicitly invoked. Same Go registration; it's "triggered" vs "manual" distinguishes auto-firing from operator-initiated.

Both produce a Run in the local store on each invocation. The dashboard's
runs list and `sparkwing runs list` surface them uniformly. (For repo-local
bash chores -- formatters, port-forwards, the small Makefile-style stuff --
use dowing; sparkwing is the
Go-pipeline platform.)

A Go pipeline is a struct that implements `Plan(ctx, run) (*Plan, error)`
and returns a DAG of nodes. One-node pipelines return a Plan with a single
`Step`. See [`sdk.md`](sdk.md) for the SDK reference and
[`pipelines.md`](pipelines.md) for the Plan/Work model.

```yaml
# .sparkwing/sparkwing.yaml
pipelines:
  - name: build-deploy
    entrypoint: BuildDeploy
    description: Build and deploy the app
    on:
      push:
        branches: [main]
```

```go
// .sparkwing/jobs/build_deploy.go
import sw "github.com/sparkwing-dev/sparkwing/sparkwing"

type BuildDeploy struct{ sw.Base }

func (p *BuildDeploy) Plan(ctx context.Context, plan *sw.Plan, _ sw.NoInputs, run sw.RunContext) error {
    test := sw.Job(plan, "test", &Test{})
    sw.Job(plan, "build", &Build{}).Needs(test)
    return nil
}

type Test struct{ sw.Base }

func (j *Test) Work(w *sw.Work) (*sw.WorkStep, error) {
    sw.Step(w, "run", func(ctx context.Context) error {
        _, err := sw.Bash(ctx, "go test ./...").Run()
        return err
    })
    return nil, nil
}

type Build struct{ sw.Base }

func (j *Build) Work(w *sw.Work) (*sw.WorkStep, error) {
    sw.Step(w, "run", func(ctx context.Context) error {
        _, err := sw.Bash(ctx, "docker build -t myapp .").Run()
        return err
    })
    return nil, nil
}

// In .sparkwing/main.go:
//     sw.Register[sw.NoInputs]("build-deploy", func() sw.Pipeline[sw.NoInputs] { return &BuildDeploy{} })
```

Trivial single-step pipelines pass a `func(ctx) error` straight to `sw.Job`:

```go
type Lint struct{ sw.Base }

func (p *Lint) Plan(_ context.Context, plan *sw.Plan, _ sw.NoInputs, rc sw.RunContext) error {
    sw.Job(plan, rc.Pipeline, func(ctx context.Context) error {
        _, err := sw.Bash(ctx, "go vet ./...").Run()
        return err
    })
    return nil
}
```

Step boundaries inside a `Work()` are emitted automatically by each
`sw.Step` as structured `step_start` / `step_end` events; the
dashboard surfaces them as a collapsible bucket. For DAG-level
composition (parallel, sequence, needs, modifiers), use the `Plan`.

## Releasing sparkwing

Sparkwing tags itself via the in-repo `release` pipeline (no consumer
repo involvement). From the sparkwing checkout:

```bash
# preview: full validation chain, stop before tag+push (no allowance needed)
sparkwing run release --sw-dry-run

# real release -- push-tag is risk-gated, so --sw-allow is required:
sparkwing run release --sw-allow destructive,prod                    # auto-bump (default --bump minor) or top unreleased CHANGELOG entry
sparkwing run release --bump patch --sw-allow destructive,prod       # auto-bump patch instead
sparkwing run release --version v0.55.0 --sw-allow destructive,prod  # explicit version
```

The `push-tag` step declares `destructive` and `prod` risk labels, so an
actual tag+push requires `--sw-allow destructive,prod`; `--sw-dry-run` runs
every gate but stops before tagging and needs no allowance.

The pipeline runs validation gates before tagging, including:

- `validate-version` -- the resolved tag must be free on origin (refuses
  force-push)
- `check-clean-tree` -- working tree must be clean
- `gate-pre-commit` / `gate-pre-push` -- the same gate checks the git hooks run
- `prepare-changelog` -- CHANGELOG.md must contain a matching `[vX.Y.Z]`
  heading

Only after they pass does `push-tag` create the annotated tag and push it
to origin.

`sparkwing run release` is the canonical sparkwing-side release path. Don't
hand-tag and `git push` -- it bypasses the validation gates and makes
silent releases possible.

## Run Targets

`sparkwing run` executes locally; `sparkwing pipeline trigger` hands
execution to a profile's controller. Both take `--profile` to pick where
state lives and which controller to talk to. `sparkwing run` also takes
`--sw-ref <branch|tag|sha>` to compile a git ref instead of the working
tree (trigger runs the source registered with the controller):

```bash
sparkwing run build                              # run locally with local code
sparkwing run build --profile dev               # local code, state via "dev"
sparkwing run build --sw-ref main               # build the main ref locally
sparkwing pipeline trigger build --profile dev  # run on the "dev" cluster
sparkwing pipeline trigger build --profile prod # run on the "prod" cluster
```

Cluster names are profiles you configure with `sparkwing configure profiles
add`. Sparkwing itself does not run in-cluster locally - clusters named by
`--profile` are user-managed deploy targets, not local sparkwing
deployments.
