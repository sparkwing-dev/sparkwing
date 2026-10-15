# W4 continuation report

Continues [session 28](W4-session28.md). The ten merge-conflict files already had
resolved contents on entry. Amended signatures and W4 behavior remain intact.
No conflict markers remain. The index still lists the ten paths as unmerged
because SWARM-BRIEF forbids Git writes. The lead must stage them and finish the
merge. HEAD remains `14d5cf690`.

Annotations now use the SDK's existing execution guard. Fake-host annotations
during planning fail as `plan_failed`, matching the real transport's missing
execution binding. Ordinary declaration logs still work.

Fake-host annotation and summary writes now check request and fixture cancellation
while holding the record lock. A retained detached body context cannot append
records after Close. Both cases failed before the fix and now pass. The checks
reuse the existing cancellation guard and lock. No public API, setting, limit or
contract shape was added.

Added acceptance coverage for generator log, annotation and summary attribution.
Eval records go to run logs without a job. The generated child's step record
carries its bound job. The existing attribution behavior is retained.

Deleted legacy code in this continuation: **0 lines**. No additional inventory
test was ported. No API removal required another package's caller to change.
W4's amendment implementations and hard cuts remain as recorded in
[session 23](W4-session23.md). Its owned production packages contain no
unimplemented marker or Old-suffixed API.

All Go checks used `GOWORK=off`, `GOFLAGS=-buildvcs=false`, `GOMAXPROCS=4`,
`GOCACHE=/tmp/sw-astra-W4/cache`, and `GOTMPDIR`/`TMPDIR=/tmp/sw-astra-W4/tmp`.
Commands used `go -C /home/bombuntu/.xwing/envs/1082/sparkwing`.

| Check on the final Go tree | Result |
| --- | --- |
| `build -p 4 ./...` | Passed for the whole repository. |
| `vet -p 4 ./internal/nodeproto/... ./internal/protofd ./internal/sdkserve ./sparkwing/... ./internal/acceptance ./tests/acceptance ./tests/acceptance/testdata/protocolgo/...` | Passed. |
| `test -p 4 ./internal/nodeproto/... ./internal/protofd ./internal/sdkserve ./sparkwing/... -count=1 -timeout 90s` with exclusions below | Passed. |
| `test -race -p 4 ./sparkwing/sparkwingtest ./tests/acceptance -run 'TestFakeHost\|^TestProtocol/protocol-go-sdk-fake-host-and-linkage/local-go-linux/(closed-fake-host-services\|declaration-services\|eval-run-logs)$' -count=1 -timeout 90s` | Passed. |
| `test -p 4 ./tests/acceptance -run '^TestProtocol/(protocol-hello-and-handles\|protocol-version-and-hard-cut\|protocol-go-sdk-fake-host-and-linkage\|protocol-mask-acknowledgement)/local-go-linux' -count=1 -v -timeout 90s` | Passed with the acceptance limits below. |

The package test's `-skip` expression was
`^(TestFilesetHashFilesystemFallback|TestFilesetHashFramesFileContents|TestFilesetHashIncludesPermissions|TestResolveHostPortsHoldsEveryProbeUntilAllAreChosen|TestWithServicesAddrsPublishesTheAllocatedPort|TestWithServicesAddrsReportsAPinnedPortAndHostNetworking|TestWithServicesStillRunsFnWithoutAddrs)$`.
These seven exclusions are inherited. Three encounter the sandbox's ancestor
`/tmp/.git`. Four require sockets. Those unchanged failures were not retried.

The Linux Go hello and version/hard-cut targets pass. Fake-host/linkage passes its
available subtests, including planning annotation refusal, annotation/summary
refusal after Close and eval attribution. Its real HTTP subprocess subtest still
skips because sockets are denied. Mask's SDK service subtest passes. Its full
target still skips for real HTTP registration and host sink installation.
No new full placement cell was promoted.

The nested `.sparkwing` test package still fails to compile. A fresh
`test -p 4 ./jobs -run '^$'` reports removed Registration, Nodes,
AdmissionClassValue, ResourceHints, DepIDs and StepByID APIs. No tests ran. Those
remaining runtime fixture ports belong to the A1-51 integration handoff.

Real integration remains with W2/W3 for authorized mask/annotation/summary routes,
W6 for `settings.ResolveConcurrency`, W3/W1 for persisted expansion and storage
authority, and W3/W8 for verified engines and granted runner delivery. PolicyRouter,
ResolveConcurrency and the engine resolver/install contracts still have panic
stubs. W4 exercises declared service interfaces with fakes. W5 owns shared-vector
consumption. W9/lead own remaining pipeline runtime fixture ports and generated
references. T1/lead must supply native Windows/Darwin, real HTTP/api.sock, remote
placement, store/backend and full-matrix QA.

No new cross-area contract change or open design question is requested.
Independent review and the Sparkwing pipelines remain with the lead.

W4 files changed in this continuation:

- `sparkwing/sdk_contract.go`
- `sparkwing/sparkwingtest/host.go`

Shared files touched:

- `tests/acceptance/protocol_test.go`
- `tests/acceptance/protocol_cancel_test.go`
- `tests/acceptance/protocol_declaration_test.go`
- `tests/acceptance/protocol_closure_test.go`
- `design/rewrite/contracts/c3-go-sdk-notes.md`
- This report.

Friction remains denied sockets, ancestor `/tmp/.git`, stale Overwing and the
nested pipeline test port. No Sparkwing binary, Git write, release or deployment
ran. Formatting and `git diff --check` passed. Git has no local user identity or
core.worktree override. All started checks finished. Scratch under
`/tmp/sw-astra-W4/` was removed after verification.

STATUS: CONTINUE
