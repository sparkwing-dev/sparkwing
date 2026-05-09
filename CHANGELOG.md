# Changelog

All notable changes to **sparkwing-sdk** are documented here. Format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [v0.2.1] - 2026-05-07

### Fixed
- **Fresh proxy snapshot of the v0.2.0 fix.** v0.2.0's proxy.golang.org
  snapshot was cached at the original commit (which had broken web/src
  JSX from a regex over-match in the OSS-fit audit pass) before the
  re-tagged fix landed; that snapshot is immutable per Go module
  rules. v0.2.1 re-tags the fixed commit so consumers doing
  `go get .../sparkwing@latest` get the working code. Source-level no-op
  vs v0.2.0 (origin); only the proxy snapshot differs. Validated
  end-to-end via the moonborn-ws build-test-deploy pipeline running
  through to a clean ECR image push + gitops state commit.

## [v0.2.0] - 2026-05-06

### Deprecated -- pre-launch artifacts in proxy.golang.org

The Go module proxy permanently caches every version that's ever
been fetched, even after the corresponding tag is deleted from the
repo. The following snapshots exist in proxy.golang.org from prior
to the project's public launch and **should not be used**: `v0.0.1`,
`v1.0.0`, `v1.1.0`, `v1.2.0`, `v1.2.1`, `v1.3.0`, `v1.3.1`, `v1.3.2`,
`v1.3.3`, `v1.3.4`, `v1.3.5`, `v1.4.0`, `v1.4.1`, `v1.4.2`, `v1.5.0`,
`v1.5.1`, `v1.5.2`, `v1.5.3`, `v1.5.4`, `v1.6.0`. The v1 line was a
misstep; the project is rebaselining on `v0.x.y`. These versions are
also retracted at the `go.mod` level so `go get @latest` and
`go mod tidy` will skip / warn against them.

### Added
- **Run receipt: identity hashes + per-step observability + simple cost.** `sparkwing runs receipt --run X` and
  `GET /api/v1/runs/{id}/receipt` emit a per-run audit + cost artifact
  separate from `runs status`. The receipt bundles
  `identity.{pipeline_version_hash, inputs_hash, plan_hash,
  outputs_hash}` (canonical-JSON sha256 over the compiled plan
  snapshot, the resolved Args, the DAG topology, and per-node typed
  outputs respectively), `steps[]` with per-node duration + outcome
  (`success | failed | skipped | cancelled`) + `skip_reason`, and a
  simple compute cost (`compute_cents` = runner-time × profile-rate,
  `currency: USD`, `rate_source` provenance string, `settled: false`
  until cloud-billing reconciliation lands as. A top-level
  `receipt_sha` certifies the receipt against the run state it
  summarizes. Receipts are recomputed on demand from runs+nodes; only
  four small queryable columns persist on the runs row
  (`receipt_sha`, `cost_cents`, `cost_currency`, `cost_settled`),
  added via `ensureColumns` so existing dev DBs migrate cleanly. The
  rate is set per-profile (`profile.cost_per_runner_hour`, default 0
  -> `compute_cents: 0`) and per-controller (`Server.WithCostRate`).
  Output is JSON-only today (`-o json` is the canonical view).

### Removed (BREAKING)
- **Unified DAG-builder grammar across Plan and Work layers.**
  Pipeline authors learn one builder pattern and apply it identically to
  both DAGs. Six coordinated changes ship as a single breaking-change
  shipment:

  a. `Workable.Work() *Work` -> `Workable.Work(w *Work) (*WorkStep, error)`.
     The orchestrator constructs the `*Work` and passes it in; authors
     stop calling `sw.NewWork()`. The returned `*WorkStep` becomes the
     Job's typed output (replaces the old `w.SetResult(...)`
     mechanism). Returning `nil` means the Job has no typed output.

  b. **Single `sw.Step` verb.** Drops `sw.Out[T]`, `sw.Result[T]`,
     `w.SetResult`, the `*TypedStep[T]` generic wrapper, and the
     method-form `w.Step`. Replacement: `sw.Step(w, id, fn) *WorkStep`
     where `fn` is either `func(ctx) error` or `func(ctx) (T, error)`
     (reflection at register time sets the step's `outType`).

  c. **`sw.StepGet[T any](ctx, step *WorkStep) T`** -- typed read inside
     another step's body, mirroring Plan's `Ref[T].Get(ctx)`. Used when
     composing values from multiple typed steps into a single returned
     result.

  d. **Verb renames for prefix consistency.** Plan layer:
     `sw.Approval` -> `sw.JobApproval`, `sw.Group` -> `sw.GroupJobs`.
     Work layer (free functions now): `w.SpawnNode` ->
     `sw.JobSpawn(w, ...)`, `w.SpawnNodeForEach` ->
     `sw.JobSpawnEach(w, ...)`. Every verb that adds a Plan Node is
     `Job*`-prefixed; tab-completing `Job` surfaces the full set
     regardless of caller layer.

  e. **`sw.GroupSteps(w, name, steps...) *StepGroup`** for named
     Work-layer clustering. Subsumes the old `w.Parallel(...)`'s fan-in
     role and adds a name the dashboard's Work view can render. Drops
     `w.Sequence(...)` (pure sugar over `.Needs(prev)`) and
     `w.Parallel(...)` (subsumed). `*StepGroup.Needs(...)` and
     `*StepGroup.SkipIf(...)` mirror `*WorkStep`'s existing modifiers;
     future step modifiers will be added in tandem.

  f. **`sw.Job(plan, id, X)` accepts `Workable` or `func(context.Context)
     error`.** Drops `sw.JobFn`. Trivial single-closure pipelines become
     `sw.Job(plan, "lint", p.run)` -- no struct, no wrapper.

  Migration recipe (mechanical):

  ```
  // Workable.Work signature
  func (j *J) Work() *sw.Work {                ->  func (j *J) Work(w *sw.Work) (*sw.WorkStep, error) {
      w := sw.NewWork()                              // (delete this line)
      sw.Result(w, "run", j.run)                     out := sw.Step(w, "run", j.run)
      return w                                       return out, nil
  }                                              }

  // Step verbs
  w.Step(id, fn)                                ->  sw.Step(w, id, fn)
  sw.Out(w, id, fn).Get(ctx)                    ->  sw.StepGet[T](ctx, sw.Step(w, id, fn))
  sw.Result(w, id, fn) + w.SetResult(...)       ->  sw.Step(w, id, fn) (return it from Work)

  // Plan-layer renames
  sw.Approval(plan, id, cfg)                    ->  sw.JobApproval(plan, id, cfg)
  sw.Group(plan, name, ...)                     ->  sw.GroupJobs(plan, name, ...)

  // Work-layer renames + free-function migration
  w.SpawnNode(id, job)                          ->  sw.JobSpawn(w, id, job)
  w.SpawnNodeForEach(items, fn)                 ->  sw.JobSpawnEach(w, items, fn)

  // Sugar drops
  w.Sequence(a, b, c)                           ->  b.Needs(a); c.Needs(b)
  w.Parallel(x, y)  // for fan-in               ->  // direct .Needs(x, y)
  w.Parallel(x, y)  // for UI cluster           ->  sw.GroupSteps(w, "name", x, y)

  // JobFn drop
  sw.Job(plan, id, sw.JobFn(fn))                ->  sw.Job(plan, id, fn)
  ```

  After migration the Work layer has 4 free-function verbs (`Step`,
  `JobSpawn`, `JobSpawnEach`, `GroupSteps`) plus `StepGet[T]` for typed
  reads. The Plan layer has 6 (`Job`, `JobFanOut`, `JobFanOutDynamic`,
  `JobApproval`, `GroupJobs`, plus `RefTo[T]` / `RefToLastRun[T]` for
  refs). Both layers read with identical grammar:
  `sw.<Verb>(<container>, ...args).<modifier>(...)`.

  Step-level modifier additions (Retry, Optional, hooks, Cache) are
  separate follow-up tickets.

## [v0.1.0] - 2026-05-03

### Added
- **`cmd/sparkwing-runner`** — the cluster runner agent. Connects
  outbound to a controller (your hosted SaaS or self-hosted enterprise)
  and executes pipelines on customer infrastructure.
- **`cmd/sparkwing-cache`** — binary cache service for compiled
  pipeline binaries + source archives. Self-hostable; customer
  typically runs it in their own region for fast cache hits.
- **`cmd/sparkwing-logs`** — log aggregation service. Self-hostable
  alongside cache.
- **`internal/cluster`** — runner-agent worker logic, trigger loop,
  pool agent CLI plumbing.
- **`internal/runners/{k8s,warmpool}`** — k8s pod dispatch and warm
  PVC pool runner implementations.
- **`logutil/`** — small logging helper used by the new binaries.

### Notes
- All new packages are marked "implementation, unstable" via doc.go
  conventions where applicable. User pipeline code does not import
  any of these.
- Module now requires Go 1.26 (transitive bump from k8s.io/client-go).
- The runner uses the **pull-based agent model** — outbound HTTPS
  only. Customers do not need to expose any inbound network surface.
  Documented in the architecture doc as a key product property.

## [v0.0.1] - 2026-05-03

Initial extraction from the sparkwing engine repo.

### Added
- `sparkwing/` package: stable user-facing DSL — `Plan`, `Job`, `Work`,
  `Step`, modifiers, `Bash`, `Path`, `Info`, `Secret`, `Register[T]`,
  `RunContext`, wire types (`TriggerInfo`, `Git`, `Outcome`, `LogRecord`,
  `DescribePipeline`, etc.). Subpackages: `inputs/`, `docker/`, `services/`,
  `git/`, `planguard/`.
- `orchestrator/` package: runtime that user pipeline binaries link.
  Exported as implementation; APIs may change without notice.
- `controller/client/` package: HTTP client for talking to a sparkwing
  controller. Implementation.
- `bincache/`, `logs/`, `otelutil/`, `profile/`, `repos/`, `secrets/`:
  leaf utility packages used by user binaries. Implementation.
