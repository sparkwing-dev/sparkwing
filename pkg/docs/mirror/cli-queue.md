<!-- GENERATED from the CLI command registry by `sparkwing commands --format markdown --output plain`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference: sparkwing queue

Every `sparkwing queue` command, flag, and argument, generated from the CLI's own command registry. All command groups are indexed in [cli-reference.md](cli-reference.md).

## `sparkwing queue`

Inspect queued work and scheduling priority

### Subcommands

- `list` -- List running and queued work with expected start and finish
- `priority` -- Change the priority of queued work

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |

## `sparkwing queue list`

List running and queued work with expected start and finish

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--profile PROFILE` | Connection profiles come only from the user's config; allowed before the verb if the command accepts it. Local run refuses --profile. |
| `--group GROUP` | group |

## `sparkwing queue priority`

Change the priority of queued work

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--profile PROFILE` | Connection profiles come only from the user's config; allowed before the verb if the command accepts it. Local run refuses --profile. |
| `--run ID` | Stable run ID or per-team run number; '-' reads run IDs from stdin on cancel, retry and prune. (required) |
| `--set VALUE` | set (required) |
| `--group GROUP` | group |
