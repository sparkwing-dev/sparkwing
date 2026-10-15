# W9 handoff

STATUS: CONTINUE

Base: 4e497c640. Production composition and doctor cleanup remain open.
The owner constructors on this checkout still require missing dependencies.
No production factory was replaced with a fake. No Git write, Sparkwing
executable, service or installer ran.

## This continuation

Changed the doctor regression in
`cmd/sparkwing/review_integration_unix_test.go` to invoke public `runDoctor`
with an isolated home and the real local-services resolver. It no longer
substitutes the runtime, lifecycle or machine inspector. It closes the fixture
store before invoking the command, decodes the JSON report and checks the
`unrecorded_step_outcome` finding and completed controller diagnostics.
The test remains under `rewrite_integration` because production is incomplete.

Before this change, the existing probe reproduced the cleanup failure without
sockets: doctor returned `controller_checked:true`, left the pruned run's session
record and emitted no warning. The public probe now reaches the actual lifecycle
client and fails because this sandbox refuses its Unix socket connection.
That is a native execution limit, not evidence that cleanup works.

The production execution probes still fail before sockets:

- Local run: `local run integration requires the local controller and shared host binding`.
- Local controller: `elected local controller factory is unavailable in this build`.
- Local schedules: `local schedules require the local schedule service factory`.

`TestDaemonLifecycleMissingAndCancelledRuntime` was already fixed and passes.
`TestProcessLivenessRegressionDoesNotSleep` and the legacy opsview sweep facade
were already deleted. No finished test port or deletion was repeated.

## Contract implementations needed in other areas

1. **W2/W8 local controller:** implement the object returned by
   `LocalRuntimeConfig.Open`. It must supply `RunStore`, orphan reconciliation,
   workers and activity reporting, `TypeSafeAPIKey`, HTTP/UI and joined cleanup.
   `NewLocalRuntime` requires this callback. There is still no production
   implementation of `wingd.LocalController`. `Server.ServeWith` starts its own
   TCP listener and private workers and cannot be used as this object.
2. **W2 peer credentials:** bind verified local-peer identity to production
   authority. `policyStoreReader.ReadAuthentication` forwards `CredentialPeer`
   to the store. `authenticationAuthorityTx` accepts only bearer/session classes
   and refuses the peer. W2's peer fixtures supply a fake reader. The CLI cannot
   mint itself a replacement bearer to bypass this gap.
3. **W2/W3 native host:** provide authenticated private `ReadSnapshot`,
   `ReadNode`, `TransitionRun` and `TransitionNode` operations and concrete
   captured-environment, process-delegation, data/log and owner-policy bindings.
   `cliHostState` already adapts the declared plan, step, finish and renewal wire.
   `NewNativeHost` consumes these dependencies. The public scheduling projection
   cannot replace private execution state or discard transition extras.
4. **W2/W8 schedules:** expose local checkout/environment registration, proof and
   pin transactions, and tick with dry-run input. `ScheduleRepositoryManager`
   carries remote repository declarations. Its contract and C2 routes have no
   local registration, proof, pin or tick operation.
5. **W8 doctor:** expose controller-owned cleanup with dry-run input and findings.
   `LocalLifecycle.Doctor(ctx, version)` only reads status and managed units.
   It has no sweep operation or dry-run argument. Sweep success must survive
   recording failures as `unrecorded_step_outcome` warnings. Sweep failures must
   remain errors. W9 can forward and render that operation once declared.

The swarm brief requires reporting foreign contract changes instead of making
them in W9. These are the same missing operations found in the previous session,
verified against this checkout. No new product decision is requested. Existing
fake-backed native-host composition tests remain component evidence.

## Verification

All Go commands used GOWORK=off, GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-gocache, GOMAXPROCS=4 and TMPDIR/GOTMPDIR=/tmp/sw-astra-W9.
Tests use at most -p 4.

- Whole-repository `go build -p 4 ./...` passes.
- Scoped vet passes for cmd/sparkwing, opsview, clisurface, controllerwire and
  controller/client. The integration-tagged CLI also passes vet.
- Full opsview, clisurface, controllerwire and controller/client suites pass.
- Short CLI diagnostic: **956 top-level pass, 15 skip, 0 fail**, with the socket
  exclusions below. No assertion or repository skip changed.
- `go test -p 4 ./tests/acceptance -run '^TestSurface' -count=1` passes.
- `go run ./internal/commentcheck -base main .` passes across 2,130 files.
- Production execution and public doctor probes remain red as described above.
- An unnecessary full acceptance-package run was interrupted. It supplies no
  acceptance result. No whole-repository test run was started.

The short CLI diagnostic uses `-short -count=1` and this `-skip` expression:

```
^(TestDocsMigrationsValidatesWebBoundsBeforeFetching|TestDownloadAndInstall_.*|TestDownloadFile_BoundsBodySize|TestMaterialize_ColdCacheContactsNoController|TestResolveSparks_WithManifest_WritesOverlay|TestRunDocsListVersions_Web.*|TestRunDocsMigrations_VersionWebFetches|TestRunDocsRead_Web.*|TestRunUpdateBinary_Failure_SpawnsNoGo|TestRun_PinnedPipelineRunsWithTheNetworkDenied)$
```

Production probe invocation:

```
go test -p 4 -tags rewrite_integration ./cmd/sparkwing -run '^TestReview(ProductionExecutionBindings|PublicDoctorSweepsAndWarnsForPrunedRun)$' -count=1
```

No native acceptance cell was newly passed or unskipped. No new archived test was
ported. This continuation adds/deletes **0 production Go lines**. W9 versus main
remains **+15,606 / -23,758, net -8,152**, within its -2,900 budget.
No API was removed, no callers in another package needed migration, and no shared
source file or lead-owned contract file changed. Only the tagged W9 regression
and this report changed. No production mechanism was added.

## Assignment audit and retained work

| Assignments | Implemented W9 boundary and remaining integration |
| --- | --- |
| A1-03/08/09/13-18/22/38/55 | Final errors, claim/execution/failure/expansion/cloud transports, source ordinals, fenced steps and Job-instance sync. Native owner and launcher cleanup integration remains. |
| A1-10/11/19; identity and alias handoffs | Separate execution/pipeline commits, source preflight, resolved alias/entrypoint and selected-engine inspection. Native composition through hello remains. |
| A1-20/39/40/41/49 | Credential, runner enrollment, lifecycle, managed-unit and home adapters. Native controller/runner/service composition remains. |
| A1-29/30/34/37/50 | Signed-data, opt-in cache, sealed here resume and intake components. Public cache/publication/here/local bindings remain. Publication failure must stay separate from the local outcome. |
| A1-43/45/47 | Exact resolver dispatch, notices, upgrades, explicit settings and shared guard grammar. Native selected-source hosting and scheduled intake remain. |
| A1-51 | Protocol v2 schemas, final client APIs, safe public debug readers, source guides and legacy client/host retirement. Native debug resume, remaining runtime and API-reference integration remain. Duplicate credential metadata was already removed. |
| A1-52; Windows packaging handoff | Windows bridge assembly, native workflow and publication dependencies implemented. Native Windows execution and publication remain unverified. |
| A1-53 | Component evidence only. Native placement/backend/platform cells and real-repository QA remain with T1/lead. |
| 07:40 step windows and launcher grammar | Sealed StartAt/StopAt/Only, --docker-image, removal of --deadline and plan awards without a job are already implemented. |
| 07:40 route removals | Final source guide and migration removal details retained. Auth guide route drift was already fixed. |
| 12:35 composition and doctor | Production probes remain red, with the missing owner operations listed above. |

Other outstanding A1-50 contracts remain the immutable initial here reason,
authorized pre-intake source bundles and publication, and the debug-resume
release route. Initial trigger preparation still refuses unavailable here,
source-bundle and captured-environment retention. Profile enrollment with scope,
run annotations and relative priority admission also retain their named owner
gaps. No unsupported path was made falsely successful.

## Friction and next handoff

The installed Overwing binary reports that it is stale. Socket execution is
restricted here. The constructors present on this branch are dependency
constructors, and do not supply the missing controller, credential, host,
schedule or cleanup operations. A newer owner implementation, or a lead-owned
cross-area contract change, is required to finish the corresponding wiring.

Scratch `/tmp/sw-astra-W9` is removed at session end. The shared build cache
`/tmp/sw-gocache` is retained.

STATUS: CONTINUE
