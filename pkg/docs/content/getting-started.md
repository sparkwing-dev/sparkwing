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
(`sparkwing run <pipeline>`). The `wing` shortcut some examples
reference is a symlink to `sparkwing`; create it yourself if you
want the shorter name:

```bash
ln -s /usr/local/bin/sparkwing /usr/local/bin/wing
```

### Windows

No prebuilt Windows binary yet. The CLI does build cleanly from
source on Windows; install [Git for Windows](https://git-scm.com/download/win)
(needed at runtime: pipelines call out to `sparkwing.Bash` / `sparkwing.Exec`)
plus a Go toolchain, then in a Git Bash terminal:

```bash
go install github.com/sparkwing-dev/sparkwing/cmd/sparkwing@latest
```

The `wing` shortcut is not installed on Windows -- invoke pipelines
as `sparkwing run <pipeline>` instead.

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

(If you symlinked `wing -> sparkwing` per the install instructions
above, `wing release` is the same thing as `sparkwing run release`.)

For a build/test/deploy DAG instead of a single node, pass
`--template build-test-deploy`:

```bash
sparkwing pipeline new --name release --template build-test-deploy
```

Each `wing` invocation compiles `.sparkwing/` and runs the pipeline as a host
subprocess. Run state lives under `~/.sparkwing/` (SQLite + log files).
`sparkwing dashboard start` spawns a detached local web server (`pkg/localws`,
embedded in the CLI) against the same SQLite store, exposing the dashboard
plus the JSON / logs APIs on one port - useful when several runs are going
in parallel and the terminal gets crowded. `sparkwing dashboard status` /
`kill` manage its lifecycle. (A standalone `sparkwing-local-ws` binary
still ships as an opt-in wrapper for running the dashboard as a separate
process; the laptop default uses the detached spawn.)

If you want a local Kubernetes cluster as a deploy target for user apps
(not for sparkwing itself), bring your own - any local Kubernetes setup
works. Sparkwing does not run in-cluster locally; the controller is a
prod-only component.

### Storage class

When you deploy sparkwing in-cluster (Helm chart at `charts/sparkwing`
or the manifests under `k8s/sparkwing/`), the controller / cache / dind
pods each provision a PersistentVolumeClaim. PVCs that omit
`storageClassName` fall back to the cluster's default StorageClass; on
clusters without one (some bare-metal kubeadm installs, fresh kind
clusters with the local-path provisioner not installed, etc.) those
PVCs sit `Pending` indefinitely with no clear error.

Set the class explicitly via `storage.className`:

```bash
helm install sparkwing charts/sparkwing --set storage.className=gp3
```

Common values: `gp3` (EKS), `standard-rwo` (GKE), `managed-csi` (AKS),
`standard` (kind/minikube with the default local-path provisioner).

For the kustomize path (`k8s/sparkwing/`), patch `storageClassName` per
PVC in your overlay. The controller logs a `WARNING` at startup when no
PVC declares a class and no default exists on the cluster.

## What `sparkwing pipeline new` Creates

```
.sparkwing/
  pipelines.yaml    # registry of every pipeline this repo defines
  main.go           # registers Go jobs
  go.mod            # Go module for pipeline code
  go.sum            # dependency checksums
  jobs/             # pipeline implementations
  shared/           # repo-specific reusable helpers (optional)
```

## The Model

A **pipeline** is anything `wing` (or `sparkwing run`) can invoke. Two
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
# .sparkwing/pipelines.yaml
build-deploy:
  description: Build and deploy the app
  on:
    push:
      branches: [main]
  tags: [ci, deploy]
```

```go
// .sparkwing/jobs/build_deploy.go
import sw "github.com/sparkwing-dev/sparkwing/sparkwing"

type BuildDeploy struct{ sw.Base }

func (p *BuildDeploy) Plan(ctx context.Context, plan *sw.Plan, _ sw.NoInputs, run sw.RunContext) error {
    test := sw.Job(plan, "test", &TestJob{})
    sw.Job(plan, "build", &BuildJob{}).Needs(test)
    return nil
}

type TestJob struct{ sw.Base }

func (j *TestJob) Work() *sw.Work {
    w := sw.NewWork()
    w.Step("run", func(ctx context.Context) error {
        _, err := sw.Bash(ctx, "go test ./...").Run()
        return err
    })
    return w
}

type BuildJob struct{ sw.Base }

func (j *BuildJob) Work() *sw.Work {
    w := sw.NewWork()
    w.Step("run", func(ctx context.Context) error {
        _, err := sw.Bash(ctx, "docker build -t myapp .").Run()
        return err
    })
    return w
}

// In .sparkwing/main.go:
//     sw.Register[sw.NoInputs]("build-deploy", func() sw.Pipeline[sw.NoInputs] { return &BuildDeploy{} })
```

Trivial single-step pipelines just register one Job via `sw.JobFn`:

```go
type Lint struct{ sw.Base }

func (p *Lint) Plan(_ context.Context, plan *sw.Plan, _ sw.NoInputs, rc sw.RunContext) error {
    sw.Job(plan, rc.Pipeline, sw.JobFn(func(ctx context.Context) error {
        _, err := sw.Bash(ctx, "go vet ./...").Run()
        return err
    }))
    return nil
}
```

Step boundaries inside a `Work()` are emitted automatically by each
`w.Step` as structured `step_start` / `step_end` events; the
dashboard surfaces them as a collapsible bucket. For DAG-level
composition (parallel, sequence, needs, modifiers), use the `Plan`.

## Releasing sparkwing

Sparkwing tags itself via the in-repo `release` pipeline (no consumer
repo involvement). From the sparkwing checkout:

```bash
wing release                          # auto-bump from latest tag (default --bump minor),
                                      #   or pick the top unreleased CHANGELOG entry
wing release --bump patch             # auto-bump patch instead
wing release --version v0.55.0        # explicit version
wing release --dry-run                # full validation chain, skip tag+push
```

The pipeline runs four checks before pushing:

- `validate-version` -- the resolved tag must be free on origin (refuses
  force-push)
- `check-clean-tree` -- working tree must be clean
- `check-changelog` -- CHANGELOG.md must contain a matching `[vX.Y.Z]`
  heading
- `push-tag` -- creates the annotated tag and pushes to origin

`wing release` is the canonical sparkwing-side release path. Don't
hand-tag and `git push` -- it bypasses the validation gates and makes
silent releases possible.

## Run Targets

Every pipeline supports `--on` and `--from` flags:

```bash
wing build                          # run locally with local code
wing build --on dev                 # run on the "dev" cluster with local code
wing build --on prod                # run on the "prod" cluster with local code
wing build --from main --on dev     # run main branch code on "dev" cluster
wing build --from main --on prod    # run main branch code on "prod" cluster
```

Cluster names are profiles you configure with `sparkwing configure profiles
add`. Sparkwing itself does not run in-cluster locally - clusters that
appear in `--on` are user-managed deploy targets, not local sparkwing
deployments.
