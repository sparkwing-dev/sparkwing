# W8 local store composition

This continuation starts at db4cc36d2 with the integration merge in progress.

## Implemented

`internal/wingd/localcontroller.Config.Open` now supplies the elected local
runtime's real SQLite store owner. It opens the home's state.db once, selects
DefaultTeam and constructs EngineController over that same handle. Its shared
service binder supplies authorization handlers, state projection and queue
execution. Partial binding failures close services before the store and preserve
both errors. The runtime still owns election, listeners and admission drain.

The composition runs the real leased unit reaper and concurrency maintenance
before readiness. Expired unstarted local owners requeue. Started local orphans
fail, and that result survives reopening. It renews the reaper while serving.
Queue, activity observation and maintenance cancel and join together. Closing
the composition while those workers run is refused. Closing after they join
releases services and then the store, once.

Invariant: startup reconciliation cannot make the controller ready or start its
queue after losing its reaper fence. Both property seeds failed before the final
renewal was added. They now pass, along with 100 generated cases covering expiry
and replacement generations. The existing store remains the fencing owner.

Managed units already render `wingd run --stay --home ...`. The foreground CLI
now parses --stay and carries it to LocalStartOptions. The existing election
test uses that managed argument. The final factory is still unavailable in the
CLI because the shared service binder is not implemented.

The new W8-local contracts are Config, Binding, Services, State and Queue in the
composition package. No foreign Go contract changed. The State interface names
the still-missing local projections instead of adding W8 SQL or a second policy
router. Queue accepts the shared runner lifetime. HTTP uses the existing
LocalHTTP handlers and verified LocalPeer context.

## Merge and deletion

All 15 conflict paths have resolved working-file contents. The handoff file is
byte-identical to integration stage 3. W8's service leak test remains. The store
default-team caller inventory drops removed HeldRunStore methods and the obsolete
profile twin. No conflict markers remain.

The following 12 retired files stay deleted. Integration had modified them after
W8's earlier deletion. Their integration versions total 6,575 lines. This
continuation restores those deletions rather than claiming a second removal of
the same implementation.

- internal/cluster/runner_pool_cli.go and runner_pool_cli_test.go
- internal/cluster/trigger_loop.go
- internal/orchestrator/trigger_runner_factory.go
- internal/orchestrator/wingd_daemon.go
- internal/orchestrator/wingd_api_contention_test.go
- internal/orchestrator/wingd_api_routes_test.go
- internal/orchestrator/wingd_api_test.go
- internal/orchestrator/wingd_runstore_test.go
- internal/runners/k8s/dead_token_test.go, k8s.go and k8s_test.go

No additional legacy file is newly retired. Whole-repository compilation finds
no remaining production caller of these removed APIs. W3 still owns the retained
orchestrator host implementation and its fixtures. W7 owns the remaining cache
chart cut. Neither is reimplemented here.

Worker Git writes are forbidden by SWARM-BRIEF. The index therefore still lists
the 15 unmerged paths. The lead must stage these resolved contents and deletions
and finish the merge. No staging or commit was attempted.

## Amendment and handoff disposition

Every assigned amendment and W8 handoff was rechecked. Earlier completed work was
retained. This table supersedes earlier green acceptance claims for A1-18/38/39
because the merged store now requires an explicit source attempt.

| Row | Current W8 disposition |
| --- | --- |
| A1-08 | The single ordered claim loop remains implemented. Real HTTP and Actions enrollment wiring remain W2/W9 integration. |
| A1-09 | NewLauncher selects EngineOptions.Launcher. The shared client body remains a W9 stub. |
| A1-10 | Selected-engine SDK installation remains implemented. W3 source integration is open. |
| A1-11 | Awarded source and seals remain captured. Acquisition consumes W3's declared interface. |
| A1-12 | Retired private Git builders remain deleted. The shared source/procenv binding remains W3 work. |
| A1-18 | Ordinal zero remains preserved in W8 inputs. The merged store refuses the old wire transition because SourceAttempt is absent. W9/W3 must transport the captured ordinal. |
| A1-19 | Node engine, runner, seals and fences remain captured before callbacks. W3 still needs run/plan/eval identity inputs and real hello construction. |
| A1-20 | Enrollment and bootstrap use declared issuance interfaces. Production provisioning remains W2 integration. |
| A1-26 | W8 sends observed capacity and labels. The store derives registered identity. Production enrollment remains W2 work. |
| A1-27 | W1's billing joins are merged. W8 does not create accounting facts. The transition integration failure described below blocks the two W8 store components. |
| A1-36 | Host storage binding remains explicit. Per-attempt byte/record constructors remain W3/W7/W9 work. |
| A1-38 | Cloud award/bootstrap and batch sync composition remain implemented. Sync correlation and the controller/client bodies remain open. The store bootstrap component now fails at SourceAttempt. |
| A1-39 | The legacy dispatcher, cluster claim/source/heartbeat and daemon cuts survive the merge. Runner/launcher suites pass. Shared host, lease and client implementations remain foreign integration points. |
| A1-40 | Real local store ownership, startup reaping, concurrency maintenance and joined queue/activity/maintenance are added. Live state and policy handlers remain explicit service interfaces. |
| A1-41 | Managed argv remains final. The foreground --stay caller is repaired. W9's dashboard/update/doctor binder and native service proofs remain open. |
| A1-42 | Cloud isolation and scratch construction remain implemented. Deployed enforcement, probes and complete teardown still need T1/lead evidence. |
| A1-43 | Historical signed matrices, exact selection and fallback remain implemented. W9 owns notice presentation and upgrade composition. |
| A1-45 | W6's winner remains authoritative. W8 adds no scheduled-class inference. |
| A1-46 | Install paths retain the shared process environment and proxy masker. Their suites pass. |
| A1-48 | Captured input boundaries remain. Shared source/host startup and schedule construction remain open. |
| A1-49 | Private-file enrollment/startup and partial-failure cleanup remain implemented. Final issuer and host binding remain W2/W9 integration. |
| A1-50 | W8's engine install and local runtime interfaces remain available. Final CLI here/cache/publish/host factories remain W9 work. |
| A1-51 | W8's old v1 runtime paths remain deleted. Shared corpus and retained host fixtures remain with W3/W4/W5/W9. |
| A1-52 | Windows npm construction and historical matrices remain. Native Windows and packaged bridge evidence remain open. |
| A1-53 | Native catalog cells remain blocked. No native driver or real-repository QA is claimed. |
| A1-55 | Named here claim selection and approved independent stop/cancellation handling remain implemented. Runner suites pass. Native reclaim/ancestry/response-loss proof remains open. |

The approved pending-claim outcome and cancellation-classification handoffs remain
complete. That code is unchanged. The launcher synchronization handoff remains
open because LauncherObservation still lacks the immutable claim generation and
Kubernetes Job-instance correlation W1 requires.

## Integration requests and limits

- W1/W2 must bind localcontroller.State to the real local admission run-store,
  secrets, startup activity and live activity observation. W2 supplies the shared
  API/dashboard policy handlers. W3/W9 supply the queue's host/client construction.
  Config.Open now owns the real store and reaper/concurrency work. It does not
  supply these missing service implementations.
- W9 must add SourceAttempt to JobTransitionRequest and the controller adapter.
  W3 must send the captured source ordinal and consume Applied.Attempt. The
  existing CloudStoreBootstrapW8Components and RunnerStoreLifecycleW8Components
  node case both fail with `holder transition requires a nonnegative source
  attempt`. Their fake wire adapter is not changed to infer the ordinal because
  that would conceal the missing contract field. This is the existing W1
  attempt-identity handoff, not a new proposal.
- W8/W9 still need an amended launcher sync contract carrying the claim fence
  and Kubernetes instance identity. No foreign wire fields were added here.
- W3 source, keeper, shared host constructors, delegated routes, generator replay
  and run/plan/eval identity remain open. W9 EngineClient/DataClient remain stubs.
  W2 PolicyRouter and SyncLauncher remain stubs. The new binder tests use fakes
  for these foreign boundaries rather than calling panic stubs.
- The store's incompatible-baseline refusal has no exported typed sentinel.
  Config.Open preserves that actionable store error, but cannot safely classify
  it as ErrLocalStoreIncompatible without parsing its text. W1 should expose the
  typed refusal so W8 can preserve the local doctor's specific classification.
- W3/W7 workspace-off binary-cache hashing, W9 launcher/source-init grammar,
  bound storage and native Cloud/platform/placement evidence remain open as in
  the earlier report. No new product decision is requested.

## Verification

Checks use GOWORK=off, GOFLAGS=-buildvcs=false, go -C and -p 4. GOCACHE,
GOTMPDIR and TMPDIR are under /tmp/sw-astra-W8.

| Check | Result |
| --- | --- |
| Whole-repository go build -p 4 ./... | Passed after the final production edit. |
| W8, CLI and acceptance go vet -p 4 | Passed. The changed composition and CLI were checked again after final edits. |
| New localcontroller suite with -race | Passed, including startup fence property seeds. |
| FuzzLocalReconciliationKeepsFence, 100x, parallel=2 | Passed 100 generated executions after both seeds reproduced the missing final fence check. |
| Full runners/... and engine/... suites | Passed. |
| Focused wingd and client local lifecycle/HTTP suites | Passed with in-memory transports. |
| Cluster suite | Passed except the explicitly excluded native metrics-listener test. |
| CLI TestWingdRun selection | Passed, including managed --stay parsing and election-before-open. |
| Store TestDefaultTeam selection | Passed after the inventory conflict resolution. |
| Full tests/acceptance and internal/acceptance | Failed only the Cloud-store component and runner-store node component at the missing SourceAttempt boundary. Other component tests ran. Native drivers remain skipped. |
| commentcheck -base main . | Passed across 1,257 Go files on the final source. |
| git diff --check | Passed. |

No native acceptance cell was activated and no historical test was newly ported.
The new tests exercise new store composition. The existing sizing and identity
ports remain in the passing runner/launcher suites.

Shared production files touched are cmd/sparkwing/wingd.go and the conflicted
orchestrator files listed above. Shared tests are cmd/sparkwing/wingd_runtime_test.go
and internal/store/default_team_callers_test.go. The lead-owned handoff file is only
restored to integration's exact contents. No amendment file was edited.

The lead retains independent review, merge staging/commit, native verification
and pipeline execution. No Sparkwing binary, Git write, deployment or release ran.
Overwing discovery worked and reported its installed binary behind source.
All check sessions finished. No scratch-backed test process remained. The worker
scratch directory was deleted. Git config has no local identity or core.worktree
override.

STATUS: CONTINUE
