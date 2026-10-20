# W4 continuation report

Continues [session 25](W4-session25.md). All ten merge paths already had resolved
contents on entry. No conflict markers remain, and the whole repository builds.
The index still records those paths as unmerged because SWARM-BRIEF forbids Git
writes. The lead must stage the resolutions and finish the merge.

## Implementation

`sparkwingtest.Host.Close` now cancels and joins admitted plans, steps, evals and
service callbacks. Admission and shutdown share the fixture mutex, so shutdown
prevents new WaitGroup additions before waiting. Callback execution and joining
hold no fixture lock. Call Close from outside operations and callbacks.

The new acceptance regression failed before the fix for steps, evals and OIDC
callbacks. It now passes for those paths plus latest-output and child callbacks.
Channel barriers prove that cancellation happens before Close returns and that
Close waits for callback cleanup. Calls after shutdown are refused.

Deleted legacy code this session: **0 lines**. No additional inventory tests were
ported. No removed API required caller changes. The earlier amendment work and
hard cuts remain in place, as recorded in [session 23](W4-session23.md).

## Verification

All Go commands used `GOWORK=off`, `GOFLAGS=-buildvcs=false`, `GOMAXPROCS=4`,
`GOCACHE=/tmp/sw-astra-W4/cache`, and `GOTMPDIR`/`TMPDIR=/tmp/sw-astra-W4/tmp`.
Commands used `go -C /home/bombuntu/.xwing/envs/1082/sparkwing`.

- `build -p 4 ./...` passed.
- `vet -p 4 ./internal/nodeproto/... ./internal/protofd ./internal/sdkserve ./sparkwing/... ./internal/acceptance ./tests/acceptance ./tests/acceptance/testdata/protocolgo/...` passed.
- `test -p 4 ./internal/nodeproto/... ./internal/protofd ./internal/sdkserve ./sparkwing/... -count=1 -timeout 90s` passed with the seven inherited exclusions below.
- `test -race -p 4 ./sparkwing/sparkwingtest ./tests/acceptance -run 'TestFakeHost|^TestProtocol/protocol-go-sdk-fake-host-and-linkage/local-go-linux/(fake-host-close|queued-eval-cancellation)$' -count=1 -timeout 90s` passed.
- `test -p 4 ./tests/acceptance -run '^TestProtocol/(protocol-hello-and-handles|protocol-version-and-hard-cut|protocol-go-sdk-fake-host-and-linkage|protocol-mask-acknowledgement)/local-go-linux' -count=1 -v -timeout 90s` passed.
- Formatting and `git diff --check` passed.
- An independent review of a frozen file snapshot found no defect in the shutdown
  change. It confirmed the admission/Wait ordering and the test barriers.

The three selected Linux Go hello, version/hard-cut and fake-host/linkage targets
pass. The new five-case shutdown regression passes within fake-host/linkage.
Its HTTP subprocess subtest remains socket-blocked. Mask's SDK service subtest
passes, while the complete mask target remains skipped for its real host driver.
No additional full acceptance cell was promoted.

The package test's inherited `-skip` expression was
`^(TestFilesetHashFilesystemFallback|TestFilesetHashFramesFileContents|TestFilesetHashIncludesPermissions|TestResolveHostPortsHoldsEveryProbeUntilAllAreChosen|TestWithServicesAddrsPublishesTheAllocatedPort|TestWithServicesAddrsReportsAPinnedPortAndHostNetworking|TestWithServicesStillRunsFnWithoutAddrs)$`.
Three cases encounter the sandbox's ancestor `/tmp/.git`; four require sockets.
Those unchanged environmental failures were not retried. Nested pipeline runtime
test compilation remains the inherited handoff and was not rerun.

## Remaining integration

- W2/W3 must mount the mask, annotation and summary routes with current process
  authority and sink-wide acknowledgement. `internal/controller/policy_contract.go`
  still contains panic stubs. W4 exercises the declared transport with fakes.
- W6 must implement `settings.ResolveConcurrency`, which still contains a panic
  stub. W4 preserves declaration omission and validates explicit values.
- W3/W1 own persisted expansion/replay, storage authority and read-edge adapters.
  W3/W8 own verified engine and granted runner delivery to every remote hello.
- W5 owns consumption of the shared declaration/error vectors. W9/lead own the
  remaining pipeline runtime-test ports and generated public references.
- T1/lead must provide native Windows/Darwin, real HTTP/api.sock, remote placement,
  store/backend and complete acceptance matrix evidence.

No new contract change is needed in another area. No design question remains
from this change. The internal admission helper and WaitGroup implement the
existing joining contract without a new public API, setting or limit.

## Files and friction

W4 files changed: `sparkwing/sparkwingtest/{host,host_contract}.go`.

Shared files touched:

- `tests/acceptance/protocol_test.go`
- `tests/acceptance/protocol_cancel_test.go`
- `design/rewrite/contracts/c3-go-sdk-notes.md`
- This report.

Friction remains denied sockets, the ancestor `/tmp/.git`, the stale Overwing
binary and the inherited nested-pipeline test port. Standards discovery worked.
No Sparkwing binary or Git write ran. Shared Git config retains Korey's identity
and has no `core.worktree` override. All started checks and the reviewer finished.
Scratch and build caches under `/tmp/sw-astra-W4/` were removed after verification.

STATUS: CONTINUE
