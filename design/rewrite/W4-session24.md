# W4 continuation report

Continues [session 23](W4-session23.md). Its amendment implementation and caller
migrations remain in the supplied worktree.

All ten conflict files already had resolved contents on entry. Verification found
no conflict markers. The resolutions preserve amended contracts, W4 behavior and
the merged host masking changes. Git still reports ten unmerged index entries
because the swarm brief forbids Git writes. The lead must stage them and finish
the merge.

A1-06 now also covers queued SDK evals. A canceled eval previously waited on
another closure's mutex and could invoke its closure afterward. The SDK now
serializes evals through a cancelable channel and checks cancellation before
invocation. The public fake-host acceptance regression checks early cancellation,
absence of the canceled callback and successful subsequent evaluation.

The regression reproduced the blocked mutex with a five-second test timeout.
An earlier invocation without that bound was interrupted. The corrected test
passes under the race detector without sleeps or elapsed-time assertions.

Four implementation lines were replaced. No additional legacy files were
deleted. Earlier SDK/API hard cuts and the previous session's lint-cache deletion
remain intact. No new inventory-source test was ported.

The other W4 amendment actions remain as recorded in session 23: acknowledged
mask/record transport, collision refusal, omission-preserving concurrency, public
cache keys, shared v2 vectors and approval conditions. No new cross-area contract
change or open design question is requested.

Verification used `GOWORK=off`, `GOFLAGS=-buildvcs=false`, `GOMAXPROCS=4`,
`GOCACHE=/tmp/sw-astra-W4/cache`, and `GOTMPDIR`/`TMPDIR` under
`/tmp/sw-astra-W4/tmp`. Commands used `go -C` with the supplied absolute worktree.

Passed checks:

- Whole-repository `go build -p 4 ./...` after the implementation change.
- `go vet -p 4` over `internal/nodeproto/...`, `internal/protofd`,
  `internal/sdkserve`, `sparkwing/...`, `internal/acceptance`,
  `tests/acceptance` and `tests/acceptance/testdata/protocolgo/...`.
- W4 package tests with the same seven inherited exclusions listed in session 23.
  Three filesystem fallback tests encounter the sandbox's empty ancestor
  `/tmp/.git`. Four service-port tests require sockets. These were not retried.
- The selected `local-go-linux` hello/handles, version/hard-cut and public
  fake-host/linkage cells, including the new `queued-eval-cancellation` subtest.
  The linkage cell retains its socket-blocked host-route subprocess subtest.
- Mask's SDK service-contract subtest. The full mask target remains skipped for
  its W2/W3 host driver.
- Race checks for queued cancellation, public fake-host execution, mask collisions,
  later declaration collisions and approval conditions.
- Formatting and `git diff --check`.

The acceptance selection was
`^TestProtocol/(protocol-hello-and-handles|protocol-version-and-hard-cut|protocol-go-sdk-fake-host-and-linkage|protocol-mask-acknowledgement)/local-go-linux`.
The new regression is selected by
`^TestProtocol/protocol-go-sdk-fake-host-and-linkage/local-go-linux/queued-eval-cancellation$`.
Tests used `-count=1`; the final focused race run used `-timeout 60s`.

Production files changed this session are `sparkwing/plan.go` and
`sparkwing/protocol_operations.go`. Shared files touched are
`tests/acceptance/protocol_test.go`, new `tests/acceptance/protocol_cancel_test.go`,
`design/rewrite/contracts/c3-protocol-notes.md`, and this report. No removed API
required another caller migration this session. The inherited shared-file list
and merge resolutions remain in session 23.

The remaining real integrations are W2/W3 node-host routes and sink installation,
W6 concurrency resolution, W3/W1 persisted expansion/replay and storage authority,
W5 consumption of the new corpus rows, and T1/lead native and placement drivers.
The nested pipeline runtime-test migration and generated public references remain
with their owners under A1-51. Their inherited failures were not rerun. Native
Windows/Darwin, real HTTP/api.sock, remote placements and full acceptance remain
unverified. The lead owns independent review, pipelines and Git completion.

The channel replaces the mutex because a mutex wait cannot observe cancellation.
It adds no public setting, limit or compatibility path. All started checks have
finished. The shared Git configuration still names Korey and has no core.worktree
override. Scratch and build caches under `/tmp/sw-astra-W4/` are removed after the
final checks. No Sparkwing binary or repository Git write ran.

STATUS: CONTINUE
