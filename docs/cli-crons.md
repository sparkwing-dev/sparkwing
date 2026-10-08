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
systemd user timer on Linux, a launchd agent on macOS -- that calls `sparkwing crons tick` every minute. Sparkwing evaluates every cron
expression itself inside that tick, so the machine holds one timer however
many schedules are armed.

Each tick resolves every due instant exactly once: it launches the run, skips
it when the previous scheduled run is still going and the policy is skip, or
records it missed when it fell outside the catch-up window. A scheduled run
carries the trigger source "schedule" and executes through the same detached
path as `sparkwing run --sw-detached`.

Arming pins by default: install compiles the pipeline and keeps that binary, so
a checkout updated afterwards does not change what runs unattended. Re-run
install to move the pin, `crons set --unpin` to follow the checkout
again, and `crons set` to override a declared cadence on this host
alone.

--profile NAME points every verb but tick, set --pin and set --unpin at a
controller instead of this host.
`crons install --profile` pushes the repo's `where: controller`
entries to it, pinned at HEAD unless --follow; the controller evaluates them
from a loop of its own, one evaluator per store, and each fire becomes a
trigger the cluster clones and runs.

### Subcommands

- `install` -- Arm a repo's declared schedules on this host and install the OS timer
- `uninstall` -- Disarm a repo's schedules, and remove the timer when nothing is left
- `list` -- List the schedules armed on this host
- `show` -- Show one schedule's full record and its recent fires
- `set` -- Override a declared cadence on this host
- `run` -- Launch a schedule's pipeline now

### Examples

```sh
# Arm this repo's schedules on this host
sparkwing crons install

# See what is armed and when it next fires
sparkwing crons list

# Check the timer and the last tick
sparkwing crons list --timer

# Push this repo's controller schedules
sparkwing crons install --profile prod
```

## `sparkwing crons install`

Arm a repo's declared schedules on this host and install the OS timer

Reads .sparkwing/sparkwing.yaml, records every on.schedule
entry that declares "where: local" against this home, and ensures the OS timer
that runs the tick. An entry declaring "where: controller" is reported and left
alone: this host does not fire it.

Each pipeline is compiled first and has to appear in the binary's own
description, because a schedule fires unattended: a pipeline that will not
build is refused here before unattended execution. That compile is also
the pin: the binary is copied under the sparkwing home and recorded with the
checkout's HEAD, so every fire runs what was armed however the checkout moves
afterwards. --follow arms without a pin, and each fire compiles the checkout.
--no-prove skips the compile, and so pins nothing.

--only arms a subset, naming pipelines or pipeline/name entries; a name the
repo does not declare is refused before anything is written.

A repo that declares no schedule is reported as nothing to arm and installs no
timer. Re-running install is the explicit update: it re-pins at the current
checkout, republishes what the repo declares, marks a pipeline that stopped
declaring a cadence undeclared, and re-bases this host's overrides onto the new
declaration. Pause state, cursor, fire history and the override values survive.

Arming is per host. Another machine runs the same schedule only when the
schedule is also armed on that machine.

--profile NAME pushes the repo's "where: controller" entries to that
controller instead, and reports the "where: local" ones as this host's. The
push needs a git origin, because the cluster clones the source at each fire; it
pins every fire to the checkout's HEAD unless --follow, which clones the branch
tip. A HEAD no remote branch carries is refused, because every fire would fail
at the clone; uncommitted edits are a warning, since the pushed commit is what
runs. Re-running the push is the explicit update, and it moves the pin.

### Flags

| Flag | Description |
|---|---|
| `--profile NAME` | Profile name; omit for this host |
| `--fleet` | Arm every registered repo instead of one |
| `--only NAMES` | Arm only these pipelines or pipeline/name entries (comma-separated or repeatable) |
| `--follow` | Arm without pinning, so every fire compiles the checkout |
| `--no-prove` | Arm without compiling the pipelines first, which pins nothing |
| `-o, --output FMT` | Output format: pretty\|json\|plain |

### Examples

```sh
# Arm the current repo
sparkwing crons install

# Arm a different repo
sparkwing -C /path/to/repo crons install

# Arm two entries only
sparkwing crons install --only nightly,sweep/quick

# Arm without pinning
sparkwing crons install --follow

# Arm every registered repo
sparkwing crons install --fleet

# Push the controller entries to a cluster
sparkwing crons install --profile prod

# Push them following the branch tip
sparkwing crons install --profile prod --follow
```

## `sparkwing crons list`

List the schedules armed on this host

One row per schedule: its id, its repo/pipeline name, the
cron expression and zone it is read in, when it next fires, when it last
fired, that fire's outcome, and whether it is armed, paused, or undeclared.

Schedules the repo no longer declares are hidden behind a count; --all shows
them. They keep their history and never fire.

--timer answers whether this host is actually evaluating what it armed: whether
the timer is installed and running, whether it runs this sparkwing or one that
has since moved, when the tick last landed and what it reported, and how many
schedules are armed, paused, and undeclared. It exits non-zero when schedules
are armed and the timer is not running, runs another binary, or has not ticked
in the last few minutes, so a check script can read the exit code. A host with
nothing armed is healthy. With --profile it reads the controller's scheduler:
its counts, when its loop last ticked, and what that tick reported.

--next N merges the next N instants of every armed schedule, each in its
configured zone; `crons show NAME --next N` reads one schedule.

### Flags

| Flag | Description |
|---|---|
| `--profile NAME` | Profile name; omit for this host |
| `--all` | Include schedules the repo no longer declares |
| `--timer` | Report the OS timer, the last tick and the counts instead of the rows |
| `--next N` | Show the next N instants across every armed schedule instead of the rows |
| `-o, --output FMT` | Output format: pretty\|json\|plain |

### Examples

```sh
# What is armed here
sparkwing crons list

# Include withdrawn schedules
sparkwing crons list --all

# Check the timer and the last tick
sparkwing crons list --timer

# What fires next on this host
sparkwing crons list --next 5

# What a controller evaluates
sparkwing crons list --profile prod

# Machine-readable (NDJSON)
sparkwing crons list -o json
```

## `sparkwing crons run`

Launch a schedule's pipeline now

Runs the pipeline immediately, whatever the cadence says and
whether or not the schedule is paused, and records the launch in the
schedule's history as a manual fire.

The cursor does not move: a manual run is not one of the cadence's due
instants, so the next one still fires on time.

### Arguments

- `NAME` (required) -- Schedule id, repo/pipeline[/name], pipeline/name, or a unique pipeline name

### Flags

| Flag | Description |
|---|---|
| `--profile NAME` | Profile name; omit for this host |
| `-o, --output FMT` | Output format: pretty\|json\|plain |

### Examples

```sh
# Run a schedule's pipeline now
sparkwing crons run fictional-nightly
```

## `sparkwing crons set`

Override a declared cadence on this host

Lays this host's own value over what the repo declares, for
the cron expression, the zone, the overlap policy, the catch-up window and the
launch's arguments. Everything left unnamed keeps the declared value, and a
field named again replaces the previous override.

--arg replaces the declared argument set whole, so name every argument the
schedule should launch with.

The override survives re-arming; --reset drops it, returning the schedule to
what the repo declares while the pin, the pause state, the cursor and the fire
history stay.

--pin compiles the pipeline, keeps that binary under the sparkwing home, and
records the checkout's HEAD, so every later fire runs that binary however the
checkout moves. The pin covers the pipeline the repo declares; scripts and
binaries it runs from the checkout or from PATH are outside it. --unpin drops
the pin and its binary, so every later fire compiles the checkout as it stands
at that minute and the tick's refresh reads the declaration again. A
controller schedule is pinned by the commit it was pushed at, so neither takes
--profile.

--pause stops the schedule firing and keeps it armed. A paused schedule still
advances its cursor on every tick, so --resume fires the next due instant
instead of replaying the ones that passed while it was paused.

--pin, --unpin, --pause, --resume and --reset each stand alone on a call.
`sparkwing crons list` marks an overridden expression with *, and
`sparkwing crons show` prints the declared, override and effective
value side by side.

### Arguments

- `NAME` (required) -- Schedule id, repo/pipeline[/name], pipeline/name, or a unique pipeline name

### Flags

| Flag | Description |
|---|---|
| `--profile NAME` | Profile name; omit for this host |
| `--cron EXPR` | Cron expression to run instead of the declared one |
| `--tz ZONE` | Zone the expression is read in, such as America/Denver or local |
| `--overlap POLICY` | What a due instant does while the previous run is going: skip\|queue |
| `--catch-up DUR` | How late a due instant may still fire, such as 6h |
| `--arg K=V` | Argument the launch passes (repeatable; replaces the declared set) |
| `--reset` | Drop this host's override and run what the repo declares |
| `--pin` | Pin the schedule to the checkout as it stands |
| `--unpin` | Let the schedule follow the checkout again |
| `--pause` | Stop the schedule firing, keeping it armed |
| `--resume` | Let a paused schedule fire again |
| `-o, --output FMT` | Output format: pretty\|json\|plain |

### Examples

```sh
# Run it later on this host
sparkwing crons set nightly --cron '0 5 * * *'

# Read the expression locally
sparkwing crons set nightly --tz local

# Launch with arguments
sparkwing crons set sweep/quick --arg depth=shallow --arg dry-run=true

# Run what the repo declares
sparkwing crons set nightly --reset

# Pin a schedule at HEAD
sparkwing crons set nightly --pin

# Follow the checkout again
sparkwing crons set nightly --unpin

# Pause a schedule
sparkwing crons set fictional-nightly --pause

# Resume it
sparkwing crons set fictional-nightly --resume
```

## `sparkwing crons show`

Show one schedule's full record and its recent fires

Prints every stored field with absolute times, then the
instants that have resolved, newest first: when each was due, when the tick
decided it, what it decided, the run it launched and that run's current
status, and the reason for any outcome that is not a launch.

NAME is a schedule id, a repo/pipeline name, or a bare pipeline name that is
unique across this host's schedules.

--next N prints the next N instants the schedule fires, in its configured
zone, instead of the record.

### Arguments

- `NAME` (required) -- Schedule id, repo/pipeline[/name], pipeline/name, or a unique pipeline name

### Flags

| Flag | Description |
|---|---|
| `--profile NAME` | Profile name; omit for this host |
| `--fires N` | How many recent fires to show (default: 10) |
| `--next N` | Show the next N instants this schedule fires instead of its record |
| `-o, --output FMT` | Output format: pretty\|json\|plain |

### Examples

```sh
# Inspect one schedule
sparkwing crons show fictional-nightly

# Read further back
sparkwing crons show fictional-nightly --fires 50

# Check one expression
sparkwing crons show fictional-nightly --next 10
```

## `sparkwing crons uninstall`

Disarm a repo's schedules, and remove the timer when nothing is left

Deletes every schedule of one checkout, and its fire history,
from this home. When no schedule remains armed anywhere, the OS timer goes
too: the timer exists to serve armed schedules and nothing else.

--fleet disarms every schedule this home holds.

--name NAME removes one schedule instead: its fire history and its pinned
pipeline binary go with it, and every other schedule of the same pipeline and
repo stays armed. The timer is left as it is. To stop a schedule without
losing its history, `crons set NAME --pause` instead.

--profile NAME deletes the repo's schedules from that controller instead,
naming the repo by its git origin, or with --name the one schedule.

### Flags

| Flag | Description |
|---|---|
| `--profile NAME` | Profile name; omit for this host |
| `--fleet` | Disarm every schedule this home holds |
| `--name NAME` | Disarm this one schedule (id, repo/pipeline[/name], pipeline/name, or a unique pipeline name) |
| `-o, --output FMT` | Output format: pretty\|json\|plain |

### Examples

```sh
# Disarm the current repo
sparkwing crons uninstall

# Disarm everything on this host
sparkwing crons uninstall --fleet

# Remove one named entry
sparkwing crons uninstall --name sweep/quick

# Remove this repo from a controller
sparkwing crons uninstall --profile prod
```
