# W8 separate local consumer retirement

Continuation from `2550e1624`. The cluster worker, warm-pool dispatcher,
run-host Kubernetes dispatcher and old pod executor were already removed.
This session removes the separate local trigger-consumer process.

`orchestrator.ServeConsumer` and its dashboard contention entry point are gone,
including their election, store opener, dispatch coordinator, cancellation
settlement and heartbeat loop. `runs consumer` and `__runs-consume` are gone.
Detached and scheduled startup callers use the local-controller lifecycle.
Startup checks the captured home, preserves errors and requires a running owner.
Detached submission closes its store before requesting controller startup.
Submission receipts drop the removed process's PID and start-time fields.
The unused `localws.Run` dashboard opener is removed too. HTTP handler tests
retain a fixture server. Standalone CLI child execution remains in use.

Production Go adds **67 lines** and deletes **1,259 lines**.
**Net production delta: -1,192 lines.** Tests, testdata, acceptance and contract
files are excluded. All changes are shared cleanup: orchestrator -736,
CLI -285 and localws -171. W8-owned production Go has a zero delta.

The lead's directory-based accounting still measures W8 at **+4,169 lines
against main**, **6,469 over** its -2,300 budget. Its directory deltas remain
cluster -1,971, runners +1,521, wingd +2,398 and engine +2,221. This session
claims no budget waiver. The area remains incomplete.

## Ports and verification

The retirement map is appended to `contracts/t1-port-inventory.md`.
Checkout selection and log-path assertions survive in `local_submission_test.go`.
The consumer-specific process/election/heartbeat fixtures retire in favor of
the final lifecycle, shared keeper, host and real-store composition suites.
Native detachment, restart and placement remain acceptance work.

New tests check captured lifecycle options, wrong-home refusal, cancellation
before service resolution, missing services, startup error preservation,
readiness and removal of the consumer dispatcher. Existing lifecycle and
composition tests check elected ownership, reconciliation, activity holds,
claim execution, cancellation and joined store close.

Invariant: a local startup never starts a second owner or executes a
standalone request against a foreign owner's home. The existing
`FuzzLocalProcessLifecycleKeepsOwnership` passes **11,993 executions**.
Final environment snapshot, claimed-host acquisition, setup-failure and
cancellation ports pass too.

Checks use GOWORK=off, GOFLAGS=-buildvcs=false, GOCACHE=/tmp/sw-gocache,
GOTMPDIR/TMPDIR=/tmp/sw-astra-W8, GOMAXPROCS=4 and package concurrency at most four.

- Whole-repository `go build ./...` passes.
- Full runners, launcher, runner-service, cluster, engine and localcontroller
  suites pass.
- Focused CLI startup/surface, local runtime/lifecycle/control/dashboard/drain,
  snapshot, retained checkout/log and final claimed-host ports pass.
- W8, LauncherCloud and native-store owner acceptance components pass.
  **No native placement cell is newly closed.** Their foreign service boundaries
  still use fixtures.
- Focused lifecycle and composition suites pass under the race detector.
- Focused vet and commentcheck pass. Diff whitespace checks pass.
- All changed shared Go packages compile, including their retained tests.
  Socket-free localws mux/address tests and CLI drift checks pass.

Native API socket tests cannot bind in this sandbox (`setsockopt: operation
not permitted`). Localws handler/version test servers also cannot bind TCP.
Those attempts are unavailable evidence. The full legacy CLI/orchestrator and
localws suites are not claimed green. Several retained legacy fixtures still
use the preceding schema and execution path. No runtime or pipeline was started.

## Decisions, amendments and remaining integrations

07:40 item 5 remains implemented: `LocalController.HTTP()` returns `LocalHTTP`.
W2 must supply production peer-authorized API and dashboard handlers.
Item 6 remains implemented: launcher accepts `--docker-image` and `--poll`,
and rejects `--deadline`. Full cluster tests pass.
Item 10 remains implemented at W8's boundary. Launcher observations carry
operational kind/ID, holder, live claim generation, Job namespace/name/UID and
node UID. Eval source generation stays separate from its live expansion lease.
**A1-38 remains open.** `LauncherSyncReader.ReadLauncherSync` still has only
test implementations. W1/W2 must provide transactional ancestry, live-award and
Job-instance correlation. 09:40 item 4 remains done: the four named dead
contract declarations have no Go declarations or callers.

A1-39/40/41 advance through the separate consumer and dashboard opener cuts.
W9's `resolveLocalServices` remains a stub. Final sealed local intake and
schedule submission remain W9 work. Their retained legacy submission code still
accesses the store directly, so this cut does not establish sole ownership for
those entire command paths. The elected localcontroller composition already
uses the real store and shared queue with fake foreign services. W1's activity
projection and W2's HTTP binding remain necessary.

Existing A1-08/09/10/11/12/18/19/26/27/36/46/48/51/55 components remain in place.
Native remote-store, source authority, per-process delegation, per-attempt
storage and CLI factory bindings remain. W3 still needs Cloud plan/eval native
entry points and adoption of the init container's checkout. The orphan legacy
repository-admission helpers remain until the machine source-authority binding
preserves that boundary. Trigger authority cannot replace a controller-owned
plan/eval claim.

A1-20/49 need production enrollment/provisioning and failed-enrollment revocation.
A1-42 needs deployed Probe/Clear, default-deny network, aggregate scratch and
complete teardown evidence. A1-43/52 retain native Windows packaging and process
inheritance obligations. A1-45/50/53 retain final CLI composition and native QA.
No foreign contract declaration changes are requested or made in this cut.

## Shared files and friction

Shared production files changed are `cmd/sparkwing/crons.go`, `help_registry.go`,
`main.go`, `run_detached.go`, `run_dispatch.go`, the new `local_start.go`,
`internal/orchestrator/submission_environment.go` and `pkg/localws/localws.go`.
Shared production files deleted are `cmd/sparkwing/runs_consumer.go` and
`internal/orchestrator/trigger_consumer.go`.

Shared tests changed are `cmd/sparkwing/crons_test.go`,
`run_detached_process_test.go`, `run_risk_gate_test.go`,
`internal/orchestrator/lapsed_lease_repro_test.go`, `local_trigger_loop_test.go`,
`pkg/localws/localws_test.go` and `origin_test.go`. New tests are
`cmd/sparkwing/local_start_test.go` and
`internal/orchestrator/local_submission_test.go`. Removed tests are the old
`run_detached_daemon`, `trigger_consumer`, `trigger_consumer_retry_interval`,
`trigger_consumer_comment`, `hosted_consumer_e2e`, `retry_environment`, localws
example and listener-ownership files. Other detached process assertions remain.

Documentation changes are `internal/wingd/LOCAL-CONTROLLER.md`,
`docs/migrations/_unreleased.md` and the port inventory. Lead-owned handoffs and
amendments are untouched. No new runtime service, interface, environment variable
or compatibility path was added. The startup adapter uses the existing lifecycle.

Overwing reports its binary behind source, but matching and reading standards
work. Sandbox socket restrictions prevent native server checks. Git has no
local identity or core.worktree override. No Git write was attempted. Worker
scratch is removed at the end. The shared build cache is retained.

STATUS: CONTINUE
