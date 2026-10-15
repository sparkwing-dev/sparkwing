Continues session 32 from committed HEAD `e026d501c`. The worktree was clean.

The SDK now escapes dots in every variable host-service path segment. Valid
identifiers `.` and `..` previously triggered HTTP path-cleaning redirects.
Secret, output, latest-output, child-await and cleanup calls use one encoder.
No public API, wire field, caller or customer limit changed.

Invariant: a host-service identifier occupies one opaque escaped path segment
and round-trips unchanged without selecting another route.

The regression failed for both dot identifiers before the fix. The new
`FuzzHostRouteIdentifiers` checks outgoing requests through a fake HTTP transport,
including separators, percent escapes, query/fragment delimiters, Unicode and
control characters. Its final 15-second fuzz run passed 73,255 executions.
This proves the SDK request boundary, not the receiving authorization router.

Local contract clarification: `contracts/c3-go-sdk-notes.md` now states opaque
segment encoding and one-time decoding. No contract type or signature changed.

New receiver integration requirement for W2/W3: match route templates over escaped
segments before decoding wildcard values. A direct Go ServeMux probe with
`GET /node/v1/jobs/{job}/output` does not match
`/node/v1/jobs/%2F/output`. The valid job ID `/` needs the same one-segment
semantics as `a/b`. Do not restrict accepted IDs or double-decode them to hide
the router gap. `PolicyRouter` remains a stub; W4 leaves its implementation to W2.

Legacy code deleted: **0 lines** in this continuation. Existing hard cuts remain
unchanged. Inventory tests ported: **0**. New property test: **1**. No caller
outside W4 needed migration.

All Go checks used `GOWORK=off`, `GOFLAGS=-buildvcs=false`, `GOMAXPROCS=4`,
`GOCACHE=/tmp/sw-astra-W4/cache`, `GOTMPDIR=/tmp/sw-astra-W4/tmp` and
`TMPDIR=/tmp/sw-astra-W4/tmp`, with `go -C` pointing to this worktree.

Passed:

- `build -p 4 ./...` for the whole repository.
- `vet -p 4 ./internal/nodeproto/... ./internal/protofd ./internal/sdkserve ./sparkwing/... ./internal/acceptance ./tests/acceptance ./tests/acceptance/testdata/protocolgo/...`.
- `test -p 4 ./internal/nodeproto/... ./internal/protofd ./internal/sdkserve ./sparkwing/... -count=1 -timeout 120s`, with the seven exclusions below.
- `test -p 4 ./internal/sdkserve -run '^$' -fuzz '^FuzzHostRouteIdentifiers$' -fuzztime 15s -parallel 4 -timeout 120s`.
- `test -p 4 ./tests/acceptance -run '^TestProtocol/(protocol-hello-and-handles|protocol-version-and-hard-cut|protocol-go-sdk-fake-host-and-linkage|protocol-mask-acknowledgement)/local-go-linux' -count=1 -v -timeout 120s`.
- Formatting and `git diff --check`.

The package exclusions remain
`^(TestFilesetHashFilesystemFallback|TestFilesetHashFramesFileContents|TestFilesetHashIncludesPermissions|TestResolveHostPortsHoldsEveryProbeUntilAllAreChosen|TestWithServicesAddrsPublishesTheAllocatedPort|TestWithServicesAddrsReportsAPinnedPortAndHostNetworking|TestWithServicesStillRunsFnWithoutAddrs)$`.
The first three encounter the sandbox's ancestor `/tmp/.git`. The other four
need sockets. This continuation did not rerun those known environment failures.

Acceptance passes Linux hello/handles, version/hard-cut and the available
fake-host/linkage subtests. Its real HTTP subtest skips because sockets are denied.
Mask's SDK-service subtest passes; the full mask target skips for W2/W3's real
registration and sink drivers. No newly complete placement cell is claimed.

Other real integrations remain as recorded in session 32: W2/W3 authenticated
records and masking, W6 sealed concurrency resolution, W1/W3 persisted expansion
and storage authority, W3/W8 engine delivery, W5 shared corpus consumption and
native placement QA. `ResolveConcurrency` and `Resolved.InstallSDK` remain stubs.
No other open W4 design question or cross-area signature request was found.

Owned changes are `internal/sdkserve/routes.go` and its new property test.
Shared files touched are `contracts/c3-go-sdk-notes.md` and this report. Friction
remains denied sockets, ancestor `/tmp/.git` and stale Overwing. No runtime
mechanism, environment variable, compatibility surface or restriction was added.

All checks finished. No scratch-owned process remained, and `/tmp/sw-astra-W4/`
was deleted. Git has no local identity or core.worktree override. No Sparkwing
binary or Git write ran. The lead retains pipelines, review and committing.

STATUS: CONTINUE
