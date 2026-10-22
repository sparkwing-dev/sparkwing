<!-- GENERATED from the CLI command registry by `sparkwing commands --format markdown --output plain`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference: sparkwing runs

Every `sparkwing runs` command, flag, and argument, generated from the CLI's own command registry. All command groups are indexed in [cli-reference.md](cli-reference.md).

## `sparkwing runs`

Inspect and control pipeline runs

### Subcommands

- `annotations` -- Read or append persistent job + step annotations
- `approvals` -- List approval gates (pending and history)
- `cancel` -- Request cancellation of in-flight runs
- `grep` -- Search run log text
- `list` -- List recent pipeline runs
- `logs` -- Print a run's logs
- `prune` -- Delete finished runs older than a threshold, or by id
- `retry` -- Trigger fresh runs copying pipeline + args from old ones
- `stats` -- Report run counts, success rate, and duration percentiles
- `status` -- Read one run's status, attempts, notices and selected view

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |

## `sparkwing runs annotations`

Read or append persistent job + step annotations

### Subcommands

- `add` -- Append an annotation to a job or step
- `list` -- List annotations on a run

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |

## `sparkwing runs annotations add`

Append an annotation to a job or step

Plain emits run/job[/step]. JSON retains the controller replay result.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--run RUN_ID` | Stable run ID or per-team run number; '-' reads run IDs from stdin on cancel, retry and prune. (required) |
| `--job JOB` | Job within the selected run. |
| `--step STEP_ID` | Step within the selected job. |
| `--message TEXT` | message (required) |
| `--profile NAME` | Connection profiles come only from the user's config; allowed before the verb if the command accepts it. Local run refuses --profile. |

## `sparkwing runs annotations list`

List annotations on a run

JSON emits job_id, optional step_id, level, message and source location per annotation. Plain emits one message per line, with line breaks replaced by spaces.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--run RUN_ID` | Stable run ID or per-team run number; '-' reads run IDs from stdin on cancel, retry and prune. (required) |
| `--job JOB` | Job within the selected run. |
| `--step STEP_ID` | Step within the selected job. |
| `--steps` | steps |
| `--profile NAME` | Connection profiles come only from the user's config; allowed before the verb if the command accepts it. Local run refuses --profile. |

## `sparkwing runs approvals`

List approval gates (pending and history)

### Subcommands

- `approve` -- Approve a pending approval-gate job
- `deny` -- Deny a pending approval-gate job
- `list` -- List pending approvals (or one run's history)

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |

## `sparkwing runs approvals approve`

Approve a pending approval-gate job

The controller derives the approver from authentication. Plain emits run/job. JSON includes the approval and replay result.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--run ID` | Stable run ID or per-team run number; '-' reads run IDs from stdin on cancel, retry and prune. (required) |
| `--job JOB` | Job within the selected run. (required) |
| `--comment STR` | comment |
| `--profile NAME` | Connection profiles come only from the user's config; allowed before the verb if the command accepts it. Local run refuses --profile. |

## `sparkwing runs approvals deny`

Deny a pending approval-gate job

The controller derives the approver from authentication. Plain emits run/job. JSON includes the approval and replay result.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--run ID` | Stable run ID or per-team run number; '-' reads run IDs from stdin on cancel, retry and prune. (required) |
| `--job JOB` | Job within the selected run. (required) |
| `--comment STR` | comment |
| `--profile NAME` | Connection profiles come only from the user's config; allowed before the verb if the command accepts it. Local run refuses --profile. |

## `sparkwing runs approvals list`

List pending approvals (or one run's history)

Without --run, list pending approvals. With --run, include resolved history. Plain emits run/job.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--run RUN_ID` | Stable run ID or per-team run number; '-' reads run IDs from stdin on cancel, retry and prune. |
| `--profile NAME` | Connection profiles come only from the user's config; allowed before the verb if the command accepts it. Local run refuses --profile. |

## `sparkwing runs cancel`

Request cancellation of in-flight runs

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--run RUN_ID` | Stable run ID or per-team run number; '-' reads run IDs from stdin on cancel, retry and prune. (required) |
| `--profile NAME` | Connection profiles come only from the user's config; allowed before the verb if the command accepts it. Local run refuses --profile. |

## `sparkwing runs grep`

Search run log text

Search controller logs across matching runs. Filters apply before the run page limit. --max-matches limits matches per log subject, with zero meaning uncapped.

JSON emits matching records and a page record with next_cursor and truncated. Quiet JSON emits run_id records and sends continuation metadata to stderr. Plain emits matching lines, or run IDs with --quiet.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--job JOB` | Job within the selected run. |
| `--grep PATTERN` | Match log text only. |
| `--since DURATION` | since |
| `--profile NAME` | Connection profiles come only from the user's config; allowed before the verb if the command accepts it. Local run refuses --profile. |
| `--pipeline NAME` | Pipeline name. |
| `--status STATUS` | status |
| `--branch BRANCH` | branch Repeatable; comma-separated values are accepted. |
| `--sha PREFIX` | sha |
| `--started-after DATE` | started after |
| `--started-before DATE` | started before |
| `--limit N` | Output count at least one; paginated commands continue with --cursor. |
| `--max-matches N` | max matches |
| `-q, --quiet` | quiet |
| `--cursor CURSOR` | Continue from next_cursor using the same filters. |

## `sparkwing runs list`

List recent pipeline runs

Read runs through the selected controller. Filters apply before pagination. Runs are ordered by descending team number. --since accepts days and weeks, such as 7d or 2w.

JSON emits compact run records and a page record with next_cursor and truncated. Quiet JSON emits id records and sends continuation metadata to stderr. Plain emits run IDs.

--by-pipeline totals every matching page and retains the newest --sparkline statuses. --group-by run|job|step requires --status failed and groups the selected page.

--wait blocks until a matching run appears. --follow reads every newer matching run between polls. Grouped follow output describes each new batch. Interrupted or timed-out waits exit 3.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--pipeline NAME` | Pipeline name. |
| `--status STATUS` | status |
| `--branch BRANCH` | branch Repeatable; comma-separated values are accepted. |
| `--sha PREFIX` | sha |
| `--error SUBSTR` | error |
| `--search QUERY` | Match records; never scan log text. |
| `--since DURATION` | since |
| `--started-after DATE` | started after |
| `--started-before DATE` | started before |
| `--finished-after DATE` | finished after |
| `--finished-before DATE` | finished before |
| `--limit N` | Output count at least one; paginated commands continue with --cursor. |
| `--cursor CURSOR` | Continue from next_cursor using the same filters. |
| `-q, --quiet` | quiet |
| `--by-pipeline` | by pipeline |
| `--sparkline N` | sparkline |
| `--style STYLE` | style |
| `--repo OWNER/NAME` | Repository identity, never a checkout path; typed identity grants no authority. Repeatable; comma-separated values are accepted. |
| `--root-only` | root only |
| `--group-by KEY` | group by |
| `--wait` | wait |
| `--wait-timeout DURATION` | wait timeout |
| `-f, --follow` | Stream until the observed work is done. |
| `--profile NAME` | Connection profiles come only from the user's config; allowed before the verb if the command accepts it. Local run refuses --profile. |

## `sparkwing runs logs`

Print a run's logs

Read job-attempt and trigger, plan or eval logs through the controller. --job selects a job. --tree includes descendants. --events-only and --no-events select lifecycle events or output.

--follow consumes every available page, then waits for new records until final log segments are complete. An incomplete or interrupted observation exits 3.

JSON emits tagged log records. Plain emits log lines. Use --grep without --run to search across runs with the same filters and pagination as runs grep.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--run RUN_ID` | Stable run ID or per-team run number; '-' reads run IDs from stdin on cancel, retry and prune. |
| `--job JOB` | Job within the selected run. |
| `--tail N` | tail |
| `--head N` | head |
| `--lines A:B` | lines |
| `--grep PATTERN` | Match log text only. |
| `--since DURATION` | since |
| `--tree` | tree |
| `--events-only` | events only |
| `--no-events` | no events |
| `-f, --follow` | Stream until the observed work is done. |
| `--profile NAME` | Connection profiles come only from the user's config; allowed before the verb if the command accepts it. Local run refuses --profile. |
| `--pipeline NAME` | Pipeline name. |
| `--status STATUS` | status |
| `--branch BRANCH` | branch Repeatable; comma-separated values are accepted. |
| `--sha PREFIX` | sha |
| `--started-after DATE` | started after |
| `--started-before DATE` | started before |
| `--limit N` | Output count at least one; paginated commands continue with --cursor. |
| `--max-matches N` | max matches |
| `-q, --quiet` | quiet |
| `--cursor CURSOR` | Continue from next_cursor using the same filters. |

## `sparkwing runs prune`

Delete finished runs older than a threshold, or by id

Select --run, including --run - for IDs on stdin, or --older-than. Age selection reads every page and includes only terminal runs started before the cutoff. A failed selection deletes nothing.

--dry-run prints the selected IDs without deleting. The controller enforces retention pins. JSON reports the selected run and any structured failure. Plain emits acknowledged run IDs.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--older-than DURATION` | older than |
| `--run RUN_ID` | Stable run ID or per-team run number; '-' reads run IDs from stdin on cancel, retry and prune. |
| `--dry-run` | Preview with no side effects. |
| `--profile NAME` | Connection profiles come only from the user's config; allowed before the verb if the command accepts it. Local run refuses --profile. |

## `sparkwing runs retry`

Trigger fresh runs copying pipeline + args from old ones

Select --failed to reuse successful jobs or --all-jobs to execute the sealed plan again. Retry preserves the source request, plan, source trust and actor. The controller records the retrier separately.

Repeat --run or use --run - for newline-separated IDs on stdin. JSON emits one safe receipt or structured failure per source run. Plain emits new run IDs. Local and remote retries use the same controller API.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--run RUN_ID` | Stable run ID or per-team run number; '-' reads run IDs from stdin on cancel, retry and prune. Repeatable; comma-separated values are accepted. (required) |
| `--failed` | failed |
| `--all-jobs` | all jobs |
| `--profile NAME` | Connection profiles come only from the user's config; allowed before the verb if the command accepts it. Local run refuses --profile. |

## `sparkwing runs stats`

Report run counts, success rate, and duration percentiles

Read every matching root-run page through the selected controller. --since accepts days and weeks. JSON emits pipeline counts, success percentage, mean duration and nearest-rank P95. Plain emits pipeline names.

--sizing reads controller measurements, job rows, source, drift, memo exclusions and demand floors. --reset selects one --pipeline or --all-pipelines --yes and preserves resource pins. --since applies to run statistics only.

Sizing reset requires an authorized controller route. Until that contract exists the adapter returns policy_missing without changing state.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--pipeline NAME` | Pipeline name. |
| `--since DURATION` | since |
| `--sizing` | sizing |
| `--reset` | reset |
| `--all-pipelines` | all pipelines |
| `--yes` | Confirm bulk destruction. |
| `--profile NAME` | Connection profiles come only from the user's config; allowed before the verb if the command accepts it. Local run refuses --profile. |

## `sparkwing runs status`

Read one run's status, attempts, notices and selected view

Read the same public run detail through a controller profile or the local controller. Numeric run selectors are per-team numbers.

Views: summary, timeline, receipt, errors, tree. Summary includes recorded failing steps and errors for failed jobs. --steps includes all recorded steps. Receipts retain the controller's hashes and settled cost.

--follow streams observations until the run ends. Exit 0 means success, 1 means a failed or cancelled run, 2 means invalid usage, and 3 means the observation is incomplete. --exit-zero suppresses terminal failure only.

JSON preserves run details and notices. Plain summary prints the run status, every job's status and outcome, and failure details. Errors prints failed job IDs, tree prints run IDs, and receipt prints the receipt hash. --steps adds recorded steps to summary and tree output.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--run RUN_ID` | Stable run ID or per-team run number; '-' reads run IDs from stdin on cancel, retry and prune. (required) |
| `-f, --follow` | Stream until the observed work is done. |
| `--steps` | steps |
| `--exit-zero` | exit zero |
| `--view VIEW` | view |
| `--width N` | width |
| `--timeout DURATION` | timeout |
| `--poll DURATION` | poll |
| `--profile NAME` | Connection profiles come only from the user's config; allowed before the verb if the command accepts it. Local run refuses --profile. |
