<!-- GENERATED from the CLI command registry by `sparkwing commands -o markdown`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference: sparkwing commands

Every `sparkwing commands` command, flag, and argument, generated from the CLI's own command registry. All command groups are indexed in [cli-reference.md](cli-reference.md).

## `sparkwing commands`

Index of every command: one path and synopsis per line

The whole CLI as one index -- 139 verbs, one line each, so
"what is this CLI" is answered by reading rather than by
walking every -h page.

Drill down two ways: '<any path> --help' for one verb's flags,
arguments, and examples, or --path PREFIX to narrow this list
to a subtree. The prefix may leave off the leading 'sparkwing'
(--path runs and --path "sparkwing runs" select the same
subtree). It matches whole path components, so --path run
selects 'run' and its subcommands and not the separate 'runs'
group, and a prefix that matches nothing is an error rather
than an empty listing.

-o json emits the full record for every verb -- path, synopsis,
description, positional args, flags, examples, subcommands --
from the same Command values that power --help, as NDJSON: one
complete JSON object per line, so 'head -5' returns five whole
records instead of a truncated array. That is still 200KB for
the unfiltered surface, so pair it with --path unless you
genuinely want all of it. -o plain is one path per line for
shell consumption; -o markdown renders a reference page, and
with --split-dir writes the docs/cli-*.md reference (one page
per top-level command group plus a cli-reference.md index).

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Output format: pretty \| json \| markdown \| plain (default: pretty) |
| `--split-dir DIR` | With -o markdown: write one page per top-level command group into DIR (plus a cli-reference.md index), pruning stale generated pages |
| `--path PREFIX` | Only emit commands at or under PREFIX, matched by whole path components, with or without the leading 'sparkwing' (runs, sparkwing runs, runs list); a prefix matching nothing is an error |
| `--include-hidden` | Also emit Hidden:true commands (default: skip) |

### Examples

```sh
# Full CLI surface (agent self-discovery)
sparkwing commands

# Just the pipelines subtree
sparkwing commands --path pipeline

# The same subtree, fully qualified
sparkwing commands --path "sparkwing pipeline"

# All paths, one per line
sparkwing commands -o plain
```
