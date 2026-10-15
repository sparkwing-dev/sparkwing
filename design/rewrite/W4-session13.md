# W4 continuation report

Continues `06fc8bec2` and `W4-session12.md`.

Recovery jobs now register their `WithArgs` fields before input validation
and binding. Their flags previously disappeared from describe, explicit
values failed as unknown flags, and missing required values passed planning.
Ordinary job argument defaults also survive describe serialization.

The new `job-arguments` acceptance regression failed before these fixes.
It passes through fake-host planning and real SDK subprocesses, covering
describe metadata, defaults, overrides, required fields and enums. A recovery
step returns its resolved typed arguments. The existing pipeline-flag collision
test now checks recovery jobs too, for both names and short aliases.

C3 Go notes clarify the retained `WithArgs` contract. No public signature,
wire field, external contract or runtime mechanism was added.
Deleted: 0 additional legacy lines. No API was removed and no caller outside
W4 needed migration. No new T1 inventory port was required.
The existing `TestSecretIdentifiers` port passes.

Linux acceptance passes:

- `protocol-hello-and-handles/local-go-linux`.
- `protocol-version-and-hard-cut/local-go-linux`.
- `protocol-go-sdk-fake-host-and-linkage/local-go-linux`, including
  `job-arguments`. Its socket-dependent `real-process-host-routes` subtest skips.
- `protocol-mask-acknowledgement/local-go-linux/sdk-service-contract`.
  Its enclosing cell still skips pending C2's acknowledged Mask route contract.

Whole-repository build, W4/acceptance vet, focused W4 tests, Go process
conformance, schema snapshots, formatting and diff whitespace pass.
Race checks pass for `sparkwing`, `sparkwingtest`, the new acceptance subtest
and the extended collision test. Nine inherited exclusions remain: two tests
reach C2's decoder stub, three Git fallback tests encounter the harness's empty
`/tmp/.git`, and four service tests require sockets. No source skip was added.

Reproduce after creating `/tmp/sw-astra-W4/cache` and `/tmp/sw-astra-W4/tmp`:

```sh
export GOWORK=off GOFLAGS=-buildvcs=false
export GOCACHE=/tmp/sw-astra-W4/cache GOTMPDIR=/tmp/sw-astra-W4/tmp TMPDIR=/tmp/sw-astra-W4/tmp
go -C /home/bombuntu/.xwing/envs/1082/sparkwing build -p 4 ./...
go -C /home/bombuntu/.xwing/envs/1082/sparkwing vet -p 4 ./internal/nodeproto/... ./internal/protofd ./internal/sdkserve ./sparkwing/... ./internal/acceptance ./tests/acceptance ./tests/acceptance/testdata/protocolgo/...
go -C /home/bombuntu/.xwing/envs/1082/sparkwing test -p 4 ./internal/nodeproto/... ./internal/protofd ./internal/sdkserve ./sparkwing/... -skip '^(TestParseFrameV2Shapes|TestReplyDecodeV2|TestFilesetHashFilesystemFallback|TestFilesetHashFramesFileContents|TestFilesetHashIncludesPermissions|TestResolveHostPortsHoldsEveryProbeUntilAllAreChosen|TestWithServicesAddrsPublishesTheAllocatedPort|TestWithServicesAddrsReportsAPinnedPortAndHostNetworking|TestWithServicesStillRunsFnWithoutAddrs)$' -count=1
go -C /home/bombuntu/.xwing/envs/1082/sparkwing test -p 4 ./tests/acceptance -run 'TestProtocol/(protocol-hello-and-handles|protocol-version-and-hard-cut|protocol-go-sdk-fake-host-and-linkage|protocol-mask-acknowledgement)/local-go-linux' -count=1 -v
go -C /home/bombuntu/.xwing/envs/1082/sparkwing test -race -p 2 ./sparkwing ./sparkwing/sparkwingtest -count=1
go -C /home/bombuntu/.xwing/envs/1082/sparkwing test -race -p 2 ./tests/acceptance -run 'TestProtocol/protocol-go-sdk-fake-host-and-linkage/local-go-linux/job-arguments' -count=1
go -C /home/bombuntu/.xwing/envs/1082/sparkwing test -race -p 2 ./sparkwing -run TestInvoke_RefusesJobFlagsThatShadowPipelineInputs -count=1
```

Contract changes needed in other areas: C2 still must specify acknowledged
Mask registration's method, route, payload, response and authorization.
Its policy for masks matching accepted identifiers remains open.
No new external contract change is requested. The lead's approval SkipIf
interpretation still needs confirmation.

Real integration remains open: W2 canonical error decoding and Mask registration,
W6 limits and process environment composition, W3 latest-output/child handlers,
W3/C1 persisted expansions and reconstruction hashes, claims, lifecycle and
artifact/reference authority. Native macOS/Windows execution, socket checks,
placement drivers and W5 parity remain unverified. SDK subprocess tests do
not prove store-backed recovery admission or persistence.

W4 production files: `sparkwing/pipeline.go`, `sparkwing/protocol_describe.go`.
W4 unit test: `sparkwing/jobargs_test.go`.
Shared files: `tests/acceptance/protocol_arguments_test.go`,
`tests/acceptance/protocol_test.go`,
`tests/acceptance/testdata/protocolgo/arguments.go`,
`tests/acceptance/testdata/protocolgo/fixture.go`,
`design/rewrite/contracts/c3-go-sdk-notes.md`, and this report.
No other area's production file changed. The lead retains independent review,
committing and integrated pipeline gates.

Friction: sockets are denied, `/tmp/.git` is empty, and Overwing reports a stale
binary. No Git writes or Sparkwing CLI executions occurred. The shared Git
config has no injected worktree or user identity settings. Scratch and Go caches
stayed in `/tmp/sw-astra-W4/`, which was deleted after verification.

STATUS: CONTINUE
