# W8 shared dispatcher retirement

Continuation from `077d326cb`. The cluster worker, warm-pool dispatcher,
run-host Kubernetes dispatcher and old orchestrator pod builder were already
deleted. This cut removes their remaining shared executor and scheduler
dispatch mode.

`nodehost.HostDispatchedNode`, pod reconstruction, pod-specific layering,
row polling and result adoption are deleted. `Config.Dispatch`, `Resume` and
`ClaimedRowPoll` had no production caller and are removed. The local planner
retains its behavior in `plan_document.go`. The local scheduler and admission
remain W3's runtime migration work.

Production Go adds 91 lines and deletes 354 lines, including the retained
planner's move. **Net production delta: -263 lines.** All production changes
are shared W3 cleanup. W8-owned production has a zero delta this session.
Tests, testdata, acceptance and contract declaration files are excluded.

W8 remains **+4,169 production lines against main**, **6,469 over** its -2,300
budget. The measured directory deltas are cluster -1,971, engine +2,221,
runners +1,521 and wingd +2,398. The signed engine distribution, managed runner
and local lifecycle additions remain substantial. This report does not establish
that nothing smaller works and requests no budget waiver. W8 is incomplete.

## Ports and acceptance

The replacement map is appended to `contracts/t1-port-inventory.md`.
New final-executor tests cover generated recovery with persisted failure,
exact nested ancestor order, one evaluation per ancestor, timeout during
replay, joined cleanup and a 70-generation lineage. The existing cycle and
adversarial lineage properties remain active.

Invariant: no registered secret appears in a returned execution error or a
transition payload, including failures outside the attempt or during output
publication. `FuzzNodeExecutorMasksOuterAndPublicationFailures` checks both
paths across the former headline truncation boundary. It passes 2,812 generated
executions. The scheduler's retained masking tests now use their text sink and
pass without the obsolete SQL row writer. State and logger tests call their
remaining masking owners directly.

The old dispatch fixtures are retired. The scheduler's resume/spec assertion
survives in `resume_drift_test.go`. Other retained scheduler fixtures use an
execution callback. Final owner, claimed-host and executor suites cover the
claim behavior. `Config.Resume`'s refusal test retires with that API.

W8 and LauncherCloud acceptance components pass, including the native-store
local-controller composition. **No native placement cell is newly closed.**
The new executor ports use scripted process and state interfaces. Native
generated recovery, retry and deployed placement remain QA obligations.

## Lead decisions and amendments

- 07:40 item 5 remains implemented. `LocalController.HTTP()` returns `LocalHTTP`.
  W2's production authorized API and dashboard binding remains open.
- 07:40 item 6 remains implemented. The launcher accepts `--docker-image` and
  `--poll` and rejects `--deadline`. Cluster and launcher tests pass.
- 07:40 item 10 is implemented on W8's wire boundary. Observations retain
  operational kind/ID, holder, live generation, Job namespace/name/UID and node
  UID. Eval source generation remains distinct from its live expansion lease.
  **A1-38 is not closed.** `EngineController.SyncLauncher` delegates to
  `LauncherSyncReader.ReadLauncherSync`, which has only test implementations.
  W1/W2 must supply the transactional authority, award and Job correlation.
- 09:40 item 4 remains done. The four named dead declarations have no Go
  declarations or callers.

A1-19/39/51 advance through retirement of the remaining dispatcher and pod
executor. Existing A1-08/09/10/11/12/18/26/27/36/46/48/55 components remain in
place. Their production remote bindings and native evidence remain subject to
the integrations below. The approved pending-claim and cancellation mechanisms
are unchanged and their runner tests pass.

A1-20/49 still need production enrollment, provisioning and failed-enrollment
revocation bindings. A1-40/41 have elected store ownership, reconciliation,
queue, maintenance, readiness, activity holds, drain and managed units. W1's
activity projection, W2's peer-authorized handlers and W9's foreground service
binder remain open. A1-42 needs deployed Probe/Clear bindings, network and
aggregate scratch enforcement, and full teardown evidence. A1-43/52 retain
the native Windows packaging and inheritance obligation. A1-45/50/53 retain
final CLI composition and native QA obligations.

## Contract changes and real integrations left

No foreign contract declaration changes in this cut. W3 still needs native
Cloud plan/eval entry points and adoption of the init container's checkout.
Trigger ownership cannot substitute for a controller-owned plan/eval claim.
The remaining boundaries are production remote NativeHostStore, source
authority, per-process delegation, per-attempt storage, runner/launcher CLI
factories, local State and HTTP services, and launcher synchronization.

## Verification and friction

Checks use GOWORK=off, GOFLAGS=-buildvcs=false, GOCACHE=/tmp/sw-gocache,
GOTMPDIR/TMPDIR=/tmp/sw-astra-W8, GOMAXPROCS=4 and package concurrency at most four.

- Whole-repository build passes.
- Full runners, launcher, service, cluster, engine and localcontroller suites pass.
- W8 and LauncherCloud acceptance components pass.
- Final owner, claimed-host, executor, lineage and retained masking tests pass,
  including the race detector.
- The masking property completes 2,812 fuzz executions.
- Focused vet and commentcheck pass. Git diff --check passes.

The broader legacy generated-fixture attempt cannot bind its route server in
the sandbox. Old scheduler SQLite fixtures also call CreateRun's removed
args_json schema path. Those are not final-host failures. The full legacy
nodehost suite is not claimed green. No Sparkwing binary or pipeline ran.
Overwing can match/read standards but reports its installed binary behind source.

Shared production files deleted are `internal/nodehost/dispatch.go` and
`podnode.go`. Shared production files changed/added are `scheduler.go` and
`plan_document.go`. Shared test files deleted are `dispatch_test.go` and its
pod-only cases in `generated_test.go`. Shared test files changed are
`generated_test.go`, `rehydrate_resume_test.go` and `masking_unix_test.go`.
Shared test files added are `executor_pod_ports_test.go` and `resume_drift_test.go`.
Documentation edits are `internal/runners/HOSTING.md` and the port inventory.
Lead-owned handoffs and amendments are untouched. Git has no local identity
or core.worktree override. Worker scratch is removed at the end and the shared
build cache is retained.

STATUS: CONTINUE
