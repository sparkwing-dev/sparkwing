# C3 engine and version policy contracts

<!-- design:begin -->

The engine package implements these contracts. Component tests cover pin
resolution, signed releases, policy, caching, selected-engine SDK installation
and upgrades. Source/host composition and native platform evidence remain separate work. The
implementation status is recorded in [the W8 handoff](../W8-IMPLEMENTATION.md).

## Amendment 1: selected-engine installation

`install.go` implements `Resolved.InstallSDK(SDKInstallRequest) SDKCommand`.
Source acquisition resolves the exact pin before calling it. The verified selected
Sparkwing executable implements hidden `__sdk-install`, reads request JSON on stdin,
inherits only procenv.Install's environment, and writes one SDKCommand JSON value.
Diagnostics go to masked stderr. The in-memory request retains the source
mask registry, including allowed operator values. The registry is excluded from
JSON and stays in the host that masks the selected engine's complete diagnostics.
The command contains locally built Go argv or
Node/main argv without protocol flags or a preload. Installation rechecks the pin
against Resolved and cannot substitute the host's engine or a remote pipeline binary.
Eval/work use the accepted identity and the same selected engine's host operations.

`internal/engine/sdkinstall.Run` serves that request in the selected executable.
It rechecks the build version, uses the existing local binary cache for Go and
uses `npm ci` for TypeScript. Go dependency selection must retain the pinned SDK.
The cache key includes the pin, source key and install environment, keeping these
locally built entries separate from the retired remote binary path. TypeScript
returns Node and its main file. Native tool diagnostics pass through the shared
proxy masker. The hidden CLI route skips update and version-state startup hooks.

`internal/pipelineentry.NPMCommand` constructs npm argv for installation, upgrades
and cache-directory queries. Windows command shims use Node with the installed
npm-cli.js. Native npm executables and Unix npm keep their direct invocation.
Arguments never pass through a shell. Callers supply the environment and run
through gotoolchain.Run so cancellation reaches the existing process group.
The same function accepts the host's captured executable lookup for job cache
queries, without consulting an ambient PATH.
Upgrade tools also receive procenv.Install with its proxy masker, and errors
mask captured diagnostics before returning them.
The Windows layout follows [npm's launcher](https://github.com/npm/cli/blob/latest/bin/npm.cmd).

`SDKInstallRequest.UnmarshalJSON` rejects duplicate and unknown fields, including
an attempted environment field. `PipelineDirectory` keeps both sides on the same
checkout-relative path validation. These are W8-local additions. The install
adapter is a child package because the binary cache depends on store/runspec,
which already imports the engine identity package.

W8 retains historical signed asset matrices even after current cache-binary removal.
W9 renders shared version notices and wires upgrade verification. W5/W9 package and
stamp the TypeScript Windows bridge for supported architectures with its matching
engine release. Decision 104 requires native Windows handle-inheritance evidence.
These are integration obligations under decisions 102-104, not a new distribution
origin, trust root, fallback-age cutoff or support-window expiry refusal.


The CLI delegates `__sdk-install` to `sdkinstall.Run`. Cache warm resolves the
exact verified engine and calls its `InstallSDK`; it has no second Go/npm
installer. The old CLI installer and its duplicate tests are removed. Shared
engine tests own dependency installation and version checks.

Selected-engine Go installation hashes the captured build environment with
`GOWORK=off` through the shared binary-cache key owner. A workspace disabled for
compilation cannot add graph inputs or make an otherwise usable cache entry fail.
The generated workspace-mutation test covers this boundary.

Go installation and the CLI use `bincache.MaterializePipeline` to populate
effective checksums before hashing and preserve sparks overlays. The selected
engine copies the executable into the caller's private `ExecutableDirectory`
while retaining the cache lease. `SDKCommand.Close` removes that private copy
after execution. `source.Entry.Close`, upgrade verification and cache warm join
that cleanup. `CachedExecutable` identifies the durable cache entry for cache
warm reports. It does not authorize execution after the lease is released.
Real Go fixtures cover missing checksums, overlays, undeclared SDK replacement refusal and
execution after cache pruning. Native platform evidence remains separate.

Dependency preparation runs `go list -deps` for the pipeline package, using
the same workspace selection as compilation. Covering workspaces remain active.
Unrelated workspaces are disabled for preparation. Outside a covering workspace,
it uses `-mod=mod` and the effective modfile to prepare imported package
checksums before computing the cache key. It no longer
downloads every module archive in the graph. An unavailable unused archive
cannot block a pipeline whose build dependencies are already available.

Local dispatch consumes ReadPin, VersionForPin and Resolve for both SDKs. It
rechecks policy on every invocation and stops re-executing only when the current
and resolved executables are the same file. Info reads pin metadata without
fetching or guessing cache paths. Source/host composition and native execution
remain separate integration work.


## Ownership

`internal/engine` owns exact SDK-to-engine selection, release verification,
machine caching, signed version policy and repository pin upgrades. It consumes
`internal/releaseasset` and `internal/releaseauth`, which also own the asset matrix
and trust roots used by `verify-release`. It adds no signing key or mirror.
C4 owns shared durations and CLI grammar. C1 stores the portable engine identity
at seal point two. C3 hosts consume it on every execution placement.

## Exact selection and verification

Decision 111 permits Go SDK replacements. `ReadPin` records the applicable
replacement module selector in `Pin.Replacement` and retains the declared SDK
requirement. `VersionForPin` selects the installed executable's embedded version
for that pin. `Resolve` returns that executable with `engine_pin_disabled` notice
and its SHA256 as both asset and manifest identity, without fetching a release.
`Ensure` and installation require the same installed executable and identity.
SDK hello still checks protocol and language; the replacement's self-reported
SDK version need not equal the replaced requirement. The Go installer verifies
that native module selection retains the recorded replacement. Undeclared
overlay replacements remain refused. Release pins retain signed policy and
release verification.

The Go pin is the `github.com/sparkwing-dev/sparkwing` module requirement in the
pipeline's go.mod. The TypeScript pin is `@sparkwing/sdk`, resolved exactly by its
manifest and native lockfile. `ReadPin` reads those files before executing pipeline
code. CLI selection and `info` pass the pipeline directory to `ReadPin`, matching
source acquisition. A range alone cannot select an exact engine. Go `vX.Y.Z` and TypeScript
`X.Y.Z` select engine `vX.Y.Z`, retaining any prerelease suffix. A newer installed
engine does not satisfy an older pin. Missing, conflicting and non-release pins
produce an actionable `invalid_args` error.

`Resolve` checks the signed policy, fetches the matching release and verifies it
before SDK build, describe or plan. Fetches use only the official repository's
GitHub Releases assets. Redirects may reach GitHub's release-asset CDN, but a
pipeline cannot choose a release origin, trust root or download URL. Engine
fetching is separate from pipeline compilation. Pipeline binaries remain locally
built and cached under the existing binary-cache contract.

`VerifyRelease` requires the shared trusted key set, a signed SHA256SUMS, a signed
asset, the matching digest, the exact binary/OS/architecture and the requested
embedded release version. It reads executable metadata without executing the
untrusted download. Manifest membership must equal `releaseasset.NamesForVersion(version)` with
no duplicates, missing executables or extras. The closed set and trust roots are
shared with `verify-release`, not copied into the engine package.
`Names()` remains the current publishing matrix. `NameForVersion` and
`VerifyForVersion` use the release's retained matrix, so removing a target from
today's publication set cannot invalidate an older permitted release.

`Resolved` contains the verified version, platform, asset digest and local
executable path. Its `Identity` contains the pin, release version and complete
manifest digest. Only `Identity` enters `runspec.Resolved.Engine`, because a
retry may execute on another platform. `Ensure` obtains that platform's asset
while requiring the same SDK pin, release and signed manifest. Reconstructed
source must still contain the accepted pin.

`source.Acquire` resolves the engine before running the SDK command. Local,
local-controller, runner-hosted, here, Actions and Cloud planning all use that
boundary. Eval and work use the accepted identity. The runner image's version
cannot substitute for it. Pipeline engines require a `releaseasset.Sparkwing`
target. Runner updates require `releaseasset.SparkwingRunner`. Controller and
cache executables cannot be selected for either role. `RunInput.Engines`, `NodeInput.Engine` and
`CloudJobAward.Engine` carry these dependencies. The Go and TypeScript SDKs both
support Linux, macOS and Windows. TypeScript uses hello bearer transport and
needs no credential preload.

## Machine caching

`DefaultCacheDirectory` owns the default `os.UserCacheDir()/sparkwing/engines`
path on each OS. `NewResolver` uses it when `CacheDirectory` is empty, so all
placements share one machine cache without user configuration. An explicit
directory is a host/test override. No new environment variable selects the path.

The cache key contains release, binary, OS, architecture and manifest digest.
Publication accepts `VerifiedRelease` only. It retains the verification material
and publishes atomically, so partial downloads never become executable. Concurrent
requests may converge on the same valid entry. Missing or corrupt recoverable
cache entries cause a miss and verification on refetch.
An executable replaced by a directory, or missing its owner's execute permission
on Unix, is also a cache miss. Auto mode restores the verified executable. Local
mode reports the missing executable without fetching.

Every start checks the current verified policy, even for a cached executable.
`Cached` alone grants no execution permission. `Resolve` and `Ensure` enforce the
policy and recheck cached bytes before use. The existing `auto` mode may fetch a
missing release. `local` requires the exact verified cached executable.

The cache belongs to the machine/runtime, not the run. A persistent host reuses
it across runs. An ephemeral Cloud Job retains it for its runtime lifetime and
may lose it at teardown. This contract adds neither a hostPath mount nor a Cloud
engine mirror.

## Signed version index

The index is `https://sparkwing.dev/versions.json`, with a detached signature at
`versions.json.sig`. The signature covers the exact JSON body bytes and uses the
shared release trust roots. The format contains:

| Field | Meaning |
| --- | --- |
| `format` | `VersionIndexFormat` (1) |
| `published_at` | Signed policy publication time and rollback floor |
| `latest` | Latest permitted release for explicit upgrades |
| `minimum` | Version floor with reason, security flag, announcement and enforcement dates |
| `revoked` | Individual version restrictions with the same reason and date fields |
| `versions` | Every release's version, publication time and support-window end |

Duplicate keys, duplicate version identities, malformed required fields and
unsupported formats fail validation. Unknown additive fields remain readable
only after the entire body signature verifies. Version records remain available
after their support window ends. `SupportUntil` is the release publication time
plus `limits.ControllerCompatibilityMonths` calendar months.

`Index` refreshes after `limits.VersionIndexRefresh` (five minutes). A network,
signature or validation failure retains the last verified copy without an age
cutoff and emits a fallback notice. It never replaces verified state with
unverified bytes. An index with an older `published_at`, or different body bytes
at the same timestamp, cannot replace the cached policy. Identical replay is
allowed. On refresh failure, fallback adopts any newer verified policy another
process has saved in the machine cache. An older in-memory copy cannot bypass
that policy's revocations or minimum. For controller runners, a failed fetch or verification without any verified
copy returns `unavailable` with connectivity/retry guidance before execution. First use has only the fetched signature as freshness evidence.

Decision 113: host-owned `Config.LocalOnly` reads the last verified policy without
network refresh. No cached policy yields `NoticePolicyUnavailable` and permits
local execution. Cached restrictions and executable verification still apply.
`EvaluateRunner` refuses a missing policy even if it came from a local resolver.
The local controller selects its local policy only for a persisted local subject.
The embedded queue's startup and refresh checks use the same
`EngineController.CheckRunner` selection as claim admission. The queue
authenticates its bearer before that check; claim admission still authorizes
the requested work separately. A fresh local controller therefore needs no
online runner policy to remain available after admitting a local run.
Machine and service claims keep the strict runner policy. Compatibility and run
status remain available without policy metadata for local execution.

An active individual revocation returns `version_revoked`. A release below an
effective minimum returns `version_unsupported`. Both include the signed reason
and `sparkwing upgrade` as the repository fix. A restriction that affects a
release within its support window requires a security reason. Ordinary changes
announce weeks before enforcement. An emergency security revocation may take
effect immediately. Pending restrictions produce notices before enforcement.

A support-window end alone produces a notice and keeps the engine runnable.
The twelve-calendar-month promise applies to the engine/controller HTTP API.
Each engine carries the matching node protocol. `engine.Notice` is shared by run
output and the controller API/dashboard, so one policy yields the same visible
warning on each surface.

## Upgrades and runner software

`sparkwing upgrade` changes the current repository's SDK pin and native lockfiles.
An explicit `--version` selects a permitted release, otherwise the signed latest
release wins. `Upgrade` computes concrete edits in a private staging
checkout. `UpgradeRequest.Checkout` and `UpgradeVerifier.Verify` both receive
the checkout root. Pin reads, native dependency edits and collected changes use
its validated `.sparkwing` directory. Application manifests at the root remain
separate inputs. `UpgradeVerifier` resolves dependencies, compiles Go or type-checks
TypeScript, then runs describe/plan with the exact verified engine. Job bodies
never run. Only verified changes apply, with each original digest rechecked to
avoid overwriting concurrent edits. Failures leave original pins unchanged.
`--dry-run` reports proposed changes without applying them or restarting services.

Installed machine software remains the `sparkwing update` responsibility.
Runners check signed minimum/revocation policy on startup and index refresh and
prepare their own verified update. A blocked old runner takes no new claims.
The denial cannot stop fetching its verified replacement. `EvaluateRunner` uses
automatic update/restart guidance instead of a repository pin-upgrade hint.
It continues renewing held claims while draining, then replaces the binary,
refreshes its managed units and restarts. The controller enforces the same
minimum before issuing new awards.

`RunnerInstallation.Apply` performs one serialized update: drain, replace,
refresh managed units, then restart. A failed step stops the operation. Individual
stages are private, so callers cannot reorder them. `ApplySoftwareUpdate` checks
the verified release before invoking that operation. Updates preserve credentials,
configuration, executable paths, service registration and schedules.
Its refresh uses the existing managed-unit
refresh boundary for controller services and timers, plus the runner's existing
service registration. Linux, macOS and Windows use their native replacement
paths. A failed update reports an actionable error and retains recoverable prior
configuration and registration. Updating software never requires reinstalling
schedules by hand.

## Acceptance obligations

Implementation must prove exact older-pin selection, Go/TypeScript version
normalization, all supported platforms, missing/bad signatures, wrong embedded
version or architecture, closed-set violations and cache corruption. Cached and
fresh executions must reject the same active revocation or minimum policy.
Index tests cover short-cache reuse, verified fallback, first-use failure,
rollback refusal and identical replay. Support expiry must continue to run.
Upgrade tests cover both native pin formats, lockfile edits, failed verification,
concurrent edits and dry-run. Runner update checks cover claim draining,
renewals, minimum enforcement and service/schedule survival.

## Cross-area agreements

| Consumer | Required seam |
| --- | --- |
| C1 `runspec.Resolved.Engine` | Seal `engine.Identity`; preserve it on retry |
| C3 `source.Acquire` / hosts | Resolve before SDK build/describe/plan; ensure accepted identity before eval/work |
| C3 plan cache | Include the complete engine identity in cache inputs; hits still pass acceptance |
| C3 controller wire | Carry notices and preserve the twelve-month engine/controller API window |
| C4 limits and grammar | Own refresh/compatibility constants and the `upgrade` command |
| C2 API errors | Register `version_revoked` and `version_unsupported`; use `unavailable` for absent verified policy |
| Runner/launcher | Enforce minimum on new claims; every plan/eval/work runtime uses the selected engine |

## Open questions for the lead

None block these declarations. The first-use failure and five-minute refresh are
explicit choices. The verified fallback has no maximum age. Local development
pins that do not identify a published release are outside the automatic signed
release resolver and need an explicit development workflow during implementation.

## Export inventory

### engine.go

| Export | Purpose |
| --- | --- |
| [`SDKKind`](../../../internal/engine/engine.go) | distinguishes the two pin formats without selecting a different engine. |
| [`Pin`](../../../internal/engine/engine.go) | records the SDK requirement, applicable replacement and source files. |
| [`ReadPin`](../../../internal/engine/pin.go) | resolves a Go requirement or a TypeScript lockfile entry without running pipeline code. |
| [`VersionForPin`](../../../internal/engine/pin.go) | maps release pins to their exact engine and Go SDK replacements to the installed engine. |
| [`Identity`](../../../internal/engine/engine.go) | binds the selected engine to SDK inputs in the accepted run seal. |
| [`Resolved`](../../../internal/engine/engine.go) | includes the selected executable for this machine's platform. |
| [`ReleaseBundle`](../../../internal/engine/engine.go) | is untrusted until VerifyRelease checks the shared release trust roots. |
| [`VerifiedRelease`](../../../internal/engine/engine.go) | cannot be constructed outside this package before verification. |
| [`VerifyRelease`](../../../internal/engine/release.go) | uses releaseauth.PublicKeys and releaseasset.Verify, the verify-release trust owners. |
| [`Config`](../../../internal/engine/engine.go) | supplies machine-owned storage and transport, never repository-selected trust roots or URLs. |
| [`Resolver`](../../../internal/engine/engine.go) | owns the version-index cache and verified executable cache for one machine. |
| [`DefaultCacheDirectory`](../../../internal/engine/resolver.go) | Returns the shared OS-native machine cache path. |
| [`NewResolver`](../../../internal/engine/resolver.go) | Uses the shared default unless the host or tests provide a private override. |
| [`Resolve`](../../../internal/engine/resolver.go) | checks the current verified version policy, fetches the exact release only from GitHub Releases when absent, verifies it, and atomically publishes a reusable machine-cache entry. |
| [`Ensure`](../../../internal/engine/resolver.go) | resolves a sealed identity on another executor and requires the same release manifest. |
| [`Cached`](../../../internal/engine/resolver.go) | returns only an integrity-checked cache entry; absence or damage is a miss. |
| [`StoreVerified`](../../../internal/engine/resolver.go) | publishes only verified bytes and retains signatures for future integrity checks. |
| [`VersionInfo`](../../../internal/engine/engine.go) | retains every published release, including releases beyond their support window. |
| [`Restriction`](../../../internal/engine/engine.go) | carries signed notice and enforcement dates; an empty Version means no restriction. |
| [`VersionIndex`](../../../internal/engine/engine.go) | is signed as the exact JSON body bytes. |
| [`SignedIndex`](../../../internal/engine/engine.go) | keeps the exact bytes and detached signature for verification and persistent fallback. |
| [`VerifiedIndex`](../../../internal/engine/engine.go) | is obtainable only by checking the shared releaseauth trust roots. |
| [`VerifyIndex`](../../../internal/engine/policy.go) | checks releaseauth.PublicKeys signatures, unique version identities, required fields, recognized format and restriction dates/reasons before decoding a usable policy. |
| [`Offline`](../../../internal/engine/policy.go) | reports local execution without a cached verified policy. |
| [`Policy`](../../../internal/engine/policy.go) | returns a copy for diagnostics and policy evaluation; callers cannot mutate verified state. |
| [`IndexResult`](../../../internal/engine/engine.go) | reports fallback explicitly so output and the dashboard can show the same notice. |
| [`Index`](../../../internal/engine/index.go) | fetches only VersionIndexURL and its detached signature after the short refresh interval. |
| [`NoticeKind`](../../../internal/engine/engine.go) | is stable presentation data shared by run output and controller API/dashboard readers. |
| [`Notice`](../../../internal/engine/engine.go) | carries actionable policy text without turning a warning into an implicit revocation. |
| [`EvaluateRunner`](../../../internal/engine/policy.go) | Applies the same restrictions to new runner claims with automatic-update guidance. |
| [`Evaluate`](../../../internal/engine/policy.go) | refuses active revocations as apierr.VersionRevoked and releases below an effective minimum as apierr.VersionUnsupported, with signed reason and exactly UpgradeCommand as the fix. |

### upgrade.go

| Export | Purpose |
| --- | --- |
| [`UpgradeRequest`](../../../internal/engine/upgrade.go) | chooses an exact release or the signed index's latest permitted release. |
| [`PinEdit`](../../../internal/engine/upgrade.go) | preserves the source digest so an upgrade cannot overwrite concurrent user edits. |
| [`UpgradePlan`](../../../internal/engine/upgrade.go) | contains concrete pin edits and the matching verified engine selection. |
| [`UpgradeVerifier`](../../../internal/engine/upgrade.go) | checks dependency resolution, Go compilation or TypeScript type checking, then describe/plan with the exact selected engine. |
| [`UpgradeResult`](../../../internal/engine/upgrade.go) | reports verification separately from a dry-run preview. |
| [`Upgrade`](../../../internal/engine/upgrade.go) | verifies the staged pin changes before applying them, rechecking BeforeDigest on each input. |
| [`SoftwareUpdate`](../../../internal/engine/upgrade.go) | keeps a verified runner replacement separate from an SDK-matched pipeline engine. |
| [`PrepareRunnerUpdate`](../../../internal/engine/upgrade.go) | applies the signed minimum/revocation policy to the runner itself, then obtains the latest permitted runner asset through the same GitHub-only verification/cache owner. |
| [`Version`](../../../internal/engine/release.go) | returns the release version whose embedded executable identity was verified. |
| [`Target`](../../../internal/engine/release.go) | returns the exact platform and binary verified against the closed release matrix. |
| [`Asset`](../../../internal/engine/release.go) | supplies immutable verified bytes to the native installer without bypassing verification. |

The constants `GoSDK`, `TypeScriptSDK`, `GoModule`, `TypeScriptPackage`,
`GitHubReleaseBase`, `VersionIndexURL`, `VersionIndexSignatureURL` and
`UpgradeCommand` define the pin vocabulary and fixed publication origins.
`VersionIndexFormat` defines the signed schema version.
`NoticeSupportEnding`, `NoticeSupportEnded`, `NoticeEnforcement` and
`NoticeIndexFallback`, `NoticePinDisabled` and `NoticePolicyUnavailable` define
the warning kinds.

`releaseasset.NamesForVersion` returns the retained closed release matrix.
`Target.NameForVersion` names a target within that matrix. `VerifyForVersion`
applies the shared signature and digest rules against it.

`limits.ControllerCompatibilityMonths` and `VersionIndexRefresh` own the shared
numbers. `runners.RunnerInstallation` and `ApplySoftwareUpdate` declare native
installation and service preservation. `clisurface.Registry` owns command grammar.

<!-- design:end -->
