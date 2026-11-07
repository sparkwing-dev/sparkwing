> Historical measurement record. The compatibility fixtures and matrix were removed before launch.

# Historical SDK compatibility evidence

Implemented the direct compatibility matrix for the three released Go modules
and three npm packages selected independently on 2026-11-05. Both registries
selected 0.69.2, 0.69.1 and 0.69.0. QA, prerelease and retracted Go releases
were excluded. The manifest
records each artifact checksum, frozen source revision, toolchain contract,
wire major, advertised capabilities and expected result. Go fixtures have exact
go.mod/go.sum files without replacements. npm fixtures have exact lockfiles
with published tarball integrity.

The candidate is the test executable containing this checkout's nodehost,
protocol session, identity validation and executor. The principal cells launch
released SDK processes over real private pipes. The executor cells inject a
test resolver returning that same candidate identity and process entry. No
historical engine can replace it. Hello uses each real released SDK identity,
and logs record the candidate executable SHA-256 separately.

The approved proposal Decisions section was reread after it appeared. This
change freezes legacy behavior. It adds no thin SDK release, operation service,
compatible-host mapping or change to production routing.

## Verified boundaries

Each of the six cells asserts hello identity/capabilities, describe, argument
metadata, plan validation, argument values and typed step output, stdout and
stderr, exit 7, cancellation after a real grandchild starts, and termination of
that grandchild after host Close. HTTP barriers force separate deliveries of a
split secret through both the SDK exec stream and the outer raw stdout pipe.
The host must mask the secret and withhold its partial prefix. Returned secret
and OIDC values remain raw callback results while host logs contain neither.

The lifecycle cells run two overlapping steps through NodeExecutor, fail a
local executable once, retry through the host and pass on the next attempt.
They execute a dependent job with its source persisted as successful. They
check the declared one-minute timeout, inject deadline expiry after real helper
readiness, and assert the host's failure result and process cleanup. They
exercise a released memo callback through host miss/hit lookup and verified
output copying, declared cache save/delete/restore through the real cache
adapter with local blob fakes, and generated declarations through persisted
ancestor discovery and host replay. Git and Docker are local fake executables.
OIDC and secret reads use the registered nodehost router with local fake
services. No provider or real database is contacted.

The dependent executor test starts from a persisted successful source. It does
not retest scheduler readiness blocking. Timeout expiry is injected rather
than waiting one minute. These choices keep the fixture deterministic.

## Raw timings

Linux amd64, Go 1.26.10, Node 26.13.0, npm 10.9.8. Cells ran sequentially on
the shared machine. These are single observations, not latency guarantees.
All six passed in each recorded run. Candidate executable SHA-256:
`1d0ceedd41e1e64e40eb04494b1fea5778ddb4b97bd3060e0b13759c5e13456b`.

| Cell | Warm preparation ms | Warm whole cell ms | Cold preparation ms | Cold whole cell ms |
| --- | ---: | ---: | ---: | ---: |
| Go v0.69.2 | 45 | 1076 | 416 | 1545 |
| Go v0.69.1 | 45 | 1050 | 446 | 1590 |
| Go v0.69.0 | 47 | 1085 | 414 | 1485 |
| TypeScript 0.69.2 | 92 | 4498 | 688 | 4520 |
| TypeScript 0.69.1 | 94 | 3889 | 560 | 4314 |
| TypeScript 0.69.0 | 90 | 4067 | 561 | 4603 |

| Invocation | Wall seconds | User seconds | System seconds |
| --- | ---: | ---: | ---: |
| Initial final-fixture preparation and execution | 22.66 | 24.51 | 7.44 |
| Warm fixture reuse and execution | 17.52 | 8.44 | 5.72 |
| Cold fixture preparation and execution | 20.09 | 10.92 | 6.95 |

Warm reused all six prepared artifacts. Cold bypassed every prepared artifact
and npm used empty private download caches. Go retained the required shared
module cache and compiler cache. This is a cold prepared-fixture measurement,
not a cold Go toolchain/download benchmark. Whole-cell time includes
preparation, execution and cleanup. Invocation wall time also includes the Go
test command's build/setup work.

[measurements.json](measurements.json) preserves the raw per-cell records,
fixture identities, checksums, cache keys, host identity, tool versions,
authoring measurements and process clocks. [warm.log](warm.log),
[cold.log](cold.log) and [warm-fill.log](warm-fill.log) retain the raw output.

## Shared behavior vectors

vectors.json contains 14 vectors.
Five execute against every released artifact:

- Callback environment mutation reaches subprocesses in both languages.
- Go string capture preserves a byte-order mark. TypeScript trims it.
- Go Git helpers retain quoted newline-bearing paths. TypeScript decodes NUL
  separated paths into the original newline-bearing string.
- Go Docker results use exported field names and null registries. TypeScript
  uses lower-case names and an empty array.
- Go Secret refuses a value whose stored kind changes to unmasked.
  TypeScript returns the value.

Source-inspection vectors also record protocol-token environment overrides,
body runtime-option filtering, Unix signal escalation and session-ledger
ownership, Windows tree termination, missing Docker metadata, bind mounts,
file-helper phase guards and malformed run-context validation. Their evidence
labels distinguish inspection from execution. No behavior was aligned or
changed. Each difference remains for the maintainer to ratify before porting.

## Delivery and checks

`gate` selects warm cells for host, SDK, protocol, environment, process, secret
or pin changes. The broad `--full` override includes them. `pre-release` runs
all six with cold preparation. Neither hook tier acquires historical SDKs.
The worktree's `.sparkwing/cache/sdk-compat/` survives isolated test homes.
Keys include frozen source, released artifact, toolchain and platform, while
excluding the candidate host. `-count=1` reruns the candidate every time.

The installed Sparkwing v0.69.2 launcher ran only the new pre-release step:

```sh
sparkwing run pre-release --sw-start-at sdk-compat --sw-stop-at sdk-compat
```

It passed as `run-3888b0cfb383a64b817b1a16` under an isolated HOME and
SPARKWING_HOME. [pipeline.ndjson](pipeline.ndjson) retains the run log.
The matrix inside that job built the candidate host from this checkout.

Focused checks passed: six warm and six cold runtime cells, fixture-cache
and artifact-integrity tests (0.050 s), pipeline selection/invocation tests
(0.101 s), commentcheck across 13 changed Go files, formatter checks, and
golangci-lint with zero issues for nodehost/sdkcompat. Pipeline-job lint had
also passed with zero issues. The full gate belongs to the lead after this
session. No landing or push was performed.

Measured runtime coverage is Linux. The process fixtures use Unix helpers.
Native macOS/Windows execution, historical Windows bridge coverage, the
separate 12-month routing matrix and future thin/capability anchors are not
established by these measurements. The first thin artifact does not exist yet.
Those cells remain distinct from this six-release direct matrix.

## Friction and mechanisms

- A private HOME hid asdf's Go selection. Explicit ASDF_DATA_DIR and
  ASDF_GOLANG_VERSION restored it while preserving the required shared Go
  caches.
- Existing Go-only executor fakes supplied an empty trigger source and a
  publisher that refused output. TypeScript exposed both assumptions. The
  fixture now supplies a valid local run context and accepts the dependent
  callback's explicit null output.
- Reusing a terminal step in one attempt was refused. Memo and replay now
  share the original verified output bytes instead of invoking that body twice.
- Concurrent explicit-path commits briefly collided. Git writers were then
  sequenced. No reset, amend, rebase or git configuration edit was used.
- Overwing reported its installed binary behind source. Standards discovery
  and reads still worked.

The new mechanisms are the requested fixture preparation cache, test-only
matrix flags, local barrier/helper processes and the affected check selector.
They replace repeated ad hoc builds and transcript-only assertions. There are
no new product environment variables, services or runtime policy layers.

Decisions needing the lead

Ratify the recorded language differences before any thinning port. Native
platform and long-window routing cells need their own verification work.
