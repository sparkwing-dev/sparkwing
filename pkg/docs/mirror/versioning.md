# Versioning and the plugin ecosystem

A repository's SDK pin selects its engine. The Go SDK, TypeScript SDK and engine
release together. Spark libraries use their own module versions.

## Versioning per repo

Go pipelines pin `github.com/sparkwing-dev/sparkwing` in `.sparkwing/go.mod`.
TypeScript pipelines pin `@sparkwing/sdk` through their manifest and native
lockfile. A version range alone cannot select an exact engine. Go `vX.Y.Z` and
TypeScript `X.Y.Z` identify engine `vX.Y.Z`, including any prerelease suffix.

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
policy is checked before every execution, including cache hits. Pipeline binaries
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
no fallback-age cutoff. First use without any verified copy fails with an
unavailable error and connectivity guidance.

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
Verification then compiles Go packages or type-checks TypeScript and runs list,
describe and plan through that engine, including hidden pipelines. It never runs
job bodies. Install and check environments pass through the shared environment
builder, and registered proxy credentials are masked in failures.

Only verified edits apply. Each original digest is checked again so concurrent
user edits are not overwritten. Failed verification leaves the original pins
unchanged. A dry run reports proposed edits without installing or applying them.

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
store. Explicit standalone CI mode uses the same controller code in process.
See [Local execution](local-execution.md) and [Controller HTTP API](api.md).

<!-- design:end -->

## What this means for plugin authors

Pin exact SDK versions and test supported versions of the library. Describe the
supported SDK range in the library's README. For local Go development, a `replace`
directive can point at editable source. Published pins must remain reproducible.
A release's SDK declaration contract and its matching engine travel together.
