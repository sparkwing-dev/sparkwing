<!-- GENERATED from the CLI command registry by `sparkwing commands -o markdown`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference: sparkwing queue

Every `sparkwing queue` command, flag, and argument, generated from the CLI's own command registry. All command groups are indexed in [cli-reference.md](cli-reference.md).

## `sparkwing queue`

The truthful view of local admission: holders, connections, waiters, and why

Reads the local admission daemon and prints one honest picture of
where every run stands: each resource (host cores, memory, and every
named concurrency semaphore) with its capacity and how much is in use;
every run currently holding resources, with the repo it came from, how
long it has held, and what it is charged; connected run registrations that
hold no resources, labeled separately; and every waiter in admission
order, with its position, priority, cost, and exactly what it is waiting on.
A child run attached to its parent's lease renders indented under that
parent. The header carries a one-line summary of the daemon's recent
admission outcomes -- runs granted, median wait, evictions, queue
timeouts, younger backfills, and protected waiters -- so chronic patterns show
up at a glance.

A holder that is alive but has burned near-zero CPU while runs queue
behind it is flagged as stalled, together with the exact command to
clear it -- 'sparkwing runs cancel --run <id>'. The queue never kills a
run for you and never points at a host-wide destructive verb.

Pretty on a terminal, JSON when piped (add -o json to force it), and
one tab-separated record per line with -o plain for shell pipelines.

Every view says outright whether it reached the daemon, because an empty
queue and an unanswered one look identical otherwise. With no daemon
running there is nothing to arbitrate, so the command reports an empty
queue and exits 0. When the daemon's socket cannot be reached at all --
blocked by a sandbox, wedged, gone mid-read -- what is queued is unknown
rather than empty: the command says so, names the dial failure, and exits
4 instead of reporting a quiet machine it never looked at.

With --profile NAME the view switches to that profile's controller: the
same renderer prints the controller's admission state -- every
concurrency key, its holders and waiters, and each registered runner's
free capacity -- so one vocabulary reads local and cluster admission
alike.

### Subcommands

- `exec` -- Run a command under local machine admission

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Output format: pretty \| json \| plain |
| `--home DIR` | Sparkwing home to inspect (default: $SPARKWING_HOME or ~/.sparkwing) |
| `--profile NAME` | Inspect this profile's controller instead of the local daemon |

### Examples

```sh
# Show the current queue
sparkwing queue

# Agent-readable snapshot
sparkwing queue -o json

# One record per line for shell pipelines
sparkwing queue -o plain

# Inspect a controller's admission state
sparkwing queue --profile prod
```

## `sparkwing queue exec`

Run a command under local machine admission

Submits the command to the local admission daemon before starting it. While blocked, the command is visible in sparkwing queue. Once granted, its complete process tree runs under the lease; interruption or cancellation terminates and reaps that tree before the lease is released. Exact process-session ownership is available on Linux and macOS; queue exec refuses before admission on Windows and other Unix platforms.

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
