<!-- GENERATED from the CLI command registry by `sparkwing commands --format markdown --output plain`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference: sparkwing update

Every `sparkwing update` command, flag, and argument, generated from the CLI's own command registry. All command groups are indexed in [cli-reference.md](cli-reference.md).

## `sparkwing update`

Update the CLI binary

Update installed machine software using verified release assets. Re-render managed timers and restart an installed local-controller service through one upgrade hook, preserving schedules and configuration. A CLI update reports that installed runner services update themselves from their controller; it does not restart them. Repository SDK pins use sparkwing upgrade. --dry-run performs neither action.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--dry-run` | Show what would change without changing anything. |
| `--force` | Replace an existing object. |
| `--override-hold` | Allow the update to go past the operator's version hold. |
| `--version TAG` | CLI release tag to install instead of the latest release. |
