# Migration guides

One guide per release that contains a breaking change; releases with
no breaking changes have no guide. The pre-release manicuring agent
generates the file from the breaking entries in `[Unreleased]` and
adds its row below. Adopters jumping multiple versions follow the
guides in ascending version order.

Format conventions live in [../changelog-style.md](../changelog-style.md).

## Releases

| Version | Date | Summary |
|---|---|---|
| [v0.41.0](v0.41.0.md) | 2026-09-04 | Fleet authority and compatibility advance the runs store from schema 23 to 31; controller, execution, and local-process trust boundaries tighten. |
| [v0.37.3](v0.37.3.md) | 2026-08-30 | Login-required dashboards require a controller session backend and enforce same-origin CSRF and live session revocation. |
| [v0.37.0](v0.37.0.md) | 2026-08-28 | Authenticated dashboards require browser sessions for their proxy, keep controller service credentials server-side, and direct bearer-token automation to the controller API. |
| [v0.4.0](v0.4.0.md) | 2026-05-20 | Author-SDK reshape (`*Node` → `*JobNode`, typed `Dep`/`WorkDep` for `Needs`, `CacheOptions` rename, spawn / risk APIs reshaped), package layout finalized (`orchestrator/` → `internal/`, `logs/` → `pkg/logs/`, `secrets/` → `internal/`, several others), CLI flag renames + retirements, `--json` / `--pretty` aliases dropped in favor of canonical `-o`/`--output`, `pipelines.yaml` `group:` field removed. Many breaking changes; each section in the linked guide is mechanical. |
| [v0.5.0](v0.5.0.md) | 2026-05-28 | Config collapses to two files and two flags: the five `.sparkwing/` files (`pipelines.yaml`, `backends.yaml`, `runners.yaml`, `sources.yaml`, `sparks.yaml`) become one `.sparkwing/sparkwing.yaml`, profiles absorb every backend spec, `--profile` becomes the only "where" flag, and `sparkwing pipeline trigger` takes over remote execution. Hard cut, no shims. |
| [v0.6.0](v0.6.0.md) | 2026-05-29 | One pipeline binds one deployment shape: `targets:`, `--target`, `sparkwing.Target(ctx)` and `OnTarget(...)` are removed in favor of multiple pipelines sharing an entrypoint. Backends become one uniform `backends.Surfaces` bundle (secrets is now a fourth surface, `pkg/sources` is gone) and profiles are named bundles selected wholesale by `--profile`. |
| [v0.9.0](v0.9.0.md) | 2026-06-09 | `.Cache()` splits into two primitives: `Cache` for content-addressed result memoization and `Concurrency` for a named shared budget. The old overloaded `CacheOptions.Namespace` meant both at once, which made throttled nodes replay each other's results. The signature change is a compile error at every call site. |
| [v0.10.0](v0.10.0.md) | 2026-06-14 | No functional change over v0.9.3; it versions the runs-store schema 3 → 4 change correctly (the `sparkwing_meta` table shipped as a patch in v0.9.2). The store migrates automatically on open; the break is version skew, so read on only if a module of yours pins sparkwing. |
| [v0.11.0](v0.11.0.md) | 2026-06-17 | Node artifacts land: content-addressed file edges between nodes. The runs-store schema moves 4 → 5 to add the `nodes.artifact_manifest` column (auto-applied on open, and it repairs databases where the column went missing), and authors of a custom `pkg/storage.StateStore` backend add one method. |
| [v0.15.4](v0.15.4.md) | 2026-07-10 | `sparkwing.ConcurrencyLimit` and `client.TriggerPlanAdmission` gain host-admission fields, so positional struct literals must become keyed literals. Plan-level budgets compose rather than replace. |
| [v0.15.5](v0.15.5.md) | 2026-07-10 | Runs-store schema moves 5 → 6, adding `concurrency_holders.queue_arrived_at` so admission state can report when a promoted holder entered the queue. Migration is automatic on open; upgrade every binary sharing the state database together. `store.ConcurrencyHolder` and `client.WaiterHolder` need keyed literals. |
| [v0.16.0](v0.16.0.md) | 2026-07-12 | Local concurrency is rebuilt around the per-host admission daemon `sparkwingd`, which measures the machine and owns host admission and the queue with no heartbeats or leases. The box-slot semaphore, store-side local admission slots, `HostAdmission` in the SDK, the trigger API's `plan_admission`, and the destructive verbs are removed; schema moves to 10. No daemonless fallback. |
| [v0.17.0](v0.17.0.md) | 2026-07-13 | Honest capacity measurement under contention advances the runs-store schema 10 → 11, adding five additive columns to `pipeline_profiles` (`plan_hash`, the demand floor, and the predecessor plan's peak). Migration is one-way, so upgrade every pinned binary on a machine in one sitting. |
| [v0.23.0](v0.23.0.md) | 2026-08-08 | The sparks-core registry moves off the pipeline-creation path: `pipeline templates` becomes `sparkwing examples`, and `pipeline new --template` now takes a pipeline shape instead of a registry entry. `sparkwing commands` prints a one-line index by default; `-o json` returns the full records. |
| [v0.35.0](v0.35.0.md) | 2026-08-24 | Learned CPU charges price sustained demand instead of the burst peak; the runs-store schema moves 13 → 14 with two additive backfilled columns on `pipeline_profiles`. Migration is automatic on open; upgrade every binary sharing the state database together. |
