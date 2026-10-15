<!-- GENERATED from the CLI command registry by `sparkwing commands --format markdown --output plain`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference: sparkwing daemon

Every `sparkwing daemon` command, flag, and argument, generated from the CLI's own command registry. All command groups are indexed in [cli-reference.md](cli-reference.md).

## `sparkwing daemon`

Inspect and manage the local controller

### Subcommands

- `events` -- Read retained local controller events
- `recover-state` -- Preserve unreadable daemon state after its holders stop
- `restart` -- Restart an answering local controller
- `status` -- Report local controller status
- `stop` -- Stop the local controller

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |

## `sparkwing daemon events`

Read retained local controller events

Show the newest 50 matching records by default, in chronological order within each page. Positive --limit and opaque --cursor continue toward older records with the same filters. Newer arrivals do not shift a continuation. Changed or removed unread history requires restarting without the cursor.

JSON emits event records followed by a page record with next_cursor and truncated. Plain and pretty output send continuation instructions to stderr. --explain reads the complete run timeline, including descendants, without pagination.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--run ID` | Stable run ID or per-team run number; '-' reads run IDs from stdin on cancel, retry and prune. |
| `--since DURATION` | since |
| `--kind KIND` | kind Repeatable; comma-separated values are accepted. |
| `--incarnation N` | incarnation Must be at least 0. |
| `--limit N` | Pagination count at least one, continued with --cursor; zero is invalid. |
| `--explain` | explain |
| `--cursor CURSOR` | Continue from next_cursor using the same filters. |

## `sparkwing daemon recover-state`

Preserve unreadable daemon state after its holders stop

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--yes` | Confirm bulk destruction. (required) |

## `sparkwing daemon restart`

Restart an answering local controller

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--force` | Replace an existing object. |

## `sparkwing daemon status`

Report local controller status

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |

## `sparkwing daemon stop`

Stop the local controller

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
