# W9 continuation handoff

W9 remains incomplete. This continuation starts at `dd87173cc`. Earlier work is
committed. These changes remain uncommitted for lead review. The assigned
worktree was reused. No Sparkwing executable, service or Git metadata was
changed. Scratch is removed before the final report.

## Earlier work retained

Earlier commits supply final grammar, help/completion/discovery, local run and
trigger parsing, machine-only connection profiles, authenticated engine
transport, run observations/listing/prune/log/review adapters, statistics,
sizing reads, pools, separate team/deployment token managers, dashboard lifecycle
adapters, staged upgrade verification, typed secrets and runner observations.
Profile removal requires confirmed member-token revocation.

Schedule management already uses typed `/api/v1/schedules` routes. It preserves
empty/zero overrides, resolves selections before mutations, retains controller
upcoming/history projections and keys immediate launches. Schedule wire models
live in controllerwire. Runner credential issuance/removal already uses typed
TokenManager operations, explicit runner kind, exact grants, one idempotency key
and safe receipts. Removal validates controller/name before stopping a service
and revoking its credential. Do not redo these completed pieces.

## This continuation

Machine update now parses clisurface directly. Removed `--cli`, `--sdk` and
`--check` stay rejected. `--dry-run` retains the read-only comparison, operator
hold, provenance and output behavior. Canonical version/output validation and
help run before dependency resolution. Preview does not construct a managed-unit
manager or install anything.

A real update resolves the existing ManagedUnitManager contract before replacing
software, performs the existing verified release installation, then calls
`Refresh(ctx, installedPath, installedVersion)`. The receipt adds
`managed_units: "refreshed"` only after that operation succeeds. A current CLI
also refreshes its units, so a repeated update can finish a previous partial
refresh. A refresh error produces no success receipt and names the installed
version/path plus doctor and retry commands. Unconfirmed or conflicting installed
identity cannot be handed to managed units. Renderers now return writer errors.

Dashboard and update share `resolveLocalServices`. Its concrete construction
remains unavailable because W8 has not supplied the runtime/unit/log binding.
This is an explicit integration point, not a completed native update. No W8
contract shape changed. The updater verification/atomic-install implementation
is retained. No new environment variable, timeout, service, compatibility path
or permanent product restriction was added.

The legacy Client cron API had no production caller. It and its five request/
response models are deleted. Final ScheduleManager/ScheduleRepositoryManager
cover the operations. Bounce reads/consumption and automatic-retry reset are also
removed from Client. Removing bounce methods required deleting the remote-client
case and its import from `internal/orchestrator/hosted_run.go` (three lines).
The old local bounce implementation remains W3-owned. The orphaned deleteJSON
helper and runUpdateBinary wrapper are deleted.

The update guide documents preview statuses, managed refresh and partial failure.
Security documentation now describes build-identity inspection without executing
downloaded bytes. Sidebar, embedded copies, changelog and the client API snapshot
are synchronized.

## Deleted and ported

Deleted whole files total **356 lines**:

- `pkg/controller/client/bounce.go`: 51.
- `pkg/controller/client/crons.go`: 156.
- `pkg/controller/client/crons_test.go`: 149.

Tracked Go deletions total **479 lines gross** against this continuation's
starting HEAD, including replaced parser and fixture lines. This is not a net
reduction claim. Thirteen exported Client methods and five cron models are gone.

The three original cron tests map to the final proofs:

- `TestClientCronsPushListAndRead`: retained repository-management and schedule
  transport tests, plus `TestScheduleManagerRetainsUpcomingAndEffectiveCadence`
  for health counts, multiple rows, effective queue/catch-up/args and due instants.
- `TestClientCronsDrivesOneSchedule`: retained TestSurfaceSchedulesTransport and
  repository-management tests cover pause/resume, keyed launch, deletion and
  withdrawal. `TestScheduleManagerConfirmsCronOverrideAndDeclaredReset` retains
  distinct declared/effective cron values and reset.
- `TestClientCronsReportsAMissingSchedule`:
  `TestScheduleManagerReturnsStructuredNotFound` verifies the selected route,
  single error unwrap, returned code and lack of retries.

These use real HTTP client code with fake transport and server projections.
The missing-schedule test uses the existing decodeError injection because W2's
apierr.DecodeInner still panics. It does not prove real decoding or persistence.
The old assertion that immediate launch writes a store row remains W1/W2 native
integration work; a fake cannot establish that fact.

Machine updater regressions use final flag spellings. Their metadata fixture
uses fake release lookups rather than a socket listener. Twelve new update
command tests cover ordering, failure, recovery, identity confirmation, dry-run,
argument preflight, help, output selection and writer errors. A guide test parses
its shell examples. T1's parked inventory assigns no original function directly
to W9.

## Verification

Use GOWORK=off, GOFLAGS=-buildvcs=false, GOCACHE=/tmp/sw-astra-W9/cache and
GOTMPDIR/TMPDIR=/tmp/sw-astra-W9/tmp. Recreate the children before running checks.
Tests use -p 4 at most.

Passed:

- Whole-repository `go build -p 4 ./...`.
- `go vet -p 4` for cmd/sparkwing, controller/client, internal/orchestrator,
  tests/acceptance and pkg/docs.
- Full clisurface, controllerwire and tests/acceptance contract suites.
- Client tests matching `^TestSchedule`.
- CLI tests matching `^TestUpdateCommand|^TestUnifiedUpdate|^TestUnifiedSameLabel|
  ^TestRunUpdateReports|^TestClassifyDowngrade|^TestInstalledArtifactIdentity|
  ^TestUpdateMetadata|^TestVersionUpdate|^TestVersionCard|^TestVersionHold|
  ^TestUpdateDoesNotExecute|^TestVerifyReleaseAsset|^TestInstallVerifiedAsset|
  ^TestUpdateGuide|^TestCommandReferenceMatchesRegistry|^TestDashboard` with -short.
- Pkg/docs mirror/changelog/link checks, sync-docs --check, commentcheck,
  sleepcheck and git diff --check.

TestSurfaceGrammarContract now also refuses the removed update flags.
TestSurfaceRemovedClientRecoveryAPIs proves the thirteen methods are absent.
The final schedule transport and projection tests pass. Native T1 surface cells
remain skipped. No complete native acceptance cell is claimed here.

The initial broad updater run failed when its old metadata fixture attempted a
sandbox-denied TCP listener. Replacing that external lookup fixture with fakes
allowed its tests to run. An initial missing-schedule test reached W2's decoder
stub; it now uses the existing decoder seam. No production workaround was added.
Full legacy CLI/client suites, socket/service/native execution, pipelines and
launch QA remain for the lead. An earlier internal/crons suite failed final-schema
repo_key requirements and inherited Git signing; it was not rerun here.

## Remaining W9 work

1. Compose W3 hosting with W6 sealed requests/settings. Publication, cache-from,
   target selection, here resume and pipeline-ref acquisition remain unfinished.
   Detached execution still starts the removed consumer entrypoint. Move it to
   the local-controller queue, then delete consumer/process helpers.
2. Compose W8 runtime, lifecycle, managed units and canonical logs. Bind
   resolveLocalServices. Finish hidden wingd arguments, doctor, local schedule
   install/pin/uninstall and __tick. The update orchestration is done. Native
   refresh/restart verification remains open.
3. Implement W2 schedule projections and W1 writes, including alias selection,
   history/upcoming, masked args, exact override presence and keyed launches.
   The old Client cron API and its tests are now deleted. The CLI's local
   direct-store schedule paths still require replacement after runtime binding.
4. Compose W6's final machine schema/private credential directory with W8's
   runner service/start/refresh. Delete the CLI's agentFileConfig, agentconfig
   and agentservice plumbing. Integrate W2 registration and pool reservations.
5. Replace profile.Load/Save and connection selection with W6's machine parser
   and persistence. Profile scope enrollment remains a W2 gap.
6. Migrate queue, cache warm and remaining legacy client/management surfaces.
   Finish guides, scripts, examples and runtime tests with removed vocabulary.
7. Execute every native surface cell and launch QA after owner integration.

## Contract changes needed and real integration points

No contract in another area changed here. Existing gaps remain:

- W1/W2: failed/all retry selection and authorized sizing reset that clears
  learned samples/floors while preserving explicit resource pins.
- W2: mixed trigger/job claims, profile scope enrollment, run annotations and
  ordinary-member annotation authority. C3 projections and apierr.DecodeInner
  need implementation. Secrets need encrypted/restricted persistence/enforcement.
- W2: team-admin runner registration with pool reservations. TokenCreate has no
  reservation proposal. Granted authority stays distinct from labels.
- W8: concrete runtime/unit/log construction, access-policy/install-force fields
  and effective endpoints. ManagedUnitManager has no schedule installation seam.
  A stable __tick needs a controller-owned scheduler-pass operation. Doctor must
  use the local controller instead of opening a second writing store. Native
  Refresh must preserve schedules and restart an installed controller.
- W3/W6/W8: real local/here/detached hosting and final runner startup composition.
- W7: persisted log cursors and final completeness.

No new human decision is requested. Frozen C4 update grammar supersedes the
older standard-init-update card's --cli/--check examples.

## Shared files and friction

Outside W9 production ownership, only
`internal/orchestrator/hosted_run.go` changed (three-line compile migration after
removing the remote bounce API). Shared changes are tests/acceptance, this
handoff, CHANGELOG.md, .apidiff/pkg_controller_client.txt, pkg/docs/changelog.md
and generated documentation mirrors. All other source changes are W9-owned.

Friction: W8 construction, W2 decoding/projections and native execution remain
unfinished. Standards discovery reports an older installed Overwing binary,
but matching/showing worked. The lead owns review, commits, gates and landing.
Git identity remains Korey Gambill and core.worktree is unset.
