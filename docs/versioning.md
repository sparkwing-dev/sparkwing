# Versioning and the plugin ecosystem

A repository's SDK pin selects its engine. The Go SDK, TypeScript SDK and engine
release together. Spark libraries use their own module versions.

## Versioning per repo

Go pipelines pin `github.com/sparkwing-dev/sparkwing` in `.sparkwing/go.mod`.
TypeScript pipelines pin `@sparkwing/sdk` through their manifest and native
lockfile. A version range alone cannot select an exact engine. Go `vX.Y.Z` and
TypeScript `X.Y.Z` identify engine `vX.Y.Z`, including any prerelease suffix.

For local TypeScript development, pin a version-stamped SDK directory with
`file:../sdk` or a tarball such as `file:vendor/sparkwing-sdk-0.68.0.tgz`.
Relative tarball paths without `file:` also work. Run npm install to update the
native lockfile. Sparkwing reads the local package's name and version without
executing it, checks that the lockfile resolves that source, and selects that
exact release's engine. Local pins retain signed engine verification. Keep local
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

## The SDK pin selects the CLI

Every placement acquires its sealed source, reads the pin, resolves and verifies
the matching engine, then uses that engine to install/build and host the SDK.
A newer ambient CLI or runner image does not substitute for an older exact pin.
The CLI resolves the pin even when its installed version matches. It reuses the
selected executable only after the resolver checks policy and cached bytes.
A missing release fails instead of selecting another version.
Retries retain the accepted portable engine identity and exact source SHA. Each
machine resolves the appropriate platform asset for that same identity.

Engines come from the official GitHub Releases assets. Verification uses the
shared release trust roots, signed checksums, exact asset digest, embedded version
and the historical release's closed asset matrix. Removing an asset from today's
publishing set cannot invalidate an older permitted release.

The machine caches verified engines under its user cache directory in
`sparkwing/engines`. A partial or corrupt recoverable entry is a miss.
`SPARKWING_TOOLCHAIN=auto` may fetch a missing verified release.
`SPARKWING_TOOLCHAIN=local` requires the exact verified cached release. Current
Available policy is checked before every execution, including cache hits. Pipeline binaries
are compiled and cached locally rather than downloaded from a controller.

## Support and security policy

From launch freeze, the controller accepts requests emitted by every engine for
twelve calendar months after its release. Older releases keep working until an
explicit security revocation or minimum-version policy takes effect. Age alone
never refuses execution. The selected engine supplies its matching node protocol.

The signed index at `https://sparkwing.dev/versions.json` records the latest
release, minimum version, individual revocations and per-release support windows.
Its detached signature is `versions.json.sig`. Clients verify the exact bytes,
refuse rollback and retain the last verified copy when refresh fails. There is
no fallback-age cutoff. Local-only execution reads cached policy without any
network refresh. Without a verified cached policy, or for a release newer than
the cached one lists, it proceeds with a warning.
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
compatibility queries, engine selection and runner updates. A development build
needs an explicit `--version` when upgrading a repository because it does not
fetch the latest-version index. Released controller runners retain signed policy checks, regardless of the
version reported by a runner or selected by a pipeline.

Warnings appear in run output and the dashboard before enforcement, with signed
reason and fix guidance. A revoked or unsupported release fails loudly and names
`sparkwing upgrade`. The CLI renders the same notice data as run status.
Before launch, a hard cut changes code, wire, data, tests and docs together.

## Upgrading a repository

```bash
sparkwing upgrade --dry-run
sparkwing upgrade
sparkwing upgrade --version v1.2.3
```

`upgrade` stages concrete SDK pin and lockfile edits in a private checkout.
The selected verified engine installs the staged SDK through `InstallSDK`.
`cache warm` uses the same selected-engine installer. The hidden SDK-install
command delegates to the engine package, which owns Go builds and `npm ci`.
Each installation makes one helper attempt with a ten-minute deadline. Failure
stops the install, reports the cause, and kills and reaps its helper process tree.
Verification then compiles Go packages or type-checks TypeScript and runs list,
describe and plan through that engine, including hidden pipelines. It never runs
job bodies. Install and check environments pass through the shared environment
builder, and registered proxy credentials are masked in failures.

Pin and lockfile edits apply once the new SDK resolves against the verified
engine. When `go mod tidy` fails on a package the new release removed, the Go
pin moves with `go get` instead. Each original digest is checked again so
concurrent user edits are not overwritten. When the pipeline then fails to
compile, describe or plan against the new pin, the pin stays applied and the
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
admission socket remains a separate protocol. Unknown additive API fields and
future canonical error codes remain readable. Required types still validate.

Store incompatibility is an actionable refusal. It never silently opens another
store. v0.68.0 starts a new store and refuses one from v0.67 or earlier.
Before 1.0, schema changes may require moving the local store aside or
recreating a server database. The binary refuses a mismatched store with a
move-aside remedy for an older local store. v0.68.3 ships baseline 28 as a store
reset. Forward migrations start after launch. See
[Schema compatibility](backup-restore.md#schema-compatibility). Explicit standalone CI mode uses the same controller code in process.
See [Local execution](local-execution.md) and [Controller HTTP API](api.md).

<!-- design:end -->

## What this means for plugin authors

Pin exact SDK versions and test supported versions of the library. Describe the
supported SDK range in the library's README. For local Go development, a `replace`
directive can point at editable source. Published pins must remain reproducible.
A release's SDK declaration contract and its matching engine travel together.
