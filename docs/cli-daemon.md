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

Show the newest 50 matching records by default, in chronological order within each page. Positive --limit controls the bounded output; increase it to include older records.

JSON emits event records followed by a page record with truncated. Plain and pretty output warn on stderr when older matching records are omitted. --explain reads the complete run timeline, including descendants, without pagination.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--run ID` | Run ID or per-team run number. |
| `--since DURATION` | Show only events recorded within this lookback, such as 30m, 12h, 7d or 2w. |
| `--kind KIND` | Show only events of this kind. Repeatable; comma-separated values are accepted. |
| `--incarnation N` | Show only events from this numbered controller start, the N in the #N.seq column; zero shows every start. Must be at least 0. |
| `--limit N` | Maximum number of records to print, at least one. Raise it to include older records. |
| `--explain` | Print the complete admission timeline for the --run run and all its descendant runs, without pagination. |

## `sparkwing daemon recover-state`

Preserve unreadable daemon state after its holders stop

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--yes` | Confirm a destructive change without prompting. (required) |

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
