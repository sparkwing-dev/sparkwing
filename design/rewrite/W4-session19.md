# W4 continuation report

Continues `39480811e` and `W4-session18.md`.

SDK planning errors now redact longer secret inputs before their prefixes.
Previously, declaring `plan-secret` before `plan-secret-tail` exposed `-tail`
in a returned planning error. The existing registration collector now sorts
its values by length. No new interface, setting, environment variable or
service was added.

Four acceptance cases compare the public fake host with a separate SDK
process: defaults, supplied prefix first, supplied prefix second and distinct
values. The first two failed before the fix in both transports. All four
pass after the fix. They run under the existing fake-host/linkage cell.

Deleted zero lines in this continuation. Earlier legacy API removals remain
intact. The W4 production scan finds no Old-suffixed declarations or
unimplemented markers. No additional inventory test needed porting. The
existing secret-identifier port, conformance/schema snapshots and removed-API
compile probes pass.

Verification uses these environment values:

```sh
export GOWORK=off GOFLAGS=-buildvcs=false
export GOCACHE=/tmp/sw-astra-W4/cache
export GOTMPDIR=/tmp/sw-astra-W4/tmp TMPDIR=/tmp/sw-astra-W4/tmp
mkdir -p "$GOCACHE" "$GOTMPDIR"
```

Final checks:

```sh
go -C /home/bombuntu/.xwing/envs/1082/sparkwing build -p 4 ./...
go -C /home/bombuntu/.xwing/envs/1082/sparkwing vet -p 4 ./internal/nodeproto/... ./internal/protofd ./internal/sdkserve ./sparkwing/... ./internal/acceptance ./tests/acceptance ./tests/acceptance/testdata/protocolgo/...
go -C /home/bombuntu/.xwing/envs/1082/sparkwing test -p 4 ./internal/nodeproto/... ./internal/protofd ./internal/sdkserve ./sparkwing/... -skip '^(TestParseFrameV2Shapes|TestReplyDecodeV2|TestFilesetHashFilesystemFallback|TestFilesetHashFramesFileContents|TestFilesetHashIncludesPermissions|TestResolveHostPortsHoldsEveryProbeUntilAllAreChosen|TestWithServicesAddrsPublishesTheAllocatedPort|TestWithServicesAddrsReportsAPinnedPortAndHostNetworking|TestWithServicesStillRunsFnWithoutAddrs)$' -count=1
go -C /home/bombuntu/.xwing/envs/1082/sparkwing test -p 4 ./tests/acceptance -run '^TestProtocol/(protocol-hello-and-handles|protocol-version-and-hard-cut|protocol-go-sdk-fake-host-and-linkage|protocol-mask-acknowledgement)/local-go-linux' -count=1 -v
go -C /home/bombuntu/.xwing/envs/1082/sparkwing test -race -p 4 ./sparkwing/sparkwingtest -count=1
```

The whole-repo build, vet, package tests, selected acceptance and race checks
passed. Formatting and diff whitespace checks pass.

The Linux hello, version/hard-cut and fake-host/linkage cells pass. The latter
still skips its real-host HTTP-route subtest because sockets are denied.
Mask's SDK service-contract subtest passes, while its full cell remains
skipped because C2 has no acknowledged registration route contract. No
additional complete cell was unskipped in this continuation.

The nine package-test exclusions are inherited. Two depend on C2's stubbed
`apierr.DecodeInner`. Three fileset tests reproduced their sandbox failure:
an empty ancestor `/tmp/.git` is detected as a repository, then `git ls-files`
fails. Setting `GIT_CEILING_DIRECTORIES` does not fix the filesystem probe.
The other four require sockets and were not retried.

Contract changes needed in other areas remain C2's Mask method, route,
payload, response and authorization. No other contract shape changed.
Real integration remains open for W2 error decoding and mask registration,
W6 environment and limit composition, W3 latest-output/child handlers and
W3/C1 persisted expansion/reconstruction hashes, claims, lifecycle and
artifact authority. Existing fake-service tests cover the declared
interfaces. They do not prove those owners' implementations. W5 parity,
other placements and native Windows/macOS execution remain open.

Open questions remain the lead's approval SkipIf reading versus removed
executable hooks, and newly registered masks matching accepted identifiers.

The only production file changed is `sparkwing/pipeline.go`. Shared files:

- `tests/acceptance/protocol_test.go`
- `tests/acceptance/protocol_plan_error_test.go`
- `tests/acceptance/testdata/protocolgo/fixture.go`
- `tests/acceptance/testdata/protocolgo/plan_error.go`
- `design/rewrite/contracts/c3-go-sdk-notes.md`
- This report.

No other area's production caller needed migration. Friction remains denied
sockets, empty `/tmp/.git` and Overwing's stale-binary warning. One read-only
bootstrap `sparkwing info --for-agent` invocation ran before the swarm brief
was read. No further Sparkwing command or Git write occurred. Shared Git
config contains no injected user identity or worktree setting. All command
sessions finished, and `/tmp/sw-astra-W4/` was deleted after verification.

STATUS: CONTINUE
