<!-- GENERATED from the CLI command registry by `sparkwing commands --format markdown --output plain`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference: sparkwing commands

Every `sparkwing commands` command, flag, and argument, generated from the CLI's own command registry. All command groups are indexed in [cli-reference.md](cli-reference.md).

## `sparkwing commands`

Index of every command: one path and synopsis per line

Search command paths and synopses with --query; every word must match.
--path narrows to a subtree, with or without the leading sparkwing.
Results are lexical, at most 40 by default. JSON ends with a kind:page
record reporting total, returned, truncated and next_cursor. Continue
with --cursor and the same filters, or --limit 0 for every match.

Rows carry path, synopsis and full-tree subcommand_count. Read a selected
command with <path> --help. Hidden commands require --include-hidden.
Plain prints paths only, with continuation on stderr.

--format markdown exports the full reference and rejects query/pagination
flags. --split-dir writes generated files.

### Flags

| Flag | Description |
|---|---|
| `-q, --query TEXT` | Match every word against paths and synopses |
| `--limit N` | Maximum records; 0 returns every remaining match (default: 40) |
| `--cursor CURSOR` | Continue after next_cursor with the same filters and binary version |
| `--format markdown` | Export the full command reference as Markdown |
| `-o, --output FORMAT` | Output format: pretty \| json \| plain (default: pretty on TTY, json when piped) |
| `--split-dir DIR` | With --format markdown: write one page per top-level command group into DIR (plus a cli-reference.md index), pruning stale generated pages |
| `--path PREFIX` | Only emit commands at or under PREFIX, matched by whole path components, with or without the leading 'sparkwing' (runs, sparkwing runs, runs list, and similar paths); a prefix matching nothing is an error |
| `--include-hidden` | Also emit Hidden:true commands (default: skip) |

### Examples

```sh
# Find status commands
sparkwing commands --query status

# Just the pipelines subtree
sparkwing commands --path pipeline

# The same subtree, fully qualified
sparkwing commands --path "sparkwing pipeline"

# All paths, one per line
sparkwing commands --limit 0 -o plain
```
