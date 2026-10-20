# Schedules

A schedule starts a pipeline on a cadence. Its ID stays separate from the IDs
of the runs it starts. A repository declares the cadence, arguments and where
it fires. Installing that declaration arms it on a selected controller.

Use `--profile NAME` for a saved controller connection. Omit it to use the
local controller. Reads, pause/resume, cadence edits and immediate runs share
the same HTTP contract on both connections.

## Inspect schedules

```sh
sparkwing schedules list
sparkwing schedules list --profile production --all -o json
sparkwing schedules show --schedule check/nightly --fires 10
sparkwing schedules list --next 5
sparkwing schedules show --schedule check/nightly --next 3
sparkwing schedules list --timer
```

Selectors accept a schedule ID, full display name, `pipeline/name`, or an
unambiguous pipeline name. Ambiguous selectors fail on the controller. Repeat
`--schedule`, or use a comma list, to select several entries. Every selector
resolves before the first mutation. Aliases resolving to the same ID act once.
A later mutation failure can leave earlier mutations applied.

List JSON emits one selection record per line, including cadence, state, pin
identity and next/last-fire observations. Show JSON includes its declaration,
override, effective cadence, source pin and fire history in one object per
selected schedule. `--fires 0` requests no history. `--next N` requires a
positive count and shows the controller's upcoming instants. It does not
calculate times against the CLI machine's clock or time-zone data.

`--all`, `--timer` and `--next` select separate list reports. `show --next`
and `show --fires` also select separate reports. An unhealthy timer report
still prints its diagnosis and exits with failure. Paused schedules alone do
not require an active timer.

## Pause, edit and run

```sh
sparkwing schedules pause --schedule check/nightly
sparkwing schedules resume --schedule check/nightly
sparkwing schedules set --schedule check/nightly --cron '0 4 * * *' --tz America/Denver
sparkwing schedules set --schedule check/nightly --arg region=us-east,empty=
sparkwing schedules set --schedule check/nightly --catch-up 0s
sparkwing schedules set --schedule check/nightly --reset
sparkwing schedules run --schedule check/nightly --profile production
sparkwing schedules uninstall --schedule check/nightly
```

Pause keeps the declaration and history. Resume enables it again. An immediate
run starts separately from the cadence. Its request keeps one idempotency key
across transport retries, and JSON retains the run ID and replay result.

Overrides preserve field presence. An omitted field leaves the repository's
value in control. `--tz=` selects the default zone explicitly, `--catch-up 0s`
keeps zero, and `--arg key=` keeps an empty argument value. An argument value
may contain `=`. Duplicate argument names fail. Arguments replace the declared
argument map. `--arg=` alone replaces it with an empty map. `--reset` drops the override. Pin, unpin and reset each require
a separate invocation from cadence edits.

Only an unmodified fire speaks for the schedule: its OIDC token claims
`trigger:cron` with the schedule's ref and commit, and it shares a cache scope
with the schedule's other fires. An immediate `run` speaks for the principal who
asked, and a fire whose arguments are overridden speaks only for itself; both
claim `trigger:manual` with no `ref` or `sha` and write their own cache scope.
See [OIDC](oidc.md#what-each-value-proves).

Mutation JSON reports the confirmed result. Plain output emits schedule IDs,
or the new run ID for `run`. A deletion
result distinguishes removal from an already absent row.

## Declaration and installation contract

<!-- design:begin -->

A project declares schedules as a list under `on.schedule`:

```yaml
pipelines:
  - name: check
    entrypoint: Check
    on:
      schedule:
        - name: nightly
          cron: '0 3 * * *'
          where: local
          tz: America/Denver
          overlap: skip
          catch_up: 1h
          args: {region: us-east}
```

`where` selects `local` or `controller`. Catch-up values pass the shared
settings and ceiling checks. Values above a ceiling fail with both values
named. They are never silently lowered.

Installing without `--schedule` reconciles the repository's complete declaration
set and withdraws entries removed from the project file. Installing with
`--schedule` updates only the selected entries. It preserves other schedules,
their pins, overrides and pause state. A selection that runs elsewhere makes no
declaration changes on the selected controller.

Local installation records the selected declarations and captured environment
identity durably. Proofs and later pin changes use that captured environment.
Environment values stay in private host storage and do not appear in schedule
views. Installation pins a proved binary unless `--unpinned` is selected. Linux and macOS managed timers are
intended to invoke the stable hidden `sparkwing __tick` entrypoint and start the
local controller on demand. Windows schedules are intended to run through that
controller. Managed-unit refresh and native timer integration remain part of
the rewrite. Reinstall units that still invoke the removed `crons` command.

<!-- design:end -->

See [CLI schedules](cli-schedules.md) for the generated flag reference and
[Scheduling](scheduling.md) for runner placement.

Controller fires seal the repository, ref and effective arguments before they
enter the run queue. Repeating the same due instant returns the original run.
App-managed schedules require a live installation and repository automation
consent. A scheduled run does not carry verified-push or default-branch trust.
