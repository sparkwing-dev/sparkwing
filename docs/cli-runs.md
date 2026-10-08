<!-- GENERATED from the CLI command registry by `sparkwing commands --format markdown --output plain`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference: sparkwing runs

Every `sparkwing runs` command, flag, and argument, generated from the CLI's own command registry. All command groups are indexed in [cli-reference.md](cli-reference.md).

## `sparkwing runs`

Inspect and control pipeline runs

Inspect recorded pipeline executions and control their lifecycle.
Commands support local runs and runs stored through a named profile.
Pass --profile NAME to select that profile's backend.

### Subcommands

- `consumer` -- Inspect or control the process that executes submitted runs
- `list` -- List recent pipeline runs
- `status` -- Show one run's status (non-zero exit unless status=success)
- `logs` -- Print a run's logs
- `stats` -- Report run counts, success rate, and duration percentiles
- `annotations` -- Read or append persistent node + step annotations
- `approvals` -- List approval gates (pending and history)
- `retry` -- Trigger fresh runs copying pipeline + args from old ones
- `cancel` -- Request cancellation of in-flight runs
- `bounce` -- Restart one running job's process without failing the run
- `prune` -- Delete finished runs older than a threshold, or by id

## `sparkwing runs annotations`

Read or append persistent node + step annotations

Annotations are short summary strings that pipelines (via
sparkwing.Annotate) and agents append to a node or step during a
run. They show up on the dashboard alongside outcome. This verb
lets an agent read every annotation on a run or contribute one
without going through the SDK.

### Subcommands

- `list` -- List annotations on a run
- `add` -- Append an annotation to a node or step

## `sparkwing runs annotations add`

Append an annotation to a node or step

Appends one message to the annotations list on a node, or on a
step when --step is given. Annotations are append-only; the same
message string can be added more than once and the order is
preserved as the dashboard renders them.

### Flags

| Flag | Description |
|---|---|
| `--run RUN_ID` | Run identifier (required) |
| `--node NODE_ID` | Node identifier (required) |
| `--step STEP_ID` | Step identifier (annotates the step instead of the node) |
| `-m, --message TEXT` | Annotation text (required) |
| `--profile NAME` | Profile name; omit for local-only |

### Examples

```sh
# Note something on a node
sparkwing runs annotations add --run run-fictional --node deploy -m 'agent: retried after 502'

# Note something on a step inside a node
sparkwing runs annotations add --run run-fictional --node deploy --step canary -m 'rolled out 5%'
```

## `sparkwing runs annotations list`

List annotations on a run

Prints node-level annotations by default. Pass --steps to also
include per-step annotations as separate rows; passing --step
implies step-scope and limits to the matching step.

### Flags

| Flag | Description |
|---|---|
| `--run RUN_ID` | Run identifier (required) |
| `--node NODE_ID` | Limit to one node |
| `--step STEP_ID` | Limit to one step (implies step-scope reads) |
| `--steps` | Include per-step annotations |
| `-o, --output FORMAT` | Output format: pretty\|json\|plain |
| `--profile NAME` | Profile name; omit for local-only |

### Examples

```sh
# Every node annotation on a run
sparkwing runs annotations list --run run-fictional

# Include per-step annotations
sparkwing runs annotations list --run run-fictional --steps

# One node's annotations as JSON
sparkwing runs annotations list --run run-fictional --node build -o json
```

## `sparkwing runs approvals`

List approval gates (pending and history)

Inspect approval gates. Without --run returns every pending
gate across all runs; with --run returns one run's full history
(pending + resolved).

### Subcommands

- `list` -- List pending approvals (or one run's history)
- `approve` -- Approve a pending approval-gate node
- `deny` -- Deny a pending approval-gate node

## `sparkwing runs approvals approve`

Approve a pending approval-gate node

Resolves the named approval gate as 'approved'. The gate's
downstream nodes begin dispatching on the next orchestrator
poll (roughly 500ms). The approver is recorded from the
authenticated principal when --profile is set, or from $USER in
local mode.

Exit code is 0 on success, non-zero if the gate doesn't exist
or was already resolved (409).

### Flags

| Flag | Description |
|---|---|
| `--run ID` | Run ID holding the approval gate (required) |
| `--node ID` | Node ID of the approval gate (required) |
| `--comment STR` | Optional note recorded on the approval |
| `--profile NAME` | Profile name; omit for local-only |

### Examples

```sh
# Approve a local gate
sparkwing runs approvals approve --run run-fictional --node approve-prod

# Approve a prod gate with a comment
sparkwing runs approvals approve --run run-fictional --node approve-prod --profile prod --comment "release notes ok"
```

## `sparkwing runs approvals deny`

Deny a pending approval-gate node

Resolves the named approval gate as 'denied'. The gated node
fails; downstream nodes see the failure and propagate per
their ContinueOnError / Optional settings.

### Flags

| Flag | Description |
|---|---|
| `--run ID` | Run ID holding the approval gate (required) |
| `--node ID` | Node ID of the approval gate (required) |
| `--comment STR` | Optional note recorded on the approval |
| `--profile NAME` | Profile name; omit for local-only |

### Examples

```sh
# Deny a local gate
sparkwing runs approvals deny --run run-fictional --node approve-prod

# Deny a prod gate with a reason
sparkwing runs approvals deny --run run-fictional --node approve-prod --profile prod --comment "tests still red"
```

## `sparkwing runs approvals list`

List pending approvals (or one run's history)

Prints a table of approval rows. Without --run the list is the
cross-run pending queue; with --run it's every approval for that
run, both pending and resolved.

### Flags

| Flag | Description |
|---|---|
| `--run RUN_ID` | Restrict to one run's approvals |
| `-o, --output FORMAT` | Output format: pretty\|json\|plain |
| `--profile NAME` | Profile name; omit for local-only |

### Examples

```sh
# Pending gates on the local store
sparkwing runs approvals list

# Pending gates on prod
sparkwing runs approvals list --profile prod

# Full history for one run
sparkwing runs approvals list --run run-fictional

# Emit JSON for an agent
sparkwing runs approvals list -o json
```

## `sparkwing runs bounce`

Restart one running job's process without failing the run

Stops the process executing one running job and runs that
job again, in place. The run keeps going: the job never reaches a
terminal state, so nothing downstream sees a failure and no other
job is disturbed.

Use it for a job that is wedged or misbehaving when cancelling the
whole run would cost more than it saves.

The request is recorded and the verb returns; the runner supervising
the job picks it up within a few seconds, stops the process (SIGTERM,
then SIGKILL after the grace period), and re-runs the job from its
first step. Steps therefore run again, so a job with side effects
needs the same idempotency a restarted pod already demands.

A job that finishes before the stop lands is left alone. Bouncing
again is allowed -- one request is one restart.

The local runner is what acts on the request, whether the run's state
lives here or on a controller. A job the in-cluster Kubernetes runner
executes records the request and nothing consumes it, so the job keeps
running; cancel the run and retry it instead.

### Flags

| Flag | Description |
|---|---|
| `--run RUN_ID` | Run id owning the job |
| `--node NODE_ID` | Job id to bounce |
| `--profile NAME` | Profile name for remote runs; omit for local runs |

### Examples

```sh
# Bounce a wedged job
sparkwing runs bounce --run run-fictional --node build

# Bounce a job in a run a controller holds
sparkwing runs bounce --run run-fictional --node build --profile prod
```

## `sparkwing runs cancel`

Request cancellation of in-flight runs

Sends a cancel request per run to the controller. Each run
transitions to 'cancelling' and then 'cancelled' once the runner
acknowledges. Already-finished runs surface a per-id error but
don't abort the batch.

Pass --run once per id (repeatable). Use --run - to read ids
from stdin, one per line. For local runs sharing an admission lease,
cancelling a child also cancels its descendants. Its parent and siblings
continue. Cancelling the root cancels every member of that lease.
Children launched after a parent exits attach under its nearest live ancestor
while a live descendant retains its lineage. Otherwise they attach under the
lease root, and the daemon logs the parent resolution.

### Flags

| Flag | Description |
|---|---|
| `--run RUN_ID` | Run id to cancel (repeatable; use --run - to read ids from stdin) |
| `--profile NAME` | Profile name for remote runs; omit for local runs |

### Examples

```sh
# Cancel one run
sparkwing runs cancel --run run-fictional --profile prod

# Cancel every running prod run
sparkwing runs list --status running --profile prod -q | sparkwing runs cancel --run - --profile prod
```

## `sparkwing runs consumer`

Inspect or control the process that executes submitted runs

One consumer per Sparkwing home claims queued triggers and executes them.
A file lock grants exclusive ownership; a dashboard uses the same lock.
A detached launch starts a consumer when needed.

Stopping the consumer leaves queued runs available for a later consumer.
An interrupted executing run returns to the queue. Cancel a run to prevent
further execution.

A detached launch from a different build replaces the consumer. Replacement
interrupts active work and returns it to the queue for the new consumer.

### Subcommands

- `start` -- Start a consumer for this home if none is running
- `status` -- Report whether a consumer is resident
- `stop` -- Stop the resident consumer

## `sparkwing runs consumer start`

Start a consumer for this home if none is running

Starts the resident trigger consumer and waits until it owns the
home's queue. A no-op when one is already running.

Rarely needed by hand: 'sparkwing run --sw-detached' does this
before it acknowledges a run.

### Flags

| Flag | Description |
|---|---|
| `-o, --output pretty\|json\|plain` | Pretty on a terminal, NDJSON otherwise. Plain prints running or stopped. |
| `--idle DUR` | Exit after this long with no work (default 5m) |
| `--claim-lease DUR` | Lease stamped on each claimed run, renewed while it executes (default 3m) |

### Examples

```sh
# Start one for the default home
sparkwing runs consumer start

# Keep one resident for an hour
sparkwing runs consumer start --idle 1h
```

## `sparkwing runs consumer status`

Report whether a consumer is resident

Prints the resident consumer's pid, home, and log path. Exits 1
when no consumer is running, so it composes in shell conditions.

### Flags

| Flag | Description |
|---|---|
| `-o, --output pretty\|json\|plain` | Pretty on a terminal, NDJSON otherwise. Plain prints running or stopped. |

### Examples

```sh
# Check for a resident consumer
sparkwing runs consumer status
```

## `sparkwing runs consumer stop`

Stop the resident consumer

Signals the resident consumer to drain and exit. Queued runs are
not cancelled -- they stay queued and execute when a consumer
comes back, which the next 'sparkwing run --sw-detached' arranges.

To cancel a queued run instead, use 'sparkwing runs cancel'.

### Flags

| Flag | Description |
|---|---|
| `-o, --output pretty\|json\|plain` | Pretty on a terminal, NDJSON otherwise. Plain prints running or stopped. |

### Examples

```sh
# Stop the resident consumer
sparkwing runs consumer stop
```

## `sparkwing runs list`

List recent pipeline runs

Reads runs from the selected backend. Pass --profile NAME to select a
named profile. Filters compose with AND semantics across flag types
(pipeline=X AND status=Y), OR
semantics within a repeated flag (pipeline=X OR pipeline=Y).

A local listing merges this home's own store with every standalone
store under it -- the ones runs that could not reach the admission
daemon wrote -- newest first. Each row carries the store it came
from: 'shared', or the store's path under the home. An id in both
stores lists once, from the shared store. The STORE column appears
only when a standalone run is in the table; every run record in
-o json carries the field. A standalone store this build cannot read is named on
stderr after the table instead of listed.

With -q / --quiet the output contains run identifiers, one per line, for
shell piping:

  sparkwing runs list --pipeline X --limit 1 -q --profile prod \
      | xargs -I{} sparkwing runs logs --run {} --profile prod --follow

Results are paged. JSON ends with a kind:page record reporting returned,
limit, truncated and next_cursor, plus total where the count can be
exact; limit is the page size served, so a request above the ceiling
reports the ceiling rather than the number asked for. Continue with
--cursor and the same filters until truncated is false. Under -q, and in
the other formats, a cut listing says so on stderr instead. --limit 0 is
refused: this listing serves pages, so a page of zero has no meaning.

--by-pipeline aggregates every run the filters admit. Its JSON ends with
a kind:summary record carrying truncated and, where it stopped short,
reason, in place of a kind:page record.

--status failed --group-by run prints the page this listing selects as
failures, each run with its failing step and error; --group-by step or node
clusters that page's failures. Filters, --limit, --cursor and the page record
work as they do for the table. --wait blocks
until a run matches (a CI job waiting for the run its push started), and
--watch keeps printing each newer matching run. Both match on --pipeline,
--status, --branch, --sha, --repo, --root-only and --since.

### Flags

| Flag | Description |
|---|---|
| `--pipeline NAME` | Filter by pipeline name (repeatable; prefix `!` to exclude) |
| `--status STATUS` | Filter by status: running\|success\|failed\|cancelled (repeatable; prefix `!` to exclude) |
| `--branch BRANCH` | Filter by git branch (repeatable; prefix `!` to exclude) |
| `--sha PREFIX` | Filter by git sha prefix (repeatable; prefix `!` to exclude) |
| `--error SUBSTR` | Substring match against the persisted failure reason |
| `--search QUERY` | Free-text search across pipeline/branch/sha/id/error; prefix a term with `-` to exclude |
| `--since DURATION` | Only runs newer than this (1h, 24h, 7d, and similar durations) |
| `--started-after DATE` | Only runs whose StartedAt >= this (today, yesterday, 24h, 7d, or a date) |
| `--started-before DATE` | Only runs whose StartedAt <= this |
| `--finished-after DATE` | Only runs whose FinishedAt >= this (excludes still-running) |
| `--finished-before DATE` | Only runs whose FinishedAt <= this (excludes still-running) |
| `--limit N` | Runs per page; a request above the ceiling is served at the ceiling, which the page record reports (default: 20) |
| `--cursor CURSOR` | Continue after next_cursor with the same filters |
| `-o, --output FORMAT` | Output format: pretty\|json\|plain |
| `-q, --quiet` | Print only run ids, one per line (JSON strings with -o json) |
| `--by-pipeline` | Pivot into one row per pipeline with a status sparkline of the last N runs |
| `--sparkline N` | Sparkline length when --by-pipeline is set (default: 30) |
| `--style STYLE` | Sparkline glyph style: ascii\|block\|dot (default: ascii) |
| `--repo OWNER/NAME` | Filter by the repository a run declared (repeatable) |
| `--root-only` | Exclude child runs |
| `--group-by KEY` | With --status failed: list each failure (run) or cluster them by step or node |
| `--wait` | Block until at least one run matches, then list (exit 2 on timeout) |
| `--wait-timeout DURATION` | How long --wait blocks (default: 2m) |
| `-w, --watch` | After listing, print each newer matching run as it appears |
| `--profile NAME` | Profile name; omit for local-only |

### Examples

```sh
# Last 20 local runs
sparkwing runs list

# Continue after a truncated page
sparkwing runs list --since 30d --cursor 1700000000000000000:run-fictional:1697408000000000000

# Failed runs in the past day
sparkwing runs list --status failed --since 24h

# Exclude success from the list
sparkwing runs list --status '!success' --since 24h

# Runs on main, excluding canary
sparkwing runs list --branch main --search '-canary'

# Runs that hit a specific failure
sparkwing runs list --error 'permission denied'

# Runs finished today
sparkwing runs list --finished-after today

# List prod runs
sparkwing runs list --profile prod --limit 50

# By-pipeline rollup with sparkline
sparkwing runs list --by-pipeline --since 7d

# By-pipeline JSON for an agent
sparkwing runs list --by-pipeline -o json --since 24h

# Pipe the most recent run id into another verb
sparkwing runs list --limit 1 -q | xargs -I{} sparkwing runs logs --run {}

# Cluster the past week's failures by step
sparkwing runs list --status failed --since 7d --group-by step

# Wait for the run a push started
sparkwing runs list --sha abc123 --repo acme/web --root-only --wait -q

# Print each new run as it starts
sparkwing runs list --limit 1 --watch
```

## `sparkwing runs logs`

Print a run's logs

Without --profile, reads logs from the local run directory. Pass --profile
NAME to read from a remote controller's logs service (profile must
carry both controller + logs URLs). Line-selection filters
(--tail/--head/--lines/--grep) apply server-side in cluster mode so
the CLI never tails giant logs over the wire.

--since D drops nodes whose StartedAt is older than now-D; useful for
runs that have been retried several times where only the newest
attempt matters. Filtering is node-level (log lines aren't
timestamped on disk). --events-only and --no-events are mutually
exclusive views of the unified stream.

--events-only emits the envelope records the dispatcher writes beside a
local run (run_start, node_start, run_finish, ...). A run read through a
backend emits that run's stored event records instead (admission_wait,
concurrency_wait, cache_hit, ...) -- a different record shape. That is
any profile whose state is a shared database, an object store or a
controller, and any profile that declares its own logs surface.

--grep without --run searches the logs of recent runs instead, scanning up
to --limit runs the run filters (--pipeline, --status, --branch, --sha,
--since, --started-after, --started-before) select and printing up to
--max-matches lines per node; -q prints only the matching run ids.

When a node's logs live in a logs service, a line framed by em dashes
follows its log when the log is not known to be whole: lines missing
after the runner sealed it, a stream that ended without the runner's
seal, or a runner that does not seal. The line is the reader's, never
part of the stored log, and JSON output omits it.

### Flags

| Flag | Description |
|---|---|
| `--run RUN_ID` | Run identifier |
| `--node NODE_ID` | Limit output to one node id |
| `--tail N` | Print only the last N lines |
| `--head N` | Print only the first N lines |
| `--lines A:B` | 1-indexed inclusive line range |
| `--grep PATTERN` | Substring match (case-sensitive) |
| `--since DURATION` | Only include nodes that started within the last D; with --grep and no --run, only runs newer than D (5m, 1h, 7d, and similar durations) |
| `--tree` | Merge root + descendant runs into one stream (local only) |
| `--events-only` | Include event records and omit node body output |
| `--no-events` | Include node body output and omit event records |
| `-f, --follow` | Tail the log(s) until the run terminates |
| `-o, --output FORMAT` | Output format: pretty\|json\|plain |
| `--profile NAME` | Profile name (omit for local-only reads) |
| `--pipeline NAME` | Cross-run search: filter runs by pipeline (repeatable; prefix `!` to exclude) |
| `--status STATUS` | Cross-run search: filter runs by status (repeatable; prefix `!` to exclude) |
| `--branch BRANCH` | Cross-run search: filter runs by git branch (repeatable; prefix `!` to exclude) |
| `--sha PREFIX` | Cross-run search: filter runs by git sha prefix (repeatable; prefix `!` to exclude) |
| `--started-after DATE` | Cross-run search: only runs whose StartedAt >= this |
| `--started-before DATE` | Cross-run search: only runs whose StartedAt <= this |
| `--limit N` | Cross-run search: max candidate runs to scan (default: 50) |
| `--max-matches N` | Cross-run search: per-node match cap (0 = no cap) (default: 5) |
| `-q, --quiet` | Cross-run search: print only the unique matching run ids |

### Examples

```sh
# Read local logs
sparkwing runs logs --run run-fictional

# Last 20 lines of a remote run
sparkwing runs logs --run run-fictional --profile prod --tail 20

# Only the most recent attempt's output
sparkwing runs logs --run run-fictional --profile prod --since 5m

# Search logs for an error substring
sparkwing runs logs --run run-fictional --grep 'permission denied'

# Find which recent failed runs logged a message
sparkwing runs logs --grep 'connection reset' --status failed --since 24h

# Merge a parent run with every descendant
sparkwing runs logs --run run-fictional --tree

# Read only structured event records
sparkwing runs logs --run run-fictional --events-only

# JSON stream for an agent
sparkwing runs logs --run run-fictional -o json

# Plain text with node/step prefix
sparkwing runs logs --run run-fictional -o plain

# Force the colored renderer when piping
sparkwing runs logs --run run-fictional -o pretty | less -R
```

## `sparkwing runs prune`

Delete finished runs older than a threshold, or by id

Prunes terminal runs (success / failed / cancelled) so the
controller's SQLite store doesn't grow unbounded. Supply either
--older-than DUR (batch by age) or one-or-more run ids via --run
(repeatable). Use --run - to read ids from stdin. The two modes
are mutually exclusive.

Use --dry-run first to confirm the matching runs.

### Flags

| Flag | Description |
|---|---|
| `--older-than DURATION` | Prune runs older than this |
| `--run RUN_ID` | Run id to prune (repeatable; use --run - to read ids from stdin) |
| `--dry-run` | List matching runs without deleting |
| `--profile NAME` | Profile name for remote runs; omit for local runs |

### Examples

```sh
# Preview what a 7-day prune would delete
sparkwing runs prune --older-than 7d --dry-run --profile prod

# Delete a few specific runs
sparkwing runs prune --run run-A --run run-B --profile prod

# Prune ids from another query
sparkwing runs list --pipeline scratch -q | sparkwing runs prune --run - --profile prod
```

## `sparkwing runs retry`

Trigger fresh runs copying pipeline + args from old ones

Issues a new trigger per source run with the same pipeline, args,
branch, and SHA. Each new run is tagged with retry_of=<old-id>.

Local retries are refused because the original execution environment is
unavailable. Captured submission environments are deleted when execution
starts. Submit a new run from the intended environment.
A queued local retry whose execution environment is unavailable also fails
before execution. Controller-backed retries use their configured execution
context; select one with --profile.

A retry is not weighed against the pipeline's risk labels the way a launch is:
it re-queues the source run's own declarations, so a retry of a run whose step
declares a Risk is queued with no allow behind it.

Pick a rerun scope explicitly:
  --failed   reuse cached/passed nodes from the source run;
             re-execute only the failed or unreached subset.
  --all      ignore prior outcomes and re-execute every node.

One of --failed or --all is required.

Pass --run once per source id (repeatable). Use --run - to read ids
from stdin, one per line. Failures on individual ids don't abort
the batch; the verb prints a per-id status line and exits non-zero
only when at least one id failed.

### Flags

| Flag | Description |
|---|---|
| `--run RUN_ID` | Source run id (repeatable; use --run - to read ids from stdin) |
| `--failed` | Rerun from failed: reuse passed nodes, re-execute only failed/unreached |
| `--all` | Rerun all: re-execute every node from scratch |
| `--profile NAME` | Profile name for remote runs; omit for local runs |

### Examples

```sh
# Rerun only the failed nodes
sparkwing runs retry --failed --run run-fictional --profile prod

# Rerun every node from scratch
sparkwing runs retry --all --run run-fictional --profile prod

# Rerun every recently failed run
sparkwing runs list --status failed --since 1h -q | sparkwing runs retry --failed --run - --profile prod
```

## `sparkwing runs stats`

Report run counts, success rate, and duration percentiles

Reports per-pipeline counts and durations over the selected run window.
Running runs contribute to counts and are excluded from duration percentiles.

--capacity reports measured duration, CPU, memory, admission charge,
queue wait, sample count, and the source of each charge. Memory charges use
peak demand; CPU charges use sustained demand. Explicit resource pins remain
in effect when measurements are reset.

Capacity profiles are local and scoped by repository identity and pipeline.
Linked worktrees and clones with the same origin share measurements.
The table shows each key as repository/pipeline.

--reset clears samples and learned demand floors. --pipeline accepts the key
shown by --capacity; a bare pipeline name matches that name across
repositories.
--all --yes resets every profile. The result reports removed rows, cleared
pinned rows, samples, and demand floors.

### Flags

| Flag | Description |
|---|---|
| `--pipeline NAME` | Restrict to one pipeline (required with --reset unless --all) |
| `--since DURATION` | Only runs newer than this (7d and similar durations) |
| `--capacity` | Show measured capacity profiles instead of run aggregates |
| `--reset` | Delete a pipeline's learned capacity profile so it re-learns (keeps pins) |
| `--all` | With --reset, reset every pipeline's learned profile |
| `--yes` | Confirm --reset --all |
| `-o, --output FORMAT` | Output format: pretty\|json\|plain |
| `--profile NAME` | Profile name; omit for local-only |

### Examples

```sh
# 7-day local stats
sparkwing runs stats --since 7d

# Prod stats as JSON
sparkwing runs stats --profile prod -o json

# Measured capacity per pipeline
sparkwing runs stats --capacity

# Reset a poisoned profile
sparkwing runs stats --reset --pipeline myrepo/build

# Reset every learned profile
sparkwing runs stats --reset --all --yes
```

## `sparkwing runs status`

Show one run's status (non-zero exit unless status=success)

Prints a summary of the run (pipeline, status, node states).
With --follow, polls until the run reaches a terminal status. Pass
--profile NAME to read from a remote controller.

A local read looks the id up in this home's own store first and then
in each standalone store, and reports which one held it. The verbs
that write to a run -- bounce, annotations add, approvals approve
and deny, debug rerun, debug replay -- write in whichever store held
it. Cancel and retry cannot act on a standalone run at all, because
no daemon arbitrates one, and say so instead of reporting it
missing.

Runs that wrote their logs to a filesystem also report log_path: the
directory holding the run's per-node .log files, on the machine that
executed the run. With -o json it is a top-level field, so an agent
holding a run id can read the logs off disk instead of scraping them
out of a stream. That machine may not be this one -- a cluster run
records its own pod-local path -- so the text output marks a directory
that is not present here; the JSON reports it as recorded. Runs whose
logs live on a controller or in an object store omit it.

Exit code contract: after rendering, 'runs status' exits 0 only when
status == success. Any non-success terminal status (failed, cancelled)
exits 1; a run that is still running when the (non-follow) read
returns also exits 1. Pass --exit-zero to inspect a known-failed run
while returning zero.

--follow --timeout D blocks until the run is terminal, polling every
--poll, then renders once. It exits 0 on success, 1 on failed or
cancelled, 2 when D elapses first, and 3 when the run cannot be read.

--view renders one view of the run instead of the status summary:
  summary   groups, work items, modifiers and annotations
  timeline  an ASCII waterfall of nodes (--steps adds steps, --width sets bars)
  receipt   the audit and cost receipt, always JSON; a local run carries
            zero cost because no rate is configured on this machine
  errors    each failed node's error chain
  tree      the run and every descendant run

### Arguments

- `[RUN_ID]` (optional) -- Run identifier, when --run is not supplied

### Flags

| Flag | Description |
|---|---|
| `--run RUN_ID` | Run identifier. Positional fallback accepted. |
| `-f, --follow` | Poll until the run reaches a terminal state |
| `-o, --output FORMAT` | Output format: pretty\|json\|plain |
| `--steps` | Render every step under every node (plain output). Failed / skipped / annotated nodes always include their steps; this flag forces success nodes too. |
| `--exit-zero` | Return exit code 0 even when the run failed/cancelled |
| `--view VIEW` | Render one view: summary\|timeline\|receipt\|errors\|tree |
| `--width N` | Timeline bar width (--view timeline) (default: 60) |
| `--timeout DURATION` | With --follow, exit 2 when the run is not terminal after this long |
| `--poll DURATION` | Poll interval while --follow waits without the live view (default: 3s) |
| `--profile NAME` | Profile name; omit for local-only |

### Examples

```sh
# Check a local run once
sparkwing runs status run-fictional

# Block until a run finishes (exit 2 after 30m)
sparkwing runs status run-fictional --follow --timeout 30m

# The run's full record as JSON
sparkwing runs status run-fictional -o json --exit-zero

# Waterfall of a run's nodes and steps
sparkwing runs status run-fictional --view timeline --steps

# Why a run failed
sparkwing runs status run-fictional --view errors

# Follow a running job to completion
sparkwing runs status --run run-fictional --follow

# Inspect a known-failed run without nonzero exit
sparkwing runs status --run run-fictional --exit-zero

# Expand every step on every node
sparkwing runs status --run run-fictional --steps

# Check a prod run
sparkwing runs status --run run-fictional --profile prod
```
