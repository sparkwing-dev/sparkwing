<!-- GENERATED from the CLI command registry by `sparkwing commands -o markdown`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference: sparkwing cache

Every `sparkwing cache` command, flag, and argument, generated from the CLI's own command registry. All command groups are indexed in [cli-reference.md](cli-reference.md).

## `sparkwing cache`

Inspect or trim the compiled pipeline binary cache

Every pipeline invocation compiles .sparkwing/ to a binary keyed
on a fingerprint of its source, and those binaries are cached under
$SPARKWING_HOME/cache/pipelines. They are large -- often 90 MB or
more each -- so the cache is bounded rather than allowed to grow.

Pruning runs automatically after a compile, keeping the most
recently used entries within a byte ceiling and an entry count.
These verbs are for looking at what is cached and for reclaiming
space on demand.

### Subcommands

- `info` -- Print cache location, size, ceilings, and recent entries
- `prune` -- Evict least recently used entries down to the ceilings
- `explain` -- Show the key for a pipeline and what went into it

### Examples

```sh
# See what is cached
sparkwing cache info

# Reclaim space now
sparkwing cache prune
```

## `sparkwing cache explain`

Show a pipeline's cache key and the inputs behind it

Prints the cache key for a pipeline module, whether that key is
already cached, and every input that produced it -- the Go toolchain,
the platform, the module tree, each local replace target, a covering
go.work, and the resolved module pins -- each with its own digest and
how much it covered.

File counts note how many files git ignores and excluded, which is
the usual explanation when an edit does not trigger a rebuild.

When other cached entries came from the same checkout, each is listed
with the inputs that differ from the current key. That is the direct
answer to why a rebuild happened.

### Flags

| Flag | Description |
|---|---|
| `--dir PATH` | Pipeline module directory (default: ./.sparkwing) |
| `-o, --output FORMAT` | Output format: pretty \| json (default: pretty) |

### Examples

```sh
# Why did this rebuild?
sparkwing cache explain

# Agent-readable
sparkwing cache explain -o json
```

## `sparkwing cache info`

Print cache dir, size, ceilings, and recent entries

Lists the cache directory, its total size, the configured
ceilings, and the most recently used entries with their sizes and
last-use times. Entries are ordered by last use, which is what
pruning evicts on -- not by when they were built.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Output format: pretty \| json (default: pretty) |
| `--all` | List every entry rather than the ten most recent |

### Examples

```sh
# Human-readable
sparkwing cache info

# Agent-readable
sparkwing cache info -o json

# Every entry
sparkwing cache info --all
```

## `sparkwing cache prune`

Evict least recently used binaries down to the ceilings

Removes the least recently used cached binaries until the cache
fits both the byte ceiling and the entry ceiling. Defaults come
from $SPARKWING_CACHE_MAX_BYTES and $SPARKWING_CACHE_MAX_ENTRIES;
either accepts 0 to disable that dimension.

An execution lease protects each running binary. Prune skips active
and busy entries, bounds the number examined, and reports observed
capacity separately from removed entries. Callers making admission
decisions remeasure filesystem capacity after pruning.

### Flags

| Flag | Description |
|---|---|
| `--max-bytes SIZE` | Byte ceiling, e.g. 512MiB |
| `--max-entries N` | Entry ceiling |
| `--all` | Remove every entry, ignoring both ceilings |
| `-o, --output FORMAT` | Output format: pretty \| json (default: pretty) |

### Examples

```sh
# Trim to the configured ceilings
sparkwing cache prune

# Trim to a smaller budget
sparkwing cache prune --max-bytes 512MiB

# Reclaim everything
sparkwing cache prune --all
```
