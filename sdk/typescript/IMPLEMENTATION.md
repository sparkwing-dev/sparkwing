# TypeScript SDK implementation handoff

Snapshot: 2026-10-14. W5 remains incomplete at the native Windows and integrated
host acceptance boundaries. The source implementation and local SDK proofs are
ready for integration. The lead owns commits, pipelines and native verification.

## Continuation: data-service acceptance boundaries

Added protocol tests for artifact declarations and consumption edges, dependency
and prior-run output reads, explicit child inputs, pending child waits, false-valued
child output and canonical child failures. The tests run the SDK against its
`HostServices` interface with `NodeRoutes` and fake HTTP responses. Optional reads
return absence only for `not_found`. Forbidden, not-ready, unknown future errors
and missing response payloads fail the body instead of becoming cache misses.

`protocol-sdk-artifacts-references-children` now runs these tests as its
`typescript_sdk_boundary` child. `protocol-mask-acknowledgement` now runs the
existing acknowledgement and delivery-error tests. Artifact bytes, authorization,
retention pins, child ancestry and persistence still require the real host/store.
No placement target was enabled by these SDK proofs.

The only shared edit is four lines in `internal/acceptance/typescript.go` selecting
those tests. No runtime or contract changed. No further T1 inventory tests were
ported or legacy lines deleted. The prior 2,896-line deletion remains.

W4's parser and validator are still stubs in this branch. Windows bridge artifacts
and a Windows compiler/runtime are absent. Native Windows, real W3/W4 and
W1/W2/W7 integration, DinD and placements remain open. The W3 route definitions
and W4 cancellation/concurrency agreements below remain unchanged. Another Linux
SDK-only session cannot establish those integration results.

Verification: all 122 TypeScript tests passed with `node --test
--experimental-test-isolation=none 'test/**/*.test.ts'`. Strict typechecking,
Prettier and focused `go vet -p 4 ./sdk/typescript/test/nativehost
./internal/acceptance ./tests/acceptance` passed. `go test -p 4
./internal/acceptance ./tests/acceptance` passed, including Linux native pipes
and both newly enabled SDK children. Package preflight still refuses the missing
Windows x64 bridge. Go checks used `GOWORK=off`, `GOFLAGS=-buildvcs=false`,
`GOMAXPROCS=4`, and caches and temporary files under `/tmp/sw-astra-W5`.
Repository-wide `go build -p 4 ./...` and `git diff --check` passed. The temporary
directory was removed. No Sparkwing binary, Git write or new runtime mechanism
was used.

## Continuation: sealed trigger facts

Pipeline code could change `run.trigger` and its nested pull-request facts.
Those changes reached subsequent bodies and retry attempts because the SDK
froze only the outer run context. The SDK now freezes both nested objects.
The regression sends manual and pull-request invocations through the protocol,
attempts mutation during planning and execution, and checks both attempts' output.
Both cases failed before the fix and passed afterward. The README documents
the read-only facts. No API, contract, dependency or mechanism changed.

All 109 TypeScript tests passed with `node --test
--experimental-test-isolation=none 'test/**/*.test.ts'` from `sdk/typescript`.
Strict typechecking and Prettier passed. Repository-wide `go build -p 4 ./...`,
focused `go vet -p 4 ./sdk/typescript/test/nativehost ./internal/acceptance
./tests/acceptance`, and `go test -p 4 ./internal/acceptance ./tests/acceptance`
passed. The acceptance run includes compiled ESM through Linux native pipes.
The existing SDK children of `protocol-sdk-secrets-mask-annotations` and
`protocol-job-closures-and-skip` include the regression. No placement or Windows
cell was enabled, and no additional T1 inventory test was ported.

No shared file changed. No additional legacy code was deleted. The prior
2,896-line deletion remains. The W3 route definitions, W4 job-only cancellation
and concurrency-presence agreements below remain open. Both Windows bridge
artifacts are still absent and no Windows compiler was found on PATH. Native
Windows execution, W3/W4 host/validator integration, W1/W2/W7 authorization and
persistence, real DinD and placement lanes remain unverified.

Go checks used `GOWORK=off`, `GOFLAGS=-buildvcs=false`, `GOMAXPROCS=4`, and
`GOCACHE`, `GOTMPDIR` and `TMPDIR` under `/tmp/sw-astra-W5`. No Sparkwing binary
or Git write ran. The temporary directory was removed after verification.

## Continuation: portable acceptance fixtures

The Docker, Git and memo tests now use the platform temp directory instead of
seven hard-coded W5 scratch paths. Git and Docker assertions use native path
separators. The tree test exercises directory links, using a junction on Windows,
and retains file-link coverage on Unix.

Before the change, ten tests failed when the hard-coded helper directory was
unavailable even though `TMPDIR` selected a writable directory. After the change,
all 106 TypeScript tests passed with the old path still blocked and a temporary
path containing spaces. Strict typechecking and Prettier passed. No runtime API,
contract, dependency or mechanism changed. No shared file changed and no further
legacy code or T1 inventory test was deleted or ported. The prior legacy deletion
remains 2,896 lines.

Repository-wide `go build -p 4 ./...` passed. `go vet -p 4` passed for
`sdk/typescript/test/nativehost`, `internal/acceptance` and `tests/acceptance`.
`go test -p 4 ./internal/acceptance ./tests/acceptance` passed, including the Linux
native-pipe fixture. The existing SDK children in
`protocol-memo-input-boundaries` and `protocol-host-layers-risk-cleanup` cover the
changed fixtures. Placement targets remain skipped by the acceptance skeleton.

Go commands used `GOWORK=off`, `GOFLAGS=-buildvcs=false`, `GOMAXPROCS=4`, and
`GOCACHE`, `GOTMPDIR` and `TMPDIR` beneath `/tmp/sw-astra-W5`. Node tests used
`TMPDIR='/tmp/sw-astra-W5/portable temp'` and
`node --test --experimental-test-isolation=none 'test/**/*.test.ts'` from
`sdk/typescript`. The temporary files were removed after verification.

Native Windows execution remains blocked. PowerShell is on PATH through WSL,
but a read-only tool-discovery invocation failed with
`UtilBindVsockAnyPort: socket failed 1`. No Windows compiler is available in this
sandbox. Package preflight still refuses the missing x64 bridge. Both x64 and
arm64 builds and native runs remain required. Another Linux-only session cannot
establish those acceptance results.

The W3/W4 parser, validator and host integration points below remain open, as do
the W3 service routes, W4 job-only cancellation and concurrency-presence contract
agreements. Real storage, authorization, DinD and placement tests still need the
other workers' implementations. No integration or native Windows cell was
marked complete by these fixture changes.

## Implemented

- Protocol 2 hello, describe, flat plan, run_step, every eval kind and cancellation.
  Credentials come from hello and stay out of public context and child env.
- All declared author builders, groups, approvals, recovery, generators,
  references, arguments, policies and presence-bearing settings.
- Sealed declarations, recorded-output generator replay, atomic local expansion
  registration, attempt-local step outputs and body/startup env separation.
- Lazy host services for secrets, plain variables, outputs, annotations, summaries,
  acknowledged masking, OIDC, child runs, latest refs and cleanup.
- Command, filesystem, memo, Git, Docker and service helpers. Docker uses copy
  operations and owned container cleanup. Service cleanup uses immutable IDs.
- Public root exports and compiled ESM packaging. The preload and v1 aliases are
  removed. Error constructor signatures in index.d.ts now describe their runtime
  arguments instead of inheriting unusable Error constructors.
- Windows argv parser and native bridge source/build script. The bridge uses
  Node's exported libuv conversion because Node and an addon may use different
  C-runtime descriptor tables. See the primary
  [libuv implementation](https://github.com/libuv/libuv/blob/v1.x/src/win/pipe.c).
  Package preparation refuses missing x64 or arm64 bridge artifacts.

Deleted prototype lines, measured by git diff: 1,561 source lines and 1,335 test
lines. These include the 70-line preload/startup pair, run_node dispatch,
environment bearer transport, v1 documents and obsolete fixtures.

## Verification

- All 95 TypeScript tests passed, including native Unix private-pipe execution.
- Strict TypeScript checking passed, including a consumer importing only the
  public package, every author category, and negative checks for removed APIs.
- The compiled ESM build passed. Package dry-run inspection includes dist/index.js
  and index.d.ts and excludes prototype source/preload files.
- Repository-wide `go build -p 4 ./...` passed after caller migration.
- `go vet -p 4` passed for internal/acceptance, tests/acceptance,
  internal/pipelineentry and internal/nodeproto/conformance.
- `go test -p 4 ./internal/acceptance ./tests/acceptance` passed.
- Focused pipelineentry detection and no-preload launch tests passed.
- `git diff --check` passed. No shared Git config worktree override was present.

Go checks used GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4 and GOCACHE,
GOTMPDIR and TMPDIR beneath /tmp/sw-astra-W5. TypeScript tests ran with
`node --test --experimental-test-isolation=none 'test/**/*.test.ts'` because this
sandbox blocks Node's socket-based child-test stdio. The task temp directory was
removed after verification.

## Acceptance boundary cells enabled

`tests/acceptance` now executes a `typescript_sdk_boundary` child in:

- protocol-hello-and-handles
- protocol-version-and-hard-cut
- protocol-envelope-transcripts
- protocol-strict-plan-parity
- protocol-errors-and-frame-identity
- protocol-sdk-secrets-mask-annotations
- protocol-sdk-artifacts-references-children
- protocol-mask-acknowledgement
- protocol-job-closures-and-skip
- protocol-memo-input-boundaries
- protocol-host-layers-risk-cleanup

These are SDK proofs with a fake host where needed. The existing placement,
persistence, Go/TS parity and native Windows cells remain pending. They were not
marked successful by running Linux fakes.

The retained TypeScript coverage was ported to v2: argument/default validation,
graphs and groups, closure dispatch, generated reconstruction, dry-run declarations,
body failures, concurrent steps, cancellation/EOF, 16 MiB output boundaries,
route isolation and framing. Go helper behaviors were ported for command
capture/errors/cancellation, memo inputs, Git tags, Docker digest/cleanup and
service readiness. The T1 port inventory has no TypeScript-specific source rows;
its host/store findings are not claimed closed by W5.

## Contract agreements needed

- W4 must accept job-only cancel requests for job-level evals. A step-only cancel
  cannot address skip_if, memo or generator closures. The existing Request type
  already makes step optional; its Validate implementation and host callers must
  agree with this SDK behavior.
- W3 must define or confirm the missing bound-service routes: POST
  /node/v1/annotations (end_line wire field), /node/v1/summary (markdown), and
  /node/v1/masks (value, response after registration). The SDK implements these
  against its HostServices interface and tests them with fakes. Existing output
  routes use the declared /node/v1/jobs path and job query spelling.
- W4/C4 must resolve concurrency omission. Author ConcurrencyLimit fields are
  optional, but Go ConcurrencySpec requires scope/capacity/on_limit. The builder
  uses retained global/1/queue defaults and cost 1 because that is the available
  wire. If omission must reach settings resolution, change that wire contract.

No other area's contract files were changed. W5's local declaration repair adds
constructors for SDKError, ExecError, InputError, VerifyError and StepError.
SecretSpec.variable remains SDK accessor classification; the host's resolver
selects masked versus plain values, so describe still carries names and optionality.

## Real integration still open

1. Build both Windows bridge artifacts, package them, and run native Windows
   raw-handle/child-inheritance tests. The packaging preflight currently fails
   because these artifacts are absent. Linux parser tests prove no native pass.
2. Run the SDK against W3's host and W4's validator/schema corpus. Verify accepted
   hashes, replay drift, shared limits, secret-containing identifiers, risk,
   retries and actual cleanup after process death.
3. Exercise W1/W2/W7 authorization and persistence: artifacts, memo/depcache,
   reference pins, secret masking, child ancestry, OIDC and failure propagation.
4. Run actual Docker/DinD and Git remote helper cases, all six placements, and the
   Windows/Darwin native lanes. W9 must stamp the npm version with the release.
5. Migrate W4's old conformance corpus to v2. Its TypeScript launch caller is
   updated, but the shared v1 request/transcript suite was not rewritten by W5.
   W9's old protocol docs and generated mirrors also remain in its scope.

## Shared files changed

- internal/acceptance/typescript.go: SDK boundary test selection.
- tests/acceptance/typescript_test.go: Node boundary driver.
- tests/acceptance/skeleton_test.go: invoke the SDK child before pending targets.
- internal/pipelineentry/pipelineentry.go: remove preload lookup and Node's
  bootstrap argument.
- internal/pipelineentry/pipelineentry_test.go: migrate the launch assertion.
- internal/nodeproto/conformance/conformance_test.go: migrate the Node caller.

## Friction and implementation choices

npm registry access failed. The exact lockfile dependencies were extracted from
available local npm cache blobs. No dependency versions changed. Node's async
stdio over child socket pairs is blocked here; tests use direct writes and real
FIFOs, and the acceptance driver uses Node's in-process test mode. Native Windows
build/run tooling is unavailable. Sparkwing pipelines remain the lead's work.

The added mechanisms implement the frozen scope: an async planning guard,
private command bindings, a host-service test seam, and the Windows handle
bridge. The bridge is required because raw Windows handles are not Node file
descriptors. No new environment knob, runtime dependency or compatibility shim
was added. Local multi-platform Docker builds request --load so an unsupported
image store fails rather than reporting an image that exists only in build cache.

## Continuation: native transport and package evidence

This continues commit `a00605433`. The implementation above remains in place.

Added a native host fixture in `test/nativehost`. It imports the compiled public
package and exchanges the actual Go `nodeproto` contract types over OS pipes.
It covers hello, describe, explicit zero/false plan fields, box concurrency,
body environment, all six eval kinds, generated declarations, paired cancellation
replies, debug forwarding and separation of author stdout from protocol frames.
A child tool checks that it cannot access either protocol pipe or the old bearer
environment value. The host compares Node's architecture with its own so an
emulated x64 Node cannot establish an arm64 bridge pass.

Unix uses inherited descriptors 3/4. Windows uses `AdditionalInheritedHandles`
and decimal argv handles. `npm run test:native` builds the SDK and runs this
fixture. Missing dependencies or the native bridge fail. There are no skipped
platform branches in this host fixture.

`protocol-additive-fields-and-child-handles` now executes both the TypeScript
boundary tests and a `typescript_native_pipes` child. Its placement targets
remain pending. The fake host uses concrete request/reply/plan structs because
W4's parser and validator functions still panic in this branch. This establishes
wire-shape agreement, not shared validation or Go SDK behavioral parity.

The package preflight now rejects an architecture mismatch, an executable in
place of a DLL, and truncated or invalid image headers. The regression test
first reproduced acceptance of an x64 DLL header under the arm64 filename.
Its image fixtures exercise header checks only. Real addon loading remains the
native fixture's responsibility. Header offsets and machine values follow the
[Microsoft PE specification](https://learn.microsoft.com/en-us/windows/win32/debug/pe-format).

No additional prototype code was removed. The previous hard cut removed 2,896
source/test lines. This continuation replaces three lines of the existence-only
package check. No further T1 inventory rows belong to W5, and no previously
ported tests were removed.

Shared files changed in this continuation:

- `internal/acceptance/typescript.go`: add the SDK tests for the child-handle cell.
- `tests/acceptance/typescript_test.go`: run the native host fixture in that cell.

No shared contract changed. The three contract agreements above remain open:
W3 bound-service route definitions, W4 job-only cancellation and C4 concurrency
omission. Native Windows x64/arm64 addon builds and execution, real W3/W4
host/validator integration, W1/W2/W7 authorization and persistence, and the
placement/DinD lanes still require their owners' implementations or machines.
The package gate continues to refuse this checkout because both Windows bridge
binaries are absent. No release artifact was produced.

The added test host is needed because a JavaScript stream fake cannot establish
OS handle inheritance or agreement with Go's contract structs. It adds no runtime
dependency, environment setting or public SDK API. Removing `test/nativehost`,
its npm script and its acceptance invocation reverses that test-only mechanism.

Verification for this continuation:

- All 96 TypeScript tests passed with
  `node --test --experimental-test-isolation=none 'test/**/*.test.ts'`.
- `node node_modules/typescript/bin/tsc -p tsconfig.json` passed.
- The native fixture passed on Linux against compiled ESM, including its child
  pipe-access probe. Its compilation runs `tsc -p tsconfig.build.json`.
- `go build -p 4 ./...` passed for the whole repository.
- `go test -p 4 ./internal/acceptance ./tests/acceptance` passed. After the final
  native fixture change, the focused
  `go test -p 4 ./tests/acceptance -run 'TestProtocol/protocol-additive-fields-and-child-handles' -v`
  passed both SDK children. All 34 placement targets in that scenario remain
  skipped by the existing acceptance skeleton.
- `go vet -p 4 ./sdk/typescript/test/nativehost ./internal/acceptance ./tests/acceptance`
  passed after the final code change.
- Both `GOOS=windows GOARCH=amd64 go test -p 4 -c -o <temporary-path> ./sdk/typescript/test/nativehost`
  and its `GOARCH=arm64` counterpart passed. These compile Go test hosts only.
  They neither compile nor execute the native C bridge.
- `node native/check-package.mjs` refused the missing x64 bridge as expected.
  Native build/run tooling is absent here. This remains a release blocker.
- `git diff --check` passed. Shared Git config has no `core.worktree` override.

Go commands used `GOWORK=off`, `GOFLAGS=-buildvcs=false`, `GOMAXPROCS=4`,
`GOCACHE=/tmp/sw-astra-W5/gocache`, `GOTMPDIR=/tmp/sw-astra-W5/gotmp` and
`TMPDIR=/tmp/sw-astra-W5`. Node tests used that same `TMPDIR`. The temporary
directory, cross-compiled test executables and check output were removed after
verification. No Sparkwing binary ran and no Git write occurred. The lead still
owns independent review, pipelines and the native release lanes.

## Continuation: eval cancellation and retry output isolation

This continues commit `dccdf3b24`. Two SDK failures were reproduced through the
existing protocol test host before changing the implementation:

- A cancelled `after_run` closure returned success when its callback completed
  normally. It now checks cancellation after the callback, as the other closure
  kinds already did. The regression exercises all six kinds and verifies one
  reply for cancel and one failed eval result for the interrupted invocation.
- An eval carrying attempt 2 could read step outputs from attempt 1 before the
  next step request cleared them. Attempt validation and output reset now run
  when either an eval or a step enters an attempt. Tests cover before/after
  hooks, stale ordinals, overlap with a running earlier step, and continued
  execution after that overlap is refused.

No public API, contract, dependency, environment variable or runtime mechanism
was added. The existing attempt check moved to the shared context construction
path. W3 must send the existing `Request.Attempt` on attempt-scoped evals so
retry hooks can identify their attempt before its first step. Evals without an
attempt keep their existing behavior for owner-side conditions and replay.

The SDK README now describes these behaviors, and the protocol regression tests
exercise them. No shared files changed. No further legacy code or T1 inventory
tests were removed or ported. The earlier 2,896-line legacy deletion stands.

All 105 TypeScript tests passed, including six cancellation subtests. Strict
typechecking and Prettier checks passed. `go vet -p 4` passed for the native SDK
test host and both acceptance packages. `go test -p 4 ./internal/acceptance
./tests/acceptance` passed, including the Linux native-pipe fixture against the
compiled public package. The existing `typescript_sdk_boundary` children of
`protocol-job-closures-and-skip` and `protocol-sdk-secrets-mask-annotations` now
include the regressions. No additional placement or Windows cell was enabled.
Repository-wide `go build -p 4 ./...` and `git diff --check` passed. The shared
Git config has no `core.worktree` override.

The package preflight still refuses the missing Windows x64 bridge. No Windows
compiler or runtime tooling was found on PATH. Both native
bridge builds and native x64/arm64 execution remain open. W3/W4 host and shared
validation, W1/W2/W7 authorization and persistence, real DinD, and the placement
lanes remain integration work. The existing route, job-only cancellation and
concurrency-presence agreements above remain unresolved. No other area's
contract was changed.

Commands used the same Go flags as the previous continuation, with `GOCACHE`
and `GOTMPDIR` under `/tmp/sw-astra-W5` and `TMPDIR=/tmp/sw-astra-W5/tmp`.
Node tests used `--experimental-test-isolation=none`. No Sparkwing binary or
Git write ran. The lead retains review, commits, pipelines and native lanes.
The temporary cache and test directory `/tmp/sw-astra-W5` was removed after
verification.

## Continuation: service callback cancellation

This continues commit `18488cf51`. A public-helper regression reproduced a
cancelled service scope resolving successfully when its callback returned
normally. `withServices` and `withServicesAddrs` now check the signal after the
callback. Container removal still runs with a live cleanup signal. A failed
removal preserves the cancellation error and leaves host cleanup registered.
The regression covers successful and failed removal without sleeps or Docker.
The README describes the behavior.

All 106 TypeScript tests and strict typechecking passed. The changed TypeScript
files pass Prettier. `go vet -p 4` passed for the native SDK fixture and both
acceptance packages. `go test -p 4 ./internal/acceptance ./tests/acceptance`
passed, including compiled ESM execution through the Linux native pipes. The
existing `protocol-host-layers-risk-cleanup/typescript_sdk_boundary` now includes
the service regression. No additional placement or native Windows cell was
enabled.

No public API, contract, dependency, environment variable or mechanism was
added. No shared files changed, no additional T1 test was ported, and no legacy
code was deleted. The earlier 2,896-line deletion stands.

Windows remains the W5-specific completion blocker. No Windows compiler or
runtime is on PATH, and package preparation refuses the absent x64 bridge.
Both bridge artifacts and native x64/arm64 execution still need a Windows
environment. The shared protocol parser and validator remain panic stubs in
this branch, so SDK tests continue to use the declared structs and fake host
services. The existing W3 service-route, W4 job-only-cancellation and concurrency
presence agreements remain open. Real host, storage, authorization, DinD and
placement integration still require the corresponding workers' implementations.

Repository-wide `go build -p 4 ./...` and `git diff --check` passed. Go checks
used `GOWORK=off`, `GOFLAGS=-buildvcs=false`, `GOMAXPROCS=4`, and `GOCACHE`,
`GOTMPDIR` and `TMPDIR` beneath `/tmp/sw-astra-W5`. Node tests used that same
`TMPDIR` and `--experimental-test-isolation=none`. The temporary directory was
removed after all commands finished. Shared Git config has no `core.worktree`
override.

No Git write occurred. Initial discovery ran installed `sparkwing info
--for-agent` before the swarm brief's no-binary rule was read. No further
Sparkwing command, candidate or pipeline ran. The lead retains commits, review,
pipelines and native verification.
