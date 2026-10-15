# W4 continuation report

Continues `b3376c191` and `W4-session16.md`.

Protocol decoding now rejects absent required fields at every typed nesting
level. Describe replies can no longer omit pipeline, argument, secret,
environment, example or step-risk metadata required by the existing wire
structs and schemas. Request decoding applies the same rule to Git and trigger
snapshots. Optional fields and additive fields remain readable. Explicit false,
zero and empty collections satisfy presence.

The acceptance regression failed in all ten missing-describe-field cases before
the fix and passes afterward. A real Go SDK process refuses four incomplete
run snapshots before invoking planning, then accepts a complete snapshot in the
same session. That control proves malformed requests do not consume planning.
The C3 notes clarify the existing required-field rule. No signatures, schemas,
wire fields, limits, environment variables or mechanisms were added.

Deleted six production lines containing redundant result-presence checks now
covered by the shared decoder. Deleted zero additional legacy-API lines.
Earlier API cuts remain intact. W4 has no remaining Old-suffixed declarations
or unimplemented panics. No API removal required caller migration. No new
inventory test port was needed. The existing `TestSecretIdentifiers` port and
removed-API probes pass.

All final commands below passed. Use these environment values for each command:

```sh
export GOWORK=off GOFLAGS=-buildvcs=false
export GOCACHE=/tmp/sw-astra-W4/cache
export GOTMPDIR=/tmp/sw-astra-W4/tmp TMPDIR=/tmp/sw-astra-W4/tmp
mkdir -p "$GOCACHE" "$GOTMPDIR"
go -C /home/bombuntu/.xwing/envs/1082/sparkwing build -p 4 ./...
go -C /home/bombuntu/.xwing/envs/1082/sparkwing vet -p 4 ./internal/nodeproto/... ./internal/protofd ./internal/sdkserve ./sparkwing/... ./internal/acceptance ./tests/acceptance ./tests/acceptance/testdata/protocolgo/...
go -C /home/bombuntu/.xwing/envs/1082/sparkwing test -p 4 ./internal/nodeproto/... ./internal/protofd ./internal/sdkserve ./sparkwing/... -skip '^(TestParseFrameV2Shapes|TestReplyDecodeV2|TestFilesetHashFilesystemFallback|TestFilesetHashFramesFileContents|TestFilesetHashIncludesPermissions|TestResolveHostPortsHoldsEveryProbeUntilAllAreChosen|TestWithServicesAddrsPublishesTheAllocatedPort|TestWithServicesAddrsReportsAPinnedPortAndHostNetworking|TestWithServicesStillRunsFnWithoutAddrs)$' -count=1
go -C /home/bombuntu/.xwing/envs/1082/sparkwing test -p 4 ./tests/acceptance -run '^TestProtocol/(protocol-hello-and-handles|protocol-version-and-hard-cut|protocol-go-sdk-fake-host-and-linkage|protocol-mask-acknowledgement)/local-go-linux' -count=1 -v
```

The three native Linux cells for hello, hard cuts and fake-host/linkage pass,
including `required-run-snapshot-fields` and `required-describe-fields`.
The real-host route subtest remains skipped because sockets are denied. Mask's
SDK service subtest passes, while its enclosing cell remains skipped for the
missing C2 acknowledged registration route. The complete W4 test attempt
confirmed the nine inherited exclusions: two encounter C2's `DecodeInner`
stub, three encounter the harness's empty `/tmp/.git`, and four need sockets.
Go subprocess conformance and schema snapshots pass. Baseline race checks for
sdkserve, sparkwingtest and the main SDK package also passed before the fix.
Formatting and diff whitespace checks pass.

Contract changes needed in other areas remain unchanged: C2 must define Mask's
method, route, payload, response and authorization. Its handling of masks
matching accepted identifiers remains open. The inherited lead question is
approval SkipIf versus removed executable hooks. No new cross-area contract
change is requested.

Real integration remains open for W2 error decoding and Mask registration,
W6 environment/limits composition, W3 latest-output and child handlers, and
W3/C1 persisted expansion/reconstruction hashes, claims, lifecycle and artifact
authority. The existing fake services exercise the declared boundaries without
claiming those implementations work. W5 parity, other placements and native
Windows/macOS runtime evidence remain open. Full acceptance completion still
depends on those integrations.

W4 production files are `internal/nodeproto/protocol_decode.go` and
`internal/nodeproto/protocol_contract.go`. Shared files are
`tests/acceptance/protocol_output_test.go`,
`tests/acceptance/protocol_process_test.go`,
`tests/acceptance/protocol_test.go`,
`design/rewrite/contracts/c3-protocol-notes.md` and this report.
No other area's production file changed.

Friction remains denied sockets, empty `/tmp/.git` and stale Overwing.
No Git writes or Sparkwing CLI executions occurred. Shared Git config has no
injected worktree or user identity settings. All command sessions finished.
`/tmp/sw-astra-W4/` was deleted after verification.

STATUS: CONTINUE
