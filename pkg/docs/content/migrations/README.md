# Migration guides

One guide per released version. The pre-release manicuring agent
generates the file from the breaking entries in `[Unreleased]` and
appends a row below at release time. Adopters jumping multiple
versions follow the guides in chronological order.

Format conventions live in [../changelog-style.md](../changelog-style.md).

## Releases

| Version | Date | Summary |
|---|---|---|
| [v0.4.0](v0.4.0.md) | 2026-05-20 | Author-SDK reshape (`*Node` → `*JobNode`, typed `Dep`/`WorkDep` for `Needs`, `CacheOptions` rename, spawn / risk APIs reshaped), package layout finalized (`orchestrator/` → `internal/`, `logs/` → `pkg/logs/`, `secrets/` → `internal/`, several others), CLI flag renames + retirements, `pipelines.yaml` `group:` field removed. Many breaking changes; each section in the linked guide is mechanical. |
