# W4 continuation report

Continues `a886f3d44` and `W4-session11.md`.

Closure IDs now encode their job component because a job condition on
`scope/step/release` previously collided with a step condition on job `scope`,
step `release`. Generator IDs use the same constructor. The acceptance
regression failed before the fix with `closure ID is not unique`. It now
plans and evaluates every condition and generator through the public fake
host and real SDK subprocesses. Literal `%2F` identifiers and nested step
paths remain distinct. Reconstructed plans agree between both transports.

The C3 Go notes clarify existing uniqueness requirements. No signature or
wire field changed. Opaque IDs for job names requiring URL escaping change.
No compatibility path or new runtime mechanism was added.

Deleted: 0 additional legacy lines. Replaced two ID-construction lines.
No public API removal or external production caller migration was needed.
No new T1 inventory port was required. `TestSecretIdentifiers` still passes.

Linux acceptance passes:

- `protocol-hello-and-handles/local-go-linux`.
- `protocol-version-and-hard-cut/local-go-linux`.
- `protocol-go-sdk-fake-host-and-linkage/local-go-linux`, including the new
  `closure-identifiers` subtest. `real-process-host-routes` skips because
  the sandbox denies sockets.
- `protocol-mask-acknowledgement/local-go-linux/sdk-service-contract`.
  The enclosing cell still skips pending C2's acknowledged Mask route contract.

Whole-repository build, W4/acceptance vet, focused W4 tests, Go process
conformance, schema snapshots, formatting and diff whitespace pass.
Nine exclusions remain: two tests reach C2's decoder stub, three Git fallback
tests encounter the harness's empty `/tmp/.git`, and four tests require sockets.
No new race run was selected because ID encoding adds no shared state.

Reproduce after creating `/tmp/sw-astra-W4/cache` and `/tmp/sw-astra-W4/tmp`:

```sh
export GOWORK=off GOFLAGS=-buildvcs=false
export GOCACHE=/tmp/sw-astra-W4/cache GOTMPDIR=/tmp/sw-astra-W4/tmp TMPDIR=/tmp/sw-astra-W4/tmp
go -C /home/bombuntu/.xwing/envs/1082/sparkwing build -p 4 ./...
go -C /home/bombuntu/.xwing/envs/1082/sparkwing vet -p 4 ./internal/nodeproto/... ./internal/protofd ./internal/sdkserve ./sparkwing/... ./internal/acceptance ./tests/acceptance ./tests/acceptance/testdata/protocolgo/...
go -C /home/bombuntu/.xwing/envs/1082/sparkwing test -p 4 ./internal/nodeproto/... ./internal/protofd ./internal/sdkserve ./sparkwing/... -skip '^(TestParseFrameV2Shapes|TestReplyDecodeV2|TestFilesetHashFilesystemFallback|TestFilesetHashFramesFileContents|TestFilesetHashIncludesPermissions|TestResolveHostPortsHoldsEveryProbeUntilAllAreChosen|TestWithServicesAddrsPublishesTheAllocatedPort|TestWithServicesAddrsReportsAPinnedPortAndHostNetworking|TestWithServicesStillRunsFnWithoutAddrs)$' -count=1
go -C /home/bombuntu/.xwing/envs/1082/sparkwing test -p 4 ./tests/acceptance -run 'TestProtocol/(protocol-hello-and-handles|protocol-version-and-hard-cut|protocol-go-sdk-fake-host-and-linkage|protocol-mask-acknowledgement)/local-go-linux' -count=1 -v
```

Contract changes needed: C2 must specify acknowledged Mask registration's
method, route, payload, response and authorization. Its policy for masks
matching accepted identifiers remains open. No new external contract change
is requested. The lead's approval SkipIf interpretation still needs confirmation.

Real integration remains open: W2 canonical error decoding and Mask registration,
W6 limits and process environment composition, W3 latest-output/child handlers,
W3/C1 persisted expansion and reconstruction hashes, claims, lifecycle and
artifact/reference authority. Native macOS/Windows execution, socket checks,
placement drivers and W5 parity remain unverified. W5 should include path and
literal-percent identifiers in its shared plan corpus.

W4 production files: `sparkwing/protocol_plan.go`, `sparkwing/sdk_contract.go`.
Shared files: `tests/acceptance/protocol_closure_test.go`,
`tests/acceptance/protocol_test.go`,
`tests/acceptance/testdata/protocolgo/closure.go`,
`tests/acceptance/testdata/protocolgo/fixture.go`,
`design/rewrite/contracts/c3-go-sdk-notes.md`, and this report.
No other area's production file changed. The lead retains independent review
and integrated gates.

Friction: sockets are denied, `/tmp/.git` is empty, and Overwing reports a stale
binary. No Git writes or Sparkwing CLI executions occurred. The shared Git
config has no injected worktree or user identity settings. Scratch and Go caches
stayed in `/tmp/sw-astra-W4/`, which was deleted after verification.

STATUS: CONTINUE
