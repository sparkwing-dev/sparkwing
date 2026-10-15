# W3 remote hosting cut

<!-- design:begin -->

This session removes **1,844 net production Go lines** (250 added, 2,094
removed). W3 removes **1,656 net lines** (245 added, 1,901 removed).
Against main, W3 is **+2,557** (12,103 added, 9,546 removed), **4,857 over**
its -2,300 budget. Counts exclude tests, testdata, acceptance and contract files.
The overage remains unfinished local hosting deletion.

## Implementation and deletion

Initial here hosting now uses the same retained-authority handoff as retry and
resume. It retains the initial bearer before resolving the runtime, claims through
the final Controller interface, validates the sealed award and calls RunHost.Run.
The native resolver still reports unavailable before claiming. Initial submission
still uses the legacy intake client. This is a host-caller cut, not completed
native intake or placement integration.

Deleted cmd/sparkwing/worker.go, HostTriggerRun, claimedNodeHost, hostPodNode,
HostDispatchedNode, the pod SDK installer, controller-backed secret resolver,
trigger run-context reconstruction and default-branch lookup. The source package
and shared host contracts own those concerns. Removed the orphan segment upload,
live pusher and watch registry after their last hosting caller disappeared.

Deleted the legacy scheduler's Dispatch, Resume and ClaimedRowPoll configuration,
remote-result polling, row adoption and resume reconstruction. Deleted Claim,
NewClaim and the legacy HTTP output/OIDC/child proxy. Local host services remain.
Local planning, its scheduler, Server and their layers still need migration.
Session and StartSession were already deleted.

Removed the unused pod fixture and its SDK-side compile-only test. Retained the
shared local placement and guard helpers in pipeline_requires.go and
hosted_guards.go. Moved the protocol trigger-source test and two local test
helpers out of the deleted trigger fixtures. Masking tests exercise maskState and
maskLogger directly instead of constructing the removed pod adapter.

## Test coverage and acceptance

The new TestInitialHereHandoffClaimsAndRunsSharedHost composes typed initial
intake, private bearer retention, final claim, shared host input and cleanup with
the existing scripted controller/host doubles. It checks that neither authority
appears in the receipt. Existing here binding and diagnostic properties remain.

Removed tests for the deleted legacy APIs and their token-table fixtures. Their
retained behavior is covered in the native packages:

- HostingDispatchesUnderKeeperAndJoinsBeforeCleanup, HostingLostLeaseDuringPreparationCannotExecute
  and HostingPreparationFailureStopsKeeperAndClosesPartialSource cover acquisition
  liveness and joined cleanup.
- NativeHostRunsSharedTransportForOwnerAndClaimedWork and NodeExecutorDriftPreventsBody
  cover host composition and drift. Executor retry, cancellation, recovery and
  generator-replay tests cover the replaced pod execution path.
- TestDispatch, TestGeneratorReplay, TestCheckoutPlanning, TestNativeExecutorSteps
  and the sealed environment tests cover adoption, generated work, checkout
  arguments/guards, step selections and process projection.
- logseg sink tests cover dropped lines, incomplete manifests and watched output.
  Native transport binding to that storage remains open.

No new acceptance cell or archived inventory row is claimed complete. Lead
09:40 items 1-3 remain done: host checks stay in nodehost/lease tests, scripted
fakes remain consolidated, and keeper checks are not repeated. Lead 07:40 item 9
still passes TestExpansionPreservesConcurrencyDeclarations.

## Verification

Commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOCACHE=/tmp/sw-gocache,
GOTMPDIR/TMPDIR under worker scratch, GOMAXPROCS=4 and at most four test packages.

- Whole-repository build and scoped vet pass.
- Full nodehost suite passes **490** top-level tests, with no skips. Lease passes
  three, source 21, runners 57 and sdkserve 21.
- Focused CLI here/intake/trigger/profile checks pass, including the new handoff.
- Native host, dispatch and process-authority race checks pass. The here
  preparation identity property passes 128 generated inputs.
- Expansion concurrency mismatch check passes.
- Commentcheck against main, sleepcheck against HEAD, documentation mirror/drift
  and whitespace checks pass.

The local hosting dependency selection cannot execute loopback services because
this sandbox refuses sockets. It also finds four failures reproduced unchanged
with a HEAD source overlay: two guards.reject parser fixtures,
TestHostedRunHandleWaitsForTheRunRow (removed args_json column), and
TestPipelineRequiresFallsBackToTheProjectDefaults (old placement expectation).
Those existing tests remain. This selection is not reported green.

The direct-dependent W8 acceptance selection also fails unchanged on a HEAD
source overlay. TestRunnerAuthorityW8Components, TestRunnerControllerW8Components,
TestRunnerMixedClaimsKeepControllerBindingsSeparateW8 and
TestRunnerStoreLifecycleW8Components use awards without the required granted
runner identity. W8 owns those fixture updates. This selection is not green.

## Remaining integration and contracts

W3 still owns migrating RunHosted and the local trigger consumer to the common
owner host, then deleting their scheduler, Server and layers. Full native
HTTP/api.sock, placement, Windows and Darwin execution remain unverified.

The existing cross-area requests remain:

- W9: a full execution snapshot, including seals, owner, execution rows, accepted
  expansions and result references. ScheduleSnapshot cannot implement OwnerStore.
  The native controller contract also needs the run-start transition.
- W2/W9: production process delegation, storage/source authority, unit renewal
  and state subscription bindings. resolveHereRuntime is still a stub.
- W9: migrate initial trigger intake to typed CreateRun and its sealed receipt.
  The initial here caller only projects the allocated ID and one-time bearer
  into the existing handoff. It does not invent a request seal.
- W8/W9: the selected engine's typed host invocation. InstallSDK is insufficient
  to prove that the selected release owns execution.

No foreign contract signature, customer setting, limit or compatibility API was
added. No new mechanism needs ratification. Restoring this cut would mean
restoring its deleted callers and implementation together.

Shared files touched are cmd/sparkwing/pipeline_trigger.go,
cmd/sparkwing/pipeline_trigger_here.go, cmd/sparkwing/profile_guard_test.go,
cmd/sparkwing/trigger_intake_test.go and the deleted cmd/sparkwing/worker.go and
worker_host_test.go. The orphan internal/sdkserve/fixture_build_test.go is deleted.
The hotfix-runner source page and generated mirror describe the shared handoff.
The C3 protocol contract links this report. No lead-owned amendment or handoff
file changed.

Friction was the socket sandbox, stale legacy parser/schema fixtures and W8
acceptance awards missing granted identity.
The lead owns native integration, review, Git writes and landing. No runtime,
pipeline, installation or release ran. Git identity and worktree config remain
unchanged. Worker scratch is removed after checks join. The shared cache stays.

<!-- design:end -->

STATUS: CONTINUE
