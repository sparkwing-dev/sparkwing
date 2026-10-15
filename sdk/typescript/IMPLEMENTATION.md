# TypeScript SDK implementation handoff

W5 remains incomplete at the native Windows and real host/store boundaries.
The author API and SDK implementation are ready for those integrations.
The lead owns commits, pipelines, independent review and native verification.

## This continuation

Command environment maps now preserve valid names such as `__proto__`.
Command options, `Cmd.env`, `Cmd.envMap`, `withCommandEnv` and body env all
reach child tools. Restoring body env distinguishes an absent variable from
an inherited JavaScript property, so closing the protocol restores absence.
Two regressions failed before the fix and passed afterward. The protocol
regression also checks that recorded body env overrides later ambient changes.

Only `src/command.ts`, `src/runner.ts` and their tests changed at runtime.
This handoff replaces repeated session notes. No public API, contract,
dependency, environment setting or runtime mechanism was added. No shared
file changed in this continuation.

## Implemented

- Protocol 2 hello, describe, flat plan, run_step, every eval kind and
  cancellation. Hello supplies credentials and granted runner identity.
  Credentials stay out of public context and child env.
- Every declared author builder, group, approval condition, recovery,
  generator, reference, argument and policy. Presence-bearing settings retain
  zero and false. Duration args accept Go syntax, including signed zero.
- Sealed declarations and trigger facts, recorded-output generator replay,
  atomic local expansion registration, attempt-local step outputs and
  startup/body env separation. Attempt-scoped evals reset outputs before hooks
  run. Cancelled bodies and closures fail even if callbacks return normally.
- Lazy host services for secrets, plain variables, outputs, annotations,
  summaries, acknowledged masks, OIDC, child runs, latest refs and cleanup.
  Optional reads suppress only `not_found`.
- Command, file, memo, Git, Docker and service helpers. Commands preserve
  capture/error/cancellation diagnostics. Git tests use a real temporary bare
  remote through file transport. Fileset hashes omit literal ignored directory
  descendants. Docker uses `cp` under DinD. Service cleanup uses immutable
  container IDs, runs after cancellation and preserves the first failure.
- Public root exports and compiled ESM packaging. Error constructor signatures
  match their runtime arguments. Consumer typechecking covers every author
  category and refuses removed APIs.
- Windows argv parsing and native bridge source/build script. Package preparation
  requires both x64 and arm64 bridge artifacts and checks their PE architecture,
  DLL flag and header validity.

The earlier hard cut deleted 1,561 source lines and 1,335 test lines, totaling
2,896. It removed preload/startup, run_node, env bearer transport, v1 documents
and obsolete fixtures. This continuation deletes no further legacy source code.

## Acceptance boundary cells enabled

The `typescript_sdk_boundary` child runs in these scenarios:

- protocol-hello-and-handles
- protocol-version-and-hard-cut
- protocol-envelope-transcripts
- protocol-additive-fields-and-child-handles
- protocol-strict-plan-parity
- protocol-errors-and-frame-identity
- protocol-sdk-secrets-mask-annotations
- protocol-sdk-artifacts-references-children
- protocol-mask-acknowledgement
- protocol-job-closures-and-skip
- protocol-memo-input-boundaries
- protocol-host-layers-risk-cleanup

The child-handle scenario also runs `typescript_native_pipes`. Its test host
imports compiled ESM and exchanges concrete Go `nodeproto` contract structs
over OS pipes. It checks every eval kind, explicit zero/false settings, body
env, cancellation, frame/stdout separation and child handle/credential exclusion.
Unix uses inherited descriptors 3/4. Windows uses AdditionalInheritedHandles
and decimal argv handles. The fixture requires matching Go/Node architectures.

These are SDK boundary proofs. The shared parser and validator still panic in
this branch, so the fixture does not invoke them. Placement, persistence,
Go/TS behavioral parity and native Windows results remain pending. No new
acceptance cell was enabled in this continuation. Existing runner and command
children include the new regressions.

Retained prototype coverage was ported to v2 for args/defaults, graphs/groups,
closure dispatch, generated reconstruction, dry-run declarations, body failures,
concurrent steps, cancellation/EOF, 16 MiB outputs, route isolation and framing.
Go helper behaviors were ported for command errors/cancellation, memo inputs,
Git tags, Docker digest/cleanup and service readiness. The T1 inventory has no
TypeScript-specific source rows. This continuation adds two regressions and
ports no further inventory tests.

## Contract agreements needed from other owners

- W3 must define or confirm POST `/node/v1/annotations` with `end_line`, POST
  `/node/v1/summary` with `markdown`, and POST `/node/v1/masks` with `value`.
  Mask replies follow registration. The SDK tests these against HostServices
  and NodeRoutes fakes. Existing output paths use `/node/v1/jobs` and query `job`.
- W4 must accept job-only cancel for job-level evals. Request already permits
  an omitted step. Its validator and host callers must agree.
- W4/C4 must resolve concurrency omission. Author ConcurrencyLimit fields are
  optional, but wire ConcurrencySpec requires scope/capacity/on_limit. The SDK
  emits retained global/1/queue defaults and cost 1 against that contract.

No other area's contract was changed. W5's earlier local declaration repair
added constructors for SDKError, ExecError, InputError, VerifyError and StepError.
SecretSpec.variable is accessor classification. Describe carries secret names
and optionality, while the host resolver chooses masked or plain values.
W3 must send existing Request.Attempt on attempt-scoped evals so retry hooks
identify their attempt before its first step.

## Real integration left open

1. Build both Windows bridge artifacts, package them and run native x64/arm64
   handle and child-inheritance tests. Package preflight refuses the missing
   x64 bridge. No Windows compiler is available in this sandbox. Linux parser
   tests and cross-compiled Go hosts establish no native Windows pass.
2. Run against W3's host and W4's validator/schema corpus. Verify canonical
   hashes, replay drift, shared limits, secret-bearing IDs, risk, retries and
   actual cleanup after SDK process death. Migrate W4's remaining v1 conformance
   corpus to v2. SDK launch callers are already migrated.
3. Exercise W1/W2/W7 authorization and persistence for artifacts, memo/depcache,
   reference pins, masks, child ancestry, OIDC and failure propagation.
4. Run authenticated Git transports, actual Docker/DinD, all six placements
   and native Darwin/Windows lanes. W9 owns npm release-version stamping and
   remaining protocol docs/generated mirrors.

## Verification

All 129 TypeScript tests and strict typechecking passed. Both new regressions
ran red before the repair. Prettier passed for changed TypeScript files.
Repository-wide `go build -p 4 ./...` and focused `go vet -p 4` passed.
`go test -p 4` passed for `./sdk/typescript/test/nativehost`,
`./internal/acceptance` and `./tests/acceptance`, including Linux native pipes
and existing SDK acceptance children. Placement targets remain skipped.

Go checks use `GOWORK=off`, `GOFLAGS=-buildvcs=false`, `GOMAXPROCS=4`,
`GOCACHE=/tmp/sw-astra-W5/go-cache`, `GOTMPDIR=/tmp/sw-astra-W5/go-tmp` and
`TMPDIR=/tmp/sw-astra-W5/tmp`. Invoke `go -C <absolute-worktree>`.
TypeScript checks run from the SDK with `env -C <absolute-sdk-path>`:

```sh
node --test --experimental-test-isolation=none 'test/**/*.test.ts'
node node_modules/typescript/bin/tsc -p tsconfig.json
```

The Linux native fixture builds compiled ESM itself. Package preflight remains
blocked by missing Windows artifacts. The lead must run pipelines and gates.
No Sparkwing binary or Git write ran in this continuation.
The temporary cache/test directory `/tmp/sw-astra-W5/` was removed after checks.

## Shared files migrated in earlier sessions

- internal/acceptance/typescript.go: SDK boundary test selection.
- tests/acceptance/typescript_test.go: Node boundary and native-pipe driver.
- tests/acceptance/skeleton_test.go: SDK children before pending targets.
- internal/pipelineentry/pipelineentry.go: remove preload lookup/Node bootstrap.
- internal/pipelineentry/pipelineentry_test.go: migrate launch assertion.
- internal/nodeproto/conformance/conformance_test.go: migrate Node caller.

## Friction and implementation choices

The exact lockfile dependencies were recovered from local npm cache because
registry access failed. No versions changed. Node's child-test socket stdio is
blocked here, so tests use in-process isolation mode and native pipe fixtures.
Missing Windows build/run tooling prevents package completion. Another Linux
run cannot establish Windows or placement acceptance.

The existing mechanisms implement the frozen scope: an async planning guard,
private command bindings, HostServices test seam and Windows handle bridge.
The bridge uses Node's exported libuv conversion because the addon and Node
may have separate C-runtime fd tables. See the primary
[libuv implementation](https://github.com/libuv/libuv/blob/v1.x/src/win/pipe.c).
Package architecture checks follow the
[Microsoft PE specification](https://learn.microsoft.com/en-us/windows/win32/debug/pe-format).
The native test host is needed because stream fakes cannot prove OS handle
inheritance or agreement with Go's contract structs. Local multi-platform
Docker builds request `--load`, propagating unsupported image-store errors.
