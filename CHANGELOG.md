# Changelog

## Unreleased

### Added

- `pkg/runner` package with `runner.Main()`: the user-facing entry
  point that `.sparkwing/main.go` imports. Wraps `orchestrator.Main()`
  so internal changes to the orchestrator package don't propagate
  into user repos' `main.go`.

### Changed

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
