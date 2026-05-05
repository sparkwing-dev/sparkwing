# Caching

The current cache model is content-addressed **node** caching via the
`*Node.CacheKey(fn)` modifier plus a top-level `sparkwing.Key(...)`
builder. See [sdk.md](sdk.md) for the modifier reference and
[pipelines.md](pipelines.md) for usage in the Plan/Work model.

Sparkwing caches at two levels:

1. **Node-level content-addressed caching.** Each node in a Plan can
   declare a `CacheKey`. The orchestrator substitutes the first
   completed node with the same key -- same code, same inputs, same
   output, zero re-execution.
2. **Build-layer caching.** Docker layer cache, BuildKit cache mounts,
   warm PVC pool, and the dependency proxy. See
   [build-caching.md](build-caching.md) for that layer.

This doc is about (1).

## The model

```go
build := sw.Job(plan, "build", &BuildJob{}).Cache(sw.CacheOptions{
    Key: "build",
    CacheKey: func(ctx context.Context) sw.CacheKey {
        return sw.Key("build", target, sourceDigest.Get(ctx))
    },
})
```

When the orchestrator evaluates `build`, it:

1. Runs upstream dependencies so `Ref[T]` values are resolved.
2. Invokes the `CacheKeyFn` with the resolved context.
3. Looks up the key in the runs store. If a prior completion exists,
   substitutes that completion's output and records a cache-hit event.
4. Otherwise runs the node and persists its output under the key.

`CacheKey` is a node modifier, not a step. You cannot conditionally
save or restore inside a job body -- the decision is made declaratively
by the Plan and evaluated once per node.

## Building keys

```go
// Primitive parts
sparkwing.Key("deploy", target, "v1.2.3")

// Upstream output (resolve the Ref -- do NOT pass the Ref directly,
// which would hash to the node ID)
img := build.Output()              // Ref[BuildOutput]
sparkwing.Key("deploy", target, img.Get(ctx).Digest)

// Content of a file on disk (if it's a build input)
sum, _ := sparkwing.HashFile("go.sum")
sparkwing.Key("go-test", sum)
```

Determinism caveats (from `sparkwing/cachekey.go`):

- `nil` stringifies to `"<nil>"`; pass a sentinel if the distinction matters.
- Maps stringify in non-deterministic order; convert to a sorted
  `[]string` of `"k=v"` first.
- Refs default-stringify to their `NodeID`. If you want the upstream's
  *output* in the key, call `ref.Get(ctx).Field` inside the
  `CacheKeyFn`.

## What a cache hit skips

The entire node body. No `Run` invocation, no step logs, no exec. The
cached output is materialized into the downstream `Ref[T]` as if the
node had just completed. Downstream nodes observe no difference.

## Limitations

- **No partial-node caching.** The old `step.SaveCache` lets you skip
  one step inside a job. That is not expressible today; split the
  cachable work into its own node.
- **Cache retention.** Node outputs are persisted in the runs store
  under the key's row; the runs store does not GC automatically. There
  is no TTL knob yet.
- **No dependency caching helper.** The old `step.SaveCache("ruby-gems", ...)`
  / `step.RestoreCache(...)` pattern for gems / node_modules / pip does
  not have a first-class SDK replacement. Today: use the dependency
  proxy (gitcache `/proxy/...`) or a warm PVC. This is a known gap;
  open an issue if it blocks you.
- **Build-layer caching is unchanged.** See
  [build-caching.md](build-caching.md).

## Historical reference

The pre-rewrite imperative API lived in `pkg/step` and `pkg/cache` with
these entry points (all removed):

```
step.SaveCache(key, paths...)
step.RestoreCache(key)
step.CacheKey(prefix, lockfile)
step.CacheKeyFromWorkDir(prefix, lockfile, workDir)
cache.NewClient(baseURL); client.Save/Restore/Has
```

Those tarballs-on-gitcache are reachable via git log before commit
`18e1dec` if you need to resurrect anything.
