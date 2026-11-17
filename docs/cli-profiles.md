<!-- GENERATED from the CLI command registry by `sparkwing commands --format markdown --output plain`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference: sparkwing profiles

Every `sparkwing profiles` command, flag, and argument, generated from the CLI's own command registry. All command groups are indexed in [cli-reference.md](cli-reference.md).

## `sparkwing profiles`

Manage controller connection profiles

Manage saved controller connections. Pass --profile to select one for a command.

### Subcommands

- `add` -- Add a controller connection profile
- `list` -- Print every registered profile
- `delete` -- Remove a controller connection profile
- `set` -- Update a profile's controller URL or token
- `show` -- Print one controller connection profile

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |

### Examples

```sh
# List saved connections
sparkwing profiles list
```

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

### Examples

```sh
# Save a controller connection, reading the token from stdin
sparkwing profiles add --controller https://ci.example.com --profile prod --token-stdin
```

## `sparkwing profiles delete`

Remove a controller connection profile

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--profile NAME` | Controller connection profile from your config. Must be supplied explicitly. (required) |
| `--keep-token` | Remove only the local connection, without revoking its token at the controller. |

## `sparkwing profiles list`

Print every registered profile

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |

### Examples

```sh
# List saved controller connections
sparkwing profiles list
```

## `sparkwing profiles set`

Update a profile's controller URL or token

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--profile NAME` | Controller connection profile from your config. Must be supplied explicitly. (required) |
| `--controller URL` | Controller URL for this connection profile. |
| `--token-stdin` | Read the controller credential from standard input. |

## `sparkwing profiles show`

Print one controller connection profile

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--profile NAME` | Controller connection profile from your config. Must be supplied explicitly. (required) |
