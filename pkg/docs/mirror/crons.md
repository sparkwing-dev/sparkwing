# Local crons

Sparkwing runs a pipeline on a cadence from the machine you arm, with no
controller, no cluster, and no resident process of its own. A repository
declares the cadence; one host evaluates it.

## A schedule

A schedule is one of a pipeline's `on.schedule` entries, armed on one host. Two
things make one:

1. The repository declares the cadence in `.sparkwing/sparkwing.yaml`.
2. Someone runs `sparkwing crons install` on the host that should evaluate it.

Declaring is not arming. Another machine with the same checkout stays idle
until it is armed too, so two hosts never race for the same instant.

Each schedule carries an id -- `crn_` and twelve hex characters, derived from
the checkout path, the pipeline name, and the entry's name -- and a display
name of `<repo directory>/<pipeline>` for the entry named `default`, or
`<repo directory>/<pipeline>/<name>` for any other. Commands accept the id, the
display name, a `pipeline/name` pair, or a bare pipeline name that is unique
across the host.

An armed schedule carries three things beyond the cadence: what it runs (the
pinned pipeline binary, or the checkout when it follows one), this host's own
override of the declaration, and the arguments its launch passes.

## Declaring the cadence

One entry is a cron expression and the side that fires it. `where` is
required and has no default, so nothing fires somewhere the repository did not
say it should; `local` is the side a host arms:

```yaml
pipelines:
  - name: nightly-rebuild
    entrypoint: NightlyRebuild
    on:
      schedule:
        cron: "0 3 * * *"
        where: local
```

The same entry sets the zone and the policies:

```yaml
pipelines:
  - name: nightly-rebuild
    entrypoint: NightlyRebuild
    on:
      schedule:
        cron: "0 3 * * *"
        where: local
        tz: America/Denver
        overlap: queue
        catch_up: 6h
```

Every field, its accepted values, and its default are in
[scheduling.md](scheduling.md#schedule-triggers-cron). `tz: local` resolves to
the zone of whichever host evaluates the schedule, so a repository moved
between machines follows the machine.

### Several cadences on one pipeline

`on.schedule` takes a list, so one pipeline can carry more than one cadence.
Each entry then needs a `name`, unique within the pipeline, which becomes the
last segment of the schedule's display name and the way `pause`, `resume` and
`run` address it. `args` gives an entry its own argument values, keyed by CLI
flag name exactly as `args:` on the pipeline is, so two cadences of the same
pipeline run different work:

```yaml
pipelines:
  - name: sweep
    entrypoint: Sweep
    on:
      schedule:
        - name: quick
          cron: "*/15 * * * *"
          where: local
          args:
            depth: shallow
        - name: full
          cron: "0 4 * * *"
          where: local
          args:
            depth: deep
```

A schedule's args sit above the pipeline's own `args:` and below a host's
override, and the run executes with the merged set, so a `guards:` token like
`arg:depth=deep` reads them and each fire records what it ran with.

### Daylight saving

A schedule is walked in wall-clock time in its own zone, which settles the two
days a year that have no single answer:

- A minute that does not exist on the spring-forward day is skipped. `0 2 * * *`
  in `America/Denver` does not run on 2026-03-08, because 02:00 never happens
  that morning; the next run is 2026-03-09.
- A minute that occurs twice on the fall-back night runs once, at its first
  occurrence. A `*/15` schedule fires four times during that repeated hour, not
  eight.

## Arming a host

```sh
sparkwing crons install                     # the enclosing repo
sparkwing crons install --repo /path/to/repo
sparkwing crons install --only nightly,sweep/quick
sparkwing crons install --fleet             # every registered repo
```

Install arms the `where: local` entries of the repo. An entry declaring
`where: controller` is listed as the controller's and never stored here, so one
repo can carry both sides and each host takes only what it is asked to run.

`--only` arms a subset, naming pipelines or `pipeline/name` entries. A name the
repo does not declare fails the command before anything is written, so a typo
never half-arms a checkout.

### The pin

Install compiles each declared pipeline and requires the binary to name it
before arming the cadence, because a schedule fires unattended: a pipeline that
will not build is refused here rather than at three in the morning.

That compile is also the pin. The binary is copied to
`<sparkwing home>/crons/<schedule id>/pipeline` and recorded on the schedule
with the checkout's `HEAD` and the cache digest it was built from. Every fire
runs that file, so pulling, branching or editing the checkout afterwards does
not change what runs at 03:00. Re-running install is the explicit update: it
compiles again, replaces the pinned binary, and moves the recorded commit.

The pin covers the pipeline the repo declares and everything compiled into it.
Scripts and binaries the pipeline executes from the checkout or from `PATH` are
outside it: a step that runs `./scripts/deploy.sh` reads whatever that file
holds at the moment it fires, and one that runs `terraform` gets whichever
version is installed. Pin those the way you would for any other unattended job.

`--follow` arms without a pin, which is what v0.47.0 did: each fire compiles the
checkout as it stands that minute. `sparkwing crons unlock <name>` moves an
armed schedule to that behaviour, and `sparkwing crons lock <name>` pins it
again at the current checkout. `--no-prove` skips the compile, and so pins
nothing.

### What re-arming keeps

Re-running install republishes what the repo declares. A changed expression,
zone, overlap policy, catch-up window or argument set is stored; a pipeline that
stopped declaring a cadence is marked undeclared and stops firing while keeping
its history. Pause state, the cursor, the fire history and this host's
overrides survive, and an override is re-based onto the new declaration.

Only a config sparkwing could read withdraws a schedule. A checkout that has
moved, been deleted, or sits on a volume that is not mounted is reported in the
tick's errors and `sparkwing crons status`, and its rows are left armed --
being unable to read a repository is not a decision to stop scheduling it.

`sparkwing crons disarm <name>` removes one schedule, its history and its
pinned binary, leaving every sibling armed. `sparkwing crons uninstall` does
the same for a whole checkout, and when nothing is left armed anywhere, the OS
timer goes with it.

A build installed beside the released binary (`SPARKWING_INSTALL_NAME=sparkwing-crons
bash bin/install.sh`) can arm and tick on its own, but a scheduled run still
starts its admission daemon from the `sparkwing` on PATH, and a daemon from a
different build refuses the run. Set `SPARKWING_WINGD_BIN` to that build's path
when you run `install`; the unit carries it, so the runs it launches are hosted
by the same build.

## Overriding a schedule on one host

A repo declares one cadence for everyone who arms it. `crons set` lays this
host's own values over that declaration, for the expression, the zone, the
overlap policy, the catch-up window and the launch's arguments:

```sh
sparkwing crons set nightly --cron "0 5 * * *"
sparkwing crons set nightly --tz local
sparkwing crons set sweep/quick --arg depth=deep --arg dry-run=true
sparkwing crons reset nightly          # run what the repo declares again
```

Each `set` keeps what an earlier one said and replaces only the fields it
names. `--arg` is the exception: it replaces the declared argument set whole, so
name every argument the schedule should launch with.

An override that would not evaluate -- an unparseable expression, a zone this
host cannot load, an overlap policy that is neither `skip` nor `queue` -- is
refused when it is set, and the schedule keeps running what it was.

`crons list` marks an overridden expression with `*`, and `crons show` prints
the declared value, the override and the effective value side by side. An
override is **stale** once the repo changes the declaration it was set against:
it still applies, and both `list` and `show` say so, because a cadence someone
chose against `0 3 * * *` may mean nothing against `*/5 * * * *`. Re-running
install re-bases it, which is the host acknowledging the new declaration.

## Drift

A locked schedule does not read the working tree: its declaration is what was
armed with it. What the checkout has done since is derived when you ask, and
reaches you as the `LOCK` column of `crons list`:

| Cell | What it means |
| --- | --- |
| `abc1234` | Pinned at that commit, and the checkout is on it and clean. |
| `abc1234 ahead` | The checkout has newer commits the pin does not carry. |
| `abc1234 dirty` | The checkout has uncommitted edits the pin does not carry. |
| `follows` | No pin: every fire compiles the checkout. |
| `missing` | The pinned binary is gone, so the schedule fires nothing. |

Drift is never an error on its own -- a pin is doing its job by ignoring the
checkout -- and `sparkwing crons install` resolves every case by pinning the
checkout as it stands. A `missing` pin is different: its fires are recorded as
failed with a reason naming `sparkwing crons install`, because there is nothing
to run and the checkout is exactly what the pin exists to ignore.

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

The launch passes the schedule's effective arguments after the pipeline name,
exactly as `sparkwing run <pipeline> --key value` would, so they merge over the
repo's `defaults.args` and the pipeline's own `args:` the same way and a
`guards: {require: [arg:depth=deep]}` token reads them. Each fire records the
arguments it launched with, and `crons show` prints them. A locked schedule
hands the consumer its pinned binary, which the consumer executes instead of
compiling the checkout, with the checkout as the working directory.

Run the tick by hand on a platform sparkwing has no timer for, from that
machine's own scheduler, once a minute:

```sh
sparkwing crons tick
```

It is quiet on success -- one summary line and the id of each run it
launched -- and exits non-zero only when the tick itself could not run.

## Inspecting

```sh
sparkwing crons list          # what is armed, what it runs, when it next fires
sparkwing crons show nightly-rebuild   # declaration, override, effective, lock, fires
sparkwing crons next          # the next instants across every armed schedule
sparkwing crons next nightly-rebuild --count 10
sparkwing crons status        # the timer, the last tick, the counts
```

Every verb takes a schedule id, a `<repo>/<pipeline>[/<name>]` display name, a
`pipeline/name` pair, or a bare pipeline name unique across this host. An
ambiguous name lists the candidates instead of guessing.

`crons next` is the cheapest way to check that an expression means what it
looks like -- a day-of-week field, a daylight-saving boundary, a zone that is
not yours. It walks the same evaluator the tick uses.

`crons show` prints the declared cadence, this host's override and the
effective values side by side, then the lock and what the checkout has done
since, then each resolved instant: what the tick decided, the arguments it
launched with, the run, and that run's current status.

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

It also counts what is pinned, what follows the checkout, how many pins the
checkout has moved past, and how many overrides were set against a declaration
that has since changed. None of those makes the host unhealthy, and every one
of them is answered by re-running `sparkwing crons install`, which is the
sentence status prints when any is non-zero.

## Related

- [scheduling.md](scheduling.md) -- the `on.schedule` fields, and how a run
  reaches a runner once it is launched.
- [cli-crons.md](cli-crons.md) -- every flag and argument.
- [local-execution.md](local-execution.md) -- what the detached path a
  scheduled run uses does with the local store.
