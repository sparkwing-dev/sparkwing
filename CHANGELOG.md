# Changelog

All notable changes to **sparkwing** are documented here. Format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html). The release
pipeline refuses to ship a new version without a matching entry below.

## How to read this

Each entry leads with a bold scope (`**sdk:**`, `**cli:**`, `**controller:**`,
`**cache:**`, `**config:**`, `**release:**`, `**docs:**`, ...) so you can
scan for the surface that affects you. Breaking changes get an inline
`(Breaking)` marker after the scope and a link to a section in that
release's [migration guide](docs/migrations/) -- click through for
before/after code, ordering guidance, and gotchas the inline summary can't
fit.

What belongs here:

- User-facing behavior. New features, surfaces, defaults, removals, fixes
  that an adopter would notice.
- Breaking changes. Every break in an exported `pkg/` or `sparkwing/` API,
  CLI flag, wire protocol, or YAML config field. Tagged `(Breaking)` inline.
- Migration steps for breaking changes, linked to the per-release guide.

What does **not** belong here:

- Internal refactors invisible to adopters. Renames inside `internal/`,
  test reshuffles, snapshot regenerations.
- Per-commit narrative. The release page is the narrative; commits are
  the audit trail. The pre-release manicuring agent (see
  [docs/changelog-style.md](docs/changelog-style.md)) consolidates related
  commits into one user-facing entry.
- Internal-only design docs and dev-only tooling unless adopters
  meaningfully see the result.

## Pre-1.0 caveat

sparkwing is on the `v0.x` track. Per [VERSIONING.md](VERSIONING.md),
breaking changes are permitted in minor bumps until v1.0.0. We do **hard
cuts**: removed symbols are gone, not aliased, and there is no deprecation
runway. Each minor release that breaks something ships a migration guide
so the cut is documented even though it isn't softened. Releases at
`v1.0.0+` are blocked at the release pipeline and require a deliberate
code change to unlock.

---

## [Unreleased]

### Fixed

- **docs:** `_sidebar.json` now excludes `proposals/` and `migrations/`
  alongside the existing `design/` exclusion. Downstream sites that
  walk a release tag's docs (e.g. sparkwing.dev) failed prerendering
  when a new proposal landed without being categorized; both
  directories carry per-document content that doesn't belong in the
  user-docs sidebar, so they're flat-excluded instead.

## [v0.7.0] - 2026-05-31
### Changed

- **box-slot semaphore is now opt-in.** Default `SPARKWING_BOX_SLOTS`
  changed from `max(1, NumCPU/workersPerRun)` (resolving to 1) to
  `0` (disabled). Most pipelines aren't CPU-pegged -- they're I/O on
  Docker pulls, network, registry pushes -- so the conservative
  default surprised users with "waiting for box slot (1 active, max
  1)" whenever any other sparkwing process was running. Users on
  small boxes who launch concurrent CPU-saturating pipelines can
  re-enable explicitly: `export SPARKWING_BOX_SLOTS=2` (or any N).
  The primitive remains the right answer for explicit host
  throttling -- it's just no longer always-on.

## [v0.6.3] - 2026-05-31
### Fixed

- **`sparkwing pipeline new` scaffold now produces a working project
  out of the box.** Three bugs converged to break the first-run
  experience: (a) the scaffold wrote `.sparkwing/pipelines.yaml`
  while every other CLI command reads `.sparkwing/sparkwing.yaml`,
  so `pipeline list`, `pipeline describe`, and `pipeline hooks
  install` all reported "no .sparkwing/sparkwing.yaml found"; (b)
  the generated `go.mod` pinned a non-existent fallback SDK version,
  so `go mod tidy` failed and the compile cycle never recovered;
  (c) the generated `jobs/*.go` mixed `sw.` and `sparkwing.` aliases
  in the same file, so the file didn't compile. All three are fixed
  and a fresh `sparkwing pipeline new --name X` → `git commit` (with
  a pre_commit trigger and `sparkwing pipeline hooks install`)
  now scaffolds + builds + dispatches end-to-end.

### Removed

- **`cmd/sparkwing-local-ws/`** is gone. Its job (long-lived local
  dashboard server) is fully owned by `sparkwing dashboard start`,
  which spawns a detached supervisor under the same `pkg/localws`
  code path. The dev scripts (`bin/dev-start.sh` /
  `bin/dev-stop.sh` / `bin/dev-restart.sh`) now drive the supervisor
  via `sparkwing dashboard {start,kill}` instead of forking the
  retired binary directly.

### Added

- **`pre-push` now runs a repo-wide gofmt check.** The existing
  golangci-lint step runs in `.sparkwing/` only, so a struct-alignment
  fix at the top of the tree slipped past pre-push and got caught
  later by `sparkwing run lint`. Both gates now reject the same
  unformatted file.
- **Dashboard nav now shows the CLI version pill.** A small monospace
  pill renders next to the "sparkwing" logo (e.g. `v0.6.2`), reading
  the value the serving binary injects via the SPA template. Operators
  can see what build they're connected to without opening dev tools.
  Source builds without an `-ldflags` version stamp fall back to the
  Go build-info pseudo-version so the pill is still informative.

### Fixed

- **Postgres state from a laptop + `RunAndAwait` now works
  end-to-end.** The parent's local trigger dispatcher forwards its
  active profile (`--profile <name>`) to the child `handle-trigger
  --local`, which resolves the same profile and opens the same state
  backend the parent used. Previously the child defaulted to local
  sqlite and could not find the trigger row the parent had enqueued
  in postgres, producing a 30s timeout with a misleading error.
- **Controller profiles no longer need `controller: <self>` on every
  surface.** When `InheritControllerDefaults` fills URL+Token onto a
  surface from the profile's top-level `controller:` block, it now
  also fills the surface's `controller:` (profile-name reference) so
  the lookup callback can resolve it. A profile that just declares
  `controller: { url, token }` + `state/cache/logs/secrets: { type:
  controller }` is now a complete, working spec.

### Changed

- **install.sh installs only `sparkwing`.** Previous revisions also
  dropped `sparkwing-local-ws` and `sparkwing-web` into `~/.local/bin`;
  both are now removed on next install (sweep is silent if absent).
  Cluster-side binaries (`sparkwing-cache`, `-controller`, `-logs`,
  `-runner`, `-web`) run only as pods and are published as Docker
  images; install.sh sweeps them from `$DEST` and from `$GOPATH/bin`
  on every run so a stale `go install ./cmd/sparkwing-<x>` artifact
  cannot keep shadowing the laptop CLI on PATH. `sparkwing-local-ws`
  is superseded by `sparkwing dashboard start` and is no longer
  published as a release binary.

### Fixed

- **dashboard:** `sparkwing dashboard start` now fails fast with a clear
  error when the bind address is already in use, naming the holding
  process (e.g. `address 127.0.0.1:4343 already in use by
  sparkwing-local-ws (pid 37326)`). Previously the supervisor would
  silently crash, the PID file never got written, and `sparkwing
  dashboard kill` would then report "not running" even though something
  was visibly serving the port. `start` also treats listener-not-ready
  and missing-PID-file as hard errors, surfacing the tail of
  `dashboard.log` instead of printing a success banner with a dead PID.
- **dashboard:** `sparkwing dashboard start` now restarts an existing
  supervisor it owns instead of refusing. After upgrading the CLI,
  re-running `sparkwing dashboard start` is enough to pick up the new
  embedded SPA bundle -- no manual `kill` step needed. Foreign
  processes on the bind address are still left alone (the error path).
- **flake:** `TestApproval_ApprovedFlowsToSuccess` previously silently
  swallowed errors from the test resolver goroutine (`store.Open`,
  `ListPendingApprovals`, `ResolveApproval`), so any transient failure
  there surfaced as a misleading `status = "failed"` from the
  orchestrator's downstream timeout. The resolver now reports its own
  errors via `t.Errorf`, the approval window was widened from 5s to
  30s, and the test joins the resolver goroutine before returning.
  Verified clean under `go test -race -count=100`.

## [v0.6.2] - 2026-05-30

### Fixed

- **dashboard:** `sparkwing dashboard start` no longer ships a stale
  embedded dashboard bundle. Two binaries embed it via
  `//go:embed all:next-out`: `sparkwing` (powers `dashboard start`)
  and `sparkwing-web` (cluster pod). The release workflow previously
  rebuilt the bundle only for `sparkwing-web`, so released
  `sparkwing` binaries used whatever stale `internal/web/next-out/`
  was on the runner cache (committed `.gitkeep` only). `bin/install.sh`
  also skipped the rebuild. Both paths now call `bin/build-web.sh`,
  so every install + every released artifact ships the current
  dashboard SPA. Set `SKIP_WEB_BUILD=1` on `install.sh` to bypass
  during Go-only iteration.

## [v0.6.1] - 2026-05-30

### Fixed

- **orchestrator:** `BindPipelinesFromYAML` now runs before
  `parseTypedFlags`, so YAML-only pipeline names (multiple pipelines
  sharing one entrypoint via `RegisterEntrypoint`) resolve correctly.
  Previously the typed-flag parser called `sparkwing.Lookup` and got
  "unknown pipeline" because the bind happened after.

## [v0.6.0] - 2026-05-29

### Added

- **sdk:** `RegisterEntrypoint[T](name, factory)` declares a Go work
  unit by its entrypoint type name. Combined with the new
  `BindPipelinesFromYAML(cfg)` bootstrap, one entrypoint can back
  many pipelines -- each pipeline in YAML names the entrypoint and
  supplies its own policy.
- **sdk:** Typed-args system via `sparkwing.WithArgs[T]` + optional
  `Schema()` method (`Required` / `RequiredWhen(predicate)` /
  `Default` / `Computed(fn)` / `OneOf` / `Min` / `Max` / `Range` /
  `Positive` / `Custom(fn)` / group rules). Predicate vocab:
  `ArgEq`/`ArgNeq`/`ArgIn`/`ArgSet`/`ArgUnset` plus `And`/`Or`/`Not`
  and `Local`/`Remote`/`Profile(name)`/`Always`. `sparkwing.Arg[T]`
  reads a resolved arg by CLI flag name.
- **cli:** `sparkwing run <pipeline> --help` lists every transitive
  `WithArgs[T]` flag declared by jobs the pipeline registers,
  annotated with `[from job <id>]` so authors can trace each flag
  back to its owning job.
- **config:** Top-level `defaults:` block (`profile`, `args`,
  `guards`, `requires`) supplies per-pipeline fallbacks. `profile`,
  `guards`, `requires` replace wholesale at pipeline level when
  declared; `args` merges per-key (pipeline wins per-key).
- **config:** Project YAML grows a `profiles:` map (same shape as
  `~/.config/sparkwing/profiles.yaml`). A pipeline references one
  via `pipeline.profile: NAME`; `defaults.profile: NAME` provides
  the project-wide default.
- **config:** Pipeline `guards:` block. Token vocabulary normalized
  to `namespace:rest`: `profile:local`, `profile:controller`,
  `profile:name=NAME`, `git:branch=NAME`, `git:branch=default`,
  `arg:FLAG=VALUE`. `require:` is AND-composed; `reject:` is
  OR-composed and fires first.
- **config:** Pipeline `requires: [labels]` lists runner labels
  every job in the pipeline must satisfy (unioned with each job's
  own `Job.Requires(...)` declarations). The reserved `local` label
  pins execution to in-process (same effect as `--sw-local-only`).
- **config:** Backend specs gained `token_env: VAR` for sourcing
  the controller token from an env var instead of inlining it --
  intended for checked-in project YAML where inline tokens are a
  non-starter.
- **config:** Backend spec gained `type: none` (valid only on the
  `secrets` surface). Profile validator requires every profile to
  declare all four surfaces (`secrets`, `state`, `cache`, `logs`);
  pipelines with no secrets-resolving jobs use `type: none` to
  satisfy the requirement explicitly.
- **config:** Per-surface controller fields (`url`/`token`/
  `token_env`) inherit from the profile's top-level `controller:`
  block when omitted. A profile that routes every surface through
  the same controller writes the URL/token once instead of five
  times.
- **sdk:** `Git.DefaultBranch` populated from origin's HEAD
  symref. Feeds `git:branch=default` guard evaluation.

### Changed (Breaking)

- **config:** Source/backend specs unified. The standalone `sources`
  registry and `sources.Source` type are gone; secrets are a fourth
  `backends.Surfaces` field alongside `state`/`cache`/`logs`. Valid
  secrets `type:` values: `controller`, `filesystem`, `env`, `none`.
- **config:** Pipeline `defaults:` field renamed to `args:`. Same
  semantics, clearer name.
- **config:** Pipeline `dispatch:` block removed wholesale. Its
  former contents (`source`, `requires_approval`, `protected`,
  `backend`, `runners`) are gone or relocated: source resolution
  now flows through the active profile's `secrets:` surface;
  approval is a job-level concern (declare an approval job); the
  "protected" gate is expressed via `guards.require: [git:branch=default]`;
  per-pipeline backend overrides are gone (use `--profile` to swap
  the bundle); runner allowlists moved to job-level
  `Job.Requires(...)` labels + pipeline-level `requires:`.
- **config:** Project YAML's `runners:` and `sources:` registries
  removed. Job-level `Job.Requires(...)` labels replace runner
  registration; inline `secrets:` surface on the active profile
  replaces named source registries.
- **profile:** Profile resolution is `--profile NAME` only -- no
  laptop fallback, no `default:` field in profiles.yaml, no
  `sparkwing.yaml profile:` hint, no env-detect rules. When no
  profile is selected, the orchestrator runs against a sqlite-only
  test/dev shape; remote-controller verbs (`pipeline trigger`,
  `users`, `gc`, `approvals`, `debug replay`) refuse to run without
  a profile that has a `controller:` block.
- **profile:** `--profile X` wins wholesale -- the named profile's
  full backend bundle applies; per-pipeline `profile:` selections
  are discarded. Keeps state/cache/logs/secrets coherent so a run
  can't have its logs in one place and its state in another.
- **config:** Guard token grammar rewritten to `namespace:rest`.
  `profile-local` -> `profile:local`, `profile-controller` ->
  `profile:controller`, `profile-name:NAME` -> `profile:name=NAME`,
  `git-branch:NAME` -> `git:branch=NAME`, `git-branch:default` ->
  `git:branch=default`. Old syntax errors at parse time.
- **config:** Pipeline-level trims: `tags`, `hidden`, `on.manual`,
  `on.deploy`, `description` rationalized; `dispatch.runners`
  allowlist gone (use `requires:`); `dispatch.approvals` enum gone
  (approval is a job).
- **config:** Profile `controller:` is a nested block with `url:` +
  `token:` (was two flat fields).
- **config:** Profile fields removed: `gitcache`, `cost_per_runner_hour`,
  `auto_allow`, `default_runner`, `log_store`, `artifact_store`,
  `detect`. The CLI discovers the cache pod via the controller's
  `GET /api/v1/services` endpoint; the other fields were unused or
  footguns.
- **sdk:** `PipelineConfig[T]`, `ConfigProvider`,
  `ResolvePipelineConfig`, `InspectPipelineConfig`, `ConfigField`,
  `WithPipelineConfig` removed. Use `WithArgs[T]` with YAML `args:`
  for per-deployment overrides, or hardcode constants in Go.
- **sdk:** `OnTarget(...)` on Job/WorkStep/JobGroup removed.
  `sparkwing.Target(ctx)` removed. Split multi-target pipelines into
  one pipeline per target shape.
- **cli:** `--target` removed. Pipeline name is the deployment
  selector.
- **controller:** New `--cache-pod-url` flag (or `CACHE_POD_URL`
  env var) on `sparkwing-controller`. When set, the controller
  announces the URL via `GET /api/v1/services` so operator CLIs
  can discover it.

### Fixed

- **release:** `prepare-changelog` and `bump-self-replace` no longer
  race on `git commit`. They previously ran in parallel and both did
  `git add <file>` + `git commit -m ...` without path scoping, so
  whichever committed second found "nothing to commit." Now
  `bump-self-replace` is serialized after `prepare-changelog`.
- **sparks:** The resolver no longer errors when a `go.work` is in
  scope. The overlay's `.resolved.sum` write is skipped (with a
  single-line warning) instead of failing, matching the existing
  workspace-mode tolerance in `internal/bincache`.

### Docs

- **docs:** v0.6.0 migration guide at `docs/migrations/v0.6.0.md`
  walks the entrypoint-vs-pipeline split, the unified backend
  model, the new `defaults:` and `profiles:` blocks, the
  `namespace:rest` guard grammar, and the `--profile`-wholesale
  resolution.

## [v0.5.1] - 2026-05-28
### Changed

- **release:** The `release` pipeline now composes the `PreCommit`
  and `PrePush` job types directly into its plan as `gate-pre-commit`
  and `gate-pre-push` nodes, gating every mutating step on their
  success. Previously a release tag pushed via `sparkwing run release`
  skipped both pipelines entirely, so lint / em-dash / race / vuln
  regressions catchable by an everyday push could ship past the
  release path. The gates run in parallel after `check-clean-tree`
  and block `prepare-changelog` + `bump-self-replace` + `push-tag`
  -- if either fails, no commit lands. See
  `docs/proposals/release-pipeline-gates.md` for the DAG, the
  alternatives considered (subprocess, `RunAndAwait`), and the
  general lesson on local-composition vs remote-dispatch primitives.
  Wall-clock cost: about 35 seconds added per release.

## [v0.5.0] - 2026-05-28
### Added

- **sdk:** `CacheOptions.QueueTimeout` for queue-shaped concurrency.
  When set, a queued arrival under `OnLimit: Queue` that doesn't get a
  slot within the duration fails cleanly with `failure_reason:
  queue_timeout` instead of waiting indefinitely. Zero (the default)
  preserves the wait-forever behavior.
- **cli:** `sparkwing pipeline trigger <name> --profile <p>` submits a
  trigger to the named profile's controller and tails the remote run by
  default; `--detach` for fire-and-forget. Replaces `sparkwing run --on`
  for remote dispatch. `sparkwing run` now exclusively means "execute
  here."
- **cli:** `sparkwing profile` prints the resolved profile and the
  resolution chain (flag, project hint, default) without running
  anything.
- **config:** Per-profile `detect:` block in `profiles.yaml` for
  environment auto-selection. Replaces the `environments:` block in
  `backends.yaml`. `gha` and `kubernetes` ship as built-in profiles
  that detect their respective env vars.
- **config:** Per-profile `mirror_local:` flag (default `true`) controls
  whether local execution against a remote profile also writes to local
  SQLite for offline post-hoc viewing.

### Changed

- **cli:** The `run_summary` headline now leads with the
  root-cause node -- the one that actually errored -- and a one-line
  error tail, then reports cascaded cancellations separately
  ("N nodes cancelled by the failure"). The node tally splits
  `cancelled` (an upstream-failure cascade) from `skipped` (a SkipIf /
  filter decision) instead of lumping both, so a single broken leaf no
  longer reads as a wall of failures.
- **orchestrator (Breaking):** A node that spawns a child pipeline via
  `RunAndAwait` now emits structured `child_run_start` and
  `child_run_finish` events into the parent's stream, replacing the
  prior single `pipeline_await_spawned` audit event. `child_run_finish`
  carries the child's `run_id`, terminal `status`
  (success/failed/cancelled/timeout), and `duration_ms`, so the parent
  links to the child without inlining its output. Read the child's own
  logs with `sparkwing runs logs --run <child_id>` or
  `sparkwing runs logs --run <parent> --tree`. See
  [migration guide](docs/migrations/v0.5.0.md#audit-stream-events-for-spawned-children).
- **config (Breaking):** Project YAML collapses to a single
  `.sparkwing/sparkwing.yaml` file. See
  [migration guide](docs/migrations/v0.5.0.md#single-sparkwingsparkwingyaml-per-repo).
  The separate `pipelines.yaml`, `backends.yaml`, `runners.yaml`,
  `sources.yaml`, and `sparks.yaml` files are no longer read; sparkwing
  errors at startup if any of them exist in a `.sparkwing/` directory.
- **config (Breaking):** `~/.config/sparkwing/profiles.yaml` profiles
  now carry the full backend triple (`state`, `cache`, `logs`) alongside
  any `controller` / `token`. See
  [migration guide](docs/migrations/v0.5.0.md#profiles-absorb-all-backend-specs).
- **cli (Breaking):** `--on` and `--sw-on` are removed; `--profile`
  replaces them for storage / dispatch addressing. See
  [migration guide](docs/migrations/v0.5.0.md#--profile-is-the-only-where-flag).
- **cli (Breaking):** `--sw-target` is renamed to `--target` (same
  semantics -- the pipeline-internal deployment-environment selector,
  moved out of the `--sw-` namespace). See
  [migration guide](docs/migrations/v0.5.0.md#--profile-is-the-only-where-flag).
- **cli (Breaking):** `sparkwing run --on prof` no longer dispatches
  to a remote controller; use `sparkwing pipeline trigger ... --profile prof`.
  See
  [migration guide](docs/migrations/v0.5.0.md#sparkwing-pipeline-trigger-for-remote-execution).
- **orchestrator (Breaking):** Local execution against a remote profile
  dual-writes state to local SQLite + the profile's backend. Previously
  state went only to the resolved backend. See
  [migration guide](docs/migrations/v0.5.0.md#dual-write-state-when-local-execution-writes-to-a-profile).

### Removed

- **config (Breaking):** `.sparkwing/backends.yaml` is removed. State,
  cache, and logs specs move to per-profile entries in
  `~/.config/sparkwing/profiles.yaml`. See
  [migration guide](docs/migrations/v0.5.0.md#profiles-absorb-all-backend-specs).
- **config (Breaking):** `.sparkwing/sources.yaml`, `.sparkwing/runners.yaml`,
  `.sparkwing/sparks.yaml`, and `.sparkwing/pipelines.yaml` are removed
  as standalone files. Their content moves under top-level keys in
  `.sparkwing/sparkwing.yaml`. See
  [migration guide](docs/migrations/v0.5.0.md#single-sparkwingsparkwingyaml-per-repo).

### Fixed

- **orchestrator:** The dispatcher no longer hangs indefinitely when a
  per-node goroutine fails to terminate. `dispatch` bounds its
  post-DAG `wg.Wait` with `Options.DispatchWaitTimeout` (env
  `SPARKWING_DISPATCH_WAIT_TIMEOUT`, default 30m). On timeout it emits
  a `dispatch_wait_timeout` event with the list of stuck nodes and a
  full goroutine stack dump, then returns -- which fires the deferred
  concurrency-namespace release so a wedged run can't lock the rest
  of the fleet behind a process that will never make progress. Set to
  a negative duration (or `SPARKWING_DISPATCH_WAIT_TIMEOUT=off`) to
  restore the historical wait-forever behavior.
- **store:** `SQLITE_BUSY` under concurrent writers no longer fails the
  run. The state store opens with a 30s `busy_timeout` and takes its
  write lock at transaction start, so multiple `sparkwing run`
  invocations sharing one `state.db` wait their turn instead of aborting
  with `database is locked`. The local dashboard reads through a
  read-only connection so it can't starve out active runs.

### Docs

- **docs:** New "Gate-shaped pipelines" section in `docs/caching.md`
  documenting `OnLimit: Queue` plus `QueueTimeout` as the recommended
  pattern for CI gates contended across processes, instead of
  hand-rolling poll-and-retry around `OnLimit: Fail`.
- **docs:** New migration guide at `docs/migrations/v0.5.0.md` covering
  the config flatten, the new `pipeline trigger` verb, the `--profile`
  unification, and the dual-write state model.

## [v0.4.0] - 2026-05-20

A large release that converges on the v1-ready API surface. Two
foundational reshapes ship here: the **author-facing SDK** (`sparkwing/`)
is cleaned up -- `*Node`/`*NodeGroup` types renamed to `*JobNode`/`*JobGroup`,
30+ orchestrator-only plumbing symbols moved out, `Needs()` typed via the
new `Dep` / `WorkDep` interfaces, and the cache / spawn / risk APIs
reshaped -- and the **package layout** finalizes the public/private
boundary (`orchestrator/` → `internal/`, `logs/` → `pkg/logs/`,
`secrets/` → `internal/`, and several more moves). Adopters hit a lot of
compile errors in one release; this is deliberate so the rest of the
v0.x line can stay quiet.

Other major adds: declarative target/runner config via new `backends.yaml`
/ `runners.yaml` / `sources.yaml`; OpenAPI 3.0 spec for the controller
HTTP API; `.apidiff/` snapshots for every covered package; storage +
cipher conformance test suites; release tooling that auto-rewrites
`[Unreleased]` to a versioned section and uses the CHANGELOG entry as
the GitHub Release body.

### Added

- **web:** `Tab` / `Shift+Tab` cycles the active tab in the runs view
  (Summary, Logs, Resources, DAG, Timeline, Setup) with wrap-around.
  Works from any column once a run is open, so operators can flip
  through tabs without first moving their cursor.
- **sdk:** `sparkwing.Dep` and `sparkwing.WorkDep` closed interfaces for
  typed dependency wiring. Implementations are limited to sparkwing-defined
  handles -- Plan-layer `Dep` is `*JobNode` / `*ApprovalGate` /
  `*JobGroup`; Work-layer `WorkDep` is `*WorkStep` / `*StepGroup` /
  `*SpawnSpec` / `*SpawnGenSpec`. The two interfaces are disjoint, so a
  `*WorkStep` in `*JobNode.Needs` (or vice versa) is a compile-time
  error.
- **sdk:** `sparkwing.NoCache` typed sentinel for explicit cache opt-out
  from a `CacheOptions.ContentHash` function. Distinct from the zero
  `CacheKey`: operators see an "explicit opt-out" log line vs a "missing
  key" warning, so deliberate skips no longer look like hashing bugs.
- **sdk:** `EnvVarDocer` optional interface. Pipelines implementing
  `EnvVars() []EnvVarDoc` declare the environment variables they read as
  inputs; `sparkwing pipeline describe` and `sparkwing run <pipeline>
  --help` surface them under an "environment variables" section
  alongside typed `Inputs`. Prefer typed `Inputs` for user-controlled
  values; `EnvVarDocer` is for process-wide config or external-system
  integration that already uses env.
- **sdk:** `OnTarget(...)` verb on `*JobNode` / `*WorkStep` and a
  `sparkwing.Target(ctx)` accessor for per-target dispatch. Pairs with
  the new `targets:` block in `pipelines.yaml` and the `--sw-target`
  CLI flag.
- **sdk:** `Workable` optional interfaces for declarative runner
  selection: `Requires() []string`, `Prefers() []string`, `WhenRunner()
  []string`. Chainable equivalents on `*JobNode` (`Requires`, `Prefers`,
  `WhenRunner`) for direct authoring; the Workable form lets shared job
  types carry their own constraints.
- **sdk:** Pipelines can implement optional `Config() any` and `Secrets()
  any` methods. The orchestrator resolves them at run-start from
  `pipelines.yaml` `values:` / `secrets:` blocks, the matched trigger
  spec, and any `targets[<active>]` overlay; step bodies read them via
  `sparkwing.PipelineConfig[T](ctx)` and
  `sparkwing.PipelineSecrets[T](ctx)`.
- **sdk:** Node body errors are automatically prefixed with the node ID
  when the author hasn't already prefixed them. Bare `return err` or
  `errors.New("boom")` from a step surfaces in dispatch logs as
  `<node-id>: boom` so failure messages identify the failing node by
  default; authors writing richer messages keep their full content.
- **config:** New declarative YAML surfaces for target + runner
  configuration. `backends.yaml` selects cache / logs / state backends
  per environment with `match:` rules. `runners.yaml` declares named
  runner pools with label constraints. `sources.yaml` declares config +
  secrets sources per target. `pipelines.yaml` gains `targets:`,
  `runners:`, `values:`, and `secrets:` fields. `profiles.yaml` gains
  `default_runner:`.
- **controller:** Cluster controller now exposes `GET
  /api/v1/runs/{id}/attempts` (the retry-tree listing the dashboard's
  Attempts dropdown reads) and supports `?full=1` on `POST
  /api/v1/runs/{id}/retry` for the "rerun all" mode. Matches the laptop
  controller's surface.
- **controller:** `pkg/controller.Server` functional options
  `WithArtifactStore` (enables `GET /api/v1/artifacts/{key}` for laptop
  mode) and `WithReconcileHook` (runs a sweep closure before list-runs /
  get-run reads, eliminating stale "running" rows from crashed in-process
  orchestrators). Pool routes (`GET /api/v1/pool*`) are registered only
  when `AttachPool` is also called.
- **controller:** Stdout logs backend (`pkg/storage/stdoutlogs`) for
  cluster runs that route logs to container stdout.
- **controller:** SQLite state backend wired through the backend factory.
- **cache:** `sparkwing-cache` accepts pflag-based command-line flags
  for every setting (`--addr`, `--data-dir`, `--proxy-cache-dir`,
  `--fetch-interval`, `--proxy-cache-ttl`, `--proxy-max-age`,
  `--api-token`, `--auto-register-repos`, `--ssh-key-dir`,
  `--git-fork-limit`). Each falls back to the corresponding env var so
  existing k8s ConfigMap-style configurations work unchanged.
- **wire:** OpenAPI 3.0 spec at `api/openapi.yaml` covering every public
  controller route -- runs, nodes, steps, events, triggers, approvals,
  concurrency, debug pauses, tokens, users, secrets, auth, agents,
  trends, pipelines -- plus the mode-conditional pool (cluster) and
  artifacts (laptop) routes. Two security schemes (`Authorization:
  Bearer <token>` for service callers, `Authorization: Session <id>` for
  dashboard browser flow) wired to the operations that require auth. 26
  component schemas mirror `pkg/store` types. The HTTP surface is now a
  formal contract (see VERSIONING.md).
- **wire:** Checked-in API surface snapshots under `.apidiff/` for every
  covered public package (21 files). The new `cmd/apidiff` tool walks
  each package's AST and emits a deterministic text representation of
  the exported declarations with godoc stripped. `sparkwing run lint`
  regenerates snapshots into a tempdir and diffs against the checked-in
  tree; drift fails CI with an educational message. Authors refresh the
  baseline via `bash bin/regen-api-snapshot.sh` and review the snapshot
  diff in the PR as the surface-change artifact.
- **wire:** Conformance test suites for the three plug-in interfaces:
  `pkg/storage.ArtifactStore`, `pkg/storage.LogStore`, and
  `pkg/controller.Cipher`. Each suite lives in a sibling conformance
  subpackage and exposes a `TestX(t, factory)` function any
  implementation can call from its own `*_test.go` to verify it
  satisfies the contract. Operations a partial implementation opts out
  of (e.g., `Read` on the write-only `stdoutlogs.LogStore`) skip rather
  than fail.
- **wire:** `pkg/storage.ErrNotSupported` sentinel for operations a
  partial implementation deliberately doesn't perform. Conformance
  suites use `errors.Is` against this to know which subtests to skip.
- **release:** `sparkwing run release` auto-rewrites `## [Unreleased]`
  to `## [vX.Y.Z] - YYYY-MM-DD` and commits before tagging, so the
  tagged commit ships with the versioned section in place. The
  GH-Actions workflow extracts that section as the GitHub Release body
  via `bin/extract-changelog-section.sh` -- the curated CHANGELOG entry
  is the release page, not a commit log dump.
- **release:** Hard refusal of any `v1.0.0+` tag. Pre-1.0 lock requires
  a deliberate code change to unlock (bumping to v1+ commits the API
  surface; this shouldn't happen by typo or `--bump major`). Companion
  `pre_v1_policy.go` linter catches doc drift -- CHANGELOG must not
  carry a `## [v1.x.x]` section, VERSIONING.md must not assert v1 has
  shipped, and any local `v1.0.0+` git tag is surfaced as a warning.
- **release:** CHANGELOG style + structure enforced by `changelog_lint.go`
  (`LintChangelog(body, migrations fs.FS)`), wired into `sparkwing run
  lint`. Two checks: no duplicate `### <Category>` sub-headings within a
  single section; every `(Breaking)` entry in a versioned section links
  to a real `docs/migrations/v<X.Y.Z>.md#<anchor>` whose file exists,
  anchor resolves to an H2, and version matches.
- **cli:** `sparkwing docs migrations` subcommand for in-CLI access to
  per-version migration guides. `list` shows every guide the binary
  embeds (with date + one-line summary); `read --version vX.Y.Z`
  prints one guide; `between --from --to` concatenates every guide in
  a version range with `---` separators. Default `-o markdown` so
  agents pipe straight into context. Stale-CLI hint surfaces in `list`
  when newer guides exist on the web.
- **cli:** `sparkwing docs versions` subcommand. Lists known versions
  (embedded by default; embedded + remote when `--web` is set), flags
  the latest, and surfaces source (`embedded` vs `remote`). Exits
  non-zero when `--web` discovery fails so scripts detect.
- **cli:** `--web` flag on `sparkwing docs read|list` and
  `sparkwing docs migrations read|list|between` fetches cross-version
  content from `sparkwing.dev` when the requested version isn't in
  the binary's embed. The CLI stays hermetic by default; `--web` is
  opt-in. Pairs with `--version vX.Y.Z|latest` to pick the target
  version. Companion `--no-cache` flag bypasses the on-disk cache for
  one invocation.
- **cli:** `sparkwing docs cache info` / `cache clear` for inspecting
  and resetting the on-disk web cache at `$XDG_CACHE_HOME/sparkwing/web/`
  (default `~/.cache/sparkwing/web/`). 24h TTL on `versions.json` and
  `*/index.json`; indefinite TTL on per-version `.md` content (tags
  are immutable).
- **cli:** `SPARKWING_DOCS_BASE_URL` environment variable overrides the
  default `https://sparkwing.dev` base for the web fetcher. Useful for
  testing against a local mirror; falls through to the default when
  unset.
- **cli:** `sparkwing info` advertises four new URLs for agent
  discovery: `docs_index_url`, `migration_guides_url`,
  `migration_guides_agent_url`, `migration_guides_index_url`.
- **cli:** `--sw-only=<glob>` runs a partial DAG by `path.Match` over
  JobNode IDs. Transitively pulls `Needs()` ancestors so the dispatch
  stays self-consistent -- a glob hitting only the leaves still
  schedules their preconditions. Fails fast on a malformed glob or a
  pattern that matches nothing. Mutually exclusive with
  `--sw-start-at` / `--sw-stop-at` (step-level vs job-level filter
  modes).
- **cli:** `--sw-no-cache` disables cache READS on this run's per-node
  `Cache()` lookups. Cache WRITES still occur on success, so the next
  run over the same content hits cache normally. Distinct from the
  bincache (compiled-pipeline-binary cache) gated by
  `SPARKWING_NO_BINCACHE`.
- **release:** `sparkwing run release` refuses to ship a version when
  `CHANGELOG.md` `[Unreleased]` has no entries. Pairs with the existing
  PR-time CI gate (`bin/check-changelog.sh`) that catches missing
  entries at review time.
- **release:** Pre-commit and pre-push pipelines (`sparkwing run
  pre-commit` / `pre-push`) with version-freshness gating, govulncheck,
  and a refusal-on-`replace` directive in `go.mod`.
- **release:** `.golangci.yml` at the repo root with a balanced linter
  set (gofumpt, goimports, govet, staticcheck, errcheck, errorlint,
  bodyclose, copyloopvar, ineffassign, misspell, nolintlint, unconvert,
  usestdlibvars, bidichk). Wired into the existing lint pipeline.
- **docs:** `VERSIONING.md` defines the stability promise for `pkg/`,
  `sparkwing/`, CLI flags, wire protocols, and YAML config formats;
  spells out what counts as a breaking change; documents the pre-1.0
  hard-cut stance.
- **docs:** `docs/changelog-style.md` documents the CHANGELOG conventions
  the pre-release manicuring agent applies. `docs/migrations/` carries
  per-version migration guides.
- **docs:** Curated godoc with `Example*` test functions across
  `sparkwing/` and every covered `pkg/` package (`storage`, `store`,
  `controller` + `client` + `pool`, `logs`, `pipelines`, `backends`,
  `runners`, `sources`, `runner`, `docs`, `color`, `localws`). Top-tier
  types use `[Type]` cross-reference links so `go doc` and pkg.go.dev
  render them as navigable.
- **docs:** `sparkwing.Bash` and `sparkwing.Exec` godoc now document the
  signal-propagation contract end-to-end (SIGKILL to direct child on
  `ctx` cancel, terminal SIGINT reaches the foreground process group,
  grandchildren are not torn down on programmatic cancel).

### Changed

- **web:** Arrow keys and `j`/`k`/`h`/`l` in the runs view now
  auto-select the focused run or node as the cursor moves -- pressing
  `Enter` is no longer required to load detail for the row under the
  cursor. Cursor movement clamps at the top and bottom of each list
  instead of wrapping. Arrow navigation into the tabs column has been
  removed; use `Tab` instead.
- **cli:** Tab-completion descriptions for pipeline-defined flags now
  carry an `[arg, optional]` / `[arg, required]` tag so they're
  visually distinguishable from sparkwing-owned flags like
  `--sw-profile` or `--help` in the flat menu. The internal
  `_complete-flags` and `_complete-pipeline-flags` helpers now emit
  two tab-separated columns (`--flag<TAB>description`) instead of
  three -- the group column was unused after the shell-side flatten
  step and the bucketing code in the zsh script has been removed.
- **docs:** Example struct names in sparkwing's own examples,
  documentation, and template scaffolders normalized to drop the
  redundant `Job` suffix (`&BuildJob{}` → `&Build{}`, `*BuildJob` →
  `*Build`, etc.). The constructor verb (`sparkwing.Job(...)`)
  provides "this is a job" context; the struct doesn't need to repeat
  it. No SDK behavior change; adopter code that names its own structs
  differently is unaffected.
- **sdk (Breaking):** `*Node` → `*JobNode`, `*NodeGroup` → `*JobGroup`,
  and `Node.RunsOn` / `NodeGroup.RunsOn` / `Node.RunsOnLabels` →
  `Requires` / `Requires` / `RequiresLabels`. The package-level
  `sparkwing.Job` and `sparkwing.JobGroup` constructors keep their
  names; only the Go type names change. JSON wire tags (`node`,
  `node_id`, `runs_on`, `node_start`, ...) are preserved for log /
  snapshot compatibility. See
  [migration guide](docs/migrations/v0.4.0.md#node-job-rename).
- **sdk (Breaking):** `Needs(...any)` and `NeedsOptional(...any)` on
  every dep-accepting type replaced with typed-dep signatures:
  `Needs(...Dep)` for Plan-layer methods, `Needs(...WorkDep)` for
  Work-layer methods. By-name string references to upstream nodes /
  steps are no longer supported -- the interfaces are intentionally
  closed to live handles. Patterns that built deps from yaml or other
  runtime sources via string IDs must do a two-pass construction (create
  all nodes / steps, store handles, then wire deps using the handles).
  See [migration guide](docs/migrations/v0.4.0.md#typed-dep-interfaces).
- **sdk (Breaking):** `CacheOptions.Key` → `Namespace`,
  `CacheOptions.CacheKey` → `ContentHash`, `HasKey()` → `HasNamespace()`.
  The new names match the actual concept (`Namespace` is a coordination
  scope; `ContentHash` is the content-addressed key driver) and remove
  the ambiguity that let two unrelated nodes collapse into one cache
  entry when an upstream input was missing. See
  [migration guide](docs/migrations/v0.4.0.md#cacheoptions-rename).
- **sdk (Breaking):** `JobSpawn(...)` returns `*SpawnSpec` (was
  `*SpawnHandle`); `JobSpawnEach(...)` returns `*SpawnGenSpec` (was
  `*SpawnGroup`). Chainable methods (`Needs`, `SkipIf`) now live on the
  spec types directly; the `Spec()` accessors are gone -- the handles
  were thin wrappers around the specs. Code that chains
  `sw.JobSpawn(w, ...).Needs(...)` is unchanged. See
  [migration guide](docs/migrations/v0.4.0.md#spawn-types).
- **sdk (Breaking):** `WorkStep.Destructive()` / `.AffectsProduction()`
  / `.CostsMoney()` replaced by `.Risk("destructive")` /
  `.Risk("prod")` / `.Risk("money")`. Labels are now author-defined
  (any kebab-case string works, e.g. `.Risk("rotates-key")`). Profile
  `auto_allow` switches from per-marker booleans to a list of labels.
  See [migration guide](docs/migrations/v0.4.0.md#risk-labels).
- **sdk (Breaking):** Roughly 30 orchestrator-only plumbing symbols
  relocated from the `sparkwing` package to `internal/sparkwingruntime`.
  Pipeline authors never called these -- they were always for code
  rebuilding the orchestrator. Runtime-mutator methods
  (`Plan.InsertChild`, `Plan.InsertExpanded`, `JobGroup.Finalize`,
  `WorkStep.Fn`, `WorkStep.MarkDone`, `SpawnSpec.SetResolvedID`,
  `SpawnSpec.MarkDone`) are no longer methods on the spec types; call
  them via `sparkwing.RuntimePlumbing.Fns.<Name>(...)`. `RuntimePlumbing`
  itself gains a `{Keys, Fns}` shape. See
  [migration guide](docs/migrations/v0.4.0.md#runtime-plumbing).
- **sdk (Breaking):** Author-facing surface cleanup. Renames:
  `JobNode.OnTargetList()` → `OnTargets()`, `WorkStep.OnTargetList()` →
  `OnTargets()`. Removals: `JobNode.OnFailureNodeID()`,
  `JobNode.Dynamic()`, `JobNode.IsDynamic()`, `sparkwing.ToKebabCase`,
  `sparkwing.LookupInstance`, `sparkwing.Runtime()` alias,
  `sparkwing.WithJob` / `JobFromContext` / `JobStackFromContext`,
  `sparkwing.SetDebug` (unexported -- `SPARKWING_DEBUG` at process
  start is the only supported toggle). See
  [migration guide](docs/migrations/v0.4.0.md#sdk-surface-cleanup).
- **sdk (Breaking):** `TriggerInfo.Env` removed. Trigger-supplied values
  now flow through the pipeline's typed `Config` struct via the
  trigger's `values:` block in `pipelines.yaml` (e.g. `on.push.values`)
  with a matching `sw:"..."` tag on a Config field, read in step bodies
  via `sparkwing.PipelineConfig[T](ctx)`. See
  [migration guide](docs/migrations/v0.4.0.md#trigger-values).
- **runtime (Breaking):** Package layout reorganized to finalize the
  public / private boundary:
  - `orchestrator/` → `internal/orchestrator/`. User repos MUST migrate
    to `pkg/runner.Main()`.
  - `secrets/` → `internal/secrets/`. External consumers implement
    `pkg/controller.Cipher` (two methods, `Seal` + `Open`).
  - `logs/` → `pkg/logs/` (promoted: now part of the public surface).
  - `controller/client/` → `pkg/controller/client/` (promoted).
  - `logutil`, `bincache`, `otelutil`, `profile`, `repos` → `internal/`
    (demoted: implementation detail).
  - `internal/local/` collapsed into `pkg/controller/`; mode is now
    determined by functional options (`AttachPool` for cluster;
    `WithArtifactStore` + `WithReconcileHook` for laptop).
  - `InProcessDispatcher` moved to `internal/inprocdispatch/`.

  See [migration guide](docs/migrations/v0.4.0.md#package-relocations).
- **runtime (Breaking):** Maintenance methods on `pkg/store.Store` hidden
  behind the `store.Maintenance` bridge. The 9 reaper / sweep methods
  (`ReapExpiredTriggers`, `FailNodesInRun`, `FailStaleQueuedNodes`,
  `FailExpiredNodeClaims`, `ReapStaleConcurrencyHolders`,
  `ReapStaleConcurrencyWaiters`, `SweepExpiredConcurrencyCache`,
  `SweepLRUConcurrencyCache`, `ReconcileConcurrencyKeys`) are no longer
  on the public `Store` API. Call them via
  `store.Maintenance.<Name>(s, ctx, ...)`. See
  [migration guide](docs/migrations/v0.4.0.md#store-maintenance).
- **controller (Breaking):** `pkg/controller.Server.WithSecretsCipher`
  now takes a `pkg/controller.Cipher` interface instead of a concrete
  `*secrets.Cipher`. Concrete-type callers continue to work via
  structural typing; external consumers can now supply custom cipher
  implementations without depending on sparkwing's secrets package. See
  [migration guide](docs/migrations/v0.4.0.md#cipher-interface).
- **cli (Breaking):** Five CLI flag renames:
  - `--sw-change-directory` → `--sw-cd` (the `-C` short form is unchanged)
  - `--sw-for` → `--sw-target` (the `Job.OnTarget("...")` author API is
    unchanged)
  - `--sw-on` → `--sw-profile`
  - `--sw-from` → `--sw-ref` (env-var bridge `SPARKWING_FROM` →
    `SPARKWING_REF`)
  - `--sw-allow-destructive` / `--sw-allow-prod` / `--sw-allow-money`
    collapsed into one `--sw-allow LABEL[,LABEL...]` flag (repeatable;
    comma-separated).

  See [migration guide](docs/migrations/v0.4.0.md#cli-flag-renames).
- **cli (Breaking):** Retired flags. `--sw-retry-of` / `--sw-full` use
  `sparkwing runs retry RUN_ID [--failed | --all]`. `--sw-job` /
  `--sw-prefer` declare runner selection in the pipeline via
  `Job.Requires` / `Job.Prefers`. `--sw-backends-env` -- fix `match:`
  rules in `backends.yaml` or `DetectEnvironment` logic.
  `--sw-config` preset feature removed. `--help-all` removed
  (`--help` now shows everything). Flag-group section headers in
  `--help` and tab-completion dropped (one flat list). See
  [migration guide](docs/migrations/v0.4.0.md#cli-retired-flags).
- **cli (Breaking):** `wing` CLI binary retired. `sparkwing run` is the
  only entry point. Scripts that invoked `wing ...` must update to
  `sparkwing run ...`. See
  [migration guide](docs/migrations/v0.4.0.md#cli-retired-flags).
- **cli (Breaking):** `--json` and `--pretty` flag aliases removed
  across every command. They were soft duplicates of `--output json` /
  `--output pretty`. Update scripts and shell aliases to use the
  canonical `-o`/`--output` form (e.g. `sparkwing runs list -o json`).
  See [migration guide](docs/migrations/v0.4.0.md#cli-output-aliases).
- **cli (Breaking):** `SPARKWING_NO_CACHE` env var renamed to
  `SPARKWING_NO_BINCACHE`. The new `SPARKWING_NO_CACHE` env var (and
  its CLI flag `--sw-no-cache`) gates the per-node result cache --
  what most operators mean when they say "no cache." Update shell
  aliases or CI configs that set `SPARKWING_NO_CACHE` expecting
  bincache-bypass behavior. See
  [migration guide](docs/migrations/v0.4.0.md#no-cache-env-rename).
- **config (Breaking):** `pipelines.yaml` `group:` field and the matching
  `--group` flag on `sparkwing pipeline new` removed. The field had no
  backing on the `pipelines.Pipeline` struct, so strict YAML parsing
  rejected any file that used it. Strip `group:` lines from existing
  `.sparkwing/pipelines.yaml` files. Plan-DAG UI grouping
  (`sw.GroupJobs`, `GroupSteps`) is a separate feature and is
  unaffected. See
  [migration guide](docs/migrations/v0.4.0.md#pipelines-yaml-group).
- **wire (Breaking):** `LogRecord` JSON shape loses the (always-empty)
  `job` and `job_stack` fields, following the removal of
  `sparkwing.WithJob` / `JobFromContext` / `JobStackFromContext`.
  Consumers of JSON log streams that explicitly read these fields will
  see them as missing rather than empty. See
  [migration guide](docs/migrations/v0.4.0.md#logrecord-fields).
- **cli (Breaking):** `sparkwing info -o json` field names normalized
  on the `docs` sub-object. The previously-flat `web` key splits into
  named URL fields with `_url` suffixes: `web` → `web_url`,
  `llms_full` → `llms_full_url`, `llms_txt` → `llms_txt_url`. Three
  new fields (`docs_index_url`, `migration_guides_url`,
  `migration_guides_agent_url`, `migration_guides_index_url`) join
  the object. Consumers parsing `sparkwing info -o json` against the
  `docs` sub-object must update field reads. See
  [migration guide](docs/migrations/v0.4.0.md#info-docs-json).
- **sdk (Breaking):** `pkg/docs.Entry` and `pkg/docs.MigrationEntry`
  reshaped to align with the web's `/docs/index.json` and
  `/migrations/index.json` JSON schemas. `Entry` drops its `Path`
  field (the cache-internal relative path) and now matches
  `{Slug, Title, Summary, Bytes}`. `MigrationEntry` is
  `{Version, Slug, Title, Date, Summary, Bytes}` (with `Slug` ==
  `Version` for parity with the web schema). External consumers
  reading `pkg/docs.List()` or `pkg/docs.MigrationsList()` results
  must update field names; the underlying JSON shape now matches
  what the web emits so agents can consume either source with one
  schema. See
  [migration guide](docs/migrations/v0.4.0.md#pkg-docs-entry-reshape).
- **cache:** `sparkwing-cache` business logic moved from
  `cmd/sparkwing-cache/main.go` (~1700 LOC) into a new `internal/cache`
  package. HTTP wire protocol unchanged; same routes, same shapes;
  existing clients (`pkg/storage/sparkwingcache` adapter, etc.) work
  without modification. Knobs (`APIToken`, `AutoRegisterRepos`,
  `SSHKeyDir`, `GitForkLimit`) resolved from `cache.Config` instead of
  ad-hoc env / hardcoded path reads inside the package; env-var
  fallback now lives at the binary entry point.
- **code-health:** `.golangci.yml` adoption cleared 135 findings across
  the tree. Mechanical mix: gofumpt + goimports formatting, US-locale
  spelling normalization (with `cancelled` / `Cancelled` exempted
  because it's the persisted `Outcome` constant), `usestdlibvars` (HTTP
  verbs / statuses pinned to stdlib constants), `errcheck` wraps,
  `bodyclose`, `errorlint` `%w`, `nolintlint` directives, idiomatic
  naming (`SparkAscii` → `SparkASCII`, etc.). No behavior changes.

### Fixed

- **cli:** `sparkwing run` no longer fails with `-modfile cannot be
  used in workspace mode` when a `go.work` is in scope. When sparkwing
  detects a workspace, it skips its `.resolved.mod` overlay so the
  workspace's module resolution wins, and prints a one-line warning to
  stderr so it's clear sparks pinning is dormant for that build. Honor
  `GOWORK=off` and the explicit `GOWORK=<path>` form. Sparks resolve
  itself (`sparkwing sparks resolve`) still requires no workspace in
  scope and now returns a friendly error instead of the raw toolchain
  message. The canonical multi-module local-dev pattern is documented
  in `docs/sparks.md` -- list every repo you're editing in
  `.sparkwing/go.work`.
- **controller:** `TrendPoint.avg_wait_ms` is now actually computed
  (`started_at - created_at` averaged per bucket, excluding zero-created
  / clock-skew rows). The dashboard's "avg wait" chart shows real
  intake-to-start latency instead of flat zero.
- **controller:** Cluster controller's retry response now returns the
  canonical shape (`{"status":"pending", "trigger_source":"retry",
  "started_at":<creation time>}`) matching the laptop controller. Prior
  cluster behavior used inconsistent field names (`trigger` vs
  `trigger_source`) and status values (`running` vs `pending`);
  dashboards talking to a cluster controller no longer need to
  special-case the response.
- **controller:** Cluster controller pre-allocates the Run row in
  `pending` state before invoking a retry trigger, eliminating the
  window where the retry had been accepted but no row existed yet.
- **controller:** Dead route registration for `GET /api/v1/auth/session`
  removed. The route was registered twice in `pkg/controller/server.go`;
  Go's `http.ServeMux` specificity made the outer (unauthenticated)
  registration win, leaving the inner copy as unreachable dead code.
  Resolved to the intended unauthenticated path (the handler reads
  `Authorization: Session <id>`, not a bearer token).
- **controller:** Stale `handleWaiterNotify` doc comment referenced a
  `coalesced` SSE event that the handler never emits. Rewritten to
  match the three terminal events the handler actually sends (`ready`,
  `superseded`, `stream_end`).
- **cache:** Fragile `init()` ordering in `sparkwing-cache` where
  directory creation ran at package-load time against hardcoded
  `/data/*` paths, before env-var parsing could rebind those paths.
  Directory creation now happens inside `cache.New(cfg)` AFTER the
  resolved Config is in hand. `backgroundFetchLoop` /
  `proxyCleanupLoop` accept the cancellable ctx and exit cleanly on
  shutdown (the prior shape blocked SIGTERM for the full sleep
  interval).
- **cli:** `RunLocal` now surfaces `res.Error` when a run-lifecycle
  failure occurred (previously dropped).
- **cli:** sqlite state without an explicit path falls back to
  `DefaultStateDB` (previously empty-string).
- **cli:** `opts.SparkwingDir` is now treated as the directory, not the
  `pipelines.yaml` path.
- **cli:** Tab-completion wires `--sw-target` / `--sw-prefer` /
  `--sw-backends-env` / `--sw-job` correctly.
- **cli:** OnTarget-skipped jobs are hidden from the CLI plan listing
  (UI metadata still surfaces the skip), and when shown they render
  dimmed with a `[skip: target]` marker.

### Removed

All breaking removals in this release are paired with replacements and
listed above under **Changed**. Quick inventory: `sparkwing.SetDebug`
(debug flag now `SPARKWING_DEBUG`-only), `JobNode.OnFailureNodeID()`,
`JobNode.Dynamic()` / `IsDynamic()`, `sparkwing.ToKebabCase`,
`sparkwing.LookupInstance`, `sparkwing.Runtime()` alias,
`sparkwing.WithJob` / `JobFromContext` / `JobStackFromContext`,
`LogRecord.Job` / `JobStack` fields (and the always-empty `job` /
`job_stack` JSON tags), `TriggerInfo.Env`, `pipelines.yaml` `group:`
field, `--group` flag on `pipeline new`, `--sw-retry-of` / `--sw-full`
/ `--sw-job` / `--sw-prefer` / `--sw-backends-env` / `--sw-config` /
`--help-all` CLI flags, the `wing` CLI binary, `internal/local/`
package (collapsed into `pkg/controller/`).

Non-breaking removals (no replacement needed): `PoolListForTesting` on
`pkg/controller.Server` (had zero callers anywhere; add a same-package
test helper in a `*_test.go` file if you need PVC introspection in
tests). Vestigial `sdk_doc.go` files under `pkg/store/`, `pkg/logs/`,
and `pkg/controller/client/` (replaced by `doc.go` files describing the
actual public surface).

## [v0.3.0] - 2026-05-13

Pre-changelog snapshot. Detailed history wasn't tracked in this file
for releases before v0.4.0; the git log (`git log v0.2.1..v0.3.0`) is
the source of truth. Subsequent versions are documented here in full.

## [v0.2.1] - 2026-05-07

Pre-changelog snapshot. See `git log v0.2.0..v0.2.1`.

## [v0.2.0] - 2026-05-06

Pre-changelog snapshot. See `git log v0.1.0..v0.2.0`.

## [v0.1.0] - 2026-05-06

Initial public release.
