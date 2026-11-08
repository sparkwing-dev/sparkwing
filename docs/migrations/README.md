# Migration guides

One guide per release that contains a breaking change; releases with
no breaking changes have no guide. Write each section in
[_unreleased.md](_unreleased.md) as the breaking change lands, and link
the changelog entry to its anchor there. `sparkwing run release` rolls
that file to `vX.Y.Z.md` when it cuts the tag, writes a fresh
`_unreleased.md`, and adds the row below. Guides marked Before are historical
snapshots. Start with the latest guide and the unreleased changes for the
current CLI.

Format conventions live in `CHANGELOG-STYLE.md` at the repository root.

## Releases

| Version | Date | Summary |
|---|---|---|
| [v0.70.0](v0.70.0.md) | 2026-11-08 | This release moves command execution to host helpers, updates controller commands, and upgrades the store to schema 34. |
| [v0.69.0](v0.69.0.md) | 2026-11-05 | This release upgrades the store to schema baseline 30 in place, and removes the retired `warm_cpu_class_cores` credit setting. |
| [v0.68.7](v0.68.7.md) | 2026-11-04 | The schema baseline advances to version 29, the first forward migration: a store at schema 28 with the v0.68.3 baseline digest (v0.68.3 and later 0.68 releases) upgrades in place on its first open The upgrade adds `nodes.trusted_host` and records version 29 and its digest in one transaction on SQLite and PostgreSQL; on PostgreSQL the migration advisory lock lets controllers starting together run it once; `--sw-run-handle-file` writes an identity-only handle without `status` The handle recorded `"status":"pending"` at intake and kept it after the run finished, so a reader polling it saw a run that never ended; `--sw-run-handle-file` writes an identity-only handle without `status` The handle recorded `"status":"pending"` at intake and kept it after the run finished, so a reader polling it saw a run that never ended. |
| [v0.68.3](v0.68.3.md) | 2026-11-02 | Shared Cloud pools replace hand-made per-team NodePools Replace per-team pool manifests with one pool per hardware class and the generic dedicated taint; Confine warm placeholder authority to a dedicated namespace Move placeholders and their Pod get/delete and ReplicaSet get Role to `launcher.warmNamespace` (default `<release namespace>-cloud-warm`); Replace per-team Karpenter NodePools with one shared pool per requested hardware class and deployment Nodes receive their permanent team/run binding at first reservation and never move between runs; Prepare the pipeline and fetch its source once per run Only Cloud-awarded planning owners publish source and SDK installation snapshots for jobs of that run; List endpoints that returned whole collections now return cursor pages of 50 records by default, up to 200 per request Follow `next_cursor` while `truncated` is true; Each team can create 20 Stripe Checkout Sessions per rolling hour across Add funds and saved-card setup Idempotent retries count once; The schema baseline advances to version 28 in v0.68.3 as a store reset Before 1.0, schema changes may require moving the local store aside or recreating a server database; Sizing and capacity responses drop stored resource pins, demand floors, queue waits, contention counters and drift Update API clients using the [capacity-profile migration guide](v0.68.3.md#capacity-profiles-report-measured-data); Sparkwing releases use the native tag-and-publication flow Platform deployment is separate; Personal machines and here hosts never claim repository-proven runs, including run-now and burst `machines`; Job credentials require an intake GitHub compare check; Latest outputs default to verified default-branch runs and exclude publication; Teams have no member cap Remove `free_team_members` from operator signup settings, invitation `warning` handling and the `team_full` refusal reason; Accounts can create teams without a lifetime creation cap Deleting or leaving a team does not reduce future team-creation access; Removed the version-aware takeover ladder Local startup attempts one replacement; Removed installation proofs, fleet modes, automatic global-hook chaining and doctor's fleet survey `--no-prove`, `--all-repos` and `--ungated` are gone; Removed commit-status reporting Runs report through Checks only; Removed `internal/guardcheck`, its `.guards/` allow-lists and standalone gate steps Dependency rules run through depguard in `.golangci.yml`, including the fast push tier; Removed binary-cache checkout owners, use counts, shared-entry counts and `related_entries` history `cache explain` retains the current build key and input digests; Removed the $100 admin grant guard and `--allow-large` `sparkwing-controller admin grant` accepts any positive amount the credit ledger can hold; Removed `SPARKS_GO_BIN` Select the Go executable through `PATH` for sparks resolution and overlay updates; Removed `--live-log-node-kb`, `--live-log-total-mb`, `--live-log-max-nodes` and `--live-log-idle` Live buffers retain fixed limits of 512 KiB per node, 64 MiB total, 1,024 nodes and a 10-minute idle timeout. |
| [v0.68.0](v0.68.0.md) | 2026-10-30 | v0.68 rewrites the engine, the store and the trust model. |
| [v0.67.0](v0.67.0.md) | 2026-10-12 | CLI, source selection and environment configuration changes. |
| [v0.66.0](v0.66.0.md) | 2026-10-01 | Historical release changes. Follow the latest migration for the current CLI. |
| [v0.65.0](v0.65.0.md) | 2026-09-29 | Historical release changes. Follow the latest migration for the current CLI. |
| [v0.62.0](v0.62.0.md) | 2026-09-27 | Historical release changes. Follow the latest migration for the current CLI. |
| [v0.61.0](v0.61.0.md) | 2026-09-26 | Historical release changes. Follow the latest migration for the current CLI. |
| [v0.57.0](v0.57.0.md) | 2026-09-20 | Historical release changes. Follow the latest migration for the current CLI. |
| [v0.52.3](v0.52.3.md) | 2026-09-15 | Historical release changes. Follow the latest migration for the current CLI. |
| [v0.50.5](v0.50.5.md) | 2026-09-14 | Historical release changes. Follow the latest migration for the current CLI. |
| [v0.50.4](v0.50.4.md) | 2026-09-14 | Historical release changes. Follow the latest migration for the current CLI. |
| [v0.50.2](v0.50.2.md) | 2026-09-14 | Historical release changes. Follow the latest migration for the current CLI. |
| [v0.50.1](v0.50.1.md) | 2026-09-13 | Historical release changes. Follow the latest migration for the current CLI. |
| [v0.50.0](v0.50.0.md) | 2026-09-12 | Historical release changes. Follow the latest migration for the current CLI. |
| [v0.49.0](v0.49.0.md) | 2026-09-10 | Historical release changes. Follow the latest migration for the current CLI. |
| [v0.48.1](v0.48.1.md) | 2026-09-09 | Historical release changes. Follow the latest migration for the current CLI. |
| [v0.48.0](v0.48.0.md) | 2026-09-09 | Historical release changes. Follow the latest migration for the current CLI. |
| [v0.47.0](v0.47.0.md) | 2026-09-09 | Historical release changes. Follow the latest migration for the current CLI. |
| [v0.41.0](v0.41.0.md) | 2026-09-04 | Historical release changes. Follow the latest migration for the current CLI. |
| [v0.46.0](v0.46.0.md) | 2026-09-08 | Historical release changes. Follow the latest migration for the current CLI. |
| [v0.37.3](v0.37.3.md) | 2026-08-30 | Historical release changes. Follow the latest migration for the current CLI. |
| [v0.37.0](v0.37.0.md) | 2026-08-28 | Historical release changes. Follow the latest migration for the current CLI. |
| [v0.4.0](v0.4.0.md) | 2026-05-20 | Historical release changes. Follow the latest migration for the current CLI. |
| [v0.5.0](v0.5.0.md) | 2026-05-28 | Historical release changes. Follow the latest migration for the current CLI. |
| [v0.6.0](v0.6.0.md) | 2026-05-29 | Historical release changes. Follow the latest migration for the current CLI. |
| [v0.9.0](v0.9.0.md) | 2026-06-09 | Historical release changes. Follow the latest migration for the current CLI. |
| [v0.10.0](v0.10.0.md) | 2026-06-14 | Historical release changes. Follow the latest migration for the current CLI. |
| [v0.11.0](v0.11.0.md) | 2026-06-17 | Historical release changes. Follow the latest migration for the current CLI. |
| [v0.15.4](v0.15.4.md) | 2026-07-10 | Historical release changes. Follow the latest migration for the current CLI. |
| [v0.15.5](v0.15.5.md) | 2026-07-10 | Historical release changes. Follow the latest migration for the current CLI. |
| [v0.16.0](v0.16.0.md) | 2026-07-12 | Historical release changes. Follow the latest migration for the current CLI. |
| [v0.17.0](v0.17.0.md) | 2026-07-13 | Historical release changes. Follow the latest migration for the current CLI. |
| [v0.23.0](v0.23.0.md) | 2026-08-08 | Historical release changes. Follow the latest migration for the current CLI. |
| [v0.35.0](v0.35.0.md) | 2026-08-24 | Historical release changes. Follow the latest migration for the current CLI. |
