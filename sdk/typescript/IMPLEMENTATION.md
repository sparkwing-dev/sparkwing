# TypeScript SDK implementation handoff

Snapshot: 2026-10-14. W5 remains incomplete at the native Windows and integrated
host acceptance boundaries. The source implementation and local SDK proofs are
ready for integration. No commits or Sparkwing binaries were run.

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
