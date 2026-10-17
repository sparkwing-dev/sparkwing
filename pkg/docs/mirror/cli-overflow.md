<!-- GENERATED from the CLI command registry by `sparkwing commands --format markdown --output plain`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference: sparkwing overflow

Every `sparkwing overflow` command, flag, and argument, generated from the CLI's own command registry. All command groups are indexed in [cli-reference.md](cli-reference.md).

## `sparkwing overflow`

Manage your Cloud overflow allowance

### Subcommands

- `show` -- Show your monthly overflow allowance and usage
- `set` -- Set or remove your monthly overflow cap

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |

## `sparkwing overflow set`

Set or remove your monthly overflow cap

Set your monthly Cloud allowance with --minutes, or remove your saved cap with --uncapped. Exactly one is required. Zero prevents new Cloud overflow while local and registered-machine work remain eligible. Running work finishes. JSON returns the saved allowance and usage. Plain prints the allowance in minutes or uncapped.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--profile NAME` | Connection profiles come only from the user's config; allowed before the verb if the command accepts it. Local run refuses --profile. (required) |
| `--minutes N` | minutes Must be at least 0. |
| `--uncapped` | uncapped |

## `sparkwing overflow show`

Show your monthly overflow allowance and usage

Report the authenticated account's monthly Cloud allowance, used seconds and UTC period start. A null limit means uncapped. Plain prints the allowance in minutes or uncapped. Team standing and billing admission still apply.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--profile NAME` | Connection profiles come only from the user's config; allowed before the verb if the command accepts it. Local run refuses --profile. (required) |
