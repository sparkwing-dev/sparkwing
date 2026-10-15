# W9 handoff

STATUS: CONTINUE

Current branch: 9b73e2fa5. The previous implementation below is committed.
This continuation changed only this report. No Git write, candidate installation
or Sparkwing executable ran.

## Current continuation: owner implementations are still missing

No source change or new test port was needed for the named test failures.
`TestDaemonLifecycleMissingAndCancelledRuntime` is fixed and passes in the short
suite. `TestProcessLivenessRegressionDoesNotSleep` and the legacy opsview sweep
facade are already deleted. The retained CLI and opsview tests are green under
the diagnostic selection below. Their completed work was not repeated.

The two 12:35 findings remain open. The unchanged production probes fail:

- Local run stops at `resolveLocalRunRuntime`'s missing owner binding.
- Local controller stops at `openLocalController`'s missing elected factory.
- Schedules install stops at `resolveLocalScheduleServices`' missing local service.
- Doctor returns `controller_checked:true`, leaves one session record for the
  pruned run, and emits no `unrecorded_step_outcome` warning.

These failures occur before socket binding. The constructors on this branch do
not supply the missing operations. `NewLocalRuntime` requires `Open`.
`NewNativeHost` requires private state, process authority, source, storage and
logging dependencies. Existing W9 composition tests already invoke that real
constructor with declared fake boundaries. Adding another factory wrapper or
another copy of those fakes would not connect production.

### Contract changes or implementations needed in other areas

1. **W2/W8 local controller:** provide the implementation returned by
   `LocalRuntimeConfig.Open`, including `RunStore`, `Reconcile`,
   `Run(ctx, activityChanged)`, `TypeSafeAPIKey`, `HTTP` and `Close`. The only
   production `LocalController` references are its declaration and consumers.
   `controller.Server.HTTP` supplies handlers, but `ServeWith` owns private
   worker startup and reconciliation and also starts its own TCP listener. It
   cannot substitute for W8's elected runtime and activity reporting.
2. **W2 local peer credentials:** connect the verified peer presentation to
   production authority. `policyStoreReader.ReadAuthentication` forwards
   `CredentialPeer` to `Store.ReadAuthentication`, whose authority lookup accepts
   only bearer and session classes. A concrete HTTP handler alone cannot make
   the local API usable. The CLI must not grant itself a replacement token.
3. **W2/W3 private host operations:** supply authenticated C1-shaped
   `ReadSnapshot`, `ReadNode`, `TransitionRun` and `TransitionNode`, and the
   captured-environment, process-delegation, data/log and owner-policy bindings.
   `cliHostState` already adapts the declared plan, step, finish and renewal wire.
   The public scheduling snapshot does not contain the private host state.
4. **W2/W8 local schedules:** expose controller-owned checkout/environment
   registration, proof/pin transactions and tick with dry-run input. The current
   `ScheduleRepositoryManager` carries remote repository declarations. No local
   registration, pin or tick operation exists on its contract or C2 route table.
5. **W8 doctor:** expose a controller-owned sweep with dry-run input and findings.
   `LocalLifecycle.Doctor(ctx, version)` only inspects status and managed units.
   It cannot receive dry-run or invoke a sweep. The owner must turn failures from
   recording otherwise successful cleanup into `unrecorded_step_outcome`
   warnings, while preserving sweep failures as errors. W9 can then forward
   dry-run and render those findings through the public command.

The brief requires stopping a piece when it needs a foreign contract change.
These requests are recorded here rather than adding W2/W3/W8 behavior in the CLI.
The remaining assignment audit below still applies. No new product decision is
requested, and no foreign contract or lead-owned file changed.

### Fresh verification

All commands used GOWORK=off, GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-gocache, TMPDIR/GOTMPDIR=/tmp/sw-astra-W9 and GOMAXPROCS=4.

- Whole-repository `go build -p 4 ./...` passes.
- Scoped `go vet -p 4` passes for cmd/sparkwing, opsview, clisurface,
  controllerwire and controller/client.
- Full opsview, clisurface, controllerwire and controller/client suites pass.
- `go test -p 4 ./tests/acceptance -run '^TestSurface' -count=1` passes.
  Existing fake-backed cells remain component evidence. No native cell was
  unskipped or newly completed.
- Short CLI diagnostic: **956 pass, 15 skip, 0 fail** at top level, using the
  socket-test exclusion expression recorded below. No assertion or skip changed.
- `go run ./internal/commentcheck -base main .` passes across 2,130 files.
- The production probe command recorded below still fails on the four owner
  gaps above. Its failures are retained and are not treated as passing acceptance.

This continuation added and deleted **0 production Go lines** and touched no
shared source file. W9's delta against main remains **+15,606 / -23,758, net
-8,152**, within its -2,900 budget. No API was removed and no caller migration
was needed. No new test was ported or deleted. Scratch is removed at session end.
The shared build cache is retained. Overwing discovery worked and reported a
stale installed binary. Native socket execution remains unavailable here.

## Previous implementation

Fixed the last two retained short CLI failures without changing their
expectations. TestSDKCheckRejectsFIFO now returns a refusal rather than blocking.
TestSDKCheckRefusesOversizedModuleWithoutWriting now reports the existing 1 MiB
inspection limit instead of returning a 1,048,606-byte parser diagnostic.

The new engine pin path had bypassed the CLI's regular-file and size checks.
All manifest and lockfile reads, including digest reads, now use one internal
engine reader. It checks the opened file's regular type and identity. Unix opens
use O_NOFOLLOW and O_NONBLOCK. The existing Go-module limit applies before parsing
and during reading. Go lockfiles and TypeScript manifests retain their prior
size behavior. No public engine API or foreign contract changed.

Invariant: pin inspection reads regular manifest and lockfile inputs and refuses
linked or special-file inputs. A 100-case generated CLI property covers Go and
TypeScript manifests and lockfiles, with links, dangling links, directories and
FIFOs. A boundary test accepts exactly 1 MiB Go modules, larger go.sum files and
larger TypeScript manifests. Source versioning guidance and its generated mirror
now describe the inspection behavior.

TestDaemonLifecycleMissingAndCancelledRuntime already contains the requested
fix and passes. TestProcessLivenessRegressionDoesNotSleep and the legacy opsview
sweep facade were already deleted in earlier commits. Their work was not repeated.

W9-owned production Go: **0 net lines this continuation**. Versus main:
15,606 added / 23,758 deleted, net **-8,152**, within the -2,900 budget.
The shared engine fix adds 71 / deletes 5 production lines, net **+66**.
No production file or legacy subsystem was deleted this continuation.

## Verification

Commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOCACHE=/tmp/sw-gocache,
GOMAXPROCS=4 and TMPDIR/GOTMPDIR=/tmp/sw-astra-W9. Tests use -p 4.

- Whole-repository `go build -p 4 ./...` passes.
- `go vet -p 4 ./cmd/sparkwing ./internal/engine/... ./internal/opsview
  ./internal/clisurface ./internal/controllerwire ./pkg/controller/client
  ./tests/acceptance` passes.
- Full engine (including npmcmd and sdkinstall), controller/client, clisurface,
  controllerwire, opsview and doccheck suites pass.
- `go run ./internal/commentcheck -base main .` passes across 2,130 files.
- Focused CLI and acceptance checks pass with
  `go test -p 4 ./cmd/sparkwing ./tests/acceptance -run '^(TestCommandReference|TestEmbeddedReference|TestRepoDocs|TestDaemonLifecycleMissingAndCancelledRuntime|TestDoctor|TestCLIOwnerComposition|TestLocalServices|TestLocalRun|TestLocalSchedule|TestSurface|TestHere|TestSchedule|TestSDKPinInspection)' -count=1`.
- Short CLI diagnostic: **956 pass, 15 skip, 0 fail** at top level.
- Windows amd64 cross-build of internal/engine passes. No native Windows
  execution is claimed.
- Docs/mirror synchronization and git diff --check pass.

The diagnostic selection omits the known socket tests because this sandbox
cannot bind sockets. No repository skip or weakened assertion was added.
Reproduce it with `go test -p 4 -short ./cmd/sparkwing -count=1` and this
`-skip` expression:

```
^(TestDocsMigrationsValidatesWebBoundsBeforeFetching|TestDownloadAndInstall_.*|TestDownloadFile_BoundsBodySize|TestMaterialize_ColdCacheContactsNoController|TestResolveSparks_WithManifest_WritesOverlay|TestRunDocsListVersions_Web.*|TestRunDocsMigrations_VersionWebFetches|TestRunDocsRead_Web.*|TestRunUpdateBinary_Failure_SpawnsNoGo|TestRun_PinnedPipelineRunsWithTheNetworkDenied)$
```

Existing fake-backed surface and transport cells pass, including
TestSurfaceRunnerEnrollmentCredentialTransport for HTTP and api.sock endpoint
shapes. No native acceptance cell was newly completed or unskipped. No archived
T1 test was newly ported or deleted. Both retained SDK inspection regressions
now pass through the shared engine reader.

## Production integration: findings 1 and 7 remain open

The unchanged production probes ran without substituting production factories:

```
go test -p 4 -tags rewrite_integration ./cmd/sparkwing -run '^TestReview(ProductionExecutionBindings|PublicDoctorSweepsAndWarnsForPrunedRun)$' -count=1
```

All three composition subtests fail at the explicit local-run, elected-controller
or local-schedule refusal. Public doctor returns success but leaves one pruned
run's session record and emits no unrecorded_step_outcome warning. These failures
do not depend on socket availability. No fake factory was installed as production.

The available constructors remain dependency constructors. NewLocalRuntime
requires LocalRuntimeConfig.Open. No production implementation of
wingd.LocalController supplies its store, queue, reconciliation and workers.
NewNativeHost requires private state, source/engine, environment snapshots,
delegated processes, data/log and owner policy bindings. Existing W9 components
exercise those declared interfaces with fakes.

The default production policyStoreReader forwards CredentialPeer to
Store.ReadAuthentication. Its authority selector handles bearer/session classes
and refuses peers with "credential class has no persisted authentication
authority". W2's peer tests supply a fake reader. Server.HTTP and authenticated
socket transport alone cannot compose a usable local API.

Contract implementations or changes needed in other areas:

1. W2/W8: the concrete elected LocalController constructor, including the
   controller-owned store, queue, orphan reconciliation, workers and HTTP/UI.
   Bind verified local-peer authentication in the production policy reader.
2. W2/W3: private ReadSnapshot/ReadNode/TransitionRun/TransitionNode transport,
   captured environments, delegated process grants, source/engine, data/log and
   owner-policy bindings. Public scheduling projections cannot replace private
   host rows or discard transition extras.
3. W2/W8: controller-owned local schedule registration with checkout/environment
   retention, proof and pin operations, and tick/dry-run execution. Existing
   ScheduleRepositoryManager transports remote repository declarations; it is
   not a local pin/proof/tick contract.
4. W8: a controller-owned doctor sweep with dry-run input and independent
   recording warnings. LocalLifecycle.Doctor(ctx, version) only inspects status
   and units. It has no sweep input or operation. The exit invariant remains:
   sweep success survives an unrecorded outcome, with a warning in the report.

The swarm brief requires foreign contract changes to be reported instead of
made locally. No foreign contract was changed in this continuation. No human
product decision is newly requested.

## Assignment audit and retained work

| Assignments | Implemented W9 boundary and remaining integration |
| --- | --- |
| A1-03/08/09/13-18/22/38/55 | Final errors, claim/execution/failure/expansion/cloud transports, source ordinals, fenced steps and Job-instance sync. Native owner and launcher cleanup integration remains. |
| A1-10/11/19; identity and alias handoffs | Separate execution/pipeline commits, source preflight, resolved alias/entrypoint and selected-engine inspection. Native composition through hello remains. |
| A1-20/39/40/41/49 | Credential, runner enrollment, lifecycle, managed-unit and home adapters. Native controller/runner/service composition remains. |
| A1-29/30/34/37/50 | Signed-data, opt-in cache, sealed here resume and intake components. Public cache/publication/here/local bindings remain. Publication failure must stay separate from the local outcome. |
| A1-43/45/47 | Exact resolver dispatch, notices, upgrades, explicit settings and shared guard grammar. Native selected-source hosting and scheduled intake remain. |
| A1-51 | Protocol v2 schemas, final client APIs, safe public debug readers, source guides and legacy client/host retirement. Native debug resume, remaining runtime and API-reference integration remain. Duplicate credential metadata is removed this continuation. |
| A1-52; Windows packaging handoff | Windows bridge assembly, native workflow and publication dependencies implemented. Native Windows execution and publication remain unverified. |
| A1-53 | Component evidence only. Native placement/backend/platform cells and real-repository QA remain with T1/lead. |
| 07:40 step windows and launcher grammar | Sealed StartAt/StopAt/Only, --docker-image, removal of --deadline and plan awards without a job are already implemented. |
| 07:40 route removals | Final source guide and migration removal details retained. Auth guide route drift fixed this continuation. |
| 12:35 composition and doctor | Production probes remain red, with the missing owner operations listed above. |

Other outstanding A1-50 contracts remain the immutable initial here reason,
authorized pre-intake source bundles and publication, and the debug-resume
release route. Initial trigger preparation still refuses unavailable here,
source-bundle and captured-environment retention. Profile enrollment with scope,
run annotations and relative priority admission also retain their named owner
gaps. No unsupported path was made falsely successful.

## Shared files and friction

Shared production files touched: internal/engine/pin.go,
internal/engine/pin_file.go, internal/engine/pin_file_unix.go and
internal/engine/pin_file_windows.go. The only other shared file is the generated
pkg/docs/mirror/versioning.md. Remaining edits are W9 tests, source docs and this
report. No lead-owned amendment or handoff changed.

Overwing discovery succeeds and reports a stale installed binary. Socket tests
cannot run in this sandbox. The branch still has no concrete LocalController
implementation or controller-owned doctor sweep. Production probes fail at
those explicit gaps. The available constructors take dependencies; their presence
alone does not close production composition. No Git write, Sparkwing executable,
service or installer ran. The temporary directory is deleted at session end;
/tmp/sw-gocache is retained.
