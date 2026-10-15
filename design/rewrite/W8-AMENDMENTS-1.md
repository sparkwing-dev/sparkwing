# W8 amendment integration audit

The [dashboard activity continuation](W8-DASHBOARD-ACTIVITY.md) records the
runtime's HTTP connection hold, current verification and remaining integrations.

## Continuation: acquired nodes and concrete HTTP composition

The committed cancellation redesign passes both retained regressions. Its stop
path remains 84 -> 75 lines, its pending wait 44 -> 40 and loop.go 408 -> 401.
This continuation leaves that completed mechanism unchanged.

`NodePreparation.Prepare` constructs accepted node input, acquires the accepted
source under the keeper and returns partial acquisition cleanup. Hosting joins
the keeper before source and service cleanup. The shared execution constructor
captures granted identity before acquisition. Foreign routes and engines refuse
execution. Bound host services still supply the job hash, delegated routes,
storage and generator replay. The default acquisition uses W3's declared
ForExecution/Acquire interface. Tests substitute that boundary because its
production body is a stub.

`NewManagedRunner` composes the claimant client, unified loop, host and signed
version policy. Award clients retain the same HTTP transport with separate
bearers. `NewLauncher` joins the wire client, Kubernetes and enforced Cloud
boundary, always selecting the launcher route. Bootstrap exchange also retains
the caller's HTTP transport. Final CLI factories still require shared host and
source-authority constructors.

The local runtime now serves the shared handlers through `LocalHTTP`. The
W8-owned LocalController interface replaces ServeAPI/ServeDashboard with
HTTP() LocalHTTP. `LocalPeer` carries verified APIConn identity to the request
authorizer. Shutdown joins connections and handlers, including hijacked HTTP
connections. A synctest regression reproduced a deadlock before hijacked
connections were tracked and passes after the correction. Closed hijacked
connections leave the tracking set when closed.

Production Go changes add 364 lines and delete 14. The removed surface is the
local backend's pair of serving methods and their delegation. Whole old files
deleted: zero. The earlier cluster/daemon/dispatcher cuts remain deleted. The
fixture's old serving loop is removed. Only W8 source changed. The shared
acceptance caller is tests/acceptance/runner_store_w8_test.go; it now uses the
production node preparer between real SQLite claims and the fake executor.
No caller in another production package required migration.

Source documentation changed in internal/wingd/LOCAL-TRANSPORT.md and
internal/runners/launcher/CLOUD-JOBS.md. Shared contract notes changed in
contracts/c3-protocol-sdk.md and contracts/handoffs.md. All four W8 local-runtime
fixture files were migrated. There is no new T1 inventory port or native cell.

Verification uses GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4, go -C and
-p 4, with GOCACHE/GOTMPDIR/TMPDIR inside /tmp/sw-astra-W8.

| Check | Result |
| --- | --- |
| Whole-repository go build -p 4 ./... | Passed after the final source edit. |
| W8, CLI and acceptance vet | Passed. |
| Full runners/... and engine/... suites | Passed. |
| tests/acceptance and internal/acceptance | Passed. Acceptance took 86.120 seconds. |
| RunnerStoreLifecycleW8Components | Passed through NodePreparation and real claim/start/finish persistence. |
| Focused local runtime/dashboard/control/HTTP race suite | Passed, including the hijacked-handler regression. |
| Runner/launcher composition and preparation race selection | Passed. |
| FuzzNodePreparationRejectsForeignRoutes, 100x | Passed 128 generated executions. |
| FuzzLocalHTTPIdentity, 100x | Passed 100 generated executions on the final HTTP implementation. |
| commentcheck -base main . | Clean across 1,189 Go files. |
| Cluster suite with -skip '^TestStartMetricsListener_ServesMetrics$' | Passed. The excluded test requires native sockets. |

The local race selector was
`go test -race -p 4 ./internal/wingd -run '^(TestLocal(Runtime|HTTP|Dashboard|Control)|FuzzLocalHTTP)' -count=1`.
The runner/launcher race selector covered TestNodePreparation,
TestManagedRunnerComposition, TestLauncherComposition, TestBootstrapExchanges
and FuzzNodePreparation. The two properties use `-run '^$' -fuzz <name>
-fuzztime=100x -parallel=2`. Full acceptance and runner/engine suites used
`go test -p 4 ./internal/runners/... ./internal/engine/... ./tests/acceptance
./internal/acceptance -count=1`.

The added mechanisms are the runtime constructors, source preparer and local
HTTP connection owner. They join the authorized contracts while keeping
credential and resource lifetimes explicit. Reverting them removes these four
files and restores the backend serving methods.

The same 26 amendment rows retain their component/native boundary. A1-08/19/38/39/40
advance through the compositions above. A1-10/12/36/48 retain the source,
storage and captured-environment dependencies. A1-41/42/43/49 and the other
assigned rows retain their previously implemented behavior and recorded native
or cross-area obligations. All W8 handoffs were read; none introduces another
unhandled W8 design approval.

Real integration still needs W3 source/keeper bodies, shared RunHost/NodeHost
constructors, run/plan/eval identities, delegated routes and generator replay;
W9 EngineClient/DataClient bodies, final CLI factories and recorded launcher
grammar repairs; W1/W2 local store/queue/maintenance adapters, shared policy
handlers, plan/eval resource awards and SyncLauncher; and W3/W7 workspace-off
binary-cache hashing and bound storage construction. Native HTTP/api.sock,
Windows/macOS, deployed Cloud probes/cleanup, full placement and real-repository
QA remain unprovided. Fakes preserve these interfaces without completing their
owners' implementations. The local composition change is W8-owned; no foreign
Go contract changed and no new product decision is requested.

The lead retains independent review, commits and native verification. Worker
Git writes and Sparkwing binaries/pipelines were not used. Overwing discovery
worked while reporting its installed binary behind source. Socket-dependent
metrics and native acceptance remain outside this sandbox's evidence.
Git config has no local identity or core.worktree override. Checks finished
without retained test processes. Worker scratch was removed after verification.

<!-- design:begin -->

This snapshot covers every amendment-1 row whose action column names W8. A
component pass proves its named boundary. It does not activate a native catalog
cell or establish an HTTP, Cloud or OS deployment proof.

The approved pending-claim outcomes and cancellation classification are
implemented. A1-08/39/55 preserve awards and independent stop results through
Loop and ManagedRunner. The policy-denial and arbitrary-cause regressions pass.
All 26 assigned rows and W8 handoffs were rechecked. Foreign implementations
and native evidence remain integration work as recorded in the table.

The final source review found no actionable issue. The policy/claim stop path
shrinks from 84 to 75 physical lines. The pending wait alone shrinks from 44 to
40, and loop.go from 408 to 401 after the capacity-wait cancellation correction.
The diff removes 49 production lines and adds 42. No public API, foreign caller,
new T1 port or native acceptance cell changed.

| Amendment | W8 action and remaining integration |
| --- | --- |
| A1-08 | The unified loop uses one ClaimRequest with both kinds. RunnerStoreLifecycleW8Components now exercises the real EngineController and SQLite claim scan through that loop. W9 NewEngineClient remains a stub. HTTP response-loss evidence remains open. |
| A1-09 | NewControllerControl implements the Cloud claim/sync adapter against the shared wire interface. Its transport must select EngineOptions.Launcher. NewEngineClient and final factory composition remain W9 integration. |
| A1-10 | Implemented Resolved.InstallSDK, the selected-engine install adapter and the hidden CLI route. Shared npm command construction now covers Windows shims, SDK install, upgrades and cache queries. Workspace-off cache hashing remains open below. Signed executable fixtures cover Go/TypeScript transport. Native tool fakes cover build/cache and npm lockfile behavior. source.Acquire remains a stub. |
| A1-11 | ClaimedUnit retains the amended sealed request and source generation. Source acquisition and host preparation remain integration points. |
| A1-12 | The retired cluster source paths stay deleted. Shared source.Acquire remains a stub and must consume Options.Base and procenv.Git. No private Git environment builder was added. |
| A1-18 | Runner hosting and Cloud work accept ordinal zero and preserve it until Start. RunnerStoreLifecycleW8Components checks a real store award at zero, Start at one and persisted completion at one under the same generation. W3 still owns execution using Applied.Attempt. |
| A1-19 | Hosting captures the granted runner, engine and claim fence before preparation. One node-input constructor supplies those fields to NodeHost and refuses an acquired engine that differs from the award or accepted spec. Claimed jobs and exchanged Cloud work share it. Shared run/plan/eval input construction and actual hello still need W3 contracts and integration. |
| A1-20 | Runners consume awards and bootstrap exchange, not root provisioning. Final enrollment issuance remains W1/W2/W9 integration. |
| A1-26 | The loop sends observed labels/capacity without manufacturing granted identity. ClaimProfileResolver and final root registration remain controller integration. |
| A1-27 | W8 does not calculate settlement. Final store claim/start/finish/reaper integration must preserve the server award's billing facts. |
| A1-36 | Hosting retains the complete award for preparation. Bound byte/record clients must still be supplied to RunInput/NodeInput by the concrete host composition. |
| A1-38 | NewControllerControl maps immutable awards and sync decisions. Jobs persist UnitID and report Job name/node UID after restart. Eval retains its source fence and separate Expansion lease. The immutable Secret carries award.json beside the bootstrap. ReadBootstrapAward and BootstrapHost.RunFile restore it before one-time exchange. CloudStoreBootstrapW8Components joins a real EngineController/SQLite work claim and exchange to Hosting through the builder's private files. It proves live replay refusal, retained root ancestry, renewal and persisted completion with fake source/keeper/executor ports. A fuzz property refuses altered claim identity, engine, deadlines and resources. Plan/eval resources, SyncLauncher, W9 grammar/HTTP and source/host composition remain open. |
| A1-39 | The old cluster claim/source/heartbeat loops and old daemon remain deleted. Unified loop and Hosting components pass, including real EngineController/SQLite claim, renewal and completion with fake hosts/keeper. Root revocation refuses preparation through the real renewal boundary. Hosting binds node execution identity, checks direct delivery and unit purpose, and uses the earliest binding/owner/parent deadline. Property and controlled-clock components exercise these bounds. source.Acquire, lease.Keeper.Run and the engine HTTP client still panic. Host factories, delegated node routes and generated-code replay construction remain undeclared. |
| A1-40 | The implemented local runtime keeps readiness/status/stay/drain and dynamic dashboard attachment. APIConn exposes a verified UID/SID identity snapshot. DialSocket/ListenSocket use Unix peer UIDs and Windows client identification-token SIDs. Windows DACL, anonymous-token, framing and lifecycle tests cross-compile for amd64/arm64. Native execution, distinct-account refusal and shared local controller/store/consumer services remain open. |
| A1-41 | Implemented the native home-bound ManagedUnitManager and final foreground/timer arguments. Refresh preserves captured settings and restarts installed controllers. W9 dashboard/schedule/update/doctor composition and native login/crash/drain proofs remain open. |
| A1-42 | Existing builder/boundary tests retain exclusive nodes, enforced network evidence, DinD and aggregate 20 GiB scratch. Deployed probes, reservation cleanup and native teardown proofs remain open with T1/lead. |
| A1-43 | Historical closed release matrices and signed-index fallback remain intact. W9 still owns presentation notices and the upgrade verifier integration. |
| A1-45 | The merge retains W6's settings winner. W8 adds no admission-class inference. Actual scheduled host intake remains shared-controller integration. |
| A1-46 | Both install sides use procenv.Install with the shared masker. Tests assert proxy credentials do not reach diagnostics. Old cluster proxy/source paths remain deleted. |
| A1-48 | Local lifecycle and runner preparation retain captured input boundaries. The install path uses the shared process builder. Final host/schedule snapshots and startup declaration rules remain W3/W6 integration. |
| A1-49 | Enrollment writes runner.repos and an absolute private credentials_dir. Startup reads the file once into a transport callback. Failed enrollment attempts revocation, removes its new credential/service files and restores prior settings while preserving concurrent replacements. Component tests cover aliases, explicit empty collections and service-manager failure. Final capability issuance/HTTP and host composition remain open. |
| A1-50 | Added the hidden selected-engine install route. Final here/cache/publish/upgrade/status adapters remain W9 integration against shared contracts. |
| A1-51 | W8's retired cluster v1 host paths stay deleted. Shared Go/TS corpus parity, remaining orchestrator fixtures and generated references remain W3/W4/W5/W9 work. |
| A1-52 | The install result has no preload. Windows npm shim argv uses Node through the shared npmcmd helper and process-group runner. Windows execution and the packaged TS bridge still need native evidence. No Linux fixture is counted as Windows proof. |
| A1-53 | Native catalog cells stay blocked. Component acceptance passes only its component suite. No native driver or real-repository QA was available in this sandbox. |
| A1-55 | A here loop with RunID and omitted Kinds now requests only its trigger, matching the shared client's named route contract. A component checks the request, unchanged award and no retry after a lost response. A fuzz property checks request selection and foreign-run refusal. Successful policy refreshes retain the pending request. A controlled-clock component and a second fuzz property prove that delayed delivery and response loss never replay that claim. Independent outcome handling now preserves the award and stop result. The approved interruption classification is implemented and its regression/property pass. Native claim/reclaim, requester ancestry and HTTP response-loss evidence remain integration obligations. |

## Merge resolution

All ten conflicted paths have resolved working-file contents. Contract additions
win, while W8 runtime behavior and deletions remain. No Git write ran, so the
index still records unmerged paths for the lead to stage and commit. The four
modify/delete conflicts keep their deletions: cluster/headroom.go and
orchestrator/local_secrets_test.go, wingd_api.go and wingd_daemon.go.

The merged userconfig budget parser replaces one stale W8 caller in
internal/cluster/runner_cli.go. The merged runner enrollment test replaces its
removed agentservice import and adapts Exec to accept a context. No other
removed API needed a production caller migration in this continuation.

## Contract changes needed and open review findings

The independent review's native npm finding now has a shared implementation.
Workspace-off hashing still blocks completion of the native install adapter.

1. **Explicit binary-cache workspace policy (source/cache owner, W3/W7).** Add
   a cache-key operation that can hash the pipeline with workspace resolution off,
   matching the pinned install build. PipelineCacheKey currently reads the ambient
   go.work even though installGoSDK gives native Go GOWORK=off. A parent workspace
   with `use (./pipeline ./missing-unrelated-module)` causes an unrelated missing
   go.mod error before Go runs. The reviewer reproduced this through an overlay.
   Do not add a second hasher or mutate the host environment to hide it.

`internal/engine/npmcmd.Command` replaces depcache.npmCacheCommand. SDK install,
native lockfile updates and npm cache queries consume it and the existing
process-group runner. Windows shim fixtures provide npm.cmd plus node.exe,
without a native npm.exe escape path. An offline Linux component installs a
local SDK tarball with the real Node/npm tools. Windows execution and process-tree
proofs remain native acceptance work. No other area's frozen contract changes.

Mixed-case JSON aliases were also identified. The final decoder accepts only the
exact directory, pipeline_dir and sdk field spellings. Tests now reject both
`directory` plus `Directory` and `sdk` plus `SDK` under a matching engine version.

The existing W3 host-construction/delegated-route/replay and local-services
contracts remain required. Managed-unit CLI composition, the final runner
transport/host factory, capability issuance, Cloud probes and node cleanup remain open.
No new human product decision is requested.

The controller-facing peer projection is now W8's `APIConn.PeerIdentity`.
W2 can consume the existing `LocalPeerIdentity` through that interface. This
closes the missing UID/SID projection request. Windows now reads client SID
evidence through the named-pipe identification token. Native execution and
same-account/foreign-account proofs remain open.

## Runner credential and enrollment continuation

File loading and failed-enrollment cleanup are implemented at their component
boundaries. The default start reader uses userconfig.ReadConfig. Machine files
use credentials.ReadPrivate and fssecure. Warm services retain the existing
Kubernetes projected-file reader. Actions credentials stay in memory. The
runner factory receives a Bearer callback suitable for EngineOptions.Bearer.
No factory or HTTP implementation is inferred from this callback.

Enrollment stops writing holder_prefix because claim holders come from the
server registration. Both configured file paths and generated credential paths
are captured as absolute paths. A failed forced enrollment restores the prior
validated values, including YAML aliases and explicit empty collections, without
replacing unrelated settings or a later enrollment. A failed service install
removes only its new file, including when the manager becomes unreachable.
The manager error remains visible. Native loaded-process cleanup remains an
acceptance obligation.

The shared credential owner adds ReadPrivate. Agentconfig's surviving removal
caller uses it instead of a duplicate reader. No other area's frozen contract
changes. The full evidence and shared-file list are in the W8 handoff.

## Local contract additions and mechanisms

The SDKInstallRequest fields and InstallSDK signature are unchanged. Its strict
JSON decoder and PipelineDirectory method share request/path validation with
the selected-engine adapter. sdkinstall.Run is the hidden route's implementation
entrypoint. No other area's contract changes.

The child package avoids an import cycle through the existing local binary cache,
store and runspec. It uses the existing cache owner with a separate key for local
SDK builds. Undoing it removes the hidden handler and these W8 install files.
No cache service, remote pipeline-binary fetch, setting, environment variable,
trust root or customer timeout was added.

## Continuation: integration merge and award-bound controller

The five new modify/delete conflicts retain W8's deletions: the three
orchestrator wingd files and both warmpool files. Incoming changes remove old
routes/reconciliation or rename the old matcher. They add no replacement
contract. Their incoming contents total 2,046 lines. The files are absent, while
the index remains unmerged for the lead because workers cannot write Git metadata.

A1-39 now binds Hosting to the shared controller contract. ControllerURL and
NewController replace the generic Renew callback. NewController defaults to
client.NewEngineClient. Its EngineOptions.Bearer closes over one awarded bearer.
The same Controller confirms and renews that unit and reaches PrepareRun or
PrepareNode as the third argument. The keeper projects cancellation and watches
without starting execution. Failure before confirmation cannot prepare source.

The invariant is that a unit's renewal and preparation share a client bound to
that unit's award, while root authority stays with claims. Component tests cover
two concurrent mixed-kind claims, retained independent bearer callbacks, both
preparation paths, connection/confirmation failure and watch projection. A fuzz
property generates bearer/unit values and cancelled credential lookups. These
are component proofs against fakes of the shared HTTP and host ports.

This is a W8-local signature change. The two existing acceptance callers are
migrated. No other-area production caller uses the removed callback. The existing
controller client factory replaces one callback rather than adding another host
or renewal layer. Keeping Renew would leave credential selection outside the
common host boundary. Undo restores Hosting and those callers together.

The merged W1 store and controller lifecycle/claim/renewal operations now have
runtime implementations. Earlier statements in this audit that call those
operations stubs are superseded. A1-08/26/27 still need final enrollment and HTTP
composition. W9 NewEngineClient, W3 source projections/acquisition, Keeper.Run,
remote owner operations and host construction remain unimplemented here.
A1-11/12/19/36/38/40/48/49/50/55 remain integration obligations through those
ports. The existing workspace-off cache-key, host construction/delegation/replay,
local services/activity and Cloud bootstrap grammar/source-handoff requests remain.
A1-41/42/43/51/52/53 keep their recorded CLI, deployment and native proof limits.
No native catalog cell is unskipped and no T1 inventory port is newly claimed.

## Continuation: bootstrap exchange boundary

The new merge has one modify/delete conflict. The W8 deletion of
internal/orchestrator/wingd_api_routes_test.go is retained, removing 336 incoming
legacy test lines. Its additions classify routes on the retired local API.
No Git write ran. The lead must stage the resolution and complete the merge.

A1-38 now has BootstrapHost against the amended controller Exchange interface.
Its transport retains the bootstrap in a context-aware bearer callback, calls
exchange once and gives the complete direct award to runners.UnitHost. It refuses
changed binding, engine, accepted-run or resource inputs and checks cancellation
before dispatch. A1-19's accepted engine and granted runner reach the host intact.
Renewal/source/execution remain that host's responsibility under A1-39.

The original binding, including the eval's distinct live generation and terminal
source generation, is the execution confinement invariant. Table tests and a fuzz
property cover changed identities, generations and authority. The new acceptance
component covers plan/eval/work with fake transport and unit hosts. Native cells
remain blocked. No additional T1 inventory test is claimed.

The new W8-local BootstrapHost API replaces no public or other-area contract.
The existing binding validator now accepts the required delivery kind as an
argument, so claim/sync keep bootstrap-only validation and exchange requires direct
delivery. No duplicate binding validator, setting, environment variable, timeout,
trust root or compatibility path is added. Undo removes BootstrapHost and its new
tests and restores the private validator's signature.

The concrete grammar request remains with W9: fetch-source/run-node require a job
for plan units and reject --unit, --attempt, --engine, --source-job and
--source-generation emitted by BuildJob. The original award/source handoff also
needs a shared declaration. This continuation leaves those contracts unchanged.
W3 host construction/delegated routes/replay and W1/W2 local-service composition,
workspace-off binary-cache hashing and native/Cloud proofs remain open.

Inspection also confirms that pkg/controller/engine_claim.go clears Execution
for plan/eval claims and exchanges. W1/W2 must supply the resolved Cloud resources
already required by ControllerControl.Claim and BootstrapHost. The existing wire
shape supports this. No other-area runtime or contract was edited to work around it.

## Continuation: execution identity handoff

The worktree starts clean at ec12ba220 with the prior changes committed and no
merge conflicts. All 26 W8 amendment rows and the W4 handoff were rechecked.
A1-19/39 gain the node-input constructor described above. A1-38 now has a
component proving exchanged Cloud work uses that same Hosting path. The other
rows retain their recorded integration limits.

W4 finding 3 is partial. The W8 node-award slice is implemented. The complete
single-constructor/hello requirement remains open because RunInput has no
granted runner field and C3 declares neither plan/eval execution inputs nor a
process constructor consuming the selected engine and runner together. W3
must declare that shared boundary before W8 can compose every unit kind.
The existing node-host runtime still reads runner identity from ambient env.
No W3 runtime or contract was changed.

The constructor consumes existing ClaimedUnit, source.Entry and NodeInput
contracts. It captures authority before controller/observer/preparation calls,
copies runner labels and derives job/generation/attempt from the award. An
input's stale Engine or GrantedRunner cannot replace those values. Source's
exact pin, manifest and release identity must agree with the accepted award.
Actual release verification and per-start policy checks remain with source
and NodeHost. Fakes of those interfaces prove this component boundary only.

No other-area contract shape changed. The existing workspace-off binary-cache
operation, host construction/delegation/replay, local services/activity,
Cloud bootstrap grammar/source handoff and plan/eval resources remain open.
Native cells stay blocked pending real placement, platform and deployment
proofs. No additional T1 inventory port or old-code deletion is claimed.

## Continuation: private Cloud award transport

A1-38 now has a W8-owned source and execution payload contract. BuildJob puts
`award.json` beside `bootstrap` in the same immutable Secret. The metadata
contains engine, source, binding, CPU and memory. ReadBootstrapAward reads both
files through the shared projected-credential reader. BootstrapHost.RunFile
passes that original award through the existing exchange and dispatch path.
The reader rejects malformed, duplicate, unknown and incorrectly cased fields,
without returning partial authority or parser diagnostics containing token bytes.

All three Cloud kinds pass from the builder through real files and exchange to
a fake shared unit host. A Linux filesystem test uses Kubernetes projection
symlinks. Admission mutation tests now replace award.json as well as the token.
A fuzz property checks roundtrip inputs and credential-free failures.

This is a local addition to the W8 CloudJobAward contract. The metadata has
explicit JSON field names. The bootstrap path stays the same. Unit, attempt,
engine and eval-source arguments are removed because their values now travel
in the private file. The builder's eval/work commands pass the shared parser.
The existing Kubernetes JSON module becomes a direct dependency at its existing
version because strict nested decoding avoids another parser implementation.
No other area's Go contract or production caller changes.

W9 can now compose fetch-source with ReadBootstrapAward and run-node with
BootstrapHost.RunFile. Its registry still requires a job for plans. W3 still
owns source acquisition, keepers, shared hosts and
the missing run/plan/eval identity inputs. The merged W1/W2 controller still
clears Execution for plan/eval and leaves SyncLauncher unimplemented. The
workspace-off binary-cache operation, local shared services and native platform,
deployment and real-repository proofs remain open. The existing fake component
proofs do not complete those integrations or activate native catalog cells.

## Continuation: integration merge and chart identity plumbing

All 26 amendment rows assigned to W8 and every W8 handoff were rechecked.
The two merge resolutions retain the W8 deletion of runner_pool_cli.go and
both handoff histories. The incoming CLI edit removes its ambient runner
identity path. W8's awarded NodeInput constructor already replaces that path.
The index still needs the lead's staging and merge commit under the no-Git-write rule.

A1-19/48/51 also lose the chart's default POD_NAME injection, as frozen section
7.4 requires. Source and vendored runner charts now rely on the controller's
enrollment identity. Both existing render tests rejected POD_NAME before the
cut. The chart's Auth documentation records the identity source. No contract
shape, setting, environment name or API was added.

The previous source/keeper/client stubs and missing shared run/plan/eval
identity constructor remain. Plan/eval resources, SyncLauncher, workspace-off
binary-cache keys and shared local services still need their owning areas.
W8 keeps its existing fake-based component coverage at those declared ports.
Native platform, placement, deployment and real-repository evidence remains
open. No native cell or new T1 port is claimed.

## Continuation: launcher polling

RunLaunchCLI now accepts the registry's existing --poll option and applies an
explicit positive value to the real launch loop. Omission preserves its configured
interval. Controlled-clock components prove one-second claim spacing and preserved
omission. Zero, negative, malformed and empty values fail before runtime construction.
No old code, API or production caller was removed. No native cell or T1 port is claimed.

The remaining W9 grammar request now includes launch. Its registry omits the
runtime's required --docker-image and declares --deadline without a corresponding
CloudJobConfig input. Temporary overlay probes reproduce both failures. These
are contract requests in handoffs.md, not silent grammar amendments.

Whole-repository build, W8 vet, commentcheck, full runner/launcher/engine suites,
W8 component acceptance and focused local-controller tests pass. Cluster passes
with its socket-based metrics listener excluded. No new production mechanism,
setting, environment variable or timeout was introduced. Remaining source/keeper,
HTTP/data, shared host/local-service, plan/eval resource, launcher sync and native
deployment obligations are unchanged.

## Continuation: named here claims

The default here loop previously sent both trigger and node kinds. The shared
client contract selects /runs/{run}/claim only for one trigger kind, so the loop
could not select that route without caller correction. RunID with omitted Kinds
now defaults to one trigger kind. Ordinary runners retain their mixed scan, and
explicit kind selections retain their behavior.

The invariant is that default here requests select only the named trigger and
foreign-run awards never reach the host. TestHereRunnerNamedClaimW8Components
failed before the fix. Its award and response-loss cases now pass with the real
loop and fake transport/host. FuzzLoopHereSelection covers arbitrary run bytes,
valid awards and foreign awards. These are component proofs. Real HTTP routing,
requester ancestry, server reclaim windows and native placements remain open.

No contract signature, caller, setting, environment variable or customer timeout
changed. Old code deleted: 0 lines. No native cell or new T1 port is claimed.

Whole-repository build, W8 vet and package suites, focused local-controller
checks, W8 components and commentcheck pass. The here property passed 25,326 fuzz
executions. Sleepcheck against HEAD passes. Against main it finds three inherited
waits in untouched W6/W4 tests: limits/deadline_test.go:58,66 and
sparkwing/sparkwingtest/host_test.go:214. Those files remain with their owners.

## Continuation: pending claims during policy refresh

A1-08/39/55 require the claim request to remain unique while its response is
uncertain. The loop cancelled a pending request at each successful policy
refresh, then requested another award. TestHereRunnerNamedClaimW8Components's
new response_lost_after_refresh case reproduced three requests through an
overlay of the old loop. TestLoopPendingClaimSurvivesPolicyRefresh also failed
on a second request.

The loop now checks policy while the original request stays pending. Its
lifecycle timer interrupts only after an actual idle or claim deadline.
Existing tests retain prompt policy refusal, drain, joined cancellation and
ownership of awards returned during cancellation. The property test varies run
bytes, here versus ordinary selection, refresh count and delivered versus lost
responses. It asserts one request and exactly the expected host delivery.

The three modify/delete conflicts keep these deletions:

- internal/orchestrator/wingd_runstore_test.go, 549 incoming lines.
- internal/runners/k8s/k8s_test.go, 1,567 incoming lines.
- internal/runners/k8s/profile_cpu_policy_test.go, 64 incoming lines.

These 2,180 incoming test lines exercised removed implementations. Their
working files are absent. The lead must stage the resolutions because the
brief forbids Git writes. No production API, caller or other area's contract
changed. No mechanism, setting, environment variable or timeout was added.
Undo restores the pending-claim select cases and their tests together.

The complete remaining execution-identity handoff in contracts/handoffs.md still
needs W3's run/plan/eval identity inputs and shared process constructor. Native
catalog cells and new T1 inventory ports remain unclaimed. Component coverage
uses the declared source, keeper, controller and host interfaces with fakes.

## Approved pending-claim outcomes

Implemented the lead-approved independent outcome representation. Run adopts a
committed award before applying its stop result and joins hosts. Loop.run also
keeps policy errors separate from other failures through ManagedRunner, which
consumes only the update policy result. Cancellation, invalid-award and host
errors survive. The hostFailure marker and extraction are deleted.

TestLoopPendingAwardPreservesPolicyDenial, the managed-update regressions and
the here component's pending_award_after_policy_refusal case pass. The final
outcome property passed 45,134 generated cases. These remain component proofs.
No native catalog cell or T1 inventory port was added.

## DESIGN: interruption classification

Invariant: arbitrary legal error values never panic, and independent denials
survive cancellation. Review first reproduced clean drain being reported as a
policy failure. The shared classification now distinguishes cancellation-only
error trees from joined denials. Final review found a second bug: comparing an
error with context.Cause(ctx) panics when both hold a non-comparable error value.
TestLoopPendingPolicyAcceptsArbitraryCancellationCause retains the failing case.
The two-strikes rule stops further runtime patches pending the lead's decision.

Proposed redesign: recognize only ctx.Err's comparable sentinel at cancellation
leaves, while Run's joining defer preserves arbitrary caller causes. Test nested
wrappers/joins, independent denials and comparable/non-comparable custom causes.
The approved outcome representation and the other 26-row integration limits
remain unchanged. No other area's contract needs to change.

<!-- design:end -->

## Continuation: approved cancellation classification implemented

The two approved W8 pending-claim designs in contracts/handoffs.md are complete.
The earlier interruption DESIGN status is superseded. Cancellation-only error
trees drain cleanly. A denial in any joined branch remains observable. Arbitrary
caller causes reach Run's existing joining defer without equality comparisons.
The property covers deadline and explicit cancellation with comparable and
slice-backed causes, direct causes, nested wraps/joins and optional awards.

The capacity-wait correction reuses the existing outer stop checks. Its original
race failure reproduces 16/100 times with the untouched source overlay and passes
100/100 after the correction. The fresh reviewer found no actionable issue in
either runtime change. The review was source-based, not native execution.

Real integration remains open for W3 source/projections, lease keeper, host and
process construction, run/plan/eval identities and bound storage; W9 engine/data
HTTP clients, final factories and launcher grammar; W1/W2 plan/eval resources,
SyncLauncher and the elected local-controller service adapter; W3/W7 workspace-off
binary-cache hashing; and T1/lead deployed Cloud network/cleanup, native Windows
and macOS, full placement and real-repository proofs. These boundaries retain
contract-based fake components. No foreign contract change was made.

Shared file touched: contracts/handoffs.md. No other area's code or caller was
migrated. The working-file merge conflict is resolved, but its index still needs
the lead's stage/merge commit because worker Git writes are prohibited.
No new product decision or contract amendment is requested.

### Verification for the cancellation continuation

Commands use go -C with this worktree, GOWORK=off, GOFLAGS=-buildvcs=false,
GOMAXPROCS=4 and GOCACHE/GOTMPDIR/TMPDIR under /tmp/sw-astra-W8. Tests use -p 4.

| Check | Result |
| --- | --- |
| Whole-repository build -p 4 ./... | Passed after the final runtime edit. |
| W8/acceptance vet | Passed after the final runtime edit. |
| Full internal/runners suite | Passed, including the original policy-denial and arbitrary-cause regressions. |
| Loop/Managed race selection | Passed after the capacity correction. The capacity regression also passed 100 race runs. |
| FuzzLoopPendingClaimStopOutcomes, 10s, parallel=4 | Passed 28,066 executions on the final runtime. |
| FuzzLoopPendingPolicyInterruption, 10s, parallel=4 | Passed 58,400 executions on the final runtime. The corpus includes twenty explicit seeds. |
| Engine, npmcmd, sdkinstall, launcher and runner-service suites | Passed. |
| W8 and Cloud component acceptance | Passed, including real EngineController/SQLite runner and bootstrap boundaries with fake foreign host/source/keeper ports. |
| Focused local runtime, lifecycle, dashboard, managed units and clients | Passed. |
| Cluster suite | Passed with its previously blocked metrics-listener test excluded. |
| commentcheck -base main . | Passed across 1,176 files. |
| sleepcheck -base HEAD ., gofmt and git diff --check | Passed. |

The combined final selector also matched two native wingd tests through Loop:
TestDaemon_AcceptLoopClosesAForeignConnection and
TestHeadroomSamplerLoopAdmitsAfterExternalCPUDrops. Both could not run because
the sandbox rejects Unix-socket setsockopt. Their fixtures removed the temporary
socket directories. This is unexecuted native evidence, not a passing daemon
suite. Native Windows/macOS, deployment probes, full placement cells and real
repository QA remain untested here. No native catalog cell was unskipped.

No public docs impact: these changes implement the approved internal stop/error
contract and preserve the CLI grammar, settings and error wording. The source
contract handoff and worker reports carry the behavior and verification update.
No foreign public contract, caller migration or additional T1 port was needed.

Friction: initial bootstrap ran installed sparkwing info --for-agent before the
brief's no-binaries instruction was read. It printed discovery context. No
further Sparkwing binary or pipeline ran. Overwing standards remained usable
while reporting its installed binary behind source. Worker Git writes are
prohibited, so the lead must finish the already-resolved working-file merge.
No deployment or release ran. Git config has no local user identity or
core.worktree override. Check processes finished and worker scratch was removed.

## Continuation: merged baseline verification

The worktree starts clean at 71f20e0a3. The lead committed the previous
cancellation continuation and finished its merge. The integration merge changes
no W8 runtime file. Both approved pending-claim designs remain implemented.
The policy-denial, arbitrary-cause and pending-outcome regressions pass.

All 26 assigned amendment rows and the W8 handoffs retain their recorded status.
Existing components exercise the declared foreign interfaces with fakes.
The remaining contract requests still need their owners:

- W3 must declare run/plan/eval identity inputs, shared host/process constructors,
  delegated node routes and generator replay construction.
- W9 must resolve launcher's image/deadline grammar and allow plan bootstrap
  commands without a job. The source-init/runtime handoff also needs a declaration.
- W3/W7 must expose workspace-off binary-cache hashing for selected-engine installs.

Production integration still needs W3 source and keeper implementations, W9
HTTP/data clients and final CLI factories, W1/W2 local-controller services,
plan/eval resource awards and SyncLauncher. Cloud enforcement and cleanup,
Windows/macOS execution, actual SDK hello, native placement cells and real-repo
QA remain unverified. Missing foreign contract shapes prevent completing those
W8 compositions. No new contract request or product decision is introduced.

Whole-repository build, W8/acceptance vet, full runner/launcher/engine/service
suites, both acceptance packages, focused local lifecycle/managed-unit suites
and commentcheck pass. Acceptance took 70.894 seconds. Commentcheck covered
1,176 files. Cluster passes with its socket-based metrics test excluded.
Commands retain GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4, -p 4 and
GOCACHE/GOTMPDIR/TMPDIR under /tmp/sw-astra-W8.

This continuation changes only this report. Runtime changes, deleted production
lines/files, new T1 ports, activated native cells and foreign caller migrations
are all zero. Overwing discovery works but reports an outdated installed binary.
No Sparkwing binary, pipeline, Git write, deployment or release ran. Local Git
config has no identity or core.worktree override. The lead retains committing
and native verification. Worker scratch is removed after the final file check.

## Continuation: private HTTP and launcher reconciliation

The approved cancellation implementation was already present at the start of
this continuation. Both TestLoopPendingAwardPreservesPolicyDenial and
TestLoopPendingPolicyAcceptsArbitraryCancellationCause pass. The approved stop
path remains 84 -> 75 physical lines, its pending wait 44 -> 40, and loop.go
408 -> 401. No finished cancellation work was repeated or changed.

A1-38/39/40 now have two further production integrations:

- wingd/client.NewAPIClient and APIBaseURL own HTTP over DialSocket. Requests
  cannot select a proxy or follow a redirect. Statuses and bodies remain intact;
  the shared route client owns application retries. The existing 45-second
  request and five-second dial budgets are retained. Secret CLI, orchestration
  coordination and health probes consume this owner. Orchestration keeps its
  existing retry wrapper because W3 has not replaced its legacy controller client.
- Launcher.Sync reconstructs all live observations from Kubernetes and makes
  one controller sync call. It validates the entire unordered result before
  retiring live Jobs. Missing, duplicate, changed or extra decisions fail without
  live teardown. Local terminal/expired cleanup remains available during a
  controller failure. LaunchOne uses the same binding validation before activation.

The transport invariant is that a private API request never reaches an ambient
proxy or a remote redirect. The sync invariant is that every controller-directed
teardown matches one persisted observed binding. New HTTP behavior tests and
FuzzAPITransportUsesPrivateSocket exercise private-directory validation with
adversarial destinations, cancellation, redirects and non-success responses.
TestSyncReconcilesAllPersistedJobsTogether and FuzzSyncRejectsUnboundCleanup
cover reordered decisions and invalid complete batches. The existing
TestLauncherControllerW8Components now composes the wire adapter, launcher and
Kubernetes API fake with two persisted Jobs, selective retirement and transport
failure. No native catalog cell is activated and no additional T1 port is claimed.

Production changes add 98 and delete 59 physical lines. Removed surfaces are
orchestrator.NewAPISocketClient, HostedAPIBaseURL, apiSocketTransport and the
single-Job shouldRetire helper. The old orchestrator definitions account for
41 deleted lines; 12 local composition lines retain its existing retry/probe
policy around the W8 transport. No compatibility alias remains.

Shared caller migrations are cmd/sparkwing/secret.go,
internal/orchestrator/hosted_backends.go,
internal/orchestrator/hosted_retry_test.go and
internal/orchestrator/local_secrets.go. The shared acceptance edit is
tests/acceptance/launcher_controller_w8_test.go. Source docs are
internal/wingd/LOCAL-TRANSPORT.md and internal/runners/launcher/CLOUD-JOBS.md;
the new behavior tests cover their changed contracts. The handoffs file records
the transport entrypoint for W9. No other area's frozen Go contract changed.

All 26 amendment rows and W8 handoffs retain their prior coverage, with the
transport and sync progress above. Real construction remains open at these
verified boundaries:

- W3 source.ForRequest, ForExecution, Acquire and Entry.Close still panic.
  lease.Keeper.Run and CancelError.Error still panic. Hosting already calls the
  declared keeper and tests it through its fake runtime port.
- W3 has no concrete RunHost/NodeHost contract constructors or plan/eval inputs.
  Granted run/plan/eval identity, delegated node routes, storage binding and
  generated-code replay construction remain the recorded contract requests.
- W9 NewEngineClient and NewDataClient still panic. The W8 private transport is
  ready for both. Launcher image/deadline grammar, plan-without-job grammar and
  source-init/runtime handoff remain their recorded contract requests.
- W2 EngineController.SyncLauncher still panics. W1/W2 have no shared local
  controller/store/services adapter satisfying wingd.LocalController. W8's local
  runtime continues to exercise readiness, reconciliation, serving and joined
  shutdown against that declared interface with fakes.
- Plan/eval CPU/memory awards, the W3/W7 workspace-off binary-cache key, Cloud
  network probes and reservation-scoped cleanup remain open. Native placement,
  Windows/macOS execution, actual SDK hello and real-repository QA still need
  their real drivers. None is claimed by these component passes.

No new product decision or foreign contract change is requested. No Git write,
deployment, release or pipeline ran. Initial sparkwing info discovery happened
before the swarm brief's no-binaries rule was loaded; no further Sparkwing binary
ran. Overwing discovery worked while reporting its installed binary behind source.
The metrics-listener test and native sockets remain unexecuted in this sandbox.

Verification for this continuation:

| Check | Result |
| --- | --- |
| GOWORK=off GOFLAGS=-buildvcs=false go build ./... | Passed over the complete repository. |
| go vet -p 4 | Passed for runners/..., engine/..., wingd/..., cluster, orchestrator, cmd/sparkwing and tests/acceptance. |
| go test -p 4 ./internal/runners/... ./internal/engine/... -count=1 | Passed full suites. |
| go test -p 4 ./tests/acceptance ./internal/acceptance -count=1 | Passed; acceptance took 85.753 seconds. Native catalog drivers remain skipped. |
| go test ./internal/wingd -run '^TestLocalRuntime' -count=1 | Passed. |
| go test ./internal/wingd/client -run '^(TestAPIClient\|FuzzAPITransport)' -count=1 | Passed the new HTTP test and property seeds. |
| FuzzLoopPendingPolicyInterruption, fuzztime=100x, parallel=2 | Passed 100 executions. |
| FuzzSyncRejectsUnboundCleanup, fuzztime=100x, parallel=2 | Passed 100 executions. |
| FuzzAPITransportUsesPrivateSocket, fuzztime=100x, parallel=2 | Passed 100 executions. |
| Cluster suite excluding TestStartMetricsListener_ServesMetrics | Passed. The excluded test needs native sockets. |
| commentcheck -base main . | Clean across 1,181 Go files. |

All Go checks used GOMAXPROCS=4, GOWORK=off, GOFLAGS=-buildvcs=false and
GOCACHE/GOTMPDIR/TMPDIR under /tmp/sw-astra-W8. No sleep was added. The new
properties ran generated cases as well as their seeds. The lead retains Git
commits and the real HTTP, native-platform, deployment and full placement proofs.

The migrated orchestrator's socket-free TestHostedRetryPolicy_ selection also
passes. Git config has no local identity or core.worktree override. Worker
scratch is removed after all checks finish.


## Continuation: awarded trigger input construction

The handoff conflict from the integration merge is resolved in the working file.
Both sides' entries survive. Worker Git writes are prohibited, so the lead must
stage that file and finish the merge.

Invariant: a trigger or here execution receives the run and seals awarded by the
controller, regardless of prepared input or later mutation of the award.
Hosting now captures RunInput's run ID, sealed request/hash and accepted spec/plan
before renewal and preparation. It refuses mismatched run/team/request seals
before acquisition. New runs discard any prepared accepted plan. Resume consumes
the awarded second seal. Bound authority, storage, engine resolver and plan cache
still come from host services. The shared accepted-plan projection replaces the
node preparer's duplicate serialization.

FuzzHostingRunInputKeepsAwardSeals failed both seeds on the prior production code
and passes after the change. The SQLite/controller lifecycle component now
checks the trigger RunInput before real persisted completion. Its fixture now
populates the request/spec hashes stored beside its JSON. No foreign production
caller or contract shape changed. The shared callers edited are
`tests/acceptance/runner_authority_w8_test.go` and
`tests/acceptance/runner_store_w8_test.go`. Contract prose changed in
`c3-protocol-sdk.md` and `handoffs.md`.

This continuation adds 50 production lines and deletes 9. No whole legacy file
is deleted. Earlier cluster, dispatcher, daemon and socket-client cuts remain
removed. No new T1 port or native catalog cell is claimed.

The approved cancellation fix was already committed and both retained regressions
pass. Recounting decce44f0's first parent against this tree confirms stop logic
84 -> 75 lines: the pending wait, policy classifier/check helper and their three
call sites. The pending wait is 44 -> 40; loop.go is 408 -> 401.

Every W8 amendment row and handoff was rechecked. A1-11/39 now include the awarded
trigger-input projection. A1-19 still needs W3's run/plan/eval runner-identity
fields and actual hello construction. The remaining rows retain the
component/native boundary recorded above.

Real integration remains open for W3's source.ForRequest/ForExecution/Acquire,
Entry.Close, lease.Keeper.Run and shared host constructors; W9's NewEngineClient,
NewDataClient, final factories and launcher grammar; W1/W2's elected local
store/queue/maintenance and policy-handler adapter, plan/eval resource awards and
EngineController.SyncLauncher; and W3/W7's workspace-off binary-cache contract.
The declared defaults still target those owners. Components fake those contracts
rather than adding another implementation or undeclared constructor. Native
HTTP/api.sock, Windows/macOS, deployed Cloud enforcement/cleanup, placement cells
and real-repository QA remain unverified. No new cross-area contract change is
requested; the existing requests remain open.

Verification uses GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4, go -C,
-p 4 and GOCACHE/GOTMPDIR/TMPDIR under /tmp/sw-astra-W8.

- Whole-repository build and W8/acceptance vet pass.
- Full runners/... and engine/... suites pass.
- tests/acceptance passes in 39.009 seconds; internal/acceptance has no test files.
- Both original cancellation regressions pass on production code.
- Commentcheck is clean across 1,190 Go files.
- The runner composition/regression race selection passes.
- The new awarded-input property passes 1,026 generated executions; the existing
  interruption property passes 1,000, including its twenty baseline seeds.
- Focused local-runtime and client suites pass. Cluster passes with the native
  metrics-listener test excluded because this sandbox blocks sockets.

No T1 inventory test was newly ported. No product or contract question was
resolved outside W8. The remaining questions are the construction and native
integration points listed above. Git config has no local identity or
core.worktree override. No Sparkwing executable, pipeline, Git write, deployment
or release ran. Check processes finished; worker scratch was removed.

No public docs impact: this implements the internal awarded-input contract;
CLI grammar, settings and wire signatures are unchanged. Source contract prose
records the behavior. The lead retains commits, review and native verification.
