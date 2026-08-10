# Sparks Libraries

Reference for the sparks library ecosystem: the `spark.json` manifest,
the consumer `sparks:` block in `.sparkwing/sparkwing.yaml`, version
resolution, and the `sparkwing pipeline sparks` CLI.

## What a sparks library is

A sparks library is a normal Go module that declares itself as a sparks
library by placing a `spark.json` manifest at its module root. It exposes
opinionated helpers (ArgoCD sync, ECR auth, Kustomize deploy, language-specific
`go vet` / `goimports` style checks, etc.) that do not belong in the
unopinionated sparkwing SDK. Consumers import its Go packages directly; the
manifest and the resolver only layer discoverability, version pinning, and
update ergonomics on top of standard Go module mechanics.

There is no plugin binary, no dynamic loader, no runtime injection. A sparks
library is code that consumers import and link at pipeline compile time.

## Relationship to the SDK

The split is deliberate and stable.

**In the SDK (`sparkwing/` package):** unopinionated, language-and-tooling-agnostic
primitives.

- Docker: `Build`, `BuildAndPush`, `Push`, `Login`, `ComputeTags`
- Git: `ShortCommit`, `IsDirty`, `FilesetHash`, `CurrentBranch`, `TagsAtHead`, `PushTag`
- Services: `WithServices` (docker-run backed sidecars)
- Approval gates: `JobApproval` with `ApprovalConfig` (`ApprovalApprove` /
  `ApprovalDeny` / `ApprovalFail` expiry policies)
- Plan / modifiers: `CacheKey`, `Requires`, `RunAndAwait`,
  typed `Ref[T]` outputs

**In a sparks library:** anything with deep opinions on specific tooling.

- ArgoCD sync, Kustomize patch, `deploy.Run` composite
- ECR detection, AWS profile discovery, netrc seeding
- Go-specific checks (`GoFmt`, `GoVet`, `GoTest`) in sparks-core's `checks`
  block module
- Toolchain helpers for other languages (Ruby, Python, Java)
- Anything that ties a pipeline to a specific registry, control plane, or
  cloud provider

The rule of thumb: if the helper would make zero sense outside one opinionated
stack, it belongs in a sparks library, not the SDK.

## `spark.json` schema

Every sparks library places a `spark.json` file at its root - the module
root for a single-module library, the repository root for a multi-module
monorepo. It is valid JSON with the following fields.

| Field | Type | Required | Description |
|---|---|---|---|
| `name` | string | yes | Short library name. Must be unique in a consumer's `sparks:` block. Conventionally matches the last path segment of the module (e.g. `sparks-core` for `github.com/sparkwing-dev/sparks-core`). |
| `description` | string | yes | One-sentence summary. Used by registry and discovery tooling. |
| `author` | string | yes | GitHub handle, org, or author name. Used only for display. |
| `version` | string | no | Current library version, semver with `v` prefix (e.g. `v0.4.6`). When absent, the resolver uses the latest Go module tag as truth. Kept in `spark.json` mostly for local inspection; the Go module tag is authoritative. |
| `sdk_min_version` | string | no | Minimum compatible sparkwing SDK version (semver with `v` prefix). Declarative metadata for discovery tooling; no sparkwing command reads it. Omit during pre-1.0 churn. |
| `stability` | string | no | One of `experimental`, `beta`, `stable`. Defaults to `experimental`. Informational only; does not affect resolution. |
| `packages` | array | one of | Non-empty list of sub-packages within the library's single Go module. Each entry documents an import path. See the `packages[]` schema below. |
| `modules` | array | one of | Non-empty list of the Go modules a multi-module monorepo holds, each with its own tag series. See the `modules[]` schema below. |
| `dependencies` | array | no | Other sparks libraries this one depends on. Pure metadata - actual Go module resolution still happens via the dependent library's own `go.mod`. Shape mirrors `sparks:` entries: `{name, source, version}`. |

A manifest declares exactly one of `packages` and `modules`. The choice
follows the repository's module layout: one `go.mod` at the root means
`packages[]`, a `go.mod` per subdirectory means `modules[]`.

### `packages[]` entry schema

| Field | Type | Required | Description |
|---|---|---|---|
| `path` | string | yes | Import path relative to the module root, naming a directory that exists. E.g. `docker` for `github.com/acme/my-sparks/docker`. |
| `description` | string | yes | One-sentence summary of what the package provides. |
| `stability` | string | no | Per-package override of the library-level `stability`. Useful when a library has one stable package and one experimental package. |

### `modules[]` entry schema

| Field | Type | Required | Description |
|---|---|---|---|
| `path` | string | yes | Subdirectory relative to the manifest, naming a directory that exists. E.g. `docker`. |
| `module` | string | yes | The Go module path that subdirectory declares. When the directory holds a `go.mod`, this must equal its `module` line - that is the path consumers pin in their `sparks:` block. |
| `description` | string | yes | One-sentence summary of what the module provides. |
| `stability` | string | no | Per-module override of the library-level `stability`. |

### Example `spark.json`

A single-module library:

```json
{
  "name": "my-sparks",
  "description": "Pipeline helpers for the acme stack - Docker builds, GitOps deploys, AWS helpers",
  "author": "your-github-handle",
  "version": "v0.4.0",
  "stability": "beta",
  "packages": [
    {
      "path": "docker",
      "description": "Docker build, push, multi-registry tagging with deterministic content hashing"
    },
    {
      "path": "gitops",
      "description": "GitOps deployment with kustomize patching, retry, and ArgoCD sync"
    }
  ]
}
```

A multi-module monorepo, the shape sparks-core uses:

```json
{
  "name": "my-sparks",
  "description": "Multi-module monorepo of pipeline libraries - each module versioned independently",
  "author": "your-github-handle",
  "modules": [
    {
      "path": "docker",
      "module": "github.com/acme/my-sparks/docker",
      "description": "Docker build, push, multi-registry tagging with deterministic content hashing"
    },
    {
      "path": "gitops",
      "module": "github.com/acme/my-sparks/gitops",
      "description": "GitOps deployment with kustomize patching, retry, and ArgoCD sync"
    }
  ]
}
```

`sparkwing pipeline sparks lint` validates this shape.

## Consumer manifest: the `sparks:` block

A consumer repo declares the sparks libraries it wants live-tracked under
the `sparks:` key in `.sparkwing/sparkwing.yaml`. The block is optional -
if absent, the pipeline compiles using the exact versions pinned in the
consumer's `go.mod` and no overlay is created.

### Schema

```yaml
# .sparkwing/sparkwing.yaml
sparks:
  - name: <short name>            # advisory display label
    source: <go module path>      # e.g. github.com/sparkwing-dev/sparks-core/docker
    version: <constraint>         # exact tag, range, or "latest"
```

Per-entry fields:

| Field | Type | Required | Description |
|---|---|---|---|
| `name` | string | no | Advisory display label. When set it should match the library's declared `name` in its `spark.json`. Only `source` and `version` are enforced; a missing `name` is accepted. |
| `source` | string | yes | Go module path. Private modules need GOPRIVATE + netrc/SSH configured as usual. |
| `version` | string | yes | `latest`, an exact tag (`v0.10.3`), or a semver range (`^v0.10.0`, `~v0.10.3`). Range syntax follows standard semver (caret: same major, tilde: same minor). |

sparks-core is a multi-module monorepo: each block is its own Go module
with its own tag series, so pin the block modules you import rather than
the umbrella root path, which holds no importable packages.

### Example: exact pins

```yaml
sparks:
  - name: sparks-core-docker
    source: github.com/sparkwing-dev/sparks-core/docker
    version: v0.24.0
  - name: sparks-core-templates
    source: github.com/sparkwing-dev/sparks-core/templates
    version: v0.28.0
```

Deterministic: every build uses exactly these tags. No network call to the
module proxy on the hot path.

### Example: `latest`

```yaml
sparks:
  - name: sparks-core-docker
    source: github.com/sparkwing-dev/sparks-core/docker
    version: latest
```

Opt-in live tracking: every `sparkwing run <pipeline>` hits the module proxy to
discover the newest non-prerelease tag. Acceptable cost (~100ms per run) given
the user opted in. Use `--sw-no-update` to bypass when offline.

### Example: semver ranges

```yaml
sparks:
  - name: sparks-core-docker
    source: github.com/sparkwing-dev/sparks-core/docker
    version: ^v0.24.0      # any v0.24.x or higher minor within v0.x
  - name: my-sparks
    source: github.com/example/my-sparks
    version: ~v0.2.1       # any v0.2.x >= v0.2.1
```

Ranges trade off determinism for ergonomic updates. Resolution picks the
highest tag satisfying the constraint at build time. For modules covered by
`GOPRIVATE` there is no version list to scan: the range resolves to the
module's latest tag and errors if that tag falls outside the constraint,
rather than falling back to an older satisfying tag. Pin exact tags for
private modules.

## Resolution and the overlay-modfile pattern

The consumer's `go.mod` and `go.sum` are never modified by sparkwing tooling.
This is a hard rule. `go mod tidy` remains the user's authority over what is
in their `go.mod`.

### Flow

On every `sparkwing run <pipeline>` run (and on explicit `sparkwing pipeline sparks resolve`):

1. If the `sparks:` block is absent, no-op. Compile with plain `go build`
   against the user's `go.mod`.
2. Otherwise resolve each entry to a concrete version:
   - exact tag: no network call, used as-is
   - range: module-proxy call to list tags, pick highest that matches
   - `latest`: module-proxy call for newest non-prerelease tag
3. Materialize an overlay modfile at `.sparkwing/.resolved.mod`
   (gitignored). It is a copy of the user's `go.mod` with `require` lines
   for drifted sparks libraries rewritten to the resolved versions. If the
   freshly built overlay is byte-identical to the one already on disk it is
   left untouched (the fast path); otherwise it is rewritten.
4. Run `go mod download -modfile=.sparkwing/.resolved.mod` to populate
   `.sparkwing/.resolved.sum`.
5. Compile the pipeline with
   `go build -modfile=.sparkwing/.resolved.mod ...`.

The git-tracked `go.mod` and `go.sum` remain pristine. The one file
resolution touches is `.gitignore`: when `.sparkwing/.resolved.*` is not
already ignored, sparkwing appends it to the repo's `.gitignore`. Commit
that line and later runs leave the tree clean. Consumers who never declare
a `sparks:` block see behavior identical to plain Go builds.

### Fast-path skip

Whenever the `sparks:` block is non-empty the overlay is materialized and
compile builds with `-modfile`. The fast path is not skipping the overlay,
it is skipping the *rewrite*: when a freshly resolved overlay is
byte-identical to the one already on disk (exact pins, or a `latest` that
has not moved), the existing `.resolved.mod` / `.resolved.sum` are reused
untouched and the build proceeds against them. The only cost beyond
compilation is the proxy lookup itself.

### `latest` resolution

`latest` hits the Go module proxy on every run
(`proxy.golang.org/<module>/@latest`). For modules covered by `GOPRIVATE`,
sparkwing falls back to `go list -m -json <module>@<query>`, which walks
the git remote directly and picks the highest semver tag that is not a
prerelease. Authentication reuses the same mechanisms as `go get`:
`~/.netrc` for HTTPS, SSH keys for `ssh://git@...`.

Cost: ~100ms per `latest` entry per run. Users who pinned exact tags pay
nothing.

### GOPROXY and GOPRIVATE

Both `latest` and semver-range resolution go through `proxy.golang.org` by
default. Modules whose path matches `GOPRIVATE` bypass the proxy; for those,
sparkwing resolves tags by invoking `go list -m -json <module>@<query>`,
which walks the git remote directly using the user's configured auth. Only
`GOPRIVATE` triggers this direct path -- `GONOPROXY` is not consulted, and a
bare `GOPROXY=direct` (or `off`) entry is dropped, so resolution falls back
to `proxy.golang.org` rather than going VCS-direct for everything. No
separate sparkwing auth flow exists - if `go get` works for a `GOPRIVATE`
module, sparks resolution works.

### Offline work: `--sw-no-update`

`sparkwing run <pipeline> --sw-no-update` skips the resolution step entirely. If a
previous overlay exists at `.sparkwing/.resolved.mod`, it is reused; otherwise
compile uses the git-tracked `go.mod`. Useful on flights, in offline CI, or
while debugging a stale pin without touching the network.

### Ghost pin guidance

A `sparks:` overlay MASKS a stale or ghost version in `go.mod` at
build time - the overlay's rewritten `require` lines take precedence during
compile. It does NOT replace normal `go.mod` hygiene.

`go mod tidy` remains the authority for what is in `go.mod`. If the
checked-in `go.mod` pins a tag that does not exist (a "ghost pin"), that is
still a repo-level bug - fix it with a real pin. The overlay is
a build-time convenience, not a substitute for a correct `go.mod`.

### Interaction with `go.work`

The Go toolchain refuses `-modfile` whenever a `go.work` is in scope
("go: -modfile cannot be used in workspace mode"). The overlay
mechanism uses `-modfile=.resolved.mod`, so the two cannot coexist.

When sparkwing detects a workspace whose `use` list covers the `.sparkwing`
module, it skips the overlay and prints a one-line warning to stderr:

```
warning: /path/to/go.work in effect; skipping sparks resolution. Modules resolve from go.mod + workspace, not .resolved.mod. To use local copies of sparks libs too, add them to go.work.
```

A `go.work` that is in scope but does *not* list `.sparkwing` is a
different case: sparkwing builds with `GOWORK=off`, the overlay still
applies, and the warning names that instead.

Builds resolve sparks libs from `go.mod` (+ any workspace `use`
directives) instead of `.resolved.mod`. This is the right call when
you are deliberately iterating on multiple modules together: list
every repo you are editing in `.sparkwing/go.work`, e.g.

```go
// .sparkwing/go.work
go 1.26.3
use (
    .
    ../../sparkwing
    ../../sparks-core
)
```

The workspace then resolves everything from local checkouts, and sparks
pinning is suspended for the duration. The pre-push gate refuses to
push a committed `go.work` or `go.work.sum`, so this stays a local-only
convenience -- shipped builds always go through the overlay.

`sparkwing pipeline sparks resolve` still runs while a workspace is in
scope: it writes `.resolved.mod` as usual but skips materializing
`.resolved.sum` (the toolchain refuses `go mod download -modfile=X` under
`go.work`), printing a one-line notice. Remove the workspace file or set
`GOWORK=off` in your shell to also refresh the sum.

## Cache tiers

Compiled pipeline binaries are cached under a `PipelineCacheKey` that hashes
the pipeline source, local replace targets, the resolved sparks versions,
and the overlay modfile contents. The same key is used locally
(`~/.sparkwing/cache/pipelines/<key>/`) and in gitcache (`/bin/<key>`).

Three tiers of cache behavior fall out of that key, each with a rough
latency cost. Actual numbers vary by machine, network, and repo size; the
values below are order-of-magnitude on a developer laptop.

| Tier | Latency | When it applies |
|---|---|---|
| Binary cache hit | ~0s | Same source, same resolved sparks versions, same overlay. The compiled binary is fetched and executed. The common case. |
| Go build cache hit | ~2-3s | New sparks version or drifted overlay, so the binary cache misses, but most dependency object files are still in the Go build cache. Only the changed module recompiles and the final link runs. |
| Fully cold | ~10-15s | First-ever build in a fresh environment, a Go toolchain version change, or an invalidated build cache. Every object file is rebuilt from source. |

In-cluster, only the compiled pipeline binary is shared, via the gitcache
`/bin/<key>` endpoint (see [Warmup](#warmup)). The Go build cache lives on
the worker pod's scratch volume and dies with the pod, so a fresh pod that
misses the binary cache pays the cold tier.

## `sparkwing pipeline sparks` CLI

The `sparkwing pipeline sparks` command group manages the `sparks:` block
and the overlay. What each subcommand does:

- **list** -- show the declared sparks libraries and their resolved versions.
- **lint** -- validate a library's `spark.json` (schema, required fields,
  entry-path existence, and the `module` a `modules[]` entry declares
  against that directory's `go.mod`).
- **resolve** -- resolve versions per the `sparks:` block and materialize the
  overlay modfile at `.sparkwing/.resolved.mod` + `.resolved.sum`. Idempotent,
  cheap when nothing has drifted, and never touches git-tracked `go.mod`.
- **update** -- re-resolve one or all libraries against the module proxy and
  re-materialize the overlay modfile (`.resolved.mod` / `.resolved.sum`).
  For a range or `latest` constraint this picks up any newer tag; for an
  exact pin it is a no-op. Does not edit the `sparks:` block.
- **add** / **remove** -- add or remove a library entry in the `sparks:` block.
- **warmup** -- pre-compile pipeline binaries across consumer repos after a
  release (see [Warmup](#warmup) below).
- **inflate** -- copy a spark library's source into your repo so you can own
  and edit it (see [Inflating a spark library](#inflating-a-spark-library)
  below).

For the exact flags each subcommand takes, see
[cli-reference.md](cli-reference.md).

### Warmup

`sparkwing pipeline sparks warmup` pre-compiles the pipeline binary across consumer
repos after a sparks library release. It resolves the declared versions,
compiles the repo's `.sparkwing/` package into its single pipeline binary,
and uploads that binary to gitcache under its cache key (pass
`--clear-cache` to discard the existing local binary cache first). The next
`sparkwing run <pipeline>` run - locally or in-cluster - gets a binary-cache hit
instead of paying the full compile cost.

Warmup uses the exact same build path as `sparkwing`, so cache keys match. It is
an optimization, not a requirement: pipelines always resolve versions on
build, warmup just removes the first-run compile cost after a release.

Most useful as a post-release step in a sparks library's own release
pipeline. After tagging and pushing a new version, iterate over consumer
repos and warm each:

```bash
for repo in repo-a repo-b repo-c; do
    cd ~/code/$repo && sparkwing pipeline sparks warmup
done
```

### Inflating a spark library

Most of the time you consume a spark library by importing it and letting
the overlay pin its version. Sometimes you want the opposite: to take a
module's source into your own repo, edit it freely, and stop tracking
upstream. `sparkwing pipeline sparks inflate` does that.

```bash
# inflate the sparks-core templates module
sparkwing pipeline sparks inflate --module templates

# inflate any other spark library by full module path
sparkwing pipeline sparks inflate --module github.com/example/my-sparks
```

A bare `--module` name resolves to a sparks-core block module
(`templates` -> `github.com/sparkwing-dev/sparks-core/templates`); a value
containing a slash is treated as a complete module path.

What it does:

1. Reads the module's version from `.sparkwing/go.mod`'s `require` list
   (falling back to `latest` when the module is not yet required) and runs
   `go mod download` to locate it in the module cache.
2. Copies the module source (everything except a top-level `vendor/`) into
   `.sparkwing/sparks/<name>/` and makes the copied tree writable -- module
   cache files are read-only, so this step is what makes the code editable.
3. Adds a `replace <module> => ./sparks/<name>` directive to
   `.sparkwing/go.mod` and runs `go mod tidy`.

Because the build now resolves the module through the `replace` directive
pointing at your local copy, **your import paths do not change** and the
module's transitive dependencies keep resolving normally. Nothing in your
pipeline code needs editing -- the same imports now compile against source
you own.

The command refuses to overwrite an existing
`.sparkwing/sparks/<name>/` directory. To undo an inflate: delete that
directory and remove the `replace` directive from `.sparkwing/go.mod`.

Unlike the overlay resolver, inflating **deliberately edits the
git-tracked `.sparkwing/go.mod`** (it adds the `replace`). That is the
whole point -- you are opting out of upstream version tracking for this
module and committing the source into your repo. Commit
`.sparkwing/sparks/<name>/` and the `replace` line together.

## The template catalog

The catalog is the corpus of worked pipelines shipped as the `templates`
block module of sparks-core: static-site deploys, containerized deploys
to Kubernetes, migrate+deploy flows, CI-hygiene gates, canary rollouts,
test sharding. The `template-verify` pipeline scaffolds every one and
proves it compiles, lints, and explains; templates tagged runnable also
execute end-to-end against a synthesized fixture -- so unlike prose they
cannot quietly stop being true.

They are reference, not a starting point. `sparkwing pipeline new
--template <shape>` begins a pipeline from one of five structural
shapes; an example shows how a finished one is built. Reaching for an
example to *start* means inheriting another repo's assumptions and
deleting them one at a time, which is slower than writing the bodies
you actually want into a shape that already runs green.

Most arrivals should come through `sparkwing docs search`, which ranks
examples alongside the documentation -- searching `"ecs fargate"`
answers the question without anyone browsing a list. Browse and inspect
directly with `sparkwing examples`:

```bash
# list every template with its "when to use" signal and parameters
sparkwing examples

# narrow the list
sparkwing examples --cloud aws
sparkwing examples --category ci-hygiene

# full detail for one template: description, parameters table,
# applicability, and its README
sparkwing examples --name lint-test-go

# ... plus the pipeline body rendered with default / <placeholder> params
sparkwing examples --name lint-test-go --body
```

Filters are advisory metadata on each template. A template that declares
no cloud is cloud-agnostic and always passes a `--cloud` filter; a
`--category` filter matches templates that declare that exact category.
Every view has an `-o json` form for agent consumption -- the list emits
the manifests, and a `--name` detail view emits the manifest, README, and
(with `--body`) the rendered body.

Once you have read one, start your own from the shape it uses:

```bash
sparkwing pipeline new --name deploy --template build-test-deploy
```

`--template` takes a shape -- `minimal`, `build-test-deploy`,
`ci-pr-check`, `release`, or `scheduled-report` -- and passing an
example name is an error, with the `sparkwing examples --name <name>
--body` command to read it instead. To own and modify the catalog
itself, inflate it (see
[Inflating a spark library](#inflating-a-spark-library)).

## Authoring a sparks library

A sparks library is a Go module, or a monorepo of them. Author steps:

1. Create a Go module (normal `go mod init <module>`).
2. Add `spark.json` at the manifest root, filling in the schema above.
   `packages[]` must list every user-importable sub-package; a
   multi-module monorepo uses `modules[]` instead and lists every
   independently tagged module.
3. Pick a version. Stay on `v0.x` until the public surface is stable; push
   through `v1.0.0` only when you are ready to commit to the surface under
   semver-major stability.
4. Tag releases normally (`git tag v0.1.0 && git push --tags`). The Go
   module proxy will pick up the tag; sparkwing's resolver reads from there.
5. Never force-push a tag. Force-pushing a module tag breaks the Go module
   proxy checksum database and cascades into every consumer's `go.sum`
   mismatch. Always increment (e.g. `v0.1.0` -> `v0.1.1`), never overwrite.
6. For private repos, ensure GOPRIVATE covers the module path and that
   consumers can fetch via `~/.netrc` (HTTPS) or SSH. Sparkwing does not
   invent a separate auth flow; it reuses `go get`'s.

### Depending on another sparks library

A sparks library can list others in its manifest `dependencies`. This is
informational - actual Go module resolution happens through the library's
own `go.mod`. `spark lint` checks that each entry has a `source` and a
`version`; nothing else reads the list. `sparkwing pipeline sparks list`
renders the consumer's `sparks:` block, not library manifests.

## Non-goals

Explicit scope limits, baked in to avoid drift:

- **No binary plugins.** Sparks libraries are Go modules, linked at pipeline
  compile time. No `.so` loading, no RPC plugin model, no Wasm runtime.
- **No forced updates.** Consumers who pin exact versions stay on those
  versions forever. `latest` is opt-in per library entry in the `sparks:` block.
  Sparkwing never silently bumps a library the consumer did not ask to track.
- **No auto-discovery.** Consumers explicitly list every sparks library they
  use in the `sparks:` block. There is no classpath scan, no `go.mod` walk to
  detect libraries by manifest presence, no implicit enrollment.
- **No modification of git-tracked files during resolution.** Version
  resolution, the overlay, and every `sparkwing` run leave `go.mod`,
  `go.sum`, and the rest of the repo pristine. The single write resolution
  performs is a one-time append of `.sparkwing/.resolved.*` to `.gitignore`
  when that entry is missing. Generated files live under
  `.sparkwing/` with names starting `.resolved.` and are gitignored. The
  other deliberate exception is `sparkwing pipeline sparks inflate`, which you
  invoke explicitly to inflate a library's source and which does edit the
  git-tracked `.sparkwing/go.mod` (see
  [Inflating a spark library](#inflating-a-spark-library)).
- **No cross-module locking.** Each consumer resolves independently. There
  is no workspace-level lock that spans multiple consumer repos.

## Cross-references

- [`sdk.md`](sdk.md) - the unopinionated SDK that sparks libraries layer
  helpers on top of.
- [`pipelines.md`](pipelines.md) - how pipelines are authored against the SDK.
- [`sparks-core.md`](sparks-core.md) - an example sparks library.
