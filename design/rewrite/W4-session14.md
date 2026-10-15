# W4 continuation report

Continues `5126706a2` and `W4-session13.md`.

Recovery chains now serialize every declared job with its immediate recovery
parent. Lookup and argument registration traverse the same chain. Generated
chains retain the source dependency and generator ancestry on every member.
Previously a recovery attached to a recovery disappeared from the document,
describe and execution lookup.

Generated jobs now bind `WithArgs` before their validated expansion becomes
executable. Binding uses a snapshot of the original run arguments, so caller
mutation cannot change replay. Defaults, required fields and enums apply to
ordinary and recovery jobs. Name and short-alias collisions with pipeline
inputs, initial jobs and earlier expansions fail without inserting executable
jobs. A pipeline's existing extra-input bag carries flags introduced only
during generation. Describe still does not execute generators.

The acceptance regression failed before each fix. It passes through fake-host
evaluation and SDK subprocesses. It checks the complete recovery chain,
describe metadata, defaults, overrides, required fields, enums, generated
ancestry and execution. Invalid generated arguments return `invalid_args` and
leave the target absent. Unit regressions cover ordinary/generated recovery
arguments, argument snapshots and all four collision sources. The existing
pipeline-flag collision test now includes nested recoveries.

The C3 Go notes clarify these retained declaration semantics. No public
signature, wire field or external contract changed. The only added runtime
state is the original argument snapshot because typed pipeline inputs cannot
recover dynamically declared job flags. One internal iterator owns recovery
traversal. No new setting, environment variable, interface or service was added.

Deleted: 0 additional legacy-API lines. Replaced 31 production lines of shallow
traversal and incomplete binding. No removed API required caller migration.
No new T1 inventory port was needed. The existing `TestSecretIdentifiers` port
passes in the focused package suite.

Linux acceptance passes:

- `protocol-hello-and-handles/local-go-linux`.
- `protocol-version-and-hard-cut/local-go-linux`.
- `protocol-go-sdk-fake-host-and-linkage/local-go-linux`, including
  `recovery-chains`. Its socket-dependent `real-process-host-routes` subtest skips.
- `protocol-mask-acknowledgement/local-go-linux/sdk-service-contract`.
  The enclosing cell still skips pending C2's acknowledged Mask route contract.

Whole-repository build, W4/acceptance vet, focused W4 tests, Go process
conformance, schema snapshots, formatting and diff whitespace pass. Targeted
race checks pass for generated arguments, collisions, nested recovery argument
namespaces and the new acceptance subtests. Nine inherited exclusions remain:
two tests reach C2's decoder stub, three Git fallback tests encounter the
harness's empty `/tmp/.git`, and four service tests require sockets. No source
skip was added. Native macOS/Windows and other placement cells were not run.

Reproduce after creating `/tmp/sw-astra-W4/cache` and `/tmp/sw-astra-W4/tmp`:

```sh
export GOWORK=off GOFLAGS=-buildvcs=false
export GOCACHE=/tmp/sw-astra-W4/cache GOTMPDIR=/tmp/sw-astra-W4/tmp TMPDIR=/tmp/sw-astra-W4/tmp
go -C /home/bombuntu/.xwing/envs/1082/sparkwing build -p 4 ./...
go -C /home/bombuntu/.xwing/envs/1082/sparkwing vet -p 4 ./internal/nodeproto/... ./internal/protofd ./internal/sdkserve ./sparkwing/... ./internal/acceptance ./tests/acceptance ./tests/acceptance/testdata/protocolgo/...
go -C /home/bombuntu/.xwing/envs/1082/sparkwing test -p 4 ./internal/nodeproto/... ./internal/protofd ./internal/sdkserve ./sparkwing/... -skip '^(TestParseFrameV2Shapes|TestReplyDecodeV2|TestFilesetHashFilesystemFallback|TestFilesetHashFramesFileContents|TestFilesetHashIncludesPermissions|TestResolveHostPortsHoldsEveryProbeUntilAllAreChosen|TestWithServicesAddrsPublishesTheAllocatedPort|TestWithServicesAddrsReportsAPinnedPortAndHostNetworking|TestWithServicesStillRunsFnWithoutAddrs)$' -count=1
go -C /home/bombuntu/.xwing/envs/1082/sparkwing test -p 4 ./tests/acceptance -run 'TestProtocol/(protocol-hello-and-handles|protocol-version-and-hard-cut|protocol-go-sdk-fake-host-and-linkage|protocol-mask-acknowledgement)/local-go-linux' -count=1 -v
go -C /home/bombuntu/.xwing/envs/1082/sparkwing test -race -p 2 ./sparkwing ./tests/acceptance -run 'TestGeneratedJobArgument|TestInvoke_RefusesJobFlagsThatShadowPipelineInputs|TestProtocol/protocol-go-sdk-fake-host-and-linkage/local-go-linux/recovery-chains' -count=1 -v
```

Contract changes needed in other areas: C2 still must specify acknowledged
Mask registration's method, route, payload, response and authorization.
Its policy for masks matching accepted identifiers remains open. No new
external contract change is requested. The lead's approval SkipIf interpretation
still needs confirmation.

Real integration remains open: W2 canonical error decoding and Mask registration,
W6 limits and process environment composition, W3 latest-output/child handlers,
W3/C1 persisted expansions and reconstruction hashes, claims, lifecycle and
artifact/reference authority. SDK subprocesses prove declaration reconstruction
and body execution, not store-backed recovery admission or persistence.
Placement drivers and W5 parity remain unverified. The lead retains independent
review, committing and integrated pipeline gates.

W4 production files:
`sparkwing/jobargs.go`, `sparkwing/pipeline.go`, `sparkwing/plan.go`,
`sparkwing/protocol_operations.go`, `sparkwing/protocol_plan.go`.
W4 tests: `sparkwing/jobargs_test.go`,
`sparkwing/protocol_generated_args_test.go`.
Shared files: `tests/acceptance/protocol_test.go`,
`tests/acceptance/protocol_recovery_chain_test.go`,
`tests/acceptance/testdata/protocolgo/fixture.go`,
`tests/acceptance/testdata/protocolgo/recovery_chain.go`,
`design/rewrite/contracts/c3-go-sdk-notes.md`, and this report.
No other area's production file changed.

Friction: sockets remain denied, `/tmp/.git` is empty, and Overwing reports a
stale binary. No Git writes or Sparkwing CLI executions occurred. Shared Git
config has no injected worktree or user identity settings. Scratch and Go caches
stayed in `/tmp/sw-astra-W4/`, which was deleted after verification.

STATUS: CONTINUE
