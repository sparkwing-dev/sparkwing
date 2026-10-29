<!-- GENERATED from the CLI command registry by `sparkwing commands --format markdown --output plain`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference: sparkwing commands

Every `sparkwing commands` command, flag, and argument, generated from the CLI's own command registry. All command groups are indexed in [cli-reference.md](cli-reference.md).

## `sparkwing commands`

Search the command index or export the CLI reference

Search command paths and summaries. Lists return bounded pages and a next_cursor when more matches exist. Markdown export includes the full selected command tree.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--search TEXT` | Text to match against record names and summaries; log text is not searched. |
| `--limit N` | Maximum number of records to print, at least one. Continue with --cursor when more exist. |
| `--cursor CURSOR` | Continue from next_cursor using the same filters. |
| `--format markdown` | Artifact format; markdown exports the full command reference. |
| `--split-dir DIR` | Directory for one generated Markdown page per command family. |
| `--path PREFIX` | Command path prefix, with or without the sparkwing binary name. |
| `--all` | Include entries that are hidden by default. |

### Examples

```sh
# Find status commands
sparkwing commands --search status

# Export the reference
sparkwing commands --format markdown --output plain
```
