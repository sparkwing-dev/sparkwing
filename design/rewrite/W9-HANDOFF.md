# W9 prepared initial-trigger intake continuation

Started clean at 868e22d62. W9 remains incomplete. No Git write, Sparkwing
executable, service, pipeline or release ran. Writes stayed in this worktree and
/tmp/sw-astra-W9, deleted before reporting. The lead still owns commits and
native integration.

## Implemented component

`submitTriggeredRun` consumes a fully prepared C3 CreateRunRequest through the
shared Controller interface. It submits once, preserves a supplied idempotency
key, generates one when absent, and rejects invalid output and detached here
execution before intake. The transport retains responsibility for C2's retry
rules. The component adds no retry loop.

It verifies the returned first seal, initial run identity and pipeline before
using the response. A fresh here response requires a bearer; replay requires its
absence. The complete private response reaches the retaining here handoff.
Cancellation during intake still hands a received bearer to that owner before
returning. The real private-file regression proves retention without starting
a host after cancellation.

Receipts carry run ID, allocated team number, replay, an observed terminal
outcome and a masked error. Detached submission never follows. Missing,
unfinished or failed observation returns exit 3. An observed failed or cancelled
run retains its recorded verdict and nonzero exit. The existing shared masking
wrapper protects requested values, returned execution arguments and the here
bearer in messages, hints, details and detail keys. No seal is serialized into
the receipt.

This is a prepared-request component, not a completed public-command cutover.
`runPipelineTrigger` still reaches its preceding preparation/intake/here path.
No new public behavior, compatibility path, flag, timeout or authority rule was
introduced. The component adds 128 production lines.

## Contract changes needed and remaining bindings

- W6/W1/W2/W9 must preserve the required here reason. CreateRunRequest,
  IntakeRequest and runspec.Request have no reason field. Initial here intake
  cannot record `--reason` through those contracts. The reason must survive the
  immutable intake and replay. This is recorded in contracts/handoffs.md.
  No foreign contract was changed, and no argument/env/actor workaround was added.
- Native preparation still needs canonical repository discovery, the exact
  selected execution/pipeline sources, captured submit-environment linkage and
  authorized signed source-bundle upload. GitHub identity needs a numeric ID;
  it cannot be substituted with a slug. These remain preparation work, not
  evidence established by the transport fake.
- Connect the component to the public command after that preparation and the
  reason contract are complete. Bind its here callback to handoffHereRun and
  its follower to the final public log/status readers. W3's shared native host
  and W8's local lifecycle constructor remain unavailable in this checkout.
- The previous source-attempt fencing request, publication/cache composition,
  local/detached-host retirement, schedule pass, effective endpoints and native
  launch evidence remain open as recorded below.

No old code was deleted: **0 lines**. Its callers were not migrated because the
public cutover cannot drop the required reason. No production file outside W9
ownership was touched.

## Acceptance, tests and verification

Invariant: initial-trigger receipts and diagnostics expose neither requested
argument values nor one-time here authority, including encoded diagnostics.
FuzzInitialTriggerDiagnosticsExcludePrivateInputs passed four seeds and 1,341
executions over five requested seconds with four workers. It includes Unicode,
quotes, control characters, encoded hints, overlapping values and detail keys.

Eight new CLI tests cover detached submission, private handoff, preflight,
invalid receipts, recorded/unknown outcomes, incomplete here receipts, cancelled
handoff retention and failure propagation. These port detached/follow/preflight
expectations to the final interface; the preceding tests remain with their
still-connected command. T1's parked inventory assigns no original function
directly to W9. No native scenario was unskipped.

TestSurfaceInitialTriggerIntakeTransport passes the remote-HTTP and api.sock
transport-fake cells for ordinary and here requests. It verifies the route,
exact request, idempotency key and complete first seal/bearer round trip. It
does not establish native socket, authority, source or store behavior.

All Go commands used GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-astra-W9/cache and GOTMPDIR/TMPDIR=/tmp/sw-astra-W9/tmp.
Tests used -p 4 or less. Passed on the final production source:

- Whole-repository go build -p 4 ./....
- Vet on cmd/sparkwing, clisurface, controllerwire, controller/client and
  tests/acceptance. CLI and acceptance vet were repeated after the code changes.
- The eight CLI intake tests and fuzz seeds, plus the generated property run.
- Full clisurface and controllerwire tests.
- Acceptance TestSurface and TestProtocolSchema selections, including the new
  transport cell. internal/acceptance compiles and has no test files.
- commentcheck -base main, clean across 1,302 Go files, sleepcheck -base HEAD,
  gofmt and git diff --check.

The initial compile failed before the component existed. The cancellation
regression then reproduced a discarded received bearer before the handoff was
ordered first; the missing-fresh-bearer regression reproduced an invalid handoff
before presence was checked. Both pass. The first transport fixture omitted
required seal fields; using the real sealed response fixed that fixture.

A broader acceptance invocation hit its 90-second timeout in
TestProtocol/protocol-additive-fields-and-child-handles/typescript_native_pipes.
That invocation is not a broad acceptance pass. The corrected W9 selection
passes. No corresponding child process was visible in the sandbox process
sweep. No full CLI/client/native suite, pipeline or release was run.

## Amendment and handoff audit

All amendments-1 rows naming W9 and the W9 handoffs were checked against the
preceding ledger. No completed implementation was redone.

| Rows / handoff | This continuation and remaining integration |
| --- | --- |
| A1-03/08/09/13/14/15/16/17/18/20/22/38/55 | Retained final errors, claims, reconciliation, failures, expansion and cloud wire. Native owner composition and source-attempt fencing remain. |
| A1-10/11/19; execution and alias identity | Initial intake component preserves the prepared request and returned seal. Native source preparation, selected-engine construction and alias execution remain. |
| A1-29/30/34/37 | Retained signed-data transport. Native read-only cache, publication copying and legacy storage-consumer retirement remain. |
| A1-39/40/41/49/50 | Prepared initial submission, receipt and handoff composition is implemented with fakes. Public cutover waits for preparation and the reason contract. Local/detached hosting, native lifecycle/runner construction and scheduler seams remain. |
| A1-43/45/47 | Prior notice, upgrade, settings and guard work is retained. Native engine/index and scheduled-intake composition remain. |
| A1-51 | C3 documents the component and its native limits. Initial-trigger/runtime retirement and future regenerated surface docs remain. |
| A1-52; Windows packaging | Prior packaging work is retained. Native Windows publication evidence remains unverified. |
| A1-53 | New transport/component evidence only. T1/lead still own native topology/backend/platform drivers and real-repository QA. |

Shared files touched are tests/acceptance/trigger_contract_test.go,
contracts/c3-controller-wire.md, contracts/handoffs.md and this report.
No foreign API caller migration was needed. The required here-reason field is
the new contract friction. The broad acceptance timeout and cold instrumentation
build are verification friction, not evidence of a W9 runtime defect. Git still
reports Korey Gambill and no core.worktree setting.

---

# W9 retained-event cursor continuation

Started clean at 9ddb98836. W9 remains incomplete. No Git write, Sparkwing
executable, service, pipeline or release ran. Writes stayed in this worktree and
/tmp/sw-astra-W9, deleted before reporting.

## Implemented

- daemon events now consumes clisurface, including repeated/comma-separated kinds,
  day/week lookbacks, positive limits and opaque cursors. Offset and zero-limit
  paths are removed. The default remains the newest 50 matching records, with
  chronological order inside each page.
- Continuation binds the journal directory, normalized selectors, original since
  cutoff and exact unread prefix. Newer appends do not shift older pages. Deleted,
  reordered, edited or late-inserted unread records fail with a restart hint.
  The token contains hashes, a cutoff and a count. It grants no authority and
  creates no cursor store. Output format and page size may change on continuation.
- JSON emits event rows and a final page record, including empty/final pages.
  Human continuation goes to stderr. Explain retains the complete related
  timeline and descendant traversal, without list filters or page metadata.
  Read, output and diagnostic failures propagate. Reads still work from retained
  files without starting a controller or opening the runs store.
- Help renders each declared numeric minimum. It previously said one for every
  bound, including incarnation zero. Source guides, migration, changelog,
  generated references and embedded mirrors are synchronized. The admission
  guide's obsolete machine-reader warning is corrected against the merged reader.

## Tests, ports and deletions

Invariant: pagination returns each record from the initial retained selection once,
while newer appends leave its unread prefix intact. Changed history fails loudly.
FuzzDaemonEventsPagesPreserveRetainedHistory passed its seeds and 3,041 generated
cases with four workers over three seconds. FuzzDaemonEventCursorDecode passed
4,490 cases with four workers, including a valid canonical-token seed.

The real-journal TestDaemonEventsCursorKeepsHistoryAcrossAppends fails against
the preceding daemon.go through a Go overlay with missing final page metadata.
It passes on the final implementation. The help minimum regression failed with
Must be at least one and passes with the declared zero bound.

Ported the default-window, filtered-pagination, empty-journal and skipped-record
regressions to the final metadata/cursor shape. Existing explain descendant
traversal, admission wording and lifecycle tests remain active.
Added changed-history/filter/directory, fixed-cutoff, explicit-format,
help/preflight and read/writer-failure cases. No sleeps were added.

Removed 126 gross lines from daemon.go, including the offset parser/pagination,
old journal/read wrappers and unchecked JSON writer. Replaced 32 gross old
journal-test lines. No entire file was deleted. Added production files total
251 lines. T1's parked port inventory assigns no original test directly to W9.

## Evidence

Go used GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-astra-W9/cache and GOTMPDIR/TMPDIR=/tmp/sw-astra-W9/tmp.
Tests used -p 4 or less.

Passed on the final source:

- go build -p 4 ./... over the whole repository.
- go vet -p 4 on cmd/sparkwing, clisurface and tests/acceptance. The earlier
  broader selection also covered controllerwire and controller/client.
- CLI -short selections TestDaemon, TestExplain, TestDashboard, TestDoctor,
  TestCommandReferenceMatchesRegistry, TestHelp and FuzzDaemonEvent seeds.
- Full clisurface, controllerwire and doccheck package tests.
- Acceptance TestSurface and TestProtocolSchema selections. The expanded
  TestSurfaceGrammarContract accepts the final event forms and refuses offsets,
  zero limits, negative incarnations and explain/pagination combinations.
- Pkg/docs mirror, changelog, link, catalog, private-name and symbol selections.
- Source doccheck: 308 CLI invocations, 69 HTTP references and 397 links pass.
  Its 72 partial Go blocks remain partial, not compile evidence.
- commentcheck -base main, sleepcheck -base HEAD, sync-docs --check,
  check-changelog, gofmt and git diff --check.

Generated command pages through the existing test renderer with a temporary Go
overlay, because the brief forbids running Sparkwing executables. The ordinary
command-reference drift test passes. During porting, one rewritten fixture lacked SPARKWING_HOME. Restoring its
selection fixed the failure. Earlier red overlay/help checks are regression
evidence.

No native T1 cell was unskipped. Full CLI/client/native suites, platform services,
real TTY, runtime producer lifecycle, pipelines and launch QA remain unverified.
The lead owns independent review, commits and integration checks.

## Amendment and handoff audit

Rechecked every amendments-1 row naming W9 and every W9 entry in handoffs.md.
Previous completed implementations remain intact.

| Rows / handoff | Disposition and remaining integration |
| --- | --- |
| A1-03/08/09/13/14/15/16/17/18/20/22/38/55 | Retained final errors, claim/reconcile/expansion/cloud adapters and complete failure reports. Native producers/controller/store composition remains. |
| A1-10/11/19; execution identity and alias handoffs | Installer and alias inspection are retained. Real execution constructors still need exact engine/source and granted identity, including alias execution. |
| A1-29/30/34/37 | Signed-data transport is retained. Native opt-in remote-cache composition, local publication copying and remaining legacy storage consumers are open. |
| A1-39/40/41/49/50 | Retained local lifecycle/runner schema/here adapters. Event cursor cutover is complete. W8 construction, effective endpoints, native units/schedules/tick and runner startup remain. Initial sealed intake, local hosting and detached-consumer retirement remain W9 work. |
| A1-43/45/47 | Notices, selected-engine upgrade, settings and guard work are retained. Native engine/index and scheduled sealed-intake composition remain. |
| A1-51 | Current event help/references and source/embedded docs are synchronized. Remaining initial-trigger and local-runtime consumers still need the final contracts. |
| A1-52; Windows packaging | Prior workflow/package implementation is retained. Native Windows execution/publication evidence remains unverified. |
| A1-53 | Component journal/grammar evidence only. Native topology/backend/platform drivers and real-repository QA remain T1/lead work. |

Contract changes needed in other areas remain unchanged. W1/W3/W9 must finalize
source-attempt identity together across NodeTransition, JobTransitionRequest,
replay identity and the report producer. W9 must not infer it from mutable state
or salt a digest. W8 needs the scheduler-pass/managed-schedule and effective
endpoint seams already recorded below. W1/W2 sizing reset, member annotations
and runner pool registration remain open. No new human decision is requested.

The next W9 execution work is final initial-trigger intake and local/detached
hosting, source/pipeline-ref selection, publication and cache-from composition.
W3 RunHost has no native constructor here, W8 local lifecycle construction still
returns unavailable and the scheduler has no final controller-owned pass API.
Use final interfaces with fakes for each composition before owner integration.
Do not redo the merged machine reader, preparation masking, here resume,
Windows packaging or job failure transport.

## Shared files, mechanisms and friction

No production file outside W9 ownership changed. Shared files are
tests/acceptance/surface_contract_test.go, the W9-owned C4 contract prose,
this handoff, CHANGELOG.md and generated pkg/docs artifacts. No foreign API
caller migration was needed.

Mechanism added: an opaque continuation fingerprint over the retained unread
prefix, because offsets shift on appends. It reuses the journal reader and JSON
page renderer and adds no persisted state, flag, environment variable, timeout,
service or authority rule. Reverting this W9 event cutover removes it. The
positive-limit hard cut was already required by C4.

Friction: native owner integration remains incomplete. Overwing discovery/show
worked despite its stale-binary notice. Fuzz instrumentation rebuilt dependencies
before execution. Git identity remains Korey Gambill and core.worktree is unset.

---

# W9 failure reporting and daemon lifecycle continuation

Started clean at d294d02fa after the completed integration merge. W9 remains
incomplete. No Git write, Sparkwing binary, service, pipeline or release ran.
Writes stayed inside this worktree and /tmp/sw-astra-W9, removed at handoff.

## Implemented

- JobResult.Failure replaces JobResult.Error. It transports the existing
  nodeproto.JobFailure with job, stage and canonical error. The controller
  passes it unchanged to NodeResult.Failure and derives MaskedError from its
  inner message. C1 still owns validation, authority, persistence and replay.
  Unknown error codes, hints and details survive recovery reads. This is a
  local W9 C3 contract cut. No foreign contract shape changed.
- Daemon status, stop and restart parse clisurface and use LocalLifecycle via
  resolveLocalServices. The native constructor remains W8 work. Status uses
  LocalStatus without opening state.db or probing the legacy daemon transport.
  JSON reports safe lifecycle fields and activity. Plain output is the state.
- Restart leaves stopped controllers stopped. It skips an exact-version match
  unless forced, joins Stop, confirms stopped status, then starts the captured
  executable/home/address with the preceding stay value. A still-answering
  controller or an unready/wrong-version replacement fails without a success
  receipt. The existing status/restart/stop budgets remain 2/20/30 seconds.
- Dashboard and daemon share one stop confirmation and one status projection.
  Dashboard stop previously returned success with state running if Stop did
  not finish. Its regression failed before this change and passes now.
- Updated the C3/C4 contracts, API/local-execution guides, migration notes,
  changelog, generated OpenAPI and embedded mirrors.

## Tests, ports and deletions

Invariant: a reported failure retains its complete job, stage and canonical
error through the wire and persisted recovery boundary. The new acceptance
TestSurfaceJobFailureReportTransport passes on fake HTTP and api.sock transports.
It failed on the preceding source because the failure disappeared. The SQLite
TestEngineReportedFailureReachesRecovery also failed before the cut with a nil
failure. It now verifies persisted recovery, exact replay and refusal of a
foreign job, missing stage, null inner error and empty error code without
advancing the state. FuzzEngineReportedFailureProjection passed its seeds and
694 executions over three seconds with four workers.

Ported the old daemon exact-version, forced-restart, absent-controller, stop
confirmation, output/preflight and nested-shutdown-budget regressions through
LocalLifecycle. Added cancellation, error propagation, captured home/address/
stay preservation, unready replacement and dashboard stop cases. The recovery
and journal tests remain active and pass. No test sleeps were added.

Removed 369 gross lines from daemon.go's legacy status/control implementation,
including daemonReport, schema/store readers, probes, restart/stop dependency
wrappers and obsolete remedies. Removed 568 gross old daemon-test lines,
including all 149 lines of daemon_api_report_test.go and 99 lines of
daemon_stop_test.go. Retained the recovery test in daemon_test.go. The removed
native schema/API-ready tests are not claimed as final runtime ports: W8 must
provide those diagnostics through LocalLifecycle.Doctor and readiness through
its native runtime. No old health projection or cache-artifact fallback remains
in these CLI commands. T1's parked inventory assigns no source function to W9.

## Verification

Go used GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4 and GOCACHE,
GOTMPDIR/TMPDIR under /tmp/sw-astra-W9. Tests used -p 4 or less.

- Whole-repository go build -p 4 ./... passes on the final source.
- Vet passes for cmd/sparkwing, clisurface, controllerwire, apispec, doccheck,
  pkg/controller, controller/client and tests/acceptance. CLI vet was repeated
  after the shared stop change.
- Full clisurface, controllerwire, apispec and doccheck tests pass.
- CLI TestDaemon, TestDashboard, TestShutdownWindowsNest, TestDoctor,
  TestCommandReferenceMatchesRegistry and TestHelp selections pass.
- All controller TestEngine tests and the new failure fuzz seeds pass.
- Client TestEngineClient, TestQueue and TestDataClient selections pass.
- Acceptance TestSurface and TestProtocolSchema selections pass.
  internal/acceptance compiles with no test files. No native T1 cell was
  unskipped, and no full native acceptance pass is claimed.
- Focused pkg/docs mirror/changelog/link/catalog/privacy/symbol tests pass.
  Source doccheck passes 308 CLI invocations, 69 HTTP references and 397 links.
  Its 72 Go blocks remain partial/non-self-contained, not compile evidence.
- commentcheck -base main is clean across 1,295 Go files. sleepcheck -base HEAD,
  sync-docs --check, gofmt and git diff --check pass.

The initial doccheck invocation lacked its required path arguments. The corrected
source-doc check passed. No full CLI/client/native suite or pipeline ran. The
lead owns independent review, commits, integration checks and landing.

## Amendment and handoff audit

Every amendments-1.md row naming W9 and every W9 handoff was rechecked. The
previous ledgers below retain completed work and remain applicable except where
this continuation advances them.

| Rows / handoff | Action and remaining integration |
| --- | --- |
| A1-03/08/09/13/15/16/17/18/20/22/38/55 | Retained strict error, claims, reconciliation, expansion, cloud and authority adapters. Native owner/controller/store composition remains. |
| A1-14 | Complete report failure and persisted recovery now work through the wire/server adapter. W3 native report producers must consume JobResult.Failure. |
| A1-10/11/19; execution identity and alias handoffs | Existing installer and alias-inspection work retained. Native engine/source/host constructors, granted identity and alias execution remain open. |
| A1-29/30/34/37 | Existing signed-data transport retained. Native remote cache, publication and legacy storage consumer retirement remain. |
| A1-39/40/41/49/50 | Daemon control now uses the final lifecycle with fakes. W8 native local services, effective endpoints, timers/units and runner startup remain open. Initial/local/detached hosting, publication and cache-from composition remain W9 work. |
| A1-43/45/47 | Retained notice/upgrade/settings/guard adapters. Native engine/index and scheduled sealed-intake composition remain. |
| A1-51 | Regenerated the changed JobResult schema and source/embedded docs. Remaining unmapped API bodies and old runtime consumers still need retirement. |
| A1-52; Windows packaging | Prior implementation retained. Native Windows execution and publication credentials remain unverified. |
| A1-53 | New fake transport and SQLite component evidence only. Native platform/placement/backend drivers and real-repository QA remain T1/lead work. |

Contract changes needed in other areas: the existing W1/W9/W3 source-attempt
handoff remains open. NodeTransition still lacks its source ordinal. Do not
invent a digest salt or infer the source attempt from mutable state. W9's wire
field must be finalized together with that C1 contract and W3's report producer.
No new human decision is requested.

Do not redo the merged machine profile reader or cache-warm masking. Source
inspection confirmed loadProfiles uses userconfig.ReadConfig, userconfig.Read
has no legacy migration, and cache warm composes procenv.Build with the shared
masked install writer. Exact engine selection and executable-lease lifetime
still require their native owners.

The old remaining-work list also includes final initial-trigger intake, detached
consumer deletion, source/repository identity, publication copying, local schedule
installation/tick, sizing reset, member annotations, runner pool registration,
daemon event cursor cutover, unfinished guides/examples and native acceptance.

## Shared files and friction

Minimal foreign caller migrations: pkg/controller/engine_contract.go and
engine_store.go. Ported engine_store_test.go and added engine_failure_report_test.go.
Other shared files: tests/acceptance/job_failure_contract_test.go, api/openapi.yaml,
the C3/C4 contracts, this handoff, CHANGELOG.md and generated pkg/docs artifacts.
All remaining edits are W9 CLI source/tests and source guides.

No new service, environment variable, flag, timeout, credential class or authority
mechanism was added. LocalLifecycle and JobFailure are existing contracts.
Friction remains the unimplemented native W8 constructor, C1 attempt-fence seam
and native acceptance environment. Overwing discovery worked despite its stale
binary notice. Git identity remains Korey Gambill and core.worktree is unset.

---

# W9 integration-merge, result identity and guide continuation

Started at 3718486f4 with an integration merge in progress. W9 remains incomplete.
No Git write, Sparkwing executable, service, pipeline or release ran. Writes stayed
inside this worktree and /tmp/sw-astra-W9, which is deleted before reporting.

## Merge resolutions

All six conflict contents are resolved. Retained NewLogReader in the client API
snapshot and combined both migration-note additions. Kept the already-deleted
legacy doctor and capacity files deleted. Their incoming copies contained 928
lines (320 doctor test, 304 capacity implementation, 304 capacity tests). Those
are rejected merge-side lines, not new deletions against this session's HEAD.
The index still reports those six unmerged paths because the brief forbids Git
writes. The lead must stage the resolutions and complete the merge.

## Implemented

- ReuseSource.Result transports the existing C1 MaterializedResultSource through
  run_id, job_id and state_seq. It preserves a terminal result with no execution
  attempt, including state sequences above JavaScript's exact-number range.
  The controller projects both directions and leaves lineage, read-edge,
  exclusivity and state-fence validation to C1. Ordinary attempt sources remain
  unchanged. Transactional record copying still needs the W1/W7 binding.
- ScheduleSnapshot.GuardRefusal carries the existing C1 sealed guard refusal
  alongside cancellation. The controller returns the consistent projection
  instead of an unavailable error. Cancellation and guard evidence both survive.
  W3 must consume both before dispatch in its native remote owner.
- These are local W9 C3 contract additions, documented in c3-controller-wire.md.
  No foreign contract shape, authorization rule, retry policy or route changed.
  OpenAPI and embedded documentation were regenerated.
- Added an HTTP guide check against capability.Routes. It checks explicit
  method/path examples, normalizes parameter names, retains methods and reports
  file/line failures. Migration history remains excluded. It caught 22 stale
  examples before the guide changes and passes all 69 retained references.
- Removed cache-service deployment recipes, split claim routes, cache grants,
  offer rounds, RetryAuto guidance, old node routes and conflicting output,
  annotation, retention and billing limits. Source/Actions guides describe live
  claim authority, consent and direct acquisition. Native boundaries stay stated.
  Current docs/ guides contain no sparkwing-cache, gitcache, cache-grant-key or
  /nodes/ path. Historical migration pages retain their original context.
- The existing dead-token check now refuses removed cache recipes in published
  guides/help and RetryAuto on its shared surfaces. Cache recipe rules stay in
  the guide check because auxiliary scans also read still-live foreign runtime
  source and chart documentation. Their retirement remains W7/W8 work.

## Tests and deletions

New acceptance checks pass on fake HTTP and api.sock transports:
TestSurfaceMaterializedResultTransport and TestSurfaceSnapshotPreservesGuardRefusal.
They preserve a memoized job with attempt zero, exact source state sequence and
both cancellation states. No native T1 cell was unskipped.

Ported the old unrepresentable-guard snapshot regression to the final field.
The SQLite controller test preserves refusal and cancellation. Corruption,
foreign-team, revoked-owner and stale-lease tests remain active.

The invariants are: a reused result retains its exact persisted job/state identity
without inventing an execution attempt, and a sealed guard refusal cannot vanish
from the private scheduling projection. The properties vary source names and
state sequences, and guard reasons/pass/cancellation respectively. Three-second
fuzz runs with two workers completed 4,317 and 6,223 executions. Overlays dropping
either projection make its seeds fail. Production source passes.

Deleted pkg/controller/triggers_list_test.go, 134 lines calling the previously
removed Client.ListTriggers. That file prevented the controller test package
from compiling. Final run filtering remains covered through RunReader and the
surface run-query acceptance. No removed trigger-list API was restored.

Gross source-guide deletions are 1,115 lines against the starting HEAD, excluding
mirrors. No additional whole production file was deleted. The two conversion
helpers replace inline projections in the existing controller adapter.

## Verification

All Go commands used GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4 and
GOCACHE/GOTMPDIR/TMPDIR under /tmp/sw-astra-W9. Tests used -p 4 at most.
Recreate cache and tmp children there before reproducing checks.

- Whole-repository go build -p 4 ./... passes.
- Vet passes for cmd/sparkwing, internal/doccheck, controllerwire, apispec,
  pkg/controller, controller/client and tests/acceptance.
- Full clisurface, controllerwire, doccheck, apispec and apiref tests pass.
- All controller ^TestEngine tests and both new fuzz properties pass.
- Client ^TestEngineClient and ^TestQueue pass. CLI ^TestDoctor, ^TestSizing,
  command-reference and help selections pass.
- Acceptance ^TestSurface and ^TestProtocolSchema pass, including both new cells.
- Focused embedded-doc mirror, changelog, privacy, symbol, link and guide tests pass.
- Full source doccheck passes: 308 CLI invocations, 69 explicit HTTP route
  references, valid YAML, 397 resolved links and no schema/sidebar/auxiliary drift.
  Its 72 Go blocks remain partial/non-self-contained, not full-program evidence.
- commentcheck -base main is clean across 1,289 Go files. sleepcheck, mirror,
  regenerated client snapshot comparison and diff --check pass.

The broader tests/acceptance run failed in W5's native TypeScript cell because
sdk/typescript/node_modules/typescript/bin/tsc is absent. The failure was preserved;
no native pass or full acceptance pass is claimed. The first new HTTP fixture
also omitted required execution fields and was corrected. An initial globally
shared cache-token rule reached foreign build code; its final scope is the
published guide/help check. Full doccheck then passed. These are separate from
product behavior and do not change foreign runtime code.

## Amendment and handoff audit

Every amendments-1.md row naming W9 and every W9 handoff was checked against the
retained implementation and prior ledger. Completed adapters were not rebuilt.

| Rows / handoff | W9 state and remaining integration |
| --- | --- |
| A1-03/08/09/15/18/20/22/55 | Existing strict errors, claims, cancellation, ordinal delivery and authority boundaries retained. Native C1/C2 authorization and claims still need composition. |
| A1-13/16/17/50 | Added result-state identity and sealed guard transport to existing private reconciliation/expansion/snapshot clients. C1/W7 result-copy binding and W3 native consumption remain. |
| A1-14 | ReadFailure retains the persisted JobFailure. JobResult still lacks the complete failure/stage payload for reports; its existing Error field cannot reconstruct that stage. This needs the remaining W9 C3 report cut and W2/W3 consumption. |
| A1-10/11/19; execution-identity and alias handoffs | Existing installer/inspection adapters retained. W3 source/host and W8 engine resolution remain unbound. Initial/local/detached execution, exact-engine/granted-identity construction and native alias execution remain unfinished. |
| A1-29/30/34/37 | Existing signed-data/cache interfaces retained. Native remote-cache/publication composition, logical record copying and legacy service/build/chart retirement remain with W1/W3/W7/W8 and W9 callers. |
| A1-38/39/40/41/49 | Existing cloud payload, enrollment, foreground and lifecycle adapters retained. Unified runner startup, native local services/endpoints, service refresh and schedule tick composition remain. |
| A1-43/45/47 | Existing exact-upgrade, settings and guard behavior retained. Native engine/index, captured schedule intake and timer ownership remain. |
| A1-51 | Guide/API-schema regeneration and final route/removed-recipe checks advanced. Remaining API body mappings and native consumer retirement are open. |
| A1-52; Windows packaging handoff | W9 packaging/publication implementation remains delivered from the prior session. Native Windows executions and publication credentials remain unverified. |
| A1-53 | New fake-transport and SQLite component evidence only. Native platform/placement/backend drivers and real-repository QA remain T1/lead work. |

The new W1/W9/W3 retry-attempt handoff is still blocked on its C1/C3 amendment.
Invariant: a report affects only its named attempt, and replay cannot consume a
later attempt under the same claim. C1 NodeTransition still has no source attempt
field. W9 did not invent a digest salt or redefine retry timing to conceal it.

Other retained contract gaps are audited here reason, GitHub URL-to-numeric-ID
resolution, source-bundle ownership, publication result copying, sizing reset,
member annotation writes, local API endpoint projection, schedule tick/install
and runner pool registration. No new human decision is requested.

## Shared files and handoff

Minimal W2 production projections: pkg/controller/engine_contract.go,
engine_store.go and engine_snapshot.go. Shared tests: engine_snapshot_test.go,
new engine_materialized_source_test.go and deletion of triggers_list_test.go.
Other shared changes: internal/doccheck/{main,apiroutes,bannedtokens} and their
tests, tests/acceptance/materialized_result_contract_test.go, CHANGELOG.md,
api/openapi.yaml, generated pkg/docs artifacts, the W9 contract and this handoff.
W9's remaining files are source docs and controllerwire/contracts.go.

The only new mechanisms are the requested C3 transport fields and read-only
source-guide checks. No runtime service, environment variable, flag, deadline,
capability restriction, compatibility alias or replacement authority was added.
Git identity/config remains unchanged. The lead owns independent review, staging,
merge completion, native gates and landing. Scratch is removed at handoff.

---

# W9 OpenAPI and Windows release packaging continuation

Started clean at c7e91e446. W9 remains incomplete. Changes stay in this worktree
and /tmp/sw-astra-W9, which is removed at handoff. The lead owns commits, native
acceptance, pipelines and landing. No release or live service was changed.

## Delivered

- OpenAPI now generates from capability.Routes and compiled shared wire types.
  It includes all 239 policy operations, 75 mapped success-body contracts and
  235 nested component schemas. Complete credential/grant/binding/liveness
  alternatives remain together with audience, owner, authentication and retry.
  The canonical error envelope and additive code registry have one source.
- Replaced the legacy route-registration rewrite and AST handler/store schema
  scanner. Serialization schemas retain embedded fields, omission, explicit zero,
  pointer/collection nulls, byte encoding, timestamps and opaque JSON. Duplicate
  policies, normalized-path collisions, orphan body bindings and unsupported
  serialization fail before output. Short writes and output errors fail.
- The remaining 164 operations have no body mapping in this generator and carry
  x-sparkwing-wire: undocumented. This is remaining documentation work, not proof
  that every owner lacks a body declaration. No legacy body was carried forward.
- Required Windows x64 and arm64 release jobs install matching Node/Go/MSVC,
  verify downloaded Node headers/import libraries against their checksums, and
  invoke the existing SDK release:native command. That command stamps the engine
  version and requires TestNativeProtocol and TestNativeConformance without skips.
  Each job uploads its bridge and verification receipt. Assembly consumes both
  from the same resolved source and invokes release:pack before publication.
- SDK text has LF checkout endings so native and assembly source digests agree.
  Native addons remain binary. Publication uses the checked tarball and disables
  lifecycle scripts. NPM_TOKEN is required before signing; its existence and npm
  authority were not inspected. Latest/next/version-specific tags follow release
  eligibility. npm publication precedes the immutable GitHub release. Retrying
  accepts an existing version only when registry integrity equals the tarball's
  SHA-512; lookup, integrity and publish failures stop the job.
- Updated API prose, delivery guidance, changelog and embedded mirrors.

The authorization-document invariant is: generation preserves every complete
access alternative without lending its grants, binding or liveness to another.
The property varies adversarial grant/binding strings. An overlay discarding
Access fails both seeds. The production source passes its seeds and 2,949 fuzz
executions with two workers over three seconds.

The publication invariant is: an existing version is accepted only for the exact
assembled bytes, and failed metadata never authorizes publication. The property
varies archive bytes and matching/mismatched registry digests. Its final run
completed 83 executions with two workers over three seconds. Removing the digest check through a test-only overlay
makes the different-bytes regression fail. Production tests pass.

## Verification

Go used GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4 and
GOCACHE/GOTMPDIR/TMPDIR under /tmp/sw-astra-W9. Tests used -p 4 or less.

- Whole-repository go build -p 4 ./... passes on the final source.
- Vet passes for internal/apispec, internal/apiref and bin.
- Full apispec, apiref, doccheck, clisurface and controllerwire tests pass.
- Exact bin selection passes TestTypeScriptReleaseRequiresBothNativeBridges,
  TestTypeScriptPublicationRequiresAnExactImmutablePackage and publication fuzz
  seeds. Eight publication cases include missing packages, identical/different
  bytes, missing integrity, authentication/network failures and failed upload.
- Existing acceptance TestSurfaceGrammarContract, removed-recovery-API,
  run-query/prune, management-namespace, wire-detail and observation transports
  pass, as does TestProtocolSchemaContract. TestSurface's native driver cells
  remain pending. No new native acceptance cell was unskipped.
- Four existing SDK native-package/native-result preflight tests pass. They cover
  malformed/mislabeled/missing bridges, native receipts and version stamps.
  These Linux tests are not Windows execution evidence.
- actionlint v1.7.12 passes with the repository's existing queue exception.
  No action pin, concurrency exception or hook behavior changed.
- Independent Python Draft 2020-12 validation accepts all 235 components and
  resolves every local reference. JSON examples accept embedded fields and
  explicit zero and reject wrong field types.
- Full source doccheck passes: 313 resolved CLI invocations, valid configuration
  examples, 394 resolved links and no auxiliary/sidebar/schema drift. Its 72 Go
  blocks are partial/non-self-contained, not full-program compile evidence.
- API drift, mirror, gofmt, node syntax and diff checks pass. commentcheck with
  -base main is clean across 1,159 Go files. No test sleeps were added.

Ported old generator fixed-point/inventory/schema/error-path coverage to the new
source-driven generator. Removed the obsolete handwritten-scope/prose and AST
fixture tests. No parked T1 port-inventory row names W9-owned tests.

Gross deletions against this session's HEAD: 756 production Go lines, including
all 419 lines of internal/apispec/rewrite.go, and 627 old test lines. Regenerated
OpenAPI and mirrors are excluded. These are gross deletions, not net reduction.

## Amendment and handoff ledger

All amendments-1.md rows naming W9 and the three W9 handoffs were rechecked.
Previously completed work remains in place.

| Rows | Remaining W9/integration state |
| --- | --- |
| A1-03/08/09/13-18/22/29/30/34/55 | Existing error, claim/reconcile, execution-state and signed-data clients remain implemented with fakes. Native authorization, claims, read edges, store and backend bindings remain owner integrations. |
| A1-10/11/19; handoff finding 3 | Exact-install upgrade adapter remains. Initial/local/detached source and engine intake, and one execution constructor carrying granted identity through hello, still need W3/W8/W9 composition. |
| A1-20/38/39/40/41/49 | Existing enrollment/schema/private-file, cloud award, foreground and managed-unit adapters remain. Native issuance, unified runner, local lifecycle, service refresh, schedule tick and pool registration remain open. |
| A1-37/45/47/50 | Native cache/publication composition, captured schedule intake, timer ownership, target/source dispatch and legacy host/client/consumer retirement remain open. Prior guard/settings work is preserved. |
| A1-43/50 | Exact selected-engine upgrade verification remains tested with contract fakes. Resolver/staging and native SDK installation are not established here. |
| A1-51; handoff finding 7 | OpenAPI inventory and mapped schemas now use final contracts. Unmapped bodies and remaining runtime-guide audit remain. Protocol schemas stay current. Native alias execution still needs the shared host binding. |
| A1-52; Windows packaging handoff | W9 workflow/assembly/publication implementation delivered. Both native Windows CI executions, usable NPM_TOKEN and real publication remain unverified. W5 finding 6 is not closed by Linux evidence. |
| A1-53 | Native placement/platform/backend drivers and real-repository QA remain T1/lead integration work. |

## Contracts and mechanisms

No foreign contract changed. Existing unresolved seams still include persisted
here reason, GitHub URL-to-numeric-ID resolution, source-bundle upload ownership,
publication result-copy operations, sizing reset, member/run annotation writes,
schedule/timer intake and runner pool registration. Do not synthesize authority
from display repository names or smuggle a reason into actor/source/env fields.

The build-time body-binding table and JSON shape renderer replace the old route,
handwritten-body and AST scanner mechanism. Policy-only generation would leave
obsolete schemas behind. These functions add no runtime route or authority.
The publication helper compares immutable tarball integrity because blind
republishing cannot recover after npm succeeds and GitHub publication fails.
It adds no retry loop, runtime setting or service. Undo is a direct npm publish
call, losing that recovery check. NPM_TOKEN/NODE_AUTH_TOKEN are CI vendor
credentials, not product process variables or a new runtime trust root.

Shared files changed: .gitattributes, .github/workflows/release.yaml,
internal/apispec/*, bin/check-api-spec.sh, bin/gen-api-docs.sh,
bin/publish-typescript.mjs and its two Go test files, DELIVERY.md, CHANGELOG.md,
this handoff and generated api/pkg/docs artifacts. The two shell callers migrated
for the removed apispec repository/spec arguments. No foreign production caller
needed migration. internal/apiroutes still serves three W2/W3 legacy registration
test files and was retained for their owning areas' runtime retirement.

## Friction and scope error

The first npm subprocess fake hit sandbox EPERM. Tests now execute the real
publisher with a Node builtin process-boundary fake, without adding a production
callback. Real registry behavior and credentials remain untested. A fuzz fixture
also exposed # in Go seed temporary paths; its preload now uses a data URL.

I accidentally ran a broad bin test filter while trying to combine focused
checks. It selected installer fixtures that write temporary Git repositories,
plus an installer candidate-build test. I stopped that run with SIGINT and reran
only the exact W9 tests. This exceeded the worker's no-Git-writes/test boundary.
No native/runtime pass is claimed from it. Checkout HEAD and Git identity/config
were unchanged. All identified task scratch, including installer remnants, is
removed at handoff. Do not repeat that broad bin selection.

Overwing discovery works and reports a stale installed binary. Native Windows,
real controller transports and product pipelines remain outside worker evidence.
The lead retains independent review, commits, native gates and landing.

---

# W9 selected-engine upgrade and HTTP reference continuation

This continuation started clean at 7f556bd82. W9 remains incomplete. No Git
writes, Sparkwing executable, candidate, service or pipeline ran. Writes stayed
inside this worktree and /tmp/sw-astra-W9. Scratch is removed before reporting.

## Delivered

- Upgrade verification calls the supplied verified engine's InstallSDK with the
  staged checkout, SDK kind and filtered install environment. It no longer
  performs a second Go dependency download or npm install. Go package compilation
  and TypeScript type checking remain, followed by list, describe and plan through
  that same engine, including hidden pipelines. Invalid SDKs/executables, cancelled
  input and invalid installer results fail before later verification stages.
- The install and host-child environment builders share a masker. Every returned
  verification failure passes through maskedCLIError, preserving its cause and
  canonical code while masking message, hint, details and detail keys.
- The HTTP reference now derives directly from capability.Routes. Each row retains
  audience, ownership lookup, authentication, retry policy and complete access
  alternatives. Conjoined grants stay within their alternative. Removed the AST
  route/scope renderer, its admin-bypass prose and the unused repo-root argument.
  Its only script caller now uses the new invocation. A drift test compares the
  rendered reference with the published artifact. Output errors remain failures.
- Replaced the API, signed-data and versioning guides with their frozen contracts.
  Removed old trigger/node paths, cache grants, legacy cache fallback, historical
  toolchain substitution, automatic second-store fallback and age/floor behavior
  that conflicts with signed engine policy. The target sections use design markers
  and explicitly name native integration boundaries. Updated changelog and mirrors.

The masking invariant is: no registered proxy credential appears in upgrade
failure output, whether the failure comes from installation, type checking,
list, describe or plan. The fuzzer varies credential content and URL userinfo
escaping, checks raw/base64 forms, canonical hints/details/detail keys and every
CLI output mode, and verifies that excluded bearer env never reaches the tools.
An overlay removing only error masking makes the seed property fail. The checked-in
source passes. This introduces no new credential source or masking algorithm.

## Verification

All Go commands used GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4 and
GOCACHE/GOTMPDIR/TMPDIR under /tmp/sw-astra-W9. Tests used -p 4.

- Whole-repository go build -p 4 ./... passes on the final production source.
- Vet passes for cmd/sparkwing, internal/apiref, internal/clisurface,
  internal/controllerwire and tests/acceptance. CLI/apiref vet was repeated after
  their final source/test edits.
- All upgrade tests and the masking property's seeds pass, as do all apiref tests.
  The final adversarial fuzzer completed 18 executions with two workers over its
  three-second fuzz budget, after loading twelve cached/seed corpus entries.
- Full clisurface, controllerwire and tests/acceptance suites pass.
  internal/acceptance has no test files. No native placement cell was unskipped.
- Full doccheck/apiref package tests and focused pkg/docs mirror, changelog,
  section, search, read, guide, symbol and privacy tests pass.
- Full source doccheck passes: 313 CLI invocations, no unresolved grammar,
  valid YAML, no broken links or schema/auxiliary/sidebar drift. Its 72 Go blocks
  are partial/non-self-contained with no SDK drift, not complete-program compile
  evidence. Protocol schemas were already current and were not rewritten.
- commentcheck -base main is clean across 1,143 Go files. Mirror check,
  gofmt and diff --check pass. No test sleeps were added.

Ported the existing exact-engine and stage-failure upgrade verifier tests to the
InstallSDK boundary. Added two upgrade validation tests and one fuzz property.
Replaced the old apiref scope fixture with four tests covering complete policy
alternatives, every final route once, artifact drift and output failure. No parked
T1 port-inventory row names a W9-owned source test.

Gross deletion counts against this continuation's starting HEAD are 54 production
Go lines, 23 old test lines and 501 source-guide lines. The regenerated HTTP
reference replaces another 243 generated lines. Mirrors are excluded. No entire
production file was deleted, and these are not net-reduction counts.

## Remaining amendments and handoffs

Every amendments-1.md row naming W9 and both W9 design handoffs were rechecked
against the previous ledger. Previously completed work was retained.

| Rows | Status and remaining integration |
| --- | --- |
| A1-03/08/09/13-18/22/29/30/34/55 | Existing strict errors, claim/reconcile, execution-state and signed-data clients remain. Native authorization, claims, read edges, log completeness and store/backend composition remain with their owners. The generated reference now preserves their complete policy metadata. |
| A1-10/11/19; handoff finding 3 | Upgrade now consumes selected-engine SDK installation. Source.Acquire/ForRequest/ForExecution and engine resolution/InstallSDK remain declared stubs. Initial/local/detached execution and one granted-identity constructor through hello still need the W3/W8 host binding. |
| A1-20/38/39/40/41/49 | Prior enrollment/schema/private-file, cloud award, foreground and managed-unit adapters remain. Native issuance, unified runner, local lifecycle, service refresh, schedule tick and pool registration remain open. |
| A1-43/50 | This continuation completes the upgrade verifier's selected-install and diagnostic boundary with contract fakes. Exact-engine resolution/staging, local host retirement, cache/publication composition, initial here intake and target/source dispatch remain unfinished. |
| A1-37/45/47 | Existing lint/guard/settings work remains. Native cache binding, schedule intake/captured env, timer ownership and old client/consumer retirement remain unfinished. |
| A1-51; handoff finding 7 | Protocol schemas remain current. API/signed-data/versioning guides and generated policy reference are corrected. OpenAPI still derives from legacy controller registrations, and other runtime guides need the final audit. Alias inspection evidence does not prove native execution. |
| A1-52/53 | Windows bridge packaging/stamping, native platform/placement drivers and real-repository QA remain unclaimed. |

## Contract changes needed and next integration

No foreign contract was changed and no new contract question was introduced.
Existing unresolved contracts still apply:

- W6/W1/W2 must agree a typed persisted/audited here reason. Request and
  CreateRunRequest still have no such field. W9 does not encode it as actor,
  source or environment data.
- W2/W6/source intake must agree URL-to-numeric-ID resolution for GitHub.
  CanonicalRepository requires that numeric identity. W9 does not synthesize
  a git:github.com key or grant authority from a typed display name.
- Source-bundle upload ownership, publication result-copy operations, sizing reset,
  schedule install/tick ownership and runner pool registration still need their
  existing owner resolution.

Next W9 work includes migrating OpenAPI from legacy registration/schema inputs,
then replacing the initial/local/detached intake and old host/consumer once their
contract projections can be composed. Fakes should continue to establish those
adapters while the owner implementations remain unavailable. Native evidence
requires the source/engine/shared-host, owner actor, claim-scoped controller/storage,
captured environment, plan cache and lease keeper bindings. resolveHereRuntime
still returns an explicit unavailable error. No legacy runtime was claimed retired.

## Shared files, mechanisms and friction

Shared files changed are internal/apiref/main.go and main_test.go,
bin/gen-api-docs.sh, CHANGELOG.md, this handoff, and generated pkg/docs artifacts.
No foreign production caller was migrated. The generator script is the only caller
migrated for the removed apiref repository argument. No API/schema contract changed.

The only new callback is the private upgrade install function, defaulting directly
to Resolved.InstallSDK. It lets tests use the existing engine contract while that
implementation is a stub. The smaller direct call cannot be exercised with a fake
Resolved receiver. Undo is to remove the callback and invoke the method directly.
The renderer reads the existing policy table without another registry or service.
No public flag, setting, environment variable, retry loop or customer limit was added.

Friction: native Sparkwing executables/pipelines remain forbidden for this worker,
so actual engine resolution/installation and placement QA are unverified. OpenAPI's
legacy generator is still separate work and was not claimed synchronized by the
HTTP reference change. Doccheck caught one banned word, which was removed.
The property fixture was corrected to use URL userinfo encoding and an actual
pipeline entry before its final run. Overwing discovery/show work but report a stale
installed binary. Git still resolves Korey Gambill's default identity and no local
core.worktree. The lead retains independent review, commits, native gates and landing.

---

# W9 documentation grammar and library inflation continuation

This continuation started clean at fe2071f89. W9 remains incomplete. No Git
writes, Sparkwing executable, candidate, service or pipeline ran. Writes stayed
inside this worktree and /tmp/sw-astra-W9, which is removed before reporting.

## Delivered

- Fenced shell examples, including unlabeled command fences, now pass their
  complete invocation to clisurface.Parse. The existing checker still handles
  inline fragments by command name. Quotes, continued lines, prompts, comments,
  pipes and ordinary redirections are tokenized without executing a shell.
  Directory/read errors fail the check instead of producing a clean empty scan.
- The initial negative controls reproduced five kinds of invalid grammar passing
  the checker. Its first corpus run exposed 25 stale examples. Fixed removed
  selectors, output flags, fleet flags and pipeline argument separators across
  the authoring, CLI, hooks, cache, admission, sparks and getting-started guides.
- Restored an invocable library inflater through the shared command grammar.
  Required --library matches the catalog selector and accepts a sparks-core
  module name or full Go module path. Removed the old --module parser and its
  unused help binding. The command validates before project lookup or vendoring,
  honors cancellation, returns output errors, emits one JSON receipt and emits
  only the destination in plain mode. Catalog, info, generated help/reference,
  migration guide and tests use the same selector. No compatibility alias remains.
- Corrected info's suggestions and authoring/discovery snippets: --pipeline,
  --search, explicit -o json and dashboard start. A new test parses every
  structured next-step and agent suggestion with the real command registry.
  Its negative control failed on seven stale suggestions before the correction.
- Replaced old local execution, scheduling, security and workstation threat prose
  with the frozen contracts. Removed descriptions of environment credentials,
  typed legacy tokens/argon2 capability lookup, open bootstrap, standalone
  second-store fallback, RetryAuto/replacement runs, five-second warm overflow,
  same-team Job node sharing, per-request protocol negotiation and cache mirrors.
  Corrected the CLI map, mixed claim-route spelling and related guide examples.
  These guides explicitly identify their native integration boundary.
- Regenerated the changed CLI reference through its unit-test renderer and
  synchronized embedded mirrors. The temporary renderer test was removed.

The sole local contract correction is the missing inflater selector and output
rule, recorded in c4-surface-config.md. No foreign contract was changed.

## Verification

All Go commands used GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4 and
GOCACHE/GOTMPDIR/TMPDIR under /tmp/sw-astra-W9. Tests used -p 4.

- Whole-repository go build -p 4 ./... passes.
- Focused CLI tests pass for sparks inflation/catalog, command references,
  command discovery, info, authoring quickstart and durable discovery output.
- Full internal/clisurface, internal/controllerwire, internal/doccheck and
  tests/acceptance suites pass. internal/acceptance has no test files.
- TestSurfaceGrammarContract now covers inflater output formats and refuses the
  removed selector/missing selector. No native placement cell was unskipped.
- Focused pkg/docs section/search/read/guide/catalog/mirror/symbol tests pass.
  WebClient socket tests and the broad legacy CLI runtime suite were not run.
- Vet passes for cmd/sparkwing, clisurface, controllerwire, doccheck,
  tests/acceptance and pkg/docs. Commentcheck -base main and diff --check pass.
- Full doccheck passes: 322 CLI invocations, no unresolved commands, valid YAML,
  no broken file links, no stale banned tokens or auxiliary/sidebar drift.
  Its 72 Go blocks are partial/non-self-contained with no SDK API drift. This
  is not a claim that those examples compile as complete programs.
- Embedded mirror check passes. No new test sleeps or runtime limits were added.

Added five CLI test functions and four doccheck test functions. Ported catalog
selector tests and the info configuration diagnostic to the strict W6 reader.
The latter now asserts file/line context instead of the retired parser's wording.
No parked T1 port-inventory row names a W9-owned test source.

Against this continuation's starting HEAD, tracked edits remove 53 production
Go lines, 8 old test lines and 2,924 source-document lines gross. Mirrors are
excluded from the documentation count. No entire production Go file was deleted.
The new inflater test file has 99 lines. These are gross deletion counts, not a
net code-reduction claim.

## Remaining amendments and handoffs

Every amendments-1.md row naming W9 and both W9 handoffs were rechecked against
the prior ledger. Completed transport/enrollment/here work was retained.

| Rows | Remaining integration |
| --- | --- |
| A1-03/08/09/13-18/22/29/30/34/38/55 | Existing adapters remain. Native controller/store authorization, claim/reconcile, data and persisted-log completeness remain with their owners. The auth guide now names the mixed /runners/claim route. |
| A1-10/11/19; handoff findings 3/7 | Final initial/local/detached source and selected-engine composition, one granted-identity execution constructor and old-host retirement remain. W9 inspection alias evidence does not establish native execution. |
| A1-20/39/40/41/43/49 | Issuance, unified runner, native local lifecycle/services/timers, exact-engine upgrade and final startup/service consumption remain. Enrollment/removal's final schema and private files were already completed. |
| A1-37/45/47/50 | Final host cache/publication binding, schedule intake/captured settings, source intake and legacy consumer/client retirement remain unfinished. This continuation removes obsolete guide claims, not these runtime consumers. |
| A1-51 | Command/example drift and the four legacy runtime/security guides advanced. Other guides, schema parity and native consumer retirement still need the final composition audit. |
| A1-52/53 | Windows packaging/native bridge proof and real platform/placement/repository QA remain unclaimed. |

## Contract changes needed / owner guidance

- W6/W1/W2: initial here submission requires --reason, but runspec.Request and
  CreateRunRequest have no agreed field that persists and audits that reason.
  Do not put it in actor, source or environment fields. Agree the typed intake
  projection and persistence, then W9 can replace the initial legacy path.
- W2/W6/source intake: CanonicalRepository requires a numeric ID for GitHub.
  The current wire has no declared URL-to-ID resolution call. Agree where that
  lookup occurs before final request construction; W9 must not synthesize a
  git:github.com key or treat a display name as authority.
- Existing open contracts remain: source-bundle upload ownership, publication
  result-copy operations, sizing reset, schedule installation/tick ownership and
  runner pool registration. No contract was invented around these gaps.

source.Acquire/ForRequest/ForExecution, blob.OpenRemoteCache and the local
lifecycle constructor remain declared stubs. resolveHereRuntime still lacks a
native shared-host binding. The old initial trigger, local host and detached
consumer remain and are not claimed retired. Fake evidence does not satisfy them.

## Shared files, mechanisms and friction

Shared edits are internal/doccheck/cliverbs.go and cliverbs_test.go,
tests/acceptance/surface_contract_test.go, CHANGELOG.md, the W9 C4 contract note,
this handoff and generated pkg/docs content. No production caller in another
area was migrated or removed. W9's catalog and info callers were migrated.

Mechanism added: the existing doc checker gains a bounded shell-example lexer
and calls the shared CLI parser. It adds no shell execution, runtime package,
service, environment variable, cache or retry. Private locator/vendor callbacks
make the existing inflater testable without network or project mutations. They
add no public interface. Undo is to revert the inflater/selector and checker
changes with their docs/tests together.

Friction: the standard documentation generator executes the Sparkwing CLI,
which this worker is forbidden to run, so its existing renderer was called from
an ephemeral unit test instead. The old info test expected a superseded parser
word. Overwing reports its installed binary is stale but discovery/show work.
Git identity remains Korey Gambill and local core.worktree is unset. The lead
retains review, commits, native gates and landing.

---

# W9 runner schema and here attachment continuation

This continuation started clean at 89f4556e0. W9 remains incomplete. No Git
writes, Sparkwing executable, service, candidate or pipeline ran. All writes
stayed in this worktree and /tmp/sw-astra-W9. The scratch directory is removed
before the final report.

## Implemented

- Enrollment writes userconfig.RunnerConfig directly. The duplicate
  agentFileConfig and its YAML validation round-trip are deleted. Bearers are
  separate function inputs and never a member of the configuration model.
  RunnerPreferences validates through the shared W6 resolver. Omitted
  max-concurrent remains absent and resolves to the contracted default of one.
- Explicit empty labels and repos survive serialization and failed-enrollment
  rollback. Other supported runner preferences retain their presence. Removal
  uses ReadConfig and fssecure.OpenPrivateConfig directly, checks the selected
  controller/name, and refuses a public credential before stopping or revoking.
  W9 production code no longer imports agentconfig. Existing tests that load
  the written file through that older runner loader remain startup-consumer
  regression evidence, not a claim that W8's final startup is bound.
- Sealed here resume and retry share an attached session. Public logs use the
  caller's display connection, claims use the retained private bearer, and the
  host receives its fresh award capability. Explicit output modes reach logs
  and receipts. Host success drains final persisted log segments. Log failure
  remains visible while hosting finishes. Incomplete observation exits 3.
- The first interrupt requests controller cancellation while hosting continues.
  The second interrupts hosting and observation. The coordinator joins both
  operations and any cancellation request before returning. The earlier resume
  NotifyContext path that stopped hosting on the first interrupt is deleted.
- Updated the here and machine guides, enrollment migration, W9 wire prose and changelog. Embedded
  mirrors were generated with sync-docs.sh.

The credential invariant is: no generated runner bearer appears in machine YAML;
its only persisted copy is the private agent-token file. The new fuzz property
checks arbitrary bearer payloads against that invariant. Existing here secrecy
and controller/run-binding fuzz properties remain.

The private attachment callbacks compose existing host/log/cancel operations and
permit channel-driven tests. They add no service, protocol, flag, environment
variable, credential authority or customer limit. The YAML node helper preserves
empty-list presence lost by omitempty; it does not parse a second schema.

## Verification and accounting

Passed with GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4, and GOCACHE,
GOTMPDIR and TMPDIR below /tmp/sw-astra-W9. Test commands used -p 4 or lower.

- Final whole-repository go build -p 4 ./....
- Vet on cmd/sparkwing, internal/clisurface, internal/controllerwire,
  pkg/controller/client and tests/acceptance; final cmd/sparkwing vet repeated
  after its source changed.
- Full clisurface/controllerwire/tests/acceptance suites. internal/acceptance
  compiles and has no test files. Native scenario skips remain unchanged.
- Focused CLI tests for here resume/attachment, retry, runner enrollment/removal,
  log following and generated command reference.
- Final race run for TestHere, TestRetry, TestRunnerEnrollment,
  TestRunnerRemoval and TestRunners.
- New runner credential fuzzer: 13 executions with two workers over three
  seconds, with two seed inputs loaded.
- Pkg/docs mirror/changelog/privacy/read/search/guide/catalog checks, full source
  doccheck, sync-docs --check, commentcheck -base main, gofmt and diff --check.
  Commentcheck is clean across 1,135 Go files.

Added 13 CLI tests and one fuzz property. Existing enrollment, forced replacement,
service-failure, concurrent rollback, revocation and typed retry fixtures were
ported to the final model/callback signature. No parked T1 inventory row names a
W9-owned source function. No new native acceptance cell is claimed.

Tracked Go edits remove 60 production lines and 17 old test lines gross against
this session's starting HEAD. No complete file was deleted. The three new Go
files contain 121 production lines and 400 test lines. This is not a net code
reduction claim.

The first checks failed on the missing new adapters. The final-schema regression
then caught empty labels becoming absent and passes after preserving the node.
One new log fixture omitted required next_cursor/truncated fields; correcting
that fixture made the real-client attachment test pass. No production decoder
was relaxed.

Whole-tree sleepcheck -base main still fails on existing W6
internal/limits/deadline_test.go:58,66 and W4
sparkwing/sparkwingtest/host_test.go:214. No W9 finding was printed. Native
sockets/services and the broad legacy CLI suite were not exercised.

## Amendment and handoff disposition

Every amendment row naming W9 was checked. Earlier completed work was retained.

| Rows | Disposition / remaining integration |
| --- | --- |
| A1-03, A1-08/09/55 | Existing strict errors, mixed/named claims and retained-bearer adapters remain. Here attachment now adds final logs and cancellation. Claim timing/ancestry remain W1/W2. |
| A1-10/11 | Installer and typed source transport evidence remains. W3 source acquisition and W8 selected-engine installation are still stubs; local/initial-trigger source intake remains unfinished. |
| A1-13/14/15/16/17/18 | Existing materialize-only, failure, cancel, private execution, expansion and attempt transport remains. Native owner/executor bindings remain W1/W2/W3/W8. |
| A1-19; handoff finding 3 | Granted identity/exact engine transport remains. W3/W8 still need one real execution-input constructor and hello integration; W9's old local host has not been replaced. |
| A1-20/22 | Existing server-derived provisioning/evidence contracts remain. Native issuer/authentication/audit integration belongs to W1/W2. |
| A1-29/30/34/37 | Signed data/record and lint consumer work remains. Native RemoteCache/OpenRemoteCache and publication composition remain unfinished. |
| A1-38/39 | Immutable cloud payload/sync/exchange wire remains. W8 still needs the unified concrete runner and host loop. |
| A1-40/41/43 | Earlier lifecycle/foreground/update/notices adapters remain. Native runtime, managed units, tick and exact-engine verification remain open. |
| A1-45/47 | Existing settings and guard grammar remain. Schedule intake/timer ownership and captured settings still need composition. |
| A1-49 | W9 now uses the final complete runner schema and private file for enrollment/removal. W8 startup/service/refresh and W2 pool registration remain open. |
| A1-50 | Sealed here public-log and interruption composition is implemented with fakes. Initial trigger, local/detached hosting, target/source selection, cache/publication and legacy client retirement remain unfinished. |
| A1-51; handoff finding 7 | Alias metadata/inspection, v2 schemas and docs remain. W3 native execution must carry the resolved pair. This continuation updates the two changed guides and their mirrors. |
| A1-52/53 | Windows bridge packaging/stamping and native platform/placement/real-repository QA remain unclaimed. Linux component evidence does not satisfy them. |

## Contracts, integration and shared files

No foreign contract was changed or newly requested. Existing questions still
apply: a sealed here reason, publication/result-copy operations, schedule
installation/tick ownership, sizing reset and runner pool registration need
owner resolution. No new human decision is requested.

Native here construction still needs W3/W8's shared RunHost, owner actor,
claim-scoped storage/controller clients, source acquisition, captured env, exact
engine, plan cache and lease keeper through resolveHereRuntime. W7 must provide
persisted-log completeness. Initial trigger still uses its old intake/host path;
this continuation adds no TriggerResponse conversion adapter. Local detached
execution still uses the old consumer and needs the local-controller queue cut.

W8's service renderer still emits sparkwing-runner agent --config in
internal/agentservice/agentservice.go; it must use the final start command with
final startup configuration. W9's manual receipt already names start. The
renderer was not changed from this area. source.Acquire/ForRequest/ForExecution,
wingd.NewLocalLifecycle and blob.OpenRemoteCache are still declared stubs.

No production caller outside W9 ownership changed. Shared files touched are
CHANGELOG.md, pkg/docs/changelog.md, generated pkg/docs/mirror/hotfix-runner.md
and machine-config.md, the generated migration-guide mirror, this handoff, and W9-owned contract prose
contracts/c3-controller-wire.md. No public Go API snapshot changed.

Friction: cold fuzz/race compilation, existing W4/W6 sleepcheck findings and
missing native owner implementations. Overwing discovery worked despite its
installed-binary staleness notice. Git's identity remains Korey Gambill and
core.worktree is unset. The lead retains review, commits, native gates and landing.

---

# W9 v2 merge and inspection continuation

W9 remains incomplete. This continuation started at d448d9d8c with an in-progress
merge of bw-3095-rewrite. All 13 conflict paths have resolved file contents,
including deletion of runs_capacity.go. No conflict markers remain. The index
still records the merge conflicts because SWARM-BRIEF forbids Git writes. The
lead must stage the resolutions and finish the merge. No Sparkwing executable,
service, installed candidate or pipeline ran. Scratch is /tmp/sw-astra-W9 and is
removed before the final response.

## Delivered

- Kept W9's registry-based help and alias metadata behavior while migrating to
  nodeproto.PipelineDescription, ArgumentDescription and canonical API errors.
  Preserved W9's RetryRequest.Validate and the incoming amended wire shapes.
  Kept the incoming v2 protocol/SDK guides and W3 schema tests. Retained the
  capacity implementation's earlier deletion instead of restoring old APIs.
- Inspection requests select the configured entrypoint; RunContext keeps the
  invoked alias. A real Go SDK subprocess test loads project YAML, accepts
  required pipeline/job flags, and observes deploy-prod/deploy-stage in author
  Plan code. This proves inspection, not accepted-engine run hosting.
- Extended W9's C4 grammar so pipeline plan --pipeline NAME -- <args> can supply
  required inputs. It still rejects a pipeline positional. Generated help and
  references use the same rule. This local contract change is documented in
  c4-surface-config.md and the unreleased migration guide. No foreign interface
  changed.
- Plan previews retain only redacted display arguments. SDK requests keep the
  execution values. Structured planning errors use the existing maskedCLIError
  boundary. Secret enum diagnostics expose neither submitted nor allowed values.
  Preserved comma-list enum validation after removing the old SDK CheckEnum API.
- Plan preview JSON now exposes jobs, and text renders Job. Removed the
  unreachable static-plan parser/renderer, its unused trigger renderer, and
  their obsolete tests. Updated repos.go, the remaining preview consumer.
- Ported CLI subprocess fixtures to v2 hello/describe and required fields.
  Removed every pkg/runner import in W9 fixtures in favor of sparkwing.Main.
  Updated fixture entrypoint declarations to their actual SDK registration names.
  Native detached/source/risk suites still require the lead's runnable environment.
- Regenerated published protocol schemas and SDK references, including the
  sparkwingtest page and removal of the planguard page. Fixed sidebar entries,
  final guard/schedule YAML, removed command links and guide examples for Memo,
  Cache, Artifacts, Generate, Approval and Inline. Synchronized embedded mirrors.
- The docs command checker now consumes clisurface.Registry rather than regexes
  over the retired help table. Temporary documentation builds disable VCS
  stamping because they have no repository identity. A negative control first
  showed a nonexistent SDK function passing as a partial example. The fix made
  it fail and exposed 13 stale examples, all migrated. The full docs check now
  reports no SDK drift; fragments remain partial/non-self-contained, not fully
  compiled examples.

Invariant: classified planning arguments reach SDK execution inputs but do not
appear in preview JSON or structured error messages, hints or details, including
encoded forms. The final process-level fuzzer passed 93 generated cases using two
workers for three seconds. Example regressions reproduced the raw preview leak
and rejected argument separator before the fixes.

Mechanisms added: no production package, interface, retry, cache, service,
credential, environment variable, timeout or customer limit. W9's existing
separator metadata now includes plan inspection. Existing shared error masking
and protocol decoding own validation. Undo is to revert these consumers and the
local grammar change together.

## Tests, deletion and evidence

Added ten test functions and one fuzz target. TestSurfacePlanArgumentsContract
passes; TestSurfacePipelineIdentityContract remains passing. No native T1 cell
was unskipped and no T1 inventory port is claimed. Ported W9's existing protocol
and SDK fixture consumers rather than recreating completed transport adapters.

Deleted 532 production Go lines and 251 test lines from the retired inspection
path, measured against HEAD: action_explain.go (381), explain_triggers.go (135),
takeStaticFlag (16), the two old test files (224), and the old snapshot test (27).
The merged action_explain.go had 358 lines before deletion. Regeneration also
removed the 13-line sdk-planguard source page and its mirror. Other replaced
lines are not included in these deletion counts.

All Go commands used GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4 and
GOCACHE/GOTMPDIR/TMPDIR inside /tmp/sw-astra-W9. Tests used -p 4 or lower.
Passing evidence:

- Whole repository go build -p 4 ./... and focused go vet over cmd/sparkwing,
  clisurface, controllerwire, controller/client, doccheck, orchestrator,
  pkg/docs and tests/acceptance.
- Full clisurface, controllerwire, doccheck and tests/acceptance packages.
  Acceptance includes incoming W4 component evidence; native cells retain their
  existing skips and are not claimed complete by W9.
- CLI plan, argument/help, alias, describe cache, source binding, template,
  surface-error and generated-reference tests, including real SDK planning.
- Engine/data transport tests in controller/client. Full client tests reach
  httptest listeners and are refused by the sandbox.
- Protocol schema/transcript tests in orchestrator and published-schema tests
  in doccheck. Full documentation check and all 61 documentation tests outside
  the TestWebClient family. WebClient needs native sockets.
- FuzzPlanInspectionDoesNotDisplaySecrets, commentcheck -base main,
  sync-docs --check and git diff --check.

Sleepcheck -base HEAD still reports incoming W4 code at
sparkwing/sparkwingtest/host_test.go:214 (time.After). W9 adds no sleeps.
The native error writer is also a foreign stub: apierr.WriteJSON and WriteHTTP
panic. Tests inject an encoder through the existing callback and verify the
real W9 masking/projection. W2 must implement its existing contract.

## Remaining amendments and handoffs

Handoff finding 7: W9 discovery, parsing and v2 inspection now use the configured
pair, including author Plan context. Real run execution remains W3/W8/W9 work:
RunHosted still passes cfg.Pipeline as the SDK selector. Finding 3/A1-19 still
requires one accepted-engine/granted-runner constructor and native hello evidence.
The CLI's inspection hello is not selected-engine or awarded-runner proof.

The prior ledger remains in force. Completed adapters were not reimplemented.
Unfinished W9 work and its real integration points:

| Rows | State after this continuation |
| --- | --- |
| A1-10/11 | Go/TS installer adapters remain implemented. W3 source Acquire/ForRequest/ForExecution and W8 selected-engine resolution/install remain stubs. Local/detached/here intake must use their accepted source and engine contracts. |
| A1-29/30/34 | Data signer/record transport remains tested. Local host RemoteCache composition is unfinished; blob.OpenRemoteCache is a stub. Publication is unfinished and must preserve the local verdict. |
| A1-39/40/41 | W9 lifecycle/foreground/update adapters retain fake evidence. Native runtime, managed units, stable tick, runner startup and final local queue composition remain. NewLocalLifecycle is a stub. |
| A1-43 | Shared notices and upgrade verifier remain implemented. Native exact-engine upgrade/index/release verification remains W8 integration. |
| A1-45/47 | Final guard/schedule examples now parse. Native schedule intake must retain captured settings and infer batch only below explicit settings. |
| A1-49 | W9 credential-file and rollback work remains implemented. W8 must connect final startup/service paths. |
| A1-50 | Inspection, redaction, grammar, private wire and docs advanced. Final local/detached/here hosts, source intake, publication/cache and obsolete host/client removal remain unfinished. |
| A1-51 | W9 v2 fixtures, guides, generated schemas/reference and drift checks advanced. Native old-host retirement and parity remain other owners' work. Broader legacy runtime prose, especially local-execution/security, still needs the final host cutover audit. |
| A1-52/53 | Native Windows bridge packaging, supported-platform/placement evidence and real-repository launch QA remain unclaimed. Linux process tests cannot satisfy those cells. |
| A1-55 | Existing sealed-resume/private-bearer adapter evidence remains. Native here binding, reclaim and requester authority remain W1/W2/W3/W8/W9 integration. |

No new foreign contract change is requested. Earlier initial-here reason/audit
and authorized working-tree source-bundle proposals remain open with W6/W1/W2/W7.
W9 still owns substantial composition work beyond these foreign stub bodies.

## Shared files and friction

Shared changes are internal/doccheck/{main,main_test,cliverbs,cliverbs_test,
auxdocs,auxdocs_test,protocolschema_test}.go, the merge resolution in
internal/orchestrator/node_protocol_schema_test.go, the C4 contract, the acceptance
identity-contract test, CHANGELOG.md, generated pkg/docs content,
charts/sparkwing-full/README.md and web/README.md. No foreign production API was
removed or changed. The sole additional production preview caller migrated is
W9-owned cmd/sparkwing/repos.go.

Friction: native sockets are refused, merge index writes are forbidden, source,
engine, storage and lifecycle native bindings are incomplete, and W2's canonical
error writers remain stubs. The first fuzz build overlapped deletion of the dead
source file and failed to compile. Its completed rerun passed. Git identity is
Korey's default and local core.worktree remains unset. The lead owns review,
staging, merge completion, pipelines, landing and native launch QA.

# W9 pipeline identity continuation

W9 remains incomplete. This continuation started clean at f91bb20ac. No Git
write, Sparkwing executable, service, pipeline or installed candidate ran. Writes
stay in this worktree and /tmp/sw-astra-W9, which is removed before reporting.

## Implemented

- Added clisurface.PipelineIdentity and ResolvePipeline as W9's shared mapping
  from an invoked project name to its registered SDK entrypoint. The resolver
  takes validated project configuration and captures string values without
  process globals. Unconfigured pipelines keep the same name for both fields.
- Local argument parsing, help, pipeline catalog metadata, flag completion,
  pipeline completion, describe-secrets selection and inspection parsing select
  the entrypoint schema. Their public names and argument diagnostics retain the
  invoked alias. Multiple aliases can share an entrypoint. Missing entrypoints
  cannot fall back to an unrelated registration named after the alias.
- Removed the foreground risk gate's second describe/cache lookup. It consumes
  the schema already selected for argument parsing. Detached risk checking uses
  the same binding rule and rejects a missing entrypoint. The regression first
  reproduced an alias bypassing the entrypoint's prod risk declaration.
- Source-revision inspection binds acquired SDK code to the invoking checkout's
  project declarations. Invalid binding configuration fails before describing
  pipeline code. Config errors do not echo the invalid value.
- Corrected the pipeline registry guide's name/entrypoint explanation, stale
  project-profile and guard prose, removed its webhook declaration example and
  synchronized its embedded mirror and the changelog.

The security invariant is that flags, secret classifications and risks for a
configured pipeline come from its selected entrypoint even when another SDK
registration has the alias's name. FuzzPipelineAliasMetadataIsolation generates
alias/entrypoint collisions in both schema orders and checks secret classification
and risk refusal. It passed 1,352 executions with two workers over three seconds.

Mechanism added: one pure shared identity resolver because replacing only the
help name would leave argument, completion and risk readers selecting different
registrations. Undo is to revert the resolver and its consumers together. No
public flag, environment variable, service, grant, timeout or restriction was added.

## Tests and deletion

Added eight test functions and one fuzz target. TestSurfacePipelineIdentityContract
passes in tests/acceptance for local/trigger grammar, shared entrypoints, captured
identity, unconfigured pipelines and invalid configuration. CLI tests cover
required pipeline/job flags, alias help/errors, catalog metadata, completion,
risk refusal, missing entrypoints, invalid config and source-revision binding.
SDK-shaped subprocess fakes establish inspection behavior only. They still use
the retained legacy session and do not establish v2 or matched-engine execution.
No native T1 cell was unskipped. No inventory function was assigned directly to
W9, and no additional T1 inventory port is claimed.

Deleted 49 gross production Go lines and eight test lines. This removes
declaredRisks, alias-name schema selection at the migrated call sites, the
foreground fail-open cache lookup and its DegradesGracefully test. No whole
legacy hosting file was retired. The native hosting hard cut remains unfinished.

All Go commands used GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4 and
GOCACHE/GOTMPDIR/TMPDIR under /tmp/sw-astra-W9. Tests used -p 4 or lower.
Passing evidence on final Go source:

- Whole-repository `go build -p 4 ./...`.
- Focused go vet over cmd/sparkwing, clisurface, controllerwire,
  controller/client, pkg/docs and tests/acceptance.
- Full clisurface, controllerwire, doccheck and tests/acceptance packages.
- CLI alias, risk, describe-cache, source-binding, argument, help, plan-inspection,
  registry, discovery, command-reference and shipped-prose selections.
- All 61 pkg/docs tests outside the TestWebClient family.
- FuzzPipelineAliasMetadataIsolation, commentcheck -base HEAD, sleepcheck
  -base HEAD, sync-docs --check and git diff --check.

The retained TestPersistSubmissionRefusesDeclaredRiskBeforeQueuing fails with
no such column: pipeline. The same test fails with the same error using an
overlay restoring HEAD's CLI source and excluding this continuation's tests.
It reaches legacy store reads after the risk refusal. This is not claimed as
a passing detached-submission integration test.

The first full pkg/docs run reaches a native httptest listener and is refused
by the sandbox. The 61-test selection excludes the TestWebClient family and
passes. The required whole-tree commentcheck -base main still reports the two
incoming W1 comments in pkg/store/state_materialize.go:15 and
pkg/store/state_result_records.go:9. This continuation's diff is comment-clean.

## Remaining handoffs and amendments

Handoff finding 7's W9 metadata/argument half is implemented. Its final execution
half remains open: Request.Pipeline must select the entrypoint and
RunContext.Pipeline must carry the alias through real SDK planning and author
code. W9's retained inspection session is repos.Session, whose Call accepts
RequestOld; nodehost.Session also accepts RequestOld. W3/W4 must provide the
shared v2 describe/plan session boundary with verified engine/runner hello.
Do not add a second CLI protocol implementation or an old/new request adapter.
ResolvePipeline is available for the shared host's constructor.

Handoff finding 3 and A1-19 still need W3/W8's execution-input constructor and
native hello tests. W9's already implemented wire preserves the granted runner
and selected engine, but that is not constructor evidence.

The earlier amendment action ledger remains in force. No completed transport,
installer, profile, queue, enrollment or schema work was redone. Remaining W9
composition covers A1-10/11, A1-29/30/34, A1-39/40/41, A1-43,
A1-45/47/49/50/51/52/53 and A1-55: local/here/detached hosting, source intake,
read-only cache/publication, native lifecycle and managed schedules, runner
startup, selected-engine execution, Windows packaging and native acceptance.
The other rows retain their existing W9 component evidence and owner integration
requirements. Initial here intake still needs the W6/W1/W2 sealed reason/audit
proposal. Working-tree intake still needs W1/W2/W7's authorized source-bundle
handoff. No foreign frozen interface changed.

Local W9 contract addition: PipelineIdentity/ResolvePipeline and the pair's
semantics are recorded in c4-surface-config.md. No new human decision is requested.

## Shared files and friction

No production file outside W9 ownership changed, so no cross-package production
caller migration was needed. Shared files touched are CHANGELOG.md,
pkg/docs/changelog.md, pkg/docs/mirror/pipelines.md,
tests/acceptance/pipeline_identity_contract_test.go,
design/rewrite/contracts/c4-surface-config.md and this handoff.

Friction: native sockets are unavailable, the legacy detached-store test is
incompatible with the merged schema, the v2 inspection session is absent, and
the two incoming W1 comments fail whole-tree commentcheck. Standards discovery
works despite its stale installed-version notice. Git identity remains Korey's
default and local core.worktree is unset. The lead owns review, commits,
pipelines, landing and native launch QA.

# W9 Go preparation continuation

W9 remains incomplete. This continuation started clean at 7d6e534c2. The
previous merge is committed and there are no unmerged paths. No Git write,
Sparkwing executable, pipeline, service or installed candidate ran. All scratch
stayed under /tmp/sw-astra-W9 and is deleted before reporting.

## Implemented

- Replaced the Go preparation refusal in __sdk-install with the existing local
  binary cache and shared compiler. The installer retains its exact-pin checks
  before preparation and its pin-drift check afterward. Cache warm now uses the
  same Go preparation path instead of failing at that refusal.
- Hard-cut the compiler, uncached compiler and Go recovery helpers to explicit
  environment and diagnostic inputs. Updated every caller. Go executable lookup,
  workspace selection, overlay selection, floor probing, compilation and
  missing-go.sum recovery consume the captured environment. The CLI passes its
  masking stream through compiler output, notices and recovery output. Writer
  errors and cancellation fail the operation.
- Cache keys include captured Go build settings, including GOFLAGS, target and
  CGO controls. Workspace dependency hashing uses the captured GOWORK selection.
  The normalized workspace content owns its identity, so relocating an otherwise
  identical workspace does not change its key. Credentials and run IDs do not
  become compilation inputs or printed key details.
- Reproduced a failed toolchain probe leaking proxy credentials through the
  global debug logger. Removed raw probe errors from that log. The compiler's
  actual output continues through its caller's diagnostic writer.
- Updated the cache command registry, generated reference page, changelog and
  embedded mirrors. Reversed the cache-warm missing-installer test. Updated the
  docs breadcrumb test to use repeated Flags headings because the earlier
  command rewrite removed its Examples headings.

The security invariants are that controller credentials never enter compiler or
recovery subprocess environments, and registered proxy credentials never appear
in compiler or probe logs, including encoded forms. Both have fuzz targets.
The failed-probe regression failed before the one-line logging fix and passes
after it. Failed builds publish no cache entry. Successful cache reuse compiles
once, and missing-sum recovery stays under the same environment/masking boundary.

Assumptions/mechanisms: CompileOptions carries the two inputs the shared compiler
previously obtained from process globals. Extending that compiler avoids a
second compiler or global stdout/environment swapping. Existing helper APIs now
require these inputs, with no old-signature adapter. Go build settings extend the
existing cache key because reusing a binary built with another setting would be
silently wrong. Undo is to revert these helper-signature, key and caller changes
together. No public flag, environment variable, service, timeout or grant was
added. These helpers are shared implementation APIs, not frozen contract changes.

## Tests and accounting

Added ten test functions and two fuzz targets across three files. Reversed one
cache-warm stub expectation. No T1 inventory function names a W9-owned package,
and no inventory port is claimed. Replaced 69 gross production Go lines,
including the Go preparation refusal and ambient-only helper signatures. No
whole legacy host file was retired. Initial trigger, legacy here hosting and
local/detached host hard cuts remain unfinished.

All Go commands used GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4 and
GOCACHE/GOTMPDIR/TMPDIR under /tmp/sw-astra-W9. Tests used -p 4 or lower.
Passing evidence on final Go source:

- Whole repository: go build -p 4 ./...
- go vet -p 4 on cmd/sparkwing, internal/bincache, internal/gotoolchain,
  internal/clisurface, internal/cluster, internal/orchestrator, internal/repos
  and pkg/docs.
- Full internal/clisurface, internal/controllerwire, internal/gotoolchain,
  internal/doccheck and tests/acceptance packages.
- cmd/sparkwing selection ^(TestCache|TestSDKInstall|TestLocalPipelineBinary|
  TestWarmTypeScript|TestPipelineToolchain|TestPipelineCacheKey).
- cmd/sparkwing, internal/bincache and internal/gotoolchain selection
  ^(TestCache|TestSDKInstall|TestLocalPipelineBinary|TestWarmTypeScript|
  TestPipelineToolchain|TestCapturedGo|TestGoCommands|TestCompilePipeline|
  TestPipelineCacheKey|TestBuildEnv|FuzzSDKInstallGo).
- The existing CLI registry, help, discovery and shipped-prose selection from
  the preceding continuation, including TestCommandReferenceMatchesRegistry.
- Pkg/docs read/list/search, content, catalog, changelog, guide, migration,
  section, breadcrumb, command-name and privacy selections.
- FuzzSDKInstallGoNeverReceivesControllerCredential: 924 executions, 3s,
  two workers.
- FuzzSDKInstallGoMasksCompilerDiagnostics: 1,645 executions, 3s, two workers.
- commentcheck -base HEAD over the entire tree, sleepcheck -base HEAD,
  sync-docs --check, gofmt and git diff --check.

TestSDKInstallGoWithNativeCompiler exercised the real Go compiler and produced
a regular executable through sdkInstaller.run. Its pin/version readers are
fakes because W8's engine.ReadPin and VersionForPin remain panic stubs. It does
not establish a working default __sdk-install invocation, matched engine fetch,
RunHost integration or native acceptance cell. No new T1 cell was unskipped.
The existing acceptance-contract suite passes with its required native skips.

The required commentcheck -base main . ran and still fails only on incoming W1
comments at pkg/store/state_materialize.go:15 and
pkg/store/state_result_records.go:9. W9's diff is comment-clean. No deployment,
PostgreSQL, Windows/Darwin, Docker, real-repository QA or full native suite ran.

## Remaining contracts and real integration

No foreign frozen contract was changed. This continuation advances A1-10 and
A1-50's Go preparation/masking work. The earlier amendment ledger still applies.

- W8: implement engine.ReadPin, VersionForPin, Resolved.InstallSDK and verified
  matching-engine resolution. The native compiler check used fake pin metadata.
  W3 must bind acquired source and host execution to that selected engine.
- W3/W8: concrete RunHost/owner/source/storage/plan-cache/lease construction,
  RemoteOwnerStore and NewLocalLifecycle still require implementation.
- W6/W1/W2: initial here intake still has no sealed reason/audit proposal. Keep
  this as a foreign-contract change request rather than hiding it in actor,
  argument or environment fields.
- W1/W2/W7: working-tree submission needs an authorized source_bundle handoff.
  The old CLI uploads before intake, while the final UploadIdentity requires a
  live owner/publication or job binding. Specify how the sealed digest becomes
  authorized and ready before another runner claims it. No account upload
  authority or pre-intake reservation was invented here.
- W1/W2: failed/all retry selection remains absent from the merged Retry intake
  transaction. Its adapter calls AdmitRun without retaining RetryRequest.Mode.
- W9: initial trigger migration, local/detached hosting, public log following
  and signals around the shared host, read-only cache/publication composition,
  local schedules/tick and managed-unit wiring remain incomplete. The initial
  trigger still uses TriggerResponse and the legacy host. No compatibility
  adapter was added.

There is no new human decision request. The foreign contract gaps need the
owning areas or lead to amend the declarations before those pieces can be cut.

## Shared files and friction

Outside W9 ownership, the shared compiler changes are in
internal/bincache/bincache.go and internal/bincache/toolchain.go, plus
internal/gotoolchain/toolchain.go. Migrated production callers are
internal/cluster/trigger_loop.go, internal/orchestrator/local_trigger_loop.go,
internal/orchestrator/run_node_remote.go and internal/repos/resolver.go.
Their changes only supply the inputs the old helpers inferred.

Migrated shared tests are internal/bincache/bincache_test.go,
cachedir_test.go, cancel_test.go, owners_test.go, portable_test.go and
toolchain_floor_test.go. New shared compiler tests are compiler_inputs_test.go
and compiler_inputs_unix_test.go in that package. The portability fixture now
sets GOFLAGS=-buildvcs=false rather than clearing the worker's required flag.
Other shared files are pkg/docs/sections_test.go, CHANGELOG.md,
pkg/docs/changelog.md, pkg/docs/mirror/cli-cache.md and this handoff.

Friction: the first fuzz compilation was invalidated by source edits made while
its cold instrumented build was running. A frozen-source rerun passed, followed
by both fuzz targets. Required native cells remain unavailable in the worker's
socket-free verification path. Git's effective author is Korey Gambill and
core.worktree is unset. The lead owns independent review, commits and landing.

---

# W9 merge, local transport and recovery continuation

W9 remains incomplete. This continuation started at 68bef3fb7 during the merge
of bw-3095-rewrite. All 20 conflicted file contents are resolved. There are no
remaining conflict markers. The index still lists the paths as unmerged because
the swarm brief prohibits worker Git writes. The lead must stage the resolved
paths and finish the merge. No Sparkwing binary, pipeline, service or Git write
ran. Scratch is confined to /tmp/sw-astra-W9 and is deleted before reporting.

## Implemented

- Kept W9's final registry, help and command docs instead of restoring removed
  commands. Preserved incoming store retention guidance in the migration guide
  and source-bundle security prose. Took the incoming hostedBounces removal and
  kept removed CLI callers out of the default-team inventory and client snapshot.
- Final CLI local clients now use a socket transport without the legacy hosted
  retry layer. The shared C2 route policy owns retries for HTTP and api.sock.
  Dial, request and idle settings retain their preceding values.
- Retained here authority now binds the actual absolute API socket path as well
  as controller URL and run ID. Handoff and resume derive it from the captured
  transport. A second home, another socket, or an unbound network transport cannot
  recover the local bearer. Remote URLs remain usable without a socket binding.
  Private records contain the socket selector but no new execution inputs.
- Non-retryable requests disable body replay in net/http. Its standard transport
  otherwise retries keyed POSTs on reused connections after losing a response,
  even when the engine's own retry loop is disabled. The regression reproduced
  two here-intake and retry-intake requests before this change, and one afterward.
  This applies to the existing route policy rather than a second retry list.
- ReadFailure now preserves JSON null as no recorded failure, matching the merged
  store's nullable result. Other successful controller responses still require
  objects. A present structured error cannot be null and still uses DecodeInner.
  This closes the earlier absent-failure wire question without synthesizing an
  SDK failure or changing another area's Go interface.
- Updated the here guide, W9 wire contract prose and changelog, then regenerated
  embedded documentation with the owning sync script.

Invariants: retained authority cannot cross controller/run/socket bindings, and
an award-capable request is never automatically replayed after response loss.
Both have fuzz tests. The HTTP test uses real net/http connection reuse over
net.Pipe, so it exercises the standard transport's replay behavior without a
native listener. The local transport tests also cover dial refusal and 503 replies.

Assumption/mechanism: the small CLI socket transport wrapper retains its endpoint
identity while removing the old retry wrapper. URL-only identity cannot distinguish
homes. Undo removes the wrapper and socket-bound storage together. No public API,
flag, environment variable, timeout, grant or customer restriction was added.
The private record filename now hashes all three binding components. No legacy
record reader or compatibility adapter was added.

## Tests and accounting

Added seven test functions and two fuzz targets. The new
TestRecoveryFailureAbsenceContract passes over HTTP and api.sock-shaped fake
transports. Existing W9 acceptance contract tests and the full acceptance package
pass. Native T1 cells remain skipped and are not claimed complete.
No inventory function is assigned directly to W9, and none was ported here.

Replaced 15 gross production lines across controller_client.go, here_credentials.go,
engine.go and engine_methods.go. No complete legacy host file was retired. Initial
trigger intake, legacy here hosting, local/detached hosting and their hard cuts
remain unfinished. Incoming areas' deletions are not counted as W9 implementation.

All Go commands used GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4, -p 4,
and GOCACHE/GOTMPDIR/TMPDIR under /tmp/sw-astra-W9. Passing evidence:

- Whole-repository go build -p 4 ./... on final Go source.
- Focused go vet over cmd/sparkwing, clisurface, controllerwire,
  controller/client, doccheck and tests/acceptance.
- Full clisurface, controllerwire, doccheck and tests/acceptance packages.
- Controller/client selection ^(TestEngine|FuzzEngineNoReplay).
- CLI selection ^(TestHere|FuzzHere|TestLocalController|TestRetry|FuzzRetry|
  TestParseTrigger|TestSDKInstall|TestCacheWarm|TestInvocation|TestTriggeredOutcome|
  TestSurface|TestAllCommands|TestDiscovery|TestEveryDiscovered|TestCommandsMarkdown|
  TestCommandReference|TestEmbeddedReference|TestEveryPageExemption|
  TestRepoDocsNameOnlyDispatched|TestShippedProse|TestHonestyCheck|TestPrintHelp|
  TestHelpListing|TestCommandFlags|TestExamplesUse).
- Pkg/docs content, changelog, privacy, read/list/search, section, guide, migration,
  catalog and command-name checks using the preceding continuation's selection.
- FuzzEngineNoReplayPolicySurvivesHTTPReuse: 1,930 executions, two workers, 3s.
  FuzzHereCredentialsNeverCrossSockets: 421 executions, two workers, 3s.
- Scoped W9 commentcheck -base HEAD, whole-tree sleepcheck -base HEAD,
  sync-docs --check, gofmt and git diff --check.

The required commentcheck -base main . ran and failed only on incoming W1
narration at pkg/store/state_materialize.go:15 and
pkg/store/state_result_records.go:9. Those files are outside W9 and were left
untouched. The whole-tree comment gate is not claimed clean. No native socket,
PostgreSQL, deployment, platform or real-repository launch QA ran.

## Remaining contracts and real integration

The existing amendment action ledger below remains applicable. This continuation
advances A1-03, A1-08/A1-09/A1-55, A1-14 and A1-50. No foreign contract was changed.

- W1/W2: selected retry still needs a transaction contract. The merged
  EngineController.Retry now calls AdmitRun but ignores RetryRequest.Mode.
  It must validate and retain failed/all selection in intake and idempotency.
  The earlier report that both adapters were stubs is superseded by this finding.
- W6/W1: initial here intake still lacks its sealed reason/audit proposal.
- W3/W8: supply the concrete shared RunHost and owner, source, engine, storage,
  plan-cache and lease bindings. RemoteOwnerStore methods and
  NewLocalLifecycle remain stubs. Fakes do not establish native execution.
- W9: finish initial trigger and local/detached hosting, public log following and
  signal behavior around the shared host, read-only cache/publication composition,
  native schedule/tick and managed-unit wiring, and Go preparation. The initial
  trigger still uses TriggerResponse and its legacy host. No adapter was added.
- W2/W7/W8: finish signed-data bindings, real local runtime/unit constructors,
  exact engine/installer execution and native acceptance. DataController.BindData
  and engine.Resolved.InstallSDK remain unimplemented in this tree.

Shared files touched are internal/orchestrator/hosted_run.go and
pkg/store/default_team_callers_test.go for conflict resolution, the client API
snapshot, tests/acceptance/recovery_failure_contract_test.go, CHANGELOG.md,
embedded docs/changelog, the W9 wire contract prose and this handoff. Other
changes are within W9 ownership. No new cross-area caller migration was needed.

Friction: Git staging is prohibited, native listeners and Sparkwing binaries are
outside this worker's verification path, and the two W1 comments block the
whole-tree comment gate. Instrumented fuzz compilation needed a cold cache.
Git identity remains Korey Gambill and core.worktree is unset. The lead owns
independent review, merge completion and landing.

---

# W9 private here handoff and resume continuation

W9 remains incomplete. This continuation started clean at 2c8def4ac. All writes
stay in this worktree and /tmp/sw-astra-W9, which is deleted before reporting.
No Git metadata, candidate runtime, service, release or Sparkwing pipeline was
changed. Installed discovery info was read during bootstrap before loading the
swarm brief. No Sparkwing binary was run afterwards.

## Implemented

- `pipeline trigger --resume` now resolves a stable ID or team number through
  the public controller reader, loads the retained here bearer for that exact
  controller/run, and makes one named trigger claim. It creates no run and
  consults no checkout, pipeline arguments or machine settings to replace seals.
- Here retries retain their one-time bearer before hosting. The retry connection
  captures its controller options once. A changed profile cannot redirect that
  handoff through another resolution. Missing native hosting leaves the private
  record intact and returns exit 3 with the allocated run in JSON.
- Private records contain only controller URL, stable run ID and bearer. Their
  filenames hash the complete normalized binding. The fssecure directory/file
  APIs enforce owner-only access and refuse symlinks. Reads reject malformed,
  foreign and empty records without echoing their contents. A saved bearer is
  never overwritten with different authority. A replay carrying another raw
  bearer is refused before private storage. Failed writes remove only the
  file this call created. No durability lock, fsync or cleanup sweep was added.
- The claim adapter checks the requested run against the award and fence, keeps
  retry destination identity separate from the original sealed request, verifies
  request/spec/plan hashes, and passes the immutable inputs to nodehost.RunHost.
  Accepted plan decoding refuses unknown fields. Full plan capability and
  semantic validation remains the host's responsibility.
- The runtime binder receives the complete server award, including granted
  runner identity, deadlines and generation. It supplies owner/storage/engine/
  plan-cache bindings. W9 supplies the awarded request and accepted seals rather
  than trusting replacement inputs from the binder. The native resolver returns
  unavailable before Claim until those shared constructors exist.
- Cancellation is checked before credential reads, claiming, construction and
  execution. A constructed session closes once even if cancellation prevents its
  start. Cleanup failure and missing terminal outcomes remain incomplete. The
  CLI emits only run ID and terminal outcome after successful hosting.
- Moved the existing installer error masker into the shared CLI error owner.
  SDK installation and warming use the same implementation. Here diagnostics
  register retained and awarded bearers plus request/resolved argument values.
  If a private value appears in an error code, output uses internal instead of
  echoing it. Message, hint and details remain masked, and errors.Is preserves
  the original cause. The diagnostic fuzz seed failed before the code-field fix.
- Replaced the here guide's old one-claim-only rule with the final reclaim,
  retained-bearer and sealed-resume contract. The guide states the native gaps.
  An embedded-doc test parses its resume example against clisurface. Source
  documentation, changelog and embedded mirrors agree.

Security invariants: a retained bearer is used only for its recorded controller
and run; no retained/awarded bearer or private argument reaches diagnostics; an
award cannot change the named run fence. Each has a fuzz target. No new flag,
environment variable, timeout, cap, grant or authority rule was introduced.

## Tests, deletions and evidence

The new acceptance contract tests pass:

- TestHereResumeClaimContract, over HTTP and api.sock-shaped fake transports.
- TestHereClaimResponseLossIsNotRetried.

These are contract proofs. Native T1 lifecycle cells remain skipped because the
real drivers are stubs. No native socket, store transaction, secret release,
lease keeper, platform process or deployment behavior is claimed.

Added focused tests for immutable retry/resume inputs, private record binding,
malformed/tampered records, Unix public-file/symlink refusal, lost handoff,
numeric selectors, cancellation checkpoints, cleanup failure, receipt privacy
and the guide example. Three new fuzz targets exercised 1,628 inputs in their
bounded runs (1,607 credential-binding, 7 diagnostic and 14 award-fence executions).
The parked inventory assigns no source function directly to W9. No additional
inventory function was ported in this continuation.

Removed 51 gross lines across cache_warm.go, pipeline_trigger.go, runs_retry.go
and sdk_install.go, plus 21 source-guide lines. Thirty-four production lines
were relocated masking logic, rather than retired behavior. No complete legacy
host file was deleted. The initial legacy trigger and here-host paths still
need their hard cut.

Go commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4, -p 4 and
GOCACHE/GOTMPDIR/TMPDIR beneath /tmp/sw-astra-W9. Passing evidence:

- Whole-repository go build -p 4 ./... .
- Focused go vet for cmd/sparkwing, clisurface, controllerwire,
  controller/client, doccheck and tests/acceptance.
- Full clisurface, controllerwire, doccheck and tests/acceptance suites.
- CLI selection ^(TestHere|FuzzHere|TestRetry|FuzzRetry|TestParseTrigger|
  TestSDKInstall|TestCacheWarm|TestInvocation|TestTriggeredOutcome|TestSurface|
  TestAllCommands|TestDiscovery|TestEveryDiscovered|TestCommandsMarkdown|
  TestEmbeddedReference|TestEveryPageExemption|TestRepoDocsNameOnlyDispatched|
  TestShippedProse|TestHonestyCheck).
- Controller/client -run ^TestEngine. Pkg/docs mirror/changelog, privacy,
  read/list/search, guide, migration, section, catalog and command-name checks
  using the preceding continuation's Test(PkgDocsContent|EmbeddedChangelog|
  EmbeddedDocsCarry|EveryRecordedScrub|ScrubPatterns|Read_|ReadGuide|Migrations|
  List|Search|Sections|SectionBody|Guide|Catalog|DocsName) selection.
- Each new fuzz target with -run ^$, -fuzztime=3s and -parallel=2.
- Required commentcheck -base main . (592 Go files), sleepcheck -base HEAD .,
  sync-docs --check, gofmt and git diff --check.

The first test compilation failed on the missing new adapter. The new checkpoint
cases then reproduced acceptance of changed/missing request seals and startup
following cancellation after construction. Both now pass. A fixture initially
used an invalid repository key and later an obsolete generators plan field.
Those fixture errors were corrected to the final contracts. Earlier API snapshot
drift in seven unrelated packages remains as recorded below. This continuation
changes no exported Go API and does not regenerate those baselines.

## Remaining integration and contract questions

No foreign contract was changed or newly requested. The W9 wire prose now
records retained-bearer resume. Its Go types and C2 route/authority policy are
unchanged. Existing W1/W2 selected-retry transaction and W6 sealed here-reason
questions remain open.

W3/W8 must supply the concrete shared RunHost and native composition through
resolveHereRuntime: owner actor, claim-scoped controller/data clients, source
acquisition, captured environment, exact engine, plan cache and lease keeper.
W9 still needs public log following and the final signal/cancellation behavior
around that host. Constructor fakes prove handoff behavior only.

Before native api.sock here hosting is bound, W9 must represent the selected
local socket/home in the retained-credential binding. The shared display URL
alone cannot distinguish separate local controllers. The current native resolver
refuses before any claim, so no native claim sends a retained bearer through that
ambiguous identity. The new private-file and resume fakes use real HTTP controller
URLs. The api.sock-shaped acceptance fake proves wire behavior only.

Initial pipeline trigger intake and its legacy here host are still unchanged.
They cannot feed legacy TriggerResponse into the final adapter. Cut them over
with the final sealed request/source proposal and recorded here reason, then
delete pipeline_trigger_here.go's old host helpers and worker.go's legacy host
path. No conversion or compatibility adapter was added.

Local/detached hosting, publication and read-only cache composition, controller
schedule/tick and managed-unit construction, and Go preparation remain open as
recorded in earlier handoffs. W1/W2 still need selected-retry materialization,
and native T1/QA remains lead work. These are unfinished integrations, not
approved feature cuts.

## Shared files and friction

Shared changes: tests/acceptance/here_contract_test.go, CHANGELOG.md,
pkg/docs/changelog.md, pkg/docs/mirror/hotfix-runner.md, the W9 wire contract
prose and this handoff. All other changes are W9-owned. No production caller in
another area's package needed migration, and no exported API was removed.

Friction: shared runtime constructors and real acceptance drivers remain absent.
The first fuzz build required a cold instrumented cache. Final checks use fakes
and private files within the allowed scratch root. Git identity remains Korey
Gambill and core.worktree is unset. The lead owns review, commits and landing.

---

# W9 retry and triggered-outcome continuation

W9 remains incomplete. This session started clean at 83fe557b8 with no merge
conflicts. Git metadata is unchanged. Bootstrap read installed discovery info
before the swarm brief was loaded. No candidate runtime, daemon, service,
Sparkwing pipeline or release ran. All writes stayed in this worktree and
/tmp/sw-astra-W9, which is removed before the final report.

## Implemented

- Replaced runs retry's pflag/direct-store/standalone path with clisurface and
  Controller.Retry through the existing local/profile connection factory.
  Repeated --run, comma lists and stdin IDs are deduplicated before intake.
  Invalid selection/output, help and cancellation are handled before connection.
  Each source gets a separate idempotency key. Failure receipts retain structured
  errors, successful receipts retain the allocated number and replay marker, and
  output failures or cancellation stop further submissions.
- Added RetryMode (failed/all) and RetryRequest.Validate in W9's wire owner.
  The original wire request had no way to carry the required CLI selection.
  Missing/unknown mode and missing key fail before transport. Existing retry
  transport fixtures now supply the mode. Automatic replay remains disabled
  because the source may hold a here reservation.
- Kept retry destination identity separate from the original request seal.
  Invariant: retry cannot rewrite the original sealed request. A regression first
  reproduced the mistaken requirement that the two run IDs match. The receipt
  now accepts the original request ID unchanged across retry chains. The wire
  contract explicitly records the two identities.
- Retry output projects only source/new IDs, number, replay and structured error.
  Invariant: changing sealed inputs or a one-time here bearer cannot change
  public output or diagnostics. The fuzz test varies those private inputs and
  compares output/diagnostics with an independent baseline. Here retries return
  incomplete exit 3 after intake until a final host binding exists. Their JSON
  failure receipt identifies the new run without its bearer. No claim or legacy
  here adapter was added.
- Post-trigger outcome reads now use one public RunDetail through RunReader.
  Removed the old separate run/node/step/approval reads and unconditional retry
  timer. The shared transport handles qualified transient failures. Permission
  refusals are not retried. Failure summaries use job details, and run status,
  upgrade output and trigger output share the engine-notice writer.
- Deleted the undispatched trigger-management CLI and unused legacy RetryRun and
  ListTriggers client methods. A remaining orchestrator contention test now polls
  runs through its existing client instead of the removed trigger-list method.
- Updated retry source documentation, migration notes, command rules and the
  generated CLI reference. The embedded mirrors/changelog and W9 client API snapshot agree.
  CLI documentation was regenerated in-process without running a Sparkwing CLI.

## Tests and deletions

Added 14 test functions and one fuzz target. TestRetrySelectionContract passes
for failed/all across both HTTP and api.sock-shaped fake transports.
TestRetryRejectsUnspecifiedModeBeforeTransport passes its invalid-input cases.
The full acceptance package passes with its native T1 skips unchanged. These
are contract tests, not native lifecycle or transaction acceptance.

Ported the old terminal-outcome summaries, unavailable-read refusal, retry of a
qualified transport blip and invalid-connection behavior to the final reader and
shared client boundaries. Replaced old standalone/local retry-refusal fixtures
with a controller-connection refusal test. Final preservation of execution env,
seals and authorized successful-job reuse remains W1/W2/W3/W6 acceptance work.
The parked T1 inventory assigns no source function directly to W9.

Removed 742 gross lines across the retired retry/trigger/outcome paths:
166 in runs_actions.go, 206 in triggers.go, 78 in legacy client methods,
45 in the old outcome helper, 30 in the old process retry test and 217 in the
standalone/outcome test files. This excludes smaller replacements and generated
files. Three complete files were deleted: cmd/sparkwing/triggers.go,
cmd/sparkwing/runs_standalone_test.go and
internal/orchestrator/remote_run_outcome_test.go (423 lines total).

## Verification

All Go commands used GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4, -p 4
and GOCACHE/GOTMPDIR/TMPDIR under /tmp/sw-astra-W9.

Passed on final source:

- Whole-repository go build -p 4 ./... .
- Focused go vet over cmd/sparkwing, clisurface, controllerwire,
  controller/client, orchestrator and tests/acceptance.
- Full clisurface, controllerwire, doccheck and tests/acceptance suites.
- CLI tests selected by ^(TestRetry|TestTriggeredOutcome|TestSurface|
  TestCollectRunIDs|Test.*Upgrade|TestRunStatus|TestAllCommands|TestDiscovery|
  TestEveryDiscovered|TestCommandsMarkdown|TestEmbeddedReference|
  TestEveryPageExemption|TestRepoDocsNameOnlyDispatched|TestShippedProse|
  TestHonestyCheck|FuzzRetry).
- Controller/client tests selected by ^TestEngine.
- Pkg/docs embedded content/changelog/privacy, read, guide, migration, list,
  search, section, catalog and command-name checks using the prior handoff's
  Test(PkgDocsContent|EmbeddedChangelog|EmbeddedDocsCarry|EveryRecordedScrub|
  ScrubPatterns|Read_|ReadGuide|Migrations|List|Search|Sections|SectionBody|
  Guide|Catalog|DocsName) selection.
- FuzzRetryReceiptsExcludeExecutionInputs with two workers for three seconds:
  2,505 generated executions on final source. An earlier run passed 4,528.
- commentcheck -base HEAD (23 Go files), sleepcheck -base HEAD,
  W9 client API snapshot regeneration/comparison, sync-docs --check, gofmt and diff --check.

Full API snapshot regeneration also found seven unrelated baselines already
behind their unchanged public packages: pkg_controller, pkg_match,
pkg_projectconfig, pkg_storage, pkg_storage_storeurl, pkg_store and sparkwing.
Their baseline bytes were restored from HEAD to keep this worker in scope.
Only pkg_controller_client.txt changed for W9. A whole-tree snapshot refresh is
lead work; the full check is not claimed clean after restoring those baselines.

The temporary CLI-doc generator was removed too early during its first run,
causing vet to miss its source. Generation was rerun to completion before
cleanup. Final focused vet passes without temporary sources. The first vet also
found the remaining ListTriggers caller described above. No native socket,
platform, PostgreSQL or deployment evidence is claimed. The lead owns pipelines,
independent review and landing.

## Contract changes needed and remaining integration

Local W9 contract amendment: RetryRequest now requires mode=failed|all and a
key. Omitting the selection would silently lose --all-jobs. Undo is removal of
the field and CLI cutover together. No foreign contract was edited.

W1 must extend IntakeRequest or provide its selected-retry transaction input.
The final IntakeRequest has no retry selection field. It must retain mode in the
idempotency digest and persist the reuse/all selection without rewriting source
seals. W2 must consume RetryRequest.Validate and bind that transaction in
EngineController.Retry. Both adapters remain stubs in this worktree. W9's fakes
exercise the complete proposal/receipt boundary without implementing their
transactions. The mode transport is implemented, not its store behavior.

W3/W8/W9 still need here hosting, retained private bearer/resume, local detached
hosting, source acquisition, publication and read-only cache composition. Here
retry currently returns incomplete after allocation and does not retain its
bearer. Recover through a fresh request; the abandoned reservation retains its
original expiry. This is unfinished integration, not a permanent feature limit.
The existing legacy trigger intake and here-claim path remain to be replaced.

Earlier open questions remain: a sealed reason field for here intake, profile
scope enrollment, pool-reserving runner provisioning, sizing reset, run-level
annotations/member rights, local scheduler/tick operation and managed-unit/log
construction, shared Go installer environment/output binding and ReadFailure's
successful no-failure result. Remaining W9 work in older handoffs still applies.
There is no new environment variable, flag, timeout, cap or authority rule.

## Shared files and friction

Shared production deletion: internal/orchestrator/jobs_cli_remote.go loses only
its W9-only RemoteRunOutcome/getRunRetrying helper and timer.
Shared tests: internal/orchestrator/remote_run_outcome_test.go (ported/deleted),
internal/orchestrator/wingd_api_contention_test.go (one poll and its sample name),
pkg/store/default_team_callers_test.go (removed the deleted CLI caller),
tests/acceptance/retry_contract_test.go, engine_amendments_test.go and
surface_contract_test.go. Also changed CHANGELOG.md, pkg/docs mirrors/changelog,
.apidiff/pkg_controller_client.txt, the W9 wire contract document and this handoff.
Other implementation and tests are inside W9's owned paths.

Friction: cross-area runtime and selected-retry transaction contracts are still
unfinished. Socket/native integration is left to the lead. The local worktree
was reused as instructed. Git identity is Korey Gambill and core.worktree is
unset. No commit was attempted.

---

# W9 protocol schema and guide continuation

W9 remains incomplete. HEAD remains df4f991b6. The six merge-conflict contents
were already resolved at this continuation's start. They still contain no
conflict markers, and secret_local_test.go remains absent. The index still
lists the six paths as unmerged. SWARM-BRIEF prohibits worker Git writes, so
the lead must stage the resolutions and finish the merge. No Git metadata,
candidate runtime, daemon, service or release was changed. Bootstrap read the
installed sparkwing info output before the brief was loaded.

## Delivered

Advanced A1-51 by replacing the three published v1 schemas with generated v2
schemas. The generator lives in internal/doccheck and reads the final
nodeproto types. It preserves required fields, explicit zero/false, non-null
optional values, additive transport/describe fields and strict plan objects.
It also applies operation-specific selectors, exclusive reply payloads,
canonical inner errors, concurrency counts and shared retry/memo bounds.
It does not replace session ordering, duplicate-key, graph, secret, placement,
source-policy or transaction validation.

GOWORK=off go generate ./internal/doccheck regenerates the schemas.
bin/regen-all.sh includes that step. Doccheck and its tests reject changed or
missing schema files. The existing conformance schema helper gained the five
JSON Schema keywords needed for these shapes, with twelve independent
keyword cases. No node protocol or other area's contract signature changed.

Replaced the protocol guide with the amended hello, exact-engine, job/step,
flat-plan, structured-error, generated-code replay, node-host service and
Windows argv-handle contract. Its implementation note keeps native execution
unclaimed. Updated the environment inventory to identify remaining v1 consumers
and the final hello/argv transport. Generated docs/changelog mirrors agree with
the sources.

## Tests and deletions

Added TestProtocolSchemaContract in tests/acceptance with 29 cases. It covers
hello identity, named and request-target cancellation, contradictory selectors,
removed run_node, additive fields, inner/body errors, flat plans, missing versus
zero concurrency, explicit false versus null, retry and memo boundaries,
annotation ranges, removed wrappers and host-owned plan fields. These are
passing acceptance contract cases. No native T1 scenario was unskipped.

Ported TestDescribeSchemaDeclaresEveryEmittedField and
TestLogRecordSchemaDeclaresEveryEmittedField to the final nodeproto types.
TestLogRecordsMatchTheSchema now checks final SDK frames and annotation/summary
service messages, retaining refusal of line zero. The old guide transcript
check moved to doccheck as TestProtocolGuideExamplesMatchSchemas and validates
every JSON line in the new guide, including hello/body results. The old
notYetEmitted exceptions for tools and host-hashed memo inputs are removed.
No parked inventory source function is assigned directly to W9.

Removed 1,093 gross lines across the protocol guide, three old schemas and the
old schema tests, measured against HEAD. This is not net reduction. No whole
production file was deleted. Generated mirrors and earlier continuations are
excluded from this count.

## Verification

Go checks used GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4, -p 4,
GOCACHE=/tmp/sw-astra-W9/cache and GOTMPDIR/TMPDIR=/tmp/sw-astra-W9/tmp.

Passed:

- Whole-repository go build -p 4 ./... on the final source.
- Full internal/doccheck, tests/acceptance, internal/clisurface and
  internal/controllerwire suites. Native acceptance skips remain.
- internal/nodeproto/conformance -run '^TestSchemaV2Keywords$'.
- The three ported internal/orchestrator schema tests named above.
- Focused go vet over cmd/sparkwing, clisurface, controllerwire,
  controller/client, doccheck, nodeproto/conformance, orchestrator and
  tests/acceptance.
- Pkg/docs mirror/changelog, privacy, read/list/search, guide, section,
  migration and symbol checks selected by Test(PkgDocsContent|EmbeddedChangelog|
  EmbeddedDocsCarry|EveryRecordedScrub|ScrubPatterns|Read_|ReadGuide|Migrations|
  List|Search|Sections|SectionBody|Guide|Catalog|DocsName).
- Python jsonschema independently validates all three as Draft 2020-12 schemas
  and reproduces all 29 acceptance case verdicts.
- commentcheck -base HEAD (224 files), sync-docs --check, gofmt and
  git diff --check.

The initial acceptance run failed on the v1 shapes before implementation.
The full pkg/docs suite could not run its WebClient tests because httptest
cannot listen in this sandbox. The focused embedded-doc checks passed.
Root sleepcheck still fails only the previously reported W6 wall-clock
findings at internal/limits/deadline_test.go:58 and :66. No W9 sleep was added.
One build overlapped an import edit and failed to load the newly added archive;
the subsequent builds on stable source passed.

## Remaining integration and scope

No new contract change is requested in another area. Earlier contract questions
and the amendment action ledger below remain, except that the W9 v2 schema and
protocol-guide portion of A1-51 is now implemented. W3/W4/W5/W8 must finish real
v2 host/SDK fixture execution, the shared native transcript/hash corpus and
platform evidence. The existing runtime fixtures still use Old protocol types;
this continuation does not make them pass through a v1 schema or adapter.
Native Windows bridge and release packaging evidence remains open under A1-52.

Other W9 work remains: local/here/detached hosting against sealed requests,
publication and read-only cache composition, final trigger/retry intake,
controller-owned scheduling/tick, runtime/unit construction and matching-engine
Go preparation. The prior handoffs name their contract interfaces and gaps.
This continuation adds no flag, environment variable, timeout or product limit.
No new human decision is requested.

Shared files touched in this continuation: bin/regen-all.sh,
internal/doccheck/main.go, new protocolschema.go/protocolschema_test.go,
internal/nodeproto/conformance/schema.go and its new schema_v2_test.go,
internal/orchestrator/node_protocol_schema_test.go,
tests/acceptance/protocol_schema_contract_test.go, CHANGELOG.md,
pkg/docs/changelog.md and generated pkg/docs/mirror files. The orchestrator
change only migrates schema-dependent tests. No other area's production
implementation or exported contract changed. W9-owned source docs and this
handoff also changed.

Friction: Xwing context inspection cannot create its session lock in this
sandbox. The user-selected worktree was reused. The lead owns review, merge,
pipelines and native checks. Git identity remains Korey Gambill and core.worktree
is unset. Scratch is removed before the final report.

---

# W9 foreground runtime, installer masking and guide continuation

W9 remains incomplete. HEAD is still df4f991b6 with the integration merge index
left open. All six conflict contents remain resolved, with secret_local_test.go
absent. The lead must stage those paths and complete the merge because workers
may not write Git metadata. No Sparkwing binary, service or release was run.

## Delivered in this continuation

- Replaced the hidden foreground wingd handler's pflag/orchestrator daemon path
  with clisurface parsing and wingd.LocalRuntime.Run. It forwards absolute Home,
  executable, version, stay and standalone mode and waits for Run to return.
  Help, invalid input and cancellation are checked before runtime construction.
  Context cancellation after construction prevents startup. dashboardServices
  now carries that runtime beside lifecycle, managed units and logs. Native
  resolveLocalServices remains unbound. This completes the CLI adapter portion
  of A1-40/A1-41, not native runtime or managed-timer acceptance.
- Explicit budget, headroom and alternate state-db flags fail as unavailable
  because LocalStartOptions cannot carry them. Standalone reason selects its
  declared boolean mode. No new runtime option or fallback was invented.
- Cache warming now supplies the shared Install environment with a registered
  masker and streams TypeScript installer stdout/stderr through the existing
  diagnostic writer. Errors retain their cause and are masked before display.
  Diagnostic flush/writer failures prevent success receipts. A real child with
  fake npm proves filtering, masking and non-execution of pipeline code.
- Removed warming's legacy Go compiler path. That compiler resets the captured
  install environment and writes directly to os.Stderr. Go warming now calls
  the same preparation seam as the SDK installer and reports unavailable until
  the shared compiler binding exists. Component fakes still exercise successful
  Go receipts. This is an explicit unfinished binding, not a permanent limit.
- Fixed SDK installer errors whose Error() text was masked while errors.As still
  exposed raw apierr hints/details to CLI formatting. The wrapper now exposes
  a masked structured copy, keeps exact JSON numbers, preserves errors.Is and
  does not mutate the source error. The regression failed with raw credentials
  in JSON before the fix. Cache warming shares the corrected wrapper.
- Confirmed the merged W6 section reader already validates the complete machine
  file without migrations. New fleet-survey tests prove refusal of invalid
  unrelated sections, no credential echo, unchanged bytes and no created files.
  No repos reader production change was needed.
- Removed all undispatched command references reported by the docs honesty
  check, including the README. Retired the cache-service page and its sidebar
  entry, obsolete cache provisioning, inline agent-token instructions, public
  consumer controls and unredacted trigger-row claims. Architecture and native
  dashboard docs distinguish final contracts from missing native bindings.
  Versioned migration history was left intact. Other guide claims still need
  final runtime review; the command-name pass proves dispatch names only.
- Updated four schedule-command source comments and regenerated the config
  reference through bin/gen-config-docs.sh. Updated the docs link fixture after
  removing gitcache.md. Generated mirrors and changelog are synchronized.
  The environment inventory retains legacy names while real consumers remain;
  caching.md identifies that unfinished retirement instead of documenting a
  supported cache-service deployment.

These changes advance A1-10/A1-40/A1-41/A1-50/A1-51. All other W9 amendment rows
retain the decisions and open integrations in the ledger below. No cross-area
contract signature changed. No permanent capability restriction, environment
variable, flag, service or timeout was added.

## Evidence and remaining integration

Added 15 test functions: eight foreground-runtime tests, two survey tests, two
cache diagnostic tests, two native-tool warming tests and one structured-error
formatting regression. Existing cache warm and TypeScript non-execution tests
now accept an explicit diagnostic writer. No parked source function is assigned
directly to W9. No native T1 cell was unskipped. SurfaceGrammarContract now also
parses the amended foreground/home and stable tick invocations.

Passing checks use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4, -p 4 and
GOCACHE/GOTMPDIR/TMPDIR under /tmp/sw-astra-W9:

- Whole-repository go build -p 4 ./... .
- Focused vet over cmd/sparkwing, clisurface, controllerwire, controller/client,
  pkg/docs and tests/acceptance.
- Full clisurface, controllerwire and tests/acceptance suites. Native skips stay.
- Focused CLI foreground, warming, installer, dashboard, doctor, local-binary,
  survey, command-reference and documentation-honesty suites.
- Pkg/docs mirror/changelog, read/list/search/migration/catalog and privacy checks.
- Full internal/doccheck tests and config-reference regeneration.
- commentcheck, sync-docs --check, gofmt and git diff --check.

Not passing: internal/engineenv TestEveryEngineEnvNameIsClassified reports
SPARKWING_API_TOKEN and SPARKWING_JOB_ID in W6's procenv registry plus the
SPARKWING_CUSTOM acceptance fixture. This is the old classification scanner's
integration with the new policy/fixture, not permission to mark those credentials
as pass-through. Root sleepcheck retains the two W6 wall-clock findings in
internal/limits/deadline_test.go at 58 and 66. W9 adds no wall-clock waits.

Native W3/W8 source/host/lease/runtime/managed-unit construction, here resume,
controller-owned tick and detached execution, publication/cache composition,
matching-engine Go preparation and native Windows QA remain. W2 WriteJSON is
still a declaration; structured-error tests inject a JSON encoder and do not
claim native shared-encoder integration. LocalStartOptions still needs the
unrepresented flags if those controls survive. ManagedUnitManager still lacks a
schedule-install operation. The prior compiler/output/lease and successful
ReadFailure-with-no-failure contract questions remain. No human decision is
requested by this continuation.

Deleted docs/gitcache.md completely: 571 lines. Replaced another 746 gross lines
across the five architecture/lifecycle/storage sections counted below, plus
54 gross Go lines in wingd.go and cache_warm.go. These are gross removals against
HEAD, not net reduction or additional whole-file deletions. The five source doc
counts are architecture 342, local-execution 58, native-mode 73, security 114 and
self-hosting 159. Smaller guide edits and generated mirrors are excluded.

Shared files touched: pkg/pipelines/pipelines.go (four command-name comments),
pkg/docs/links_test.go (retired page fixture), tests/acceptance/surface_contract_test.go,
README.md, CHANGELOG.md, generated doc/changelog mirrors and this handoff.
No other area's production behavior changed. The foreground resolver and installer
callbacks are private composition seams against existing contracts.

Scratch is removed before the final report. Git identity remains Korey Gambill,
with core.worktree unset. The earlier handoffs below are historical progress;
their claims that procenv.Build is a panic stub and that the foreground CLI
handler is missing are superseded by this continuation.

---

# W9 machine-profile continuation

W9 remains incomplete. This continuation started with the prior session's
uncommitted work and the same merge index at `df4f991b6`. All six conflict
contents were already resolved and remain free of conflict markers. The index
still lists them as unmerged. The lead must stage the resolutions and complete
the merge because SWARM-BRIEF forbids worker Git writes. No Sparkwing executable,
service, release operation or external message ran.

## Delivered in this continuation

- Removed every cmd/sparkwing dependency on internal/profile. Connection
  selection, listing, edits, completion and profile consumers now use W6's
  userconfig.Profile and complete machine parser. namedProfile adds the map
  entry's name to that final type. It carries no backend fields or old-schema
  parser. Removed the unused profile-chain adapter.
- Replaced profile load/modify/save with userconfig.Update. Add checks collisions
  before probing and again under the file lock. Set changes only its selected
  fields against the latest file. Remove revokes the selected member credential,
  then compares the exact URL and optional token before deleting that connection.
  Concurrent profiles and other machine sections survive. A changed selected
  connection remains in the file and produces a failure.
- Auth probes now call the shared authenticated RunReader with the existing
  ten-second HTTP budget. Redirects, malformed successful pages and denied
  credentials fail. The public health probe also refuses redirects. Deleted the
  CLI-only bearer HTTP helper. No new timeout, environment variable or flag.
- Deleted the unreachable configure init handler and its three test files.
  Deleted runWorker, its trigger-only polling/reload loop and its unused sleep
  helper. The here host's existing checkout helpers remain until hosting changes.
  Removed the two dead help bindings. No removed command alias was restored.
- Rewrote the machine settings and controller-connection pages around the final
  schema. Removed backend/profile migration claims, inline runner credentials,
  removed sections and the automatic import story. Updated the migration guide,
  changelog and embedded copies.
- Migrated the profile documentation checker to userconfig.Decode. It now checks
  inline profile maps and rejects invalid settings in other sections, null,
  trailing documents, removed backend fields and credential-bearing URLs. All
  five published profile examples pass the same parser as the CLI.

These changes complete the machine-profile portion of A1-50 and the matching
profile documentation/checker portion of A1-51. The earlier amendment ledger
still applies to the remaining portions. The older remaining-work item about
W9's profile.Load/Save callers is superseded by this section.

## Tests, deletion and evidence

Added ten test functions. They cover concurrent addition/replacement during a
probe, replacement during revocation, preservation of explicit empty/zero/false
machine settings, complete-file refusal and credential-safe errors, typed probe
failures, sorted/private completion and writer failures, and the documentation
schema boundary. The new concurrency tests failed against the previous code
before the implementation changed. The new documentation boundary test also
failed against the legacy checker first.

Ported existing profile selection, project isolation/name-collision, token stdin,
revocation-kind and result-redaction tests to the final machine connection type.
The parked inventory assigns no old source function directly to W9. No new
native T1 cell was unskipped or claimed. Existing acceptance contract additions
and the complete tests/acceptance suite pass with native skips retained.

Deleted four complete configure files, 579 lines in the incoming worktree
(587 lines against HEAD). Removed another 111 old worker-loop/helper/import
lines against HEAD. These counts exclude other replaced loader/probe lines and
are gross removal counts, not net reduction claims.

All Go checks use GOWORK=off, GOFLAGS=-buildvcs=false and GOCACHE, GOTMPDIR and
TMPDIR beneath /tmp/sw-astra-W9. Tests use -p 4 at most.

Passing evidence:

- Whole-repository go build -p 4 ./... .
- go vet -p 4 on cmd/sparkwing, clisurface, controllerwire, controller/client
  and tests/acceptance. The documentation-checker package's full tests pass.
- Full clisurface, controllerwire, userconfig and tests/acceptance suites.
- Focused CLI suites for profiles, completion, probes, secrets, runner enrollment
  and observation, queue, doctor, cache warming, installer, upgrade, help and
  generated command-reference drift, with -short.
- Focused client engine/data/queue/log/review/secret/schedule-projection tests.
- Pkg/docs mirror/changelog/catalog/read/list/search/migration/scrub checks.
- All five published profile examples validate through the final machine parser.
- commentcheck, gofmt, git diff --check and sync-docs --check.

The full doccheck executable was not run over all SDK examples. Its unit suite
and the complete published profile-example scan ran. No native sockets,
services, full legacy CLI suite, Windows/Darwin or launch QA ran.

## Remaining integration and contract questions

No new cross-area contract shape changed. W3/W8 native source/host/runtime/engine
construction is still absent. Local/here/detached hosting, sealed here resume,
publication, remote-cache composition and target selection remain unfinished.
LocalStartOptions still cannot carry the registry's legacy hidden budget,
headroom or state-db controls. ManagedUnitManager still has no schedule-install
operation, and a controller-owned scheduler-pass operation is absent for __tick.
These need owner contracts rather than a second CLI-owned store or scheduler.
The shared Go compiler preparation contract and ReadFailure's successful no-failure
shape remain the earlier open questions. V2 schema/corpus regeneration and native
Windows packaging/evidence also remain. Do not claim native integration from fakes.

## Shared files and friction

Minimal shared consumer migration: internal/doccheck/profilecheck.go and its new
tests, because the old checker accepted a profile schema the CLI now refuses.
Other shared changes are CHANGELOG.md, pkg/docs/changelog.md, generated doc
mirrors and this handoff. No other-area production caller was changed. No public
client API snapshot changed in this continuation.

An exploratory internal/profile test run fails on legacy backend round-trips,
old error wording, empty SPARKWING_CONFIG and invalid placeholder controller URLs.
W9 no longer imports that package. Its remaining callers and tests belong to
other areas, so they were left intact. Root sleepcheck still reports the two
inherited W6 wall-clock checks in internal/limits/deadline_test.go, lines 58 and
66. W9 adds no sleeps. Standards matching/showing works despite the installed
Overwing staleness notice. Git identity remains Korey Gambill and core.worktree
is unset. Scratch is deleted before the final report.

---

# W9 amendment continuation

W9 remains incomplete. This session continued the merge of `bw-3095-rewrite`
onto `df4f991b6`. All six conflict contents are resolved. The index still lists
unmerged paths because SWARM-BRIEF forbids Git writes. The lead must stage the
resolutions and finish the merge. No Sparkwing executable or service ran.

## Delivered

- Kept W9 typed CLI/client behavior and amended contracts. The conflict in
  profile_flag.go was whitespace. Kept the removed sparks warmup implementation
  and obsolete secret_local_test.go deleted. Runner conflicts now use the final
  runner schema and private credential directory. Engine comments retain the
  amended named/here/launcher routing rules.
- Implemented mixed `/runners/claim`, named trigger/job selection and explicit
  launcher routing. Added CancelJob, ReadFailure, SyncLauncher and Exchange.
  Claims, reconcile and exchange use non-replay policies. Here CreateRun and
  Retry suppress transparent replay because replay cannot recover a lost bearer.
  Retry has no source runner selector, so it conservatively avoids replay for
  every source. Explicit caller replay still uses the supplied idempotency key.
- Removed the test-only error-decoder field and overrides. Every shared client
  error now uses real apierr.DecodeInner, including persisted JobFailure errors.
  Tests preserve unknown error codes, execution state, source generation,
  accepted engine, granted runner identity, deadlines and empty expansions.
  Sync rejects responses containing private bearer material.
- Implemented NewDataClient's nine signing/record operations. Added 28 shared
  snake_case data message types in controllerwire/data.go. Existing C2 routes
  are unchanged. Upload retries retain their one generated key, record/commit
  retries retain exact bytes, and explicit lookup budgets bound transport.
  VerifiedDefaultOnly, members and authorized read edges survive the adapter.
  Requests omit read sets and sealed ref evidence. Signed transfers refuse
  reusable controller bearer, cookie and proxy-authorization headers.
- Runner enrollment uses private credential files under runner.credentials_dir.
  Config/service failures revoke newly minted authority and remove only newly
  created files. Forced replacement restores the previous section/credential.
  Rollback compares the current section before replacing it, preserves concurrent
  changes and retains referenced files if restoration fails. A revocation failure
  returns the joined error and a manual revoke command. Manual argv uses start.
  Output-writer failure after successful service installation keeps enrollment.
- Added hidden __sdk-install dispatch and the stdin/result adapter. It validates
  the requested directory against the exact pin's manifest and engine version,
  filters the inherited Install environment, masks split-chunk diagnostics,
  rechecks the pin after preparation, and emits one SDKCommand JSON object.
  Native TypeScript preparation installs with npm ci and returns Node/main argv
  without preload or execution. Go is tested through the preparation seam.
  Native Go preparation remains unavailable pending the shared compiler contract.

## Contract changes needed and real integration

W3's shared pipeline compiler needs an explicit Install environment and output
writer, while retaining sparks overlays and checking their effective SDK pin.
Native Go preparation must populate missing checksums before cache hashing and
include the effective build configuration in the cache identity. W3/W8 must
retain or reacquire the executable lease through execution. The current
SDKCommand only returns argv. A duplicate CLI compiler was removed after review
because it bypassed overlays and reused artifacts across CGO settings. No shared
compiler API or other area's contract was changed here.

W8 must implement ReadPin, VersionForPin and Resolved.InstallSDK and connect the
verified engine. Assumption: Go Pin.InputsDigest covers the pin-establishing
manifest/effective replacement, rather than newly populated go.sum. TypeScript's
native lockfile remains part of the pin. Strict pre/post pin equality stays.

W2 must bind the existing /data routes to DataController/DataBinding using the
new shared messages, documented in c3-controller-wire.md. GET records selects
record/memo/depcache with lookup plus URL-escaped selector JSON. POST records
uses commit/copy_memo with exactly its matching payload. W1/W7 still own live
read/write authority, quota, receipts and physical-byte verification.

Successful ReadFailure currently expects the declared direct JobFailure object.
C1/C2 still need to specify the successful response for a terminal job with no
recorded failure. The client never synthesizes a failure from a status string.

## Amendment action ledger

Every row naming W9 was checked. Transport completion does not imply its native
host/controller/store integration is complete.

| Amendment | W9 action / remaining owner integration |
| --- | --- |
| A1-03 | Removed all decoder bypasses; real shared error decoding tested. |
| A1-08, A1-09, A1-55 | Mixed/named/launcher routes and one-time replay behavior implemented and tested. Reclaim timing remains W1/W2 authority. |
| A1-10 | Hidden installer dispatch/validation/masking and TS preparation implemented. W3 compiler binding and W8 caller/pin implementation remain. |
| A1-11 | Amended Source.Location survives private wire tests. CLI acquisition/sealing remains in the hosting cutover. |
| A1-13 | MaterializeOnly crosses reconcile unchanged with persisted execution result. W1/W3 implement awards and dispatch. |
| A1-14, A1-15 | Recovery failure read and scoped job cancel adapters implemented/tested. W1/W2 server/store behavior remains. |
| A1-16, A1-17, A1-18, A1-19 | Restricted state, current expansion jobs, source fences, attempt ordinal, engine and granted identity survive typed transport tests. W3/W8 host composition remains. |
| A1-20, A1-22 | Kept amended server authority/evidence contracts. W9 bodies do not supply actor/grants. Native provisioning and audit remain W1/W2. |
| A1-29, A1-30, A1-34 | Data signer/record transport implemented with explicit verified-default lookup and member metadata. RemoteCache composition and server binding remain. |
| A1-37 | Merged lint consumer no longer uses cache HTTP. SDK cache/key migration remains W4/W7. |
| A1-38 | Cloud payload types retained; sync/exchange adapters and immutable state tests added. W2 bootstrap and W8 pod integration remain. |
| A1-39, A1-40, A1-41 | Retained amended runner/runtime/readiness/home/stay contracts. Native constructors, foreground handler, stable tick and final runner startup remain W8/W9 composition. |
| A1-43 | Existing shared notice rendering and upgrade verifier retained. Historical assets/index/resolution are W8 and remain stubs. |
| A1-45, A1-47 | Retained merged settings/guard fixes and CLI project-reader cutover. Final host/schedule composition remains. |
| A1-49 | Final credential-file writer and failure rollback/revocation implemented/tested. W8 service dispatcher/templates still use agent rather than start. |
| A1-50 | Safe observation, typed wire and installer adapters advanced. Local/here/detached hosting, sealed resume, publication and cache opt-in remain unfinished. |
| A1-51 | W9 client API snapshot/docs/mirrors synchronized. Public command-reference drift test passes. v2 schemas/corpus and old runtime consumers still need W4/W3/W8 coordination. |
| A1-52 | No release packaging changes. Native Windows bridges and stamping remain W5/W8/W9 integration. |
| A1-53 | Added fake-transport acceptance contract tests. All native T1 cells and real-repository launch QA remain unclaimed. |

## Evidence and accounting

Added 36 test functions across the installer, data client, engine amendments,
acceptance transport and runner rollback tests. Ported runner schema/loading,
service-failure and forced-replacement checks to cleanup/revocation behavior.
The parked inventory assigns no original source function directly to W9.

Passing acceptance additions:
TestEngineAmendmentsPreservePrivateWireContract and
TestEngineAmendmentsNeverReplayOneTimeAuthority. Both use public clients with
HTTP/api.sock fake RoundTrippers. No native T1 cell was unskipped. The entire
existing tests/acceptance suite passes, including its honest native skips.

Whole-repository build, focused W9 vet, full clisurface/controllerwire and
acceptance suites pass. Focused CLI tests cover installer, runners/enrollment,
queue, doctor, warming, upgrade, help and generated references. Focused client
suites cover data, engine, logs, reviews, secrets, schedules and queue.
Documentation mirror/changelog/catalog checks and commentcheck pass.
All Go commands use GOWORK=off, GOFLAGS=-buildvcs=false and isolated
GOCACHE/GOTMPDIR/TMPDIR under /tmp/sw-astra-W9. Tests use -p 4 at most.

W9-edited tracked Go files show 172 gross removed lines versus starting HEAD,
including incoming merge adaptations. This is not a net reduction claim.
The incoming obsolete secret_local_test.go has 160 lines and stays deleted as
on W9's HEAD. Removed decoder overrides/split claim routing and the unused
eight-line firstNonBlank helper. No other whole tracked file was deleted here.

Friction: root sleepcheck reports two inherited W6 wall-clock checks in
internal/limits/deadline_test.go (58 and 66). W9's new tests add no sleeps.
Seven non-W9 public API snapshots still drift: controller, projectconfig, match,
store, storage/storeurl, storage and sparkwing. Only the W9 client snapshot was
updated. Pipelines, sockets, native Windows/Darwin, full legacy suites and release
QA were not run. Git identity remains Korey Gambill and core.worktree is unset.

Shared files touched: CHANGELOG.md, .apidiff/pkg_controller_client.txt,
pkg/docs/changelog.md and generated mirrors, tests/acceptance/engine_amendments_test.go,
design/rewrite/contracts/c3-controller-wire.md, c3-engine-version.md and this
handoff. No other-area production caller migration was necessary. No Git writes,
release operations or external messages occurred. Scratch is deleted before the
final report. The remaining W9 work in the earlier handoff below still applies.

---

# W9 continuation handoff

W9 remains incomplete. This continuation started at `d9e4ba202` with a clean
worktree. Earlier work is committed. The changes below are uncommitted for the
lead. No Sparkwing executable, service or Git metadata was changed. Scratch is
removed before the final report.

## Queue continuation

Queue commands now parse the final clisurface grammar. Bare `queue` shows help.
`queue list` reads the same authenticated `/api/v1/queue` route locally and from
profiles. It no longer uses a CLI-only HTTP fetch or a non-starting d.sock query.
Connection and read failures emit no empty success snapshot. Existing admission
rendering retains pressure, capacity, connected runs, waiter estimates and runner
headroom. Text rendering buffers the shared renderer so write errors survive.

`queue list --group` reads `/api/v1/queue/groups/{group}`. The shared
`QueueGroup` projection preserves effective capacity, including zero, used cost,
separate run/job identity, superseded holders, leases and one-based ordered
waiters. Text retains scope, identities, cost, lease times and ordering. JSON
retains the full projection. Capacity reductions may leave used cost above the
new effective capacity. The adapter does not invent a fallback capacity.

`queue priority --group` uses the existing PUT priority route. Integer zero stays
present. Relative front/back stays a relative request. One generated mutation
key and the exact body survive transport retries. The result keeps canonical run
and group, missing/found, prior/new rank and positions, participant count and
holder status. `--run` and `--set` are required in the registry, and profile flags
work before the command. Remote priority without a group returns policy_missing
before connecting because C2 has no run-priority route. W2 must supply that
contract. It is not an approved feature restriction.

Local admission priority keeps the unchanged d.sock operation. Team run numbers
resolve through the typed RunReader before the rank mutation. A failed, empty or
canceled resolution cannot mutate admission. The pre-change queue read must
succeed before setting priority. Missing local daemons produce missing-run
receipts. Existing holders keep their leases. Native d.sock integration was not
exercised in this sandbox.

The unused cluster concurrency command and its duplicated wire structs/rendering
are deleted. Scope behavior moved to the final group view. The raw queue HTTP
fetch and old pflag queue parsers are gone. Pure shared-renderer checks retain
their test-only forwarding helpers rather than shipping unused wrappers.

W9's controller-wire contract now names QueueManager, group observations and
priority writes/results. This completes already-declared C2 routes. No C2 route,
authorization alternative, other owner's Go interface or schema changed. W2 must
implement canonical-number resolution, queue/group projections and relative-rank
replay. W1/W2 must expose the required group-priority mutation through an
authorized store transaction. No such C1 mutation contract was found in the
frozen C1 contract document. W8 must provide native local-controller composition.

Assumptions: the unchanged wingwire admission snapshot is shared rather than
copied into another HTTP-only model. QueueManager supplies the typed HTTP seam
because the old raw HTTP path bypassed common authentication/error/retry policy.
The private CLI dependencies permit contract fakes while native owners are
unfinished. No public flag beyond the frozen queue selectors, environment
variable, timeout or service was introduced. The existing 5/30-second read and
10-second priority budgets remain.

## Queue deletion and test accounting

Deleted five whole files, **595 lines**:

- cmd/sparkwing/concurrency.go: 179.
- cmd/sparkwing/concurrency_scope_test.go: 15.
- cmd/sparkwing/queue_blindness_test.go: 165.
- cmd/sparkwing/queue_list_test.go: 47.
- cmd/sparkwing/queue_priority_test.go: 189.

Tracked Go deletions total **772 lines gross** against this continuation's
starting HEAD. This includes replaced parser/fetch/helper lines, not a net
reduction claim.

Added 18 tests: eleven CLI, five client, and two acceptance contract tests.
Ported integer/relative rank selection, holder lease caveat, JSON receipt facts,
missing-run failure and preflight checks. The old no-daemon versus idle regression
is replaced by a failed controller read versus an empty successful read. Bare
queue/list parity is obsolete because the final bare group shows help. Scope
checks for global names containing @, box keys and memo keys now exercise the
actual group renderer. Existing admission presentation tests still run. T1's
parked inventory assigns no original source function directly to W9.

Passing acceptance additions: TestSurfaceQueueTransport and
TestSurfaceQueueGrammar. They exercise final HTTP routes, explicit zero,
canonical IDs, profile-before-command and removed vocabulary on both local and
remote fake transports. They do not establish native process/API parity.
No whole native T1 surface scenario was unskipped or claimed complete.

## Queue verification

All Go commands used GOWORK=off, GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-astra-W9/cache and GOTMPDIR/TMPDIR=/tmp/sw-astra-W9/tmp.
Tests used -p 4 at most.

Passed:

- Whole-repository go build -p 4 ./... after the source changes.
- go vet -p 4 on cmd/sparkwing, internal/clisurface, internal/controllerwire,
  pkg/controller/client and tests/acceptance.
- Full clisurface, controllerwire and tests/acceptance suites.
  internal/acceptance compiles and has no test files.
- CLI ^TestQueue, ^TestCommandReferenceMatchesRegistry and ^TestHelp.
- Client ^TestQueue and ^TestEngineClient, including exact keyed retry bodies.
- Pkg/docs mirror, changelog, read/list/search and catalog checks.
- commentcheck -base HEAD <repo>, sleepcheck -base HEAD <repo>,
  sync-docs --check and git diff --check.
- The generated pkg_controller_client API snapshot matches this source.

Command references were regenerated through a temporary Go test overlay that
calls their existing generator. No Sparkwing binary ran. Source documentation,
migration notes, changelog and embedded mirrors are synchronized.

The full API snapshot comparison still reports drift for seven untouched
packages: pkg/controller, pkg/match, pkg/projectconfig, pkg/storage,
pkg/storage/storeurl, pkg/store and sparkwing. Only the W9 client snapshot was
updated. These packages' source and snapshots were unchanged by this continuation.
No whole-repository API-snapshot pass is claimed. Native sockets, services,
controller transactions, legacy full CLI/client suites, pipelines and launch QA
remain for owner integration and the lead.

No production file outside W9 ownership changed. Shared files touched are
.apidiff/pkg_controller_client.txt, CHANGELOG.md, pkg/docs/changelog.md,
generated pkg/docs/mirror files, tests/acceptance/queue_contract_test.go,
design/rewrite/contracts/c3-controller-wire.md and this handoff. No cross-package
caller migration was needed. Git's identity remains Korey Gambill and
core.worktree is unset. There is no new human decision request.

The remaining work below still applies. The old queue/concurrency CLI adapters
are no longer among those gaps. The prior continuation details are retained so
the next worker does not repeat doctor/cache work.

## Earlier work retained

Earlier commits supply final grammar, help/completion/discovery, local run and
trigger parsing, machine-only connection profiles, authenticated engine
transport, run observations/listing/prune/log/review adapters, statistics,
sizing reads, pools, separate team/deployment token managers, dashboard lifecycle
adapters, staged upgrade verification, typed secrets and runner observations.
Profile removal requires confirmed member-token revocation.

Schedule management uses typed `/api/v1/schedules` routes. It preserves empty/zero
overrides, resolves selections before mutations, retains controller upcoming and
history projections, and keys immediate launches. Schedule wire models live in
controllerwire. Runner credential issuance/removal uses typed TokenManager
operations, explicit runner kind, exact grants, one idempotency key and safe
receipts. Removal validates controller/name before stopping a service and
revoking its credential.

Machine update uses final clisurface grammar and retains verified installation,
identity checks, read-only previews and managed-unit refresh/failure receipts.
Dashboard and update share resolveLocalServices. Its native construction is still
missing. The old Client cron, bounce and automatic-retry reset APIs are already
deleted. Do not redo these completed pieces.

## Prior continuation: doctor and warming

Doctor now parses clisurface and inspects the existing
`wingd.LocalLifecycle.Doctor(ctx, installedVersion)` contract. The CLI never
opens a state store, repairs private-home permissions or reaps sessions during doctor. Removed the
opsview.Diagnose entrypoint from the CLI. The old opsview implementation remains
outside W9 ownership; no production caller remains in this checkout.

Reports project lifecycle findings as lowercase code/unit/message/hint fields.
`controller_checked` is true only after a complete inspection. A construction,
lifecycle, deadline or malformed-finding failure retains completed observations,
adds `controller_error`, and returns the actual error. Writer errors survive
alongside controller errors. Cancellation prevents later dependency calls.
Help, output and positive timeout validation precede dependencies. The existing
ten-second default is unchanged.

Project Go-toolchain checks, shadowed hooks, fleet-survey failures and competing
install diagnostics remain available without opening state.db. The native fleet
survey still uses the old repos/userconfig loader, which can migrate settings.
Full read-only doctor inspection therefore also needs the final machine reader
and a read-only repository-candidate projection. No CLI test here proves that
legacy loader read-only. Git subprocesses
now use the command context through runGitContext. The old repair tests and their
orphaned fixture-only test were removed. assertMode moved to its remaining
consumer's test file. Actual repair/reaper behavior is now a runtime obligation,
not established by these CLI tests.

`cache warm` now dispatches its final command. It resolves the local checkout,
requests `procenv.Install` before any pruning, optionally prunes the local
binary cache, and prepares the local entry. JSON emits one cache_warm receipt,
including actual prune facts when requested. Plain emits the prepared path.
Failures and cancellation emit no success receipt. Go returns compiled/cached,
path and key after releasing its lease. TypeScript runs npm ci for dependencies
and returns their path. Warming does not execute pipeline code.

The shared pipelineBinary function no longer downloads or uploads pipeline
executables through gitcache. Every local caller now builds from source once
and reuses its own binary cache. Describe-cache population moved to actual run
materialization so warming does not execute SDK code. The old sparks warmup
handler, case, alias variable and remote-transfer tests are deleted.

Native cache warm is not complete: W6's procenv.Build still panics. Tests inject
the final environment-builder signature. Shared install-output masking still
needs composition before native installer output is accepted as final C4
behavior. The current preparation helper writes package-manager output to
stderr; fake installers establish invocation and non-execution of pipeline code,
not secret masking or a real npm install. No environment fallback was added.

Command descriptions, generated references, guides, migration notes, changelog and embedded docs
are synchronized. Design markers identify unfinished native composition.

## Deletions and tests

Deleted whole files total **723 lines**:

- cmd/sparkwing/doctor_test.go: 320.
- cmd/sparkwing/doctor_unix_test.go: 372.
- cmd/sparkwing/fixture_isolation_test.go: 31.

Tracked Go deletions total **1,043 lines gross** against the starting HEAD.
This includes replaced fixture/parser lines and is not a net reduction claim.
The old 177-line compile_cache_test.go now retains only its cache-key check.

Added 12 doctor command tests, six warm command tests and two Unix preparation
proofs. Existing toolchain regressions remain. T1's parked inventory assigns no
original source function directly to W9.

Port/accounting:

- Old doctor positive-timeout and partial-report regressions are covered through
  runDoctorCommand. Partial findings, failure identity, writer failure, both
  cancellation boundaries, exact version, JSON field names and untouched
  private-state bytes are checked. No controller start/stop or store opener is
  supplied by the fake lifecycle.
- Existing unreadable hook-registry reporting remains covered. Its Go probe now
  uses a fake executable so real Go telemetry cannot outlive the fixture.
- Old remote binary hit/miss/upload expectations are reversed by
  TestLocalPipelineBinaryBuildsOnceWithoutRemoteTransfers. It uses the real
  local cache and a fake compiler, observes one compile then a cache hit, and
  observes zero HTTP calls even with the removed cache URL present.
- Running that regression with the previous compile.go through a Go overlay
  fails with network=2. The current implementation passes with network=0.
- TypeScript preparation invokes a fake npm with ci/--no-audit/--no-fund and
  needs no protocol bootstrap or pipeline execution.
- The legacy doctor permission/tree/reaper tests are not claimed as final native
  ports. W8 must supply those behaviors from the controller where retained.
  Existing opsview tests remain, but this continuation did not run that suite.

## Verification

Use GOWORK=off, GOFLAGS=-buildvcs=false, GOCACHE=/tmp/sw-astra-W9/cache and
GOTMPDIR/TMPDIR=/tmp/sw-astra-W9/tmp. Recreate the children before checks.
Tests use -p 4 at most.

Passed:

- Whole-repository `go build -p 4 ./...`.
- go vet -p 4 on cmd/sparkwing, internal/clisurface, internal/controllerwire,
  pkg/controller/client and tests/acceptance.
- Full clisurface, controllerwire and tests/acceptance suites.
  internal/acceptance compiles and has no test files.
- Focused CLI tests: ^TestCacheWarm, ^TestLocalPipelineBinary,
  ^TestWarmTypeScript, ^TestPipelineCacheKey, ^TestDoctor,
  ^TestPipelineToolchain, ^TestResolveSparksToolchain and
  ^TestCommandReferenceMatchesRegistry, with -short.
- Pkg/docs mirror, changelog, link and catalog checks.
- sync-docs --check, commentcheck -base HEAD, sleepcheck -base HEAD and
  git diff --check.

SurfaceGrammarContract now accepts cache warm and rejects pipeline sparks
warmup. Native T1 surface cells remain skipped. No complete native acceptance
cell is claimed. Native runtime, sockets/services, real dependency installers,
full legacy suites, pipelines and launch QA remain for the lead.

The first doctor test run failed during fixture cleanup because a real Go probe
left telemetry files in the temporary HOME. Replacing that unrelated probe with
a fake fixed the fixture. An obsolete doctor-only store-isolation fixture also
failed on missing sparkwing_meta; it was deleted with the removed repair test
path. These are not claims that the W1 store or Go telemetry changed.
The previous compiler overlay fails by design. A command-reference drift from
an extra paragraph was corrected and rechecked.

## Remaining W9 work

1. Compose W3 hosting with W6 sealed requests/settings. Publication, cache-from,
   target selection, here resume and pipeline-ref acquisition remain unfinished.
   Detached execution still starts the removed consumer entrypoint. Move it to
   the local-controller queue, then delete consumer/process helpers.
2. Compose W8 runtime, lifecycle, managed units and canonical logs. Bind
   resolveLocalServices. Finish hidden wingd arguments, local schedule
   install/pin/uninstall and __tick. Doctor's CLI lifecycle adapter is done;
   native findings and controller-owned repairs are not. Update orchestration
   is done; native refresh/restart verification remains open.
3. Integrate W2 schedule projections and W1 writes, including alias selection,
   history/upcoming, masked args, exact override presence and keyed launches.
   Local installation and pin paths still use older direct-store plumbing.
4. Compose W6's final machine schema/private credential directory with W8's
   runner service/start/refresh. Delete agentFileConfig, agentconfig and
   agentservice plumbing. Integrate W2 registration and pool reservations.
5. Replace profile.Load/Save, doctor's legacy registry load and connection
   selection with W6's machine parser and persistence. Profile scope enrollment remains a W2 gap.
6. Integrate the completed queue adapters with W2/W8 and migrate the remaining
   legacy client/management surfaces. Native cache
   warm needs W6 process policy and shared masked installer-output composition.
   Finish guides, scripts, examples and runtime tests using removed vocabulary.
7. Execute every native surface cell and launch QA after owner integration.

## Contracts and real integration points

No cross-area contract shape changed. Existing gaps remain:

- W1/W2: failed/all retry selection and authorized sizing reset that clears
  learned samples/floors while preserving explicit resource pins.
- W2: mixed trigger/job claims, profile scope enrollment, run annotations and
  ordinary-member annotation authority. C3 projections and apierr.DecodeInner
  need implementation. Secrets need encrypted/restricted persistence/enforcement.
- W2: team-admin runner registration with pool reservations. TokenCreate has no
  reservation proposal. Granted authority stays distinct from labels.
- W8: concrete runtime/unit/log construction, access-policy/install-force fields
  and effective endpoints. ManagedUnitManager has no schedule installation seam.
  A stable __tick needs a controller-owned scheduler-pass operation. Native
  Refresh must preserve schedules and restart an installed controller.
- W8: LocalLifecycle.Doctor must produce real schema/skew/timer findings without
  another writing store. Its signature has no repair or dry-run input. Exposing
  permission repair or cleanup needs a controller-owned operation contract.
- W3/W6/W8: real local/here/detached hosting and final runner startup composition.
- W6/W9: final Install environment, proxy-secret registration and shared masked
  installer-output composition for native cache warm. No replacement masking
  implementation was copied into the CLI.
- W7: persisted log cursors and final completeness.

No new human decision is requested. No public flag, environment variable, service, timeout or permanent capability
restriction was introduced. The private command dependencies are test seams,
not new runtime services.

## Shared files and friction

No production file outside W9 ownership changed. Shared changes are
tests/acceptance/surface_contract_test.go, this handoff, CHANGELOG.md,
pkg/docs/changelog.md and generated documentation mirrors. Other changes are
W9 source, tests and docs. No client API snapshot change is needed.

Friction: missing W8 construction, W6 process policy and unfinished native
acceptance remain. Standards discovery reports an older installed Overwing
binary, but matching/showing worked. The lead owns review, commits, gates and
landing. Git identity remains Korey Gambill and core.worktree is unset.
