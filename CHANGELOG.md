# Changelog

## Unreleased

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
