<!-- GENERATED from the CLI command registry by `sparkwing commands --format markdown --output plain`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference: sparkwing cache

Every `sparkwing cache` command, flag, and argument, generated from the CLI's own command registry. All command groups are indexed in [cli-reference.md](cli-reference.md).

## `sparkwing cache`

Inspect or trim the compiled pipeline binary cache

### Subcommands

- `explain` -- Show a pipeline's cache key and the inputs behind it
- `info` -- Print cache dir, size, ceilings, and recent entries
- `prune` -- Prune oldest cached binaries down to the ceilings
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
| `--all-entries` | List every managed cache entry instead of the 10 most recently used. |
| `--docs` | Report on the downloaded web docs cache instead of the pipeline binary cache; other flags are ignored. |

## `sparkwing cache prune`

Prune oldest cached binaries down to the ceilings

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--max-bytes SIZE` | Remove least recently used entries until the binary cache is at most this size, overriding the configured ceiling; zero means no size limit. |
| `--max-entries N` | Remove least recently used entries until the binary cache holds at most this many, overriding the configured ceiling; zero means no entry limit. |
| `--all-entries` | Remove every binary cache entry that is not in use, ignoring the size and entry ceilings. |
| `--docs` | Clear the downloaded web docs cache instead of pruning the binary cache; the size and entry flags are ignored. |

## `sparkwing cache warm`

Prepare the local pipeline cache and dependencies

Build Go pipelines into the local binary cache or install TypeScript dependencies. Binary cache entries stay on this machine. JSON emits one preparation receipt; plain emits the prepared path.

Go preparation uses the filtered install environment and masks compiler diagnostics. A failed build publishes no binary cache entry. Cached binaries retain the source, workspace dependencies and Go build settings that selected them.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--clear-cache` | Remove every unused pipeline binary cache entry on this machine before building, so the build starts cold. |
