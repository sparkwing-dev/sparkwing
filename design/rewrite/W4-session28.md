# W4 continuation report

Continues [session 27](W4-session27.md). All ten conflict files had resolved
contents on entry. No conflict markers remain. The index still lists those paths
as unmerged because the swarm brief forbids Git writes. The lead must stage the
resolutions and finish the merge. HEAD remains `14d5cf690`.

Active fake-host service callbacks now receive fixture cancellation even when
callers use `context.WithoutCancel`. The existing context-linking code is shared
with OIDC, latest-output and child callbacks. Close cancels those calls and waits
for the callbacks to return. The three new acceptance cases failed before the
fix and pass afterward, including under the race detector. Existing shutdown and
closed-service cases still pass. No public signature changed.

Deleted **34 legacy test/setup lines** in this continuation: the obsolete
lint-cache HTTP restore test, its unused imports and two removed cache credential
setup lines. The production lint cache uses the declared Cache interface already
implemented in session 23. No additional inventory source test was ported. The
three new cases extend the existing fake-host acceptance proof.

W4 amendment implementations remain as recorded in [session 23](W4-session23.md).
A1-37's obsolete lint-cache test is now retired too. W4-owned production packages
contain no unimplemented marker or Old-suffixed API. No new cross-area contract
change, design question, environment variable, limit or compatibility API was
introduced.

All Go checks used `GOWORK=off`, `GOFLAGS=-buildvcs=false`, `GOMAXPROCS=4`,
`GOCACHE=/tmp/sw-astra-W4/cache`, and `GOTMPDIR`/`TMPDIR=/tmp/sw-astra-W4/tmp`.
Commands used `go -C /home/bombuntu/.xwing/envs/1082/sparkwing` unless stated.

| Check | Result |
| --- | --- |
| `build -p 4 ./...` | Passed for the whole repository. |
| `build -p 4 ./...` in the nested `.sparkwing` module | Passed. |
| `vet -p 4 ./internal/nodeproto/... ./internal/protofd ./internal/sdkserve ./sparkwing/... ./internal/acceptance ./tests/acceptance ./tests/acceptance/testdata/protocolgo/...` | Passed. |
| `test -p 4 ./internal/nodeproto/... ./internal/protofd ./internal/sdkserve ./sparkwing/... -count=1 -timeout 90s` with exclusions below | Passed. |
| `test -race -p 4 ./sparkwing/sparkwingtest ./tests/acceptance -run 'TestFakeHost\|^TestProtocol/protocol-go-sdk-fake-host-and-linkage/local-go-linux/(fake-host-close\|closed-fake-host-services\|queued-eval-cancellation)$' -count=1 -timeout 90s` | Passed. |
| `test -p 4 ./tests/acceptance -run '^TestProtocol/(protocol-hello-and-handles\|protocol-version-and-hard-cut\|protocol-go-sdk-fake-host-and-linkage\|protocol-mask-acknowledgement)/local-go-linux' -count=1 -v -timeout 90s` | Passed with the limits below. |

The package command's `-skip` expression was
`^(TestFilesetHashFilesystemFallback|TestFilesetHashFramesFileContents|TestFilesetHashIncludesPermissions|TestResolveHostPortsHoldsEveryProbeUntilAllAreChosen|TestWithServicesAddrsPublishesTheAllocatedPort|TestWithServicesAddrsReportsAPinnedPortAndHostNetworking|TestWithServicesStillRunsFnWithoutAddrs)$`.
These are the inherited three sandbox ancestor `.git` failures and four tests
requiring sockets. They were not rerun in the unchanged environment.

Local Go/Linux hello and version/hard-cut cells pass. Fake-host/linkage passes its
available subtests, including OIDC, latest-output and child cancellation through
detached contexts. Its real host-route subprocess subtest remains skipped because
loopback sockets are denied. Mask's SDK service proof passes, while the full mask
cell remains skipped for real HTTP registration and placement drivers. No new
full placement cell was promoted.

The nested `.sparkwing` test package still cannot compile. A fresh
`test -p 4 ./jobs -run '^$'` reports removed Registration, Nodes,
AdmissionClassValue, ResourceHints, DepIDs and StepByID APIs. No tests ran.
The two small shared-file deletions here retire obsolete cache behavior without
restoring those removed SDK APIs. Remaining runtime fixture migrations stay with
the A1-51 integration handoff.

Real integration remains with W2/W3 for authorized mask/annotation/summary routes,
W6 for `settings.ResolveConcurrency`, W3/W1 for persisted expansion and storage
authority, and W3/W8 for verified engines and granted runner delivery. The policy
router and concurrency resolver still have panic stubs. W4 tests declared service
interfaces with fakes. W5 owns shared-vector consumption. W9/lead own generated
references. T1/lead must supply native Windows/Darwin, real HTTP/api.sock, remote
placement, store/backend and full-matrix QA.

Changed W4 file: `sparkwing/sparkwingtest/host.go`.

Shared files touched in this continuation:

- `tests/acceptance/protocol_cancel_test.go`
- `.sparkwing/jobs/lint_contention_test.go`
- `.sparkwing/jobs/lint_worktree_test.go`
- `design/rewrite/contracts/c3-go-sdk-notes.md`
- This report.

Friction: denied sockets, ancestor `/tmp/.git`, the stale Overwing binary and the
nested pipeline test port. The supplied Xwing worktree remained usable. No
Sparkwing binary, Git write, release or deployment ran. Formatting and
`git diff --check` passed. The repository has no local user identity or
core.worktree override. All started checks finished. Scratch under
`/tmp/sw-astra-W4/` was removed after verification.

STATUS: CONTINUE
