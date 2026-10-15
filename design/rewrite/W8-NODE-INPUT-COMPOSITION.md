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

## Integration checkpoint

The continuation starts from 18286f13a and changes only this report. The
cancellation implementation is already present. Both original regressions and
the interruption property's seeds pass. Counting the three policy call sites
and their helpers confirms 84 -> 75 physical lines. The pending wait remains
44 -> 40 and loop.go remains 408 -> 401.

All 26 assigned amendment rows and the W8 handoffs retain the dispositions above.
The declared source, keeper and controller-client defaults are already called
by W8 production code and exercised through contract fakes. Further composition
requires these existing foreign contract requests to be completed:

- W3 must declare shared RunHost/NodeHost construction, run/plan/eval identity
  inputs and plan/eval operations. Bound routes, storage and generated-code
  replay still need their shared constructors.
- W1/W2 must provide the local store activity/recovery projection and the shared
  controller, queue and maintenance composition consumed by LocalRuntimeConfig.Open.
- W9 must resolve the recorded launcher image/deadline and plan-without-job
  grammar, plus the source-init/runtime handoff. W3/W7 must expose workspace-off
  binary-cache hashing.

The source and keeper bodies, EngineClient/DataClient and SyncLauncher remain
their owners' panic stubs. Native placement, platform, Cloud enforcement/cleanup and real-repo
evidence remain open. No foreign contract was changed.

Whole-repository build, W8/acceptance vet, full runners/... and engine/... suites,
both acceptance packages, focused cluster/local-runtime/client suites and
commentcheck pass. Acceptance takes 61.203 seconds. Commentcheck covers 1,192
files. Tests use -p 4 and the temporary cache settings above. The focused local
selection uses in-memory listeners. No native catalog cell was activated and no
test was newly ported.

Production changes, deleted legacy lines/files, shared-file edits and foreign
caller migrations are all zero in this continuation. The earlier deletions
remain in place. No new mechanism or product decision is introduced. No Git
write or Sparkwing binary ran. The worker scratch directory is removed after
checks. The lead retains commits, independent review and native verification.

STATUS: CONTINUE
