# W8 launcher composition continuation

The approved cancellation redesign is committed. Both retained classification
regressions pass. Counting `decce44f0`'s first parent against HEAD confirms
84 -> 75 physical lines across the pending wait, policy helper/classifier and
their three call sites. The pending wait is 44 -> 40 and loop.go is 408 -> 401.
This continuation leaves that completed production code unchanged.

## Implemented

Invariant: every runnable Cloud Job and private bootstrap carries one canonical
operational-unit binding. Plans have no job or expansion source. Eval identifies
its terminal source job and generation. Work has no expansion source.

The acceptance property reproduced three invalid shapes accepted by BuildJob:
a plan with a job, an eval with a different source job and work with an expansion
source. The existing award validator now refuses these shapes before reserving
a node. The private award reader consumes that same validator. No extra parser,
API, callback, setting, timeout or compatibility path was added.

FuzzCloudAwardUnitBindingW8 generates unit kinds, job/source IDs and generations.
It checks builder refusal before reservation and private-file decoding through
ReadBootstrapAward. Its strings are projected to valid UTF-8 because its inputs
represent decoded JSON strings. An initial round-trip assertion exposed JSON's
replacement of raw invalid UTF-8 from the Go fuzzer. The generator now normalizes
those bytes and retains that input as an explicit seed. This changed the test
input domain, without changing production's JSON handling.

TestLauncherKubernetesBoundaryW8Components now reconstructs NewLauncher and its
ControllerControl over the same HTTP transport and launcher bearer. All three
unit kinds pass live sync and cancellation cleanup through real reservation and
finalizer code. The controller client and SyncLauncher remain contract fakes.
The test refuses any attempt to claim a replacement award after restart.

The shared Cloud fixture now uses the wire contract's plan/eval binding shapes.
The old bootstrap-test normalization is deleted. Pod assertions require --job
only for eval and work. This exposes the recorded W9 plan-parser gap instead of
concealing it behind an invented plan job.

Production adds 6 and deletes 3 physical lines. Tests delete 5 obsolete fixture
normalization lines. No whole legacy production file is deleted in this pass.
The earlier cluster, dispatcher, daemon and socket-client cuts stay deleted.
Their remaining replacements still need the foreign construction below.

## Amendment and handoff disposition

All 26 amendment rows assigned to W8 retain the dispositions in
[the amendment audit](W8-AMENDMENTS-1.md). A1-09/38/42 advance through runtime,
wire-sync and real Kubernetes-boundary composition. The pending-claim approvals
are complete, and their obsolete pending-decision paragraphs are removed from
contracts/handoffs.md. No additional T1 inventory port or native catalog cell is
claimed. The full acceptance package includes the passing W8 components.

Real integration remains open at these boundaries:

- W3 source.ForRequest/ForExecution/Acquire/Entry.Close and lease.Keeper.Run/
  CancelError.Error are panic stubs. Shared RunHost/NodeHost constructors,
  run/plan/eval identity inputs, delegated routes, bound storage, generator replay
  and the shared process constructor remain required. Existing W8 defaults call
  the declared APIs. Components use fakes at those boundaries.
- W9 NewEngineClient/NewDataClient are panic stubs. Final CLI/runner/bootstrap
  factories and the recorded launch image/deadline and plan-without-job grammar
  repairs remain required.
- W1/W2 elected local-controller/store/queue/maintenance and shared policy-handler
  construction remain required. EngineController.SyncLauncher is a panic stub.
  Plan/eval resource awards remain open.
- W3/W7 still need the recorded workspace-off binary-cache hashing operation.
- T1/lead still need native HTTP/api.sock, Windows/macOS, deployed Cloud network,
  cleanup and scratch evidence, SDK hello, full placements and real-repository QA.

No foreign contract change or new product decision is requested. No production
caller outside W8 changed. Shared files touched are contracts/handoffs.md and
tests/acceptance/{bootstrap_w8_test,launcher_boundary_w8_test,
launcher_controller_w8_test,launcher_w8_test}.go. Source documentation changes in
internal/runners/launcher/CLOUD-JOBS.md. Public docs and changelog have no impact
because this enforces the existing internal award contract without changing
customer CLI, configuration or wire shapes.

The lead retains review, commits and native verification. Worker Git writes,
Sparkwing binaries, pipelines, deployment and release were not used.

## Verification

Commands used go -C with this worktree, GOWORK=off, GOFLAGS=-buildvcs=false,
GOMAXPROCS=4 and GOCACHE/GOTMPDIR/TMPDIR under /tmp/sw-astra-W8. Tests use -p 4.

| Check | Result |
| --- | --- |
| go build -p 4 ./... | Passed over the whole repository after the production edit. |
| go vet -p 4 on runners/..., engine/..., cluster, wingd/..., tests/acceptance and internal/acceptance | Passed. Changed launcher and acceptance packages were checked again after final test edits. |
| Full runners/... and engine/... suites, tests/acceptance and internal/acceptance | Passed. Both classification regressions and their property seeds pass. |
| Final launcher and full acceptance suites | Passed. Acceptance took 50.527 seconds. Includes the new unit-binding seeds and the constructor/wire/Kubernetes restart path. |
| Focused local runtime/HTTP/dashboard/control race suite | Passed. |
| Final launcher-construction/restart and unit-binding property race selection | Passed. |
| FuzzCloudAwardUnitBindingW8, fuzztime=1000x, parallel=2 | Passed 1,029 executions on the final test. |
| Cluster suite | Passed with TestStartMetricsListener_ServesMetrics excluded because it requires native sockets. |
| go run ./internal/commentcheck -base main . | Clean across 1,192 Go files. |
| git diff --check | Passed. |

The initial binding seeds failed before the validator change. The first broad
Cloud run then exposed the fixture's old unconditional --job assertion. It now
checks the declared owner/job distinction. Neither failure was skipped.

Native sockets, deployed Cloud probes, native Windows/macOS and real repository
QA remain unexecuted. Overwing discovery worked while reporting its installed
binary behind source. Git config has no local identity or core.worktree override.
All check sessions finished. The worker scratch directory was removed.

STATUS: CONTINUE
