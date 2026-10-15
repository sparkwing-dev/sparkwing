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
| `--limit N` | Pagination count at least one, continued with --cursor; zero is invalid. |
| `--max-matches N` | max matches |
| `-q, --quiet` | quiet |
| `--cursor CURSOR` | Continue from next_cursor using the same filters. |

## `sparkwing runs list`

List recent pipeline runs

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
| `--limit N` | Pagination count at least one, continued with --cursor; zero is invalid. |
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
| `--limit N` | Pagination count at least one, continued with --cursor; zero is invalid. |
| `--max-matches N` | max matches |
| `-q, --quiet` | quiet |
| `--cursor CURSOR` | Continue from next_cursor using the same filters. |

## `sparkwing runs prune`

Delete finished runs older than a threshold, or by id

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

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--run RUN_ID` | Stable run ID or per-team run number; '-' reads run IDs from stdin on cancel, retry and prune. (required) |
| `--failed` | failed |
| `--all-jobs` | all jobs |
| `--profile NAME` | Connection profiles come only from the user's config; allowed before the verb if the command accepts it. Local run refuses --profile. |

## `sparkwing runs stats`

Report run counts, success rate, and duration percentiles

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

Views: summary, timeline, receipt, errors, tree. --steps includes recorded steps. Receipts retain the controller's hashes and settled cost.

--follow streams observations until the run ends. Exit 0 means success, 1 means a failed or cancelled run, 2 means invalid usage, and 3 means the observation is incomplete. --exit-zero suppresses terminal failure only.

JSON preserves run details and notices. Plain prints the status, failed job IDs for errors, run IDs for tree, or the receipt hash. --steps adds job/step IDs in plain status and tree output.

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
