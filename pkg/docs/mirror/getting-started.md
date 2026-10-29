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

### macOS / Linux

```bash
curl -fsSL https://sparkwing.dev/install.sh | sh
```

It detects your OS and architecture, pulls the matching release binary,
and drops `sparkwing` into `~/.local/bin`. For a specific version:
`curl -fsSL https://sparkwing.dev/install.sh | sh -s -- --version vX.Y.Z`.

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

Keep to one location: `sparkwing doctor` reports competing copies on
PATH, and a second copy shadows the first.

The install script checks what it downloads. It verifies the ed25519
signature over `SHA256SUMS`, then the asset's digest against its line in
that file, then the asset's own signature, all against a public key
built into the script, and installs nothing that fails. It needs OpenSSL
3.0 or newer; on macOS run `brew install openssl@3`, because the system
`openssl` is LibreSSL and cannot verify an ed25519 signature.

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

The bundled `sparkwing-runner` service installer supports Linux and macOS.
On Windows, supervise `sparkwing-runner.exe agent` yourself or use the Linux
installer inside WSL when systemd user services are enabled.

## Quick Start

```bash
# 1. Set up your repo
cd your-project
sparkwing pipeline new --pipeline release   # single-node minimal template by default

# 2. Run your first pipeline
sparkwing run release

# 3. (Optional) Watch runs in the browser
sparkwing dashboard start    # detached local dashboard + API on :4343
sparkwing dashboard open     # one-time sign-in link for the browser
```

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

The five shapes are structure only -- they run green in any repo, and
you replace the echo bodies with real work. For how a real pipeline is
written, read a finished one: `sparkwing examples` lists the registry
(static-site deploys, containerized deploys to Kubernetes,
migrate+deploy, CI-hygiene gates, and more), and `--example <example>
--body` prints the source. Usually `sparkwing docs search --search "<task>"`
gets you there in one step, since it ranks examples alongside the docs.

Examples are for reading, not scaffolding: `--template` takes a shape,
not an example name. See the
[template catalog](sparks.md#the-template-catalog) for the full picture.

If you want to own and edit a spark library's helper code directly rather
than importing it, `sparkwing pipeline sparks inflate --library <name>`
copies its source into your repo. See
[inflating a spark library](sparks.md#inflating-a-spark-library).

Each `sparkwing` invocation compiles `.sparkwing/` and runs the pipeline as a host
subprocess. Run state lives under `~/.sparkwing/` (SQLite + log files).
The local controller starts on demand and the dashboard commands manage its
lifecycle. See [Local dashboard](native-mode.md) for service installation.
Dashboard records include `url` when known. Use `--output plain` for a single
`running` or `stopped` value, or `--output pretty` for the terminal layout.
A stopped `status` still exits 1; stopping an absent server exits 0.

If you want a local Kubernetes cluster as a deploy target for user apps
(not for sparkwing itself), bring your own - any local Kubernetes setup
works. Sparkwing does not run in-cluster locally; the controller is a
prod-only component.

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
  sparkwing.yaml    # registry of every pipeline this repo defines
  main.go           # thin entrypoint; blank-imports jobs/ and delegates to runner.Main
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
//         sw.Register[sw.NoInputs]("build-deploy", func() sw.Pipeline[sw.NoInputs] { return &BuildDeploy{} })
//     }
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

Commands reach the connection only when they name it with `--profile`;
without the flag they act on this machine's store.

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
fetches that commit. Private GitHub identity lookup accepts `GH_TOKEN` or
`GITHUB_TOKEN` with repository read access. The controller credential needs
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
