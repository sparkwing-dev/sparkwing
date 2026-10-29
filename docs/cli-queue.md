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
| `--profile PROFILE` | Controller connection profile from your config. Optional profiles use the saved default, then local; --profile= selects local. |
| `--group GROUP` | Show only this concurrency group's capacity, lease holders and waiting runs instead of the whole queue. |

## `sparkwing queue priority`

Change the priority of queued work

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--profile PROFILE` | Controller connection profile from your config. Optional profiles use the saved default, then local; --profile= selects local. |
| `--run ID` | Run ID or per-team run number. (required) |
| `--set VALUE` | New admission priority for the run on this machine: an integer, where higher is admitted sooner, or front or back to place it ahead of or behind every other waiting run; refused with --profile. (required) |
