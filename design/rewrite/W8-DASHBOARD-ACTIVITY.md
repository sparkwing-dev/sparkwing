# W8 dashboard activity continuation

The approved cancellation redesign is already committed and remains unchanged.
Both TestLoopPendingAwardPreservesPolicyDenial and
TestLoopPendingPolicyAcceptsArbitraryCancellationCause pass in the full runner
suite. The recorded stop path is 84 -> 75 lines, pending wait 44 -> 40 and
loop.go 408 -> 401. The current file still has 401 lines.

## Implemented

Invariant: an open dashboard connection holds the local controller alive until
that connection closes, including HTTP keep-alive and hijacked connections.

The local runtime now joins dashboard connection lifetime to its idle policy
and status. Store/worker activity reports cannot erase that hold. Closing the
last connection starts a fresh idle interval. API connections add no dashboard
hold. Connection close releases its hold once, including concurrent closes.

TestLocalRuntimeDashboardConnectionsHoldIdle failed before the fix in both its
HTTP and hijacked cases. Each reported zero dashboard connections and an idle
timestamp while its connection was open. Both now pass through real HTTP,
LocalRuntime.Status, idle expiry and joined shutdown over in-memory listeners.
The test also replaces controller activity while the connection remains open.

Production Go adds 31 lines and removes 9. No legacy file is deleted in this
continuation. The retired cluster claim/source/heartbeat paths, dispatchers and
old daemon remain deleted. No production caller outside W8 needs migration.
The new regression is 113 lines. No new T1 inventory port is claimed.

The added mechanism is one internal connection-lifetime callback and a guarded
count in the existing daemon. Reporting only handler lifetime would release
hijacked or idle keep-alive connections too early. Reverting the four production
file changes removes the mechanism. No flag, timeout, environment variable,
service or foreign Go contract changes.

## Amendment and handoff disposition

Every W8 row and handoff was checked against the committed continuation. The
[26-row audit](W8-AMENDMENTS-1.md) retains the per-row details.

| Rows | Disposition |
| --- | --- |
| A1-08/09/18/19/38/39/55 | Unified claim loop, award inputs, preparation, keeper/client construction, private bootstrap and batched launcher sync remain implemented against declared interfaces. Cancellation regressions pass. Concrete foreign bodies and missing host identity inputs remain below. |
| A1-40 | Dashboard HTTP lifetime now supplies the runtime's fourth activity hold. Readiness, status, stay, drain and identity components pass. Final local services remain below. |
| A1-10/11/12/36/43/46/48/50/51/52 | Existing engine/install/source-input/environment/storage seams remain intact. Source, host, storage, final CLI and native packaging obligations remain with their owners. |
| A1-20/26/27/45/49 | Server-derived authority, registration, settlement and settings remain controller/store responsibilities. Credential-file startup and enrollment cleanup components pass. |
| A1-41/42/53 | Managed-unit and Cloud builder/boundary components pass. Native services, deployed isolation/teardown and full QA still require native evidence. |

## Real integration left open

- W3: source.ForRequest/ForExecution/Acquire/Entry.Close and lease.Keeper.Run
  are panic stubs. RunHost and NodeHost are declared interfaces without shared
  concrete constructors. RunInput still lacks granted runner identity, and no
  plan/eval execution-input contract exists. Source authority, delegated routes,
  job hashes, storage bindings and generator replay need those shared adapters.
- W9: client.NewEngineClient is a panic stub. Existing W8 compositions retain
  HTTP/api.sock transport and separate bearers. Final runner/Cloud/CLI factories
  and the recorded launch/plan parser repairs remain open.
- W1/W2: EngineController.SyncLauncher is a panic stub. Plan/eval resource awards,
  local store activity/recovery/queue/maintenance adapters and shared policy
  handlers must implement their existing interfaces. W8's LocalRuntime and
  launcher components use fakes for those boundaries.
- W3/W7: selected-engine Go installation needs the requested workspace-off
  binary-cache hashing operation. The existing cache-key function still reads
  ambient go.work. No second hasher or process-global environment mutation was
  introduced.
- T1/lead: native HTTP/api.sock, Windows/macOS, deployed network probes, node
  cleanup, Docker/scratch accounting, full placements and real-repository QA
  remain unprovided. No native acceptance cell was enabled.

These repeat existing contract requests. No new foreign contract change or
product decision is requested. Shared Markdown touched is contracts/handoffs.md.
Other edits stay in internal/wingd and W8's reports. The lead retains independent
review, commits and native verification.

## Verification

Commands run with GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4 and
GOCACHE/GOTMPDIR/TMPDIR under /tmp/sw-astra-W8. Go commands use go -C with this
worktree. Tests use -p 4.

| Check | Result |
| --- | --- |
| go build -p 4 ./... | Passed. |
| go vet -p 4 ./internal/wingd ./internal/wingd/client ./internal/runners/... ./internal/engine/... ./internal/cluster ./tests/acceptance ./internal/acceptance | Passed. |
| go test -p 4 ./internal/runners/... ./internal/engine/... ./tests/acceptance ./internal/acceptance -count=1 | Passed. Acceptance components took 32.662 seconds. |
| go test -race -p 4 ./internal/wingd -run '^(TestLocal(Runtime\|HTTP\|Dashboard\|Control)\|FuzzLocalHTTP)' -count=1 | Passed after the final test edit. |
| go test -p 4 ./internal/wingd -run '^$' -fuzz '^FuzzLocalHTTPIdentity$' -fuzztime=100x -parallel=2 | Passed 100 generated executions. |
| go run ./internal/commentcheck -base main . | Clean across 1,191 Go files. |

Friction: selecting the new synctest regression alone initially exposed Go's
process-wide signal goroutine starting inside its bubble. The test initializes
signal delivery outside the bubble before starting the real runtime. Its next
run reproduced the intended activity failure. No production workaround was added.
Overwing discovery worked and reported its installed binary behind source.
git diff --check passes. Git config has no local identity or core.worktree
override. No retained W8 test processes were found. Worker scratch was removed.
