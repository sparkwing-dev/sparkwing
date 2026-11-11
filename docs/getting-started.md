# Getting Started

## Two paths

**Local** is the product. Sparkwing is a program on your machine: it
compiles `.sparkwing/` and runs each job as a host subprocess, keeps state
in SQLite under `~/.sparkwing/`, and serves its own dashboard. No account
and no cloud. After one successful run, a pipeline whose sparks are
pinned to exact tags runs with the network unplugged; see
[offline after the first build](#offline-after-the-first-build) for what
still needs it.

**Sparkwing Cloud** is the hosted controller. A controller gives a team
one dashboard, one run history, and one queue: a triggered run waits there
until a runner claims it, and fails with `queue_timeout` at the queue
deadline when none does (see [scheduling.md](scheduling.md)). The
Cloud profile selects the hosted controller:

```bash
sparkwing profiles add --controller https://api.sparkwing.example --token-stdin
```

Start local. Read [Install](#install) and [Quick start](#quick-start),
then [Sparkwing Cloud](#sparkwing-cloud) when you want a team to see the
same runs. [Self-hosted runners](self-hosting.md#add-workstation-capacity)
adds capacity from your own machines to Cloud.

## Install

Building Sparkwing requires Go 1.26.0 or later. Pipeline modules that use only
the Go SDK require Go 1.25.0 or later. Job packages can require a newer version.
The engine's source module selects Go 1.26.11 for patched builds with automatic
toolchain selection. SDK consumers choose their own Go patch release.

### macOS / Linux

```bash
curl -fsSL https://sparkwing.dev/install.sh | sh
```

It detects your OS and architecture, pulls the matching release binary,
and drops `sparkwing` into `~/.local/bin`. For a specific version:
`curl -fsSL https://sparkwing.dev/install.sh | sh -s -- --version vX.Y.Z`.

A machine that runs work for a controller, through `sparkwing runners add`,
also needs `sparkwing-runner`. `--runner` installs it beside `sparkwing` from
the same release after the same checks:
`curl -fsSL https://sparkwing.dev/install.sh | sh -s -- --runner`.
`sparkwing update` updates only the CLI. An installed runner updates itself
from verified release assets when the signed version index names a newer
release.

To place the binary yourself, download the asset for your platform from
GitHub Releases:

```bash
# macOS Apple Silicon
curl -fsSL https://github.com/sparkwing-dev/sparkwing/releases/latest/download/sparkwing-darwin-arm64 \
    -o ~/.local/bin/sparkwing && chmod +x ~/.local/bin/sparkwing

# macOS Intel
curl -fsSL https://github.com/sparkwing-dev/sparkwing/releases/latest/download/sparkwing-darwin-amd64 \
    -o ~/.local/bin/sparkwing && chmod +x ~/.local/bin/sparkwing

# Linux x86_64
curl -fsSL https://github.com/sparkwing-dev/sparkwing/releases/latest/download/sparkwing-linux-amd64 \
    -o ~/.local/bin/sparkwing && chmod +x ~/.local/bin/sparkwing

# Linux ARM64
curl -fsSL https://github.com/sparkwing-dev/sparkwing/releases/latest/download/sparkwing-linux-arm64 \
    -o ~/.local/bin/sparkwing && chmod +x ~/.local/bin/sparkwing
```

Keep to one location; a second copy on PATH shadows the first (see
[If the wrong sparkwing runs](#if-the-wrong-sparkwing-runs)).

The install script checks what it downloads. It verifies the ed25519
signature over `SHA256SUMS`, then the asset's digest against its line in
that file, then the asset's own signature, all against a public key
built into the script, and installs nothing that fails. It needs OpenSSL
3.0 or newer; on macOS run `brew install openssl@3`, because the system
`openssl` is LibreSSL and cannot verify an ed25519 signature.

If GitHub's release download route returns a server error or times out, the
installer, updater, engine resolver and GitHub Actions fallback runner install
fetch the same tag and asset through GitHub's REST asset API. Signature and
checksum checks still apply. Anonymous API requests are subject to GitHub's
rate limits.

A hand-placed download skips every one of those checks. `SHA256SUMS`
reaches you from the same origin as the binary, so comparing the two
proves the transfer finished and nothing else. The check that carries a
guarantee is `SHA256SUMS.sig`, published beside it with one `.sig` per
asset and signed with the key `sparkwing update` carries.

Each release publishes the install script itself as `install.sh` with an
`install.sh.sig` under the same tag, so a copy taken from anywhere can be
checked before it runs. The trusted keys are
`internal/releaseauth.TrustedPublicKeys` in the sparkwing repository.

Or, if Go is on PATH, build from source:

```bash
go install github.com/sparkwing-dev/sparkwing/cmd/sparkwing@latest
```

This installs the `sparkwing` binary, which is the single CLI for
both admin / inspection (`sparkwing dashboard open`,
`sparkwing pipeline list`) and pipeline invocation
(`sparkwing run <pipeline>`).

> **Note:** `go install` does not include the Next.js dashboard
> bundle, which is a generated artifact and not checked into the
> repository. A source-built binary will refuse
> `sparkwing dashboard open` with a clear message pointing back to the
> release binary. CLI-only commands (`run`, `pipeline`, `runs`, etc.)
> work fine. If you want the dashboard from a source checkout, run
> `bash bin/build-web.sh` first to generate the bundle, then
> `go install ./cmd/sparkwing` from the repo root.

### Windows

Prebuilt Windows binaries are published to GitHub Releases:
`sparkwing-windows-amd64.exe` and `sparkwing-windows-arm64.exe`. They
embed the dashboard bundle, so `sparkwing dashboard open` works locally
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
`sparkwing dashboard open` on a source-built CLI refuses unless
you run `bash bin/build-web.sh` from a repo checkout first. The prebuilt
release binary has no such limitation.

The bundled `sparkwing-runner` service installer supports Linux and macOS.
On Windows, supervise `sparkwing-runner.exe agent` yourself or use the Linux
installer inside WSL when systemd user services are enabled.

### If the wrong sparkwing runs

Your shell runs the first `sparkwing` on PATH, and a job or service with a
different PATH can pick another copy. List every copy and check the one that
runs:

```bash
which -a sparkwing
sparkwing version
```

Remove the installation you do not want, or call the one you do by its
absolute path in scripts and services.

## Quick Start

Install [Go](https://go.dev/doc/install) and Git on PATH. The Go scaffold runs
`go mod tidy`, and the first run downloads the SDK before compiling.

```bash
# 1. Scaffold in your Git repository
sparkwing pipeline new --pipeline hello

# 2. Review and commit the generated files
sparkwing pipeline plan --pipeline hello
git add .sparkwing
git commit -m "Add local CI pipeline"

# 3. Run it, then repeat to reuse the compiled pipeline cache
sparkwing run hello
sparkwing run hello

# 4. Print a one-time sign-in link and visit it in your browser
sparkwing dashboard open
```

In a new Git repository, preview the scaffold before your first commit with
`sparkwing pipeline plan --pipeline hello`. Running requires an initial
commit so intake can record a commit identity. Review and commit your files,
or run `git commit --allow-empty -m 'Initial commit'`, then retry
`sparkwing run hello`.

The scaffold adds `sparkwing-pipeline` to `.sparkwing/.gitignore` so Git
ignores an untracked generated binary. Git does not untrack a binary already
in its index; remove that with `git rm --cached .sparkwing/sparkwing-pipeline`.
The scaffold preserves other entries in the local ignore file and leaves the
repository's root `.gitignore` untouched. If `.sparkwing/.gitignore` points
outside `.sparkwing/`, it prints a note asking you to add the binary to an
ignore file.

For a build/test/deploy DAG instead of a single node, pass
`--template build-test-deploy`:

```bash
sparkwing pipeline new --pipeline release --template build-test-deploy
```

The five Go shapes are structure only -- they run green in any repo, and
you replace the echo bodies with real work. For how a real pipeline is
written, read a finished one: `sparkwing examples` lists the registry
(static-site deploys, containerized deploys to Kubernetes,
migrate+deploy, CI-hygiene gates, and more), and `--example <example>
--body` prints the source. Usually `sparkwing docs search --search "<task>"`
gets you there in one step, since it ranks examples alongside the docs.

For a TypeScript project, scaffold a minimal pipeline with:

```bash
sparkwing pipeline new --pipeline hello --template typescript
npm --prefix .sparkwing install --package-lock-only --ignore-scripts --no-audit --no-fund
git add .sparkwing
git commit -m "Add TypeScript pipeline"
sparkwing pipeline list
sparkwing pipeline describe --pipeline hello
sparkwing pipeline plan --pipeline hello
sparkwing run hello
```

This creates `.sparkwing/package.json` with an exact `@sparkwing/sdk` pin,
`index.ts`, `jobs/hello.ts` and `sparkwing.yaml`. It uses the same `--on`,
`--hidden` and `--short` flags as the Go shapes. Repeat the command with another
pipeline name to add a pipeline to the TypeScript entry. Sparkwing installs
the SDK dependencies when the pipeline runs. Commit `.sparkwing/package-lock.json` because
the installer validates dependencies with `npm ci`. Node.js 22.18 or later and npm must be on
PATH. `pipeline plan` explains the resolved jobs and steps without running them.
`pipeline lint` analyzes Go source only. Its successful exit does not validate
TypeScript source. See [TypeScript authoring](authoring-pipelines.md#typescript-authoring)
for jobs, steps, outputs, runtime conditions and other authoring topics.

Examples are for reading, not scaffolding: `--template` takes a shape,
not an example name. See the
[template catalog](sparks.md#the-template-catalog) for the full picture.

If you want to own and edit a spark library's helper code directly rather
than importing it, `sparkwing pipeline sparks inflate --library <name>`
copies its source into your repo. See
[inflating a spark library](sparks.md#inflating-a-spark-library).

`sparkwing run` compiles `.sparkwing/` when its sources or dependencies change
and reuses the cached binary otherwise. The local runner executes jobs on this
machine. Run state lives under `~/.sparkwing/` (SQLite + log files).
The local controller starts on demand and serves the dashboard for its whole
lifetime. `sparkwing dashboard open` prints a sign-in link. On Linux and macOS,
`sparkwing daemon install` keeps the controller running as a login service.
Use `sparkwing daemon status` to inspect it and `sparkwing daemon stop` to stop
it. See [Local dashboard](native-mode.md) for service requirements.
Daemon records include `url` when known. Use `--output plain` for a single
`running` or `stopped` value, or `--output pretty` for the terminal layout.
A stopped `daemon status` exits 1. On Linux with systemd, `daemon stop` also
waits for this home’s supervised runner units to disappear, including runners
still starting. It exits nonzero with incomplete shutdown if they remain at
the stop bound or their state cannot be read. Stopping an absent controller
exits 0 only after this check passes.

If you want a local Kubernetes cluster as a deploy target for user apps
(not for sparkwing itself), bring your own - any local Kubernetes setup
works. Local CI uses the controller on your machine.

### Inspect a failed run

The run's final output includes its ID and commands to inspect it:

```bash
sparkwing runs list --profile=
sparkwing runs status --profile= --run RUN_ID
sparkwing runs logs --profile= --run RUN_ID
```

Fix the failing job and run the pipeline again. If a run stays pending, inspect
`sparkwing queue list --profile=` and the controller log at
`~/.sparkwing/wingd/d.log`. On a systemd Linux host,
local jobs need a working user manager. Check `systemctl --user status` from
your login session. See [local runner supervision](local-execution.md).

### Schedule local work

Scaffold a daily report, review and commit it, then arm its schedule:

```bash
sparkwing pipeline new --pipeline report --template scheduled-report
git add .sparkwing
git commit -m "Add daily report"
sparkwing run report
sparkwing schedules install --profile= --schedule report
sparkwing schedules list --profile=
sparkwing schedules list --profile= --timer
```

The template declares 09:00 UTC daily in `.sparkwing/sparkwing.yaml`. Edit that
cadence before installing. A declaration alone does not arm a schedule.
Installation proves and pins the pipeline, captures its environment, and installs
a systemd user timer on Linux or a launchd agent on macOS. The timer starts the
local controller on demand through `__tick`. Windows installation records
schedules but does not fire them automatically. Use `sparkwing schedules run
--profile= --schedule report` to start one explicitly. The machine must be awake
for timer-started work. Reinstall after
changing the pipeline to update its pin, or choose `--unpinned` to compile the
checkout on every fire.

`sparkwing schedules run --profile= --schedule report` starts an immediate run
without changing the cadence. `sparkwing schedules uninstall --profile= --schedule report` removes it. `--no-timer` registers the declaration without
arming a native timer. See [Schedules](schedules.md) for timezone, catch-up and
pause controls.

### Storage class

The controller's PersistentVolumeClaim and the `storageClassName` a cluster
without a default StorageClass needs are covered in
[Self-hosting](self-hosting.md#storage-class).

## Offline after the first build

After one successful `sparkwing run` in a checkout, a pipeline whose
sparks are pinned to exact tags runs with the network unplugged. The Go
modules sit in the module cache, the compiled pipeline binary sits in
`~/.sparkwing/cache/pipelines/`, and the run's state, logs, dashboard,
and admission daemon are files and sockets on your own machine.

The first run is the one that reaches out. It downloads the SDK and every
spark library your `.sparkwing/go.mod` requires, then compiles. A run
whose sources and pins have not changed reuses the cached binary and
compiles nothing.

What still wants the network is visible in advance:

- **A `latest` or range pin.** A `sparks:` entry pinned to `latest`, `^v0.24.0`,
  or `~v0.10.3` asks the module proxy for the newest matching tag on every
  run. Pass `--sw-no-update` to skip resolution and compile against the
  overlay already on disk, or pin exact tags and pay nothing. Without the
  flag an unreachable proxy fails the run and the error names it. The
  resolution rules are in [sparks.md](sparks.md).
- **A profile.** `sparkwing pipeline trigger <pipeline> --profile NAME`
  and read verbs that name a controller profile need that controller over
  HTTPS. `sparkwing run` reads and writes only local surfaces.
- **What the pipeline itself does.** A step that pulls an image, fetches a
  ref, or deploys needs whatever that step needs. Sparkwing does not
  change it.
- **Updating sparkwing.** `sparkwing update` and the install script fetch
  a release.

## What `sparkwing pipeline new` Creates

```
.sparkwing/
  .gitignore        # ignores the compiled sparkwing-pipeline binary
  sparkwing.yaml    # registry of every pipeline this repo defines
  main.go           # thin entrypoint; blank-imports jobs/ and calls sparkwing.Main
  README.md         # generated package README
  go.mod            # Go module for pipeline code
  go.sum            # dependency checksums (from go mod tidy)
  jobs/             # pipeline implementations
```

## The Model

A **pipeline** is anything `sparkwing` (or `sparkwing run`) can invoke. Two
shapes share the same surface:

- **triggered pipeline** - a YAML entry with an `on:` trigger. Push and webhook triggers need a connected controller. On Linux/macOS, local schedules fire after `sparkwing schedules install --profile=`. Implemented as a Go type whose factory is registered via `sparkwing.Register`.
- **manual pipeline** - a YAML entry with no `on:` trigger. Runs only when explicitly invoked. Same Go registration; it's "triggered" vs "manual" distinguishes auto-firing from operator-initiated.

Both produce a Run in the local store on each invocation. The dashboard's
runs list and `sparkwing runs list` surface them uniformly. (One-shot
repo-local shell chores -- formatters, port-forwards, the small
Makefile-style stuff -- do not need a run record; keep them in your
existing task runner.)

A Go pipeline is a struct that implements
`Plan(ctx context.Context, plan *sw.Plan, in Inputs, run sw.RunContext) error`.
It registers jobs on the passed-in `plan` and returns; it does not build
and return one. A one-node pipeline registers a single `sw.Job`. See
[`sdk.md`](sdk.md) for the SDK reference and
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

// At the bottom of .sparkwing/jobs/build_deploy.go:
//     func init() {
//         sw.Register[sw.NoInputs]("BuildDeploy", func() sw.Pipeline[sw.NoInputs] { return &BuildDeploy{} })
//     }
```

Sparkwing Cloud includes the Docker CLI, buildx, and Compose on amd64 and arm64.
`docker build`, `docker run`, and `docker compose` use the job's Docker sidecar
through the supplied `DOCKER_HOST`. Job commands run as root inside a user
namespace, and `/tmp` is writable by package-manager users, so `apt-get install`
works. The pod and sidecar remain unprivileged. On a self-hosted runner, install
the Docker client and plugins and configure access to a Docker daemon.

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

## Run Targets

`sparkwing run` executes on this machine and records to its local store,
logs, and cache. `sparkwing pipeline trigger <pipeline> --profile
NAME` hands execution to that profile's controller. `sparkwing run` also takes
`--sw-ref <branch|tag|sha>` to compile a git ref instead of the working
tree (trigger runs the source registered with the controller, or the
checkout's uncommitted changes with `--working-tree`):

```bash
sparkwing run build                                    # run locally with local code
sparkwing run build --sw-ref main                      # build the main ref locally
sparkwing pipeline trigger build --profile dev         # run on the "dev" cluster
sparkwing pipeline trigger build --profile dev --working-tree  # uncommitted changes on "dev"
sparkwing pipeline trigger build --profile prod        # run on the "prod" cluster
```

Cluster names are profiles. `sparkwing profiles add` writes one; see
[Sparkwing Cloud](#sparkwing-cloud). Sparkwing itself
does not run in-cluster locally - clusters named by `--profile` are
user-managed deploy targets, not local sparkwing deployments.

## Sparkwing Cloud

Sparkwing Cloud is the hosted controller. A controller owns the shared
dashboard, run history, scheduling, webhooks, and tokens, and machines
reach it over outbound HTTPS. Self-hosted runners register to Cloud and
supply capacity without operating a shared controller.

```bash
sparkwing profiles add --controller https://api.sparkwing.example --token-stdin
```

Use an existing member credential for team commands. Cloud operators can create
a team and its first administrator without OAuth:

```bash
sparkwing operator teams create --team support-test --display-name Support --profile deployment -o json
```

The deployment profile requires `operator.tokens`. The first team credential is
returned once. See [Cloud operations](auth.md#cloud-operations-and-test-environments).

It reads the token from stdin, checks health, authenticates through
`/api/v1/auth/whoami`, and saves the profile in
`~/.config/sparkwing/config.yaml`. The result contains the controller URL and a
redacted token prefix. Use `sparkwing profiles show --profile NAME` to inspect
it and `sparkwing profiles set --profile NAME --token-stdin` to replace its token.

If you set `SPARKWING_HOME` to isolate runtime state, also set `SPARKWING_CONFIG`
to a private config path before `profiles add`. The runtime home alone does not
relocate the machine config.

The profile name comes from the controller host unless `--profile` names it.
An existing profile is replaced only with `--force`. Removing a profile with
`sparkwing profiles remove --profile NAME` revokes its token first.
`--keep-token` removes only the saved connection. Project configuration cannot
supply connection profiles or credentials.

Use `--profile NAME` to select a connection, or save one with
`sparkwing profiles set --profile prod --default`. Optional-profile commands
use that default when the flag is omitted. With no saved default they use the
local controller. `--profile=` selects local for one command.

`sparkwing run` keeps its separate machine routing preference. A saved default
profile does not change local execution. Use `--sw-route cloud --profile NAME`
to send one run directly to Cloud. Without `--profile`, that explicit route
uses machine `run.routing.profile`.

```bash
sparkwing profiles show --profile prod        # saved connection, with token redacted
sparkwing profiles remove --profile prod      # revoke and remove
```

Removal first verifies that the saved credential is a member credential and
confirms its revocation. A failed revocation leaves the connection saved.
`--keep-token` removes only the connection and leaves its credential live.

To run work on this machine for that controller, add it as a runner with
`sparkwing runners add --profile prod --runner this-laptop`.

### Start a run

On a controller with teams, a team's runners fetch each run's source from
GitHub themselves, so every run names a repository. A runner claims only
runs whose repository matches one of its `--repos` patterns, and a run
nothing may build stays pending. Start one from the dashboard or a terminal.

**From the dashboard.** On the Runs page, **+ Start a run** opens a form that
takes a pipeline name, a repository such as `https://github.com/acme/app`,
and a branch (`main` unless you change it). The runner that claims it fetches
the branch tip.

Run submission and **Run now** stay pending while the controller admits the
run, including repository access checks. They have no browser request deadline.
If the connection drops, retry the same form or action to reuse its idempotency
key and recover the admitted run without creating another. Keep the form open
and its inputs unchanged for that retry. A new form or changed inputs start a
new request.

Selecting a run opens its detail beside the compact runs list. Switching to
another run keeps both panes in place while its detail loads; clearing the
selection closes the detail.

When viewing a run, the chevron above the runs list collapses both the runs
and nodes columns into status-dot rails. Hover or focus a dot to see its full
label; select it to open that run or node. Screens narrower than 1100px start
with the rails collapsed. Your choice to expand or collapse them is saved in
this browser.

**From a terminal.** Under **Team > Machines > CLI access**, **Create CLI
token** mints a personal token and shows it once with two commands:

```bash
sparkwing profiles add --controller https://api.sparkwing.example --profile acme --token-stdin
sparkwing pipeline trigger <pipeline> --profile acme
```

The first prompts for the token without echoing it and saves a profile named
after the team. Run the second inside a checkout whose commit is pushed: the
run records the checkout's `origin` repository and commit, and a team runner
fetches that commit. GitHub identity lookup first uses the selected controller's
team App or source credentials. App-covered private repositories need no local
GitHub token. An explicit `GH_TOKEN` or `GITHUB_TOKEN` with repository read access
can supply identity when controller discovery is unavailable. The CLI never
queries GitHub anonymously. The controller credential needs
`runs.write`. Add `--working-tree` to capture tracked and unignored untracked
files into an immutable Git snapshot, upload it through shared storage, and seal
its digest into the run. The original checkout and index stay untouched.
Private GitHub repositories also require the controller's GitHub App or a
configured team HTTPS source credential; the CLI's environment token stays local.

Runs started by a push come with the Sparkwing GitHub App.

## Advanced deployments

The launch offering is Local and Cloud. Self-hosted runners register to Cloud.
Operating a shared controller is deferred to a future licensed offering.
The [deployment modes](deployment-modes.md) page compares the two modes.
The controller chart stays available for Cloud operators.
