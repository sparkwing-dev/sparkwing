# Dependency caches: `.CacheDir()`

A node declares the dependency store it needs; sparkwing restores it
before the node runs and saves it after the node's first successful
run. Same code, same behavior on a laptop, a warm runner, or a fresh
Kubernetes pod -- which is exactly where it pays: a fresh pod starts
with an empty module cache, and without a dependency cache every run
re-downloads the world through your NAT gateway.

```go
sparkwing.Job(plan, "test", runTests).
    CacheDir(sparkwing.GoModules())
```

## Run the demo

From anywhere in a sparkwing checkout:

```
bash examples/dep-cache/demo.sh
```

The script scaffolds a throwaway app (with real third-party deps) and
a one-node pipeline into a temp directory, then runs it twice:

1. **Cold.** The module cache is empty. `go test` downloads modules;
   after the node succeeds the cache is archived and saved:

   ```
   depcache: miss; will save after success  cache=go-modules key=dep-go-modules-darwin-arm64-...
   depcache: saved go-modules (244.2 kB)
   ```

2. **Warm, on a simulated fresh pod.** The script wipes GOMODCACHE the
   way a new runner pod would start, then reruns with `GOPROXY=off`:

   ```
   depcache: restored go-modules (244.2 kB)
   ok  example.com/depcache-demo
   ```

   `GOPROXY=off` makes the proof structural: if the restore hadn't
   worked, the run would fail instead of quietly re-downloading.

Nothing touches your real caches: the demo uses an isolated
`SPARKWING_HOME` and `GOMODCACHE`, and cleans up after itself.

## What to declare

- `sparkwing.GoModules()` -- the Go module cache, keyed on `go.sum`.
- `sparkwing.NpmCache()` -- npm's store (not `node_modules`; the store
  survives `npm ci`), keyed on `package-lock.json`.
- `sparkwing.Dir(path, sparkwing.KeyFromFile("Gemfile.lock"))` -- any
  directory, keyed on any file.

On a laptop the archives live under `$SPARKWING_HOME/depcache`; on a
cluster they go to the sparkwing-cache service's blob store, so every
runner pod shares one cache. See `docs/caching.md` ("Dependency
caches") for key semantics, size limits, and the best-effort
guarantee.
