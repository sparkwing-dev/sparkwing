# Migrating to the next release

This guide covers breaking changes listed under `[Unreleased]`. At release,
move the sections into the versioned migration guide and update changelog links.

## `on.schedule` entries say where they fire

`on.schedule` is now a list of named entries, and every entry declares the
side that fires it.

- **Before:** one cadence per pipeline, written as a cron string or a mapping
  of `cron`, `tz`, `overlap` and `catch_up`. Where it fired was decided
  entirely by which hosts had run `sparkwing crons install`. In Go,
  `pipelines.Triggers.Schedule` was a `*pipelines.ScheduleTrigger`.

  ```yaml
  on:
    schedule:
      cron: "0 3 * * *"
      tz: America/Denver
  ```

- **After:** `on.schedule` takes a bare cron string, one mapping, or a list of
  mappings. Each entry adds `where` (required, `local` or `controller`), an
  optional `name`, and optional `args` keyed by CLI flag name.

  ```yaml
  on:
    schedule:
      - name: host
        cron: "0 3 * * *"
        tz: America/Denver
        where: local
      - name: cluster
        cron: "0 3 * * *"
        where: controller
        args:
          region: us-east
  ```

  In Go, `pipelines.Triggers.Schedule` is a `pipelines.ScheduleTriggers`, a
  slice of `pipelines.ScheduleTrigger`.

- **Migration:** every schedule already declared needs `where: local` added to
  keep firing from the host that arms it. That edit is deliberate: `where` has
  no default, so a config that does not say where a cadence fires is rejected
  at load rather than guessed at. A pipeline that declares more than one entry
  also needs a `name` on each, unique within the pipeline, matching
  `^[a-z0-9][a-z0-9-]*$` and at most 40 characters; a lone entry is named
  `default`.

  Go callers read `len(t.Schedule) > 0` where they read `t.Schedule != nil`,
  and range the slice where they read `t.Schedule.Cron`:

  ```go
  for i := range p.On.Schedule {
      entry := &p.On.Schedule[i]
      fmt.Println(entry.EffectiveName(), entry.Cron, entry.Where)
  }
  ```

  `pipelines.Triggers` now holds a slice, so it is no longer comparable with
  `==`; compare the fields that matter instead.

- **Why:** a cron runs unattended, so the config has to say which side runs it.
  One pipeline also has more than one useful cadence -- a quick sweep every
  quarter hour and a deep one nightly -- and naming the entries lets each carry
  its own arguments and be paused, resumed and run on its own.

## Runs-store schema 33: named, locked schedules

- **Before:** Schema 32 held one schedule per repository checkout and
  pipeline, keyed `UNIQUE(repo_path, pipeline)`. A schedule ran the cadence
  the repository declared, from whatever the checkout held at the moment the
  tick fired, with no arguments and no host-side edits.
- **After:** Schema 33 keeps several schedules for one pipeline and pins what
  they run. `cron_schedules` gains `schedule_name` (`default` for the lone
  schedule of a pipeline), `where_` (`local` or `controller`), `args` (a JSON
  object of CLI argument name to value), the lock -- `locked_ref`,
  `locked_binary` and `locked_digest`, all empty while the schedule follows
  the checkout -- and seven `override_*` columns holding this host's edit of
  the declaration together with the declaration it was set against. The
  unique key widens to `(repo_path, pipeline, schedule_name)`, which arrives
  as `idx_cron_schedules_repo_pipeline_name`: SQLite rebuilds the table to
  widen it, Postgres drops the old constraint and creates the index.
  `cron_schedules` also gains `git_branch`, the branch a schedule pushed to a
  controller was read from, empty for one a host armed from a working tree.
  `cron_fires` gains `args`, the arguments the launch was given.
- **Migration:** None to perform. The migration is additive: it declares no
  schema requirement, and every column it adds carries a default, so a binary
  built before it opens and writes the same database, seeing the rows it
  armed under the name `default`. Existing schedules and their fire history
  survive the widening intact.

## Armed schedules are pinned

`sparkwing crons install` now keeps the pipeline binary it compiled and runs
that file at every fire, instead of compiling the checkout each minute.

- **Before:** a schedule read the checkout at the moment it fired, so pulling a
  branch changed what ran that night.
- **After:** install records the compiled binary under `<sparkwing home>/crons/`
  along with the checkout's `HEAD`, and the fire executes it. Re-running install
  is the explicit update: it compiles again, replaces the binary and moves the
  recorded commit. `crons install --follow` and `crons unlock <name>` keep the
  old behaviour for a schedule; `crons lock <name>` pins one again.
- **Migration:** nothing breaks and nothing is pinned by the upgrade. A schedule
  armed by an earlier release keeps following the checkout until `sparkwing
  crons install` is run again on that host, which is the act that pins it.
  `sparkwing crons status` says how many schedules follow the checkout and how
  many are pinned, so a host can be checked without re-arming it.
- **Why:** a cron fires unattended. Someone updating a checkout during the day
  should not change what runs at three in the morning without saying so.

  The pin covers the pipeline and everything compiled into it. Scripts and
  binaries the pipeline executes from the checkout or from `PATH` are outside
  it, and stay whatever the machine holds when the run reaches them.
