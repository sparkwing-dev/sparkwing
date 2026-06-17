# Proposal: explicit node artifacts (content-addressed file edges)

Status: draft

## Problem

A node's typed return value (`Ref[T]`) crosses node boundaries cleanly: it
is captured on completion, memoized by `Cache`, and replayed on a hit. The
files a node writes to disk do not. There is no primitive for "this node
produces files that a later node consumes," so authors reach for the
filesystem directly -- a producer writes `coverage/*.json`, a later node
globs it -- and that only works by accident:

- **It is local-only.** Nodes share a working tree solely in local
  (in-process) execution, where every node runs under one `os.Getwd()`. In
  distributed execution each node gets its own ephemeral workspace
  (`node-runner/<run>-<node>`, fresh source fetch, removed on completion),
  so a downstream node never sees an upstream node's files -- even with no
  caching involved at all. The pattern silently means different things in
  the two modes.
- **Caching makes it worse.** A `Cache` hit replays the typed output and
  runs nothing, so a producer whose real product is files hits, returns its
  output, leaves the run green, and the files are absent. A downstream
  aggregator then sees an incomplete set on a partial-hit run and computes a
  wrong result. Today the only defense is a hand-written "self-heal" that
  detects the short set and re-runs producers -- fragile, and it does not
  reproduce the original bytes.
- **The workarounds are worse than a primitive.** The two ways to get
  predictable behavior today are (1) cram everything into one job across
  multiple work steps so it shares a process and tree, or (2) hand-roll an
  object store -- every author writing bucket paths by hand, uncoordinated
  with the DAG or the cache. (1) couples unrelated work into one node to get
  a filesystem; (2) is the right idea reinvented badly each time.

The thing missing is a first-class, content-addressed file edge between
distinct nodes.

## The model

Make file handoff an **explicit, content-addressed artifact edge**, declared
on both ends and materialized by the orchestrator -- the Bazel
outputs / Nix `$out` / Turborepo outputs model, expressed as DAG edges.

- A producer **declares the files it emits**: `.Outputs("dist/**", ...)`.
- A consumer **declares the producer whose artifacts it needs**:
  `.NeedsArtifacts(producer)` (which also implies a `Needs` edge).
- On producer completion the orchestrator captures the declared paths,
  stores each blob content-addressed in the artifact store, and records a
  **manifest** (`{path, digest, mode}`) for that producer.
- Before a consumer runs, the orchestrator **stages** the producer's
  manifest blobs into the consumer's workspace at the declared relative
  paths.

Always publish on produce, always stage on consume -- in **both** modes. In
distributed mode this is the only way the files cross at all; in local mode
it makes the shared-tree accident irrelevant by doing the same explicit
thing. The artifact store is the existing `storage.ArtifactStore`
(`fs://` locally, `s3://` / `sparkwingcache` under a controller), so
cross-machine handoff is already solved by the configured backend.

### Why this is not the bastardization it could have been

A naive version -- producer-only `.Outputs()` that auto-restores into a
shared working tree -- would have blessed exactly the implicit, shared,
mutable-filesystem coupling that breaks isolation: invisible to the
consumer, working only in local mode, doing nothing useful distributed. We
reject that.

The explicit-contract model **reinforces** node isolation instead of eroding
it. Nodes stay distinct, each in its own workspace. The only thing crossing
a boundary is a named, **immutable, content-addressed, read-only** artifact
that the consumer **explicitly asked for**. No node shares a mutable
filesystem with another; the filesystem is the delivery surface, not a
shared space. That is the difference between "consume node P's `dist/`
artifact" (an explicit dependency) and "read whatever happens to be in the
cwd" (an accident).

### Caching falls out as a non-special-case

Because a consumer **always** stages its declared inputs -- hit or miss,
local or distributed -- the cache hit stops being a special path:

- On a producer **miss**, the producer runs and publishes its manifest.
- On a producer **hit**, the producer's memo entry carries a reference to
  the same manifest; nothing re-runs, but the manifest (and its blobs) are
  already in the store under the content key.
- Either way the consumer resolves "the artifacts produced by P" and stages
  them identically.

There is no "restore files on a hit" branch to wire into the three replay
seams (`applyCacheHit`, the coalesced-waiter path, the in-flight follower)
and forget one of. The original bug does not get patched; it stops being
expressible. Caching is purely an optimization layered on top: artifacts are
the dataflow, `Cache` only decides whether the producer re-executes.

This is why **`.Outputs()` does not require `.Cache()`**. A node may produce
artifacts every run for its consumers without being memoized at all;
memoization is orthogonal.

## API

Two ordinary node modifiers, peers to `Needs` / `Cache` / `Verify`. Names
are provisional.

```go
// Producer: declare the files this node emits, by glob, relative to its
// working directory. Repeatable; the union is captured.
func (n *JobNode) Outputs(globs ...string) *JobNode

// Consumer: stage the artifacts produced by `producer` into this node's
// workspace before it runs. Implies Needs(producer).
func (n *JobNode) NeedsArtifacts(producer *JobNode) *JobNode
```

```go
producer := sparkwing.Job(plan, "shard-1", run).
    Outputs("coverage/shard-1.json").
    Cache(func(ctx context.Context) sparkwing.CacheKey {
        return sparkwing.Key("coverage", "shard-1", "v1")
    })

aggregate := sparkwing.Job(plan, "aggregate", run).
    NeedsArtifacts(producer1).
    NeedsArtifacts(producer2)
// inside aggregate's run: read coverage/*.json from the workspace as before
```

## Decisions settled

- **Explicit on both ends.** A producer declares outputs; a consumer
  declares which producer's artifacts it consumes. No implicit "files in the
  cwd" channel, in either mode.
- **Always publish, always stage.** Uniform across local/distributed and
  hit/miss. The cwd-sharing that happens to exist in local mode is not
  relied upon for correctness (see the optional skip below).
- **Artifacts are independent of `Cache`.** Producing artifacts does not
  require memoization; memoization does not change how artifacts flow. This
  supersedes any rule that `.Outputs()` requires `.Cache()`.
- **Empty output sets are allowed.** A declared glob that matches nothing
  captures an empty manifest rather than failing the node -- some outputs
  are legitimately optional. (The old "incomplete set" bug came from a
  *cache hit skipping the write*, not from an empty produce; this model
  closes that regardless.)
- **Staging overwrites.** When a staged path already exists in the
  consumer's workspace, the artifact wins -- the cache/artifact is
  authoritative, which is what makes consumption deterministic.
- **Content-addressed storage, manifest by producer.** Blobs are keyed by
  their own digest (dedup across runs and producers); the per-producer
  manifest is what a consumer resolves. A memoized producer's manifest
  reference rides its memo entry so a hit stages identically to a miss.
- **Rides the existing artifact store.** `storage.ArtifactStore` already
  exists with `fs` / `s3` / `sparkwingcache` backends and a conformance
  suite. The work is threading an `ArtifactStore` into the orchestrator's
  `Backends` (today only `State` / `Logs` / `Concurrency`) and adding
  capture/stage, not building a store.

## Non-goals

- **Replacing `Ref[T]` for data.** Typed output stays the channel for
  values; artifacts are the channel for files/blobs. Both cross boundaries
  explicitly.
- **Replacing same-job multi-step for a shared mutable scratch tree.** If
  two pieces of logic genuinely need to scribble on one directory, that is
  one node with multiple work steps -- and that is correct, not a
  workaround. Artifacts are immutable handoff, not shared scratch; we do not
  try to cover that case and will not conflate them.
- **`Verify` on a cache hit.** Today `Verify` is skipped on a hit because
  nothing is on disk. With artifacts staged that rationale weakens, but
  running postconditions on a hit is a separate behavior change; out of
  scope here.
- **Artifact digests feeding consumer cache keys.** A consumer's cache key
  could incorporate the digests of the artifacts it consumes (full Bazel
  input-hashing). Useful later; not required for the dataflow to be correct.
  Authors can already fold an upstream's typed output into their key.

## Optional, deferred: skip the round-trip when local

In local mode the producer wrote its files into the shared cwd, so on a
**same-run miss** the consumer's declared inputs are already present and the
publish-then-stage round-trip is redundant work. An opt-in modifier could
skip staging in that one case to avoid latency (most visible when local
execution is pointed at a remote store).

This is explicitly **not** built first, and maybe not at all. Its sharp edge
is exactly the mode-specific divergence this proposal removes, so it must be
tightly bounded:

- It can apply **only** on a same-run local miss where the producer actually
  populated the shared tree. On a **hit** the producer did not run, so the
  files are absent and staging is mandatory -- the skip must never apply
  there.
- Publish is still required (a future run/hit must be able to stage), so the
  only thing saved is the redundant local stage.

Given how narrow the win is and that it reintroduces a mode-conditional
path, it stays opt-in, off by default, and deferred until someone
demonstrates the latency matters. Name TBD (not `.NeedsFilesystem` -- that
re-implies a shared mutable tree, the thing we are moving away from).

## Open (non-blocking)

- **Overlapping staged paths.** Two consumed producers that declare the same
  relative path collide in the consumer's workspace. v1: last stage wins
  (consistent with the overwrite rule); a plan-time warning when two
  consumed producers' manifests overlap is a candidate.
- **Stage location.** v1 stages at the producer's declared relative paths
  into the consumer's workspace root, so existing read-by-path code works
  unchanged. A namespaced layout (per-producer subdir) is an alternative if
  collisions prove common.
- **Modifier names.** `Outputs` / `NeedsArtifacts` vs `Produces` (already
  taken by the typed-output marker) / `Consumes`.

## Versioning

Additive SDK (two new modifiers; no existing signature changes) plus
orchestrator wiring (an `ArtifactStore` in `Backends`, capture on producer
completion, stage before consumer execution). Not a breaking change. Target
a minor release.
