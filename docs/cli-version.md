<!-- GENERATED from the CLI command registry by `sparkwing commands --format markdown --output plain`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference: sparkwing version

Every `sparkwing version` command, flag, and argument, generated from the CLI's own command registry. All command groups are indexed in [cli-reference.md](cli-reference.md).

## `sparkwing version`

Inspect versions (CLI, SDK, sparks)

### Subcommands

- `hold` -- Show, set, or clear the operator ceiling on CLI upgrades

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--offline` | Skip the network check for the latest release and report only local version information. |
| `--changelog` | Print the embedded changelog entry for this CLI version instead of the version report. |

## `sparkwing version hold`

Show, set, or clear the operator ceiling on CLI upgrades

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--set VERSION` | Set a machine-wide version ceiling, as vMAJOR.MINOR or vMAJOR.MINOR.PATCH, that sparkwing update will not pass without --override-hold. |
| `--clear` | Remove the machine-wide version hold. |
