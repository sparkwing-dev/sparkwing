# W8 native preparation cut

Continuation from `7a4460d5b`. Deleted `runners.NodePreparation` and its private
acquisition helper because their only callers were tests. The shared
`nodehost.ClaimedNodeHost` already owns source acquisition and renewal and
rejects pre-acquired entries. Keeping both paths would retain two owners for
the same lifecycle.

Migrated the sole external caller in
`tests/acceptance/runner_store_w8_test.go` to return its fixture executor,
prepared input and cleanup directly. That component still proves managed-loop
claiming, awarded identity, renewal, persisted start/completion and joined
cleanup. It no longer simulates acquisition through a second production layer.
The existing native-store component still exercises `StoreHosting`, the shared
native hosts, real persistence and source/lease ownership.

Deleted the three tests of the removed adapter, including its foreign-route
property. They exercised an API with no production caller. Native process
authority and source validation remain in the shared hosts. No historical T1
port or native placement cell is newly closed.

Updated only W8's integration paragraph in `contracts/c3-protocol-sdk.md` to
name the native acquisition owner. This removes a W8-local helper contract.
No foreign Go interface changed. Lead-owned handoffs and amendments are untouched.

## Priority and amendment status

- Lead item 5 remains implemented as `LocalController.HTTP() LocalHTTP`.
  W2 still supplies the authorized API/dashboard handlers and local state.
  The elected runtime owns cancellation, draining and joining.
- Lead item 6 remains implemented. The existing launcher tests accept
  `--docker-image` and refuse `--deadline`.
- Lead item 10 is complete on W8's wire side. Observations carry the operational
  unit kind/ID, holder, live claim generation, Kubernetes Job namespace/name/UID
  and node UID. Eval source generation stays separate from its claim generation.
  **A1-38 remains open end to end.** `EngineController.SyncLauncher` calls
  `LauncherSyncReader`, which still has no production transactional binding.
- The cluster worker, warm-pool dispatcher, run-host Kubernetes dispatcher and
  old W8 pod builders remain deleted. Their earlier deletions are not counted
  again. `launcher.BuildJob` remains the production Cloud Job builder.
- A1-39 advances by removing duplicate acquisition composition. Other W8
  amendment entries retain the implemented boundaries and open integrations
  recorded in `W8-ADMISSION-COMPOSITION-CUT.md` and
  `W8-RUNNER-METRICS-CUT.md`. No completed mechanism was rebuilt.

## Remaining real integrations

1. W1/W2 must bind transactional launcher sync to current authority and award
   fences and supply policy-resolved plan/eval resources. Non-node claims still
   lack the execution resources the launcher requires.
2. W2 must supply production local readiness/activity, authorized API/dashboard
   handlers, provisioning and claimant enrollment over `localcontroller.Binding`.
   W1 still needs a typed local activity projection and incompatible-store error.
3. W3 must supply the remote `NativeHostStore` adapter, Cloud plan/eval lifecycle
   and initialized-checkout adoption. W9/W2 must align source credentials.
4. W8 and the deployment harness still need real `KubernetesBoundary.Probe` and
   `Clear` bindings and deployed isolation, aggregate scratch and teardown proof.
   Native Windows/macOS, Docker and real-repository QA remain open with T1/lead.
5. The node-duration metric still needs a committed-outcome observation from
   the host integration. A host return error cannot identify the job verdict.

Component fakes establish the existing boundaries. They do not close these
production integrations. No new foreign contract request or design decision is
introduced by this deletion.

## Lines and verification

Against starting HEAD, production Go adds zero lines and deletes 76.
**Net production lines deleted this session: 76.** The removed adapter's tests
delete another 123 lines. Tests and Markdown are excluded from the production
count. Against `main`, the lead's W8 metric is **+4,284**, or **6,584 over** the
-2,300 budget. W8 remains incomplete. The previously reported overage from
signed engine distribution, upgrade/service management and local process/HTTP
lifecycles still needs lead review. This session adds no behavior or mechanism.

All checks used `GOWORK=off`, `GOFLAGS=-buildvcs=false`, `GOMAXPROCS=4`, at most
four package workers and the shared `/tmp/sw-gocache`.

- Whole-repository `go build -p 4 ./...` passes.
- Full `internal/runners/...`, `internal/cluster` and
  `internal/wingd/localcontroller` suites pass.
- The existing `W8|LauncherCloud|LauncherController|LauncherPolling` acceptance
  selection passes. This includes native-store/local-controller components,
  launcher reconciliation, bootstrap transport and runner lifecycle evidence.
- Focused vet passes for those packages and acceptance.
- Race checks pass for native-store hosting, the migrated store lifecycle,
  Hosting's seal/identity properties and local-controller composition/fencing.
- Commentcheck against main passes across 2,122 Go files. Diff whitespace passes.

Shared files touched are the acceptance fixture and W8's paragraph in
`design/rewrite/contracts/c3-protocol-sdk.md`. All other edits are W8-owned.
No Sparkwing binary, pipeline, deployment, release or Git write ran. Overwing
reported its installed binary behind source but matching and card reads worked.
Local Git configuration has no identity or core.worktree override. Scratch is
removed and the shared cache is retained. The lead owns review and commits.

STATUS: CONTINUE
