# W8 amendment integration audit

<!-- design:begin -->

This snapshot covers every amendment-1 row whose action column names W8. A
component pass proves its named boundary. It does not activate a native catalog
cell or establish an HTTP, Cloud or OS deployment proof.

| Amendment | W8 action and remaining integration |
| --- | --- |
| A1-08 | The unified loop uses one ClaimRequest with both kinds. W9 NewEngineClient and W1 ordered claim selection remain stubs. No split queue client was restored. |
| A1-09 | NewControllerControl implements the Cloud claim/sync adapter against the shared wire interface. Its transport must select EngineOptions.Launcher. NewEngineClient and final factory composition remain W9 integration. |
| A1-10 | Implemented Resolved.InstallSDK, the selected-engine install adapter and the hidden CLI route. Shared npm command construction now covers Windows shims, SDK install, upgrades and cache queries. Workspace-off cache hashing remains open below. Signed executable fixtures cover Go/TypeScript transport. Native tool fakes cover build/cache and npm lockfile behavior. source.Acquire remains a stub. |
| A1-11 | ClaimedUnit retains the amended sealed request and source generation. Source acquisition and host preparation remain integration points. |
| A1-12 | The retired cluster source paths stay deleted. Shared source.Acquire remains a stub and must consume Options.Base and procenv.Git. No private Git environment builder was added. |
| A1-18 | Runner hosting and Cloud work now accept ordinal zero and preserve it until Start. New acceptance components cover both. Negative ordinals still fail. W1/W3 own the returned Applied.Attempt. |
| A1-19 | Hosting captures the granted runner, engine and claim fence before preparation. One node-input constructor supplies those fields to NodeHost and refuses an acquired engine that differs from the award or accepted spec. Claimed jobs and exchanged Cloud work share it. Shared run/plan/eval input construction and actual hello still need W3 contracts and integration. |
| A1-20 | Runners consume awards and bootstrap exchange, not root provisioning. Final enrollment issuance remains W1/W2/W9 integration. |
| A1-26 | The loop sends observed labels/capacity without manufacturing granted identity. ClaimProfileResolver and final root registration remain controller integration. |
| A1-27 | W8 does not calculate settlement. Final store claim/start/finish/reaper integration must preserve the server award's billing facts. |
| A1-36 | Hosting retains the complete award for preparation. Bound byte/record clients must still be supplied to RunInput/NodeInput by the concrete host composition. |
| A1-38 | NewControllerControl maps immutable awards and sync decisions. Jobs persist UnitID and report Job name/node UID after restart. Eval retains its source fence and separate Expansion lease. The immutable Secret now carries the complete award in award.json beside the bootstrap. ReadBootstrapAward and BootstrapHost.RunFile restore it before the existing one-time exchange. Component file/claim/sync/restart/exchange tests pass with fakes. W9 grammar, HTTP, source and host composition remain open. |
| A1-39 | The old cluster claim/source/heartbeat loops and old daemon remain deleted. Unified loop and Hosting components pass. Hosting binds node execution identity, checks direct delivery and unit purpose, and uses the earliest binding/owner/parent deadline. Property and controlled-clock component tests exercise these bounds. source.Acquire, lease.Keeper.Run and the engine HTTP client still panic. Host factories, delegated node routes and generated-code replay construction remain undeclared. |
| A1-40 | The implemented local runtime keeps readiness/status/stay/drain and dynamic dashboard attachment. APIConn now exposes a verified UID/SID identity snapshot to the shared controller, replacing the UID-only accessor. Shared local controller/store/consumer services and native Windows SID transport remain unwired. |
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
| A1-55 | The loop confines here work by RunID and preserves server claim deadlines. Named claim/reclaim, ancestry and one-time-bearer response-loss behavior still depend on the W1/W2/W9 implementations. |

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
closes the missing UID/SID projection request. The Windows socket reader still
has no SID evidence and continues to refuse access. Native transport and
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

<!-- design:end -->
