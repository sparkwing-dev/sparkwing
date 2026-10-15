# W4 continuation report

Continues [session 26](W4-session26.md). All ten conflict paths already had resolved
contents on entry. No conflict markers remain. The index still marks them unmerged
because the swarm brief forbids Git writes. The lead must stage the resolutions
and finish the merge. The whole repository builds.

The fake host now refuses service calls after Close even when a caller uses
`context.WithoutCancel` on its fixture context. The guard checks the fixture's own
state before reading answers or registering masks. The acceptance regression
failed for secret, variable, output and mask calls before the fix. All seven cases
now pass, including OIDC, latest-output and child calls. Closing cannot be bypassed
by retaining service values in a detached context.

Deleted legacy code this session: **0 lines**. No additional inventory test was
ported. No API removal required another caller migration. W4's amendment work and
earlier hard cuts remain as recorded in [session 23](W4-session23.md). Its owned
production packages contain no unimplemented marker or Old-suffixed API.

All Go commands used `GOWORK=off`, `GOFLAGS=-buildvcs=false`, `GOMAXPROCS=4`,
`GOCACHE=/tmp/sw-astra-W4/cache`, and `GOTMPDIR`/`TMPDIR=/tmp/sw-astra-W4/tmp`.
Commands used `go -C /home/bombuntu/.xwing/envs/1082/sparkwing`.

| Check on the final Go tree | Result |
| --- | --- |
| `build -p 4 ./...` | Passed. |
| `vet -p 4 ./internal/nodeproto/... ./internal/protofd ./internal/sdkserve ./sparkwing/... ./internal/acceptance ./tests/acceptance ./tests/acceptance/testdata/protocolgo/...` | Passed. |
| `test -p 4 ./internal/nodeproto/... ./internal/protofd ./internal/sdkserve ./sparkwing/... -count=1 -timeout 90s` with exclusions below | Passed. |
| `test -race -p 4 ./sparkwing/sparkwingtest ./tests/acceptance -run 'TestFakeHost\|^TestProtocol/protocol-go-sdk-fake-host-and-linkage/local-go-linux/(fake-host-close\|closed-fake-host-services\|queued-eval-cancellation)$' -count=1 -timeout 90s` | Passed. |
| `test -p 4 ./tests/acceptance -run '^TestProtocol/(protocol-hello-and-handles\|protocol-version-and-hard-cut\|protocol-go-sdk-fake-host-and-linkage\|protocol-mask-acknowledgement)/local-go-linux' -count=1 -v -timeout 90s` | Passed with the acceptance limits below. |

The package command's `-skip` expression was
`^(TestFilesetHashFilesystemFallback|TestFilesetHashFramesFileContents|TestFilesetHashIncludesPermissions|TestResolveHostPortsHoldsEveryProbeUntilAllAreChosen|TestWithServicesAddrsPublishesTheAllocatedPort|TestWithServicesAddrsReportsAPinnedPortAndHostNetworking|TestWithServicesStillRunsFnWithoutAddrs)$`.
Three filesystem cases still fail because ancestor discovery sees the sandbox's
empty `/tmp/.git`. Retrying those cases with `GIT_CEILING_DIRECTORIES=/tmp` did not
help because FilesetHash performs its own ancestor search. Four service-port cases
require sockets. Those exclusions remain environmental limits on the package run.

The local-go-linux hello and version/hard-cut targets pass. Fake-host/linkage
passes its available subtests, including the seven new shutdown cases. Its HTTP
subprocess subtest remains socket-blocked. Mask's SDK service subtest passes,
while its full target remains skipped for the real placement driver. No new full
placement cell was promoted.

The nested `.sparkwing` jobs tests still fail to compile. A fresh `go test -p 4
./jobs -run '^$'` from that module reports removed Registration, Nodes,
AdmissionClassValue, ResourceHints, DepIDs and StepByID APIs. No tests ran. Their
remaining runtime fixture port belongs in the integration handoff, rather than
restoring deleted SDK getters or RuntimePlumbing.

Real integration remains with W2/W3 for authorized mask/annotation/summary routes,
W6 for `settings.ResolveConcurrency`, W3/W1 for persisted expansion and storage
authority, and W3/W8 for verified engines and granted runner delivery. The policy
router and concurrency resolver still have panic stubs. W4 exercises its declared
interfaces with fakes. W5 owns shared-vector consumption. W9/lead own remaining
pipeline runtime fixtures and generated references. T1/lead must provide native
Windows/Darwin, HTTP/api.sock, remote placement, store/backend and full-matrix QA.
No new cross-area contract change or design question is requested.

Changed W4 file: `sparkwing/sparkwingtest/host.go`.

Shared files touched:

- `tests/acceptance/protocol_cancel_test.go`
- `tests/acceptance/protocol_test.go`
- `design/rewrite/contracts/c3-go-sdk-notes.md`
- This report.

Friction remains denied sockets, ancestor `/tmp/.git`, the stale Overwing binary,
and the nested pipeline test port. Xwing context discovery also failed while
trying to create a lock outside the writable roots. The explicitly selected
worktree remained usable. No Sparkwing binary or Git write ran. Formatting and
`git diff --check` passed. Shared Git config retains Korey's identity without a
core.worktree override. All started checks finished, and scratch under
`/tmp/sw-astra-W4/` was removed after verification.

STATUS: CONTINUE
