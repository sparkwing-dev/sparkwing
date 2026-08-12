# Caching

Job-level caching is content-addressed result memoization via the
`.Memoize(key, opts...)` node modifier plus the top-level
`sparkwing.Key(...)` builder. See [sdk.md](sdk.md) for the full modifier
reference and [pipelines.md](pipelines.md) for usage in the Plan/Work
model.

Sparkwing caches at four levels:

1. **Job-level content-addressed caching.** A node declares a content
   key; when a later node computes the same key, the orchestrator
   replays the first completion's output instead of re-running -- same
   code, same inputs, same output, zero re-execution.
2. **Dependency caches.** A node declares the dependency stores its
   work needs (the Go module cache, npm's store); sparkwing restores
   them before the node runs and saves them after success. The node
   always runs -- restore just makes it fast. See
   [the section below](#dependency-caches).
3. **Build-layer caching.** Docker layer cache, BuildKit cache mounts,
   warm PVC pool, and the dependency proxy. See
   [build-caching.md](build-caching.md) for that layer.
4. **Pipeline binary caching.** Your `.sparkwing/` module is a Go
   program; sparkwing compiles it and reuses the binary until the
   source changes. See [the section below](#pipeline-binary-cache).

This doc is about (1), (2), and (4).

**`.Memoize()` and `.CacheDir()` answer different questions.** `.Memoize()`
is memoization: a hit means the node does **not** run, because the
answer is already known. `.CacheDir()` is a cache volume: the node
**always** runs, and a hit means its dependency downloads are already
on disk. Porting a `actions/cache` or GitLab `cache:` block from
another CI system? That's `.CacheDir()`. Skipping work whose inputs
haven't changed? That's `.Memoize()`.

Caching is keyed on **content alone**. It carries no scope and no group:
it answers "is this the *same work*, so reuse the answer?" Bounding how
many distinct nodes run at once is a separate concern --
[`Concurrency`](sdk.md#concurrency), a named budget. The two are
independent; a node may declare either, both, or neither.

## The model

```go
shard := sparkwing.Job(plan, "coverage-shard-1", func(ctx context.Context) error {
    return nil
})
shard.Memoize(func(ctx context.Context) sparkwing.CacheKey {
    return sparkwing.Key("coverage", "shard-1", "v1")
}, sparkwing.TTL(7*24*time.Hour))
```

When the orchestrator evaluates `shard`, it:

1. Runs upstream dependencies so `Ref[T]` values are resolved.
2. Invokes the `CacheKeyFn` with the resolved context.
3. Looks up the content hash. If a live entry exists, it replays that
   output and records a cache-hit event.
4. Otherwise it runs the node and persists the output under the hash.

`.Memoize()` is a node modifier, not a step. You cannot conditionally save
or restore inside a job body -- the decision is declarative and
evaluated once per node.

`TTL(d)` bounds how long a stored result stays reusable. Omit it for the
default (`sparkwing.DefaultCacheTTL`, 7 days); values above
`sparkwing.MaxCacheTTL` (35 days) are clamped with a plan-time warning.

## Building keys

```go
sparkwing.Key("deploy", "prod", "v1.2.3")

build := sparkwing.Job(plan, "build", func(ctx context.Context) error { return nil })
buildOut := sparkwing.RefTo[string](build)
deploy := sparkwing.Job(plan, "deploy", func(ctx context.Context) error { return nil }).Needs(build)
deploy.Memoize(func(ctx context.Context) sparkwing.CacheKey {
    // resolve the Ref to put the upstream's OUTPUT in the key; passing
    // the Ref directly would hash to the node ID
    return sparkwing.Key("deploy", "prod", buildOut.Get(ctx))
})
```

Determinism caveats (from `sparkwing/cachekey.go`):

- `nil` stringifies to `"<nil>"`; pass a sentinel if the distinction matters.
- Maps stringify in non-deterministic order; convert to a sorted
  `[]string` of `"k=v"` first.
- Refs default-stringify to their `NodeID`. If you want the upstream's
  *output* in the key, call `ref.Get(ctx).Field` inside the
  `CacheKeyFn`.

## What a cache hit skips

A hit replays the node's recorded **typed output** and skips everything
else: no `Run`, no step logs, no exec, and **no `Verify`** -- the
postcondition gate does not re-run, since the output it would guard is
taken as already valid. The cached output is materialized into the
node's row and into any downstream `Ref[T]` as if the node had just
completed.

A hit restores the typed *output* and nothing else, so by itself it does
not recreate the files a node wrote to disk. Declare those files as
artifacts and they travel with the cache: a node that lists
[`Outputs`](artifacts.md) publishes its files content-addressed on every
run, and a cache hit carries the producer's artifact manifest forward
unchanged, so a downstream [`Consumes`](artifacts.md) stages the same
files whether the producer ran or hit. Caching a file-producing node is
supported -- pair `.Memoize()` with `Outputs` so the cached node's files
follow its replayed output to the nodes that need them. See
[artifacts.md](artifacts.md) for the model.

The restore is cross-run, not just in-flight: a `.Memoize()` hit from a
*previous* run writes the output onto the current run's node row, so a
downstream `RefTo[T]` resolves it -- the same as an in-flight dedupe
follower would.

## In-flight dedupe

The same content can be cache-missing and computing *right now* in two
places at once -- a burst of identical triggers, or two nodes with the
same key in one plan. Cache collapses that to a single execution: the
first arrival computes, the rest wait on the content hash and replay its
result the moment it lands. It is the same rule as a hit, one tick
earlier, so it needs no separate policy or flag -- declaring `.Memoize()`
is enough.

Because dedupe keys on content, it spans groups and runs: two nodes with
the same key dedupe even when they sit in different concurrency groups
or different runs against a shared controller.

## Opting out per invocation

A `CacheKeyFn` may return `sparkwing.NoCache` to run uncached for that
invocation -- distinct from the zero `CacheKey`, which logs a
missing-key warning:

```go
skipCache := false
sparkwing.Job(plan, "maybe", func(ctx context.Context) error { return nil }).
    Memoize(func(ctx context.Context) sparkwing.CacheKey {
        if skipCache {
            return sparkwing.NoCache
        }
        return sparkwing.Key("maybe", "v1")
    })
```

`sparkwing run --sw-no-cache` disables cache *reads* for a whole run
while still writing results on success, so the next run hits a freshly
populated cache.

## Caching a node that is also in a Skip or Fail group

When a node declares both `.Memoize()` and `.Concurrency()` on a group
whose `OnLimit` is `Skip` or `Fail`, and the cached content is being
computed in flight, the leader may resolve to the group's skip/fail
outcome rather than a successful result. An in-flight-dedupe follower
inherits a reusable successful outcome. After a failed, cancelled, or
otherwise non-reusable leader outcome, the follower runs the node itself.

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

A CI executor that starts cold -- a fresh Kubernetes pod, a rebooted
runner -- re-downloads every dependency its checks need, every run.
`.CacheDir()` is the first-class fix: declare the dependency stores a
node's work reads, and sparkwing restores them before the node runs
and saves them after the node's first successful run under that key.

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

### Helpers cache the store, not the install

`GoModules()` targets GOMODCACHE and `NpmCache()` targets npm's cache
directory (`npm config get cache`) -- the package manager's
content-addressed store -- rather than a materialized install tree
like `node_modules`. Two reasons. A store restored under a stale key
is safe by construction: the tool tops up what's missing and touches
nothing else. And the common CI invocation `npm ci` deletes
`node_modules` before installing, so restoring an install tree there
is discarded bytes. `Dir()` points anywhere you like -- including
`node_modules` for an `npm install` flow -- and you own the staleness
semantics of whatever you point it at.

### Keys

The key is `dep-<name>-<GOOS>-<GOARCH>-<hash>`, where the hash is the
content of the ecosystem's lockfile (`go.sum`, `package-lock.json`,
or the `KeyFromFile` target). Editing the lockfile changes the key;
restoring its previous bytes restores the previous key. Platform is
part of the key because compiled dependency content is not portable
across it.

Matching is exact in this iteration. A restore-keys-style prefix
fallback ("nearest older cache for this ecosystem") is a planned
follow-up; it will be safe for the ecosystem helpers precisely
because they cache stores, where a near-miss restore can only cost
bytes, never correctness.

### Storage

- **Laptop:** archives under `$SPARKWING_HOME/depcache/`.
- **Cluster:** the sparkwing-cache service's `/cache/<key>` blob
  store, reached through `SPARKWING_CACHE_URL` (node pods) or
  `SPARKWING_GITCACHE_URL` (warm runners), authenticated with the
  runner's agent token. Every pod in the cluster shares one cache.

The archive format (tar.gz, permissions and symlinks preserved) is
identical in both, so the same pipeline behaves the same everywhere.
The cache service bounds uploads at 500 MB; a larger archive logs a
warning and is skipped.

### Guarantees

Dependency caching is best-effort, always. A missing lockfile, an
unreachable cache service, an oversized archive, or a failed extract
logs a warning and the node runs as if no cache were declared. No
cache condition ever fails a node, and a failed node never saves.
A restore is also skipped when the target directory already has
content -- a warm runner's existing cache is left alone.

For scoping a *tool's* cache directory (golangci-lint and friends) to
the current worktree rather than persisting it across runs, see
`sparkwing.ToolCacheDir` in [sdk.md](sdk.md); the two compose --
`ToolCacheDir` names a directory, `Dir()` can persist one.

See `examples/dep-cache/` for a runnable cold/warm demo.

## Pipeline binary cache

Your `.sparkwing/` directory is a Go module, and sparkwing compiles it
before it can run anything. Compiling on every invocation would be
wasteful, so the binary is cached under
`$SPARKWING_HOME/cache/pipelines/v1/entries/<key>/` and reused until the source
changes.

### The key

The key is a fingerprint of everything that can change the compiled
output: the Go major/minor version, `GOOS`/`GOARCH`, the contents of
`.sparkwing/`, the contents of every local `replace` target, the
directives of a covering `go.work`, and the resolved module overlays.

Contents are hashed, not timestamps -- editing a file back to its
previous bytes restores the previous key. Paths are recorded relative
to the module, and local `replace` targets are recorded by module path
rather than by where they sit on disk. Two checkouts of the same commit
therefore compute the same key from different directories and share one
compiled binary, instead of each building their own.

**Files git ignores are excluded from the key.** A checkout accumulates
untracked local debris -- provider plugins, release outputs, coverage
data -- that no build reads and that differs on every machine. Hashing
it would make the key machine-specific and would also mean hashing
gigabytes on every invocation. `.gitignore` is committed, so every
checkout and CI runner computes the same exclusion. Directories outside
a git repository fall back to hashing everything.

If a build genuinely depends on a gitignored file -- a generated asset
pulled in by `//go:embed`, say -- set `SPARKWING_HASH_ALL_FILES=1` to
restore full hashing. That file is already invisible to your teammates
and to CI, so the durable fix is to track it.

Builds pass `-trimpath`, which keeps the build directory out of the
binary. That is what lets two checkouts produce byte-identical output;
the cost is that panics report module-relative paths rather than paths
on your machine.

### Bounding the cache

A compiled pipeline binary routinely exceeds 90 MB. The cache is
therefore bounded: after each new entry, sparkwing reclaims inactive
entries until the cache fits both a byte ceiling and an entry count.

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

A key is a content fingerprint and `-trimpath` keeps build paths out of
the binary, so an entry cannot be identified by inspecting it. Sparkwing
therefore records which checkouts have used each entry, and how often:

```
MOST RECENTLY USED (2 of 2)
  c1df5cd6-4789f450   91.8 MiB  just now   x7  ~/code/sparkwing/.sparkwing +1 more checkout(s)
  322ecb34-31432125   91.9 MiB  2d ago     x1  ~/worktrees/feature-branch/.sparkwing
```

An entry with more than one checkout is the portable key paying off: a
worktree reused the primary checkout's build instead of compiling its
own. `cache info` counts those entries on the `shared:` line.

### Why did it recompile?

`sparkwing cache explain` prints the key, whether it is cached, and every
input behind it with its own digest:

```
INPUTS
  go toolchain      669365bbd24f  go1.26
  platform          8828cb814901  darwin/arm64
  module tree       035b55fe2c64  36 files, 346.1 KiB
  replace github.com/sparkwing-dev/sparkwing  e68a991b153a  1439 files, 10.0 MiB (19 gitignored, excluded)
```

Comparing two checkouts input by input shows exactly what differs -- if
`module tree` matches and a replace target does not, the pipeline source
is identical and a dependency is not. The gitignored count is usually
the answer when an edit unexpectedly fails to trigger a rebuild.

When other cached entries came from the same checkout, `explain` lists
them with the inputs that changed since, which is the direct answer to
why the last run recompiled.

To skip the binary cache entirely for one invocation, set
`SPARKWING_NO_BINCACHE=1`; sparkwing falls back to `go run .`.
