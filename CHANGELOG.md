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

_No entries yet._

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

- **sdk (Breaking):** `*Node` → `*JobNode`, `*NodeGroup` → `*JobGroup`,
  and `Node.RunsOn` / `NodeGroup.RunsOn` / `Node.RunsOnLabels` →
  `Requires` / `Requires` / `RequiresLabels`. The package-level
  `sparkwing.Job` and `sparkwing.JobGroup` constructors keep their
  names; only the Go type names change. Workable struct types in
  examples drop the `Job` suffix (`&BuildJob{}` → `&Build{}`) per the
  new convention. JSON wire tags (`node`, `node_id`, `runs_on`,
  `node_start`, ...) are preserved for log / snapshot compatibility.
  See [migration guide](docs/migrations/v0.4.0.md#node-job-rename).
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
  `sparkwing run ...`.
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
