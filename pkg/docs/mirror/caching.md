# Caching

Job-level caching is content-addressed result memoization via the
`.Cache(key, opts...)` node modifier plus the top-level
`sparkwing.Key(...)` builder. See [sdk.md](sdk.md) for the full modifier
reference and [pipelines.md](pipelines.md) for usage in the Plan/Work
model.

Sparkwing caches at two levels:

1. **Job-level content-addressed caching.** A node declares a content
   key; when a later node computes the same key, the orchestrator
   replays the first completion's output instead of re-running -- same
   code, same inputs, same output, zero re-execution.
2. **Build-layer caching.** Docker layer cache, BuildKit cache mounts,
   warm PVC pool, and the dependency proxy. See
   [build-caching.md](build-caching.md) for that layer.

This doc is about (1).

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
shard.Cache(func(ctx context.Context) sparkwing.CacheKey {
    return sparkwing.Key("coverage", "shard-1", "v1")
}, sparkwing.TTL(7*24*time.Hour))
```

When the orchestrator evaluates `shard`, it:

1. Runs upstream dependencies so `Ref[T]` values are resolved.
2. Invokes the `CacheKeyFn` with the resolved context.
3. Looks up the content hash. If a live entry exists, it replays that
   output and records a cache-hit event.
4. Otherwise it runs the node and persists the output under the hash.

`.Cache()` is a node modifier, not a step. You cannot conditionally save
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
deploy.Cache(func(ctx context.Context) sparkwing.CacheKey {
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
supported -- pair `.Cache()` with `Outputs` so the cached node's files
follow its replayed output to the nodes that need them. See
[artifacts.md](artifacts.md) for the model.

The restore is cross-run, not just in-flight: a `.Cache()` hit from a
*previous* run writes the output onto the current run's node row, so a
downstream `RefTo[T]` resolves it -- the same as an in-flight dedupe
follower would.

## In-flight dedupe

The same content can be cache-missing and computing *right now* in two
places at once -- a burst of identical triggers, or two nodes with the
same key in one plan. Cache collapses that to a single execution: the
first arrival computes, the rest wait on the content hash and replay its
result the moment it lands. It is the same rule as a hit, one tick
earlier, so it needs no separate policy or flag -- declaring `.Cache()`
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
    Cache(func(ctx context.Context) sparkwing.CacheKey {
        if skipCache {
            return sparkwing.NoCache
        }
        return sparkwing.Key("maybe", "v1")
    })
```

`sparkwing run --sw-no-cache` disables cache *reads* for a whole run
while still writing results on success, so the next run hits a freshly
populated cache.

## Limitation: caching a node that is also in a Skip or Fail group

When a node declares both `.Cache()` and `.Concurrency()` on a group
whose `OnLimit` is `Skip` or `Fail`, and the cached content is being
computed in flight, the leader may resolve to the group's skip/fail
outcome rather than a successful result. Its in-flight-dedupe followers
then inherit that non-success outcome rather than a replayed value.
This is a rare pairing; avoid combining `.Cache()` with a `Skip` or
`Fail` concurrency group on the same node. `Queue` and `CancelOthers`
groups do not have this interaction.

## Limitations

- **No partial-node caching.** Caching is per node; you cannot skip one
  step inside a job. Split the cachable work into its own node.
- **No GC.** Stored outputs live in the runs store under the content
  hash; retention is bounded by `TTL` but the store does not compact
  expired rows automatically.
- **No dependency-cache helper.** There is no first-class save/restore
  for gems / node_modules / pip tarballs. Use the dependency proxy
  (gitcache `/proxy/...`) or a warm PVC.
- **Build-layer caching is separate.** See
  [build-caching.md](build-caching.md).
