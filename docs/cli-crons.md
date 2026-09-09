<!-- GENERATED from the CLI command registry by `sparkwing commands --format markdown --output plain`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference: sparkwing crons

Every `sparkwing crons` command, flag, and argument, generated from the CLI's own command registry. All command groups are indexed in [cli-reference.md](cli-reference.md).

## `sparkwing crons`

Arm, inspect and drive this host's local pipeline schedules

Runs the pipelines that declare an on.schedule cadence in
their .sparkwing/sparkwing.yaml, on this machine, from this home's runs
store.

Declaring a cadence does not arm it. `sparkwing crons install` arms a
repo's schedules on the host it is run from, and installs one OS timer -- a
systemd user timer on Linux, a launchd agent on macOS -- that calls
`sparkwing crons tick` every minute. Sparkwing evaluates every cron
expression itself inside that tick, so the machine holds one timer however
many schedules are armed.

Each tick resolves every due instant exactly once: it launches the run, skips
it when the previous scheduled run is still going and the policy is skip, or
records it missed when it fell outside the catch-up window. A scheduled run
carries the trigger source "schedule" and executes through the same detached
path as `sparkwing run --sw-detached`.

### Subcommands

- `install` -- Arm a repo's declared schedules on this host and install the OS timer
- `uninstall` -- Disarm a repo's schedules, and remove the timer when nothing is left
- `status` -- Report the OS timer, the last tick, and what is armed here
- `list` -- List the schedules armed on this host
- `show` -- Show one schedule's full record and its recent fires
- `next` -- Show the instants a schedule fires next
- `pause` -- Stop a schedule firing, keeping it armed
- `resume` -- Let a paused schedule fire again
- `run` -- Launch a schedule's pipeline now
- `tick` -- Evaluate every armed schedule once (the OS timer's entry point)

### Examples

```sh
# Arm this repo's schedules on this host
sparkwing crons install

# See what is armed and when it next fires
sparkwing crons list

# Check the timer and the last tick
sparkwing crons status
```

## `sparkwing crons install`

Arm a repo's declared schedules on this host and install the OS timer

Reads .sparkwing/sparkwing.yaml, records every pipeline that
declares on.schedule against this home, and ensures the OS timer that runs the
tick.

Each pipeline is compiled first and has to appear in the binary's own
description, because a schedule fires unattended: a pipeline that will not
build is refused here rather than at three in the morning. --no-prove arms
without that proof.

A repo that declares no schedule is reported as nothing to arm and installs no
timer. Re-running install republishes what the repo declares: a changed cron,
zone, overlap policy or catch-up window is stored, a pipeline that stopped
declaring a cadence is marked undeclared, and pause state, cursor and fire
history survive.

Arming is per host. Another machine reading the same repo stays idle until it
is armed too, so two hosts never race for the same instant.

### Flags

| Flag | Description |
|---|---|
| `--repo DIR` | Repo directory (default: discovered via nearest .sparkwing/) |
| `--fleet` | Arm every registered repo instead of one |
| `--no-prove` | Arm without compiling the pipelines first |
| `-o, --output FMT` | Output format: pretty\|json\|plain |

### Examples

```sh
# Arm the current repo
sparkwing crons install

# Arm a different repo
sparkwing crons install --repo /path/to/repo

# Arm every registered repo
sparkwing crons install --fleet
```

## `sparkwing crons list`

List the schedules armed on this host

One row per schedule: its id, its repo/pipeline name, the
cron expression and zone it is read in, when it next fires, when it last
fired, that fire's outcome, and whether it is armed, paused, or undeclared.

Schedules the repo no longer declares are hidden behind a count; --all shows
them. They keep their history and never fire.

### Flags

| Flag | Description |
|---|---|
| `--all` | Include schedules the repo no longer declares |
| `-o, --output FMT` | Output format: pretty\|json\|plain |

### Examples

```sh
# What is armed here
sparkwing crons list

# Include withdrawn schedules
sparkwing crons list --all

# Machine-readable (NDJSON)
sparkwing crons list -o json
```

## `sparkwing crons next`

Show the instants a schedule fires next

Walks the cron expression forward from now in the zone it is
read in. With a name, the next instants of that schedule; without one, the
next instants across every armed schedule, merged in time order.

This is the cheapest way to check a cron expression means what it looks
like -- a day-of-week field, a DST boundary, a zone that is not yours.

### Arguments

- `NAME` (optional) -- Schedule id, repo/pipeline, or a unique pipeline name; omit for every armed schedule

### Flags

| Flag | Description |
|---|---|
| `--count N` | How many instants to show (default: 5) |
| `-o, --output FMT` | Output format: pretty\|json\|plain |

### Examples

```sh
# What fires next on this host
sparkwing crons next

# Check one expression
sparkwing crons next nightly-rebuild --count 10
```

## `sparkwing crons pause`

Stop a schedule firing, keeping it armed

A paused schedule still advances its cursor on every tick, so
resuming it fires the next due instant rather than replaying the ones that
passed while it was paused.

### Arguments

- `NAME` (required) -- Schedule id, repo/pipeline, or a unique pipeline name

### Flags

| Flag | Description |
|---|---|
| `-o, --output FMT` | Output format: pretty\|json\|plain |

### Examples

```sh
# Pause a schedule
sparkwing crons pause nightly-rebuild
```

## `sparkwing crons resume`

Let a paused schedule fire again

Resumes at the next due instant. The instants that passed while the schedule was paused are behind its cursor and do not run.

### Arguments

- `NAME` (required) -- Schedule id, repo/pipeline, or a unique pipeline name

### Flags

| Flag | Description |
|---|---|
| `-o, --output FMT` | Output format: pretty\|json\|plain |

### Examples

```sh
# Resume a schedule
sparkwing crons resume nightly-rebuild
```

## `sparkwing crons run`

Launch a schedule's pipeline now

Runs the pipeline immediately, whatever the cadence says and
whether or not the schedule is paused, and records the launch in the
schedule's history as a manual fire.

The cursor does not move: a manual run is not one of the cadence's due
instants, so the next one still fires on time.

### Arguments

- `NAME` (required) -- Schedule id, repo/pipeline, or a unique pipeline name

### Flags

| Flag | Description |
|---|---|
| `-o, --output FMT` | Output format: pretty\|json\|plain |

### Examples

```sh
# Run a schedule's pipeline now
sparkwing crons run nightly-rebuild
```

## `sparkwing crons show`

Show one schedule's full record and its recent fires

Prints every stored field with absolute times, then the
instants that have resolved, newest first: when each was due, when the tick
decided it, what it decided, the run it launched and that run's current
status, and the reason for any outcome that is not a launch.

NAME is a schedule id, a repo/pipeline name, or a bare pipeline name that is
unique across this host's schedules.

### Arguments

- `NAME` (required) -- Schedule id, repo/pipeline, or a unique pipeline name

### Flags

| Flag | Description |
|---|---|
| `--fires N` | How many recent fires to show (default: 10) |
| `-o, --output FMT` | Output format: pretty\|json\|plain |

### Examples

```sh
# Inspect one schedule
sparkwing crons show nightly-rebuild

# Read further back
sparkwing crons show nightly-rebuild --fires 50
```

## `sparkwing crons status`

Report the OS timer, the last tick, and what is armed here

Answers whether this host is actually evaluating what it
armed: whether the timer is installed and running, whether it runs this
sparkwing or one that has since moved, when the tick last landed and what it
reported, and how many schedules are armed, paused, and undeclared.

Exits non-zero when schedules are armed and the timer is not running, runs
another binary, or has not ticked in the last few minutes, so a check script
can read the exit code. A host with nothing armed is healthy.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FMT` | Output format: pretty\|json\|plain |

### Examples

```sh
# Read the host's scheduler health
sparkwing crons status

# Machine-readable
sparkwing crons status -o json
```

## `sparkwing crons tick`

Evaluate every armed schedule once (the OS timer's entry point)

What the systemd timer or launchd agent runs every minute.
It takes an exclusive lock so two ticks never resolve the same instant,
re-reads what the armed repos declare, evaluates every declared unpaused
schedule against its cursor, launches what is due, and records each outcome.

Quiet on success: one summary line and the id of each run it launched. It
exits non-zero only when the tick itself could not run, so a schedule that
fails to launch is recorded against that schedule and the timer stays green.

--dry-run prints what this minute would resolve and writes nothing.

Run it by hand on a host whose platform has no sparkwing timer, from that
machine's own scheduler, once a minute.

### Flags

| Flag | Description |
|---|---|
| `--dry-run` | Evaluate and report without launching or recording anything |
| `-o, --output FMT` | Output format: pretty\|json\|plain |

### Examples

```sh
# Evaluate every armed schedule once
sparkwing crons tick

# See what this minute would do
sparkwing crons tick --dry-run
```

## `sparkwing crons uninstall`

Disarm a repo's schedules, and remove the timer when nothing is left

Deletes every schedule of one checkout, and its fire history,
from this home. When no schedule remains armed anywhere, the OS timer goes
too: the timer exists to serve armed schedules and nothing else.

--fleet disarms every schedule this home holds.

### Flags

| Flag | Description |
|---|---|
| `--repo DIR` | Repo directory (default: discovered via nearest .sparkwing/) |
| `--fleet` | Disarm every schedule this home holds |
| `-o, --output FMT` | Output format: pretty\|json\|plain |

### Examples

```sh
# Disarm the current repo
sparkwing crons uninstall

# Disarm everything on this host
sparkwing crons uninstall --fleet
```
