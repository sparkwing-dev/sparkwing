# W4 continuation report

Continues `dc5e46cb6` and `W4-session21.md`.

`sparkwing.Git.FilesetHash` now preserves literal tracked and untracked
filenames. Git's quoted output and line trimming previously omitted Unicode,
tabs, newlines, quotes, backslashes and leading or trailing spaces. Changing
those files could return the same fingerprint. One NUL-delimited Git call
replaces the two line-oriented calls. Source API documentation names the
path behavior. No public API, contract, setting or environment variable changed.

The public SDK acceptance regression covers nine filename forms, each tracked
and untracked. Fourteen of eighteen cases failed before the fix. All eighteen
pass afterward. Plain names and interior spaces are the passing controls.
Windows omits filename forms its filesystem forbids. Native Windows and macOS
execution remains unverified.

Deleted zero additional legacy APIs or files. Eight existing production-file
lines were replaced, including two comment lines. Earlier hard cuts remain
intact. The W4 production scan finds no Old-suffixed scaffolding or
unimplemented markers. No additional inventory test needed porting.
No production caller in another area needed migration.

Verification used `GOWORK=off`, `GOFLAGS=-buildvcs=false`,
`GOCACHE=/tmp/sw-astra-W4/cache`, and both `GOTMPDIR` and `TMPDIR` set to
`/tmp/sw-astra-W4/tmp`. The following commands passed using `go -C` with this
worktree's absolute path:

```sh
go build -p 4 ./...
go vet -p 4 ./internal/nodeproto/... ./internal/protofd ./internal/sdkserve ./sparkwing/... ./internal/acceptance ./tests/acceptance ./tests/acceptance/testdata/protocolgo/...
go test -p 4 ./internal/nodeproto/... ./internal/protofd ./internal/sdkserve ./sparkwing/... -skip '^(TestParseFrameV2Shapes|TestReplyDecodeV2|TestFilesetHashFilesystemFallback|TestFilesetHashFramesFileContents|TestFilesetHashIncludesPermissions|TestResolveHostPortsHoldsEveryProbeUntilAllAreChosen|TestWithServicesAddrsPublishesTheAllocatedPort|TestWithServicesAddrsReportsAPinnedPortAndHostNetworking|TestWithServicesStillRunsFnWithoutAddrs)$' -count=1
go test -p 4 ./tests/acceptance -run '^TestProtocol/(protocol-hello-and-handles|protocol-version-and-hard-cut|protocol-go-sdk-fake-host-and-linkage|protocol-mask-acknowledgement)/local-go-linux' -count=1 -v
```

The Linux hello, version/hard-cut and fake-host/linkage target cells pass.
The last includes the new `fileset-path-names` cases. Its HTTP subprocess
subtest remains skipped because sockets are denied. Mask's SDK service-contract
subtest passes. Its full target remains skipped for C2's missing acknowledged
registration route. No additional complete target was unskipped.

The nine inherited package-test exclusions remain. Two require C2's
unimplemented `apierr.DecodeInner`, three encounter the sandbox's empty
ancestor `/tmp/.git`, and four require sockets. Those exclusions were not
retried. Conformance, schema snapshots and the existing secret-identifier
port pass within the package selection. The lead retains pipelines and review.

Contract changes needed remain C2's Mask method, route, request, response and
authorization. `routeServices.Mask` still returns unavailable at that undeclared
boundary. The existing fake-service tests cover the declared SDK interface.
Real integration remains open for W2 error decoding/mask registration, W6
environment and limits, W3 latest-output/children, C1/W3 persisted expansion and
reconstruction hashes, claims/lifecycle, and artifact authority. W5 plan parity
and other placements remain unverified. Open questions remain approval SkipIf
versus removed executable hooks, and masks matching already accepted identifiers.

Shared files touched are `tests/acceptance/protocol_fileset_test.go`,
`tests/acceptance/protocol_test.go` and this report. The sole production change
is `sparkwing/git/git.go`. SDK reference generation into scratch succeeds.
`docs/sdk-git.md` retains four earlier direct-clone description differences,
with no changed FilesetHash synopsis. W9/lead still owns integrated reference
and mirror regeneration.

Friction remains denied sockets, empty `/tmp/.git` and Overwing's stale-binary
warning. No Sparkwing executable or repository Git write ran. Shared Git config
has no injected user identity or worktree setting. All checks finished.
Formatting and diff whitespace checks pass. `/tmp/sw-astra-W4/` was deleted
after verification.

STATUS: CONTINUE
