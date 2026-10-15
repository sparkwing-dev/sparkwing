# W4 continuation report

Continues `76d4b4448` and `W4-session17.md`.

The fake host now masks attribute keys and nested strings, numbers, booleans
and null values. Numeric decoding and record snapshots preserve exact JSON
numbers. Longer registered masks precede their prefixes, so registering
`prefix` before `prefix-tail` cannot leave `-tail` visible. The existing
masking helper owns these changes. No package, interface, route, environment
variable, limit or service was added.

The acceptance regression has eight cases. Seven fail against the committed
implementation through a temporary Go overlay, including the large integer
`9007199254740993` being rounded to `9007199254740992`. All eight pass against
the working tree. The escaped-string case remains a passing control.

Deleted 15 production lines from the replaced fake-host masking and snapshot
implementation. Deleted zero additional legacy-API lines. Earlier hard cuts
remain intact. The W4 production scan finds no Old-suffixed declarations or
unimplemented markers. No new inventory port was required. The existing
`TestSecretIdentifiers` port and removed-API compile probes pass.

Local contract clarification: `Host.Records` returns attribute numbers as
`json.Number`, preserving their exact values. The interface documentation and
C3 Go SDK notes record this. Two numeric annotation assertions were migrated
in `sparkwing/sparkwingtest/host_test.go` and
`tests/acceptance/protocol_mask_test.go`. No caller in another production
package needed migration.

All final commands below passed with these environment values:

```sh
export GOWORK=off GOFLAGS=-buildvcs=false
export GOCACHE=/tmp/sw-astra-W4/cache
export GOTMPDIR=/tmp/sw-astra-W4/tmp TMPDIR=/tmp/sw-astra-W4/tmp
mkdir -p "$GOCACHE" "$GOTMPDIR"
go -C /home/bombuntu/.xwing/envs/1082/sparkwing build -p 4 ./...
go -C /home/bombuntu/.xwing/envs/1082/sparkwing vet -p 4 ./internal/nodeproto/... ./internal/protofd ./internal/sdkserve ./sparkwing/... ./internal/acceptance ./tests/acceptance ./tests/acceptance/testdata/protocolgo/...
go -C /home/bombuntu/.xwing/envs/1082/sparkwing test -p 4 ./internal/nodeproto/... ./internal/protofd ./internal/sdkserve ./sparkwing/... -skip '^(TestParseFrameV2Shapes|TestReplyDecodeV2|TestFilesetHashFilesystemFallback|TestFilesetHashFramesFileContents|TestFilesetHashIncludesPermissions|TestResolveHostPortsHoldsEveryProbeUntilAllAreChosen|TestWithServicesAddrsPublishesTheAllocatedPort|TestWithServicesAddrsReportsAPinnedPortAndHostNetworking|TestWithServicesStillRunsFnWithoutAddrs)$' -count=1
go -C /home/bombuntu/.xwing/envs/1082/sparkwing test -p 4 ./tests/acceptance -run '^TestProtocol/(protocol-hello-and-handles|protocol-version-and-hard-cut|protocol-go-sdk-fake-host-and-linkage|protocol-mask-acknowledgement)/local-go-linux' -count=1 -v
go -C /home/bombuntu/.xwing/envs/1082/sparkwing test -race -p 4 ./sparkwing/sparkwingtest -count=1
```

The three Linux cells for hello, hard cuts and fake-host/linkage pass.
`attribute-masking` now runs within the fake-host cell. Its real-host route
subtest remains skipped because the sandbox denies sockets. Mask's SDK
service-contract subtest passes, while its full cell remains skipped because
C2 has no acknowledged registration route. The nine package-test exclusions
are inherited: two require C2's `DecodeInner` implementation, three encounter
the sandbox's empty `/tmp/.git`, and four require sockets. Those exclusions
were not rerun in this session. Conformance and schema snapshots pass.
Formatting and diff whitespace checks pass.

Contract changes needed in other areas are unchanged: C2 must define Mask's
method, route, payload, response and authorization. The policy for a newly
registered mask matching an accepted identifier remains open. The inherited
lead question is approval SkipIf versus removed executable hooks. No new
cross-area contract change is requested.

Real integration remains open for W2 error decoding and Mask registration,
W6 environment/limits composition, W3 latest-output and child handlers, and
W3/C1 persisted expansion/reconstruction hashes, claims, lifecycle and artifact
authority. Fake services cover the declared boundaries without proving those
implementations. W5 parity, other placements and native Windows/macOS runtime
evidence remain open. Full acceptance completion depends on those integrations.

W4 production files changed are `sparkwing/sparkwingtest/host.go` and
`sparkwing/sparkwingtest/host_contract.go`. Shared files changed are
`tests/acceptance/protocol_mask_test.go`, `tests/acceptance/protocol_test.go`,
`design/rewrite/contracts/c3-go-sdk-notes.md` and this report. No other area's
production file changed.

Friction remains denied sockets, empty `/tmp/.git` and Overwing's stale-binary
warning. No Git writes or Sparkwing CLI executions occurred. Shared Git config
contains no injected user identity or worktree settings. Command sessions
finished, and `/tmp/sw-astra-W4/` was deleted after verification.

STATUS: CONTINUE
