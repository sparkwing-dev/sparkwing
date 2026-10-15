# W9 handoff

W9 remains incomplete. This continuation started clean at 2593f01f8. The lead
owns commits, independent review, pipelines and native integration. Only the
required installed-tool discovery ran. No install, service-management command, pipeline,
release or Git write ran. Earlier work remains in Git history.

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
- W1/W3/W9: job transitions need the source attempt ordinal in the wire request,
  committing transaction and replay identity. The existing handoff still applies.
- W6: remove the retired SPARKWING_TOOLCHAIN_ACTIVE CLI inheritance allowance,
  retaining its protocol deny classification. procenv.Git is declared, but Build rejects it. Git fixtures inject the
  declared builder boundary. W9 has no alternate environment filter.
- W3/W8: source acquisition, exact-engine resolution/InstallSDK, shared host
  constructors, execution-input identity through hello and local lifecycle are
  declared stubs. Inspection fakes do not establish their native behavior.
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
| A1-03/08/09/13-18/22/38/55 | Final error/claim/execution/failure/expansion/cloud wire retained. Native owners and source-attempt fencing remain. |
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
