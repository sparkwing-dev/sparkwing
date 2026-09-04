<!-- GENERATED from the CLI command registry by `sparkwing commands -o markdown`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference: sparkwing runs

Every `sparkwing runs` command, flag, and argument, generated from the CLI's own command registry. All command groups are indexed in [cli-reference.md](cli-reference.md).

## `sparkwing runs`

Inspect and control pipeline runs

Runs are the per-invocation records of pipeline execution.
Every 'sparkwing run <pipeline>' produces a run; cluster mode surfaces
the same runs remotely via the controller.

Local-mode subcommands (list, status, logs, errors) read from
~/.sparkwing/runs/. Controller-mode subcommands (cancel, retry,
prune) require a profile; 'runs logs' supports both.

### Subcommands

- `submit` -- Queue a local run and return its id immediately
- `consumer` -- Inspect or control the process that executes submitted runs
- `list` -- List recent pipeline runs
- `status` -- Show one run's status (non-zero exit unless status=success)
- `summary` -- Aggregated work view: groups, work items, modifiers, annotations
- `timeline` -- ASCII waterfall of nodes (and optional steps) for a run
- `wait` -- Block until a run reaches a terminal status
- `find` -- Find runs by source identity or pipeline
- `grep` -- Search log bodies across recent runs for a substring
- `logs` -- Print a run's logs
- `errors` -- Surface the error trail for a failed run
- `failures` -- List recent failed runs, optionally clustered
- `stats` -- Aggregate run counts, success %, avg + p95 duration
- `last` -- Print the most recent run
- `tree` -- Show a run and every descendant run as an ASCII tree
- `get` -- Emit one run's raw JSON (run + nodes)
- `receipt` -- Emit a run's audit + cost receipt as JSON
- `annotations` -- Read or append persistent node + step annotations
- `approvals` -- List approval gates (pending and history)
- `triggers` -- Fire, list, or inspect controller triggers
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
sparkwing runs annotations add --run run-... --node deploy -m 'agent: retried after 502'

# Note something on a step inside a node
sparkwing runs annotations add --run run-... --node deploy --step canary -m 'rolled out 5%'
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
sparkwing runs annotations list --run run-...

# Include per-step annotations
sparkwing runs annotations list --run run-... --steps

# One node's annotations as JSON
sparkwing runs annotations list --run run-... --node build -o json
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
sparkwing runs approvals approve --run run-20260423-143012-abcd --node approve-prod

# Approve a prod gate with a comment
sparkwing runs approvals approve --run run-... --node approve-prod --profile prod --comment "release notes ok"
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
sparkwing runs approvals deny --run run-20260423-143012-abcd --node approve-prod

# Deny a prod gate with a reason
sparkwing runs approvals deny --run run-... --node approve-prod --profile prod --comment "tests still red"
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
sparkwing runs approvals list --run run-...

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

### Flags

| Flag | Description |
|---|---|
| `--run RUN_ID` | Run id owning the job |
| `--node NODE_ID` | Job id to bounce |
| `--profile NAME` | Profile name for remote runs; omit for local runs |
| `--home DIR` | Sparkwing home holding the run (default: $SPARKWING_HOME or ~/.sparkwing) |

### Examples

```sh
# Bounce a wedged job in a local run
sparkwing runs bounce --run run-... --node build

# Bounce a job in a cluster run
sparkwing runs bounce --run run-... --node build --profile prod
```

## `sparkwing runs cancel`

Request cancellation of in-flight runs

Sends a cancel request per run to the controller. Each run
transitions to 'cancelling' and then 'cancelled' once the runner
acknowledges. Already-finished runs surface a per-id error but
don't abort the batch.

Pass --run once per id (repeatable). Use --run - to read ids
from stdin, one per line.

### Flags

| Flag | Description |
|---|---|
| `--run RUN_ID` | Run id to cancel (repeatable; use --run - to read ids from stdin) |
| `--profile NAME` | Profile name for remote runs; omit for local runs |
| `--home DIR` | Sparkwing home for local daemon and queued-run storage (default: $SPARKWING_HOME or ~/.sparkwing) |

### Examples

```sh
# Cancel one run
sparkwing runs cancel --run run-... --profile prod

# Cancel every running prod run
sparkwing runs list --status running --profile prod -q | sparkwing runs cancel --run - --profile prod
```

## `sparkwing runs consumer`

Inspect or control the process that executes submitted runs

One consumer process per sparkwing home claims queued triggers
and executes them. 'sparkwing runs submit' starts one when none
is running and the consumer exits after a quiet window, so these
verbs are for inspection and deliberate control rather than
routine use.

Exactly one consumer serves a home at a time, enforced by a file
lock rather than a PID check -- a consumer killed with SIGKILL
releases it immediately, so there is no stale state to clear. A
running dashboard consumes the same queue; whichever holds the
lock does the work and the other stands down, so a run is never
dispatched twice.

Stopping a consumer does not cancel queued runs. They stay
queued and execute when a consumer comes back. A run that is
executing when you stop it is interrupted and returned to the
queue, not failed -- it never reached a verdict, so the next
consumer re-executes it. To stop a run for good, cancel it.

A consumer records the sparkwing version it was built from. A
submission from a different build replaces it, so an upgrade takes
effect instead of the first build serving the home forever;
replacing one interrupts whatever it was executing, and that run
returns to the queue for the new consumer to re-execute.

### Subcommands

- `start` -- Start a consumer for this home if none is running
- `status` -- Report whether a consumer is resident
- `stop` -- Stop the resident consumer

## `sparkwing runs consumer start`

Start a consumer for this home if none is running

Starts the resident trigger consumer and waits until it owns the
home's queue. A no-op when one is already running.

Rarely needed by hand: 'sparkwing runs submit' does this before
it acknowledges a run.

### Flags

| Flag | Description |
|---|---|
| `--home PATH` | Sparkwing state directory (default: $SPARKWING_HOME or ~/.sparkwing) |
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
| `--home PATH` | Sparkwing state directory (default: $SPARKWING_HOME or ~/.sparkwing) |

### Examples

```sh
# Check for a resident consumer
sparkwing runs consumer status
```

## `sparkwing runs consumer stop`

Stop the resident consumer

Signals the resident consumer to drain and exit. Queued runs are
not cancelled -- they stay queued and execute when a consumer
comes back, which the next 'sparkwing runs submit' arranges.

To cancel a queued run instead, use 'sparkwing runs cancel'.

### Flags

| Flag | Description |
|---|---|
| `--home PATH` | Sparkwing state directory (default: $SPARKWING_HOME or ~/.sparkwing) |

### Examples

```sh
# Stop the resident consumer
sparkwing runs consumer stop
```

## `sparkwing runs errors`

Surface the error trail for a failed run

Walks the run's node DAG and prints the error chain for any node that failed. Quicker than paging through full logs when you only care about the terminal failure. Reads from the local run store.

### Flags

| Flag | Description |
|---|---|
| `--run RUN_ID` | Run identifier (required) |
| `-o, --output FORMAT` | Output format: pretty\|json\|plain |

### Examples

```sh
# Inspect a local failure
sparkwing runs errors --run run-20260422-142501-abcd

# As JSON
sparkwing runs errors --run run-... -o json
```

## `sparkwing runs failures`

List recent failed runs, optionally clustered

Fetches recent runs with status=failed and extracts the first failing node's step + error message for each. --group-by clusters the output by step so a systemic failure surfaces as one row with a count.

### Flags

| Flag | Description |
|---|---|
| `--pipeline NAME` | Restrict to one pipeline |
| `--git-sha SHA` | Restrict to a git SHA prefix |
| `--branch NAME` | Restrict to one git branch |
| `--repo OWNER/NAME` | Restrict to one repository |
| `--since DURATION` | Only failures newer than this (e.g. 24h, 7d) |
| `--limit N` | Max failures to analyze (default: 20) |
| `--group-by KEY` | Cluster by: step \| node |
| `-o, --output FORMAT` | Output format: pretty\|json\|plain |
| `--profile NAME` | Profile name; omit for local-only |

### Examples

```sh
# Recent local failures
sparkwing runs failures --since 24h

# Prod failures clustered by step
sparkwing runs failures --profile prod --group-by step
```

## `sparkwing runs find`

Find runs by source identity or pipeline

Searches recent runs for a match. Use --git-sha to find
the run that was fired by a specific commit; add --pipeline to
disambiguate when multiple pipelines respond to the same push. --repo
matches the repository identity stored on the run (owner/name).

With --wait, blocks until at least one match appears, up to
--find-timeout. Pairs with 'runs wait' for the push-and-follow loop:

  git push && \
  sparkwing runs find --git-sha $(git rev-parse HEAD) --pipeline X --wait --profile prod -q | \
    xargs -n1 -I{} sparkwing runs wait --run {} --profile prod

Exit code 0 on match, non-zero on timeout-without-match or
infrastructure error.

### Flags

| Flag | Description |
|---|---|
| `--git-sha SHA` | Match runs whose git SHA starts with this value (prefix match) |
| `--branch NAME` | Restrict to one git branch |
| `--pipeline NAME` | Restrict to one pipeline |
| `--repo OWNER/NAME` | Restrict to one stored repository identity |
| `--root-only` | Exclude child runs |
| `--since DURATION` | Lookback window (default: 1h) |
| `--limit N` | Max results (default: 20) |
| `--wait` | Block until at least one match appears |
| `--find-timeout DURATION` | Give up (nonzero exit) after this long when --wait is set (default: 2m) |
| `-o, --output FORMAT` | Output format: pretty\|json\|plain |
| `-q, --quiet` | Print only run ids, one per line (or a JSON array of ids with -o json) |
| `--profile NAME` | Profile name (cluster mode). Omit to search the local SQLite store. |

### Examples

```sh
# Find a run by SHA + pipeline on prod
sparkwing runs find --git-sha $(git rev-parse HEAD) --pipeline build-test-deploy --profile prod

# Block until the matching run appears
sparkwing runs find --git-sha abc123 --pipeline X --wait --profile prod

# Pipe matching ids into runs wait
sparkwing runs find --git-sha abc --wait -q --profile prod | xargs -n1 -I{} sparkwing runs wait --run {} --profile prod
```

## `sparkwing runs get`

Emit one run's raw JSON (run + nodes)

Prints a combined {run, nodes} JSON blob to stdout, plus a top-level log_path when the run wrote its logs to a filesystem (the directory on the machine that executed it). Consumed by agents and scripts that need the full store shape rather than the summary 'status' command renders.

### Flags

| Flag | Description |
|---|---|
| `--run RUN_ID` | Run identifier (required) |
| `--profile NAME` | Profile name; omit for local-only |

### Examples

```sh
# Fetch a local run as JSON
sparkwing runs get --run run-...

# Fetch a prod run
sparkwing runs get --run run-... --profile prod
```

## `sparkwing runs grep`

Search log bodies across recent runs for a substring

Walks the runs matching the filter set and substring-greps
every node's log. Reuses the same filter flags as `runs list` so
the candidate set is identical to what that verb would return.
In cluster mode the grep runs server-side per (run, node), so only
matching bytes come back over the wire.

Default output is a table of RUN / NODE / LINE / TEXT. -q
(quiet) prints just the unique matching run ids -- the usual
shape for piping into `runs logs` or `runs status`.

Exit code 0 even when there are no matches.

### Flags

| Flag | Description |
|---|---|
| `--pattern TEXT` | Substring to match (case-sensitive) (required) |
| `--pipeline NAME` | Restrict candidate runs to one pipeline (repeatable; `!` to exclude) |
| `--status STATUS` | Restrict by status (repeatable; `!` to exclude) |
| `--branch BRANCH` | Restrict by git branch (repeatable; `!` to exclude) |
| `--sha PREFIX` | Restrict by git sha prefix (repeatable; `!` to exclude) |
| `--since DURATION` | Only runs newer than this |
| `--started-after DATE` | Only runs whose StartedAt >= this |
| `--started-before DATE` | Only runs whose StartedAt <= this |
| `--limit N` | Max candidate runs to scan (default: 50) |
| `--max-matches M` | Per-node match cap (0 = no cap) (default: 5) |
| `-o, --output FORMAT` | Output format: pretty\|json\|plain (default: pretty on TTY, json when piped) |
| `-q, --quiet` | Print only the unique matching run ids |
| `--profile NAME` | Profile name; omit for local-only |

### Examples

```sh
# Find every run that hit a permission-denied line in the past week
sparkwing runs grep --pattern 'permission denied' --since 7d

# Pipe matching run ids into runs logs
sparkwing runs grep --pattern OOMKilled --since 24h -q | xargs -I{} sparkwing runs logs --run {}

# Search prod runs as JSON for an agent
sparkwing runs grep --pattern 'connection refused' --profile prod --since 24h -o json
```

## `sparkwing runs last`

Print the most recent run

Shorthand for 'runs list --limit 1' with a compact one-line output. --watch tails for new runs, reprinting whenever a newer run ID appears.

### Flags

| Flag | Description |
|---|---|
| `--pipeline NAME` | Restrict to one pipeline |
| `-w, --watch` | Tail for new runs |
| `-o, --output FORMAT` | Output format: pretty\|json\|plain |
| `--profile NAME` | Profile name; omit for local-only |

### Examples

```sh
# Local last run
sparkwing runs last

# Watch prod for new runs
sparkwing runs last --profile prod --watch
```

## `sparkwing runs list`

List recent pipeline runs

Without --profile, reads from the local run directory. With --profile NAME,
fetches from the named profile's controller. Filters compose with
AND semantics across flag types (pipeline=X AND status=Y), OR
semantics within a repeated flag (pipeline=X OR pipeline=Y).

With -q / --quiet the output is just run ids, one per line, for
shell piping:

  sparkwing runs list --pipeline X --limit 1 -q --profile prod \
      | xargs -I{} sparkwing runs logs --run {} --profile prod --follow

### Flags

| Flag | Description |
|---|---|
| `--pipeline NAME` | Filter by pipeline name (repeatable; prefix `!` to exclude) |
| `--status STATUS` | Filter by status: running\|success\|failed\|cancelled (repeatable; prefix `!` to exclude) |
| `--branch BRANCH` | Filter by git branch (repeatable; prefix `!` to exclude) |
| `--sha PREFIX` | Filter by git sha prefix (repeatable; prefix `!` to exclude) |
| `--error SUBSTR` | Substring match against the persisted failure reason |
| `--search QUERY` | Free-text search across pipeline/branch/sha/id/error; prefix a term with `-` to exclude |
| `--since DURATION` | Only runs newer than this (e.g. 1h, 24h, 7d) |
| `--started-after DATE` | Only runs whose StartedAt >= this (today, yesterday, 24h, 7d, or a date) |
| `--started-before DATE` | Only runs whose StartedAt <= this |
| `--finished-after DATE` | Only runs whose FinishedAt >= this (excludes still-running) |
| `--finished-before DATE` | Only runs whose FinishedAt <= this (excludes still-running) |
| `--limit N` | Max runs to show (default: 20) |
| `-o, --output FORMAT` | Output format: pretty\|json\|plain |
| `-q, --quiet` | Print only run ids, one per line (or JSON array of ids with -o json) |
| `--by-pipeline` | Pivot into one row per pipeline with a status sparkline of the last N runs |
| `--sparkline N` | Sparkline length when --by-pipeline is set (default: 30) |
| `--style STYLE` | Sparkline glyph style: ascii\|block\|dot (default: ascii) |
| `--profile NAME` | Profile name; omit for local-only |

### Examples

```sh
# Last 20 local runs
sparkwing runs list

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

### Flags

| Flag | Description |
|---|---|
| `--run RUN_ID` | Run identifier (required) |
| `--node NODE_ID` | Limit output to one node id |
| `--tail N` | Print only the last N lines |
| `--head N` | Print only the first N lines |
| `--lines A:B` | 1-indexed inclusive line range |
| `--grep PATTERN` | Substring match (case-sensitive) |
| `--since DURATION` | Only include nodes that started within the last D (e.g. 5m, 1h) |
| `--tree` | Merge root + descendant runs into one stream (local only) |
| `--events-only` | Include event records and omit node body output |
| `--no-events` | Include node body output and omit event records |
| `-f, --follow` | Tail the log(s) until the run terminates |
| `-o, --output FORMAT` | Output format: pretty\|json\|plain |
| `--profile NAME` | Profile name (omit for local-only reads) |

### Examples

```sh
# Read local logs
sparkwing runs logs --run run-20260422-142501-abcd

# Last 20 lines of a remote run
sparkwing runs logs --run run-... --profile prod --tail 20

# Only the most recent attempt's output
sparkwing runs logs --run run-... --profile prod --since 5m

# Search logs for an error substring
sparkwing runs logs --run run-... --grep 'permission denied'

# Merge a parent run with every descendant
sparkwing runs logs --run run-... --tree

# Read only structured event records
sparkwing runs logs --run run-... --events-only

# JSON stream for an agent
sparkwing runs logs --run run-... -o json

# Plain text with node/step prefix
sparkwing runs logs --run run-... -o plain

# Force the colored renderer when piping
sparkwing runs logs --run run-... -o pretty | less -R
```

## `sparkwing runs prune`

Delete finished runs older than a threshold, or by id

Prunes terminal runs (success / failed / cancelled) so the
controller's SQLite store doesn't grow unbounded. Supply either
--older-than DUR (batch by age) or one-or-more run ids via --run
(repeatable). Use --run - to read ids from stdin. The two modes
are mutually exclusive.

Use --dry-run first to confirm the victim list.

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

## `sparkwing runs receipt`

Emit a run's audit + cost receipt as JSON

Recomputes the per-run receipt from the run + nodes
rows on demand and prints it as JSON. The receipt bundles identity
hashes (pipeline_version_hash, inputs_hash, plan_hash, per-node
outputs_hash), per-step observability (durations, outcomes), and
runner-time and compute-cost accounting.

inputs_hash is empty when the run carries a caller-supplied
secret:"true" argument, so the receipt cannot verify guesses of that value.

Local mode reads from the SQLite store and reports zero cost because no
local billing rate is configured. --profile NAME reads from the remote
controller's receipt endpoint and uses the controller's configured rate.

### Flags

| Flag | Description |
|---|---|
| `--run RUN_ID` | Run identifier (required) |
| `-o, --output FORMAT` | Output format: json (default) |
| `--profile NAME` | Profile name; omit for local-only |

### Examples

```sh
# Local receipt as JSON
sparkwing runs receipt --run run-...

# Prod receipt
sparkwing runs receipt --run run-... --profile prod
```

## `sparkwing runs retry`

Trigger fresh runs copying pipeline + args from old ones

Issues a new trigger per source run with the same pipeline, args,
branch, and SHA. Each new run is tagged with retry_of=<old-id>.

On a local dashboard, the retry is bound to the source run's full origin
identity, Git revision, and complete plan snapshot. Sparkwing compiles and runs
an immutable detached snapshot of that recorded revision; uncommitted or later
working-tree edits are deliberately excluded. If the source checkout is gone or
any identity has drifted, the retry fails before compilation; it never falls
back to the current directory or another repo.

Pick a rerun scope explicitly:
  --failed   reuse cached/passed nodes from the source run;
             re-execute only the failed or unreached subset.
  --all      ignore prior outcomes and re-execute every node.

One of --failed or --all is required -- the silent default
caused operators to ship a partial rerun when they meant a full
one (and vice versa).

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
sparkwing runs retry --failed --run run-...

# Rerun every node from scratch
sparkwing runs retry --all --run run-...

# Rerun every recently failed run
sparkwing runs list --status failed --since 1h -q | sparkwing runs retry --failed --run - --profile prod
```

## `sparkwing runs stats`

Aggregate run counts, success %, avg + p95 duration

Per-pipeline aggregates across the last 500 root runs (or the --since window). In-flight runs count toward RUN (running) but do not contribute to timing percentiles.

--capacity switches to the measured capacity profiles admission learns from: each pipeline's p50/p99 duration, its CPU and memory distributions (p50/p95/peak across recent runs), the CPU CHARGE column holding the core figure admission actually reserves, its queue-wait p50/p99, sample count, and whether the admission charge comes from a pin, measurement, or the cold-start default. The resource percentiles show whether a pipeline is steady or spiky. Admission charges memory from the peak, because under-reserving memory recreates the oversubscription admission exists to prevent, and charges cores from each run's sustained demand instead, because the kernel time-slices a transient CPU collision and reserving a burst peak for a whole run only refuses work the box could have run. A pipeline whose pin has drifted from its measured peaks carries the exact fix. Capacity profiles are local-only and repo-scoped for runs launched inside a git repo, so same-named pipelines in different repos never share a profile. The repo scope is the repository's canonical identity: host/owner/path from its origin remote, the object store it borrows from when it has no remote, or a private hash of a local-path remote. Every tree of one repository -- the main checkout, a linked worktree, a clone in an ephemeral directory -- therefore shares one profile: a pipeline costs what it costs whichever tree runs it, and a gate cloning into a fresh directory arrives already knowing its price. A repo with no remote at all keys by its directory name. The table prints each key as its repo scope and pipeline joined with "/".

--reset clears a pipeline's learned capacity profile so it re-learns from a cold start, the escape hatch for a poisoned measurement -- one freak run that recorded an absurd peak, or a contention-ratcheted demand floor (sparkwing doctor flags those). Name the pipeline with --pipeline NAME as --capacity shows it (repo/pipeline inside a git repo); a bare pipeline name resets every repo-scoped key that carries it, a repo/pipeline name reaches every stored encoding of that profile, and the summary names each profile it reached in the same repo/pipeline form. Reset every pipeline with --all --yes. The demand floor goes whether or not measured samples sit behind it, since a pipeline that never finished a clean run is still priced off its floor. An explicit .Resources() pin is preserved: admission keeps charging the pin while the profile re-learns. The command prints how many rows were dropped, how many pinned rows were cleared, and how many samples and demand floors were discarded.

### Flags

| Flag | Description |
|---|---|
| `--pipeline NAME` | Restrict to one pipeline (required with --reset unless --all) |
| `--since DURATION` | Only runs newer than this (e.g. 7d) |
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
without the shell redline. For a blocking wait, use 'runs wait'.

### Flags

| Flag | Description |
|---|---|
| `--run RUN_ID` | Run identifier (e.g. run-20260422-142501-abcd) (required) |
| `-f, --follow` | Poll until the run reaches a terminal state |
| `-o, --output FORMAT` | Output format: pretty\|json\|plain |
| `--steps` | Render every step under every node (plain output). Failed / skipped / annotated nodes always include their steps; this flag forces success nodes too. |
| `--exit-zero` | Return exit code 0 even when the run failed/cancelled |
| `--profile NAME` | Profile name; omit for local-only |

### Examples

```sh
# Check a local run once
sparkwing runs status --run run-20260422-142501-abcd

# Follow a running job to completion
sparkwing runs status --run run-... --follow

# Inspect a known-failed run without nonzero exit
sparkwing runs status --run run-... --exit-zero

# Expand every step on every node
sparkwing runs status --run run-... --steps

# Check a prod run
sparkwing runs status --run run-... --profile prod
```

## `sparkwing runs submit`

Queue a local run and return its id immediately

Submits PIPELINE for local execution and returns as soon as the
run is durable. Unlike 'sparkwing run', which executes in your
terminal and dies with it, a submitted run is owned by a resident
consumer process: close the terminal, drop the ssh session, log
out -- the run keeps going.

The acknowledgment is a run id and the directory its logs land
in. Address the run by that id afterwards:

  sparkwing runs status --run RUN_ID
  sparkwing runs logs   --run RUN_ID --follow
  sparkwing runs cancel --run RUN_ID

Everything after PIPELINE is passed to the pipeline as arguments, so
this command's own flags go BEFORE the pipeline name:

  sparkwing runs submit --idempotency-key k deploy --env staging

A submit flag typed after the pipeline name is refused rather than
quietly handed to the pipeline. If a pipeline declares a flag by the
same name, separate the two with '--':

  sparkwing runs submit deploy -- --request-id its-own

Flags that a detached run cannot honor (--sw-index, --sw-ref,
--sw-dry-run, --sw-only, --profile, and the other run-shaping
--sw- flags) are refused with the reason rather than ignored;
run those in the foreground with 'sparkwing run'.

Resolution order for PIPELINE: the checkout you are standing in
(or -C PATH) first, then the repo registry. The chosen checkout
is recorded on the run, so the consumer executes the tree you
submitted from even when another registered checkout declares the
same pipeline name.

Each submitted run executes with an allow-listed snapshot of the
submitting environment: SPARKWING_*, GITHUB_*, PATH, HOME, HOSTNAME,
and KUBERNETES_SERVICE_HOST, minus every credential-shaped name and
value. Widen it by naming variables in SPARKWING_SUBMIT_ENV_ALLOW,
comma separated, an entry ending in '*' matching a prefix:

  SPARKWING_SUBMIT_ENV_ALLOW='AWS_PROFILE,AWS_REGION,DOCKER_*' \
    sparkwing runs submit deploy

The owner-only snapshot is a 0600 file outside the runs database,
never stored in the run or trigger row, and it is removed when the
consumer starts the run. A run that comes back to the queue without
it fails rather than running with the consumer's own environment.

Deduplication is opt-in via --idempotency-key, scoped to the
pipeline. A second submission of the SAME pipeline carrying a key
an earlier one used returns the original run id, its current
status, and creates nothing -- which is what makes a retry after a
dropped connection safe. Reusing a key with different arguments is
refused, because a key names one intent and different arguments
are a different request. --request-id is a separate, tracing-only
field: it is recorded on the run and never affects deduplication.

A consumer is started automatically if none is running, and exits
on its own after five idle minutes. See 'sparkwing runs consumer'.

### Arguments

- `pipeline` (required) -- Pipeline to run (see `sparkwing pipeline list`)
- `[args...]` (optional) -- Arguments passed through to the pipeline

### Flags

| Flag | Description |
|---|---|
| `--idempotency-key KEY` | Deduplication token; a repeat submission with this key returns the original run |
| `--request-id ID` | Tracing identifier recorded on the run; never affects deduplication |
| `-C, --cd PATH` | Resolve the pipeline from this directory instead of the current one |
| `-o, --output FORMAT` | Output format: pretty\|json\|plain |
| `--home PATH` | Sparkwing state directory (default: $SPARKWING_HOME or ~/.sparkwing) |
| `--consumer-idle DUR` | If this starts a consumer: how long it stays alive with no work (default 5m). A resident consumer keeps its own settings. |
| `--consumer-claim-lease DUR` | If this starts a consumer: the lease it stamps on each claimed run, renewed while the run executes (default 3m) |

### Examples

```sh
# Submit a run and keep the id
sparkwing runs submit nightly-report

# Submit with pipeline arguments
sparkwing runs submit deploy --env staging

# Capture the id for scripting
RUN=$(sparkwing runs submit -o plain build)

# Make a retry safe to repeat
sparkwing runs submit --idempotency-key deploy-2026-08-11-a deploy

# Submit from another checkout
sparkwing runs submit -C ~/code/other-project lint
```

## `sparkwing runs summary`

Aggregated work view: groups, work items, modifiers, annotations

Run-level rollup of every node in one render. Mirrors the
dashboard's Summary tab: run header + run-wide annotations +
node groups + work items (nodes and inner steps) + modifiers
in effect + any approval-gate state. Useful for the
"did this run actually do what I asked" agent question.

### Flags

| Flag | Description |
|---|---|
| `--run RUN_ID` | Run identifier (required) |
| `-o, --output FORMAT` | Output format: pretty\|json (default: pretty on TTY, json when piped) |
| `--profile NAME` | Profile name; omit for local-only |

### Examples

```sh
# Quick run rollup
sparkwing runs summary --run run-...

# JSON for an agent
sparkwing runs summary --run run-... -o json
```

## `sparkwing runs timeline`

ASCII waterfall of nodes (and optional steps) for a run

Renders one row per node, laid out along the run's wall-clock
span. With --steps each node also expands into its inner Work
steps. Useful for an agent reasoning about parallelism and the
critical path without correlating logs by hand. JSON output
emits start/end offsets in milliseconds per row.

### Flags

| Flag | Description |
|---|---|
| `--run RUN_ID` | Run identifier (required) |
| `--steps` | Include per-step rows under each node |
| `--width N` | Bar width in characters (default: 60) |
| `-o, --output FORMAT` | Output format: pretty\|json (default: pretty on TTY, json when piped) |
| `--profile NAME` | Profile name; omit for local-only |

### Examples

```sh
# Default node waterfall
sparkwing runs timeline --run run-...

# Expand into per-step bars
sparkwing runs timeline --run run-... --steps

# JSON for an agent
sparkwing runs timeline --run run-... --steps -o json
```

## `sparkwing runs tree`

Show a run and every descendant run as an ASCII tree

Walks parent_run_id links so cross-pipeline spawns (RunAndAwait) show up under their originating run. Local mode reads from SQLite; --profile NAME reads from the profile's controller.

### Flags

| Flag | Description |
|---|---|
| `--run RUN_ID` | Root run identifier (required) |
| `-o, --output FORMAT` | Output format: pretty\|json\|plain |
| `--profile NAME` | Profile name; omit for local-only |

### Examples

```sh
# Tree for a local run
sparkwing runs tree --run run-20260422-142501-abcd

# Tree for a prod run as JSON
sparkwing runs tree --run run-... --profile prod -o json
```

## `sparkwing runs triggers`

Fire, list, or inspect controller triggers

Triggers are the controller's queue of pending work. Every
pipeline run starts as a trigger (from a webhook, hook, 'sparkwing
run --profile', or 'triggers fire') that a worker atomically claims and
turns into a run.

'fire' posts a synthetic trigger -- the sparkwing equivalent of
'gh workflow run'. 'list' surfaces queued / in-flight / done
entries so operators can see what's stuck without diving into
controller logs. 'get' inspects one trigger by id.

Connection info comes from the selected profile (--profile NAME);
there are no --controller / --token flags on this command.

### Subcommands

- `list` -- List pending / claimed / done triggers
- `get` -- Inspect one trigger's full metadata by id

### Examples

```sh
# List pending triggers on prod
sparkwing runs triggers list --profile prod --status pending

# Inspect one trigger
sparkwing runs triggers get --id run-... --profile prod

# Fire a trigger (use pipeline run)
sparkwing pipeline run --pipeline deploy --profile prod
```

## `sparkwing runs triggers get`

Inspect one trigger's full metadata by id

Fetches GET /api/v1/triggers/{id} and prints the full row (pipeline, args, git, env, status, claim lease). Defaults to a compact multi-line rendering; -o json emits the raw response.

### Flags

| Flag | Description |
|---|---|
| `--id TRIGGER_ID` | Trigger / run identifier (same value 'fire' prints) (required) |
| `-o, --output FORMAT` | Output format: json emits the raw response |
| `--profile NAME` | Profile name (required) |

### Examples

```sh
# Inspect one trigger
sparkwing runs triggers get --id run-20260422-142501-abcd --profile prod

# Raw JSON for scripting
sparkwing runs triggers get --id run-... --profile prod -o json
```

## `sparkwing runs triggers list`

List pending / claimed / done triggers

Queries GET /api/v1/triggers on the selected profile's
controller. Empty filters return the most recent 20 entries
across all statuses.

Useful when the queue looks stuck ("why isn't my trigger being
claimed?"): --status pending shows unclaimed work, --status
claimed shows what a worker has in-flight. The repo filter
matches GITHUB_REPOSITORY on the trigger env so webhook-driven
entries narrow cleanly; that value is not indexed, so the search
covers the newest 5,000 triggers matching the other filters and
an older entry is not reported.

### Flags

| Flag | Description |
|---|---|
| `--status STATUS` | Filter by status: pending \| claimed \| done |
| `--pipeline NAME` | Filter by pipeline name |
| `--repo OWNER/NAME` | Match GITHUB_REPOSITORY on the trigger env, over the newest 5,000 triggers |
| `--limit N` | Max triggers to show (default: 20) |
| `-q, --quiet` | Print only trigger ids, newline-separated |
| `-o, --output FORMAT` | Output format: json emits the raw triggers array |
| `--profile NAME` | Profile name (required) |

### Examples

```sh
# Recent triggers on prod
sparkwing runs triggers list --profile prod

# Just pending
sparkwing runs triggers list --profile prod --status pending

# Pipeline-specific, JSON
sparkwing runs triggers list --profile prod --pipeline build-test-deploy --limit 5 -o json
```

## `sparkwing runs wait`

Block until a run reaches a terminal status

Polls the run until its status is success / failed /
cancelled, then exits. Exit code contract:

  0   status == success
  1   status == failed or cancelled
  2   timed out before the run reached a terminal status
  3+  infrastructure error (controller unreachable, run not found, ...)

Pair with 'runs find --wait' for the "push then find then wait" loop
described in the CLI wishlist.

### Flags

| Flag | Description |
|---|---|
| `--run RUN_ID` | Run identifier to wait on (required) |
| `--timeout DURATION` | Give up (exit 2) after this long (default: 10m) |
| `--poll DURATION` | Poll interval (default: 3s) |
| `-o, --output FORMAT` | Output format: pretty\|json\|plain |
| `--profile NAME` | Profile name (cluster mode). Omit to poll the local SQLite store. |

### Examples

```sh
# Wait for a local run
sparkwing runs wait --run run-20260422-142501-abcd

# Wait with a custom timeout
sparkwing runs wait --run run-... --timeout 30m --profile prod

# Tight polling on a fast run
sparkwing runs wait --run run-... --poll 500ms --profile prod
```
