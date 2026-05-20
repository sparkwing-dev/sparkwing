# Changelog

All notable changes to sparkwing are recorded here. Format follows
[Keep a Changelog](https://keepachangelog.com/). See
[VERSIONING.md](./VERSIONING.md) for the stability policy that
governs what counts as a breaking change and when CHANGELOG entries
are required.

## [Unreleased]

### Changed

- `sparkwing-cache` now resolves `APIToken`, `AutoRegisterRepos`,
  `SSHKeyDir`, and `GitForkLimit` from `cache.Config` instead of
  reading `SPARKWING_API_TOKEN`, `GITCACHE_REPOS`,
  `SPARKWING_GITCACHE_CONCURRENCY`, and a hardcoded `/etc/ssh-key`
  ad-hoc inside the package. Env-var fallback now lives in
  `cmd/sparkwing-cache/main.go` alongside the other flags. Behavior
  unchanged; existing deployments setting these env vars keep
  working.
- `sparkwing-cache` binary refactored: business logic moved from
  `cmd/sparkwing-cache/main.go` (~1681 LOC, plus `proxy.go`) into a
  new `internal/cache` package. The `cmd/sparkwing-cache/main.go`
  shell is now 88 lines (flag parsing → `cache.Config` →
  `cache.New(cfg)` → `srv.Run(ctx)`), matching the texture of the
  other binary entry points. HTTP wire protocol unchanged; the same
  routes accept the same requests and return the same shapes; the
  same clients (`pkg/storage/sparkwingcache` adapter, etc.) work
  with no change.

### Fixed

- Fragile `init()` ordering in `sparkwing-cache` where directory
  creation ran at package-load time against hardcoded `/data/*`
  paths, before `initDataDirs()` (or in the previous shape, before
  env-var parsing) could rebind those paths. Directory creation now
  happens inside `cache.New(cfg)` AFTER the resolved Config is in
  hand, so the boot order is deterministic and reviewable.
  `backgroundFetchLoop` / `proxyCleanupLoop` now accept the
  cancellable ctx and exit cleanly when the process is asked to
  drain (the prior shape blocked SIGTERM for the full sleep
  interval).

### Added

- `sparkwing-cache` now accepts pflag-based command-line flags for
  every setting: `--addr`, `--data-dir`, `--proxy-cache-dir`,
  `--fetch-interval`, `--proxy-cache-ttl`, `--proxy-max-age`,
  `--api-token`, `--auto-register-repos`, `--ssh-key-dir`,
  `--git-fork-limit`. Each flag falls back to the corresponding env
  var (`DATA_DIR`, `PROXY_CACHE_DIR`, `FETCH_INTERVAL`,
  `PROXY_CACHE_TTL`, `PROXY_MAX_AGE`, `SPARKWING_API_TOKEN`,
  `GITCACHE_REPOS`, `SSH_KEY_DIR`, `SPARKWING_GITCACHE_CONCURRENCY`,
  plus `PORT` / `PORT_ADDR` for the bind address) so existing k8s
  ConfigMap-style env configurations continue to work unchanged.
- Conformance test suites for the three plug-in interfaces:
  `pkg/storage.ArtifactStore`, `pkg/storage.LogStore`, and
  `pkg/controller.Cipher`. Each suite lives in a sibling
  conformance subpackage (`pkg/storage/conformance` for the storage
  surfaces; `pkg/controller/ciphertest` for cipher) and exposes a
  `TestX(t, factory)` function any implementation can call from its
  own `*_test.go` to verify it satisfies the contract. All in-tree
  implementations (`fs`, `s3`, `sparkwingcache`, `sparkwinglogs`,
  `stdoutlogs`, `internal/secrets`) now run these suites as part
  of `go test ./...`. Operations a partial implementation opts out
  of (e.g., `Read` on the write-only `stdoutlogs.LogStore`, `List`
  on `sparkwingcache`) skip rather than fail. See VERSIONING.md.
- `pkg/storage.ErrNotSupported` sentinel for operations a partial
  implementation deliberately doesn't perform. Conformance suites
  use `errors.Is` against this to know which subtests to skip.
  `stdoutlogs.ErrReadUnsupported` now wraps this sentinel via
  `fmt.Errorf(...: %w, storage.ErrNotSupported)` so callers using
  either the local or shared sentinel detect the case correctly;
  the existing `ErrReadUnsupported` var name and identity are
  preserved.

### Fixed

- Removed dead route registration for `GET /api/v1/auth/session`.
  The route was registered twice in `pkg/controller/server.go`:
  once on the outer router (no auth middleware) and once inside the
  inner mux (wrapped by the bearer-token middleware). Go's
  `http.ServeMux` specificity rules made the outer registration win
  every time, leaving the inner copy as unreachable dead code.
  Resolved to **outcome A**: the route is intentionally
  unauthenticated — its handler resolves an
  `Authorization: Session <id>` header rather than a bearer token,
  so gating it behind bearer auth would 401 every legitimate caller.
  The inner registration was deleted; the outer one stays as the
  single source of truth. Behavior is unchanged (matches the
  OpenAPI spec, which already showed `sessionAuth` only on this
  route). Two regression tests added: a static check that fails if
  any route is registered more than once in `server.go`, and a live
  behavioral check that asserts `handleSession` runs outside the
  bearer middleware even when an Authenticator is wired.

### Added

- `api/openapi.yaml` — OpenAPI 3.0 spec for `pkg/controller`'s
  HTTP API. Covers every public route on the unified controller
  (run lifecycle, nodes, steps, events, triggers, approvals,
  concurrency, debug pauses, tokens, users, secrets, auth,
  agents, trends, pipelines) plus the mode-conditional pool
  (cluster) and artifacts (laptop) routes. Two security schemes
  (`Authorization: Bearer <token>` for service callers,
  `Authorization: Session <id>` for the dashboard's browser
  flow) wired to the operations that require auth. 26 component
  schemas mirror the `pkg/store` types. Wire protocol now has a
  formal contract; see VERSIONING.md.
- Checked-in API surface snapshots under `.apidiff/` for every
  covered public package (21 files: `sparkwing.txt`,
  `pkg_storage.txt`, `pkg_controller.txt`, ...). The new
  `cmd/apidiff` tool walks each package's AST and emits a
  deterministic text representation of the exported declarations
  (functions, types with exported fields, methods grouped under
  receivers, consts, vars) with godoc stripped. The `lint` pipeline
  now regenerates snapshots into a tempdir and diffs against the
  checked-in tree via `bin/check-api-snapshot.sh`; drift fails CI
  with an educational message explaining how to fix it. Developers
  refresh the baseline with `bash bin/regen-api-snapshot.sh` whenever
  they intentionally change a public API; the snapshot diff in the PR
  is the surface-change review artifact. See `VERSIONING.md`.

### Docs

- Curated godoc for the remaining `pkg/` packages: `pipelines`,
  `controller` (and its `client` + `pool` subpackages), `backends`,
  `runners`, `sources`, `localws`, `logs`, `store`, `runner`,
  `docs`, `color`. Each now has a `doc.go` overview with `[Type]`
  cross-references and `Example*` test functions where the package
  can be exercised in-process (storage round-trip, pipelines YAML
  parse, store sqlite open + run write, logs Server/Client via
  httptest, controller construction + Cipher wiring). `pkg/runner`,
  `pkg/docs`, `pkg/color`, `pkg/localws`, and `pkg/controller/pool`
  ship doc.go-only (no executable example) where the package surface
  is trivial or needs scaffolding the test runner can't provide.
  Matches the style established for `sparkwing/` and `pkg/storage/`.
- Removed three vestigial `sdk_doc.go` files
  (`pkg/store/`, `pkg/logs/`, `pkg/controller/client/`) whose
  contents claimed those packages were "not part of the stable
  public surface." Stale: the recent rewrite promoted all three to
  the covered surface in `VERSIONING.md`. Replaced by `doc.go`
  files describing the actual contract.
- Curated godoc for the `sparkwing/` author SDK and `pkg/storage/`
  public interfaces. Added `doc.go` package overviews to both
  (two-layer Plan / Work model, registration pattern, and modifier
  categories for `sparkwing/`; ArtifactStore / LogStore / StateStore
  contracts and implementation map for `pkg/storage/`). Added
  `Example*` test functions covering the primary use cases (single-
  job pipelines, typed cross-step Ref, blast-radius Risk, human
  approval gates, artifact store round-trip, log tail read).
  Top-tier types now use `[Type]` cross-reference links so `go doc`
  and pkg.go.dev render them as navigable. Establishes the
  documentation style applied across the remaining `pkg/` packages
  in subsequent commits.

### Added

- `sparkwing run release` now refuses to ship a version when
  `CHANGELOG.md` `[Unreleased]` has no entries. The PR-time CI gate
  (`bin/check-changelog.sh` in `sparkwing run lint`) catches missing
  entries at review time; this release-time fence catches them at
  ship time so a version cannot escape empty even if the CI gate
  was bypassed. Defense in depth.
- `VERSIONING.md` at the repo root. Defines the stability promise for
  `pkg/`, `sparkwing/`, CLI flags, wire protocols, and YAML config
  formats; spells out what counts as a breaking change; documents the
  deprecation procedure (godoc `// Deprecated:` + runtime warning +
  CHANGELOG entry + at-least-one-minor grace period); acknowledges
  the pre-1.0 caveat that minor bumps may still break per Go semver
  convention.
- CHANGELOG-required CI gate. `bin/check-changelog.sh` diffs the
  current commit / working tree against `origin/main` (or `BASE_REF`
  if set) and fails if any covered surface (`pkg/`, `sparkwing/`,
  `cmd/`) changed without a matching `[Unreleased]` entry in
  `CHANGELOG.md`. Excludes `_test.go`, `internal/`, `docs/`,
  `examples/`, and `testdata/`. Wired into `sparkwing run lint` so
  the existing fast-checks pipeline enforces it.

### Fixed

- Stale comments in `.sparkwing/jobs/release.go` and
  `.sparkwing/pipelines.yaml` that claimed the release pipeline
  validated a "CHANGELOG entry" (it didn't, until now) and that
  CHANGELOG.md was "no longer maintained" (it is). Docstrings,
  flag descriptions, examples, and the pipelines.yaml header now
  describe the current behavior: CHANGELOG.md carries adopter
  migration prose under `[Unreleased]`, validated at release time
  by the new `check-changelog` node; the GH-Actions workflow's
  `gh release create --generate-notes` is a separate commit-walk
  summary for the GitHub Release page.

### Removed

- `sparkwing.SetDebug` is no longer exported. It was a test-only
  helper with a single in-tree caller (the package's own
  `debug_test.go`); it now lives as an unexported `setDebug` in a
  `_test.go` file. Production code cannot flip the debug flag at
  runtime; `SPARKWING_DEBUG` at process start is the only supported
  toggle. Tests outside this module that called `sparkwing.SetDebug`
  will not compile after this release.

### Changed

- `gofmt -w` sweep across the repo. No semantic change; cleans up
  drift so future commits aren't noisy with unrelated formatting
  changes.
- `sparkwing.SetWorkDir` and `sparkwing.SetGit` godocs updated to
  reflect their real consumers. `SetWorkDir` stays exported (cross-
  package tests in `sparkwing/inputs/...` consume it); the doc now
  names the SDK-test role honestly rather than calling it
  "intended for tests". `SetGit`'s doc names the two
  orchestrator boot-time call sites that wire it.

### Added

- Promoted `orchestrator/store/` to `pkg/store/`. The persisted
  run/node/event data model is now explicitly part of the public
  SDK surface (stability promise): tooling on top of sparkwing
  (custom dashboards, analytics, audit pipelines, alternative
  controllers) can import the canonical types without reaching
  through `orchestrator/`.
- `pkg/runner` package with `runner.Main()`: the user-facing entry
  point that `.sparkwing/main.go` imports. Wraps `orchestrator.Main()`
  so internal changes to the orchestrator package don't propagate
  into user repos' `main.go`.

### Changed

- Moved `orchestrator/` to `internal/orchestrator/`. User repos
  that already migrated to `pkg/runner.Main()` (shipped in the
  previous release) see no change -- the shim's import updated
  transparently. User repos still importing
  `github.com/sparkwing-dev/sparkwing/orchestrator` directly MUST
  migrate to `github.com/sparkwing-dev/sparkwing/pkg/runner` and
  call `runner.Main()`. The soft-migration runway ends with this
  release.
- Moved `InProcessDispatcher` out of `pkg/controller` into
  `internal/inprocdispatch/`. `pkg/controller.Dispatcher` interface
  and `NoopDispatcher` stay public; the in-process implementation
  (which referenced `orchestrator.Backends` in its public field)
  is now private, since it has no production callers (only one
  full-loop test used it). External consumers wiring a controller
  use `NoopDispatcher` or their own `Dispatcher` implementation.
- `.sparkwing/main.go` template now imports
  `github.com/sparkwing-dev/sparkwing/pkg/runner` instead of
  `.../orchestrator`. Newly-generated user repos pick up the new
  path; existing user repos continue to work with the direct
  `orchestrator` import and can migrate at their leisure (or on the
  next regenerate / SDK bump).
- `pkg/controller.Server.WithSecretsCipher` now takes a
  `pkg/controller.Cipher` interface instead of a concrete
  `*secrets.Cipher`. Code that passes a `*secrets.Cipher`
  continues to work -- the concrete type satisfies the interface
  via Go's structural typing. External consumers can now supply
  custom cipher implementations without depending on
  sparkwing's secrets package.
- Moved `secrets/` to `internal/secrets/`. The AEAD cipher and
  helpers are no longer exported from this module; external
  consumers should implement `pkg/controller.Cipher` (two methods,
  `Seal` + `Open`) and pass that to `WithSecretsCipher`. Consumers
  using `secrets.Cipher` directly outside this repo will need to
  either migrate to implementing `pkg/controller.Cipher` or vendor
  a copy of the cipher.
- Promoted `logs/` to `pkg/logs/`. The HTTP logs service and its
  client are now explicitly part of the public API surface
  (stability promise); `pkg/storage/sparkwinglogs/` already
  depended on `logs.Client` / `logs.Server` types publicly, so this
  just makes the existing reality match the import path. Consumers
  outside this repo importing the old `logs/` path must update on
  next `go.mod` bump.
- Moved several packages to clarify the public/private boundary:
  - `logutil`, `bincache`, `otelutil`, `profile`, `repos` →
    `internal/` (implementation detail, no external stability
    promise).
  - `controller/client` → `pkg/controller/client` (intended-public
    HTTP client lib; stability promise).

  No public API change to any moved package; only the import path.
  Sibling repos that imported the moved top-level packages directly
  (sparks-core, moonborn-ws, okbot, moonborn-web, rangz-web) will
  break on next `go.mod` bump and need their imports updated.
- Collapsed `internal/local/` into `pkg/controller/`. The same
  control-plane code now serves both laptop and cluster modes; the
  mode is determined by which functional options the consumer sets
  (`AttachPool` for cluster; `WithArtifactStore` + `WithReconcileHook`
  for laptop). Eliminates ~30 duplicated files and the maintenance
  tax of keeping them in sync.

### Added

- `pkg/controller.Server` functional options `WithArtifactStore` and
  `WithReconcileHook`. Each unset option leaves the corresponding
  route or behavior unwired:
  - `WithArtifactStore` enables `GET /api/v1/artifacts/{key}`
    (laptop mode); cluster mode leaves the route unregistered so
    requests 404.
  - `WithReconcileHook` runs a sweep closure (laptop wires
    `orchestrator.ReconcileOrphanedLocalRuns`) before list-runs and
    get-run reads, eliminating stale "running" rows from crashed
    in-process orchestrators.
- Pool routes (`GET /api/v1/pool*`) are now registered only when
  `AttachPool` was called. Laptop mode leaves the routes
  unregistered.

### Removed

- `internal/local/` package. Laptop-mode control plane is now
  `pkg/controller/` configured via `WithArtifactStore` +
  `WithReconcileHook`.

### Fixed

- Cluster controller's retry response now returns the canonical
  shape (`{"status":"pending", "trigger_source":"retry", "started_at":<creation time>}`)
  matching the laptop controller. Prior cluster behavior used
  inconsistent field names (`trigger` vs `trigger_source`) and status
  values (`running` vs `pending`), so dashboards talking to a cluster
  controller had to special-case the response shape.

### Added

- Cluster controller now exposes `GET /api/v1/runs/{id}/attempts`
  (the retry-tree listing that powers the dashboard's Attempts
  dropdown) and supports the `?full=1` query parameter on
  `POST /api/v1/runs/{id}/retry` for the "rerun all" mode. Behavior
  matches the laptop controller's surface.

### Changed

- Cluster controller now pre-allocates the Run row in `pending`
  state before invoking a retry trigger, eliminating the window
  where the retry has been accepted but no row exists yet. Matches
  the laptop controller's pre-allocate pattern from
  `POST /api/v1/triggers`.

### Removed

- Dropped the `group:` field from `pipelines.yaml` entries and the
  matching `--group` flag on `sparkwing pipeline new`. The field had
  no backing on the `pipelines.Pipeline` struct, so strict YAML
  parsing rejected any file that used it, breaking
  `sparkwing pipeline list` and `sparkwing run` tab-complete. Strip
  `group:` lines from existing `.sparkwing/pipelines.yaml` files.
  Plan-DAG UI grouping (`Job.Group(...)`, `sw.GroupJobs`, `*Group`,
  `GroupSteps`) is a separate feature and is unaffected.

### Changed

- `WorkStep.Destructive()` / `.AffectsProduction()` / `.CostsMoney()` replaced
  by `.Risk("destructive")` / `.Risk("prod")` / `.Risk("money")`; labels are
  now author-defined (any kebab-case string works, e.g. `.Risk("rotates-key")`).
  Consumer repos using the old methods must update.
- `--sw-allow-destructive` / `--sw-allow-prod` / `--sw-allow-money` collapsed
  into one `--sw-allow LABEL[,LABEL...]` flag (repeatable; comma-separated
  allowed). Profile `auto_allow` is now a list of labels
  (`auto_allow: [destructive]`) instead of per-marker booleans.
- Renamed `--sw-change-directory` to `--sw-cd`. The `-C` short form is unchanged.
- Renamed `--sw-for` to `--sw-target`. The `Job.OnTarget("...")` author API is unchanged.
- Renamed `--sw-on` to `--sw-profile` (and its argument `NAME` to `PROFILE`).
- Renamed `--sw-from` to `--sw-ref` (and the env-var bridge `SPARKWING_FROM` to `SPARKWING_REF`).
- Tightened `--sw-*` flag descriptions in `--help`. No behavior change.
- Tightened `--sw-dry-run` description (no behavior change).
- Moved 15 orchestrator-only plumbing functions out of the sparkwing package
  into `internal/sparkwingruntime`. Pipeline authors never call these;
  relocation tightens the author-facing surface visible in IDE autocomplete
  and godoc. No behavior change.
- Moved Plan-layer plumbing (`GuardPlanTime`, `IsPlanTime`, `ValidateStepRange`,
  `SuggestClosest`, `PreviewPlan`) from `sparkwing` to
  `internal/sparkwingruntime`. Pipeline authors do not call these. No behavior
  change.
- Renamed `JobNode.OnTargetList()` to `JobNode.OnTargets()`. The setter
  `OnTarget(...)` is unchanged.
- Renamed `WorkStep.OnTargetList()` to `WorkStep.OnTargets()` for parity with
  the JobNode rename. The setter `WorkStep.OnTarget(...)` is unchanged.
- Moved pipeline-registration plumbing (`WithPipelineResolver`,
  `WithPipelineAwaiter`, `DescribeAll`, `DescribePipelineByName`) from the
  `sparkwing` package to `internal/sparkwingruntime`. Pipeline authors do not
  call these.
- Moved deep-plumbing functions (`WithInputs`, `WithPipelineSecrets`,
  `DecodePipelineConfig`, `ResolvePipelineSecrets`) from `sparkwing` to
  `internal/sparkwingruntime`. These functions only matter to code rebuilding
  the orchestrator; sparkwing retains `WithPipelineConfig`,
  `WithSecretResolver`, `ResolvePipelineConfig` as platform-extensibility
  primitives.
- Moved `WithLogger` and `WithNode` from `sparkwing` to
  `internal/sparkwingruntime`. Authors do not call these; `LoggerFromContext`
  and `NodeFromContext` remain in sparkwing for extensibility.
- Extracted 6 `sw:"..."` struct-tag reflection helpers (`parseSWTags`,
  `coerceAssign`, `toString`/`toBool`/`toInt64`/`toFloat64`) to a new
  `internal/swtags` package, used by both sparkwing and
  `internal/sparkwingruntime`. No behavior change, no public API change.

### Removed

- `JobNode.OnFailureNodeID()`. Use `OnFailureNode()` with a nil check.
- `JobNode.Dynamic()` and `JobNode.IsDynamic()`. Dynamic-node detection is via
  `Plan.IsDynamicNode(id)`, which auto-detects ExpandFrom sources.
- `sparkwing.ToKebabCase` (unused string utility) and `sparkwing.LookupInstance`
  (no callers anywhere). Consumer repos using either must inline their own
  equivalent.
- `sparkwing.Runtime()` — use `sparkwing.CurrentRuntime()` instead. The alias
  had no production callers.
- `sparkwing.WithJob`, `JobFromContext`, `JobStackFromContext` — zero callers,
  no integration anywhere. The nested-job breadcrumb design they were
  placeholding for was superseded by `Ref` / `RunAndAwait` / `SpawnNode`,
  which use different mechanisms. The `LogRecord.Job` and `LogRecord.JobStack`
  fields are also dropped; the JSON wire shape loses the (always-empty)
  `job` and `job_stack` fields.
- Retired `--sw-retry-of` and `--sw-full`; use `sparkwing runs retry RUN_ID [--failed | --all]`.
- Retired `--sw-job` and `--sw-prefer`; runner selection is now exclusively Plan-layer via `Job.Requires` / `Job.Prefers`. If you used these flags, declare the constraint in the pipeline instead.
- Retired `--sw-backends-env`. `backends.yaml` environment selection is now exclusively auto-detect — if it picks wrong, fix the `match:` rules in `backends.yaml` or the `DetectEnvironment` logic.
