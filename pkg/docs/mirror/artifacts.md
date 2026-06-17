# Node artifacts

Artifacts move **files** between nodes. A producer node declares the
files it emits; a consumer node declares which producers it draws from,
and the orchestrator stages those files into the consumer's workspace
before it runs. The transfer is explicit and content-addressed: a
consumer never reaches into another node's working directory, and the
files it receives are an immutable snapshot of what the producer
published.

Use artifacts for files. Use [`Ref[T]`](sdk.md) for data values --- a
number, a struct, a computed string passed as a typed output. The two
are complementary: a node can return a typed `Ref[T]` *and* publish
artifacts in the same run.

## The model

A producer declares its output files by glob with `Outputs`, relative to
its working directory:

```go
build := sparkwing.Job(plan, "build", func(ctx context.Context) error {
    return nil
}).Outputs("dist/**")
```

A consumer declares `Consumes(producer)`. That stages the producer's
published files into the consumer's workspace before it runs, and
implies `Needs(producer)` so the producer is ordered first:

```go
sparkwing.Job(plan, "deploy", func(ctx context.Context) error {
    return nil
}).Consumes(build)
```

By default staged files land at the paths the producer declared them
under (`dist/...`). Pass `Into` to relocate the whole set under a prefix
with the producer's internal structure preserved:

```go
sparkwing.Job(plan, "archive", func(ctx context.Context) error {
    return nil
}).Consumes(build, sparkwing.Into("artifacts/build"))
```

`Into` applies to the whole producer; per-file remapping is intentionally
absent, since it would couple the consumer to the producer's filenames.

Consuming a producer that declared no `Outputs` is a plan-time error ---
there is nothing to stage, so the edge is a mistake the plan rejects up
front rather than a silent no-op. When one consumer draws two producers
whose staged paths overlap, the plan emits a lint warning and the
last-staged producer wins at that path.

Both modifiers exist at group scope too: `JobGroup.Outputs` declares the
same globs on every member, and `JobGroup.Consumes` stages a producer
into every member's workspace. See the
[SDK reference](sdk-reference.md) for the full signatures.

## Always publish, always stage

Publishing and staging are independent of memoization. A node that
declares `Outputs` publishes its files every run, whether or not it also
declares [`.Cache()`](caching.md). A consumer stages its declared
artifacts every time it runs.

A producer that promises outputs it cannot deliver fails: if a declared
file is unreadable when the orchestrator captures it, the node fails
rather than publishing a partial set. A glob that legitimately matches
nothing records an empty set instead --- some outputs are optional, and
absence is not failure.

## Immutable, content-addressed edges

Each published file is stored under the digest of its own bytes, so
identical files across runs and producers store once. A producer's
published set is described by a manifest --- the list of relative paths
and their content digests --- stored under the manifest's own digest.
The producer node records that one digest.

A consumer stages by reading the producer's recorded manifest digest,
fetching the manifest, and writing each file's bytes at the recorded
path with the recorded permissions. Because the edge is the digest, the
files a consumer receives are exactly the bytes the producer published;
they cannot drift between publish and stage.

This is what lets caching and artifacts compose. A
[cache hit](caching.md) replays a producer's typed output without
re-running it, and carries the producer's artifact manifest forward
unchanged --- so a downstream `Consumes` stages the same files whether
the producer ran or hit. Caching a file-producing node is supported:
pair `.Cache()` with `Outputs`.

## Both execution modes

Artifacts flow the same way wherever a node runs. In local in-process
execution the orchestrator captures and stages against the node's
working directory directly. In distributed execution each node runs in
its own worker pod with a fresh workspace; the worker resolves the
shared artifact store and stages the producer's files into the pod
before the body runs, then publishes the node's outputs back to the
store on success. The producer and consumer never share a filesystem ---
the store is the only channel --- which is why the edge has to be
content-addressed rather than a path handoff.

## Non-goals

- **Passing data, not files.** A node's computed value --- a version
  string, a count, a struct --- travels as a typed [`Ref[T]`](sdk.md),
  not as an artifact. Artifacts carry files; `Ref[T]` carries data.
- **A shared mutable scratch tree.** Artifacts are immutable snapshots
  handed from one node to another, not a working directory several nodes
  read and write in place. When steps need a shared mutable tree, keep
  them as steps within one job --- a job's steps share one workspace.
- **Reaching into another node's directory.** A consumer receives only
  what a producer declared with `Outputs`, staged through the store.
  There is no path by which a node reads another running node's
  workspace.
