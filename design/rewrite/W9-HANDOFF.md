# W9 handoff

STATUS: CONTINUE

This replaces accumulated session notes with the current working state at
ff135f757. Prior evidence remains in Git history and the lead's session logs.
No Git write, candidate installation or Sparkwing executable ran.

## This continuation

- Deleted the duplicate TokenInfo and TokenSubject types. Credential rotation
  now decodes through RotatedToken, whose New field uses W2's capability.Metadata.
  The generated OpenAPI response uses that same metadata schema. The runner
  enrollment acceptance fixture uses the owner type too.
- Regenerated OpenAPI. Besides dropping the duplicate credential schemas, it now
  includes the existing ScheduleRepoRequest.mode field and its required presence.
- Fixed the auth guide's route-check failure after the integration merge.
  Active guidance names final intake, plan, transition, claim-heartbeat and
  runner operations. The migration guide retains the removed-route details.
  Synced the embedded guide. No checker exception was added.
- Confirmed the daemon missing/cancelled test fix, source-scanning liveness test
  deletion and legacy opsview sweep-facade deletion were already committed.
  TestDaemonLifecycleMissingAndCancelledRuntime passes. These changes were not
  repeated and their removals are not counted as new work.

W9-owned production Go: 10 added / 33 deleted this continuation, net **-23**.
Versus main: 15,606 added / 23,758 deleted, net **-8,152**, within the -2,900
budget. Tests, testdata and contract declaration files are excluded. No
production file was deleted. OpenAPI shrinks by 62 lines. The auth guide and its
embedded mirror each shrink by 5 lines.

## Verification

Commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOCACHE=/tmp/sw-gocache,
GOMAXPROCS=4 and TMPDIR/GOTMPDIR=/tmp/sw-astra-W9. Tests use -p 4.

- Whole-repository `go build -p 4 ./...` passes.
- `go vet -p 4 ./pkg/controller/client ./cmd/sparkwing ./internal/apispec
  ./internal/opsview ./tests/acceptance` passes.
- Full controller/client, clisurface, controllerwire, opsview, apispec and
  doccheck suites pass. The initial doccheck run failed on the two auth guide
  references fixed above. Its full rerun passes.
- `go run ./internal/commentcheck -base main .` passes across 2,127 files.
- Focused CLI and acceptance checks pass with
  `go test -p 4 ./cmd/sparkwing ./tests/acceptance -run '^(TestCommandReference|TestEmbeddedReference|TestRepoDocs|TestDaemonLifecycleMissingAndCancelledRuntime|TestDoctor|TestCLIOwnerComposition|TestLocalServices|TestLocalRun|TestLocalSchedule|TestSurface|TestHere|TestSchedule)' -count=1`.
- Short CLI diagnostic: **952 pass, 15 skip, 2 fail** at top level. The remaining
  failures are TestSDKCheckRejectsFIFO (subprocess killed after blocking) and
  TestSDKCheckRefusesOversizedModuleWithoutWriting (a 1,048,606-byte parser
  refusal instead of an oversized regular-file refusal). Both reach W8's pin
  reader. Their retained expectations were not deleted or weakened.
- The unrestricted short CLI run panics at httptest.NewServer because this
  sandbox cannot bind a socket. The diagnostic selection omits the known socket
  tests through the command below. No repository skip was added.
- Generated schema drift, docs/mirror synchronization and git diff --check pass.

Reproduce the diagnostic with `go test -p 4 -short ./cmd/sparkwing -count=1`
and this `-skip` expression:

```
^(TestDocsMigrationsValidatesWebBoundsBeforeFetching|TestDownloadAndInstall_.*|TestDownloadFile_BoundsBodySize|TestMaterialize_ColdCacheContactsNoController|TestResolveSparks_WithManifest_WritesOverlay|TestRunDocsListVersions_Web.*|TestRunDocsMigrations_VersionWebFetches|TestRunDocsRead_Web.*|TestRunUpdateBinary_Failure_SpawnsNoGo|TestRun_PinnedPipelineRunsWithTheNetworkDenied)$
```

Existing fake-backed surface and transport cells pass, including
TestSurfaceRunnerEnrollmentCredentialTransport for HTTP and api.sock endpoint
shapes. No native acceptance cell was newly completed or unskipped. No archived
T1 test was newly ported. The retained runner credential fixture was migrated.

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

Shared changes: api/openapi.yaml, pkg/docs/mirror/auth.md,
tests/acceptance/runner_credentials_contract_test.go and this report. Other
changes are W9-owned client source and docs. The acceptance fixture is the only
foreign caller migrated for the removed types. No lead-owned amendment or
handoff file changed.

Overwing discovery reports a stale installed binary but succeeds. Socket tests
cannot run in this sandbox. The branch still lacks the concrete local-controller
and doctor operation claimed by the task handoff. Existing named-interface
components remain the available evidence. No service, setting, environment
variable, timeout, retry policy or new production mechanism was added.
