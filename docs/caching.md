# Caching

Sparkwing caches at four levels:

1. **Job results.** `.Memoize(key, opts...)` replays a recorded result for
   the same content key, skipping execution.
2. **Dependency directories.** `.CacheDir(...)` restores dependency stores
   before execution and saves them after success. See
   [Dependency caches](#dependency-caches).
3. **Build layers.** Docker layers, BuildKit cache mounts, warm PVCs, and
   dependency proxies reuse build inputs. See [Build caching](build-caching.md).
4. **Pipeline binaries.** Sparkwing reuses a compiled pipeline until its
   source inputs change. See [Pipeline binary cache](#pipeline-binary-cache).

Result keys identify work across groups and runs.
[`Concurrency`](sdk.md#concurrency) independently bounds how many nodes run.

## The model

```go
shard := sparkwing.Job(plan, "coverage-shard-1", func(ctx context.Context) error {
    return nil
})
shard.Memoize(func(ctx context.Context) (sparkwing.CacheKey, error) {
    return sparkwing.Key("coverage", "shard-1", "v1"), nil
}, sparkwing.TTL(7*24*time.Hour))
```

When the orchestrator evaluates `shard`, it:

1. Runs upstream dependencies so `Ref[T]` values are resolved.
2. Resolves the `CacheKeyFn` after dependencies complete. A returned error,
   panic, empty key, or expired resolution deadline fails before dispatch.
3. Runs uncached for `NoCache, nil`; otherwise looks up the content key.
   A live entry replays its output and records a cache-hit event.
4. Otherwise it runs the node and persists the output under the hash.

`.Memoize()` resolves its key once per node before dispatch.

`TTL(d)` bounds how long a stored result stays reusable. Omit it for the
default (`sparkwing.DefaultCacheTTL`, 7 days); values above
`sparkwing.MaxCacheTTL` (35 days) are clamped with a plan-time warning.

## Building keys

```go
sparkwing.Key("deploy", "prod", "v1.2.3")

build := sparkwing.Job(plan, "build", func(ctx context.Context) (string, error) {
    return "example-digest", nil
})
buildOut := sparkwing.RefTo[string](build)
deploy := sparkwing.Job(plan, "deploy", func(ctx context.Context) error { return nil }).Needs(build)
deploy.Memoize(func(ctx context.Context) (sparkwing.CacheKey, error) {
    return sparkwing.Key("deploy", "prod", buildOut.Get(ctx)), nil
})
```

Choose parts with stable, distinct representations. `Key` formats each
part with `%v`, which omits type information, and separates parts with
byte `0x1e`. A part containing that separator can alias multiple parts.
Resolve a `Ref` inside the callback to key on its output; passing the
`Ref` itself keys on its node ID.

## What a cache hit skips

A hit restores the recorded typed output into the current node's row so
that downstream `Ref[T]` values resolve it. The node's action, steps, and
`Verify` check are skipped.

Declare file outputs with [`Outputs`](artifacts.md). A cache hit carries
that artifact manifest forward, and downstream [`Consumes`](artifacts.md)
stages its files. Files omitted from the manifest are not restored.

## In-flight dedupe

Nodes resolving the same key share an in-flight execution across groups
and runs against a shared controller. The first arrival computes the
result; followers wait and replay a reusable successful result. A follower
executes its own work when the leader ends without a reusable result.

## Opting out per invocation

Return `sparkwing.NoCache, nil` to run uncached for one invocation.
Return an error when the key cannot be computed; an empty key fails
resolution, even when returned with a nil error:

```go
skipCache := false
sparkwing.Job(plan, "maybe", func(ctx context.Context) error { return nil }).
    Memoize(func(ctx context.Context) (sparkwing.CacheKey, error) {
        if skipCache {
            return sparkwing.NoCache, nil
        }
        return sparkwing.Key("maybe", "v1"), nil
    })
```

`sparkwing run --sw-no-cache` disables cache *reads* for a whole run
while still writing results on success, so the next run hits a freshly
populated cache.

## Limitations

- **No partial-node caching.** Caching is per node; you cannot skip one
  step inside a job. Split the cachable work into its own node.
- **Bounded retention.** Cache entries expire after their `TTL`. The
  controller sweeps expired entries automatically on a schedule, and the
  cache is additionally capped at roughly ten thousand rows, evicting the
  least recently used entries past that cap.
- **Build-layer caching is separate.** See
  [build-caching.md](build-caching.md).

## Dependency caches

`.CacheDir()` declares dependency directories to restore before execution
and save after the node's first successful run under that key.

```go
sparkwing.Job(plan, "test", runTests).
    CacheDir(sparkwing.GoModules())

sparkwing.Job(plan, "web-test", runWebTests).
    CacheDir(sparkwing.NpmCache())

sparkwing.Job(plan, "gems", runSpecs).
    CacheDir(sparkwing.Dir("vendor/bundle", sparkwing.KeyFromFile("Gemfile.lock")))
```

Groups take the same declaration and apply it to every member:
`group.CacheDir(sparkwing.GoModules())`.

### Directory helpers

`GoModules()` targets GOMODCACHE. `NpmCache()` targets the directory
reported by `npm config get cache`. `Dir()` names a directory explicitly;
choose a key that invalidates every stale input stored there.

### Keys

The key is `dep-<name>-<GOOS>-<GOARCH>-<hash>`, where the hash is the
content of the ecosystem's lockfile (`go.sum`, `package-lock.json`,
or the `KeyFromFile` target). Editing the lockfile changes the key;
restoring its previous bytes restores the previous key. Platform is
part of the key because compiled dependency content is not portable
across it.

Restores require an exact key match.

### Storage

- **Laptop:** archives under `$SPARKWING_HOME/depcache/`.
- **Cluster:** the sparkwing-cache service's `/cache/<key>` blob
  store, reached through `SPARKWING_CACHE_URL` (node pods) or
  `SPARKWING_GITCACHE_URL` (warm runners), authenticated with the
  runner's agent token. Every pod in the cluster shares one cache.

Both use tar.gz archives.
The cache service bounds uploads at 500 MB; a larger archive logs a
warning and is skipped.

### Guarantees

A missing lockfile, an unreachable cache service, an oversized archive,
or a failed extract logs a warning and execution proceeds without the
dependency cache. A failed node never saves.
A restore is also skipped when the target directory already has
content -- a warm runner's existing cache is left alone.

For scoping a tool's cache directory to
the current worktree rather than persisting it across runs, see
`sparkwing.ToolCacheDir` in [sdk.md](sdk.md); the two compose --
`ToolCacheDir` names a directory, `Dir()` can persist one.

See `examples/dep-cache/` for a runnable cold/warm demo.

## Pipeline binary cache

Sparkwing compiles the pipeline module and stores the binary under
`$SPARKWING_HOME/cache/pipelines/v1/entries/<key>/` for reuse until its
source inputs change.

### The key

The key is a fingerprint of everything that can change the compiled
output: the Go major/minor version, `GOOS`/`GOARCH`, the `go build` flags
sparkwing passes, the contents of `.sparkwing/`, the contents of every
local `replace` target, the directives of a covering `go.work`, and the
resolved module overlays.

Contents are hashed, not timestamps -- editing a file back to its
previous bytes restores the previous key. Paths are recorded relative
to the module, and local `replace` targets are recorded by module path
rather than by where they sit on disk. Two checkouts of the same commit
therefore compute the same key from different directories and share one
compiled binary, instead of each building their own.

**Files Git ignores are excluded from the key.** Directories outside a
Git repository hash every file. Set `SPARKWING_HASH_ALL_FILES=1` when a
build depends on ignored files, including generated assets embedded by Go.

Builds pass `-trimpath`, which keeps the build directory out of the
binary. That is what lets two checkouts produce byte-identical output;
the cost is that panics report module-relative paths rather than paths
on your machine.

Builds also pass `-ldflags "-s -w"`, which drops the symbol table and
DWARF and takes roughly 30% off the binary and a third off its link
time. Panic tracebacks and `runtime/debug.ReadBuildInfo` survive; a
debugger attaching to the binary, and core-dump analysis, do not. Set
`SPARKWING_NO_BINCACHE=1` to run the pipeline through `go run .` when you
need those.

### Bounding the cache

After each new entry, Sparkwing reclaims inactive entries to fit a byte
ceiling and an entry count.

| Variable | Default | Meaning |
| --- | --- | --- |
| `SPARKWING_CACHE_MAX_BYTES` | `2GiB` | Total size ceiling. Accepts a suffix (`512MiB`, `4GB`). `0` disables. |
| `SPARKWING_CACHE_MAX_ENTRIES` | `20` | Entry count ceiling. `0` disables. |

Pruning advances through a bounded second-chance queue. An entry used
since it entered the queue moves behind the other candidates, so use
rather than build age drives retention without an unbounded directory
scan. A kernel-backed lease spans lookup through process exit; prune
skips active executions and writers rather than relying on a timing
window.

Prune bounds entry discovery and deletion. It reports logical cache bytes
removed separately from observed filesystem capacity gained. The latter is
evidence, not an admission decision: callers remeasure the filesystem after
pruning because concurrent activity can change free space.

Inspect and reclaim on demand:

```bash
sparkwing cache info                      # size, ceilings, recent entries
sparkwing cache info --all -o json        # every entry, machine-readable
sparkwing cache prune                     # trim to the configured ceilings
sparkwing cache prune --max-bytes 512MiB  # trim to a smaller budget
sparkwing cache prune --all               # reclaim everything
```

### Seeing what an entry is

Sparkwing records which checkouts have used each entry, and how often:

```
MOST RECENTLY USED (2 of 2)
  c1df5cd6-4789f450   71.1 MiB  just now   x7  ~/code/sparkwing/.sparkwing +1 more checkout(s)
  322ecb34-31432125   71.2 MiB  2d ago     x1  ~/worktrees/feature-branch/.sparkwing
```

`cache info` counts entries used by several checkouts on the `shared:` line.

### Why did it recompile?

`sparkwing cache explain` prints the key, whether it is cached, and every
input behind it with its own digest:

```
INPUTS
  go toolchain      669365bbd24f  go1.26
  platform          8828cb814901  darwin/arm64
  build flags       60dbf03edb4e  -trimpath -ldflags -s -w
  module tree       035b55fe2c64  36 files, 346.1 KiB
  replace example.com/sample/module  e68a991b153a  1439 files, 10.0 MiB (19 gitignored, excluded)
```

Comparing two checkouts input by input shows exactly what differs -- if
`module tree` matches and a replace target does not, the pipeline source
is identical and a dependency is not. The ignored-file count helps identify edits excluded from the key.

When other cached entries came from the same checkout, `explain` lists
them with the inputs that changed since, which is the direct answer to
why the last run recompiled.

To skip the binary cache entirely for one invocation, set
`SPARKWING_NO_BINCACHE=1`; sparkwing falls back to `go run .`.

### The shared artifact store

A filesystem, bucket, or controller `cache:` backend shares pipeline
binaries across machines. The publisher writes a `.sha256` sidecar, and
a fetch discards a binary whose digest differs. Store write permissions
determine who can publish binaries.

A missing sidecar causes the run to compile from source. Set
`SPARKWING_ARTIFACT_DIGEST_BACKFILL=1` only for a trusted store to accept
blobs without sidecars and write digests from the downloaded bytes.
