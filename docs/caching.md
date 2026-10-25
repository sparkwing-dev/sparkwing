# Caching

Job memoization, dependency caches and compiled pipeline binaries have separate
keys. Build tools can also use their own layer caches. Each declaration identifies
content. The host authorizes the storage namespace independently.

## The model

`Memo` reuses a successful job's recorded output and artifacts. It accepts a
built-in input recipe or an author callback returning a JSON value. After
SkipIf permits the job, the executing host evaluates the recipe or callback and
hashes its value under the job's admission permit, before the body runs.

```go
sparkwing.Job(plan, "build", func(context.Context) error { return nil }).
    Memo(inputs.Compose(
        inputs.Files("**/*.go", "go.mod", "go.sum"),
        inputs.Env("GOOS", "GOARCH"),
        inputs.Const("toolchain-v1"),
    ), sparkwing.MemoTTL(7*24*time.Hour))
```

The `sparkwing/inputs` helpers declare recipes in the plan. `Files` selects
tracked files, `RepoFiles` selects all tracked files with optional `Ignore`
patterns, and `Tree` includes every regular file recursively, including untracked
files, while skipping symlinks. Tree roots must be relative paths inside the
checkout. Absolute paths and paths escaping the checkout are refused. Traversal
and reads use scoped filesystem handles that confine access to the checkout.
Concurrent same-inode symlink swaps inside the checkout can go undetected.
Tracked symlinks contribute their target text and entry type. Their target
contents are never read. Missing or unreadable inputs fail on the host.

Tree traversal keeps directory handles open along the current path. Deeply nested
trees can exhaust the host's file-descriptor limit and fail the job. Raise that
limit or narrow the tree input before retrying.

Patterns without a slash match basenames anywhere. A trailing slash matches a
literal directory prefix from the checkout root, so `cache/` excludes
`cache/data` but keeps `nested/cache/data`. Other patterns match full paths,
where `**` spans path segments. These rules are shared by Go and TypeScript
because the host performs all matching.

`Env` reads the effective job environment, including startup and body declarations.
Names use the platform's comparison, including case-insensitive lookup on Windows.
Unset variables differ from empty values. `Parts` declares ordered JSON values.
TypeScript refuses nonfinite numbers and `undefined`, including nested values,
because JSON transport would lose their identity. `Const` declares a nonempty
string, validated by the host. `Compose` combines recipes in order.
TypeScript provides `files`, `repoFiles`, `ignore`, `tree`, `env`, `parts`, `constant` and
`compose` with the same host semantics.

`MemoTTL` accepts at most 30 days. Missing or corrupt cached bytes produce a miss.
Lookup refusals fail the job. Publication requires committed success.

## Building keys

`MemoKey(parts...)` collects values for host hashing. It performs no hashing in
the SDK. Resolve a typed reference inside a callback when its result is an input:

```go
build := sparkwing.Job(plan, "build", &Build{})
output := sparkwing.RefTo[BuildOutput](build)
sparkwing.Job(plan, "deploy", func(context.Context) error { return nil }).
    Memo(func(ctx context.Context) (sparkwing.MemoValue, error) {
        return sparkwing.MemoKey("deploy", output.Get(ctx).Digest), nil
    })
```

Choose stable key parts that distinguish different work. Each member of a group
needs its own key when its inputs differ.

## What a cache hit skips

A hit restores the recorded output and artifact references. The host skips the
job's body and execution hooks, then settles it as memoized and releases its
job groups. A hit still waits for its concurrency slot and briefly occupies a
machine. Declare file results with
[`Artifacts`](artifacts.md); `Consumes` stages those files for a dependent job.

## Opting out per invocation

Return `sparkwing.NoMemo, nil` from a key callback to bypass reuse for that job.
`sparkwing run <pipeline> --sw-no-memo` bypasses memoization for the invocation.
Dependency directory caches remain a separate declaration.

## Dependency caches

`Cache` declares directories restored before a job attempt. A successful attempt
can save their contents. Missing caches and failed transfers allow cold execution.

```go
sparkwing.Job(plan, "test", func(ctx context.Context) error {
    _, err := sparkwing.Exec(ctx, "go", "test", "./...").Run()
    return err
}).Cache(sparkwing.GoModules())

sparkwing.Job(plan, "web-test", func(context.Context) error { return nil }).
    Cache(sparkwing.Npm())

sparkwing.Job(plan, "gems", func(context.Context) error { return nil }).
    Cache(sparkwing.Dir("vendor/bundle", sparkwing.KeyFromFile("Gemfile.lock")))
```

### Directory helpers

`GoModules()` uses `go.sum`. `Npm()` uses an npm lockfile. `Dir()` names a
directory and explicit lockfile input. `DirCache.Key(workdir)` hashes the
declaration, platform and lockfile content inside that workspace. An escaping
path, missing input or read failure returns an error.

A `Dir()` path is relative to the workspace. Plan validation refuses an absolute
path or one that leaves the workspace, and the host opens the directory through
the workspace when it restores or saves the cache, so a symlink cannot lead it
outside. `GoModules()` and `Npm()`
on a runner the run's owner does not own use the runner's own `GOMODCACHE` and
npm cache, never values the job declares.

### Storage

Local runs keep dependency-cache records and archives in the local controller,
scoped to the checkout. A remote repository and Git branch are optional. Hosts
restore through their signed data binding and refresh it after execution, so
retries save under the final attempt. A refused binding runs cold or skips saving.
Downloads verify length and SHA-256 and close before extraction publishes files.

Remote cache records use repository-bound authority and signed `/data` transfers.
The read-only cache contract permits verified default-branch memo, artifact and
dependency-cache records. It has no write or pipeline-binary fetch operation.
Attached and detached local runs use the checkout's origin default branch as a
lookup hint. The controller verifies the producing run's default-branch trust.
Local pipeline binaries stay on the machine that builds them.

### Who shares a cache

Memo, dependency-cache, output and pipeline-reference records are written
under the scope of the identity the run was sealed with at intake. A run
started by a signed GitHub App delivery writes under its verified ref. Every
other run writes under its own scope, `manual:<writer>:<ref>`:

| Run | Writer |
| --- | --- |
| CLI, API or dashboard run, and a schedule's "run now" | The submitting principal, for example `account/<id>` |
| Fire of a schedule a non-admin armed | The member who armed it, `armer/<id>`, shared across that member's schedules |
| Fire of an admin-armed default-branch schedule ([schedules](schedules.md#pause-edit-and-run)) | That schedule, `schedule/<id>` |
| Fire of a locked, overridden or non-default-branch admin-armed schedule, and an App, schedule or Actions run demoted by a branch or tag `source:` pin | That run alone |
| Child not at its parent's exact revision, under an asserted parent | The parent's writer |
| Child not at its parent's exact revision, under any other parent | The parent run, `run/<id>` |
| Run bound to a GitHub Actions push | `actions`, shared per ref |
| Retry in full or failed mode | The source run's writer |

A child at its parent's exact revision, and every retry, write under the scope
of the run they inherit from. Retries preserve the sealed writer, code and
inputs in full and failed modes. The retrier is recorded separately as an actor.
Every run writes only its own scope. Every run also
reads verified records of the repository's default branch, which intake learns
from the verified records already stored, and a verified pull request also
reads verified records of its base branch. One member's manual run therefore
cannot feed memo or cache entries to another member's runs or to scheduled,
Actions or App builds of `main`, and manual runs by different members do not
share a cache. GitHub Actions restores from the current branch, the default
branch and a pull request's base, never a sibling branch; CircleCI shares
caches across a project.

## Pipeline binary cache

Sparkwing compiles the pipeline module and stores the binary under
`$SPARKWING_HOME/cache/pipelines/v1/entries/<key>/` for reuse until its
source inputs change. Compiled pipeline binaries are built on this machine and
stay in its local cache. Explicit publication and remote memo/artifact reads do
not transfer pipeline executables.

`sparkwing cache warm --checkout /path/to/repo` prepares this local cache. A Go
checkout compiles its pipeline. A TypeScript checkout installs dependencies.
See [Warmup](sparks.md#warmup) for receipts and the process-policy integration.

### The key

The key fingerprints the inputs sparkwing controls: the Go major/minor
version, `GOOS`/`GOARCH`, the `go build` flags sparkwing passes, the
contents of `.sparkwing/`, the contents of every local `replace` target,
the directives of a covering `go.work`, and the resolved module overlays.
Captured Go build settings, including `GOFLAGS`, `GOTOOLCHAIN`, `GOEXPERIMENT`,
`CGO_ENABLED` and the module verification settings `GOSUMDB`, `GONOSUMDB`,
`GOINSECURE`, `GOPRIVATE` and `GONOPROXY`, also contribute to the key, so a
binary built with checksum verification relaxed is never reused by a run with it
on. Different build settings produce
different cache entries.

Every file Go could embed is hashed, including directories whose names begin
with `.` or `_`. When a file vanishes or changes while the key is computed, the
key is computed again; inputs that are still changing after three attempts build
without the binary cache and store nothing.

Contents are hashed, not timestamps -- editing a file back to its
previous bytes restores the previous key. Paths are recorded relative
to the module, and local `replace` targets are recorded by module path
rather than by where they sit on disk. Two checkouts of the same commit
therefore compute the same key from different directories and share one
compiled binary, instead of each building their own.

**Files Git ignores are excluded from the key.** Directories outside a
Git repository hash every file. Pass `sparkwing run --sw-hash-all-files`
when a build depends on ignored files, including generated assets embedded
by Go.

Builds pass `-trimpath`, which keeps the build directory out of the
binary. That is what lets two checkouts produce byte-identical output;
the cost is that panics report module-relative paths rather than paths
on your machine.

Builds also pass `-ldflags "-s -w"`, which drops the symbol table and
DWARF and takes roughly 30% off the binary and a third off its link
time. Panic tracebacks and `runtime/debug.ReadBuildInfo` survive. A
debugger still attaches, without variable names or line numbers, and a
core dump cannot be symbolised. Pass `sparkwing run --sw-no-bincache` to
run the pipeline from a temporary build with debug symbols when you need
those.

### Bounding the cache

After each new entry, Sparkwing reclaims inactive entries to fit a byte
ceiling and an entry count.

| [config.yaml](machine-config.md) key | Default | Meaning |
| --- | --- | --- |
| `cache.max_bytes` | `2GiB` | Total size ceiling. Accepts a suffix (`512MiB`, `4GB`). `0` disables. |
| `cache.max_entries` | `20` | Entry count ceiling. `0` disables. |

`sparkwing cache prune --max-bytes` and `--max-entries` override them for
one prune.

Pruning scans a bounded set of entries and deletes the oldest candidates first.
A kernel-backed lease spans lookup through process exit; prune skips active
executions and writers.

Prune reports logical bytes removed, measured by file length. It does not
estimate physical space reclaimed. Callers must remeasure filesystem capacity
before admitting more work because concurrent activity can change free space.

Inspect and reclaim on demand:

```bash
sparkwing cache info                      # size, ceilings, recent entries
sparkwing cache info --all-entries -o json        # every entry, machine-readable
sparkwing cache prune                     # trim to the configured ceilings
sparkwing cache prune --max-bytes 512MiB  # trim to a smaller budget
sparkwing cache prune --all-entries               # reclaim everything
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

To skip the binary cache entirely for one invocation, pass
`--sw-no-bincache`; sparkwing builds a temporary binary with debug
symbols, then runs it with your original environment. The Go toolchain floor
adjustment applies only while building.
