# Local crons

Sparkwing runs a pipeline on a cadence from the machine you arm, with no
controller, no cluster, and no resident process of its own. A repository
declares the cadence; one host evaluates it.

## A schedule

A schedule is one pipeline's `on.schedule` cadence, armed on one host. Two
things make one:

1. The repository declares the cadence in `.sparkwing/sparkwing.yaml`.
2. Someone runs `sparkwing crons install` on the host that should evaluate it.

Declaring is not arming. Another machine with the same checkout stays idle
until it is armed too, so two hosts never race for the same instant.

Each schedule carries an id -- `crn_` and twelve hex characters, derived from
the checkout path and the pipeline name -- and a display name of
`<repo directory>/<pipeline>`. Commands accept any of the three: the id, the
display name, or a bare pipeline name that is unique across the host.

## Declaring the cadence

The short form is a five-field cron expression, read in UTC:

```yaml
pipelines:
  - name: nightly-rebuild
    entrypoint: NightlyRebuild
    on:
      schedule: "0 3 * * *"
```

The long form sets the zone and the policies:

```yaml
pipelines:
  - name: nightly-rebuild
    entrypoint: NightlyRebuild
    on:
      schedule:
        cron: "0 3 * * *"
        tz: America/Denver
        overlap: queue
        catch_up: 6h
```

Every field, its accepted values, and its default are in
[scheduling.md](scheduling.md#schedule-triggers-cron). `tz: local` resolves to
the zone of whichever host evaluates the schedule, so a repository moved
between machines follows the machine.

## Arming a host

```sh
sparkwing crons install                     # the enclosing repo
sparkwing crons install --repo /path/to/repo
sparkwing crons install --fleet             # every registered repo
```

Install compiles each declared pipeline and requires the binary to name it
before arming the cadence, because a schedule fires unattended: a pipeline
that will not build is refused here rather than at three in the morning.
`--no-prove` arms without that check.

Re-running install republishes what the repository declares. A changed
expression, zone, overlap policy, or catch-up window is stored; a pipeline
that stopped declaring a cadence is marked undeclared and stops firing while
keeping its history. Pause state and the cursor survive, so re-arming does not
replay anything.

`sparkwing crons uninstall` disarms a checkout and drops its history. When
nothing is left armed anywhere, the OS timer goes with it.

## The tick model

Install writes one OS timer for the whole host -- a systemd user timer on
Linux, a launchd agent on macOS -- that runs `sparkwing crons tick` every
minute. One timer serves every armed schedule, because sparkwing evaluates
the cron expressions itself rather than translating them into a service
manager's own calendar syntax.

Each tick takes an exclusive lock on `crons.lock` under the sparkwing home, so
two ticks never resolve the same instant. It re-reads what the armed
repositories declare, then evaluates every declared unpaused schedule against
its cursor -- the last due instant it resolved -- and resolves each one:

- **fired**: the instant is inside the catch-up window and the run is
  launched.
- **skipped_overlap**: the previous scheduled run is still going and the
  policy is `skip`. Under `queue` the run launches anyway and admission orders
  the two.
- **missed**: the instant fell outside the catch-up window, which happens when
  the host was asleep or off. A backlog is recorded as one row naming how many
  instants it covers, not one row per instant.
- **failed**: the launch itself did not happen. The schedule records why and
  the tick carries on to the others.

Every outcome moves the cursor, so an instant is considered exactly once. A
schedule that fails, is skipped, or is paused still fires on time at its next
instant.

A scheduled run goes through the same path as `sparkwing run --sw-detached`:
it is persisted against this home's runs store and executed by the resident
consumer. It carries the trigger source `schedule`, which a pipeline can
branch on through `RunContext.Trigger.Source`, and the schedule's id, so the
run traces back to the cadence that asked for it.

Run the tick by hand on a platform sparkwing has no timer for, from that
machine's own scheduler, once a minute:

```sh
sparkwing crons tick
```

It is quiet on success -- one summary line and the id of each run it
launched -- and exits non-zero only when the tick itself could not run.

## Inspecting

```sh
sparkwing crons list          # what is armed, when it next fires, how it last went
sparkwing crons show nightly-rebuild   # every field, plus recent fires and their runs
sparkwing crons next          # the next instants across every armed schedule
sparkwing crons next nightly-rebuild --count 10
sparkwing crons status        # the timer, the last tick, the counts
```

`crons next` is the cheapest way to check that an expression means what it
looks like -- a day-of-week field, a daylight-saving boundary, a zone that is
not yours. It walks the same evaluator the tick uses.

`crons show` names each resolved instant, what the tick decided, the run it
launched, and that run's current status.

Piping any of them yields NDJSON, one record per line.

## Pausing and running by hand

```sh
sparkwing crons pause nightly-rebuild
sparkwing crons resume nightly-rebuild
sparkwing crons run nightly-rebuild
```

A paused schedule still advances its cursor on every tick, so resuming fires
the next due instant rather than replaying the ones that passed. `crons run`
launches immediately whatever the cadence says, records the launch as a manual
fire, and leaves the cursor alone, so the next scheduled instant still fires
on time.

## The log

The timer appends the tick's own output to `crons.log` under the sparkwing
home (`~/.sparkwing/crons.log` by default). That is where a tick that could
not open the store, could not take the lock, or could not start a consumer
says so. Everything a tick decided about a schedule is in the store instead,
readable with `sparkwing crons show`.

## When status says stale

`sparkwing crons status` calls the tick stale when no tick has landed in the
last few minutes on a host that should be ticking. It exits non-zero whenever
schedules are armed and the host is not evaluating them, so a check script can
read the exit code. A recent tick is the evidence either way, so a host
driving the tick from its own scheduler passes without a sparkwing timer. What
to do depends on what it reports:

- **timer not installed**: run `sparkwing crons install` on this host.
- **installed, not running**: the service manager has the files but is not
  firing them. On Linux, `systemctl --user status sparkwing-crons.timer` says
  why; a user without a login session needs `loginctl enable-linger` for its
  timers to run while logged out.
- **it runs another sparkwing**: the binary moved, usually after an upgrade
  that installed elsewhere. `sparkwing crons install` repoints the unit.
- **stale with the timer enabled**: the tick is firing and failing. Read
  `crons.log`.
- **foreign**: a file sparkwing did not write sits at the unit path. Sparkwing
  never overwrites or removes one; move it aside and install again.

## Related

- [scheduling.md](scheduling.md) -- the `on.schedule` fields, and how a run
  reaches a runner once it is launched.
- [cli-crons.md](cli-crons.md) -- every flag and argument.
- [local-execution.md](local-execution.md) -- what the detached path a
  scheduled run uses does with the local store.
