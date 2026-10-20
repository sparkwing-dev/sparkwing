# W8 orphan trigger and pod executor cut

Continuation from `2d002591c`. The old cluster worker, warm-pool dispatcher,
Kubernetes run-host dispatcher and W8 pod builders stay deleted. This session
removes the orphan `HostTriggerRun` chain. No production caller remained.
Its only node executor was `claimedNodeHost`, which called `hostDispatchedNode`
and `hostPodNode`. Those implementations, their stopping-state adapter,
`nodeClaimKeeper`, old pod environment builder, TypeScript installer wrapper,
log wrapper and secret resolver are removed together.

The two old production files contain 805 lines. Another 47 lines of the
stopping/dispatched adapter are removed. The local run still consumes
`guardCheck`, `PipelineRequires` and `protocolTriggerSource`, so those helpers
move to `hosted_policy.go`. Their production behavior is unchanged. The stale
placement test now expects the project defaults for an absent pipeline entry,
as C4 requires. The source-name table and local envelope test helpers survive.
The obsolete pipeline fixture removes another 290 testdata lines.

The remaining `run_node_remote.go` contains only the old repository-admission
and URL helpers. They have tests and no production caller. Their final machine
source-authority binding is still open. `RunHosted` and the local trigger loop
also retain W3's old runtime. This cut does not claim all legacy execution is gone.

## Masking regressions closed during the ports

Invariant: every value registered during source environment construction remains
masked in the selected engine's diagnostics. The generated property failed on
its first seed because InstallSDK created a new mask registry. The in-memory
SDKInstallRequest now carries the source registry. JSON still excludes it.
The source acquisition caller supplies that same registry. The property runs
16 generated secrets through a signed selected-engine process, across the former
1 MiB line boundary. The failure is reproduced before the fix and passes after it.

Invariant: proxy credential fragments remain masked in consumer-visible forms,
without changing the inherited proxy setting. The former pod proxy table found
that the shared builder lost unsplit userinfo and delimiter-separated fragments.
Its single registration path now handles both, retaining encoded and decoded
components. The old masking implementation is deleted. The complete adversarial
table passes against procenv.Build, and its generated local/remote property
passes 1,003 fuzz executions. Missing maskers and malformed proxy hosts still
fail without echoing credentials. A second property varies 32 credential names
and allowed values, checking credential exclusion and operator masking.

These are changes to the existing mask registry and environment builder.
No process, service, setting, environment variable, limit or compatibility path
is added. SDKInstallRequest.Masker is a W8-owned in-memory contract addition.
The source caller and procenv implementation changes are listed below.

## Coverage and acceptance

The detailed replacement map is appended to `contracts/t1-port-inventory.md`.
Direct ports cover installation environment selection, operator credentials,
proxy spellings, installation-output masking and incomplete log manifests.
Final owner, claimed-host, executor, SDK-planning, captured-environment and
reconciliation suites cover the removed fixture's component boundaries.

No native acceptance cell is newly closed. W8 acceptance components pass,
including native-store owner composition and Cloud builder/bootstrap/sync
components. Source, enrollment, HTTP, deployment and unused process services
remain fixture boundaries where those tests declare them. Native generated
retry, memo-artifact aggregation and here-with-secret execution remain QA work.
The retired legacy fixture cannot establish those final placement results.

## Lead decisions and amendment handoff

- 07:40 item 5 remains done. LocalController returns HTTP() LocalHTTP. W2's
  production authorized API/dashboard binding remains open.
- 07:40 item 6 remains done. Launcher accepts --docker-image and --poll and
  rejects --deadline. Cluster and launcher suites pass.
- 07:40 item 10 remains done on W8's side. Sync observations carry operational
  kind/ID, holder, live claim generation, Job namespace/name/UID and node UID.
  Eval source generation is distinct from its live expansion lease.
  **A1-38 is not closed.** EngineController.SyncLauncher delegates to
  LauncherSyncReader.ReadLauncherSync, which has no production store
  implementation. W1/W2 still own transactional authority/award/Job correlation.
- 09:40 item 4 remains done. KeepAliveReasons, LocalPlatformPolicies,
  CloudUnitPolicies and CloudPodInvariants have no Go declarations or callers.

A1-10/12/46/48 advance through the shared mask-registry handoff and removal of
the duplicate pod environment/installer. A1-19/39/51 advance through deletion
of the uncalled trigger and pod assembly. Existing A1-08/09/11/18/26/27/36
claim, seal, identity, source, resource and storage components remain in place.
The approved pending-claim and cancellation-classification mechanisms are
unchanged. Their original regressions and properties pass in the runner suite.
A1-55 here claims retain the named route and one-time bearer behavior.

A1-20/49 still need production enrollment/provisioning and failed-enrollment
revocation bindings. A1-40/41 retain elected store/queue/maintenance composition,
readiness, activity holds, drain and managed units. Production State observation,
peer-authorized HTTP handlers and foreground Config.Bind remain open.
A1-42 still needs deployed KubernetesBoundary.Probe/Clear bindings and real
network, aggregate scratch and complete teardown evidence. A1-43/52 retain signed
engine/release behavior and the native Windows packaging/inheritance obligation.
A1-45/50/53 retain shared settings, final CLI composition and native QA work.

## Foreign contracts and real integration needed

W3 needs to expose the native hosting entry points for Cloud plan/eval units,
including adoption of the init container's checkout. NewNativeHost currently
returns trigger ownership, claimed node execution and an owner evaluator.
W8 cannot treat a controller-owned plan/eval claim as trigger ownership.
This cut does not change W3's contract to make that substitution.

Remaining integrations are production remote NativeHostStore, source authority,
per-process delegation, fresh per-attempt storage, runner/launcher CLI factories,
the elected local State and HTTP services, W1's activity/incompatible-store
projection, W1/W2 launcher synchronization and native platform/deployment proof.
Existing contract interfaces are exercised with fakes where their implementations
remain unavailable. No foreign type declaration or schema is changed here.

## Production size

Against starting HEAD, production Go adds **94** lines and deletes **874**.
**Net production delta: -780 lines.** W8 is **+4**, shared W3 cleanup is **-794**,
and the shared W6 builder is **+10**. Tests, testdata, acceptance harness and
contract declaration files are excluded. The size calculation includes the
untracked hosted_policy.go helper file.

Against main, W8 is **+4,169 production lines**, which is **6,469 over** its
-2,300 budget. This remains incomplete. The signed-engine distribution,
Windows transport and local/service compositions account for additions, but
this session does not establish that nothing smaller works. No budget waiver
is requested. The orphan host deletions count toward W3 in the lead's ownership
measurement, so they do not reduce W8's measured overage.

## Verification

Checks use GOWORK=off, GOFLAGS=-buildvcs=false, GOCACHE=/tmp/sw-gocache,
GOTMPDIR/TMPDIR=/tmp/sw-astra-W8, GOMAXPROCS=4 and package concurrency at most four.

- Whole-repository go build -p 4 ./... passes.
- Full runners/..., cluster, engine/..., source/..., procenv and
  wingd/localcontroller suites pass.
- W8 acceptance and TestLauncherCloud components pass.
- Focused final native-host, claimed-host, owner, executor, SDK-planning,
  environment, policy and reconciliation tests pass.
- Retained local placement/source-name tests, CLI profile guards, keeper
  properties and shared log-sink tests pass.
- Focused vet passes over changed packages and their direct consumers.
- Full runners, engine, procenv and source suites pass under the race detector.
- Proxy masking fuzz completes 1,003 executions.
- commentcheck -base main . is clean. Git diff --check passes.

## Shared files and friction

Shared production files deleted: internal/orchestrator/trigger_host.go and
pod_node_host.go. Shared production files changed: run_node_remote.go,
internal/source/acquire.go and internal/procenv/build.go. Shared production file
added: internal/orchestrator/hosted_policy.go.

Shared tests deleted under internal/orchestrator: pod_env_test.go,
pod_node_env_test.go, pod_node_host_test.go, proxy_mask_test.go,
run_node_verb_heartbeat_test.go, trigger_host_test.go, trigger_host_args_test.go,
trigger_host_generators_test.go, trigger_host_here_test.go,
trigger_host_yaml_test.go and trigger_run_context_test.go. The orphan
orchestrator/testdata/podnodefixture/main.go is deleted. Shared test additions
are checkout_fixture_test.go and hosted_policy_test.go. Shared test edits are
pipeline_requires_test.go, internal/source/acquire_test.go,
cmd/sparkwing/profile_guard_test.go and internal/storage/logseg/sink_test.go.
Shared documentation edits are c3-engine-version.md and t1-port-inventory.md.
W8 edits are engine/install.go, its tests, runner environment/proxy test ports
and HOSTING.md. Lead-owned handoffs and amendments are untouched.

Overwing reports its installed binary behind source but matches and reads
standards successfully. The stale local placement expectation was corrected to
C4's defaults rule. Git has no local identity or core.worktree override.
No Sparkwing binary, pipeline, deployment, release or Git write was run.
Worker scratch is removed at the end. The shared build cache is retained.
