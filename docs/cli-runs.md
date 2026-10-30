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
| `--run RUN_ID` | Run ID or per-team run number. (required) |
| `--job JOB` | Job within the selected run. |
| `--step STEP_ID` | Step within the selected job. |
| `--message TEXT` | Text of the info-level annotation to attach to the job, or to the step named by --step. (required) |
| `--profile NAME` | Controller connection profile from your config. Optional profiles use the saved default, then local; --profile= selects local. |

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
| `--run RUN_ID` | Run ID or per-team run number. (required) |
| `--job JOB` | Job within the selected run. |
| `--step STEP_ID` | Step within the selected job. |
| `--steps` | Also list annotations recorded on the run's steps, not only those on its jobs; --step implies this. |
| `--profile NAME` | Controller connection profile from your config. Optional profiles use the saved default, then local; --profile= selects local. |

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
| `--run ID` | Run ID or per-team run number. (required) |
| `--job JOB` | Job within the selected run. (required) |
| `--comment STR` | Comment recorded with the approval. |
| `--profile NAME` | Controller connection profile from your config. Optional profiles use the saved default, then local; --profile= selects local. |

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
| `--run ID` | Run ID or per-team run number. (required) |
| `--job JOB` | Job within the selected run. (required) |
| `--comment STR` | Comment recorded with the denial. |
| `--profile NAME` | Controller connection profile from your config. Optional profiles use the saved default, then local; --profile= selects local. |

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
| `--run RUN_ID` | Run ID or per-team run number. |
| `--profile NAME` | Controller connection profile from your config. Optional profiles use the saved default, then local; --profile= selects local. |

## `sparkwing runs cancel`

Request cancellation of in-flight runs

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--run RUN_ID` | Run ID or per-team run number. (required) |
| `--profile NAME` | Controller connection profile from your config. Optional profiles use the saved default, then local; --profile= selects local. |

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
| `--grep PATTERN` | Show only log lines matching this text. |
| `--since DURATION` | Search only runs that started within this lookback, such as 24h, 7d or 2w; 0 sets no bound, and the later of --since and --started-after wins. |
| `--profile NAME` | Controller connection profile from your config. Optional profiles use the saved default, then local; --profile= selects local. |
| `--pipeline NAME` | Pipeline name. |
| `--status STATUS` | Search only runs whose status matches exactly; prefix with ! to exclude a status. |
| `--branch BRANCH` | Search only runs on this exact branch name; several names match any of them, and a ! prefix excludes a branch. Repeatable; comma-separated values are accepted. |
| `--sha PREFIX` | Search only runs whose commit SHA starts with this prefix, ignoring case; prefix with ! to exclude. |
| `--started-after DATE` | Search only runs that started at or after this time: today, yesterday, a lookback such as 7d, YYYY-MM-DD, YYYY-MM-DD HH:MM:SS or RFC 3339, in local time. |
| `--started-before DATE` | Search only runs that started at or before this time, in the same formats as --started-after. |
| `--limit N` | Maximum number of records to print, at least one. Continue with --cursor when more exist. |
| `--max-matches N` | Most matching lines returned from each job or owner log, 5 by default; zero removes the cap. |
| `-q, --quiet` | Print each matching run ID once instead of the matching lines. |
| `--cursor CURSOR` | Continue from next_cursor using the same filters. |

## `sparkwing runs list`

List recent pipeline runs

Read runs through the selected controller. Filters apply before pagination. Runs are ordered by descending team number. --since accepts days and weeks, such as 7d or 2w.

JSON emits compact run records and a page record with next_cursor and truncated. Quiet JSON emits id records and sends continuation metadata to stderr. Plain emits run IDs.

--by-pipeline totals every matching page and retains the newest --sparkline statuses. --group-by run|job|step requires --status failed and groups the selected page.

--wait blocks until a matching run appears. --follow reads every newer matching run between polls. Grouped follow output describes each new batch. Interrupted or timed-out waits exit 3. A refused read (not found, unauthenticated or forbidden) exits 4, which retrying cannot change.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--pipeline NAME` | Pipeline name. |
| `--status STATUS` | List only runs whose status matches exactly; prefix with ! to exclude a status. |
| `--branch BRANCH` | List only runs on this exact branch name; several names match any of them, and a ! prefix excludes a branch. Repeatable; comma-separated values are accepted. |
| `--sha PREFIX` | List only runs whose commit SHA starts with this prefix, ignoring case; prefix with ! to exclude. |
| `--error SUBSTR` | List only runs whose recorded failure text, from run failures, job details and attempt failure reasons, contains this text, ignoring case. |
| `--search QUERY` | Text to match against record names and summaries; log text is not searched. |
| `--since DURATION` | List only runs that started within this lookback, such as 24h, 7d or 2w; 0 sets no bound, and the later of --since and --started-after wins. |
| `--started-after DATE` | List only runs that started at or after this time: today, yesterday, a lookback such as 7d, YYYY-MM-DD, YYYY-MM-DD HH:MM:SS or RFC 3339, in local time. |
| `--started-before DATE` | List only runs that started at or before this time, in the same formats as --started-after. |
| `--finished-after DATE` | List only runs that finished at or after this time, in the same formats as --started-after; unfinished runs are left out. |
| `--finished-before DATE` | List only runs that finished at or before this time, in the same formats as --started-after; unfinished runs are left out. |
| `--limit N` | Maximum number of records to print, at least one. Continue with --cursor when more exist. |
| `--cursor CURSOR` | Continue from next_cursor using the same filters. |
| `-q, --quiet` | Print only run IDs, or only pipeline names or group keys with --by-pipeline or --group-by. |
| `--by-pipeline` | Print one row per pipeline with run and failure totals, the last run and recent statuses, read across every matching page. |
| `--sparkline N` | Number of recent statuses kept for each pipeline with --by-pipeline, 30 by default. |
| `--style STYLE` | Characters used to draw the recent-status sparkline in pretty --by-pipeline output, ascii by default. |
| `--repo OWNER/NAME` | Repository identity as owner/name. Naming a repository grants no access. Repeatable; comma-separated values are accepted. |
| `--root-only` | List only top-level runs, leaving out runs started by another run. |
| `--group-by KEY` | Group the failed runs on this page by run, job or step, with counts and a sample error; requires --status failed and cannot be used with --by-pipeline. |
| `--wait` | Poll every 2 seconds until at least one matching run exists, exiting 3 if --wait-timeout passes first. |
| `--wait-timeout DURATION` | How long --wait polls before giving up, as a Go duration such as 30s or 5m, 2m by default; ignored without --wait. |
| `-f, --follow` | Keep streaming until the observed work finishes. |
| `--profile NAME` | Controller connection profile from your config. Optional profiles use the saved default, then local; --profile= selects local. |

## `sparkwing runs logs`

Print a run's logs

Read job-attempt and trigger, plan or eval logs through the controller. --job selects a job. --tree includes descendants. --events-only and --no-events select lifecycle events or output.

--follow consumes every available page, then waits for new records until final log segments are complete. An incomplete or interrupted observation exits 3. A run that does not exist or that the credential cannot read exits 4; stop polling on 4.

JSON emits tagged log records. Plain emits log lines. Use --grep without --run to search across runs with the same filters and pagination as runs grep.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--run RUN_ID` | Run ID or per-team run number. |
| `--job JOB` | Job within the selected run. |
| `--tail N` | Print only the last N lines of each job or owner log; by default every line is printed. |
| `--head N` | Print only the first N lines of each job or owner log; cannot be combined with --tail. |
| `--lines A:B` | Print only lines A through B, inclusive and counted from 1, of each job or owner log. |
| `--grep PATTERN` | Show only log lines matching this text. |
| `--since DURATION` | With --run, print only lines and events within this lookback, such as 10m or 2d, keeping lines without a timestamp; with --grep and no --run, search only runs that started within it. |
| `--tree` | Also include logs from the runs this run started, and from the runs those started. |
| `--events-only` | Print only lifecycle events, leaving out log output. |
| `--no-events` | Print only log output, leaving out lifecycle events. |
| `-f, --follow` | Keep streaming until the observed work finishes. |
| `--profile NAME` | Controller connection profile from your config. Optional profiles use the saved default, then local; --profile= selects local. |
| `--pipeline NAME` | Pipeline name. |
| `--status STATUS` | When searching with --grep and no --run, search only runs whose status matches exactly; prefix with ! to exclude a status. |
| `--branch BRANCH` | When searching with --grep and no --run, search only runs on this exact branch name; a ! prefix excludes a branch. Repeatable; comma-separated values are accepted. |
| `--sha PREFIX` | When searching with --grep and no --run, search only runs whose commit SHA starts with this prefix, ignoring case. |
| `--started-after DATE` | When searching with --grep and no --run, search only runs that started at or after this time: today, yesterday, a lookback such as 7d, YYYY-MM-DD or RFC 3339. |
| `--started-before DATE` | When searching with --grep and no --run, search only runs that started at or before this time, in the same formats as --started-after. |
| `--limit N` | Maximum number of records to print, at least one. Continue with --cursor when more exist. |
| `--max-matches N` | When searching with --grep and no --run, the most matching lines returned from each log, 5 by default; zero removes the cap. |
| `-q, --quiet` | When searching with --grep and no --run, print each matching run ID once instead of the matching lines. |
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
| `--older-than DURATION` | Delete every finished run that started longer ago than this, such as 72h, 30d or 4w; cannot be combined with --run. |
| `--run RUN_ID` | Run ID or per-team run number. |
| `--dry-run` | Show what would change without changing anything. |
| `--profile NAME` | Controller connection profile from your config. Optional profiles use the saved default, then local; --profile= selects local. |

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
| `--run RUN_ID` | Run ID or per-team run number. Repeatable; comma-separated values are accepted. (required) |
| `--failed` | Start a new run that reuses the source run's successful jobs and runs the rest again. |
| `--all-jobs` | Start a new run that runs every job of the source run's plan again. |
| `--profile NAME` | Controller connection profile from your config. Optional profiles use the saved default, then local; --profile= selects local. |

## `sparkwing runs stats`

Report run counts, success rate, and duration percentiles

Read every matching root-run page through the selected controller. --since accepts days and weeks. JSON emits pipeline counts, success percentage, mean duration and nearest-rank P50 and P95. Plain emits pipeline names.

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
| `--since DURATION` | Count only top-level runs that started within this lookback, such as 7d or 2w; by default every run counts. |
| `--sizing` | Show the controller's learned resource sizing for each job instead of run statistics. |
| `--reset` | Clear learned sizing for --pipeline or --all-pipelines while keeping resource pins; the controller has no route for this yet, so it fails with policy_missing and changes nothing. |
| `--all-pipelines` | Apply --reset to every pipeline; requires --reset and --yes. |
| `--yes` | Confirm a destructive change without prompting. |
| `--profile NAME` | Controller connection profile from your config. Optional profiles use the saved default, then local; --profile= selects local. |

## `sparkwing runs status`

Read one run's status, attempts, notices and selected view

Read the same public run detail through a controller profile or the local controller. Numeric run selectors are per-team numbers.

Views: summary, timeline, receipt, errors, tree. Summary includes recorded failing steps and errors for failed jobs. --steps includes all recorded steps. Receipts retain the controller's hashes and settled cost.

--follow streams observations until the run ends. Exit 0 means success or a completed dry run, 1 means a failed or cancelled run, 2 means invalid usage, 3 means the observation is incomplete and may be retried, and 4 means the run does not exist or the credential cannot read it, so polling again will not help. --exit-zero suppresses terminal failure only.

JSON preserves run details and notices: one document per observation, so --follow and the errors view (one record per failed job) are NDJSON. The exit code carries the outcome, and no error document follows the observation. Plain summary prints the run status, every job's status and outcome, and failure details. Errors prints failed job IDs, tree prints run IDs, and receipt prints the receipt hash. --steps adds recorded steps to summary and tree output.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--run RUN_ID` | Run ID or per-team run number. (required) |
| `-f, --follow` | Keep streaming until the observed work finishes. |
| `--steps` | Include every recorded step in summary and tree output, not only the failing steps summary shows by default. |
| `--exit-zero` | Exit 0 when the run ended failed or cancelled; invalid usage and incomplete observations still exit nonzero. |
| `--view VIEW` | Which view of the run to print, summary by default. |
| `--width N` | Width in characters of the timeline view's bars, 60 by default. |
| `--timeout DURATION` | With --follow, stop after this Go duration, such as 30m, and exit 3 if the run has not ended; refused without --follow. |
| `--poll DURATION` | Time between checks of the run with --follow, as a Go duration, 3s by default. |
| `--profile NAME` | Controller connection profile from your config. Optional profiles use the saved default, then local; --profile= selects local. |
