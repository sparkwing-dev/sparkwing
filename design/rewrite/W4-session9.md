# W4 continuation report

Continues `aa3dd8f9a` and `W4-session8.md`.

Fixed two remaining reply-boundary defects against the existing C3 contract:

- A step whose output cannot be JSON-encoded now returns an OK protocol reply
  with a failed `RunResult` and canonical `sdk_error`. Its body already ran, so
  reporting an invocation failure was wrong. A NaN fixture proves the result
  through both `sparkwingtest.Host` and a real SDK subprocess.
- `Reply.Decode` clears a reused destination before decoding. Missing hello
  fields cannot inherit a prior reply's SDK version or features. A successful
  body result cannot inherit a prior failure. Both acceptance regressions
  failed before the fix and pass afterward.

The C3 protocol notes clarify these semantics. Wire keys, signatures, error
codes and generated schemas are unchanged. No new mechanism, limit, environment
variable or compatibility surface was added.

Deleted: 0 additional legacy lines. Two faulty production lines were replaced.
The earlier W4 hard cuts remain in place. No production callers outside W4
needed migration. No new T1 inventory port was required. The existing
`TestSecretIdentifiers` port passes.

Acceptance on Linux:

- `protocol-hello-and-handles/local-go-linux` passes.
- `protocol-version-and-hard-cut/local-go-linux` passes.
- `protocol-go-sdk-fake-host-and-linkage/local-go-linux` passes, including
  `reply-result-reuse/{hello-required-fields,success-after-failure}` and
  `real-process-output-encoding-failure`. Its `real-process-host-routes`
  subtest skips because the sandbox prohibits sockets.
- `protocol-mask-acknowledgement/local-go-linux/sdk-service-contract` passes.
  Its enclosing cell remains skipped pending C2's Mask route contract.
  No additional placement cell was activated.

Verification passed: whole-repository build, W4 and acceptance vet, focused
W4 package tests, Go process conformance, schema snapshots and diff whitespace.
Race checks passed over the core W4 packages and selected Linux acceptance cells.

Reproduce after creating `/tmp/sw-astra-W4/cache` and `/tmp/sw-astra-W4/tmp`:

```sh
export GOWORK=off GOFLAGS=-buildvcs=false
export GOCACHE=/tmp/sw-astra-W4/cache GOTMPDIR=/tmp/sw-astra-W4/tmp TMPDIR=/tmp/sw-astra-W4/tmp
go -C /home/bombuntu/.xwing/envs/1082/sparkwing build -p 4 ./...
go -C /home/bombuntu/.xwing/envs/1082/sparkwing vet -p 4 ./internal/nodeproto/... ./internal/protofd ./internal/sdkserve ./sparkwing/... ./internal/acceptance ./tests/acceptance ./tests/acceptance/testdata/protocolgo/...
go -C /home/bombuntu/.xwing/envs/1082/sparkwing test -p 4 ./internal/nodeproto/... ./internal/protofd ./internal/sdkserve ./sparkwing/... -skip '^(TestParseFrameV2Shapes|TestReplyDecodeV2|TestFilesetHashFilesystemFallback|TestFilesetHashFramesFileContents|TestFilesetHashIncludesPermissions|TestResolveHostPortsHoldsEveryProbeUntilAllAreChosen|TestWithServicesAddrsPublishesTheAllocatedPort|TestWithServicesAddrsReportsAPinnedPortAndHostNetworking|TestWithServicesStillRunsFnWithoutAddrs)$' -count=1
go -C /home/bombuntu/.xwing/envs/1082/sparkwing test -race -p 2 ./internal/nodeproto ./internal/sdkserve ./sparkwing ./sparkwing/sparkwingtest -skip '^(TestParseFrameV2Shapes|TestReplyDecodeV2)$' -count=1
go -C /home/bombuntu/.xwing/envs/1082/sparkwing test -race -p 2 ./tests/acceptance -run 'TestProtocol/(protocol-hello-and-handles|protocol-version-and-hard-cut|protocol-go-sdk-fake-host-and-linkage|protocol-mask-acknowledgement)/local-go-linux' -count=1
```

The nine focused-test exclusions are unchanged: two tests reach W2's
unimplemented canonical decoder, three Git filesystem-fallback tests encounter
the harness's empty `/tmp/.git`, and four service tests require sockets.
No source skip or production workaround was added. An initial process-only
regression hit C2's decoder stub while reading the wrong invocation-error
envelope. Running the fixture assertion first then demonstrated W4's defect
directly. The corrected body-result envelope passes the process fixture.

Contract changes needed in other areas: C2 still must specify acknowledged Mask
registration's method, route, payload, response and authorization. Its policy
for masks matching accepted identifiers remains open. No new cross-area
contract change is requested.

Real integration remains open:

- W2's canonical decoder for reply, recovery, body, eval and HTTP errors.
  `routeServices.Mask` still returns `unavailable` because C2 has no registration
  contract. The SDK Mask acknowledgement behavior is covered with fake services.
- W6's `limits.Record`, resolved ceilings and process environment composition.
- W3's latest-output and child-await handlers with canonical child errors.
  W4 transport behavior remains tested against fake routes.
- W3/C1 persisted expansion and hash checks, claim identity, replay, lifecycle,
  artifact/reference authorization and children through real placements.
- Socket-enabled checks, native Windows/macOS execution, remaining placement
  drivers and W5 TypeScript parity. The lead's approval SkipIf interpretation
  still needs confirmation in the contract notes.

W4 production files: `internal/nodeproto/protocol_contract.go` and
`sparkwing/protocol_operations.go`.
Shared files: `tests/acceptance/protocol_output_test.go`,
`tests/acceptance/protocol_test.go`,
`tests/acceptance/testdata/protocolgo/fixture.go`,
`design/rewrite/contracts/c3-protocol-notes.md`, and this report.
No other area's production file changed. The lead retains independent review
and integrated pipeline gates. No Git writes occurred. The shared repository
config has no injected `core.worktree`, `user.name` or `user.email` entries.

Friction: sockets are prohibited and the harness exposes an empty `/tmp/.git`.
Overwing reports its installed binary behind source. The read-only bootstrap
`sparkwing info --for-agent` ran before the swarm brief's no-binary rule was
read. No subsequent Sparkwing binary, candidate, service or pipeline ran.
All scratch files and Go caches stayed in `/tmp/sw-astra-W4/`, which was
deleted after verification.

STATUS: CONTINUE
