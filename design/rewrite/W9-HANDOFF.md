# W9 handoff

This continuation starts at e6958e0f9 with a clean worktree. W9 is still
incomplete. The lead owns Git staging/commits, independent review, pipelines
and native QA. Session 13 supersedes conflicting earlier status below.

## This continuation (session 13)

### Implementation and deletion

Removed the CLI's remaining legacy Client dependency. Runner enrollment now
uses NewRunnerTokenManager and the shared authenticated engine transport.
The CLI's runnerController wrapper and runnerTokenOperations interface are
deleted. RunnerTokenManager relocates that existing interface to its client
owner and combines enrollment with the existing team token management methods.
Client.EnrollRunner and the unused Client.CreateServiceToken are removed.
Service callers use NewTokenManager with the deployment namespace.

W2 already implements reserved_pools on its existing enrollment request. The
earlier W9 refusal was stale. runners add now forwards repeated and
comma-separated --reserved-for values with the name and repository proposal.
The controller still owns registration and pool authority. Existing private-file
writes, service setup, revocation and rollback remain in use.

Invariant: one enrollment call sends at most one issuance request even when its
reply is lost. The generated property covers transport loss, canonical
unavailability, incomplete issuance and varied names/pools. It also checks the
single registration route and disabled HTTP body replay. Deployment token
managers refuse enrollment before transport. The existing surface component
now enrolls, reads metadata and revokes through one final client for HTTP and
api.sock transports. Those transports are fakes.

Deleted the duplicate runWingdRunWith lifecycle constructor. Its two tests now
exercise runWingdRunCommand, including the real NewLocalRuntime election and
factory-failure cleanup. Removed unused release-tag/home-display adapters,
GitHub URL parser and snapshot-size formatter.

Gross deletion is 131 production-source Go lines and 11 test-source Go lines,
including comments and blanks. Production source adds 35 lines, for a net
reduction of 96. No whole file is deleted. No foreign runtime caller changed.
No archived T1 source test is newly ported. Existing lifecycle and enrollment
tests were migrated with their implementation.

### Verification

All Go commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOCACHE and GOTMPDIR
under /tmp/sw-astra-W9, and TMPDIR under that root. Test parallelism is four.

- Whole-repository go build -p 4 ./... passes.
- Scoped vet passes for cmd/sparkwing, controller/client, clisurface,
  controllerwire and tests/acceptance.
- Focused TestWingdRun, TestRunners, TestRunnerEnrollment, TestCredential,
  TestSurface and the enrollment property seeds pass.
- FuzzRunnerEnrollmentNeverReplays passes 10,219 executions with four workers.
- Full clisurface, controllerwire, doccheck, apiref and apispec tests pass.
- The default-team caller inventory passes without a stale entry or table edit.
- Both TypeScript release/publication checks and both release-target checks pass.
  Windows packaging needed no change. Native bridge execution remains CI work.
- Commentcheck passes across 2,112 files. Whitespace and embedded-doc drift
  checks pass. The client API snapshot was regenerated with cmd/apidiff.

No native acceptance cell was activated. Full CLI/client suites, operational
Sparkwing binaries, pipelines, releases and publications were not run. The
lead retains independent review and native QA. Git configuration still uses
Korey's normal identity and has no injected core.worktree.

### Contracts and integration left open

The W9-local contract update documents RunnerTokenManager's existing W2 wire
shape and single-use transport. No foreign contract changed. The previous
amendments/handoffs audit still applies. A1-49 advances through the final
enrollment transport and closes the stale pool-request gap. Real W1/W2 origin
policy, enrollment persistence and supervised runner startup still need proof.

The remaining Client trigger, named claim and raw state methods still serve
W3's StateStoreOld/RunCoordination graph and W2/W3 legacy fixtures. This
continuation does not replace those fixtures with compatibility adapters.
Native local/here resolvers, full remote host-store and process-service
composition, captured source/environment delivery, cloud bootstrap composition,
authorized cache/result/log bindings, local schedule pin/proof/tick operations,
initial here-reason persistence and debug release remain open as recorded in
session 12. Publication must preserve the local verdict on upload failure.

### Shared edits and friction

Shared files are tests/acceptance/{runner_credentials_contract_test.go,
surface_contract_test.go}, .apidiff/pkg_controller_client.txt, CHANGELOG.md and
its generated mirror, and generated docs mirrors. W9's own c3-controller-wire.md
is updated. Lead-owned handoffs.md and amendments files are unchanged.

The initial compile caught an unavailable apierr helper and an unused import.
Both are corrected and final checks pass. Overwing matching/showing succeeded
despite its installed-binary staleness notice. Native integration and the
foreign contracts above remain the work to continue. No Git state was written.
Scratch is removed before reporting.

## This continuation (session 12)

### Deletion and caller migration

Removed the unused CLI consumer supervisor stop, PID polling and Unix/Windows
process-signaling path. LocalLifecycle remains the lifecycle owner. Deleted the
duplicate service-status renderer, positional run-ID parser, unused flag/display
helpers, old Git-context collector and unused home-path adapter. The stale
mustHomePaths test helper had no callers and is also removed.

Deleted Client.DeleteRun. The CLI already uses NewRunPruner and its acknowledged
RunDeletion response. Removed its entries from the old client's error tables and
API snapshot. The existing surface transport check still exercises typed deletion.
The client package documentation now names the shared controllerwire transport.

Gross deletion is 395 production-source Go lines and 110 test-source Go lines,
including comments and blanks. Eight complete files are deleted. Production
source is 394 lines smaller overall. No foreign runtime caller changed. The
lookback-duration test now parses the final runs-list grammar and calls the
shared duration owner. Removed tests covered the retired positional-ID grammar
and private zombie/PID helper. No parked T1 source test was newly ported.

### Verification

All Go checks use GOWORK=off, GOFLAGS=-buildvcs=false, with caches, temporary
build files and test scratch under /tmp/sw-astra-W9. Test parallelism is four.

- Whole-repository go build -p 4 ./... passes.
- Scoped vet passes for cmd/sparkwing, controller/client, clisurface,
  controllerwire and tests/acceptance.
- TestSurface components pass, including refusal of Client.DeleteRun and the
  typed run-query/deletion transport. No native acceptance cell was activated.
- Focused CLI lifecycle, local intake/hosting, standalone hosting, retained
  grammar and removed-command checks pass.
- Full clisurface, controllerwire, doccheck, apiref and apispec tests pass.
- The default-team caller inventory passes with no stale W9 entry. Its table
  needed no edit.
- Both TypeScript release/publication checks and both release-target checks
  pass. Windows x64/arm64 packaging remains implemented. Native bridge execution
  remains hosted evidence and was not rerun on Linux.
- Commentcheck passes across 2,111 files. Whitespace checks pass.

Migration/changelog source and embedded copies agree. The client API snapshot
was generated with cmd/apidiff. No Sparkwing binary, operational service,
publication or release ran. No Git state was written.

### Remaining contracts and integration

No foreign contract changed or new mechanism was added. The prior W9 assignment
audit still applies to every amendments-1/handoffs entry. Completed step-window,
source-attempt, step-report, launcher-fence, grammar and Windows packaging work
was retained.

Correction to earlier handoffs: W3 provides NewNativeHost and W8 provides
NewLocalRuntime/NewLocalLifecycle. Their constructors are present. W9 still
has unfinished native resolvers in run_host.go, here_resume.go, dashboard.go and
schedules_local.go. NativeHost needs complete owner/claim transactions,
delegated process services, owner policy/state following, captured snapshots,
authorized storage and log sinks. RemoteOwnerStore currently implements only
reconciliation, expansion and scheduling. The elected local controller factory
and full remote host-store composition remain real integration work.

Foreign contract requests still open are immutable initial here-reason retention,
local schedule checkout/environment/proof and pin operations, managed timer/tick
and dry-run operations, debug-release policy/acknowledgement, and authorized
result/log publication. W9 must preserve the local verdict on publication failure.
Cloud fetch-source/run-node still need their native bootstrap/host composition.

The remaining legacy Client lifecycle/trigger/state methods still satisfy
StateStoreOld/RunCoordination. Their live callers include orchestrator's
trigger_host.go, run_node.go, backends.go, hosted_backends.go and
pkg/storage/storeurl/spec.go. Queued-claim methods have no production caller but
are still used by controller/client and W2/W3 legacy fixtures. This continuation
does not restore those methods as a test helper or declare their retirement done.
Full native HTTP/api.sock, Windows/Darwin, storage, Docker/Kubernetes and real-repo
acceptance remains open.

### Shared edits and friction

Shared edits are tests/acceptance/surface_contract_test.go,
.apidiff/pkg_controller_client.txt, CHANGELOG.md, pkg/docs/changelog.md and
pkg/docs/mirror/migrations/_unreleased.md. Lead-owned handoffs/amendments are
unchanged. No other area's runtime or test files changed.

The first build overlapped file deletion and had to be restarted. The first
post-deletion compile found an unused pflag import, which is removed. The final
build and scoped vet/tests pass. Overwing's installed binary reports staleness
but matching/showing standards succeeded. Native evidence and the declared
composition/contract gaps remain the unfinished work. The lead reviews and
commits. Scratch is deleted before reporting.

## This continuation (session 11)

### Implementation and deletion

Deleted the unused legacy launcher client, its source-credential decoder and
server-failure sentinel. Removed the unused claim-resource callback/type,
process-identity helper, base-URL accessor and unpositioned live-log writer.
Final launcher claims retain EngineOptions.Launcher and the shared ClaimRequest.
No production caller required migration for these dead APIs.

Removed repos info's remaining direct state.db opener and Store.ListRuns path.
Its caller and builder now use controllerwire.RunReader. Pipeline history follows
the existing validated cursor visitor. Read failures propagate. Latest run number
selects the displayed state, including queued runs without a start time.
The migrated caller is cmd/sparkwing/repos_info.go.

Invariant: every here host receives the runner identity from its controller
award, regardless of the native binder's proposed identity. runHereAward now
sets GrantedRunner before invoking RunHost. A generated property reproduced
the missing assignment and passes 128 varied awarded/ambient identities.
This closes the CLI part of that handoff. Native hello remains W3 integration.

The tracked CLI/client diff deletes 169 production lines and 23 old test lines,
including two whole files. These are gross deleted-line counts, including
imports, comments and blanks. Production source is 129 lines smaller overall.
The retired source-error test covered only the removed classifier. Final typed
client retry/error tests remain active. No parked T1 source test is newly ported.

### Verification

Commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOCACHE and GOTMPDIR under
/tmp/sw-astra-W9. Test temporary files also use that root through TMPDIR.

- Whole-repository go build -p 4 ./... passes.
- Scoped go vet -p 4 passes for cmd/sparkwing, pkg/controller/client,
  internal/clisurface, internal/controllerwire and tests/acceptance.
- CLI here/local/repository tests pass, including the runner property,
  controller pagination, queued-state display and loud history failures.
- Typed engine/step/data/cache/sizing and launcher-sync client checks pass.
- All TestSurface components pass. Removed-client-API assertions now cover
  ClaimLaunch, SourceCredential, WithClaimResources, BaseURL and AppendNodeLiveLog.
  No native acceptance cell was unskipped or claimed complete.
- Full clisurface, controllerwire, doccheck, apiref and apispec tests pass.
- The default-team caller inventory passes. It reports no stale W9 row.
- Both TypeScript release/publication checks and both release-target checks
  pass. The existing Windows x64/arm64 packaging handoff required no new edit.
  Native Windows bridge execution remains CI evidence.
- Commentcheck passes across 2,113 files. Changelog and whitespace checks pass.

The client API snapshot was regenerated with cmd/apidiff. Migration/changelog
source and embedded copies are synchronized. No Sparkwing binary, operational
service, release or publication ran. No Git state was written.

### Remaining integration and contracts

No new foreign contract change is requested. Earlier open requests remain:
initial here-reason persistence through intake/replay, local schedule registration
and proof/pin operations, timer/tick/dry-run bindings, debug-release policy and
acknowledgement, and authorized result/log publication.

A1-50 still needs the native local-controller factory, private captured inputs,
source/data/cache authority and W3 host/process/lease assembly. Publication has
PublishPolicy but no callable result/log publisher contract to bind. W9 must keep
the local verdict when the eventual upload fails. The shared native bootstrap
composition for fetch-source/run-node also remains open. The interfaces and
fake/component checks do not establish real execution through those seams.

The remaining Client trigger, claim and raw state methods still serve W3's old
StateStoreOld/RunCoordination graph. Its live call sites include
internal/orchestrator/{trigger_host.go,run_node.go,backends.go,hosted_backends.go}
and pkg/storage/storeurl/spec.go. Removing that graph requires its owner to move
to the final sealed run and fenced transaction interfaces. No forwarding shim
or replacement stub was added. Full native HTTP/api.sock, platform, storage,
Docker/Kubernetes and real-repository acceptance remains open.

### Shared files and friction

Shared edits are tests/acceptance/surface_contract_test.go,
.apidiff/pkg_controller_client.txt, CHANGELOG.md and the generated docs mirrors.
No foreign runtime caller file changed. Lead-owned amendments and handoffs are
unchanged. No new environment variable, interface, package or compatibility
mechanism was added.

Friction remains the missing native composition, sandbox socket restrictions,
read-only Git metadata and native-platform evidence. Overwing reports that its
installed binary is stale, but matching/showing standards succeeded. The lead
must review and commit this continuation. Scratch is removed before reporting.

## This continuation (session 10)

### Merge and deletion

All 37 unmerged paths have resolved contents. There are no merge markers.
The index remains unmerged because the brief prohibits Git writes. The lead
must stage the resolutions. handoffs.md and amendments-1.md remain unchanged
from the integration index. W9's old trigger helper, cron commands, host loop,
consumer and duplicate installer remain deleted. W7 storage and W8 launcher
changes survive, including complete award and Kubernetes Job instance fences.

Deleted the unused Client.HeartbeatTrigger and HeartbeatStatus HTTP surface:
37 lines from production source, including comments and blanks. The four shared fixture files listed below now call the
typed Controller.Renew API. This is caller migration, not a claim that their
remaining legacy claim/provisioning fixtures are ported. The final-controller
renewal/fence tests provide current runtime evidence. The removal is pinned in
TestSurfaceRemovedClientRecoveryAPIs.

The remaining legacy Client methods still implement W3's StateStoreOld and
RunCoordination. That foreign runtime graph must switch to sealed request,
transaction and claim interfaces before those methods can be removed. W9 has
no production /triggers, /crons, cache-service or cache-grant path in its CLI
or controllerwire packages. The legacy client graph remains an open hard cut.

### Approved handoffs implemented

- Lead decision 1: runspec.Options now carries StartAt, StopAt and Only as
  Setting[string]. The settings table, intake projection, plan resolution and
  CLI preserve values, presence and origins. SealSpec rejects changes at seal
  two. Retry preserves the original selections. W3 must consume the sealed
  fields in final owner/node execution. No ambient selection transport was added.
- Lead decision 6: launcher grammar requires --docker-image and removes
  --deadline. RunLaunchCLI now uses runnerflags.Parse. fetch-source and run-node
  permit an absent --job for plan awards; W8's private award validator still
  checks the operational kind/binding. Existing poll and memory fixtures now
  supply all required configuration before testing their invalid field.
- A1-10/43 merge: cache warm delegates to the selected engine's SDK installer
  with checkout Directory plus relative PipelineDir, retaining mode validation,
  masked diagnostics and TypeScript's preparation path. The incoming hidden
  installer parser delegates to sdkinstall.Run. Its tests exercise that boundary.
- A1-49 merge: retain incoming runner service/configuration rollback. Default
  config selection still uses userconfig.Path and captures an absolute path.
  Enrollment tests retain revocation, existing-service and concurrent-edit checks.
- A1-52: Windows TypeScript packaging was already implemented. Both required
  native bridge jobs, source/version receipts, stamped assembly and publication
  dependency remain. Packaging and publication tests pass. The incoming cache
  binary deletion required one release-target fixture count change (four to
  three non-Windows executables). No release or publication ran.
- Store-caller inventory passes without edits. There are no stale W9 entries.

C4 records the approved fields and launcher grammar. The migration guide records
the changes and fixes the merged launcher-sync shape and duplicate plan paragraph.
OpenAPI, API references, API snapshots, config reference and embedded docs were
regenerated without using the Sparkwing command generator.

### Evidence

Whole-repository build and test compilation pass. The latter uses
-exec /usr/bin/true and is not whole-suite runtime evidence. Scoped vet covers
CLI, wire, client, controller, settings, runspec, cluster, launcher, orchestrator
and acceptance packages. Full runspec/settings/clisurface/controllerwire,
focused CLI/enrollment/lifecycle/installer, typed client/data/sizing, real SQLite
controller source-attempt/failure/snapshot/sync, launcher and cluster tests pass.
Doccheck, apispec, apiref, command-reference and doc-honesty checks pass.
Commentcheck and whitespace checks pass.

All 35 selected top-level acceptance components pass, including TestSurface,
here resume/response loss, typed step/failure/materialized/snapshot transport,
launcher sync and W8 restart/publication/cancellation components. The 276 native
placement cells remain skipped; no new native cell was unskipped. No T1 parked
original test is newly ported. The generated step-window property checks 256
input triples for acceptance, changed-selection refusal and retry preservation.
Existing source-precedence and CLI preparation tests cover the new fields.

TypeScript release/publication and release-target Go checks pass. The Node
native-package test file passes on Linux; this is package-preflight evidence,
not Windows raw-handle or child-inheritance evidence.

Sleepcheck still fails on the incoming wall-clock assertion in
tests/acceptance/session_flow_http_test.go:102 (unchanged). A mistakenly broad
bin-suite selection invoked a candidate install fixture, contrary to the brief's
binary restriction. It failed on obsolete scaffold --name arguments and a VCS
fixture, then hit the sandbox's forbidden listener in release_manifest_test.go.
Later runs used focused packaging checks, which pass. No operational
installation, release or publication occurred. All temporary files are removed
before the final report, and no scratch executable process remains.

### Remaining contracts and integration

No unapproved foreign contract change was made. The runspec/settings edits are
the minimal shared changes authorized by the lead's step-window decision.

- C1/W2 still need the initial here reason retained through intake/replay.
- W2/C3 local schedule checkout/environment/proof registration and pin/unpin,
  W8 timer install/removal and controller tick/dry-run remain unbound. TickView
  has no dry-run decision projection. W9 uses its declared adapters and fakes.
- C2 still needs the final debug-pause release operation and acknowledgement.
- Local CLI runtime composition still needs a concrete W2 LocalController
  factory, retained private environment/source authority, W3 host/process/lease
  assembly and scoped data/cache clients. W7 NewSignedStore and OpenRemoteCache
  and W9 NewDataClient are real implementations now; earlier reports calling
  them panic stubs are superseded. Profile/authority binding and the authorized
  result/log publication path still require composition. Publication must keep
  the local verdict when its upload fails.
- W3 must consume the new sealed selections, typed step persistence and source
  ordinals in native hosts. W8/W3 must verify exact-engine identity through actual
  hello, sparks overlays, checksums/cache identity and executable lease retention.
- Real HTTP/api.sock, Windows/Darwin, Postgres/S3, Docker/Kubernetes and real-repo
  QA are still required. Component/fake tests do not close those cells.

### Shared files and friction

Merge resolutions beyond W9: internal/depcache/depcache.go;
internal/runners/launcher/{controller.go,controller_test.go,launcher.go,sync.go};
pkg/controller/{engine_lifecycle.go,engine_store.go,engine_store_test.go,
engine_snapshot_test.go,engine_launcher_sync_test.go}; and
tests/acceptance/{launcher_boundary_w8_test.go,launcher_controller_w8_test.go}.

Additional shared edits: internal/runspec/{contracts.go,seal.go,step_windows_test.go};
internal/settings/{contract.go,table.go,resolvers.go,intake.go,projection.go,
source_pairs_test.go,resolution_test.go}; internal/cluster/{launch_cli.go,
launch_cli_test.go}; internal/orchestrator/trigger_host_test.go;
pkg/controller/{credits_test.go,here_runner_test.go,runner_scopes_test.go};
bin/build_release_target_test.go; tests/acceptance/surface_contract_test.go;
api/openapi.yaml, .apidiff/, generated references and pkg/docs/mirror/.

Friction: read-only Git metadata, unavailable sockets, stale installed overwing,
foreign composition gaps and native-platform evidence. No Git write occurred.
Scratch is removed before the final report. The lead must stage and commit.

## This continuation (session 9)

All 60 unmerged paths have resolved contents or the selected deletion. No marker
remains. The index stays unmerged under the brief's no-Git-writes rule. The lead
must stage the resolutions. amendments-1.md and handoffs.md are unchanged. W9's
final grammar is preserved. Incoming W2 controller implementations and W8's SDK
installer replace duplicate W9 implementations, with amended wire data retained.

### Implemented and deleted

- Local, detached and standalone commands use sealed CreateRun, named trigger
  claims and nodehost.RunHost. Real C4 preparation retains presence, origins,
  alias/entrypoint, secret classification, risk checks, pauses, workers, no-memo,
  cache defaults and publication selection. The native seam supplies captured
  facts rather than constructing request semantics. A returned intake cannot
  switch pipeline/purpose. Malformed/partial responses cannot expose a bearer.
  Durable run handles retain the admitted ID through later cancellation.
- Deleted run_detached.go, runs_consumer.go and their old persistence/consumer
  tests. Replaced the old RunHostedCommand/HostClaimedTrigger paths. Shared
  compiler/describe fixtures survive in cli_fixture_test.go. Dead argument,
  source, index-environment and follow helpers are removed with obsolete tests.
- Public trigger uses prepared source, exact selected-engine inspection and
  sealed /runs intake. Deleted createRemoteTrigger, direct-data bundle upload,
  worker.go and the separate here-host loop. Initial here/bundle/environment
  requests fail before intake when their required contract/binding is missing.
- __sdk-install delegates to engine/sdkinstall.Run. Cache warm calls the exact
  verified engine's InstallSDK. The second CLI installer and duplicate tests are
  gone. The remaining diagnostic writer masks inspection/warm output.
- Deleted all crons*.go production files: 1,220 lines. Final schedule managers
  handle observations and remote repository install/uninstall. Local registration,
  pin and tick adapters check required bindings before effects. __tick passes its
  explicit absolute Home into resolution. Existing managed timers use Refresh.
- Local HTTP reuses an elected controller or starts through LocalLifecycle.
  W8 NewAPIClient owns dialing, including Windows. Retained here credentials keep
  the socket binding. Deleted the CLI host-environment injector and warm-up helper.
  Removed wingd supervise; W8's spawn caller now invokes foreground wingd run.
- Debug resume no longer opens the store or calls the old client writer. Its
  adapter resolves the observed stable job identity and uses common output. It
  refuses the absent C2 release policy before effects. Repository history uses
  RunReader and propagates read failures. Deleted the dead stray-session sweep.
- Deleted unused claim-token/child-run/launcher-sync/cancel client APIs and the
  dead Pacer/backoff/advice/shedding chain. The bounded client sweep was net -638
  lines. StateStoreOld and RunCoordination still keep other legacy client methods
  live in W3. Removing that foreign runtime graph is still owner work.

Against the starting HEAD, tracked cmd/sparkwing and pkg/controller/client paths
remove **9,252 lines**, including tests, with **33 whole-file deletions**. This is
not a net reduction claim and excludes new untracked source files. Local/consumer
whole-file removals account for 3,831 lines. Generated output is excluded.

W9-local contract repair: clisurface.Flag.AllowEmpty lets --sw-cache-from= clear
a machine profile while retaining flag presence. Output still rejects empty and
unknown formats. C4 and generated help document this. No C1/C2/W7 shape changed.

### Launcher and controller merge

SourceAttempt and full structured failures reach W2's current TransitionJob
adapter. Materialized-result identity and guard-refusal/cancellation survive the
incoming snapshot/reconciliation implementations. Projection tests now exercise
the current transaction interface rather than a deleted private mapper.

Invariant: every launcher observation retains its awarded unit fence, and cleanup
names one Job namespace/name/UID. W8 creation/restart callers now send those facts.
ControllerControl rejects a different namespace or UID. Eval keeps separate
source and award generations. Existing fixtures supply API instance UIDs. W2's
SyncLauncher now uses LauncherSyncReader; its native store binding remains open.

### Packaging, inventory and verification

Windows TypeScript packaging was already implemented in 3718486f4. Current source
requires native x64/windows-2025 and arm64/windows-11-arm build/conformance jobs,
matching bridge receipts, stamped assembly and publication depending on assembly.
Release/publication Go tests, four Node packaging tests and bin vet pass. No
packaging edit or publication occurred. Native Windows evidence remains CI work.

The store-caller inventory reported exactly five stale W9 entries: cronLauncher.Active,
existingSubmissionResult, findExistingSubmission, persistSubmission and
runDebugRelease. Those rows and the unused CLI rationale constant are removed.
The inventory test passes. Thirteen retired environment rows were also removed;
the environment and command-reference checks pass.

Whole-repository build and test compilation pass. Compilation used -exec
/usr/bin/true, so this is not a whole-suite runtime claim. Scoped vet, focused
CLI/C4/source/enrollment/lifecycle/debug/history tests, real SQLite controller
transition/failure tests, final client transports, full clisurface/controllerwire/
apispec/apiref/doccheck and launcher tests pass. All TestSurface transport/grammar
components and existing W8 launcher components pass. No native W9 matrix cell was
unskipped. The T1 parked inventory assigns no original test directly to W9.

Generated properties passed 26,549 cases: local diagnostic privacy 2,925, sealed
owner preservation 515, launcher instance identity 7,541, debug identity 2,971 and
current-controller structured-failure projection 12,597. Receipt/purpose and
malformed-bearer defects were reproduced before fixing. Ported fixtures retain
alias, risk, receipt, source, attempt, failure, scheduling, cancellation, launcher
and credential assertions through their final boundaries. The old apispec text-
rewrite fixture is retired; final policy/spec drift checks remain.

Commentcheck and whitespace checks pass. Sleepcheck reports one incoming W2 wall-
clock assertion in tests/acceptance/session_flow_http_test.go:102. It was not
edited. Full client/docs runtime suites hit the sandbox's socket prohibition;
focused transports pass. The lead retains those listener suites and native QA.

### Amendment disposition and remaining work

A1-09/16/17 and W9's A1-38 wire work are retained through current implementations.
A1-41 now uses foreground lifecycle/Home arguments and the stable tick adapter.
A1-43 exact-engine/notice/upgrade adapters remain. A1-49 enrollment uses the current
runners/service API, private credential file and rollback. A1-51 schemas/docs are
regenerated. A1-52 packaging is implemented, with native evidence still open.
A1-50 has its CLI adapters and old W9 runtime cut; native execution is incomplete.
The existing typed-step, private award-file, launcher grammar, HTTP/api.sock,
pipeline identity and execution identity handoffs remain part of native integration.

Contract changes needed outside W9:

- C1/W2: retain the initial here reason through intake and replay.
- W6: seal StartAt/StopAt/Only and clarify retained build/index/admission controls.
- W2/C3: local schedule checkout/environment/proof registration and pin/unpin.
- W8: schedule timer install/removal and controller tick/dry-run operations.
  TickView currently has status without dry-run decisions.
- W2: final debug-pause release operation/policy and persistence acknowledgement.
- W7: authorized result/log publication contract. Its signed-store/remote-cache
  constructors are still panic stubs; W9 does not call them in an unbound runtime.

Native bindings still needed: local controller factory by captured Home, source
preparation/access and numeric GitHub identity, private environment retention,
shared host/process/storage/lease construction, data/cache authority, launcher
ownership lookup and W3's production step/source-ordinal senders. Missing cache
or publisher bindings fail before intake. Publication's local-verdict guarantee
still needs the real publisher. W8/W3 must verify sparks overlays, checksum/cache
identity and executable lease retention through installation and use. These are
open implementation/contract gaps, not approved feature cuts.

Shared edits beyond W9: pkg/controller engine lifecycle/store/snapshot/sync and
attempt/failure/materialized/snapshot/sync fixtures; claim_run_test.go (authority
assertions retained), cancel_test.go (retired); pkg/store/default_team_callers_test.go;
internal/runners/launcher controller/launcher/sync and controller fixtures;
internal/wingd/client/spawn.go (run argv); internal/depcache merge/import resolution;
internal/apispec/path_parameters_test.go (retired); acceptance authority/launcher
fixtures; api/openapi.yaml, .apidiff, CHANGELOG.md and generated docs/mirrors.
The integration's trigger_loop and old contention-fixture deletions are preserved.

Friction: Git staging is prohibited, overwing reports a stale installed binary,
sockets are unavailable, and native Windows/Darwin/cluster evidence cannot be
produced here. No service or background runtime was started. Scratch is removed
before the final report. The lead must stage and commit the merge.

## This continuation (session 8)

The contents of all 33 conflicted paths are resolved. The merge index remains
unmerged because SWARM-BRIEF prohibits Git writes and the worktree metadata is
read-only. The lead must stage the resolved files and deletions. The lead-owned
handoffs.md is byte-identical to stage 3, the integration branch. No amendment or
handoff entry was edited. Generated OpenAPI and the client API snapshot were
regenerated from the merged source. The final command-reference drift test
passes without running a Sparkwing binary.

W9's final command grammar, schema-based risk gate and deleted legacy docs/tests
win their conflict hunks. Tenant schedule fixtures and W3's lease keeper changes
survive. CLI description captures the validated alias/entrypoint pair once and
passes it into W3's PipelineIdentity. The real Go SDK alias fixture proves the
required pipeline/job arguments and invoked alias reach planning. The shared
npm resolver remains pipelineentry.NPMCommand. Its one incoming nodehost caller
was migrated instead of restoring a second resolver. One incoming controller
log test needed its missing io import. The contention fixture now names its
runpoll and nodeactivity samples consistently.

The source-attempt handoff is implemented in W9's wire and the existing W2
store adapter. JobTransitionRequest.SourceAttempt is an optional *int64.
Omission differs from explicit zero. The adapter forwards the field unchanged
to C1. Holder start, retry, report and release use the captured ordinal, with
zero for an initial start. C1 retains validation and replay ownership. The
controller lifecycle/failure fixtures and the failure transport acceptance
component now supply the source ordinal. No store or authorization contract
changed.

Invariant: an execution report affects only its named attempt, and replay cannot
consume another attempt. The real SQLite controller adapter test covers absent,
negative and mismatched starts, identical retries, replay without another ordinal,
a new identical failure under the next ordinal, and delayed retry/result refusal.
Generated wire/adapter properties preserve explicit zero, absence and full int64
ordinals. This proves the adapter and C1 transaction together. Native HTTP route
registration and W3's remote caller still need integration.

The typed-step handoff now has StepReportRequest, StepRecorded and
Controller.RecordStep. Running, passed/failed/cancelled and skipped reports select
the existing C2 start, finish and skip routes. Reports retain a positive source
ordinal, masked diagnostic text and optional nanosecond timestamps. They carry
no actor, team, holder or generation. Success requires recorded:true after
persistence. False/missing/malformed acknowledgements fail. Exact-body retries
retain the source ordinal and timestamps, and authority refusals stay canonical.

Invariant: step input cannot change its declared route or introduce writer
authority, and it retains the observed attempt. The generated client property
covers adversarial selectors, states, IDs and ordinals. The HTTP/api.sock
acceptance component covers all five states with transport fakes. W2 must derive
the actor and current target generation/attempt, compare source_attempt, call
C1 RecordStep and acknowledge only after persistence. C1 rechecks those fields
in its transaction. W3 must bind its remote NodeTransitionStore.RecordStep to
this client. No foreign writer or route interface was changed by W9.

The launcher-correlation handoff now uses capability.LeaseRef as the complete
observed award tuple (kind, ID, holder, generation), plus the Job namespace,
name and UID and the reserved node UID. Eval reports its own award generation,
separately from its source generation. Remove is a typed LauncherJobRef list.
Each removal requires namespace, name and UID for a Kubernetes UID deletion
precondition. The client refuses incomplete observations, name-only cleanup and
sync awards containing a raw bearer. The old UnitID/JobName-only observation
and string cleanup list have no adapter or alternate spelling.

Invariant: a sync observation retains the exact award, and cleanup names one
Kubernetes Job instance rather than any replacement reusing its name. The
HTTP/api.sock acceptance component demonstrates two instances with the same
name and different UIDs. The generated property exercises unit kinds,
generations and opaque instance identities. Native collection, reconciliation
and Kubernetes deletion remain unverified.

Invariant: no SyncLauncher result exposes a raw bearer, even when decoding
fails. The new generated test reproduced a partial-decode leak using a complete
award with private capability material and a malformed owner deadline. The
decoder populated the award before failing on time parsing. SyncLauncher now
returns an empty result on every request error, before exposing any decoded
state. The same return boundary covers every malformed-field failure rather
than special-casing the timestamp. The reproduced seed and generated cases pass.

These are W9-local C3 contract repairs requested by the handoffs. C3 records the
changed signatures, exact messages and remaining owner bindings. The new API
guide text and generated OpenAPI describe them. No grant, service, environment
variable, customer cap, deadline or compatibility path was added.

### Remaining owner work

- W1/W2 must implement SyncLauncher against current ownership, scoped to the
  authenticated launcher. W8 must retain the award tuple with the actual Job
  UID, collect observations and delete with the UID precondition before node
  release. EngineController.SyncLauncher remains a panic stub.
- W2/W3 must bind typed step persistence and native executor delivery as described
  above. W3 must send SourceAttempt on its remote transitions and consume
  Applied.Attempt. The wire and store adapter no longer block that transport.
- W6 must declare sealed StartAt/StopAt/Only options before W9 can carry them
  through intake. runspec.Options still has no representation. W9 did not alter
  that foreign contract or reconstruct these options from mutable invocation data.
- The prior public trigger, local/detached host, here, cache/publication, native
  lifecycle/timer, debug resume and acceptance work below remains unfinished.
  Source acquisition and native host constructors now have W3 implementations
  in the integration merge. Earlier statements calling those entire packages
  stubs are superseded. The public CLI still needs their composition. Engine,
  local lifecycle and remote cache entry points still include declared stubs.

### Verification and scope

Whole-repository build, scoped vet, focused engine/controller/client/CLI tests,
full clisurface/controllerwire/apispec/apiref/doccheck tests, command-reference
and embedded-doc drift, commentcheck and sleepcheck pass. The real alias fixture
runs the Go SDK through describe and planning. No native W9 matrix cell was
unskipped. New acceptance evidence is the step and launcher transport components,
plus the extended failure-report component. T1's parked inventory assigns no
original test directly to W9.

Five generated-case checks passed 46,132 cases: source-attempt transport
13,135, source-attempt store projection 14,398, step routing/ordinal 7,352,
launcher award/instance preservation 11,234, and bearer refusal across complete
and partial decoding 13. The earlier step property run passed 5,348 cases before
its selector oracle was strengthened. No failed generated corpus remains.

Retained fixtures migrated are TestEngineJobLifecycleRenewsAndReplaysUnderStoreFences,
TestEngineOwnerReconcilesExecutesAndFinishes,
TestEngineReportedFailureReachesRecovery,
TestSurfaceJobFailureReportTransport and
TestEngineClientRecoveryAndLauncherMethods. The real-store retry-ordinal test
adds the delayed-report and identical-failure boundary behind the source-attempt
handoff. The merged native alias test is retained and passes.

The default-team caller inventory initially failed on exactly four stale W9
entries (retired capacity/profile callers and runCronsShow). Removing those
entries makes TestDefaultTeamTwinsAreCalledOnlyWhereTheDefaultTeamIsMeant pass.
The earlier report's 18 unrelated stale entries no longer describe this merged
tree.

All Go commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE/GOTMPDIR/TMPDIR below /tmp/sw-astra-W9, and test -p 4 at most.
Scratch is deleted before the final report. The lead owns independent review and
native platform/backend/placement QA.

### Deletion and shared files

The old launcher wire shape loses three field lines, and the caller inventory
loses four stale test lines. No additional legacy runtime is deleted. The merge
preserves W9's earlier deletions of
cmd/sparkwing/doctor_test.go, cmd/sparkwing/runs_standalone_test.go,
docs/cli-cluster.md, docs/gitcache.md and their embedded mirrors.

Beyond W9-owned implementation and docs, shared edits are
pkg/controller/engine_store.go (the SourceAttempt assignment),
engine_store_runtime_test.go and engine_failure_report_test.go (source ordinals),
engine_attempt_wire_test.go (projection and real store proof),
log_segments_internal_test.go (missing io import),
internal/nodehost/cache_directory.go (the sole removed-resolver caller),
internal/orchestrator/wingd_api_contention_test.go (sample labels),
pkg/controller/crons_team_test.go (merged imports),
pkg/store/default_team_callers_test.go (conflict resolution and four stale rows),
internal/depcache/depcache.go and depcache_npm_test.go (merge resolution),
internal/apispec/wire.go (step message bindings),
tests/acceptance/{job_failure,step_report,launcher_sync}_contract_test.go,
api/openapi.yaml, .apidiff/pkg_controller_client.txt, CHANGELOG.md and the
generated documentation mirrors. handoffs.md only takes stage 3 unchanged.

Frictions: Git staging is unavailable under the explicit no-write rule. The
installed overwing reports a stale binary. No Sparkwing pipeline or CLI binary
was run, including gen-cli-docs.sh, because the swarm brief forbids them. The
registry reference test supplies the generated CLI documentation check instead.

## This continuation (session 7)

Debug env and attach now consume the final public job and debug-pause routes
through the same authenticated HTTP/api.sock transport. W9's new DebugReader,
DebugJob and DebugPauseResult success shapes live in internal/controllerwire/debug.go
and are recorded in C3. They add no route, grant or foreign contract shape.
Run numbers resolve to stable IDs before the second read. Missing or mismatched
identities, malformed projections and failed pause reads stay errors. Only an
explicit null pause means absent. Public serialization discards unknown private
fields, including those nested in a job or execution. These reads are separate
observations, not a transactional snapshot.

Attachment takes the server's explicit pod, namespace and execution container.
It does not interpret claim-holder names. The existing namespace override stays
available and passes the same Kubernetes-name validator. The caller's kubectl
uses the shared CLI environment builder and keeps its KUBECONFIG while controller
credentials are filtered. JSON output emits the target followed by debug_output
text chunks. Plain and pretty retain raw child stdout. Child stderr stays on
stderr. Command, cancellation and output failures propagate.

Deleted the old env and attach implementations, claim-prefix pod guessing and
their unused parser fields/default namespace. The debug.go diff removes 141
lines and adds six. The retired TestClaimToPod and source-comment scan account
for 38 deleted test lines. The namespace test now exercises the actual final
registry. Tests for resume's required run/job selectors remain. Resume still
uses its preceding writer because C2 has no final release route. Its transport
and complete common-output cutover remain required, not an approved feature cut.

The help synopsis now describes the available public diagnostics rather than
promising environment/working-directory snapshots that were never returned.
Those snapshots still need a W3-owned masked capture contract. The new guide,
CLI reference, OpenAPI and embedded mirrors describe the implemented adapter.
W2 must serve the success projections and derive the current execution target
on its existing routes. Its real handlers, actual local API socket, Kubernetes,
Windows and Darwin execution are unverified. No real kubectl or Sparkwing binary
was run. The Unix fixture is a shell program named kubectl in a private PATH.

The public-projection invariant is that unknown private wire fields never enter
a serialized debug view, and selectors never change the declared route. The
client property run passed 9,925 generated cases. The attachment invariant is
that only validated explicit target fields reach command execution. The final
attachment property run passed 1,526 generated cases with four workers.

The new TestSurfaceDebugObservationTransport passes for HTTP and api.sock endpoint
shapes using a fake transport. It does not complete a native T1 scenario. Focused
CLI tests cover all output formats, resolved identities, explicit absence,
failed reads, target refusal, namespace selection, cancellation and write errors.
A real Unix subprocess fixture verifies argv, execution-container selection,
credential filtering and KUBECONFIG preservation. T1's parked inventory assigns
no original test directly to W9. No native acceptance cell was unskipped.

Whole-repository build, scoped vet, focused CLI/client/acceptance tests, full
clisurface/controllerwire/apispec/apiref/doccheck tests, generated-reference drift,
commentcheck and sleepcheck pass. All Go commands use GOWORK=off,
GOFLAGS=-buildvcs=false, GOMAXPROCS=4 and cache/tmp paths below /tmp/sw-astra-W9.
Tests use -p 4 or less. One fuzz build overlapped a parser edit and failed to
compile an incomplete snapshot. It was rerun after source edits stopped.
The default-team caller inventory test fails on 18 unrelated stale entries,
including retired capacity/profile/concurrency callers. It reports no remaining
debug-env entry. Those foreign inventory rows were not swept in this change.

Shared edits are two internal/apispec/wire.go bindings, generated api/openapi.yaml,
the one removed debug-env entry in pkg/store/default_team_callers_test.go,
tests/acceptance/debug_contract_test.go, CHANGELOG.md, documentation mirrors,
C3, the cross-area handoff ledger and this report. No other foreign caller or
production contract changed. Overwing still reports its stale installed binary.
The lead owns commits, independent review, native integration, pipelines and
launch QA. Scratch is removed before the final report. No Git write occurs.

## Previous continuation (session 6)

Local dispatch consumes engine.ReadPin, VersionForPin and Resolver.Resolve for
both SDKs and the current executable platform. Every invocation checks policy,
including one already running the resolved executable. Matching file identity
ends re-execution. Installed version ordering and ambient recursion markers no
longer choose an engine. The adapter refuses a changed pin, version or platform,
missing verification metadata and relative executable paths. Resolver errors,
policy notices, cancellation and output failures retain their result. Invocation
arguments, cwd and captured environment survive the process replacement.

Info reads the same pin owner without fetching an engine. It reports the exact
required release, including an older pin under a newer CLI. It no longer invents
a toolchain-store path or infers local-mode cache availability. Invalid pin reads
remain visible. Native pin-reader and resolver implementations are still W8 work.
The default bindings call those declared owners; tests inject their results.

The old CLI downloader/cache, Go-only pin reader, permissive selection policy,
legacy digest sidecars and release fallback are removed. The selector shrank
from 418 to 136 lines. The still-used updater identity check moved into its own
44-line file. Three deleted test files account for 828 lines, and deleting the
unused selector matrix removes another 25. Other obsolete production deletions
remove 115 lines across update, toolchain policy, paths and doctor. No old cache
on the user's machine was read or deleted.

The smallest shared caller cleanup removes the unused cache listing from
internal/opsview/doctor.go and its doctor_toolchains_test.go, the old path methods
from internal/paths/paths.go, and the unused selection API/matrix from
internal/toolchain/policy.go and policy_test.go. Mode parsing and version holds
remain with that package. The final public doctor already uses LocalLifecycle.

Environment documentation now matches native reads. It drops retired variables,
names SPARKWING_JOB_ID, and records the moved SDK caller-selected input reader.
Unported host inputs remain identified as such. Versioning and changelog changes
are mirrored with the repository sync script. No public flag, interface shape,
credential, service, cap or deadline was added.

## Verification for this continuation

Invariant: SDK work can start only with the resolver's exact pin, release and
platform result; ambient state cannot bypass verification. The property test
passed 3,196 executions across two runs with four workers. The final run passed
193 executions after the last source changes. These tests check the CLI boundary,
not release signatures or the resolver's native cache.

New tests cover both SDKs and all three platform selectors, mismatch refusals,
resolver denials without fallback, policy checks in the selected file, notices,
context cancellation, write errors and read-only pin summaries. A Unix shell
fixture proves argv boundaries, cwd and captured environment through real exec.
A hard-link fixture proves same-file convergence. Windows/Darwin selectors in
component tests do not establish native platform execution.

Ported concerns: root-flag replay, switch-before-daemon ordering, exact pin
summaries, invalid mode/pin handling, selected-file convergence, and updater
version-query diagnostics. TestInfoReportsInvalidPipelineConfig now supplies a
fake pin owner. Old downloader signature/cache/private-store cases require W8's
native resolver evidence. T1's parked inventory assigns no original test to W9.
No native acceptance cell was unskipped or claimed complete.

All Go commands used GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4, -p 4 or
less, with GOCACHE/GOTMPDIR/TMPDIR under /tmp/sw-astra-W9. Whole-repository build
and scoped vet pass. Focused selector/info/environment-documentation tests pass.
Full clisurface, controllerwire, toolchain and doccheck tests pass. Paths and
focused doctor-rendering tests pass. Commentcheck and sleepcheck against main,
gofmt, embedded-doc drift and diff checks pass.

A broader CLI documentation run exposed stale environment documentation, now
fixed and checked, then reached a socket test that the sandbox refuses. A broad
legacy opsview diagnostic selection was interrupted; focused rendering passed.
Neither broader suite is claimed green. Native source selection, SDK releases,
HTTP/api.sock/host integration, services, timers, cache/publication, Windows,
Darwin and launch QA remain unverified. Scratch is removed before the final report.

Shared edits beyond the caller cleanup above are CHANGELOG.md,
pkg/docs/changelog.md, the two changed embedded documentation mirrors, C3's
engine integration notes, the handoff ledger and this report. No foreign
contract shape changed. Overwing discovery retained its stale-binary notice.

## Retained source preparation

Committed-source preparation preserves separate execution and pipeline commits.
The execution commit supplies configuration and Git.SHA. Its configured source
or the explicit pipeline revision supplies Source.Ref. A declared source pin
and explicit revision must resolve to the same commit. The execution commit's
invoked name/SDK entrypoint pair survives pipeline-source selection and dirty
configuration. Git discovery and preflight share the same captured Git process
projection and native reader.

Committed-source preflight requires origin's remote-tracking refs to contain
both selected commits. An unrelated remote cannot satisfy it. This ports the
existing pushed-source preflight expectations and adds divergent pipeline and
execution history. The legacy preflight tests remain with their connected caller.

Inspection calls source.Acquire for the exact pipeline commit through its declared
interface. It carries repository identity, location, default-branch hint and the
captured process base. The result must preserve repository/commit identity and
supply complete selected-engine metadata for the requested SDK and target. W3/W8
still own pin resolution, signature verification and acquisition implementation.
The selected engine's pipeline describe command runs in the acquired directory.
Its returned alias and entrypoint must match the captured pair before argument
metadata is accepted. The request preparer and existing final-intake component
consume that metadata in a fake-controller test.

Acquisition resources close on success, error and cancellation, including a
partially returned entry and cancellation during cleanup. Policy notices pass
through the existing masked writer after environment registration. A failed write
stops inspection. The existing process
builder and CLI sanitizer filter credentials and mask inspection diagnostics.
A Unix shell fixture proves argv, cwd, captured environment, credential filtering
and secret classification through the real subprocess boundary. It is a fake
engine, not a verified release or native SDK integration.

The public initial trigger still uses its legacy preparation/intake/here path.
No public flag, wire field or foreign contract changed. No new service, cache,
environment variable, timeout, retry loop or authority rule was added. The private
inspection dependencies reuse the source interface and existing command executor.
Native selected-engine process-tree cancellation remains unverified.

## Completed W9 work to retain

- Shared operator limits/object-store commands, final routes and OpenAPI bindings.
- Final clisurface grammar, help, completion and command discovery. Machine-only
  profiles use the strict userconfig schema. Final run/trigger flags are parsed.
- Authenticated engine HTTP/api.sock transport, strict errors, one-time authority
  retry rules, claims, reconciliation, complete failure reports/recovery reads,
  expansion snapshots, result-state identity and cloud award/bootstrap models.
- Run status, paginated listing/tree/steps/receipts, deletion, logs/search/review,
  retry/cancel, statistics, sizing reads, pools, tokens, secrets, runner reads and
  queue/group priority adapters. Fakes cover the final request/projection shapes.
- Sealed here handoff/resume, private retained bearer, controller/socket binding,
  public logs and incomplete-observation results. Native here construction is open.
- Dashboard, daemon control, doctor, foreground wingd and machine-update adapters
  consume final lifecycle contracts. Their native local-services binding is open.
- Typed schedule management preserves explicit empty/zero values, upcoming/history
  projections and keyed launches. Local install/tick composition is open.
- Runner enrollment/private-file and startup-schema adapters, typed issuance and
  removal. Native provisioning/service composition and pool proposals are open.
- Selected-engine upgrade verification, hidden SDK installer, Go/TypeScript local
  preparation, binary-cache hard cut, installer masking and cache warming.
- Protocol v2 schemas, HTTP policy/OpenAPI references and corrected source guides.
  OpenAPI still contains legacy registration inputs and undocumented operations.
- TypeScript Windows bridge build/assembly/publication workflow and immutable npm
  publication checks. Native Windows execution and publication remain unverified.

## Remaining W9 work

1. Connect public initial trigger to source discovery, inspection, request
   preparation and submitTriggeredRun. Bind numeric GitHub identity, captured
   private environment retention and authorized working-tree source upload.
   Initial here intake needs the persisted reason contract below. Delete the
   legacy CreateTrigger/TriggerResponse and old here host after the cutover.
2. Replace local and detached execution with W3's shared RunHost and W8's local
   controller queue. Compose owner actor, claims, source, exact engine, storage,
   plan cache, lease keeper, final logs/signals and target selection. Delete the
   old local host, detached consumer and direct-store plumbing after migration.
3. Compose explicit read-only RemoteCache and publication byte/record copying.
   Publication failure must remain separate from the completed local outcome.
   Retire remaining old storage/client consumers after their callers move.
4. Bind resolveHereRuntime and resolveLocalServices to W3/W8 implementations.
   Preserve controller/socket identity for retained here authority. Complete
   native lifecycle endpoints, managed units, logs and runner startup/refresh.
5. Finish local schedule install/pin/uninstall, captured schedule intake and
   controller-owned __tick. Integrate W1/W2 schedule persistence and projections.
6. Finish remaining legacy client/management retirement, including debug resume's
   direct store and old client paths, and the runtime-guide,
   script/example and OpenAPI audit. Regenerate owning references when public
   behavior changes. Run every native surface/placement cell and launch QA with
   the lead after the owners are implemented.

## Cross-area contracts and bindings still open

- W6/W1/W2: a typed here reason must survive immutable intake and replay.
  CreateRunRequest, IntakeRequest and runspec.Request still lack it.
- W3/W2: native job transitions must send the captured source ordinal through
  W9's completed wire/store adapter and consume Applied.Attempt. C1's committing
  transaction and the shared adapter preserve that ordinal and its replay identity.
- W6: remove the retired SPARKWING_TOOLCHAIN_ACTIVE CLI inheritance allowance,
  retaining its protocol deny classification. procenv.Git is declared, but Build rejects it. Git fixtures inject the
  declared builder boundary. W9 has no alternate environment filter.
- W3/W8: the implemented source acquisition and shared host constructors still
  need native CLI composition. Exact-engine resolution/InstallSDK and the local
  lifecycle entry points remain stubs. Execution-input identity through the
  public placements still needs native evidence.
- W6/W9: seal retained step-window selections in runspec.Options before adapting
  StartAt/StopAt/Only. W2/W3 must bind typed remote step reports. W1/W2/W8 must
  bind launcher reconciliation and UID-preconditioned cleanup to the new W9 wire.
- W1/W2/W7: authorize pre-intake source_bundle upload and publication result-copy
  operations. Existing upload identities require a live owner/publication/job.
- W1/W2: retain RetryRequest.Mode in intake, replay and successful-job reuse.
  Specify authorized sizing reset and preserve explicit resource pins.
- W2: debug resume has no release route in C2. The legacy debug path cannot
  cut over until the route, authority and replay body are specified. The old
  /nodes release route is not a final contract.
- W2: profile scope enrollment, pool-reserving runner provisioning, run/member
  annotation authority, remote admission priority without a group and final read
  projections remain owner integration or route-contract gaps. Serve the new
  OperatorManager projections on existing C2 deployment routes, including reset
  idempotency and C4's final job/schedule setting names.
- W8: managed schedule installation and controller scheduler-pass operations,
  dashboard access/install-force policy, native endpoints and unit refresh remain.
- W7: native remote-cache/storage bindings and final persisted log completeness.
- T1/lead: native HTTP/api.sock, platform/backend/placement drivers, verified
  engine/SDK execution, Windows packaging runs and real-repository launch QA.

These are unfinished integrations, not approved feature cuts. No foreign
contract changed and no new human decision is requested in this continuation.

## Assignment audit

Every amendment and handoff naming W9 was checked. Previously completed work was
retained. The rows below group the remaining bindings without claiming native
acceptance from component tests.

| Assignments | W9 result and remaining integration |
| --- | --- |
| A1-03/08/09/13-18/22/38/55 | Final error/claim/execution/failure/expansion/cloud wire retained. Source-attempt wire/store mapping, typed step reports and award/Job-instance sync wire are complete. Native owners, step delivery and launcher reconciliation remain. |
| A1-10/11/19; identity and alias handoffs | Separate commits, preflight and selected-engine inspection added. Native source/engine/host construction and hello remain. |
| A1-20/39/40/41/49 | Credential, runner and lifecycle adapters retained. Native provisioning, shared runner, local runtime, services and timers remain. |
| A1-29/30/34/37/50 | Signed-data, local-cache, here and prepared-intake components retained. Public cutover, cache/publication and old runtime/client removal remain. |
| A1-43/45/47 | Exact resolver dispatch and read-only pin summaries replace the old CLI selector/cache. Notices, upgrade, settings and guards retained. Native version policy, selected-source hosting and scheduled intake remain. |
| A1-51 | C3 and eight intake transport combinations retain separate source commits. Operator clients and dead debug run are removed. Operator OpenAPI messages are generated. Debug env/attach readers now use final public projections, and their legacy
  store/node paths and pod guessing are removed. Native debug bindings, remaining
  public/runtime retirement and reference audit stay open. |
| A1-52; Windows packaging handoff | Packaging implementation retained. Native Windows and publication evidence remain unclaimed. |
| A1-53 | New native Git and shell-fixture evidence only. No native T1 scenario was unskipped. |

## Previous verification and accounting

Invariant: operator input may change operational request bodies, never the
route or credential that authorizes them. FuzzOperatorMutationsStayOnDeclaredRoutes
passed 10,828 generated executions with four workers and three requested seconds.
Ten new test functions cover final routes, explicit zero, confirmed writes,
malformed responses, canonical refusals, exact replay, all output formats,
preflight, cancellation and transport cleanup. The acceptance transport test
covers both HTTP and api.sock endpoint shapes with fakes. No native cell was
unskipped.

Four retained tests were ported from CLI-owned models to the shared wire:
TestRenderComputeLimitsPlain, TestRenderComputeLimits,
TestRenderComputeLimitsOmitsUnavailableFleetUsage and
TestRenderComputeLimitsShowsTheRequestBudgets. Missing guards no longer acquire
invented zero values. T1's parked inventory assigns no original test directly
to W9. Earlier source-preparation test evidence remains in Git history.

Checks use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4 and cache/tmp paths
under /tmp/sw-astra-W9. Tests use -p 4 or less. Whole-repository build and scoped
vet pass. Full clisurface, controllerwire, apispec and apiref tests pass. Focused
CLI/client, TestSurface, protocol-schema, command-reference and doccheck tests
pass. Commentcheck and sleepcheck against main, gofmt, embedded-doc drift and
diff checks pass. An unscoped sleepcheck also ran and reported 390 existing
wall-clock wait sites. This continuation adds none.

Full legacy CLI/client suites, real services/sockets, verified releases, native
SDKs, Postgres/Darwin/Windows, pipelines and launch QA remain unverified. Native
W2 deployment-route integration is the new open integration point. Existing
Git environment, source, host, storage and lifecycle gaps remain as listed above.
Overwing discovery worked with its stale-binary notice.

Shared edits are internal/apispec/wire.go (four bindings), api/openapi.yaml,
pkg/docs/mirror/limits.md, tests/acceptance/operator_contract_test.go, C3's wire
contract and this report. Other edits are W9-owned source, tests and docs.
No foreign contract or production caller changed. Scratch is removed before
the final report. No Git write occurs.
