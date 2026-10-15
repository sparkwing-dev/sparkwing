<!-- GENERATED from the CLI command registry by `sparkwing commands --format markdown --output plain`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference: sparkwing cache

Every `sparkwing cache` command, flag, and argument, generated from the CLI's own command registry. All command groups are indexed in [cli-reference.md](cli-reference.md).

## `sparkwing cache`

Inspect or trim the compiled pipeline binary cache

### Subcommands

- `explain` -- Show a pipeline's cache key and the inputs behind it
- `info` -- Print cache dir, size, ceilings, and recent entries
- `prune` -- Evict least recently used binaries down to the ceilings
- `warm` -- Prepare the local pipeline cache and dependencies

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |

## `sparkwing cache explain`

Show a pipeline's cache key and the inputs behind it

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |

## `sparkwing cache info`

Print cache dir, size, ceilings, and recent entries

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--all-entries` | all entries |
| `--docs` | docs |

## `sparkwing cache prune`

Evict least recently used binaries down to the ceilings

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--max-bytes SIZE` | max bytes |
| `--max-entries N` | max entries |
| `--all-entries` | all entries |
| `--docs` | docs |

## `sparkwing cache warm`

Prepare the local pipeline cache and dependencies

Build Go pipelines into the local binary cache or install TypeScript dependencies. Binary cache entries stay on this machine. JSON emits one preparation receipt; plain emits the prepared path.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--clear-cache` | clear cache |
