# Getting Started

## Two paths

**Local** is the product. Sparkwing is a program on your machine: it
compiles `.sparkwing/` and runs each job as a host subprocess, keeps state
in SQLite under `~/.sparkwing/`, and serves its own dashboard. No account,
no cloud, and the machines you own join a run with `--sw-fleet`. After one
successful run, a pipeline whose sparks are pinned to exact tags runs with
the network unplugged; see
[offline after the first build](#offline-after-the-first-build) for what
still needs it.

**Sparkwing Cloud** is the hosted controller. A controller gives a team
one dashboard, one run history, and one queue: a triggered run waits there
until an enrolled machine claims it, and fails with `queue_timeout` at the
queue deadline when none does (see [scheduling.md](scheduling.md)). The
same command reaches any controller you can reach, including one your team
runs:

```bash
sparkwing cloud connect --controller https://api.sparkwing.example --token-stdin
```

Start local. Read [Install](#install) and [Quick start](#quick-start),
then [Sparkwing Cloud](#sparkwing-cloud) when you want a team to see the
same runs. [Advanced deployments](#advanced-deployments) covers hosting
the state, cache, or controller yourself.

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
both admin / inspection (`sparkwing serve start`,
`sparkwing pipeline list`) and pipeline invocation
(`sparkwing run <pipeline>`).

> **Note:** `go install` does not include the Next.js dashboard
> bundle, which is a generated artifact and not checked into the
> repository. A source-built binary will refuse to start
> `sparkwing serve` with a clear message pointing back to the
> release binary. CLI-only commands (`run`, `pipeline`, `runs`, etc.)
> work fine. If you want the dashboard from a source checkout, run
> `bash bin/build-web.sh` first to generate the bundle, then
> `go install ./cmd/sparkwing` from the repo root.

### Windows

Prebuilt Windows binaries are published to GitHub Releases:
`sparkwing-windows-amd64.exe` and `sparkwing-windows-arm64.exe`. They
embed the dashboard bundle, so `sparkwing serve start` runs locally
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
`sparkwing serve start` on a source-built CLI refuses to start unless
you run `bash bin/build-web.sh` from a repo checkout first. The prebuilt
release binary has no such limitation.

The bundled `sparkwing-runner` service installer supports Linux and macOS.
On Windows, supervise `sparkwing-runner.exe agent` yourself or use the Linux
installer inside WSL when systemd user services are enabled.

## Quick Start

```bash
# 1. Set up your repo
cd your-project
sparkwing pipeline new --name release   # single-node minimal template by default

# 2. Run your first pipeline
sparkwing run release

# 3. (Optional) Watch runs in the browser
sparkwing serve start    # detached local dashboard + API on :4343
```

For a build/test/deploy DAG instead of a single node, pass
`--template build-test-deploy`:

```bash
sparkwing pipeline new --name release --template build-test-deploy
```

The five shapes are structure only -- they run green in any repo, and
you replace the echo bodies with real work. For how a real pipeline is
written, read a finished one: `sparkwing examples` lists the registry
(static-site deploys, containerized deploys to Kubernetes,
migrate+deploy, CI-hygiene gates, and more), and `--name <example>
--body` prints the source. Usually `sparkwing docs search -q "<task>"`
gets you there in one step, since it ranks examples alongside the docs.

Examples are for reading, not scaffolding: `--template` takes a shape,
not an example name. See the
[template catalog](sparks.md#the-template-catalog) for the full picture.

If you want to own and edit a spark library's helper code directly rather
than importing it, `sparkwing pipeline sparks inflate --module <name>`
copies its source into your repo. See
[inflating a spark library](sparks.md#inflating-a-spark-library).

Each `sparkwing` invocation compiles `.sparkwing/` and runs the pipeline as a host
subprocess. Run state lives under `~/.sparkwing/` (SQLite + log files).
`sparkwing serve start` spawns a detached local web server (`pkg/localws`,
embedded in the CLI) against the same SQLite store, exposing the dashboard
plus the JSON / logs APIs on one port - useful when several runs are going
in parallel and the terminal gets crowded. `sparkwing serve status` /
`kill` manage its lifecycle. These commands print a compact status record when
piped, including `service`, `state`, `pid` when known, `home`, and `log`.
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
- **A profile with a `controller:` block.** Sparkwing Cloud and any other
  hosted controller own state, cache, and secrets over HTTPS, so a run
  under that profile needs the controller. `--sw-local-only` pins one run
  back to the local surfaces.
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

Cluster names are profiles. `sparkwing cloud connect` writes one; see
[Sparkwing Cloud](#sparkwing-cloud). Sparkwing itself
does not run in-cluster locally - clusters named by `--profile` are
user-managed deploy targets, not local sparkwing deployments.

## Sparkwing Cloud

Sparkwing Cloud is the hosted controller. A controller owns the shared
dashboard, run history, scheduling, webhooks, and tokens, and machines
reach it over outbound HTTPS. The same command reaches any controller you
can reach, Sparkwing Cloud and one your team runs alike.

```bash
sparkwing cloud connect --controller https://api.sparkwing.example --token-stdin
```

It reads the token you were given on stdin, writes the profile into
`~/.config/sparkwing/profiles.yaml`, and prints the dashboard URL the
controller announces along with the same probes `sparkwing configure profiles
test` runs. Nothing here asks you to edit YAML.

An administrator of a controller mints those tokens with
`--admin-token-stdin` instead, which reads an admin credential and issues a
user token carrying `runs.read`, `runs.write`, `triggers.read`, `logs.read`
and `approvals.write`. The admin token is never stored. See
[Self-hosting](self-hosting.md) for running the controller that issues them.

The profile is named after the controller host (`api-sparkwing-example`
above) unless you pass `--name`. An existing profile of that
name is replaced only with `--force`, because the token it holds stays live
until it is revoked.

Add `--set-default` inside a repository to write `defaults.profile` into its
`.sparkwing/sparkwing.yaml`, so runs in that checkout select the connection
with no flag. The name resolves against the project's own `profiles:` block
first and `profiles.yaml` second, so the token stays out of the checkout.

```bash
sparkwing cloud status --profile prod        # principal, scopes, and probes
sparkwing cloud disconnect --name prod --admin-token-stdin   # revoke and remove
```

Disconnect revokes the profile's token. The profile's own token revokes only
when it carries `admin`, so `--admin-token-stdin` supplies one that does; a
revoke the credential is not allowed to make leaves the token live and names
the prefix and the command that finishes the job. `--keep-token` removes the
profile and touches no credential.

To run work on this machine for that controller, enroll it as a runner with
`sparkwing cluster runners add --profile prod --name this-laptop`.

## Advanced deployments

Everything below is optional. A team on the two paths above never has to
read it; reach for one of these when you host the state, cache, or
controller yourself. Each is one profile away, and pipeline code does not
change between them.

**Shared object storage.** Runners write run state, cache blobs, and logs
to one bucket, and coordinate over object-store compare-and-swap with no
database and no controller. See
[shared object storage](deployment-modes.md#shared-object-storage-mode-2).

**Postgres and object storage.** Run state moves to a shared Postgres so
cache reservation, triggers, approvals, and debug pauses rest on a row
lock rather than the bucket's CAS support. Every runner then holds a
database credential. See
[Postgres and object storage](deployment-modes.md#postgres-and-object-storage-mode-3).

**Self-hosted controller.** The `sparkwing-full` Helm chart deploys the
controller, dashboard, cache, logs service, and Kubernetes runner into a
cluster you operate. See [Self-hosting](self-hosting.md) and
[hosted controller](deployment-modes.md#hosted-controller-mode-4).

**One of your own machines as the controller.** A single-instance
controller on one box, backed by SQLite and local disk, lets a laptop
point its profile at a desktop you own. See
[one of your own machines as the controller](deployment-modes.md#one-of-your-own-machines-as-the-controller).

**Profile surface YAML.** The `state` / `cache` / `logs` triple every
advanced shape selects is documented in
[Storage backends](backends.md), and the full field list in
[config-reference.md](config-reference.md).

## Releasing sparkwing

A release is a tag push. The `release` pipeline cuts the tag from the commit
in your working tree, and `.github/workflows/release.yaml` does everything
else. From the sparkwing checkout:

```bash
# preview: resolve the version and the changelog rewrite, stop before tag+push
SPARKWING_HOME="$(mktemp -d)" sparkwing run release --sw-dry-run

# real release -- push-tag is risk-gated, so --sw-allow is required:
SPARKWING_HOME="$(mktemp -d)" sparkwing run release --bump patch --sw-allow destructive,prod
```

The recipe, in five steps:

1. Resolve the version, from `--version` or by bumping the newest tag origin
   carries, and refuse anything that does not outrank it
   (`discover-version`, `validate-version`).
2. Rename the CHANGELOG.md `## [Unreleased]` section to `## [vX.Y.Z] - DATE`
   and open a fresh empty one above it (`prepare-changelog`).
3. Commit that rewrite, from a clean tree (`check-clean-tree`).
4. Check the section accounts for any runs-store schema or wire-format change
   since the previous tag (`gate-schema-changelog`, `gate-wire-changelog`).
5. Create the annotated `vX.Y.Z` tag and push the branch and the tag
   (`push-tag`).

That is the whole plan: seven nodes, all of them cheap reads of files and git.
No suite runs locally, and the pipeline never asks where origin's branch tip
is, so a release can be cut from any commit as long as its version is ahead of
the previous one.

The tag push is what starts the release. Hosted CI validates the tag first: it
re-checks that the version outranks the newest published one with
`bin/check-release-tag-order.sh`, then runs `sparkwing run release-verify
--version vX.Y.Z` over the tagged source, which repeats the changelog-section,
schema and wire checks so a tag pushed by hand is judged the same way a cut one
is. Only once that passes does it run the full gate, the pre-release tier, the
security scanners, the Postgres conformance suite and the browser suites
against the tagged source, builds the binaries for every platform and the five
container images, publishes them, and creates the GitHub release from that
tag's changelog section. A red check fails the run and publishes nothing: the
tag keeps a failed run, no half release exists, and the fix is a later patch
tag from a later commit. Tags are immutable, so a burnt version is never
re-cut.

The explicit temporary `SPARKWING_HOME` isolates prerelease state from the
operational runs store. The release runner refuses the default home so a build
with a newer embedded schema cannot migrate state used by installed readers.

The `push-tag` step declares `destructive` and `prod` risk labels, so an actual
tag+push requires `--sw-allow destructive,prod`; `--sw-dry-run` stops before
tagging and needs no allowance.

`## [Unreleased]` must hold at least one entry, and splitting entries by hand
across both `[Unreleased]` and `[vX.Y.Z]` is ambiguous, so the changelog step
refuses it.
