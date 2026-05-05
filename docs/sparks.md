# Sparks Libraries

Formal reference for the sparks library ecosystem: the `spark.json` manifest,
the consumer `.sparkwing/sparks.yaml` file, version resolution, and the
`sparkwing pipeline sparks` CLI.

This document is the source of truth. Where an item is still pending
implementation, this document marks it.

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
- Git: `ShortCommit`, `IsDirty`, `FilesetHash`, `CurrentBranch`, `Tags`, `PushTag`
- Services: `WithServices` (docker-run backed sidecars)
- Approval: stubbed call-site (`sparkwing.Approval`, panics until designed)
- Plan / modifiers: `ExpandFrom`, `CacheKey`, `RunsOn`, `AwaitPipelineJob`,
  typed `Ref[T]` outputs

**In a sparks library:** anything with deep opinions on specific tooling.

- ArgoCD sync, Kustomize patch, `deploy.Run` composite
- ECR detection, AWS profile discovery, netrc seeding
- Go-specific checks (`GoFmt`, `GoVet`, `GoTest`) - future `sparkwing-go` library
- Ruby, Python, Java toolchain helpers - future per-language libraries
- Anything that ties a pipeline to a specific registry, control plane, or
  cloud provider

The rule of thumb: if the helper would make zero sense outside one opinionated
stack, it belongs in a sparks library, not the SDK.

## `spark.json` schema

Every sparks library places a `spark.json` file at its module root. It is
valid JSON with the following fields.

| Field | Type | Required | Description |
|---|---|---|---|
| `name` | string | yes | Short library name. Must be unique in a consumer's `sparks.yaml`. Conventionally matches the last path segment of the module (e.g. `sparks-core` for `github.com/sparkwing-dev/sparks-core`). |
| `description` | string | yes | One-sentence summary. Shown in `sparkwing pipeline sparks list` and registry tooling. |
| `author` | string | yes | GitHub handle, org, or author name. Used only for display. |
| `version` | string | no | Current library version, semver with `v` prefix (e.g. `v0.4.6`). When absent, the resolver uses the latest Go module tag as truth. Kept in `spark.json` mostly for local inspection; the Go module tag is authoritative. |
| `sdk_min_version` | string | no | Minimum compatible sparkwing SDK version (semver with `v` prefix). The resolver warns when a consumer's SDK is older. Omit during pre-1.0 churn. |
| `stability` | string | no | One of `experimental`, `beta`, `stable`. Defaults to `experimental`. Informational only; does not affect resolution. |
| `packages` | array | yes | Non-empty list of sub-packages within the module. Each entry documents an import path. See the `packages[]` schema below. |
| `dependencies` | array | no | Other sparks libraries this one depends on. Pure metadata - actual Go module resolution still happens via the dependent library's own `go.mod`. Shape mirrors `sparks.yaml` entries: `{name, source, version}`. |

### `packages[]` entry schema

| Field | Type | Required | Description |
|---|---|---|---|
| `path` | string | yes | Import path relative to the module root. E.g. `docker` for `github.com/sparkwing-dev/sparks-core/docker`. |
| `description` | string | yes | One-sentence summary of what the package provides. |
| `stability` | string | no | Per-package override of the library-level `stability`. Useful when a library has one stable package and one experimental package. |

### Example `spark.json`

Current-truth reference is `sparks-core/spark.json`. Abbreviated:

```json
{
  "name": "sparks-core",
  "description": "Core pipeline library for sparkwing - Docker builds, GitOps deploys, AWS helpers, and pre-commit checks",
  "author": "your-github-handle",
  "version": "v0.10.0",
  "sdk_min_version": "v0.9.0",
  "stability": "beta",
  "packages": [
    {
      "path": "docker",
      "description": "Docker build, push, multi-registry tagging with deterministic content hashing"
    },
    {
      "path": "gitops",
      "description": "GitOps deployment with kustomize patching, retry, and ArgoCD sync"
    },
    {
      "path": "aws",
      "description": "AWS profile detection and ECR authentication"
    }
  ]
}
```

## Consumer manifest: `.sparkwing/sparks.yaml`

A consumer repo declares the sparks libraries it wants live-tracked in
`.sparkwing/sparks.yaml`. The file is optional - if absent, the pipeline
compiles using the exact versions pinned in the consumer's `go.mod` and no
overlay is created.

### Schema

```yaml
libraries:
  - name: <short name>            # must match the library's spark.json "name"
    source: <go module path>      # e.g. github.com/sparkwing-dev/sparks-core
    version: <constraint>         # exact tag, range, or "latest"
```

Top-level fields:

| Field | Type | Required | Description |
|---|---|---|---|
| `libraries` | array | yes | Entries as described above. |

Per-entry fields:

| Field | Type | Required | Description |
|---|---|---|---|
| `name` | string | yes | Must match the library's declared `name` in its `spark.json`. |
| `source` | string | yes | Go module path. Private modules need GOPRIVATE + netrc/SSH configured as usual. |
| `version` | string | yes | `latest`, an exact tag (`v0.10.3`), or a semver range (`^v0.10.0`, `~v0.10.3`). Range syntax follows standard semver (caret: same major, tilde: same minor). |

### Example: exact pins

```yaml
libraries:
  - name: sparks-core
    source: github.com/sparkwing-dev/sparks-core
    version: v0.10.3
  - name: sparkwing-go
    source: github.com/example/my-sparks
    version: v0.2.1
```

Deterministic: every build uses exactly these tags. No network call to the
module proxy on the hot path.

### Example: `latest`

```yaml
libraries:
  - name: sparks-core
    source: github.com/sparkwing-dev/sparks-core
    version: latest
```

Opt-in live tracking: every `wing <pipeline>` hits the module proxy to
discover the newest non-prerelease tag. Acceptable cost (~100ms per run) given
the user opted in. Use `--no-update` to bypass when offline.

### Example: semver ranges

```yaml
libraries:
  - name: sparks-core
    source: github.com/sparkwing-dev/sparks-core
    version: ^v0.10.0       # any v0.10.x or higher minor within v0.x
  - name: sparkwing-go
    source: github.com/example/my-sparks
    version: ~v0.2.1        # any v0.2.x >= v0.2.1
  - name: sparks-ruby
    source: github.com/sparkwing-dev/sparks-ruby
    version: v0.3.0         # exact
```

Ranges trade off determinism for ergonomic updates. Resolution picks the
highest tag satisfying the constraint at build time.

## Resolution and the overlay-modfile pattern

The consumer's `go.mod` and `go.sum` are never modified by sparkwing tooling.
This is a hard rule. `go mod tidy` remains the user's authority over what is
in their `go.mod`.

### Flow

On every `wing <pipeline>` run (and on explicit `sparkwing pipeline sparks resolve`):

1. If `.sparkwing/sparks.yaml` is absent, no-op. Compile with plain `go build`
   against the user's `go.mod`.
2. Otherwise resolve each entry to a concrete version:
   - exact tag: no network call, used as-is
   - range: module-proxy call to list tags, pick highest that matches
   - `latest`: module-proxy call for newest non-prerelease tag
3. Compare resolution against the current `go.mod` require lines. If every
   resolved version already matches what is in `go.mod`, take the fast path -
   no overlay is written, compile as normal.
4. Otherwise materialize an overlay modfile at `.sparkwing/.resolved.mod`
   (gitignored). It is a copy of the user's `go.mod` with `require` lines
   for drifted sparks libraries rewritten to the resolved versions.
5. Run `go mod download -modfile=.sparkwing/.resolved.mod` to populate
   `.sparkwing/.resolved.sum`.
6. Compile the pipeline with
   `go build -modfile=.sparkwing/.resolved.mod ...`.

The git-tracked `go.mod` and `go.sum` remain pristine; `git status` after a
`wing` run shows no changes. Consumers who never create a `sparks.yaml` see
behavior identical to plain Go builds.

### Fast-path skip

When the resolved versions match `go.mod` exactly (including for `latest`
entries where the module proxy returns the same tag already pinned), no
overlay is generated and no overlay-driven `go build` indirection happens.
This keeps the common case - exact pins, or a `latest` that has not moved -
zero-cost beyond the proxy lookup itself.

### `latest` resolution

`latest` hits the Go module proxy on every run
(`proxy.golang.org/<module>/@latest`). For modules covered by `GOPRIVATE`,
sparkwing falls back to `git ls-remote --tags` against the source repo and
picks the highest semver tag that is not a prerelease. Authentication reuses
the same mechanisms as `go get`: `~/.netrc` for HTTPS, SSH keys for
`ssh://git@...`.

Cost: ~100ms per `latest` entry per run. Users who pinned exact tags pay
nothing.

### GOPROXY and GOPRIVATE

Both `latest` and semver-range resolution go through `proxy.golang.org` by
default. Modules whose path matches `GOPRIVATE` (or `GONOPROXY`) bypass the
proxy; for those, sparkwing resolves tags by invoking `go list -m -versions
<module>`, which walks the git remote directly using the user's configured
auth. Set `GOPROXY=direct` to force direct resolution for everything. No
separate sparkwing auth flow exists - if `go get` works, sparks resolution
works.

### Offline work: `--no-update`

`wing <pipeline> --no-update` skips the resolution step entirely. If a
previous overlay exists at `.sparkwing/.resolved.mod`, it is reused; otherwise
compile uses the git-tracked `go.mod`. Useful on flights, in offline CI, or
while debugging a stale pin without touching the network.

### Ghost pin guidance

A `sparks.yaml` overlay MASKS a stale or ghost version in `go.mod` at
build time - the overlay's rewritten `require` lines take precedence during
compile. It does NOT replace normal `go.mod` hygiene.

`go mod tidy` remains the authority for what is in `go.mod`. If the
checked-in `go.mod` pins a tag that does not exist (a "ghost pin"), that is
still a repo-level bug - fix it with a real pin. The overlay is
a build-time convenience, not a substitute for a correct `go.mod`.

## Cache tiers

Compiled pipeline binaries are cached under a `PipelineCacheKey` that hashes
the pipeline source, local replace targets, the resolved sparks versions,
and the overlay modfile contents. The same key is used locally
(`~/.sparkwing/cache/<key>/`) and in gitcache (`/bin/<key>`).

Three tiers of cache behavior fall out of that key, each with a rough
latency cost. Actual numbers vary by machine, network, and repo size; the
values below are order-of-magnitude on a developer laptop.

| Tier | Latency | When it applies |
|---|---|---|
| Binary cache hit | ~0s | Same source, same resolved sparks versions, same overlay. The compiled binary is fetched and executed. The common case. |
| Go build cache hit | ~2-3s | New sparks version or drifted overlay, so the binary cache misses, but most dependency object files are still in the Go build cache. Only the changed module recompiles and the final link runs. |
| Fully cold | ~10-15s | First-ever build in a fresh environment, a Go toolchain version change, or an invalidated build cache. Every object file is rebuilt from source. |

In-cluster, the Go build cache is persisted in gitcache so that a new
sparks version incurs the middle tier rather than the cold tier across
worker pods. The build-cache key is derived from Go version, architecture,
and sparks versions; it is stored via the existing `/cache/<key>` gitcache
endpoint.

## `sparkwing pipeline sparks` CLI

Subcommands and one-line purposes:

| Subcommand | Purpose |
|---|---|
| `sparkwing pipeline sparks list` | Show declared sparks libraries in the current repo and their resolved versions. |
| `sparkwing pipeline sparks lint [path]` | Validate the `spark.json` at `path` (defaults to current directory). Checks schema, required fields, package path existence. |
| `sparkwing pipeline sparks resolve` | Resolve versions per `sparks.yaml` and materialize the overlay modfile at `.sparkwing/.resolved.mod` + `.resolved.sum`. Idempotent. Cheap when nothing has drifted. Never modifies git-tracked `go.mod`. |
| `sparkwing pipeline sparks update [name]` | Bump one or all libraries to the latest version within their declared range. Updates `sparks.yaml` only; still does not touch `go.mod`. |
| `sparkwing pipeline sparks add <source> [--version X]` | Add a library to `sparks.yaml`. Defaults `version` to `latest` if not specified. |
| `sparkwing pipeline sparks remove <name>` | Remove a library from `sparks.yaml`. |
| `sparkwing pipeline sparks warmup` | Pre-compile pipeline binaries across consumer repos after a sparks library release. See "Warmup" below. |

### Warmup

`sparkwing pipeline sparks warmup` pre-compiles pipeline binaries across consumer repos
after a sparks library release. It clears the binary cache, resolves the
latest versions, compiles each pipeline in the repo, and uploads the binaries
to gitcache. The next `wing <pipeline>` run - locally or in-cluster - gets
a binary-cache hit instead of paying the full compile cost.

Warmup uses the exact same build path as `wing`, so cache keys match. It is
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

## Authoring a sparks library

A sparks library is a Go module. Author steps:

1. Create a Go module (normal `go mod init <module>`).
2. Add `spark.json` at the module root, filling in the schema above.
   `packages[]` must list every user-importable sub-package.
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
own `go.mod`. Declaring a dependency in `spark.json` lets tooling show the
relationship in `sparkwing pipeline sparks list` and lets future resolver work
transitively check compatibility.

## Non-goals

Explicit scope limits, baked in to avoid drift:

- **No binary plugins.** Sparks libraries are Go modules, linked at pipeline
  compile time. No `.so` loading, no RPC plugin model, no Wasm runtime.
- **No forced updates.** Consumers who pin exact versions stay on those
  versions forever. `latest` is opt-in per library entry in `sparks.yaml`.
  Sparkwing never silently bumps a library the consumer did not ask to track.
- **No auto-discovery.** Consumers explicitly list every sparks library they
  use in `sparks.yaml`. There is no classpath scan, no `go.mod` walk to
  detect libraries by manifest presence, no implicit enrollment.
- **No modification of git-tracked files.** `go.mod`, `go.sum`, and the rest
  of the repo stay pristine after any `sparkwing pipeline sparks *` or `wing` run.
  Generated files live under `.sparkwing/` with names starting `.resolved.`
  and are gitignored.
- **No cross-module locking.** Each consumer resolves independently. There
  is no workspace-level lock that spans multiple consumer repos.

## Cross-references

- [`sdk.md`](sdk.md) - the unopinionated SDK that sparks libraries layer
  helpers on top of.
- [`pipelines.md`](pipelines.md) - how pipelines are authored against the SDK.
- [`sparks-core.md`](sparks-core.md) - an example sparks library.
