# W4 continuation report

Continues `e4b196703` and `W4-session14.md`.

Generator contexts now exclude the source job's body environment from SDK
commands. Process preparation already excluded those assignments during ancestor
replay, but `executionContext` still passed them to `Exec` and `Bash`. A generator
could therefore reconstruct different declarations when it ran a command. The
fix guards the existing environment loop with the existing generator predicate.
Conditions, hooks and steps retain their own job's body environment.

The new acceptance regression failed before the fix in both the public fake host
and an SDK subprocess: a generator command saw `source` instead of `outside`.
It now passes for two nested ancestor generators, then proves that the generated
leaf's condition and body commands receive `leaf`. The subprocess is bound to
the leaf throughout reconstruction. The fixture uses a native Go command to
read its environment, so it needs neither a shell nor a socket. No new source
skip was added.

The Go API comment and C3 notes clarify the existing replay requirement. No
public signature, wire field or other area's contract changed. No production
setting, environment variable, service or runtime state was added. The test's
helper argument and environment key are fixture-only.

Deleted: 0 additional legacy-API lines. Replaced 3 production lines with the
guarded traversal. No API was removed, so no caller migration was needed. No new
T1 inventory port was needed. The existing `TestSecretIdentifiers` port passes.
The removed-public-API acceptance tests pass, and a declaration scan found no
Delete-inventory or `Old` declarations in W4 production files.

Linux acceptance passes:

- `protocol-hello-and-handles/local-go-linux`.
- `protocol-version-and-hard-cut/local-go-linux`.
- `protocol-go-sdk-fake-host-and-linkage/local-go-linux`, including the new
  `replay-environment` subtest. `real-process-host-routes` remains skipped because
  the sandbox denies loopback sockets.
- `protocol-mask-acknowledgement/local-go-linux/sdk-service-contract`.
  Its enclosing cell remains skipped pending C2's acknowledged Mask route.

Whole-repository build, W4/acceptance vet, focused W4 tests, Go subprocess
conformance, schema snapshots, formatting and diff whitespace pass. Targeted
race checks pass for the existing generated-job replay test and the new
acceptance subtest. Nine inherited exclusions remain in the broad W4 test
command: two tests reach C2's decoder stub, three Git fallback tests encounter
the harness's empty `/tmp/.git`, and four service tests require sockets. Running
`TestParseFrameV2Shapes` without that exclusion still panics in
`apierr.DecodeInner`. This is a confirmed failed integration check, not a pass.

Reproduce after creating `/tmp/sw-astra-W4/cache` and `/tmp/sw-astra-W4/tmp`:

```sh
export GOWORK=off GOFLAGS=-buildvcs=false
export GOCACHE=/tmp/sw-astra-W4/cache GOTMPDIR=/tmp/sw-astra-W4/tmp TMPDIR=/tmp/sw-astra-W4/tmp
go -C /home/bombuntu/.xwing/envs/1082/sparkwing build -p 4 ./...
go -C /home/bombuntu/.xwing/envs/1082/sparkwing vet -p 4 ./internal/nodeproto/... ./internal/protofd ./internal/sdkserve ./sparkwing/... ./internal/acceptance ./tests/acceptance ./tests/acceptance/testdata/protocolgo/...
go -C /home/bombuntu/.xwing/envs/1082/sparkwing test -p 4 ./internal/nodeproto/... ./internal/protofd ./internal/sdkserve ./sparkwing/... -skip '^(TestParseFrameV2Shapes|TestReplyDecodeV2|TestFilesetHashFilesystemFallback|TestFilesetHashFramesFileContents|TestFilesetHashIncludesPermissions|TestResolveHostPortsHoldsEveryProbeUntilAllAreChosen|TestWithServicesAddrsPublishesTheAllocatedPort|TestWithServicesAddrsReportsAPinnedPortAndHostNetworking|TestWithServicesStillRunsFnWithoutAddrs)$' -count=1
go -C /home/bombuntu/.xwing/envs/1082/sparkwing test -p 4 ./tests/acceptance -run 'TestProtocol/(protocol-hello-and-handles|protocol-version-and-hard-cut|protocol-go-sdk-fake-host-and-linkage|protocol-mask-acknowledgement)/local-go-linux' -count=1 -v
go -C /home/bombuntu/.xwing/envs/1082/sparkwing test -race -p 2 ./sparkwing ./tests/acceptance -run 'TestBoundGeneratedJobReplaysThroughProtocol|TestProtocol/protocol-go-sdk-fake-host-and-linkage/local-go-linux/replay-environment' -count=1
```

Contract changes needed in other areas: C2 still must specify acknowledged Mask
registration's method, route, payload, response and authorization. The policy
for masks matching accepted identifiers remains open. No new external contract
change is requested. Approval SkipIf versus removed before/after hooks remains
the inherited question for the lead.

Real integration remains open: W2 canonical error decoding and Mask registration,
W6 limits and process environment composition, W3 latest-output/child handlers,
W3/C1 persisted expansions and reconstruction hashes, claims, lifecycle and
artifact/reference authority. Fake-host and subprocess evidence proves SDK
reconstruction and execution without supplying store authority or persistence.
Other placement drivers, W5 parity and native macOS/Windows execution remain
unverified. The lead retains integrated pipelines, review and committing.

W4 production files: `sparkwing/protocol_operations.go` and
`sparkwing/sdk_contract.go`.
Shared files: `tests/acceptance/protocol_test.go`,
`tests/acceptance/protocol_replay_env_test.go`,
`tests/acceptance/testdata/protocolgo/fixture.go`,
`tests/acceptance/testdata/protocolgo/replay_env.go`,
`tests/acceptance/testdata/protocolgo/cmd/main.go`,
`design/rewrite/contracts/c3-go-sdk-notes.md`, and this report.
No other area's production file changed.

Friction: denied sockets, the empty `/tmp/.git`, and stale Overwing persist.
No Git writes or Sparkwing CLI executions occurred. Shared Git config contains
no injected worktree or user identity settings. Scratch and Go caches stayed in
`/tmp/sw-astra-W4/`, which was deleted after verification.

STATUS: CONTINUE
