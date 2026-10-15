# W8 HTTP serving exit continuation

The approved cancellation classification is committed in production. Both
TestLoopPendingAwardPreservesPolicyDenial and
TestLoopPendingPolicyAcceptsArbitraryCancellationCause pass. Its stop logic
remains 84 -> 75 physical lines. The pending wait remains 44 -> 40 and loop.go
408 -> 401. This continuation leaves that completed work unchanged.

## Implemented

Invariant: every HTTP serving exit cancels its request contexts and joins its
handlers before the local runtime closes its store or releases its home election.

The HTTP serving scope now owns a child context. Listener failure cancels it
before graceful drain, connection cleanup and handler joining. Caller cancellation
uses the same exit path. The listener error survives cleanup. The old separate
served channel and shutdown select are deleted because they could skip request
cancellation when a listener failed.

TestLocalHTTPListenerFailureCancelsAndJoinsRequests reproduced the failure on
the prior production code for hijacked API and dashboard requests. All four
HTTP/hijacked and API/dashboard cases now pass. The ordinary HTTP cases retain
coverage of the common exit path.

TestLocalRuntimeListenerFailureJoinsRequestsBeforeStoreClose exercises the real
runtime and HTTP server with an in-memory listener and a fake shared controller.
It holds handler cleanup after request cancellation, checks that the store is
still open, then verifies automatic shutdown, the retained listener error, store
close and release of the home election. It waits for automatic runtime exit
before calling Stop, so Stop cannot supply the missing cancellation itself.

Production changes add 8 and remove 10 physical lines. serveLocalHTTP shrinks
from 50 to 48 lines, and local_http.go from 154 to 152. No whole legacy file is
deleted. The previous cluster/dispatcher/daemon/socket-client cuts remain
deleted. There is no new API, interface, option, environment variable, timeout,
service or foreign contract change. The existing serving scope owns cancellation.

## Amendment and handoff disposition

All 26 amendment rows naming W8 and the W8 handoffs retain the dispositions in
[the amendment audit](W8-AMENDMENTS-1.md). A1-40 advances through the listener
failure and joined store-lifetime regressions above. A1-08/09/18/19/38/39/55
retain their unified-loop, award-input, bootstrap, client-composition and batched
sync components. The cancellation approval remains complete. The other assigned
rows retain their recorded component/native limits.

The complete acceptance package passes, including W8's runner/store lifecycle,
revocation, bootstrap/store, launcher sync/publication, local lifecycle, managed
units, identity, credentials and engine components. No new native catalog cell
is enabled. No additional T1 inventory test is ported.

Real integration remains open at these existing boundaries:

- W3 source.ForRequest, ForExecution, Acquire and Entry.Close remain panic
  stubs. lease.Keeper.Run and CancelError.Error remain panic stubs. W8's defaults
  call these declared APIs, while component tests substitute their runtime ports.
- W3 still needs shared RunHost/NodeHost constructors, run/plan/eval runner
  identity inputs and the shared process constructor. Delegated node routes,
  bound storage and generated-code replay still need their declared construction.
- W9 NewEngineClient and NewDataClient remain panic stubs. W8 supplies the
  private HTTP transport and retains it across claimant, award and exchange
  clients. Final CLI/runner/Cloud factories and the recorded launch/plan parser
  and source-init/runtime handoff repairs remain open.
- W1/W2 still need the elected local controller/store/queue/maintenance and
  shared policy-handler factory. EngineController.SyncLauncher remains a panic
  stub. Plan/eval resource awards remain open. W8 tests these interfaces with fakes.
- W3/W7 still need the recorded workspace-off binary-cache hashing operation.
  The selected-engine install path retains the shared hasher.
- T1/lead still need native HTTP/api.sock, Windows/macOS execution, deployed
  Cloud network/cleanup and Docker/scratch evidence, actual SDK hello, complete
  placement cells and real-repository QA.

No new contract request or product question is introduced. The existing foreign
contract requests remain open. Shared file touched is contracts/handoffs.md.
All Go changes stay in internal/wingd. No foreign production caller needs
migration. Source documentation changes in internal/wingd/LOCAL-TRANSPORT.md,
and the new behavior tests cover its serving-exit contract.

## Verification

Go commands used this worktree through go -C with GOWORK=off,
GOFLAGS=-buildvcs=false, GOMAXPROCS=4 and GOCACHE/GOTMPDIR/TMPDIR under
/tmp/sw-astra-W8. Tests use -p 4.

| Check | Result |
| --- | --- |
| go build -p 4 ./... | Passed over the whole repository. |
| go vet -p 4 ./internal/wingd/... ./internal/runners/... ./internal/engine/... ./internal/cluster ./tests/acceptance ./internal/acceptance | Passed. |
| go test -p 4 ./internal/runners/... ./internal/engine/... ./tests/acceptance ./internal/acceptance -count=1 -timeout=4m | Passed. Acceptance took 63.689 seconds. |
| go test -race -p 4 ./internal/wingd -run '^(TestLocal(Runtime\|HTTP\|Dashboard\|Control)\|FuzzLocalHTTP)' -count=1 -timeout=4m | Passed, including both new regressions. |
| go test -p 4 ./internal/wingd -run '^$' -fuzz '^FuzzLocalHTTPIdentity$' -fuzztime=100x -parallel=2 -timeout=2m | Passed 138 executions. |
| go run ./internal/commentcheck -base main . | Clean across 1,192 Go files. |

The initial direct regression failed before the production change. The runtime
test's first draft called Stop before automatic failure propagation and received
nil. Waiting for automatic exit corrected that test ordering. No error-filtering
workaround was added to production.

No public docs impact: the change completes the internal serving/shutdown
contract. CLI grammar, settings and wire signatures are unchanged. The lead
retains independent review, commits and native verification. No Sparkwing binary,
pipeline, Git write, deployment or release ran. Overwing discovery worked while
reporting its installed binary behind source. Git config has no local identity
or core.worktree override. Check processes finished and worker scratch was removed.
