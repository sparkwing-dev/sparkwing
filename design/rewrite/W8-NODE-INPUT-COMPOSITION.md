# W8 node-input composition continuation

The approved cancellation redesign is in production at decce44f0. Both retained
classification regressions pass. Its stop path remains 84 -> 75 physical lines.
The pending wait is 44 -> 40, and loop.go is 408 -> 401. This continuation does
not change that completed code.

## Implemented

Invariant: every node host receives the awarded accepted spec, plan, job settings,
engine, runner and claim fence, regardless of preparation replacements or later
mutation of the award's nested values.

Hosting captured engine/runner/fence before callbacks, but accepted the prepared
NodeInput's seals and settings. NodePreparation projected those separately. The
existing node-input constructor now owns the complete awarded projection and
captures it before renewal or preparation. It uses the same seal validation as
trigger/here inputs. Missing job settings and inconsistent seals fail before
renewal. Nested execution settings are copied through their existing JSON shape.
Source, storage, delegated routes, job hash and generator replay remain prepared
host services.

FuzzHostingNodeInputKeepsAwardSeals failed both seeds before the source change.
It replaces prepared inputs, mutates accepted bytes/maps and nested settings at
renewal, and checks isolation from executor mutations. The acceptance identity
component and its property also check awarded seals/settings over forged inputs.

RunnerStoreLifecycleW8Components now constructs NewManagedRunner with the real
engine resolver and a signed version-index fixture. The constructor shares the
transport between separate claimant and award clients, then executes the unified
loop, Hosting and NodePreparation through real EngineController/SQLite claims,
renewals, start and completion. It preserves ordinal zero until start establishes
one. HTTP clients, source acquisition, keeper and executors remain contract fakes.

The production diff adds 31 and deletes 15 physical lines across execution.go,
hosting.go and preparation.go. It removes NodePreparation's duplicate accepted
input/settings projection. The acceptance component removes its manual Loop and
policy bypass. No whole legacy file is deleted in this pass. Earlier cluster,
dispatcher, daemon and socket-client deletions remain in place.

No exported API, foreign Go contract, environment variable, setting, retry,
timeout or compatibility path is added. No foreign production caller changed.
The existing input constructor owns the added fields. Reverting this diff
restores the separate preparation projection.

## Amendment and handoff disposition

All 26 amendment rows assigned to W8 retain the dispositions in
[the amendment audit](W8-AMENDMENTS-1.md). A1-18/19/39 advance through awarded
node-input construction and the managed-runner/SQLite component. The two
pending-claim design approvals remain complete. Every W8 handoff was rechecked.
No new T1 inventory port or native catalog cell is claimed.

Real-integration points remain open:

- W3 source.ForRequest/ForExecution/Acquire, Entry.Close, Keeper.Run and
  CancelError.Error are panic stubs. Shared RunHost/NodeHost constructors,
  run/plan/eval runner inputs, plan/eval execution operations, delegated routes,
  shared process construction and generator replay remain required. The default
  W8 paths call the declared APIs. Components fake those boundaries.
- W9 NewEngineClient/NewDataClient are panic stubs. Final CLI/runner/bootstrap
  factories and the recorded launch image/deadline and plan-without-job grammar
  repairs remain required.
- W1/W2 must supply elected local store/queue/maintenance construction and shared
  policy handlers. PolicyRouter and EngineController.SyncLauncher remain stubs.
  Plan/eval resource awards remain required.
- W3/W7/W9 must finish bound byte/record storage and per-attempt construction.
  W3/W7 still own the recorded workspace-off binary-cache hashing request.
- T1/lead retain native HTTP/api.sock, Windows/macOS, deployed Cloud boundary,
  cleanup/scratch evidence, actual SDK hello, full placements and real-repo QA.

No new foreign contract change or human decision is requested. Existing requests
remain open. Shared files touched are contracts/c3-protocol-sdk.md,
contracts/handoffs.md and tests/acceptance/{engine_w8_test,
runner_identity_w8_test,runner_store_w8_test}.go. The shared fixture now supplies
the sealed hashes, plan and execution settings required by the node contract.
Source documentation changed in internal/runners/launcher/CLOUD-JOBS.md.

## Verification

Commands use go -C with this worktree, GOWORK=off, GOFLAGS=-buildvcs=false,
GOMAXPROCS=4 and GOCACHE/GOTMPDIR/TMPDIR under /tmp/sw-astra-W8. Tests use -p 4.

| Check | Result |
| --- | --- |
| go build -p 4 ./... | Passed over the whole repository after the production change. |
| W8 and acceptance go vet -p 4 | Passed. Includes runners/..., engine/..., cluster, wingd/..., tests/acceptance and internal/acceptance. Acceptance was checked again after final assertions. |
| Full runners/..., engine/..., tests/acceptance and internal/acceptance suites | Passed. Both original classification regressions and all property seeds pass. |
| Final full tests/acceptance suite | Passed in 24.377 s after the final test assertions. |
| Focused runner/acceptance race suite | Passed. Covers Hosting, NodePreparation, awarded seals/identity, managed-runner/SQLite and both classification regressions. |
| FuzzHostingNodeInputKeepsAwardSeals, fuzztime=1000x, parallel=2 | Passed 1,000 generated executions. |
| FuzzRunnerExecutionIdentityW8, fuzztime=1000x, parallel=2 | Passed 1,000 generated executions on the final acceptance property. |
| Local runtime/HTTP/dashboard/control selection | Passed with in-memory listeners. |
| Cluster suite | Passed excluding TestStartMetricsListener_ServesMetrics, which needs native sockets. |
| commentcheck -base main . | Clean across 1,192 Go files. |
| git diff --check | Passed. |

The lead retains independent review, commits and native verification. Worker Git
writes, Sparkwing binaries, pipelines, deployment and release were not used.
Overwing discovery worked while reporting its installed binary behind source.
Git config has no local identity or core.worktree override.
All check sessions finished. The worker scratch directory was deleted.

STATUS: CONTINUE
