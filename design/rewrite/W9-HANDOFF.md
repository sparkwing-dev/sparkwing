# W9 handoff

STATUS: CONTINUE

This replaces accumulated session notes with the current working state at
a54e7aff3. Prior evidence remains in Git history and the lead's session logs.
No Git write, candidate installation or Sparkwing executable ran.

## This continuation

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
