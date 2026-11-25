# Versioning and the plugin ecosystem

A repository's SDK pin selects a permitted engine through the signed
SDK-to-engine mapping. The Go SDK, TypeScript SDK and engine each have their own
version. An SDK publishes only when its source changes. Spark libraries use
their own module versions.

The current stable release and its notes are at
[GitHub releases/latest](https://github.com/sparkwing-dev/sparkwing/releases/latest);
`sparkwing update --dry-run` reports which release this machine would move to.

## Compatibility and release cadence

The author SDK is the Go package `github.com/sparkwing-dev/sparkwing/sparkwing`
with its `git`, `docker`, `services`, `inputs` and `cleanup` packages, and the
TypeScript package `@sparkwing/sdk` as declared in its `index.d.ts`. Changes to
it are additive only: new functions, types, methods, options and optional
fields. If a break cannot be avoided, it ships with a migration guide and a
scripted path in `sparkwing pipeline upgrade` that makes the change for you;
the release refuses an SDK break whose changelog entry links no guide section.
`sparkwing/runtime`, `sparkwing/protocol/...` and `sparkwing/testsupport/...`
are the host's side of the SDK, are not covered, and change with it.

- Sparkwing ships on a weekly train. Each Monday, the minor release
  (`v0.Y.0`) is cut from `main` if it has unreleased changes. Patch releases
  (`v0.Y.Z`) ship between trains for fixes.
- Breaking changes ship only in the Monday minor release. Each one is listed in
  that release's notes with a `(Breaking)` marker and a migration guide. A patch
  release never contains a breaking change; the release pipeline refuses to cut
  a patch whose changelog section has a `(Breaking)` entry.
- An SDK pin stays runnable. Each new run resolves the newest permitted engine
  that accepts the pin's SDK, so a later run of an unchanged pin can use a newer
  engine; a run already accepted keeps its exact engine through retries and
  resumes. Each engine release is supported for twelve months after
  publication, so a pin you leave alone keeps resolving a supported engine for
  at least twelve months after the newest engine that accepts it was published,
  unless a security problem revokes a release or raises the minimum version.
- Release notes are the
  [GitHub releases](https://github.com/sparkwing-dev/sparkwing/releases); watch
  the repository's releases to be notified. Migration guides are at
  <https://sparkwing.dev/docs/migration-guide/> and in `sparkwing docs migrations`.

The full policy is in
[VERSIONING.md](https://github.com/sparkwing-dev/sparkwing/blob/main/VERSIONING.md).

## Versioning per repo

Go pipelines pin `github.com/sparkwing-dev/sparkwing/sparkwing` in `.sparkwing/go.mod`.
TypeScript pipelines pin `@sparkwing/sdk` through their manifest and native
lockfile. A version range alone cannot identify an exact SDK. Go `vX.Y.Z` and
TypeScript `X.Y.Z` identify SDK releases. The signed index records each SDK's
language, version, protocol and source digest. It selects a permitted engine
that accepts that protocol. The SDK and engine versions can differ.

For local TypeScript development, pin a version-stamped SDK directory with
`file:../sdk` or a tarball such as `file:vendor/sparkwing-sdk-0.68.0.tgz`.
Relative tarball paths without `file:` also work. Run npm install to update the
native lockfile. Sparkwing reads the local package's name and version without
executing it, checks that the lockfile resolves that source, and resolves that
SDK version through the signed mapping. Local pins retain signed engine verification. Keep local
sources inside the repository when they must travel with a fetched checkout.
Tarball bytes, or a directory's package manifest, participate in pin identity.

A Go SDK `replace` directive that applies to the required version disables engine
pinning for that run. Sparkwing warns and uses the installed CLI's engine to build
and run the replacement. It records the replacement and installed executable's
digest in the accepted identity. Resume requires that same executable. Removing
the replacement restores release pinning and signed-release verification.

Pin inspection reads regular manifest and lock files without running repository
code. It refuses linked or special files. Go module inspection retains its
1 MiB limit and reports oversized input before parsing it.

Sparks libraries are ordinary independent modules. A consuming pipeline pins
them like other dependencies. A library upgrade does not silently change the
pipeline's Sparkwing pin. See [Sparks](sparks.md).

## Go directive policy

The repository's Go directive and CI toolchain checks own compiler requirements.
A dependency's minimum Go version must be satisfiable by the pipeline's selected
toolchain. `go.mod` retractions remain module-distribution metadata. Execution
security policy comes from the verified version index.

<!-- design:begin -->

## The SDK pin selects the pipeline engine

Every placement acquires its sealed source, reads the pin, resolves and verifies
a permitted engine that accepts the SDK's protocol, then uses that engine to
install/build and host the SDK. New engine releases can accept the same SDK pin
without requiring another SDK publication.
The CLI resolves the pin even when its installed version matches. It reuses the
selected executable only after the resolver checks policy and cached bytes.
A missing release fails instead of selecting another version.
Retries retain the accepted portable engine identity and exact source SHA. Each
machine resolves the appropriate platform asset for that same identity.

For a shared local run, the invoking CLI starts the local controller before
handing execution to a different pinned engine. The controller owns the store
and must support its schema. An older pipeline pin therefore keeps its engine
while using the installed CLI's controller after an upgrade or `daemon stop`.
A CLI that cannot open the store refuses before delegation, even when the pin
selects a newer engine. Upgrade the installed CLI to match the store.
A `--route burst` run starts the invoking CLI's local controller before engine
handoff even when Cloud accepts the run, because it may fall back to local
execution if Cloud is unreachable.
Standalone CI uses its selected engine's in-process controller and a private home.

Engines come from the official GitHub Releases assets. Verification uses the
shared release trust roots, signed checksums, exact asset digest, embedded version
and the historical release's closed asset matrix. Removing an asset from today's
publishing set cannot invalidate an older permitted release.

The machine caches verified engines under its user cache directory in
`sparkwing/engines`. A partial or corrupt recoverable entry is a miss.
`SPARKWING_TOOLCHAIN=auto` may fetch a missing verified release.
`SPARKWING_TOOLCHAIN=local` requires the exact verified cached release. Cloud
runner images ship the release files of one engine read-only. A Cloud Pod verifies
them exactly like a download before caching them, and downloads any other release,
or a shipped release that fails verification. Current
Available policy is checked before every execution, including cache hits. Pipeline binaries
are compiled and cached locally rather than downloaded from a controller.

## Support and security policy

Clients, runners and controllers use the current contract. Upgrade them together
when that contract changes. There is no old-peer compatibility promise until
live-mode Stripe is enabled. The selected engine supplies its matching node protocol.

The signed index at `https://sparkwing.dev/versions.json` records the latest
release, minimum version, individual revocations and per-release support windows.
Its detached signature is `versions.json.sig`. Clients verify the exact bytes,
refuse rollback and retain the last verified copy when refresh fails. There is
no fallback-age cutoff. Local-only execution reads cached policy without any
network refresh. A published SDK pin needs a verified SDK-to-engine mapping.
When that cache is empty, the first run fetches and verifies the signed index,
caches it, and proceeds. If fetching or verification fails, the run refuses with
the cause and asks you to run `sparkwing update` once online.
`sparkwing update` refreshes the policy into `<sparkwing home>/engines`, the one
cache local runs, the local controller, `upgrade` and `cache warm` share.
`sparkwing update --dry-run` and `sparkwing doctor` report the cached policy's
status, latest release, publication time and age as `version_policy`, without
writing anything.
Cached restrictions still apply, and executable signatures and exact pins remain
required. Controller runners fail closed when no verified policy is available.

Stable release publication owns both public index objects. After the GitHub
release becomes public, `.github/workflows/release.yaml` runs
`bin/publish-release-index.sh` with the existing update signing secret. It lists
all public stable `v0.x` releases, adds missing records with twelve calendar
months of support from each GitHub publication date, and preserves verified
minimum-version restrictions, revocations and existing release history.
Drafts, prereleases and tags without published releases do not enter this index.
Index publication shares the release publication queue and reads the current
signed policy inside that queue. Failed lookups or signature checks stop writes.
The website deploy must reserve `versions.json` and `versions.json.sig` for this
publisher. QA uses `bin/qaindex` with its separate key and origin.
See [Delivery](https://github.com/sparkwing-dev/sparkwing/blob/main/DELIVERY.md#signed-release-index)
for credentials, verification and the index-only recovery dispatch.

A development build skips signed version-policy checks and logs once per
resolver that the checks are off. This applies to run admission, run pages,
engine selection and runner updates. A development build
needs an explicit `--version` when upgrading a repository because it does not
fetch the latest-version index. Released controller runners retain signed policy checks, regardless of the
version reported by a runner or selected by a pipeline.

A service build deployed from a main commit after a stable release is stamped
with Go's canonical pseudo-version `vX.Y.(Z+1)-0.YYYYMMDDhhmmss-SHA12`.
For example, a commit after `v0.69.2` builds as
`v0.69.3-0.20261106041046-718a7591f23a`, which sorts after `v0.69.2` and
before `v0.69.3`. The timestamp is the commit's UTC committer time and SHA12
is its first 12 lowercase hexadecimal SHA characters. This stamp enforces
signed policy like a release. Git-describe stamps `vX.Y.Z-N-gSHA` retain the
same enforcement because released image builders still produce them.

A service build's own version never enters the index: the signed index governs
the engine each run selects, which every job checks before it executes pipeline
code, and revoked or below-minimum engines still refuse. The deployment's Cloud
launcher, which holds an operator-issued service token, claims work without its
own version being looked up, because it is the signed service image the deploy
workflow built. Self-hosted runners still claim only as a listed release that is
neither revoked nor below the minimum. Other unreleased stamps, such as
`commit-<sha>` or `-dev`, and malformed pseudo-versions are development builds.

`sparkwing run build-images` fetches tags from `origin` (unshallowing a shallow
clone), selects the highest reachable release tag and derives Go's canonical
pseudo-version from that base and the commit metadata. A release-tag build
keeps its exact release version. A prerelease base uses Go's corresponding
prerelease pseudo-version form; production policy classification requires a
stable release base. The build fails when no release base is reachable. `--tag`
overrides the stamp. Source image builds require a clean checkout and label both
controller and runner images with the full Git HEAD revision, the resolved
version and the canonical repository URL. Runtime-only bases carry no source
binary and do not require a clean checkout.

The runner image keeps its pinned Go, Node/npm and Docker CLI/plugin layers
before the per-attempt Debian package refresh. Go's own regression corpus,
API baselines and HTML docs stay out of the runtime image. The standard-library
source, compiler tools and runtime dependencies remain available for pipeline
builds and tests. The runner executable is the last layer. Cloud publication
appends that executable to a pinned runtime base, so runtime changes take effect
only after rebuilding the base and updating its deployment pin.

Cloud controllers and every launcher refuse to run work from a development
build. A controller with `--deployment=cloud` refuses to start unless it is
given `--allow-development-build`. Every launcher, Cloud or self-hosted,
refuses to start unless `SPARKWING_DEVELOPMENT_BUILD=1` is set. A launcher job
refuses to run, with no override, because admission keeps the launcher's
environment out of job pods. Each refusal names the build's version and how to
fix it. Local and team controllers, self-hosted runners that are not launchers
and the local CLI keep the development-build behavior above.

Warnings appear in run output and the dashboard before enforcement, with signed
reason and fix guidance. A revoked or unsupported release fails loudly and names
`sparkwing pipeline upgrade`. The CLI renders the same notice data as run status.
Before launch, a hard cut changes code, wire, data, tests and docs together.

## Upgrading a repository

```bash
sparkwing pipeline upgrade --dry-run
sparkwing pipeline upgrade
sparkwing pipeline upgrade --version v1.2.3
```

`upgrade` stages concrete SDK pin and lockfile edits in a private checkout.
The selected verified engine installs the staged SDK through `InstallSDK`.
`cache warm` uses the same selected-engine installer. The hidden SDK-install
command delegates to the engine package, which owns Go builds and npm installation.
TypeScript preparation serializes `npm install --no-save --no-audit --no-fund`
on the selected `package-lock.json` or `npm-shrinkwrap.json`. Before installation,
`npm ci --dry-run --ignore-scripts` checks that dependencies match that lockfile
without deleting installed packages or running lifecycle scripts. The dry run
also rewrites npm's hidden lockfile, `node_modules/.package-lock.json`, without
changing any package, so preparation deletes that file before installing. npm
then reads the installed tree, replaces an SDK whose pin moved, and applies
project, user and global configuration, including workspace selection. Concurrent jobs preserve unchanged SDK packages, and removing
`node_modules` causes a fresh install. Preparation leaves the manifest and lockfile
unchanged and validates the installed SDK against the exact native pin.
Root lifecycle scripts run on each preparation. Other dependencies are reconciled
only after npm's frozen-lockfile check passes.
After a verified installation, preparation writes `node_modules/.sparkwing-sdk-receipt`.
The receipt covers the inputs npm reads but does not write: `package.json`, the
lockfile, every `package.json`, lockfile and `.npmrc` in the pipeline directory and
its ancestors, the user, global and builtin npm configuration (the account's home
directory when `HOME` is unset), `npm_config_*`, `HOME`, `NODE_ENV`, `NODE_OPTIONS`,
`NODE_PATH` and `PREFIX`, the resolved `node` and `npm` executables, the manifest
of every lockfile package outside `node_modules` (workspace members and linked
sources), and local tarball dependencies. It also records npm's `node_modules/.package-lock.json` and
the installed SDK manifest. These are not every input npm could read.
A later preparation skips both npm commands only when these match and the tree
passes a subset of npm's hidden-lockfile check: every package it lists exists and no
package folder is newer than it. Reuse still validates the SDK pin. Any
difference, an unreadable input, a copied local directory dependency or a root
lifecycle script (`preinstall`, `install`, `postinstall`, `prepublish`,
`preprepare`, `prepare`, `postprepare` or `dependencies`) runs the npm commands
above. Preparation removes the receipt before npm writes, compares the inputs taken
before npm with the inputs after it, and writes a receipt only when they match and
the SDK verifies, all under the lockfile lock. An interrupted install or an edit
that lands while npm runs therefore never matches.
Version-manager shims (asdf, mise, volta) keep the same `node` and `npm` executable
across version switches, so the receipt does not notice a switch made through them.
Remove `node_modules/.sparkwing-sdk-receipt`, or `node_modules`, after switching.
Each installation makes one helper attempt with a ten-minute deadline. Failure
stops the install, reports the cause, and kills and reaps its helper process tree.
Verification then compiles Go packages, type-checks TypeScript with `tsc --noEmit`
when `.sparkwing/tsconfig.json` exists, and runs list, pipeline detail and plan
commands through that engine, including hidden pipelines. Its command index
selects `pipeline show`, or the older detail verb on published engines that
predate it. The TypeScript scaffold writes a
`tsconfig.json` and locks `typescript` as a development dependency, so `tsc` runs from
the project's own install. A project without `tsconfig.json` skips the type check; Node
strips its types, so pipeline details and plans are its check. Verification never runs
job bodies. Install and check environments pass through the shared environment
builder, and registered proxy credentials are masked in failures.


Older TypeScript scaffolds may lack Node declarations. If `pipeline upgrade`
reports TS2307 or TS2591 for imports such as `node:fs`, install the declarations:

```bash
npm --prefix .sparkwing install --save-dev --save-exact @types/node@22
```

In `.sparkwing/tsconfig.json`, add `"node"` to `compilerOptions.types` (create
`"types": ["node"]` if absent). Keep any existing type entries. Run
`npm --prefix .sparkwing exec -- tsc --noEmit --project .sparkwing/tsconfig.json`,
then retry
`sparkwing pipeline upgrade`. Commit `package.json`, `package-lock.json` and
`tsconfig.json` under `.sparkwing/`. Upgrading the SDK pin does not add these
project settings.

Pin and lockfile edits apply once the new SDK resolves against the verified
engine. When `go mod tidy` fails on a package the new release removed, the Go
pin moves with `go get` instead. Each original digest is checked again so
concurrent user edits are not overwritten. When the pipeline then fails to
compile, show its details or plan against the new pin, the pin stays applied and the
command reports the failure, names the migration guides between the two
versions and exits non-zero. Its report names the manifest and lockfile it
changed (for Go, `.sparkwing/go.mod` and `.sparkwing/go.sum`); reverting
those files, and only those, returns to the old pin without touching other
uncommitted pipeline edits. A failure to verify for any other reason, such as
a failed SDK download, a missing tool or a malformed answer from the engine,
leaves the original pins unchanged. A dry run reports proposed edits without installing or applying them.

`sparkwing update` owns installed machine software and refreshes managed services
and timers. Runners check their own signed minimum/revocation policy and prepare
a verified replacement. A blocked old runner takes no new work while existing
claims retain renewal and drain paths.

## The daemon's wire surface

The local controller owns one store, admission, queue and dashboard. Its HTTP API
socket uses the same messages and route policy as a remote controller. The
admission socket remains a separate protocol. Required types validate at the transport boundary.

Store incompatibility is an actionable refusal. It never silently opens another
store.
Before 1.0, schema changes may require a reset after configured data has been
preserved. The binary refuses a mismatched store with a
move-aside remedy for an older local store. Schemas 30 through 36 upgrade in
place to schema 37. Earlier baselines are refused unchanged. A reset must preserve
configured data, especially secrets and their keys. Billing records require
separate review. See
[Schema compatibility](backup-restore.md#schema-compatibility). Explicit standalone CI mode uses the same controller code in process.
See [Local execution](local-execution.md) and [Controller HTTP API](api.md).

<!-- design:end -->

## What this means for plugin authors

Pin exact SDK versions and test supported versions of the library. Describe the
supported SDK range in the library's README. For local Go development, a `replace`
directive can point at editable source. Published pins must remain reproducible.
The signed mapping binds each SDK declaration contract to permitted engines.
