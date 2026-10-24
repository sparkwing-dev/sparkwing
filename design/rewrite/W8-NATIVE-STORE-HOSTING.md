# W8 native host composition

This continuation starts at e0d0348b5 with the integration merge in progress.

## Implemented

`localcontroller.StoreHosting` connects the managed claim loop to W3's real
`NewNativeHost` and `NewNodeInput` over the controller's existing store handle.
C2 authenticates and authorizes the award through its shared heartbeat policy.
The host selects the authenticated tenant, checks the persisted request and
acceptance, and constructs owner authority from the persisted owner lease.
Job inputs use the real row, including its hash, ordinal and claim prefix.
Service construction cannot substitute another store. Storage and partial
service cleanup are explicit. The shared hosts own source, renewal and joining.
There is no additional keeper or source acquisition loop in this path.

Invariant: only the original current award or its exchanged capability may
construct a host, and service callbacks cannot replace its tenant, seals, fence
or granted runner. The narrowed-owner-delegation test reproduced a missing
issuance-kind check before that check was added. The generated check covers
changed fences, seals, settings, bearer material and reporting delegations.

`RuntimeConfig.Hosting` now accepts `UnitHost`. The claimant factory is explicit
in `RuntimeConfig.NewController`. Both existing constructor callers are migrated.
The embedded controller can supply StoreHosting without opening another store.
HTTP placements retain their award-specific transport composition.

W8 now passes the captured `runspec.Request` body in RunInput.SealedRequest,
as C3 requires. Its hash and run ID remain separate. RunInput.GrantedRunner
is captured before callbacks, including an independent label slice. This fixes
planning and self-claimed work losing the server-granted identity. Cancellation
reasons now reach both the initial refusal and the shared keeper unchanged.

The new local W8 types are StoreBinding, NativeServices and StoreHosting.
No foreign contract was changed.

## Merge and retirement

All 12 conflict paths have resolved working-file contents. Seven retired files
remain deleted. Their incoming versions total 2,387 lines.

- internal/cluster/fetch_source_cli.go and fetch_source_cli_test.go
- internal/cluster/heartbeat_test.go
- internal/cluster/operator_ssh_source_test.go
- internal/cluster/trigger_loop.go
- internal/orchestrator/wingd_api.go and wingd_api_routes_test.go

The start/warm/bootstrap entrypoints and current launcher tests win over the
retired runner/agent APIs and old claim-token fixtures. The incoming depcache.Key
addition is preserved. W3's newly merged npm cache-directory caller now uses
the shared npmcmd.ForPlatform builder and gotoolchain cancellation path.
The Windows argv and missing-tool tests remain in the shared npm helper.
No duplicate npm builder was restored.

These are restored historical deletions, not 2,387 newly retired lines.
The warm-pool and run-host k8s dispatchers, worker and old pod builder remain
absent. The only Cloud pod builder found is runners/launcher/job.go.
No Git write ran. The lead must stage the resolved contents and deletions and
finish the merge. The index still reports its 12 unmerged entries.

## Amendment and handoff disposition

Every assigned amendment and W8 handoff was rechecked. Earlier completed work
is retained. This report updates W8-LOCAL-STORE-COMPOSITION.md where the merged
implementations have advanced.

| Rows | Disposition |
| --- | --- |
| A1-08,09,20,26,49,55 | Unified claims, launcher route selection, private enrollment and here handling remain. W2's policy router and claim/exchange handlers are implemented. W9's wire client and the CLI factories still need final composition. Production registration and Actions evidence remain foreign integration. |
| A1-10,11,12,43,45,46,48,50,51,52 | Selected-engine installation, source seals, shared environment, historical matrices and hard cuts remain. W8's new path consumes W3's shared native host. W9's client, notices, upgrade/startup composition and native Windows packaging evidence remain open. |
| A1-18,27 | Ordinal zero is retained. W3 and W9 still omit SourceAttempt on holder transitions. C1 refuses those writes. The billing implementation is not bypassed. |
| A1-19 | Run identity now survives preparation. Store-backed jobs use the shared input constructor. Full production hello evidence still belongs to the final host/service composition. |
| A1-36 | NativeServices carries explicit storage. Fresh executor/eval bindings and their real byte/record transports remain required in the service binder. |
| A1-38,39 | Real native-host construction is added. Old dispatcher, source, heartbeat and daemon cuts survive. Launcher award payload and sync correlation still have the foreign gaps below. |
| A1-40,41 | The elected runtime retains its real store owner, leased recovery, activity/drain lifecycle and final managed argv. The final state, policy and native queue binder is still required before wingd run can be enabled. |
| A1-42,53 | Deployed isolation, scratch/teardown probes, native platform drivers and real-repository QA remain unproven. No native cell is marked complete. |

The approved pending-claim and cancellation-classification handoffs remain
implemented. The runner race suite passes. The startup bootstrap handoff remains
open because the mounted raw bootstrap-admin-token cannot be seeded through
ProvisionCapability/Store.Issue. W8 adds no second issuer.

## Integration requests

- W1/W2 must supply the local state projection, admission run-store, API key,
  startup/live activity and local peer authorization binder. W3's native service
  config is now consumed, but the application's source, process, policy, log and
  storage services still need assembly over that same store. The CLI still
  refuses the unavailable composition instead of calling a panic stub.
- W9 NewEngineClient still panics. Keep the declared controller interface until
  its transport is implemented. W3 NewNativeHost/NewNodeInput and W2
  PolicyRouter/claim/exchange are no longer missing constructors.
- W9 must add SourceAttempt to JobTransitionRequest and its controller adapter.
  W3 must send the captured source ordinal on start/retry/report/unstarted
  release, including ClaimedNodeHost.setupFailure. C1 already enforces it.
- W2/W9 must project the complete granted RunnerIdentity into real awards.
  engineReconciledClaim currently sets Name/Type and omits Labels. The new
  fixture supplies labels. It does not prove production SDK hello.
- W8/W9 launcher sync still needs an immutable award fence and Kubernetes Job
  UID in LauncherObservation. UnitID is stable across re-awards. NodeUID is
  not a Job UID. W8 has not silently changed this foreign wire contract.
  Controller plan/eval awards also still clear Execution, while the launcher
  requires resolved execution resources. W8 cannot infer that policy.
- Native Cloud, Windows/Darwin and placement evidence remains with T1/lead.
  No new product decision is requested.

## Verification

Checks use GOWORK=off, GOFLAGS=-buildvcs=false, private cache/temp paths and at
most four Go package workers. Final results are recorded below.

| Check | Result |
| --- | --- |
| Whole-repository go build -p 4 ./... | Passed after the final production edit. |
| Focused W8, nodehost and acceptance go vet -p 4 | Passed, including the final production edit. |
| All runners/... tests | Passed. |
| All engine/... tests | Passed. |
| All runners/... and localcontroller tests with -race | Passed. The core runner suite passed again after its final test change. |
| New native-store owner, partial-bind, delegation and property seeds with -race | Passed after the issuance-kind fix. |
| FuzzNativeStoreAwardW8, -fuzztime=100x, -parallel=2 | Passed with 105 recorded executions, including narrowed reporting delegations. |
| Focused cluster, local depcache and nodehost cache-directory tests | Passed. |
| Full tests/acceptance and internal/acceptance | Acceptance failed as detailed below. internal/acceptance passed. |
| Full depcache suite | Could not run its legacy HTTP fixtures because the sandbox refuses sockets. |
| commentcheck -base main . | Passed across 1,638 Go files on the final production source. |
| git diff --check | Passed. |

The new TestNativeStoreOwnerW8 completes the managed loop through C2, the real
native owner constructor and real C1 completion. Source and claimant transport
are fixtures. No SDK body runs in that empty accepted plan. Partial binding
tests exercise both trigger and job input construction against C1.

Full acceptance remains red. CloudStoreBootstrapW8Components and the runner-store
node component fail at the missing SourceAttempt boundary. The newly merged
lifecycle event/step cells fail there too. FuzzExecutorFailureMask also fails
before its expected SDK failure path. Its assertion is not evidence of an
independent secret leak while execution cannot pass the first transition.
internal/acceptance passes.

No historical T1 port or native acceptance cell was added. Source documentation
is internal/runners/HOSTING.md and the W8 engine contract. Constructor and
generated input tests cover the changed internal interface.

Shared caller changed: internal/nodehost/cache_directory.go. The incoming W7
internal/depcache/depcache.go addition is preserved during conflict resolution.
The other shared edits are W8-owned acceptance fixtures. No lead-owned amendment
or handoff file was edited. No new public CLI/config/wire surface was introduced.
The internal composition documentation changed with its constructors.

Friction: the sandbox blocks the legacy depcache HTTP fixtures' sockets.
Focused local depcache and cache-directory checks pass. Overwing discovery
reported its installed binary behind source. Initial bootstrap discovery ran
sparkwing info --for-agent once before loading the swarm brief. No candidate
runtime, pipeline, deployment, release or Git write ran.

All check sessions finished. The worker scratch directory was deleted. The
repository has no local user identity or core.worktree override. The lead retains
independent review, merge staging/commit, pipeline execution and native QA.

STATUS: CONTINUE
