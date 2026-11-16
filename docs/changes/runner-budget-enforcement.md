### Fixed

- **cli + wingwire + api (Breaking):** `admission.budget: …,enforce` caps the local runner's jobs on Linux
  The local runner's systemd user service starts with `CPUQuota` at the budget's
  cores, `MemoryMax` when the budget names memory, and nice 10. Jobs the CLI hosts
  are niced on Linux too. `sparkwing queue list` now reads the limits back from the
  running runner and prints `OS-enforced: runner jobs capped at …` only when they
  are in force, or `not enforced (<reason>); admission cap only` otherwise. It
  previously printed "OS-enforced" from the setting alone while a 2-core budget let
  one job use five cores. A controller restart replaces a runner started under
  another budget. Queue JSON replaces `budget.enforce` with a `budget.enforcement`
  object (`applied`, `cpu_cores`, `memory_bytes`, `nice`, `reason`), and the plain
  `budget` row prints `enforced`, `not-enforced` or `-` in place of `true`/`false`.
  See the [migration guide](docs/migrations/_unreleased.md#queue-budget-reports-applied-enforcement).
