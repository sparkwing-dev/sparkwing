# Migrate from GitHub Actions

This guide maps each part of a GitHub Actions workflow to its Sparkwing equivalent, with the workflow YAML first and
then the same thing in Go and TypeScript. Each row is marked:

- **Works today**: a direct equivalent exists.
- **Works differently**: you can do it, but the shape differs from GitHub Actions.
- **Not yet**: no equivalent. The linked [known limitations](known-limitations.md) entry gives the workaround.

The gentlest start is to keep your workflow and call Sparkwing from it: a GitHub Actions job runs
`sparkwing run ci` on the runner it already has. [Running inside an existing CI job](ci-embedded.md#quick-start-github-actions)
gives a ready workflow. Move triggers, secrets and runners over once the pipeline itself works.

| GitHub Actions | Sparkwing | Status |
|---|---|---|
| [`actions/checkout`](#checkout-setup-and-toolchains) | Every job starts in a checkout of the run's commit | Works differently |
| [`actions/setup-*`](#checkout-setup-and-toolchains) | Go and Node on Cloud; fetch other versions into a cached directory | Works differently. Pinned toolchains: [Not yet](known-limitations.md#cloud-runners-ship-only-go-and-node) |
| [`actions/cache`](#cache) | `Cache` on the job | Works today |
| [`strategy.matrix`](#matrix) | One job per item, built in code | Works today |
| [`needs`](#needs) | `Needs` | Works today |
| [`if`](#if) | `SkipIf`, `OnFailure`, `AfterRun` | Works today |
| [`secrets.*`](#secrets) | Declared secrets, `sparkwing secrets set` | Works today. Environments: [Not yet](known-limitations.md#no-deployment-environments) |
| [`upload-artifact` / `download-artifact`](#artifacts) | `Artifacts` and `Consumes` | Works differently |
| [`concurrency`](#concurrency) | Concurrency groups | Works today. `cancel-in-progress`: [Not yet](known-limitations.md#superseded-runs-are-not-cancelled) |
| [`on.schedule`](#schedule) | `on.schedule` in `.sparkwing/sparkwing.yaml` | Works today |
| [`workflow_dispatch` inputs](#workflow_dispatch-inputs) | Typed pipeline inputs | Works today |
| [Deploy on push to `main`](#deploy-on-main) | `on.push.branches` plus run guards | Works differently |
| [Required status checks](#status-checks) | One `sparkwing/<pipeline>` check run per pipeline | Works differently. Per-job checks: [Not yet](known-limitations.md#one-check-run-per-pipeline) |
| [Failure notifications](#notifications) | Signed run events to your own receiver | Works differently |
| `on.push.paths` | | [Not yet](known-limitations.md#path-filters-turn-a-pipeline-off) |
| `on.push.tags`, `on.release` | Manual subscription | [Not yet](known-limitations.md#tag-and-release-triggers-need-a-manual-subscription) in repository YAML |
| Pull requests from forks | | [Not yet](known-limitations.md#pull-requests-from-forks-do-not-run) |

## Where pipelines live

A workflow file becomes a pipeline in `.sparkwing/`. Scaffold one in Go or TypeScript:

```bash
# Go
sparkwing pipeline new --pipeline ci

# TypeScript
sparkwing pipeline new --pipeline ci --template typescript
```

The TypeScript scaffold prints an `npm ... install --package-lock-only` command. Run it once and commit the lockfile.
Each pipeline is listed in `.sparkwing/sparkwing.yaml`:

```yaml
pipelines:
  - name: ci
    entrypoint: Ci
```

A Go pipeline is a type with a `Plan` method. A GitHub Actions job becomes `sw.Job`, and its `run:` steps become shell
commands or plain Go:

```go
package jobs

import (
    "context"

    sw "github.com/sparkwing-dev/sparkwing/sparkwing"
)

type Ci struct{ sw.Base }

func (Ci) Plan(ctx context.Context, plan *sw.Plan, _ sw.NoInputs, rc sw.RunContext) error {
    sw.Job(plan, "test", func(ctx context.Context) error {
        _, err := sw.Bash(ctx, "go test ./...").Run()
        return err
    })
    return nil
}

func init() {
    sw.RegisterEntrypoint[sw.NoInputs]("Ci", func() sw.Pipeline[sw.NoInputs] { return &Ci{} })
}
```

A TypeScript pipeline is a `definePipeline` call imported from `.sparkwing/index.ts`:

```ts
import { definePipeline } from "@sparkwing/sdk";

definePipeline({
  name: "Ci",
  plan(plan, run) {
    plan.job("test", async (ctx) => {
      await ctx.bash("go test ./...").run();
    });
  },
});
```

The Go snippets below are the body of `Plan`, with `plan` and `rc` in scope. The TypeScript snippets are the body of
`plan(plan, run)`, with the imports they name added to the file. Run a pipeline on your machine with
`sparkwing run ci`. To run it on GitHub events, connect the repository through the
[GitHub App](github-app.md#repository-managed-triggers). To keep using GitHub-hosted runners while you move, see
[GitHub Actions runners](github-actions-runners.md).

## Checkout, setup and toolchains

```yaml
steps:
  - uses: actions/checkout@v4
  - uses: actions/setup-node@v4
    with:
      node-version: 20
  - run: npm ci && npm test
```

There is no checkout step. On Sparkwing Cloud and shared runners, every job starts in a checkout of the run's commit.
A run on your machine works in your own checkout instead, so its jobs share your working tree and leave their outputs
in it; see [known limitations](known-limitations.md#local-jobs-share-your-working-tree). Paths are relative to the
repository root. For history or tags, as with `fetch-depth: 0`, call `plan.Checkout` (Go) or `plan.checkout({ fullHistory: true })`
(TypeScript).

Sparkwing Cloud runners ship one Go and one Node version, and nothing else. Your own runners have whatever their
machines have installed. To pin another version, fetch it into a directory the job caches. This script installs Node
20 on Linux amd64 or arm64, checks its published SHA-256, and stages downloads inside `.tools` so the final rename stays on one filesystem:

```bash
# scripts/setup-node.sh, sourced by a job
NODE_VERSION=20.19.5
case "$(uname -m)" in
  x86_64) NODE_ARCH=x64; NODE_SHA256=315046739a513a70e03a4a55a8afda8cf979f30852e576075c340084e3f8ac0f ;;
  aarch64|arm64) NODE_ARCH=arm64; NODE_SHA256=d462267863ae8ee556039ebdf559055a8ec562c633889ef1403f3adb449ba1dd ;;
  *) echo "unsupported Node architecture" >&2; return 1 ;;
esac
[ "$(uname -s)" = Linux ] || { echo "this recipe requires Linux" >&2; return 1; }
NODE_HOME="$PWD/.tools/node-$NODE_VERSION-linux-$NODE_ARCH"
setup_node() (
  mkdir -p .tools || exit 1
  download=$(mktemp -d "$PWD/.tools/sparkwing-node.XXXXXX") || exit 1
  trap 'rm -rf "$download"' EXIT
  curl -fsSLo "$download/node.tar.xz" "https://nodejs.org/dist/v$NODE_VERSION/node-v$NODE_VERSION-linux-$NODE_ARCH.tar.xz" || exit 1
  echo "$NODE_SHA256  $download/node.tar.xz" | sha256sum -c - || exit 1
  mkdir "$download/node" || exit 1
  tar -xJf "$download/node.tar.xz" -C "$download/node" --strip-components=1 || exit 1
  mv "$download/node" "$NODE_HOME"
)
[ -x "$NODE_HOME/bin/node" ] || setup_node || return 1
export PATH="$NODE_HOME/bin:$PATH"
```

```go
sw.Job(plan, "test", func(ctx context.Context) error {
    _, err := sw.Bash(ctx, ". scripts/setup-node.sh && npm ci && npm test").Run()
    return err
}).Cache(sw.Dir(".tools", sw.KeyFromFile("scripts/setup-node.sh")))
```

```ts
// import { dir } from "@sparkwing/sdk";
plan.job("test", async (ctx) => {
  await ctx.bash(". scripts/setup-node.sh && npm ci && npm test").run();
}).cache(dir(".tools", "scripts/setup-node.sh"));
```

For `actions/setup-go`, pin the Go version and its archive checksums in the same way:

```yaml
- uses: actions/setup-go@v7
  with:
    go-version: '1.26.11'
- run: go version && go test ./...
```

This recipe selects the Linux archive for the machine running the job. `x86_64` maps to Go's `amd64`,
and `aarch64` or `arm64` maps to `arm64`. These checksums apply only to those two Linux archives.
For macOS, Windows or another architecture, choose its archive and published checksum from
[Go downloads](https://go.dev/dl/). `GOARCH` used for cross-compiling your application does not select the toolchain archive.

```bash
# scripts/setup-go.sh, sourced by a job
GO_VERSION=1.26.11
case "$(uname -m)" in
  x86_64) GO_ARCH=amd64; GO_SHA256=9849f4e04ee35303714e34fbc3ac8ab44ea3e448a95aa182f91e3b9b79c710ec ;;
  aarch64|arm64) GO_ARCH=arm64; GO_SHA256=6c2f29b458b5add0835f18b776f69c8881a6d88428f1484a6da55755e620757e ;;
  *) echo "unsupported Go architecture" >&2; return 1 ;;
esac
[ "$(uname -s)" = Linux ] || { echo "this recipe requires Linux" >&2; return 1; }
GO_HOME="$PWD/.tools/go-$GO_VERSION-linux-$GO_ARCH"
setup_go() (
  mkdir -p .tools || exit 1
  download=$(mktemp -d "$PWD/.tools/sparkwing-go.XXXXXX") || exit 1
  trap 'rm -rf "$download"' EXIT
  curl -fsSLo "$download/go.tar.gz" "https://go.dev/dl/go$GO_VERSION.linux-$GO_ARCH.tar.gz" || exit 1
  echo "$GO_SHA256  $download/go.tar.gz" | sha256sum -c - || exit 1
  tar -xzf "$download/go.tar.gz" -C "$download" || exit 1
  mv "$download/go" "$GO_HOME"
)
[ -x "$GO_HOME/bin/go" ] || setup_go || return 1
export PATH="$GO_HOME/bin:$PATH"
export GOROOT="$GO_HOME"
export GOTOOLCHAIN=local
```

```go
sw.Job(plan, "test-go", func(ctx context.Context) error {
    _, err := sw.Bash(ctx, ". scripts/setup-go.sh && go version && go test ./...").Run()
    return err
}).Cache(sw.Dir(".tools", sw.KeyFromFile("scripts/setup-go.sh")))
```

```ts
// import { dir } from "@sparkwing/sdk";
plan.job("test-go", async (ctx) => {
  await ctx.bash(". scripts/setup-go.sh && go version && go test ./...").run();
}).cache(dir(".tools", "scripts/setup-go.sh"));
```

Get new checksum values from [Go downloads](https://go.dev/dl/) or
[Node's release checksums](https://nodejs.org/dist/v20.19.5/SHASUMS256.txt) when changing versions.
The cache paths include the version and architecture. Keep these setup jobs sequential on a local checkout,
because local jobs share its `.tools` directory. `GOROOT` points at the installed archive even when the runner inherited
another Go installation. `GOTOOLCHAIN=local` prevents Go from downloading a different toolchain.
To check the pin, look for `go version go1.26.11 linux/amd64` or `linux/arm64` in the job log.
Both SDKs key the `.tools` cache on `scripts/setup-go.sh`, so changing the version or checksum invalidates it.
Since `actions/setup-go` also caches Go modules by default, add [`GoModules()` (Go) or `goModules()` (TypeScript)](#cache) alongside the `.tools` cache for equivalent module caching.

Python, Java and other toolchains follow the same pattern, or run inside `docker run <image>` where Docker is
available. Changing the script changes the cache key, so a version bump downloads once and is cached again.

## Cache

```yaml
- uses: actions/cache@v4
  with:
    path: ~/go/pkg/mod
    key: go-${{ hashFiles('go.sum') }}
```

`Cache` restores before the job and saves after it succeeds. `GoModules` and `Npm` are presets; `Dir` caches any
directory in the checkout, keyed on the files you name.

```go
sw.Job(plan, "test", func(ctx context.Context) error {
    _, err := sw.Bash(ctx, "go test ./...").Run()
    return err
}).Cache(sw.GoModules())
```

```ts
// import { npm, dir } from "@sparkwing/sdk";
plan.job("test", async (ctx) => {
  await ctx.bash("npm ci && npm test").run();
}).cache(npm(), dir(".venv", "requirements.txt"));
```

Sparkwing can also skip a job whose inputs have not changed since a successful run. See `Memo` in
[caching](caching.md).

## Matrix

```yaml
strategy:
  matrix:
    target: [linux-amd64, linux-arm64, darwin-arm64]
steps:
  - run: make build TARGET=${{ matrix.target }}
```

A matrix is a loop that declares one job per item. There is no `include`/`exclude` syntax; filter the list in code.

```go
sw.JobFanOut(plan, "build", []string{"linux-amd64", "linux-arm64", "darwin-arm64"}, func(target string) (string, any) {
    return "build-" + target, func(ctx context.Context) error {
        _, err := sw.Bash(ctx, "echo building "+target).Run()
        return err
    }
})
```

```ts
const builds = ["linux-amd64", "linux-arm64", "darwin-arm64"].map((target) =>
  plan.job(`build-${target}`, async (ctx) => {
    await ctx.bash(`echo building ${target}`).run();
  }));
```

`JobFanOut` returns a group you can pass to `Needs`. In TypeScript, pass the array: `.needs(...builds)`. When the list
comes from an earlier job's output, use `Generate` (see [pipelines](pipelines.md)).

## Needs

```yaml
jobs:
  build: { ... }
  test:
    needs: build
```

```go
build := sw.Job(plan, "build", func(ctx context.Context) error {
    _, err := sw.Bash(ctx, "echo build").Run()
    return err
})
sw.Job(plan, "test", func(ctx context.Context) error {
    _, err := sw.Bash(ctx, "echo test").Run()
    return err
}).Needs(build)
```

```ts
const build = plan.job("build", async (ctx) => { await ctx.bash("echo build").run(); });
plan.job("test", async (ctx) => { await ctx.bash("echo test").run(); }).needs(build);
```

Jobs without `Needs` run in parallel. A job whose dependency failed does not start.

## If

```yaml
deploy:
  if: github.ref == 'refs/heads/main'
notify:
  if: failure()
cleanup:
  if: always()
```

`SkipIf` replaces `if:` conditions. It is plain code, so there is no expression language. `OnFailure` declares a job
that runs only when its job fails, and `AfterRun` runs after every outcome.

```go
sw.Job(plan, "deploy", func(ctx context.Context) error {
    _, err := sw.Bash(ctx, "echo deploying").Run()
    return err
}).SkipIf(func(ctx context.Context) bool {
    return rc.Git.Branch != "main"
}).AfterRun(func(ctx context.Context, err error) {
    sw.Info(ctx, "deploy finished")
}).OnFailure("report-failure", func(ctx context.Context) error {
    sw.Info(ctx, "deploy failed")
    return nil
})
```

`OnFailure` returns the recovery job, so call `SkipIf` and `AfterRun` on the deploy job before it, as above.

```ts
const deploy = plan.job("deploy", async (ctx) => { await ctx.bash("echo deploying").run(); })
  .skipIf((ctx) => ctx.run.git.branch !== "main")
  .afterRun((ctx, result) => { ctx.log.info(`deploy finished: ${result.outcome}`); });
deploy.onFailure("report-failure", async (ctx) => { ctx.log.info("deploy failed"); });
```

A skipped job reports `reason=skip_if` and still satisfies the jobs that need it. To stop a whole pipeline running on
the wrong branch, use a [run guard](#deploy-on-main) instead.

## Secrets

```yaml
- run: ./deploy.sh
  env:
    DEPLOY_TOKEN: ${{ secrets.DEPLOY_TOKEN }}
```

A pipeline declares the secrets it reads. A run whose declared secret is missing fails before any job starts, and its
error names the command that sets it. Values are masked in logs.

```go
type ReleaseSecrets struct {
    DeployToken string `sw:"DEPLOY_TOKEN,required"`
}

type Release struct{ sw.Base }

func (Release) Secrets() any { return &ReleaseSecrets{} }

func (Release) Plan(ctx context.Context, plan *sw.Plan, _ sw.NoInputs, rc sw.RunContext) error {
    sw.Job(plan, "deploy", func(ctx context.Context) error {
        token := sw.PipelineSecrets[ReleaseSecrets](ctx).DeployToken
        _, err := sw.Bash(ctx, "./deploy.sh").Env("DEPLOY_TOKEN", token).Run()
        return err
    })
    return nil
}

func init() {
    sw.RegisterEntrypoint[sw.NoInputs]("Release", func() sw.Pipeline[sw.NoInputs] { return &Release{} })
}
```

```ts
import { definePipeline } from "@sparkwing/sdk";

definePipeline({
  name: "Release",
  secrets: [{ name: "DEPLOY_TOKEN" }],
  plan(plan) {
    plan.job("deploy", async (ctx) => {
      const token = (await ctx.secret("DEPLOY_TOKEN")) ?? "";
      await ctx.bash("./deploy.sh", { env: { DEPLOY_TOKEN: token } }).run();
    });
  },
});
```

Set the value from a file or standard input, never on the command line:

```bash
# A team-wide secret, like an organization secret
sparkwing secrets set --secret DEPLOY_TOKEN --file token.txt --shared --profile cloud

# One repository's pipeline, like a repository secret
sparkwing secrets set --secret DEPLOY_TOKEN --file token.txt --repo acme/app --pipeline release --profile cloud
```

Repository secrets reach only runs whose code Sparkwing fetched from that repository. Runs from your machine read only
`--shared` secrets. Who can read which secret differs from GitHub; read [secrets](secrets.md) and the
[known limitations](known-limitations.md#no-deployment-environments) before you move deploy credentials.

## Artifacts

```yaml
build:
  steps:
    - run: make dist
    - uses: actions/upload-artifact@v4
      with: { name: dist, path: dist/ }
deploy:
  needs: build
  steps:
    - uses: actions/download-artifact@v4
      with: { name: dist }
```

An artifact is an edge between two jobs rather than a pair of steps. The producer declares the files it keeps, and the
consumer receives them under a directory you choose. `Consumes` also orders the jobs, so no separate `Needs` is
required.

```go
build := sw.Job(plan, "build", func(ctx context.Context) error {
    _, err := sw.Bash(ctx, "mkdir -p dist && echo app > dist/app.txt").Run()
    return err
}).Artifacts("dist/**")
sw.Job(plan, "deploy", func(ctx context.Context) error {
    _, err := sw.Bash(ctx, "cat staged/dist/app.txt").Run()
    return err
}).Consumes(build, sw.Into("staged"))
```

```ts
const build = plan.job("build", async (ctx) => {
  await ctx.bash("mkdir -p dist && echo app > dist/app.txt").run();
}).artifacts("dist/**");
plan.job("deploy", async (ctx) => {
  await ctx.bash("cat staged/dist/app.txt").run();
}).consumes(build, { into: "staged" });
```

On your machine, jobs share your working tree, so a consumer can see a producer's files even without `Consumes`.
Declare it anyway: on Cloud and shared runners it is the only way files move between jobs.

Download a file from a finished run with the CLI. The dashboard does not list artifacts yet.

```bash
sparkwing runs artifacts --run 42 --job build
sparkwing runs artifacts --run 42 --job build --path dist/app.txt --dest ./app.txt
```

Artifacts are kept for 30 days, with no per-artifact retention setting.

## Concurrency

```yaml
concurrency:
  group: production-deploy
  cancel-in-progress: false
```

A concurrency group caps how many jobs hold it at once. With `scope` set to global, the cap spans every run; a second
deploy waits in the queue until the first finishes.

```go
deploys := sw.NewConcurrencyGroup("production-deploy", sw.ConcurrencyLimit{Scope: sw.ScopeGlobal, Capacity: 1, OnLimit: sw.Queue})
sw.Job(plan, "deploy", func(ctx context.Context) error {
    _, err := sw.Bash(ctx, "echo deploying").Run()
    return err
}).Concurrency(deploys)
```

```ts
// import { ConcurrencyGroup } from "@sparkwing/sdk";
const deploys = new ConcurrencyGroup("production-deploy", { scope: "global", capacity: 1, onLimit: "queue" });
plan.job("deploy", async (ctx) => { await ctx.bash("echo deploying").run(); }).concurrency(deploys);
```

`cancel-in-progress: true`, which cancels the older run of a branch when a newer push arrives, has no equivalent yet.
See [known limitations](known-limitations.md#superseded-runs-are-not-cancelled).

## Schedule

```yaml
on:
  schedule:
    - cron: '0 3 * * *'
```

A schedule is declared under the pipeline in `.sparkwing/sparkwing.yaml`. Unlike GitHub Actions it takes a time zone,
an overlap policy and a catch-up window. `where: controller` fires on your team's controller, armed by the GitHub App
from the default branch. `where: local` fires on one machine after `sparkwing schedules install`.

```yaml
pipelines:
  - name: nightly
    entrypoint: Nightly
    on:
      schedule:
        - name: nightly
          cron: '0 3 * * *'
          where: controller
          tz: America/New_York
          overlap: skip
```

Pass credentials to a scheduled run only as secrets, never as schedule `args`. See [schedules](schedules.md).

## workflow_dispatch inputs

```yaml
on:
  workflow_dispatch:
    inputs:
      target:
        type: choice
        options: [staging, prod]
        default: staging
```

Inputs are typed fields. A value outside the allowed list is refused before any job starts, and
`sparkwing run deploy --help` lists them.

```go
type DeployInputs struct {
    Target string `flag:"target" default:"staging" enum:"staging,prod" desc:"Environment to deploy"`
}

type Deploy struct{ sw.Base }

func (Deploy) Plan(ctx context.Context, plan *sw.Plan, in DeployInputs, rc sw.RunContext) error {
    sw.Job(plan, "deploy", func(ctx context.Context) error {
        _, err := sw.Bash(ctx, "echo deploying to "+in.Target).Run()
        return err
    })
    return nil
}

func init() {
    sw.RegisterEntrypoint[DeployInputs]("Deploy", func() sw.Pipeline[DeployInputs] { return &Deploy{} })
}
```

```ts
import { definePipeline } from "@sparkwing/sdk";

definePipeline({
  name: "Deploy",
  args: [{ name: "target", type: "string", default: "staging", enum: ["staging", "prod"], description: "Environment to deploy" }],
  plan(plan) {
    plan.job("deploy", async (ctx) => {
      await ctx.bash(`echo deploying to ${ctx.args["target"]}`).run();
    });
  },
});
```

Pipeline arguments go after `--`:

```bash
sparkwing run deploy -- --target prod
sparkwing pipeline trigger deploy --profile cloud -- --target prod
```

The first runs on your machine; the second runs on your team's runners or Cloud. The dashboard's **Start a run** form
takes the same arguments as free text, one `name=value` per line, and does not show the allowed values.

## Deploy on main

```yaml
on:
  push:
    branches: [main]
jobs:
  deploy:
    environment: production
```

List the branch under `on.push.branches`, and turn on repository automation for the repository in **Team → GitHub**.
Sparkwing reads this file from the default branch, so a feature branch cannot change which pipelines run. One
entrypoint can serve several pipeline names, so the guarded `deploy-prod` below reuses the `Deploy` code from the
previous section.

```yaml
pipelines:
  - name: deploy-prod
    entrypoint: Deploy
    args:
      target: prod
    on:
      push:
        branches: [main]
    guards:
      when: [where:controller, git:branch=main]
```

The guards refuse a run before any job starts: `where:controller` refuses one started on a developer's machine, and
`git:branch=main` refuses any other branch. Deploy by hand by naming the branch:

```bash
sparkwing pipeline trigger deploy-prod --ref main --profile cloud
```

There are no deployment environments, so there is no reviewer gate on a secret. An approval step pauses a run, but
it is a workflow step and not a security control. Keep production credentials behind a reserved pool; see
[known limitations](known-limitations.md#no-deployment-environments).

## Status checks

Each run the GitHub App starts reports one check run named `sparkwing/<pipeline>` on the commit it builds. Require that
name in your branch protection rules instead of the per-job names GitHub Actions reported. The check moves from queued
to in progress to completed, and its **Details** link opens the run in the dashboard. GitHub's **Re-run** buttons start
the pipeline again. See [check runs](github-app.md#check-runs).

Runs started from the CLI, the API or a schedule post no check. Per-job checks and annotations on the pull request are
not sent yet; see [known limitations](known-limitations.md#one-check-run-per-pipeline).

## Notifications

GitHub Actions emails you when your workflow fails. Sparkwing sends nothing by default. Subscribe a URL to run events
instead: Sparkwing posts a signed [CloudEvents](https://cloudevents.io) message when a run starts or finishes.

```bash
sparkwing subscriptions create --url https://hooks.example.com/sparkwing --status failed --profile cloud
```

The command prints a signing secret once. [Run events](run-events.md) describes the message and includes a short
receiver that verifies it and posts to a Slack incoming webhook.
