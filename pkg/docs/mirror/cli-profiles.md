<!-- GENERATED from the CLI command registry by `sparkwing commands --format markdown --output plain`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference: sparkwing profiles

Every `sparkwing profiles` command, flag, and argument, generated from the CLI's own command registry. All command groups are indexed in [cli-reference.md](cli-reference.md).

## `sparkwing profiles`

Manage controller connection profiles

### Subcommands

- `add` -- Add a controller connection profile
- `list` -- Print every registered profile
- `remove` -- Remove a controller connection profile
- `set` -- Update a connection or select the default profile
- `show` -- Print one controller connection profile

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |

## `sparkwing profiles add`

Add a controller connection profile

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--controller URL` | Controller URL for this connection profile. (required) |
| `--profile NAME` | Name for this saved connection; defaults to the controller host. |
| `--token-stdin` | Read the controller credential from standard input. |
| `--force` | Replace an existing object. |
| `--no-probe` | Skip checking the controller connection. |

## `sparkwing profiles list`

Print every registered profile

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |

## `sparkwing profiles remove`

Remove a controller connection profile

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--profile NAME` | Controller connection profile from your config. Optional profiles use the saved default, then local; --profile= selects local. (required) |
| `--keep-token` | Remove only the local connection, without revoking its token at the controller. |

## `sparkwing profiles set`

Update a connection or select the default profile

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--profile NAME` | Controller connection profile from your config. Optional profiles use the saved default, then local; --profile= selects local. (required) |
| `--controller URL` | Controller URL for this connection profile. |
| `--token-stdin` | Read the controller credential from standard input. |
| `--default` | Use this profile when an optional --profile is omitted. --default=false restores local selection if this is the default. |

## `sparkwing profiles show`

Print one controller connection profile

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--profile NAME` | Controller connection profile from your config. Optional profiles use the saved default, then local; --profile= selects local. (required) |
