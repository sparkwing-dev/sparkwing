# W4 continuation report

Continues [session 29](W4-session29.md). HEAD remains `14d5cf690`.
The ten merge paths already contained resolved contents on entry. No conflict
markers remain. The index still marks them unmerged because the swarm brief
forbids Git writes. The lead must stage those resolutions and finish the merge.

Go jobs and steps now omit empty closure objects. Four existing shared vectors
exercise actual Go builders for omitted checkout fields, explicit false checkout
fields, approval conditions and snake_case job policy. The job vector failed
because Go emitted `closures: {}` on both job and step, changing its canonical
hash. Changing those two serialization tags to `omitzero` fixes that failure.
This is a local W4 serialization correction. The fields were already optional,
so the schema snapshots stay unchanged. No other area's contract changes.

The Go process conformance suite now uses the existing native acceptance pipe
driver. Its Unix-only build tag and duplicate process/pipe lifecycle code are
removed. Windows runs can execute the same sessions with argv handles, and Unix
runs retain their descriptor transport. Linux execution passes. Cross-compilation
does not establish native Windows or Darwin behavior.

Fake-host mask registration now rechecks cancellation while holding the registry
lock. A deterministic acceptance case pauses a call after its first cancellation
check, closes the host, then resumes it. Before the fix, the call returned success
and appended its mask after Close returned. It now returns cancellation and leaves
the registry unchanged. The test uses channels and no sleeps.

Amendment A1-02 gains that registration-race fix. A1-51 gains actual Go-builder
hash checks and the portable process corpus. The earlier A1-06 cancellation,
A1-07 declaration presence, A1-37 Cache/key helpers and A1-54 approval-condition
implementations remain in place and pass the focused package/SDK checks.

Removed **54 lines** from the duplicate Unix-only conformance harness, replacing
them with 17 lines using the shared driver. Deleted legacy production code in
this continuation: **0 lines**. W4's owned production packages contain no
unimplemented marker or Old-suffixed API. No new inventory source test was
ported. No API removal needed a caller migration in another package.

All Go checks used `GOWORK=off`, `GOFLAGS=-buildvcs=false`, `GOMAXPROCS=4`,
`GOCACHE=/tmp/sw-astra-W4/cache`, and `GOTMPDIR`/`TMPDIR=/tmp/sw-astra-W4/tmp`.
Commands used `go -C /home/bombuntu/.xwing/envs/1082/sparkwing`.

| Check | Result |
| --- | --- |
| `build -p 4 ./...` | Passed for the whole repository. |
| `vet -p 4 ./internal/nodeproto/... ./internal/protofd ./internal/sdkserve ./sparkwing/... ./internal/acceptance ./tests/acceptance ./tests/acceptance/testdata/protocolgo/...` | Passed. |
| `test -p 4 ./internal/nodeproto/... ./internal/protofd ./internal/sdkserve ./sparkwing/... -count=1 -timeout 90s` with the exclusions below | Passed. |
| `test -race -p 4 ./internal/nodeproto/conformance ./sparkwing/sparkwingtest -count=1 -timeout 90s` | Passed. |
| `test -race -p 4 ./tests/acceptance -run '^TestProtocol/protocol-go-sdk-fake-host-and-linkage/local-go-linux/mask-close-race$' -count=1 -timeout 90s` | Passed after failing before the fix. |
| `test -p 4 ./tests/acceptance -run '^TestProtocol/(protocol-hello-and-handles\|protocol-version-and-hard-cut\|protocol-go-sdk-fake-host-and-linkage\|protocol-mask-acknowledgement)/local-go-linux' -count=1 -v -timeout 90s` | Passed with the acceptance limits below. |
| `GOOS=windows GOARCH=amd64 test -p 4 -c -o /tmp/sw-astra-W4/conformance-windows-amd64.test.exe ./internal/nodeproto/conformance` | Compiled. |
| `GOOS=windows GOARCH=arm64 test -p 4 -c -o /tmp/sw-astra-W4/conformance-windows-arm64.test.exe ./internal/nodeproto/conformance` | Compiled. |
| `GOOS=darwin GOARCH=arm64 test -p 4 -c -o /tmp/sw-astra-W4/conformance-darwin-arm64.test ./internal/nodeproto/conformance` | Compiled. |

The package test's `-skip` expression was
`^(TestFilesetHashFilesystemFallback|TestFilesetHashFramesFileContents|TestFilesetHashIncludesPermissions|TestResolveHostPortsHoldsEveryProbeUntilAllAreChosen|TestWithServicesAddrsPublishesTheAllocatedPort|TestWithServicesAddrsReportsAPinnedPortAndHostNetworking|TestWithServicesStillRunsFnWithoutAddrs)$`.
These seven inherited exclusions remain: three meet the sandbox's ancestor
`/tmp/.git`, and four need sockets. They were not retried in the unchanged
environment.

Linux hello and version/hard-cut cells pass. The fake-host/linkage cell passes its
available subtests, including mask-close-race. Its real HTTP subprocess subtest
skips because sockets are denied. Mask's SDK-service subtest passes, while the
full mask target still skips for real HTTP registration and sink installation.
No new full placement cell is claimed.

Real integration remains with W2/W3 for authenticated mask/annotation/summary
routes, W6 for concurrency resolution, W3/W1 for persisted expansion and storage
authority, and W3/W8 for verified engines and granted runner delivery. Fresh
source inspection confirms panic stubs in PolicyRouter, ResolveConcurrency and
Resolved.InstallSDK. W4 tests their declared service boundaries with fakes.
W5 still owns TypeScript vector consumption. W3/W8 own runtime fixture retirement
under A1-51, with W9/lead owning consumer documentation and generated references.
The inherited nested `.sparkwing` test compile failure still needs that port.
T1/lead must supply native runs, real HTTP/api.sock, remote placements,
store/backend coverage and the full QA matrix.

No new cross-area contract request or open design question is raised. No product
option, environment variable, limit, adapter or runtime interface was added.
Independent review, hook enforcement and Sparkwing pipelines remain with the
lead under the swarm brief.

Changed W4 files in this continuation:

- `internal/nodeproto/plan_contract.go`
- `internal/nodeproto/conformance/conformance_test.go`
- `internal/nodeproto/conformance/vectors_test.go`
- `sparkwing/sparkwingtest/host.go`

Shared files touched:

- `tests/acceptance/protocol_test.go`
- `tests/acceptance/protocol_cancel_test.go`
- `design/rewrite/contracts/c3-go-sdk-notes.md`
- This report.

Friction remains denied sockets, ancestor `/tmp/.git`, the stale Overwing binary,
and the runtime fixture port. No Sparkwing binary, Git write, release or deployment
ran. Git retains no local user identity or core.worktree override.
Formatting and `git diff --check` pass. All started checks finished, and scratch
under `/tmp/sw-astra-W4/` was deleted after verification.

STATUS: CONTINUE
