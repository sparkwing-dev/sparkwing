# W4 continuation report

Continues `802ebd088` and `W4-session15.md`.

Removed the reply encoder's extra 1,024-byte payload reserve. Replies now use
the same full-frame check as logs, which counts the envelope, escaped
correlation ID and newline. Valid full-size replies reach the reader unchanged.
Oversized replies fail the serving session before any oversized bytes are
written. The separate typed-output limit remains enforced by SDK operations
and protocol decoding.

The new acceptance regression failed before the fix: a full-size describe
reply became a 109-byte error reply. It now passes at the exact frame limit
and refuses one byte over it, for short and escaped correlation IDs. The
existing log-frame boundary cases also pass. The C3 note clarifies the existing
bound. No signature, wire shape, production variable, limit or mechanism was
added.

Deleted four production lines: the duplicate threshold and its unused import.
Deleted zero additional legacy-API lines. The prior API cuts remain in place.
No API removal required caller migration. No new inventory test port was
needed. The existing `TestSecretIdentifiers` port and removed-API probes pass.
The W4 declaration scan found no remaining `Old` or unimplemented declarations.

Verification passed with `GOWORK=off`, `GOFLAGS=-buildvcs=false`,
`GOCACHE=/tmp/sw-astra-W4/cache`, and both `GOTMPDIR` and `TMPDIR` set to
`/tmp/sw-astra-W4/tmp`:

```sh
go -C /home/bombuntu/.xwing/envs/1082/sparkwing build -p 4 ./...
go -C /home/bombuntu/.xwing/envs/1082/sparkwing vet -p 4 ./internal/nodeproto/... ./internal/protofd ./internal/sdkserve ./sparkwing/... ./internal/acceptance ./tests/acceptance ./tests/acceptance/testdata/protocolgo/...
go -C /home/bombuntu/.xwing/envs/1082/sparkwing test -p 4 ./internal/nodeproto/... ./internal/protofd ./internal/sdkserve ./sparkwing/... -skip '^(TestParseFrameV2Shapes|TestReplyDecodeV2|TestFilesetHashFilesystemFallback|TestFilesetHashFramesFileContents|TestFilesetHashIncludesPermissions|TestResolveHostPortsHoldsEveryProbeUntilAllAreChosen|TestWithServicesAddrsPublishesTheAllocatedPort|TestWithServicesAddrsReportsAPinnedPortAndHostNetworking|TestWithServicesStillRunsFnWithoutAddrs)$' -count=1
go -C /home/bombuntu/.xwing/envs/1082/sparkwing test -p 4 ./tests/acceptance -run 'TestProtocol/(protocol-hello-and-handles|protocol-version-and-hard-cut|protocol-go-sdk-fake-host-and-linkage|protocol-mask-acknowledgement)/local-go-linux' -count=1
```

The hello, hard-cut and fake-host/linkage Linux cells pass, including the new
`sdk-reply-frame-boundaries` subtest. The socket-dependent real-host route
subtest remains excluded by its existing sandbox check. Mask's SDK service
subtest passes, while its enclosing cell remains skipped for the missing C2
route. Nine inherited focused-test exclusions remain: two depend on C2's
stubbed decoder, three encounter the harness's empty `/tmp/.git`, and four
require sockets. Go subprocess conformance, schema snapshots, formatting and
diff whitespace checks pass. Integrated pipelines and native-platform runtime
checks remain with the lead.

Contract changes needed in other areas: C2 must define acknowledged Mask
registration's method, route, payload, response and authorization. Its policy
for masks matching accepted identifiers remains open. Approval SkipIf versus
removed executable hooks remains the inherited lead question. No new
cross-area contract change is requested.

Real integration remains open for W2 error decoding and Mask registration,
W6 limits/environment composition, W3 latest-output and child handlers,
W3/C1 persisted expansions and reconstruction hashes, claims, lifecycle and
artifact/reference authority. Other placements, W5 parity and native Windows
and macOS execution still need integrated evidence. These remain integration
requirements, not completed acceptance cells.

W4 production file: `internal/sdkserve/serve.go`. Shared files:
`tests/acceptance/protocol_frame_test.go`, `tests/acceptance/protocol_test.go`,
`design/rewrite/contracts/c3-protocol-notes.md`, and this report. No other
area's production file changed.

Friction remains the denied sockets, empty `/tmp/.git` and stale Overwing.
No Git writes or Sparkwing CLI executions occurred. Shared Git config has no
injected worktree or user identity settings. All commands finished, and
`/tmp/sw-astra-W4/` was deleted after verification.

STATUS: CONTINUE
