<!-- GENERATED from the CLI command registry by `sparkwing commands --format markdown --output plain`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference: sparkwing cache

Every `sparkwing cache` command, flag, and argument, generated from the CLI's own command registry. All command groups are indexed in [cli-reference.md](cli-reference.md).

## `sparkwing cache`

Inspect or trim the compiled pipeline binary cache

Inspect and maintain the local pipeline build cache.

### Subcommands

- `explain` -- Show this repository's pipeline binary cache key and its inputs
- `status` -- Print cache dir, size, ceilings, and recent entries
- `prune` -- Prune oldest cached binaries down to the ceilings
- `warm` -- Prepare the local pipeline cache and dependencies

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |

### Examples

```sh
# Explain the next pipeline build
sparkwing cache explain
```

## `sparkwing cache explain`

Show this repository's pipeline binary cache key and its inputs

Show the inputs that determine whether this repository's pipeline binary can be reused.

Explain the .sparkwing directory found from the working directory or -C DIR. Every pipeline in a repository shares one binary, so the command takes no pipeline name.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |

### Examples

```sh
# Show why the next run reuses or rebuilds the pipeline binary
sparkwing cache explain
```

## `sparkwing cache prune`

Prune oldest cached binaries down to the ceilings

Remove old, unused pipeline binaries to meet cache limits. Use --docs to clear downloaded documentation instead.

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

### Examples

```sh
# Prune unused binaries to the configured limits
sparkwing cache prune
```

## `sparkwing cache status`

Print cache dir, size, ceilings, and recent entries

Show the local pipeline cache's size, limits and recent entries. Use --docs to inspect the downloaded docs cache.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--all-entries` | List every managed cache entry instead of the 10 most recently used. |
| `--docs` | Report on the downloaded web docs cache instead of the pipeline binary cache; other flags are ignored. |

### Examples

```sh
# Inspect the pipeline binary cache
sparkwing cache status
```

## `sparkwing cache warm`

Prepare the local pipeline cache and dependencies

Prepare this repository's pipelines for a later run by compiling Go source or installing TypeScript dependencies.

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

### Examples

```sh
# Prepare this repository's pipelines
sparkwing cache warm
```
