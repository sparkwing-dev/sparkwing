# W8 runner metrics and bootstrap cut

Continuation from `be8222a7d`. The three priority decisions and earlier legacy
cuts were already implemented. This session preserves them and removes another
unused bootstrap composition layer.

`RunnerCLI` now binds `--metrics-addr` before the unified managed loop claims.
An invalid address previously reached the claimant. The new regression failed
on that behavior and passes after the change. A transparent controller adapter
counts claimed, empty and error results, preserving the exact award and error,
including an award returned alongside an error. Metrics and runner lifetimes
share cancellation. Listener failure cancels and joins the runner. Runner exit
joins the HTTP serving and shutdown workers. Empty addresses bind nothing.
The existing HTTP and shutdown budgets are retained.

Removed `StartMetricsListener`, whose only callers were tests. The CLI now owns
its listener. Its old native socket test is replaced with HTTP over `net.Pipe`
through the composed runner/metrics owner. The full cluster suite now runs in
this sandbox without excluding a metrics-listener test. The node-duration
histogram remains registered, but it still needs the committed node-outcome
integration. A returned host error is insufficient to infer the final verdict.

`BootstrapRuntime.Run` now parses the container grammar directly before reading
and checking the private award. Removed `RunBootstrapCLI`, the cluster callback
type `BootstrapHost`, and all of `internal/cluster/bootstrap_cli.go` (44 lines).
The runtime's service bindings remain. Existing grammar fixtures now enter the
runtime with a real private award and stop at fake source/host services. The
acceptance fixture changes only the mounted bootstrap path to its temporary
file. The Job's other generated selectors and paths reach the same runtime.

Also removed the unused `cluster.Main`, `wingd.ProcessCapacity`,
`LocalHTTP.ServeDashboard`, and duplicate `CloudJobResources.ClaimDeadline`.
`JobDeadline` remains the single computed value. The dashboard fixture uses the
same serving function as the runtime. The daemon's lock-taking `idleElapsed`
helper moves into its tests because production only uses the locked operation.

## Priority, amendments and remaining integration

- Lead item 5: `LocalController.HTTP() LocalHTTP` remains implemented. W2 still
  supplies the authorized API/dashboard handlers. The runtime owns cancellation,
  drain, connection activity and joining.
- Lead item 6: the existing `--docker-image` acceptance and `--deadline` refusal
  tests pass. This session adds no launcher flag or execution timeout.
- Lead item 10: W8 still sends the operational kind/ID, holder and live claim
  generation with the actual Job namespace/name/UID and node UID. Eval source
  generation stays distinct from its live lease generation.
  **A1-38 remains open end to end.** `LauncherSyncReader.ReadLauncherSync` has
  no production implementation. `EngineController` calls that interface and
  strips response bearers, but cannot supply transactional decisions itself.
- The cluster worker, warm-pool dispatcher, run-host Kubernetes dispatcher and
  old W8 pod builders remain absent. Their earlier deletions are not counted
  again. `launcher.BuildJob` is the only production Kubernetes Job/pod builder.
- The four dead catalogs assigned at 09:40 remain absent. No lead-owned handoff,
  amendment, scenario registry or acceptance catalog changed.
- All other W8 amendment entries retain the implementation and open integrations
  recorded in `W8-ADMISSION-COMPOSITION-CUT.md`. No completed mechanism was rebuilt.
  The metrics and bootstrap changes advance A1-39's unified runtime composition.

The outstanding real integrations are concrete:

1. W1/W2: transactional launcher synchronization and policy-resolved plan/eval
   resource awards. `engine_claim.go` still clears non-node `Execution`.
2. W1/W2: local activity projection, readiness/API-key state, authorized HTTP
   handlers, provisioning and claimant enrollment over `localcontroller.Binding`.
   The store exposes no typed local activity projection. The existing `State`
   interface remains the boundary. No second SQL projection was added in W8.
3. W3: remote native-store adapter, Cloud plan/eval lifecycle and adoption of the
   init container's checkout. Native service binders still need process, policy,
   log, storage and committed node-metric observation integration.
4. W8 with those owners: bind the executable factories. The real wire client is
   implemented, but `cluster.MainWithVersion` still starts with an empty Runtime,
   and the CLI's `resolveLocalServices` still returns unavailable. Component
   factories exercise the declared boundaries instead of bypassing them.
5. W8/T1/lead: deployed `KubernetesBoundary.Probe` and `Clear`, enforced egress,
   exclusive nodes, scratch accounting, teardown, native Windows/Darwin/Docker
   placement and real-repository QA. No native cell is claimed by fake drivers.

No foreign contract signature changed and no new foreign contract is requested.
The local API hard cuts above have no foreign production callers. Shared files
migrated are `tests/acceptance/bootstrap_w8_test.go` and
`tests/acceptance/launcher_w8_test.go`. The source notes changed in runner hosting
and Cloud Jobs. W9 owns public docs and final executable composition.

## Lines and assumptions

Production Go adds **80** lines and deletes **116** relative to the starting
HEAD. **Net production lines deleted: 36.** Tests and Markdown are excluded.
Against `main`, the lead's W8 area metric is **+4,360**, which is **6,660 over**
the -2,300 budget. W8 remains incomplete and its overage still needs lead review.
The previous justification for the signed engine resolver and native local
lifecycle remains. This session reduces total production size while connecting
an existing metrics option.

The added mechanism is a private claimant decorator and a joined metrics/runner
owner. Calling the old listener independently was insufficient because it had no
production caller and did not join its serving goroutine on cancellation.
Removing the two private helpers and restoring the old RunnerCLI call undoes
this integration. There is no new public setting, variable, service, dependency,
trust root or customer restriction.

## Verification

All Go commands used `GOWORK=off`, `GOFLAGS=-buildvcs=false`, `GOMAXPROCS=4`, at
most four package workers, `GOCACHE=/tmp/sw-gocache` and temporary directories
under `/tmp/sw-astra-W8`.

- Whole-repository `go build -p 4 ./...` passes on the final production source.
- `go vet -p 4` passes for cluster, launcher, wingd, localcontroller and acceptance.
- Full cluster, launcher and localcontroller suites pass.
- Local controller/runtime/lifecycle/HTTP tests and property seeds pass.
- The existing `W8|LauncherCloud|LauncherController|LauncherPolling` acceptance
  selection passes, including the migrated bootstrap/deadline fixtures.
- Metrics/runner composition race checks pass. They cover bind refusal, HTTP
  serving, both stop directions, preserved claim results and joined shutdown.
- The final bootstrap/runner/metrics and W8 acceptance selection passes after
  removing the callback API.
- Commentcheck against main passes across 2,124 Go files. Diff whitespace passes.

No new native acceptance cell or historical T1 inventory port is closed. The old
metrics listener test is migrated, rather than counted as a new acceptance cell.
Native sockets and deployments remain untested. No Sparkwing executable,
pipeline, release, deployment or Git write ran. Git has no local identity or
core.worktree override. Overwing reports its binary behind source, but discovery
and card reads worked. The lead retains independent review, commits and native
verification. Worker scratch is removed after checks, and the shared cache stays.

STATUS: CONTINUE
