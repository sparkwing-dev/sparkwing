<!-- GENERATED from the CLI command registry by `sparkwing commands --format markdown --output plain`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference: sparkwing runners

Every `sparkwing runners` command, flag, and argument, generated from the CLI's own command registry. All command groups are indexed in [cli-reference.md](cli-reference.md).

## `sparkwing runners`

Manage runners

### Subcommands

- `add` -- Register a runner
- `list` -- List runners and their capacity
- `remove` -- Remove a runner

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |

## `sparkwing runners add`

Register a runner

Use team credentials; only a team admin sets --reserved-for. --repos is the explicit allow-list spelling exception.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--runner RUNNER` | Runner name or selection. |
| `--repos PATTERN` | repos Repeatable; comma-separated values are accepted. |
| `--label LABEL` | label Repeatable; comma-separated values are accepted. |
| `--max-concurrent N` | max concurrent |
| `--contribution SPEC` | contribution |
| `--config PATH` | config |
| `--force` | Replace an existing object. |
| `--no-service` | no service |
| `--profile NAME` | Connection profiles come only from the user's config; allowed before the verb if the command accepts it. Local run refuses --profile. |
| `--reserved-for POOL` | reserved for Repeatable; comma-separated values are accepted. |

## `sparkwing runners list`

List runners and their capacity

Read the same runner details from the local controller or a selected profile. JSON retains granted identity, reservations, labels, capacity, active jobs and headroom. Plain emits names; quiet JSON emits name objects.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--profile NAME` | Connection profiles come only from the user's config; allowed before the verb if the command accepts it. Local run refuses --profile. |
| `-q, --quiet` | quiet |

## `sparkwing runners remove`

Remove a runner

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--config PATH` | config |
| `--no-service` | no service |
| `--profile NAME` | Connection profiles come only from the user's config; allowed before the verb if the command accepts it. Local run refuses --profile. |
| `--runner RUNNER` | Runner name or selection. |
