<!-- GENERATED from the CLI command registry by `sparkwing commands --format markdown --output plain`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference: sparkwing queue

Every `sparkwing queue` command, flag, and argument, generated from the CLI's own command registry. All command groups are indexed in [cli-reference.md](cli-reference.md).

## `sparkwing queue`

Inspect local admission holders, connections, and waiters

Reports the local admission daemon's resource capacity, usage, and queue in
two sections: running work, then queued work in admission order.

A running row carries the repository, elapsed time, charge, and, from the
run's measured p50 profile, its expected remaining time and the clock time it
is expected to finish. A queued row carries its position, priority, cost, how
long it has waited, the resource it waits on, and, from the daemon's
admission simulation, when it is expected to start and finish. Attached child
runs appear under their parent. Connected runs that hold no resources have
separate rows.

An estimate exists only where the measurements behind it do, and a cell
without one says which measurement is missing. "unmeasured" is a row the
daemon has no profile for. "past p50" is a run that has already outlived the
profile it has, which no longer predicts it. "unknown" is a queued row the
daemon cannot place, because a run ahead of it has no estimate of its own.
None of the three is replaced by a guess. The header counts the queued runs
with no profile, because those are the ones that starve.
'sparkwing queue priority' re-ranks a queued run.

A stalled holder includes a cancellation command:
'sparkwing runs cancel --run <id>'. Inspect the holder before cancelling it.
The queue command only reports state.

Output is pretty on a terminal and JSON when piped. Select JSON explicitly
with -o json, or tab-separated records with -o plain. JSON carries each
estimate as milliseconds from the snapshot and as an RFC3339 clock time;
plain carries humanized durations and RFC3339 clock times.

An absent daemon reports an empty queue and exits 0. An unreachable daemon
reports the connection failure and exits 4; its queue state is unknown.

With --profile NAME, the view reads that profile's controller and shows each
concurrency key, its holders and waiters, and registered runner capacity.
'sparkwing queue' and 'sparkwing queue list' print the same listing.

### Subcommands

- `list` -- List running and queued work with expected start and finish
- `exec` -- Run a command under local machine admission
- `priority` -- Re-rank a run that is already queued for local admission

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Output format: pretty \| json \| plain |
| `--home DIR` | Sparkwing home to inspect (default: $SPARKWING_HOME or ~/.sparkwing) |
| `--profile NAME` | Inspect this profile's controller instead of the local daemon |

### Examples

```sh
# Show the current queue
sparkwing queue list

# Agent-readable snapshot
sparkwing queue list -o json

# One record per line for shell pipelines
sparkwing queue list -o plain

# Inspect a controller's admission state
sparkwing queue list --profile prod

# Move a queued run to the front
sparkwing queue priority --run build-123 --set front
```

## `sparkwing queue exec`

Run a command under local machine admission

Submits the command to the local admission daemon before starting it. While
blocked, the command is visible in sparkwing queue. Once granted, its complete
process tree runs under the lease; interruption or cancellation terminates and
reaps that tree before the lease is released. Exact process-session ownership
is available on Linux and macOS; queue exec refuses before admission on
Windows and other Unix platforms.

### Arguments

- `command` (required) -- Command and arguments to execute after --

### Flags

| Flag | Description |
|---|---|
| `--run-id ID` | Unique admission participant identifier (required) |
| `--name NAME` | Short operation name shown in the queue |
| `--repo NAME` | Repository name shown in the queue |
| `--cores N` | CPU cores reserved while the command runs (required) |
| `--memory-bytes N` | Memory bytes reserved while the command runs |
| `--semaphore NAME` | Logical semaphore shared with equivalent commands |
| `--semaphore-capacity N` | Capacity declared for --semaphore (default: 1) |
| `--ready-file PATH` | Write queued or granted readiness to a new JSON file |
| `--home DIR` | Sparkwing state directory |

### Examples

```sh
# Serialize a bootstrap command
sparkwing queue exec --run-id build-123 --name bootstrap --cores 1 --semaphore bootstrap -- make prepare
```

## `sparkwing queue list`

List running and queued work with expected start and finish

Reports the local admission daemon's resource capacity, usage, and queue in
two sections: running work, then queued work in admission order.

A running row carries the repository, elapsed time, charge, and, from the
run's measured p50 profile, its expected remaining time and the clock time it
is expected to finish. A queued row carries its position, priority, cost, how
long it has waited, the resource it waits on, and, from the daemon's
admission simulation, when it is expected to start and finish. Attached child
runs appear under their parent. Connected runs that hold no resources have
separate rows.

An estimate exists only where the measurements behind it do, and a cell
without one says which measurement is missing. "unmeasured" is a row the
daemon has no profile for. "past p50" is a run that has already outlived the
profile it has, which no longer predicts it. "unknown" is a queued row the
daemon cannot place, because a run ahead of it has no estimate of its own.
None of the three is replaced by a guess. The header counts the queued runs
with no profile, because those are the ones that starve.
'sparkwing queue priority' re-ranks a queued run.

A stalled holder includes a cancellation command:
'sparkwing runs cancel --run <id>'. Inspect the holder before cancelling it.
The queue command only reports state.

Output is pretty on a terminal and JSON when piped. Select JSON explicitly
with -o json, or tab-separated records with -o plain. JSON carries each
estimate as milliseconds from the snapshot and as an RFC3339 clock time;
plain carries humanized durations and RFC3339 clock times.

An absent daemon reports an empty queue and exits 0. An unreachable daemon
reports the connection failure and exits 4; its queue state is unknown.

With --profile NAME, the view reads that profile's controller and shows each
concurrency key, its holders and waiters, and registered runner capacity.
This is the same output as 'sparkwing queue'.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Output format: pretty \| json \| plain |
| `--home DIR` | Sparkwing home to inspect (default: $SPARKWING_HOME or ~/.sparkwing) |
| `--profile NAME` | Inspect this profile's controller instead of the local daemon |

### Examples

```sh
# Show the current queue
sparkwing queue list

# Agent-readable snapshot
sparkwing queue list -o json

# One record per line for shell pipelines
sparkwing queue list -o plain
```

## `sparkwing queue priority`

Re-rank a run that is already queued for local admission

Changes the admission priority of a run the local daemon is
already arbitrating, without restarting it. Higher priorities admit
first and ties keep their arrival order, exactly as at launch. A raise
that frees the run to start admits it immediately.

--set takes an integer, or `front` / `back`. The relative forms
resolve against the waiters that are not part of this run: front is one
above the highest other waiter's priority, back is one below the lowest,
and both fall back to a step either side of zero when nothing else is
waiting. Asking for front twice is therefore stable instead of an
escalating race with the run's own rank.

One run is several admission participants -- the run itself, and each of
its nodes admitting on its own. All of them move together, and the new
rank is remembered, so a node admitting later lands at it too instead of
at the priority its plan carried. The daemon forgets that rank once the
run has released every lease and has no participant waiting.

When the run already holds a lease there is nothing to re-order: the
command says so, and the change reaches only the node admissions the run
has yet to make.

Exits 0 whether the rank moved or was already what you asked for, 1 when
the daemon does not know the run -- a submitted run the consumer has not
claimed yet is not queued here, so it is not visible to local admission --
and 4 when the daemon's socket cannot be reached at all.

### Flags

| Flag | Description |
|---|---|
| `--run ID` | Run id to re-rank (required) |
| `--set VALUE` | New priority: an integer, front, or back (required) |
| `-o, --output FORMAT` | Output format: pretty \| json \| plain |
| `--home DIR` | Sparkwing home to inspect (default: $SPARKWING_HOME or ~/.sparkwing) |

### Examples

```sh
# Send a queued run to the front
sparkwing queue priority --run build-123 --set front

# Park a run behind everything else
sparkwing queue priority --run nightly-42 --set back

# Set an explicit rank
sparkwing queue priority --run build-123 --set 7

# Agent-readable answer
sparkwing queue priority --run build-123 --set 7 -o json
```
