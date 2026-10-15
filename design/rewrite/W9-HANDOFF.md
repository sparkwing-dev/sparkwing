## Continuation from f3f6b5508 - retained command tests

This section supersedes the verification counts below. W9 remains CONTINUE.
The worktree started clean. No Git writes or candidate installation ran.

### Implemented and deleted

- Ported the subprocess output tests to `profiles show --profile`, `docs read
  --full` and `docs search --search=TEXT`. The profile test uses a private real
  config file. Invalid output flags must retain exit 2 and empty stdout. The
  subprocess helper now uses the production error renderer. Removed three
  terminal cases for deleted `repos update --check` behavior.
- Replaced the production-source grep for the old jobs command with dispatch
  checks for every runs subcommand. Current help resolves the correct path.
  Retired jobs invocations return exit 2 with empty stdout. Protocol text no
  longer causes a false command-rename failure.
- Ported secret-description source drift to the existing source-entry boundary.
  It builds a real SDK pipeline, invokes the command over protocol v2 twice,
  checks renamed declarations and refuses unbuildable source without output.
  Source acquisition is a test adapter. This does not establish selected-engine
  production acquisition. Deleted its unused 113-line submit fixture scaffold.
- Reverified the previously fixed daemon missing/cancelled fixture. The orphan
  liveness source-scan test and legacy opsview doctor facade were already
  deleted in f3f6b5508 and were not recreated.

This pass changes tests only: 73 added, 179 deleted, net -106 lines. Production
Go delta is 0 this pass and **-8,111 versus main**, within the -2,900 W9 budget.
No production API was removed and no caller outside cmd/sparkwing changed.

### Verification and acceptance

All Go checks use GOWORK=off, GOFLAGS=-buildvcs=false, GOCACHE=/tmp/sw-gocache,
GOMAXPROCS=4 and TMPDIR/GOTMPDIR under /tmp/sw-astra-W9. Tests use -p 4.

- Whole-repository `go build -p 4 ./...`, scoped cmd/sparkwing and opsview vet,
  full opsview tests and commentcheck against main pass.
- The final socket-free short CLI selection has 917 passing, 15 skipped and
  two failing top-level tests. Nineteen socket-based tests were excluded from
  that diagnostic selection. No skips were added to repository tests.
- The two failures remain `TestSDKCheckRejectsFIFO` and
  `TestSDKCheckRefusesOversizedModuleWithoutWriting`, through W8's real pin
  reader. FIFO blocks until the test's existing subprocess bound. The oversized
  refusal includes 1,048,606 bytes instead of applying the retained file bound.
- The non-short migrated secret-description and command-dispatch tests pass:
  `go test -p 4 ./cmd/sparkwing -run '^Test(PipelineDescribeSecretsFollowsTheSourceAndFailsLoudly|RunCommandsDispatchThroughFinalNames)$' -count=1`.
- Output formatting and error tests pass with
  `go test -p 4 ./cmd/sparkwing -run '^TestOutputContract' -skip '^TestOutputContractPipeRoutes$' -count=1`.
  The pipe-routes test's sole failure is daemon status: the sandbox denies the
  local socket connection. That retained case stays in the test.
- Existing `go test -p 4 ./tests/acceptance -run '^(TestSurface|TestHere)' -count=1`
  components pass. No native acceptance cell is newly complete or unskipped.

### Production composition and contracts still open

The tagged production probes were rerun without replacing their production
factories. Local run, local controller and local schedules still return their
explicit integration refusals. Doctor leaves the pruned run's ledger record and
reports no unrecorded_step_outcome warning. Neither lead finding 1 nor 7 is fixed.

At f3f6b5508, NewNativeHost and NewLocalRuntime exist. The latter still requires
an Open callback. No concrete wingd.LocalController exists outside test fixtures,
and LocalLifecycle.Doctor(ctx, version) still has no dry-run/sweep input or
controller maintenance operation. The default controller authentication reader
supports persisted bearer/session authority only. It does not supply the local
peer authentication adapter. The requested constructor names/commit were asked
for while the retained tests were ported. No answer arrived in this session.

The prior foreign-owner requests still apply: W2/W8's elected store, queue,
reconciliation, maintenance and authorized API/UI composition; W2/W3's private
host state, captured environments, delegated processes, source and data/log
bindings; local schedule checkout/environment/proof/pin/tick ownership; and a
controller-owned doctor sweep carrying dry-run and independent recording
warnings. Contract fakes and W3's real native-host constructor are already
covered by the existing CLI composition tests. A substitute production factory
would not close these gaps. No foreign contract was changed.

The earlier A1-50 source-bundle, here, remote-cache, publication and debug-release
requests, plus W8's inspection compiler and upgrade pin-directory requests,
remain open as listed below. Other completed amendment/handoff work is retained.
Native platform/backend/placement evidence remains with T1/lead.

Shared files touched: none. Code/test edits are five files under cmd/sparkwing.
This report is the only documentation edit. Lead-owned handoffs and amendments
are unchanged. Friction remains the absent production adapters, W8 pin-reader
failures and sandbox socket denial. W9 scratch is removed after final checks;
/tmp/sw-gocache is kept. No new production mechanism was added.

## Continuation from db866b76a - production composition remains open

This section supersedes the status below. W9 is CONTINUE. The worktree started
clean at db866b76a. No Git writes or candidate installation were performed.

### Implemented and deleted

- Deleted the unused `opsview.Diagnose` / `RenderDoctor` facade: 1,652 production
  lines. Its obsolete protocol-lockout, standalone-store, profile reclamation,
  daemon sweep, permission and direct-store tests are removed. Those mutation
  paths have no production caller. Sessionledger and controller code are intact.
- Retained install scanning and queue tests. Ported toolchain provenance,
  install-conflict timestamps/resolved paths/remedy quoting and gate-survey
  assertions to the public doctor command. Its text output now retains those
  machine findings. These tests do not establish controller cleanup.
- Fixed migration selector exclusivity: `--version` cannot be combined with
  explicit `--from` or `--to`.
- Moved the existing Git unbinding to dispatch entry, before parsing, help and
  checkout selection. Invariant: no invocation leaves inherited repository or
  index bindings available to a child, while the gate index retains its launch
  directory and unrelated identity variables survive. A 128-case generated
  property covers the real dispatch boundary.
- Fixed `TestDaemonLifecycleMissingAndCancelledRuntime` to supply deterministic
  missing and failed runtimes. The real service resolver is not a missing
  runtime fixture. Removed the 33-line orphan
  `TestProcessLivenessRegressionDoesNotSleep`: its source file and the CLI
  `processAlive` helper were already deleted, so there is no CLI liveness
  implementation for that AST assertion to check.
- Ported retained scaffold, empty-fleet, profiles, pin suggestions, credential
  revocation, concurrency-key and public-error tests to the current grammar and
  wire. Read-only pin checks now reach `gatherSDKPinWith` and real engine readers
  instead of the deleted `repos update --check` surface.

W9-owned production Go is **+9/-2, net +7 this pass**, and **-8,111 versus main**,
within its -2,900 budget. The shared opsview cut makes this pass **-1,645 net
production lines**. Test and contract declaration files are excluded.

### Verification and acceptance

All Go checks use GOWORK=off, GOFLAGS=-buildvcs=false, GOCACHE=/tmp/sw-gocache,
GOMAXPROCS=4, and TMPDIR/GOTMPDIR under /tmp/sw-astra-W9. Tests use -p 4.

- Whole-repository `go build -p 4 ./...` passes.
- Scoped cmd/sparkwing and opsview vet passes. Commentcheck against main passes.
- Full opsview tests pass. The daemon, public doctor, local/here host adapter,
  local-service and schedule component selection passes.
- Twelve migrated command regressions pass. Eighteen focused migration,
  dispatch and checkout tests pass, including the generated Git property.
- Existing `TestSurface*` and `TestHere*` acceptance components pass. No native
  acceptance cell is newly complete or unskipped.
- The broad socket-free short selection initially recorded 907 passing,
  14 skipped and 17 failing top-level CLI tests. Its retained failures were
  triaged and ported. This is not a claim that the full CLI suite is green.
  Native socket tests remain sandbox-blocked. The CLI-subprocess output-contract
  test was ported but not rerun after the grammar edit.

### Production probes still fail; contracts and integration needed

The tagged probes were rerun with:

```
go test -p 4 -tags rewrite_integration ./cmd/sparkwing -run '^TestReview(ProductionExecutionBindings|PublicDoctorSweepsAndWarnsForPrunedRun)$' -count=1
```

All three production factories still refuse. Public doctor leaves one pruned
run's ledger entry and emits no `unrecorded_step_outcome` warning. These findings
are open, not waived. No substitute factory was added to make the probes green.

1. **W2/W8 local controller.** This HEAD has `wingd.NewLocalRuntime`, but its
   required `LocalRuntimeConfig.Open` still has no concrete `LocalController`
   implementation. `git grep 'wingd.LocalController' HEAD -- '*.go'` finds only
   W9's refusal and test factories. Bind the sole store, credentials, queue,
   reconciliation, maintenance and authorized API/UI under election.
2. **W2/W3 native host.** `NewNativeHost` exists. Its store, source engines,
   captured environments, process authority, router, logs, delegated execution,
   compute activity and owner policies are still not assembled for the CLI.
   W9's contract-tested `cliHostState` can consume the private C1-shaped
   ReadSnapshot, ReadNode, TransitionRun and TransitionNode dependencies. Their
   authenticated production transport is still absent. Public projections
   cannot replace the private rows or discard transition extras.
3. **W2/W8 schedules.** There is no local checkout/environment/proof/pin owner
   or controller tick/dry-run operation to bind. Existing schedule managers and
   native unit interfaces remain exercised with component fakes. The production
   resolver remains an explicit refusal.
4. **W2/W8 doctor.** `LocalLifecycle.Doctor(ctx, version)` still only checks
   status and units. It cannot request a dry-run sweep. Add the controller-owned
   cleanup operation with explicit dry-run input and independent recording
   warnings. W9 will invoke and render that operation. The deleted facade must
   not return as a second store owner.
5. **W8 pin reader.** Two retained tests now expose failures through the real
   engine reader. `TestSDKCheckRejectsFIFO` blocks until its existing five-second
   subprocess bound. `TestSDKCheckRefusesOversizedModuleWithoutWriting` reads an
   oversized module and includes its contents in the refusal instead of applying
   the retained 1 MiB bound. Invariant: pin inspection reads bounded regular
   files and never blocks on special files. The failure diagnostic is capped at
   200 characters. No W8 implementation or contract was changed.

The older source-bundle/publication, here/debug, inspection compiler and upgrade
pin-location handoffs below remain the continuation inventory. This pass does
not claim them closed. Lead-owned amendments and handoffs were not edited.

Shared files touched: `internal/opsview/doctor.go` and its tests. All other code
and test changes are under `cmd/sparkwing`. No API-removal caller remained
outside those files. No production owner contract was changed.

Friction: the concrete constructors named in the lead instruction are not at
this HEAD. The missing names/commit were requested while independent work
continued. Scratch /tmp/sw-astra-W9 is removed after final checks. The shared
/tmp/sw-gocache remains. Natural-writing and diagnosing-bugs skills were used.

## Adversarial review remediation from 1813bdeb9

This section supersedes conflicting status below. **Do not merge W9 yet.**
W9-owned fixes are implemented. Findings 1, 2 and 7 remain open at foreign
composition/contract boundaries. Finding 3 has its W9 contract and client fix;
W2/W1 must implement that contract in the native handler. No Git writes or
Sparkwing binaries ran. The lead retains landing, pipelines and native QA.

### Findings

| # | Status | Invariant, change and evidence |
|---|---|---|
| 1 | OPEN; DESIGN routed to W2/W3/W8 | Public execution commands must reach concrete shared owners. `TestReviewProductionExecutionBindings` reaches the actual local-run, controller and local-schedule factories without substituting them. All three fail on the unconditional refusals. The missing bindings are named below. |
| 2 | OPEN; DESIGN routed to W8 | Seal origin and current execution identity remain distinct. New tests call `Hosting.Host`, `ControllerControl.Claim` and `BootstrapHost.Run`. Initial controls pass their validators; retries fail before the next boundary. The hosting property generates independent origin/current IDs. The earlier `runInputFor` helper test is not execution-entrypoint evidence. |
| 3 | W9 FIXED; W2/W1 handler adoption required | Selection cannot delete or change unselected schedules. `ScheduleRepoRequest.mode` is required: `replace` reconciles the complete set; `upsert` changes only supplied declarations. `WriteSchedules` replaces `ReplaceSchedules` without an alias. Local registration carries the same mode. An all-elsewhere selection sends no declaration write. The client rejects unknown modes and upsert withdrawals. A stateful installed-set regression failed for both selecting one controller schedule and selecting only a local schedule, then passed. HTTP client mode/withdrawal tests pass. |
| 4 | FIXED | Routing checks, schema and help derive from the selected source. Trigger preparation captures the profile with the selected execution commit's entrypoint and uses that capture for the guard. The duplicate HEAD reader and false comment are removed. Run help captures execution configuration/identity once, selects the requested execution ref and configured/explicit code ref, then binds the source description to that identity. Engine replay cleans selected worktrees before replacing the process. Both command regressions failed before the fixes and pass; nested dirty files and malformed selected configuration are covered. |
| 5 | FIXED | Runtime construction retains configured admission policy and machine budget. The local-services constructor now calls the existing resolvers and passes policy, budget, budget origin/source and version. Invalid configuration failed the new production-constructor regression before the fix. Tests now pass for off, auto, jev, custom and classic, including custom weights and aging. |
| 6 | FIXED; minimal W6-owned edit | Git acquisition must accept its declared process kind while excluding inherited process controls and credentials. `procenv.Build` now accepts Git and enforces its existing C4 base policy. The registry includes the two declared Git CA paths. The 128-case property failed with unknown process kind before the fix and passes with Git/SSH overrides and credentials excluded. Full procenv tests pass. |
| 7 | OPEN; DESIGN routed to W8/W2 | Doctor's reported sweep must come from the public command's actual cleanup. `TestReviewPublicDoctorSweepsAndWarnsForPrunedRun` uses the command and real lifecycle/runtime with a pruned run's ledger entry. It fails because the entry remains and the warning is absent. The legacy helper test is renamed `TestSessionSweepRecordingToleratesPrunedRun`; its passing result is only recording-component evidence. |
| 8 | FIXED | Accepted output selection and origin survive intake. Parsing captures the winning format/origin once; sealing projects that capture instead of reinterpreting flag presence. Parsed command tests cover -o, --output, --sw-output and SPARKWING_LOG_FORMAT through intake and detached receipts. The three formerly dropped selections failed before the fix. The existing receipt test now parses its invocation too. |
| 9 | FIXED | A removed launcher flag must fail specifically as unknown before runtime. The test first proves otherwise-valid argv and credentials reach a nonnil factory, then asserts the exact unknown-flag error and no additional factory call. Temporarily restoring --deadline made it fail with the runtime boundary and two calls. The mutation is reverted; the corrected test passes. |
| 10 | FIXED | Environment documentation covers the authoritative policy and external reads. The inventory now unions procenv.Registry with AST-discovered reads. A new registry coverage regression failed before the docs fix. Runtime, tool, CA and deny-only rows are documented explicitly, including SPARKWING_DRY_RUN, PIP_INDEX_URL and PIP_TRUSTED_HOST. Both coverage checks and embedded-doc checks pass. |
| 11 | FIXED | Migration advice names a live intake API. The new documentation regression failed on EnqueueTriggerWithEnv. Migration and C3 guidance now use sealed CreateRunRequest through Controller.CreateRun, retaining the environment digest and options. The regression passes. |

### Contract changes needed and DESIGN routes

**DESIGN: production composition (finding 1; W2/W3/W8).** One elected controller
must own the store, queue, reconciliation, maintenance and authorized API/UI.
W2 must supply the concrete `wingd.LocalController` factory. W2/W3 must provide
private `ReadSnapshot`, `ReadNode`, `TransitionRun` and `TransitionNode` with the
C1 host signatures: public status and scheduling projections cannot reconstruct
them. W9's existing `cliHostState` can consume those implementations. Native host
assembly must also retain captured environments, source authority, delegated
process credentials, data/log storage, compute activity and joined owner policies.
Local schedule registration still needs its source/proof/pin/tick owner.
These foreign declarations/implementations are absent, so the refusal factories
were not replaced with successful component bindings.

**DESIGN: award validation (finding 2; W8 with W9 wire owner).** Validate an award
once before hosting, launcher claim projection or bootstrap exchange. Bind the
current execution ID only to the current claim fence. Bind accepted spec origin,
team and request hash to the original request seal, and verify the seals and
engine together. Do not compare either original seal ID with the current
execution ID. Pass that validated projection to all three consumers. The new
entrypoint tests must pass before treating the helper-level repair as complete.
No W8 production code or foreign contract was changed here.

**DESIGN: controller-owned doctor sweep (finding 7; W8/W2).** Add a controller-owned
maintenance operation with explicit dry-run input and a result containing sweep
outcomes plus independent recording warnings. The local lifecycle should invoke
that operation and return its findings; W9 renders them and derives failure from
cleanup, not a missing run's ledger event. The existing C4 Doctor signature takes
only a version, so it cannot convey dry-run or the required operation. W8 must
amend that contract before W9 adapts it. Retire the unused opsview doctor facade
after moving its live cleanup/reporting responsibilities. Do not open a second
writing store from the CLI to run the old facade.

**W2/W1 schedule adoption (finding 3).** C3 is W9-owned and is amended in this
worktree. The existing PUT `/api/v1/schedules/repos` carries the explicit mode;
route authority and credentials are unchanged. Apply each write atomically.
Upsert preserves unselected declarations, source pins, overrides and pause state;
empty upsert is a no-op. Local schedule registration must preserve the same scope.
The repository HTTP handler is not implemented in this worktree, so the stateful
fixture and client tests are component evidence, not native persistence evidence.

### Tests and verification

Every Go command used GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-astra-W9/gocache and TMPDIR/GOTMPDIR=/tmp/sw-astra-W9/tmp.
Tests used -p 4. The user-selected cache location overrides the brief's shared
cache default; /tmp/sw-gocache was not deleted or changed.

- Whole-repository `go build -p 4 ./...` passes after the final production edits.
- Vet passes for cmd/sparkwing, clisurface, controllerwire, controller/client,
  procenv, cluster, runners, runners/launcher and opsview.
- Full clisurface, controllerwire, controller/client, procenv and doccheck tests
  pass. Focused CLI intake/output, selected-source/help, admission, schedules,
  doctor-command and launcher suites pass. Engine-switch/replay checks pass.
  The renamed legacy recording-component test passes.
- Environment inventory, registry coverage, migration guidance and source/mirror
  equality checks pass. Commentcheck against main is clean across 2,041 files.
  Whitespace checks pass. No sleep, retry policy, customer flag or timeout added.
- Known-open integration probes are retained under the `rewrite_integration`
  build tag. They remain red, with the exact production refusal, retry validation
  and missing cleanup failures described above. Reproduce with:
  `go test -p 4 -tags rewrite_integration ./cmd/sparkwing ./internal/runners ./internal/runners/launcher -run '^TestReview'`.
  The tag keeps these explicit handoff probes separate from default component
  checks; they are not counted as passed or closed.
- No native acceptance cell was unskipped and no archived T1 port is newly
  complete. Existing component results do not establish native placement.
  The full CLI suite and Sparkwing pipelines were not run because this session
  forbids Sparkwing binaries and Git writes.

### Deletion, scope and assumptions

Removed the duplicate HEAD-based profile guard and the inline help path, with
obsolete tests of that removed reader replaced by command-level selection tests.
No production file was deleted. W9 production Go is **198 added, 83 removed,
net +115** this pass, including the new untracked helper. W9 totals **-8,128
versus main**, within the -2,900 budget. Tests, testdata and contract declaration
files are excluded. The minimal procenv edit is outside that W9 count.

The required reconciliation mode removes ambiguity that filtering cannot fix.
The output-origin capture prevents a second interpretation of the parsed flags.
The selected-help helper and pre-exec cleanup use the existing compiler and
engine switcher; reading HEAD or changing only the pin guard cannot fix source
selection. Reverting these mechanisms would restore the reproduced scenarios.
No new customer configuration, restriction, environment variable or alias was added.

Shared/foreign files touched: internal/procenv/build.go and registry.go;
internal/cluster/launch_cli_test.go; the two tagged w9_retry_review_test.go files
under internal/runners and launcher; internal/opsview/doctor_recording_unix_test.go;
C3's controller-wire contract; CHANGELOG.md and generated pkg/docs mirrors.
Lead-owned handoffs.md and amendments remain untouched. Other edits are W9-owned.
The natural-writing skill was used for prose. No harness memory was written.

Friction: native composition and the doctor operation lack the foreign contracts
listed above. Socket/native/backend QA remains with the lead. Temporary source
fixtures use a Git stand-in, not real Git writes. Scratch /tmp/sw-astra-W9 is
removed after verification. Git still resolves Korey's identity with no
core.worktree override. There are no new questions beyond these named handoffs.

---

# W9 handoff

## Continuation from 8b254e8fa

This section supersedes conflicting status below. Contract-available W9 work is
ready for lead review. Native execution still waits on the foreign integrations
listed below. The worktree started clean. No Git writes or Sparkwing executions
were performed after bootstrap discovery.

### Implementation and deletion

- `cliHostState` implements W3's `NativeHostStore`. It uses the declared controller
  operations for plan acceptance, fenced step reports, completion and renewal,
  and W3's existing reconciliation/expansion adapter. It accepts the four missing
  private operations through a subset of W3's existing host interface. No new
  route, wire type, authorization rule or successful stub was introduced.
- Renewal requires a live compute-activity callback and reads it on every beat.
  It retains cancellation reasons, watch state and lease expiry. Customer-runner
  waits must report false through that callback.
- Invariant: HTTP host writes cannot select actor authority or owner fences.
  The generated HTTP property checks exact request bodies through the real engine
  client. It varies actor material, generation, attempt and compute activity.
  Step reports retain the source ordinal. Completion retains every Applied field
  and exposes no success after a failed commit. Plan acceptance leaves Owner
  absent because C3 supplies no owner authority.
- The eight local/here, original/retry, committed/refused-completion cases now
  pass through this production adapter into NewNativeHost and the common owner.
  Private state, source acquisition and controller transactions remain fakes.
- Deleted the unused compileOptions.AfterChild callback and its unreachable
  invocation. The inspection compiler has no caller that sets this callback.
  Production Go is **97 added, 10 removed, net +87** this session.
  W9 totals **-8,243 versus main**, within the -2,900 budget. The count includes
  the new untracked production file and excludes tests and contract declarations.

### Verification

Every Go command used GOWORK=off, GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-gocache, GOMAXPROCS=4 and TMPDIR/GOTMPDIR under
/tmp/sw-astra-W9. Tests used -p 4.

- Repository go build ./... passes. Scoped CLI, clisurface, controllerwire and
  controller/client vet passes.
- TestCLIHostState* and TestCLIOwnerComposition* pass, including race. Both new
  generated properties run 128 cases. Owner, here, local, standalone, upgrade,
  SDK-install and doctor focused CLI tests pass under -short.
- Full clisurface, controllerwire, controller/client and doccheck tests pass.
  Existing TestSurface* and TestHere* acceptance components pass. No native
  placement cell was unskipped, and no archived T1 test was newly ported.
- Commentcheck against main passes. No sleep, new timeout, environment variable,
  flag or retry was added.
- The broad CLI -short run cannot complete here. Its existing
  TestResolveSparks_WithManifest_WritesOverlay opens a TCP listener and panics
  because the sandbox refuses sockets. The focused suites above avoid that test.

### Contract changes needed and real integration points

1. **W2/W3 private execution state.** Supply authenticated implementations of
   ReadSnapshot, ReadNode, TransitionRun and TransitionNode with the exact C1
   host signatures. C3's scheduling projection lacks the complete private rows.
   The job-transition body cannot carry every owner-policy TransitionExtra.
   Do not reconstruct either from public status or discard fields. The adapter
   can now consume these implementations without replacing the shared hosts.
2. **W2/W3 native assembly.** Bind the elected wingd.LocalController factory,
   store/queue/reaper/reconciliation, captured environments, audited source
   access, per-process delegation, bytes/records/logs, current compute activity,
   owner policies and state following. Bind W2's typed step handler to the
   existing StepReportRequest/StepRecorded contract. Local/here resolvers remain
   explicit refusals until these bindings exist.
3. **W8 upgrade pin location.** The prior real-resolver probe remains applicable.
   UpgradeRequest has Checkout but no PipelineDir, and Upgrade reads pins from
   Checkout. Preserve the repository root for staging and verification, and
   carry the relative pipeline directory through pin reads and edit collection.
   W9 can fill that field once W8 declares it. No foreign contract was changed.
4. **W8 inspection compiler cut.** cmd/sparkwing/compile.go still serves plan,
   named describe and run help. Its Go/TypeScript build paths duplicate the
   selected-engine SDK installer. A hard cut needs SDKInstallRequest to retain
   the existing no-update/no-binary-cache controls and sparks resolution.
   The request has no such controls, and sdkinstall builds with go.mod rather
   than the sparks resolver's overlay. W8 must declare the retained install
   inputs and implementation before W9 can delete this path and port its tests.
   Native toolchain/process-tree and local-cache behavior remain required ports.
5. Here reasons, working-tree bundles, result/log publication, local schedule
   source/environment/proof/pin/tick assembly and debug release retain their
   previously recorded foreign-contract gaps. Publication failures must preserve
   the local verdict. Native platform/backend/placement QA remains with T1/lead.

### Scope and assumptions

Only W9-owned files changed. No foreign callers were touched. Lead-owned
handoffs.md and amendments remain unchanged. C3 documents the adapter boundary.
The natural-writing skill was used for this report.

The new privateHostState interface is only the four-method subset already
required by W3's NativeHostStore. It exists because the public scheduling and
job-transition transports cannot implement that subset. A smaller adapter that
uses public projections would lose state or authority. Remove this seam once
the complete shared remote store supplies NativeHostStore directly. No new
customer capability or security policy was chosen.

Scratch /tmp/sw-astra-W9 was removed after checks. The shared
/tmp/sw-gocache is retained. Git still resolves Korey's configured identity and
has no core.worktree override.

## Continuation from 261ae6289

This section supersedes conflicting status below. W9 remains incomplete because
the elected controller factory, private host-state transport and native placements
are open. The worktree started clean. The lead owns commits, review and pipelines.

### Implementation and deletion

- Invariant: a successful selected-engine description retains the live SDK's
  argument metadata, risks, extra-argument capability and secret classifications.
  Named describe and `--secrets` now share one live protocol read. Acquisition,
  invalid bindings, missing registrations and protocol errors fail without a
  successful output. Catalog reads remain available to listings and completion.
- `Pipeline` embeds the shared `nodeproto.PipelineDescription` instead of copying
  its fields. This also retains `Extra`, which the copied CLI type dropped.
  Project aliases, profile pins and trigger metadata remain separate.
- Invariant: parent application manifests cannot select or obscure the pipeline's
  SDK pin. Toolchain selection and `info` now pass the pipeline directory to the
  real engine reader, matching source acquisition.
- Retired W9's last production scaffold filenames. `internal/clisurface/contract.go`
  moves to `grammar.go`, and `internal/controllerwire/contracts.go` moves to
  `engine.go`. Their non-comment code is unchanged. GoDoc is shortened to one
  line. No declaration, method, route or wire field was removed.
- Physical production Go: **578 added, 639 removed, net -61**. The two removed
  scaffold files contain 568 lines, replaced by 529 live lines. The budget excludes
  the old filenames but counts their replacements, so this continuation's measured
  budget delta is **+507**. W9 totals **-8,330 versus main**, within -2,900.
  Counts exclude tests, testdata, acceptance and remaining contract declarations.
  The previously committed legacy host/client cuts remain complete.

### Verification and acceptance

Every Go command used GOWORK=off, GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-gocache and TMPDIR/GOTMPDIR under /tmp/sw-astra-W9. Tests use -p 4.

- Whole-repository `go build -p 4 ./...` and scoped CLI, clisurface,
  controllerwire and controller/client vet pass. Their focused CLI suite and the
  full clisurface, controllerwire, controller/client and doccheck suites pass.
- Cold/stale successful-description regressions failed before the fix. The live
  schema property passes 64 generated cases over actual protocol pipes with fake
  source acquisition. It covers alias/entrypoint isolation and `--secrets`.
  Source, binding, registration and protocol failure cases pass.
- The trigger component passes through the actual describe command, protocol
  decoder, CLI JSON, trigger decoder and intake-argument preparation. Required
  pipeline/job metadata, extra arguments, risks and secret classification survive.
  Engine verification/acquisition remains a fake boundary.
- Real Go and TypeScript manifest/lockfile properties each pass 64 cases for
  missing, malformed and differently pinned parent manifests. The selection and
  info regressions both failed before their caller fixes. Engine replay and
  no-fallback tests pass. The new properties and retained owner components pass
  under race.
- Existing `TestSurface*` and `TestHere*` acceptance components pass. Native
  scenario cells remain skipped. No native cell or archived T1 test port is newly
  complete. Doctor's pruned-run regression still passes.
- Commentcheck against main, embedded documentation equality and whitespace
  checks pass. No sleeps or test waiting loops were added.

### Contract changes needed and integration left open

**W8 upgrade pin location:** a temporary overlay probe against the real
`engine.Resolver.Upgrade` fails reading `<repository>/go.mod` when the valid pin
is in `<repository>/.sparkwing/go.mod`. W9 passes the repository root because
W8 stages the checkout and W9's verifier needs its `.sparkwing` and project files.
W8 calls `ReadPin(request.Checkout)` and has no pipeline-directory field. Propose
retaining the repository root and carrying a relative pipeline directory through
pin reads, staging and edit collection, as SDKInstallRequest already does. W9 can
then fill that selector. Passing only `.sparkwing` would break the verifier's
checkout contract. The probe is failing evidence, not a passing native upgrade.
No foreign contract was changed to bypass it.

The prior open W2/W3 integrations remain: the elected LocalController factory,
owned store/queue/reconciliation/maintenance, private authorized snapshot and
host-write transport, typed step-report handler, captured environments, source
authority, delegated processes, storage/log bindings and joined owner policies.
Here reasons, source bundles, schedules/pins/ticks, result/log publication and
debug release retain their recorded contract gaps. Publication failures must
preserve local verdicts. Native backend/platform/placement QA remains open.
Lead decisions 07:40 items 1 and 6, doctor and Windows packaging were already
implemented and were not rewritten.

### Shared files, assumptions and friction

All production and test code changes are W9-owned. C4's source links follow
`clisurface/grammar.go`. C3's wire/engine notes, docs/cli.md, CHANGELOG.md and their
generated pkg/docs mirrors are updated. No foreign callers changed. Lead-owned
handoffs.md and amendments remain untouched. Their historical pointers to
controllerwire/contracts.go need a lead-owned update to controllerwire/engine.go.

The command's source callback uses the existing withPipelineEntry implementation
and permits a fake acquisition boundary in tests. It adds no flag, environment
variable, timeout, retry policy or dependency. The shared description replaces a
duplicate metadata type. There is no parallel protocol or compatibility path.

An early broad test regex included the old slow describe subprocess fixture.
It built and ran its isolated CLI and failed at the incorrect parent pin path.
The real-reader properties above close that caller bug. The fixture's local SDK
replacement still needs migration to verified-engine composition before it can
supply native evidence. Later focused commands use -short. No installed CLI,
real home/store, service, deployment or Git metadata was changed. Git resolves
Korey's identity without a core.worktree override. Scratch is removed at shutdown,
and /tmp/sw-gocache is retained.

## Continuation from 771a21140

This section supersedes conflicting status below. W9 remains incomplete because
native controller construction, private execution-state transport and placement
acceptance remain open. The worktree started clean. The lead owns commits,
pipelines and native QA.

### Implementation and deletion

- Invariant: host binding cannot replace the award's owner kind, run, holder or
  generation. The shared owner projection now constructs that trigger fence.
  C1's SQLite snapshot confirms its Name is empty. The binder supplies only the
  resolved authority reference and capability prefix on the actor.
- Local, standalone and here hosting use one session lifecycle for authority
  deadlines, binding, execution and exactly-once cleanup. It builds host input
  from the verified projection and copies storage, engine resolver and plan cache
  from the session. It preserves context cancellation causes through cleanup.
- Removed the duplicated local/here session lifecycle: production Go versus HEAD
  is **66 added, 65 removed, net +1**. W9 versus main is **-8,837**, within the
  -2,900 budget. Counts include untracked owner_host.go and exclude tests,
  testdata, acceptance and contract declaration paths. No legacy production file
  was deleted in this continuation. The committed host/client cuts and
  W9-CLIENT-CUT-FILES.md remain applicable.

### Verification and acceptance

Every Go command used GOWORK=off, GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-gocache and TMPDIR/GOTMPDIR=/tmp/sw-astra-W9. Tests used -p 4.

- Whole-repository `go build -p 4 ./...` passes. Scoped CLI, clisurface,
  controllerwire and controller/client vet passes.
- Full clisurface, controllerwire, controller/client and doccheck tests pass.
  Focused CLI suites pass with `-run
  '^(TestOwner|TestCLIOwner|TestHere|TestLocal|TestRunLocal|TestStandalone)'`.
- `TestOwnerBindingsCannotReplaceOwnerFenceProperty` failed before the fix and
  passes 128 generated cases for both local and here hosting. It also verifies
  that the binder's resolved authority reference and capability prefix survive.
  The retained runner/seal properties pass. The deadline property now verifies
  preservation of a custom authority-loss cause instead of only context.Canceled.
- `TestOwnerInputMatchesPersistedTriggerFence` uses a current-schema SQLite store
  and its real ReadSnapshot projection. Its fixture seeds only team/run/trigger
  rows. It verifies projection shape, not authenticated persistence writes.
- `TestCLIOwnerCompositionPreservesRetrySeals` now constructs W3's real
  NewNativeHost, ProcessAuthorities and W2 PolicyRouter. Its eight local/here,
  initial/retry and completion-write combinations still pass with a binder that
  proposes a foreign actor. Store, source acquisition and engine/authority
  boundaries remain fakes. Unexpected engine/process/output calls fail. The
  accepted plan is empty, so this proves construction and owner completion,
  not SDK hello or job execution.
- Owner/here/local-run/standalone tests pass under -race. `TestSurface` and the
  existing surface/here contract tests pass with
  `go test -p 4 ./tests/acceptance -run '^(TestSurface|TestHere)' -count=1`.
  Native scenario cells remain skipped. No archived T1 port is newly completed.
- Commentcheck against main is clean across 2,030 files. Embedded documentation
  equality and whitespace checks pass. This continuation adds no sleeps or waits.
  Full CLI tests remain outside the socket-restricted sandbox's evidence.

### Contracts and integration left open

The earlier amendment/handoff audit remains applicable. Lead decisions 07:40
items 1 and 6, the doctor fix and Windows packaging were already committed and
were not reimplemented. Lead decisions 09:40 add no W9-owned harness migration.
No foreign interface or wire shape changed in this continuation.

W2 still needs the elected wingd.LocalController factory, owned state/queue,
reconciliation, leased maintenance and authenticated API/dashboard bindings.
W3 RemoteOwnerStore still cannot satisfy NativeHostStore: private snapshots,
acceptance, transitions, finish, step persistence and renewal need an authorized
transport. The C3 Controller has no private execution snapshot operation.
W2's step decoder still uses step_id/status and an empty 204 rather than the
contracted StepReportRequest and StepRecorded acknowledgement. These are carried
contract/integration requests, not substitute public projections.

Native local/here composition still needs captured-environment retention/delivery,
source access, process delegation, data/log bindings and joined owner policies.
The native Git process builder still rejects procenv.Git. Numeric GitHub identity,
source-bundle intake, persisted here reasons, schedules/pins/ticks, result/log
publication and debug release retain their previous gaps. Publication failure
must preserve the completed local verdict. Windows runtime and native backend,
platform and placement acceptance remain with their owners and the lead.

### Shared files, assumptions and friction

Production/test edits are all under cmd/sparkwing. No foreign caller migrated
and no API was removed in this continuation. C3's W9 wire notes, docs/api.md,
CHANGELOG.md and their generated pkg/docs mirrors are updated. Lead-owned
handoffs.md and amendments remain unchanged.

The shared session function replaces two used lifecycles. It adds no interface,
flag, environment variable, timeout value, retry policy or dependency. Inlining
it would restore the duplication. C1's real projection required correcting the
older fake's trigger Owner.Name. W2 engineActor still sets that Name to UnitID;
future private adapters must preserve C1's canonical host projection rather than
copying that actor wholesale. This continuation makes the CLI projection canonical
without changing W2's store-write actor.

Git resolves Korey's identity without a core.worktree override. Session scratch
is removed at shutdown. The shared build cache is retained.

## Continuation from d526ef30b

This section supersedes conflicting status below. W9 remains incomplete because
native controller construction and private execution-state transport are open.
The worktree started clean. The lead retains commits, pipelines and native QA.

### Implementation and deletion

- Local and here handoff now share one owner-award projection. It validates the
  award and seals before binding, copies granted labels and serializes the inner
  `runspec.Request` required by C3 into `RunInput.SealedRequest`. The separate
  `RequestHash` retains the enclosing first seal's canonical hash.
- A retry's current execution ID stays separate from its original seal ID.
  Local award validation no longer applies the initial-intake receipt equality
  rule to retries. Initial intake receipts retain that equality check.
- Migrated W8's `runInputFor` with three changed lines. It passes the inner
  request and compares acceptance against the source seal ID. It permits the
  awarded execution to have a different ID. Its regression failed before this
  change with `runner award is missing its sealed request`.
- Removed the duplicated local/here seal projection. W9 production Go versus
  HEAD is **52 added, 64 removed, net -12**, including the new untracked file.
  W8's shared migration is **3 added, 3 removed, net zero**. Total production
  change is **55 added, 67 removed, net -12**. W9 versus main is **-8,838**,
  within the -2,900 budget. These counts exclude tests, testdata, acceptance and
  contract declaration paths. No legacy production file was deleted this session.
  The prior host/client cuts and W9-CLIENT-CUT-FILES.md remain applicable.

### Verification and acceptance

Every Go command used GOWORK=off, GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-gocache and TMPDIR/GOTMPDIR=/tmp/sw-astra-W9. Tests used -p 4.

- Whole-repository `go build -p 4 ./...` and scoped CLI/runner vet pass.
- `TestCLIOwnerCompositionPreservesRetrySeals` runs the CLI award paths through
  W3's real OwnerHost, lease keeper, scheduling projection and decider. Eight
  combinations cover local/here, initial/retry identity and successful/refused
  completion persistence. The private store and source acquisition are fakes.
  Every case failed before the projection fix. All pass, including under race.
  A failed completion write returns no successful completion. Cleanup runs once.
- Invariant: malformed seals and mismatched award bindings cannot reach host
  binding. `TestOwnerAwardMismatchCannotReachBindingProperty` passes 256 generated
  cases across ten mutations, including changed request bytes, accepted seals,
  plan, run/job binding, generation and missing lease metadata. It and the
  retained deadline/runner-label properties pass under race.
- The focused CLI local, standalone, here and owner suite passes. Full runners,
  clisurface, controllerwire, controller/client and doccheck suites pass.
  The runner retry regression and retained input fuzz seeds pass.
- `TestSurface` passes its existing SQLite schedule cell. Its native scenarios
  remain skipped. No new native acceptance cell or archived T1 port is claimed.
- Commentcheck against main is clean across 2,029 files. Whitespace and embedded
  documentation equality checks pass. No sleeps or test waiting loops were added.
  The known sandbox socket restriction still limits full CLI/native testing.

### Contracts, integration and shared files

C3 already specifies inner-request serialization and immutable retry seals.
No foreign interface or wire message changed. The real-host component replaces
only the host fake at this boundary. It does not establish SDK hello, verified
engine/source acquisition, a real private store transport or native placement.
The shared helper replaces duplicate projections and adds no setting, flag,
environment variable, timeout, retry policy or interface.

The earlier assignment audit remains applicable. Lead decisions 07:40 items 1
and 6, the doctor handoff and Windows packaging were already committed.
W2 still needs to supply the elected wingd.LocalController factory and its store,
queue, reconciliation, maintenance and authenticated API/dashboard bindings.
W3's NativeHostStore still has no remote private snapshot/acceptance/transition/
completion/renewal adapter. Public scheduling snapshots cannot substitute for it.
W2's step handler still uses step_id/status rather than the contracted typed
report and acknowledgement. These are existing foreign integration requests.
Captured-environment delivery, delegated process services, authorized data/log
bindings, owner policies, source-bundle intake, here reasons, schedules/pins/ticks,
publication copying and debug release retain their recorded gaps. Publication
failure must preserve the completed local verdict. Windows bridge runtime and
native platform/backend/placement evidence still require the lead and CI.

Shared code/test edits are internal/runners/execution.go and run_input_test.go.
Other code and tests are W9-owned. The W9 C3 wire notes, docs/api.md, CHANGELOG.md
and their generated pkg/docs mirrors are updated. Lead-owned handoffs and
amendments are unchanged. Friction: the previous fake hosts accepted a seal
encoding that the real W3 host refuses. The new component exercises that actual
boundary. Git still resolves Korey's identity with no core.worktree override.
Session scratch is removed at shutdown. The shared build cache is retained.

## Continuation from efb4f6c61

This section supersedes conflicting status below. The worktree started clean.
W9 remains incomplete. Native factories, private execution-state transport and
placement acceptance remain open. No Git write or Sparkwing executable ran.
Lead-owned handoffs and amendments are unchanged.

### Changes and invariants

- Invariant: host binding and execution cannot outlive the caller or the awarded
  binding, owner or parent-capability deadline. ClaimedUnit.HostContext owns the
  shared context bound. Local and here hosting apply it before binding. W8's
  Hosting uses it in place of its duplicate selection block. An expired award
  stops before binding. Cancellation during binding still closes returned resources.
  Renewable lease expiry stays with the lease keeper and store clock.
- Invariant: binding cannot change the runner identity delivered to the host.
  Local and here input preparation copy the granted labels before calling the
  binder. Both host paths use that retained input. The generated regression
  reproduces the shared-slice mutation before the fix.
- Renamed runspec/step_windows_test.go to step_window_test.go. Go had treated the
  former filename as Windows-only, so earlier Linux runs did not execute it.
  Once enabled, it failed compilation because the literals initialized promoted
  fields directly. The test now initializes Optional explicitly. Its 256
  generated seal/acceptance/retry cases pass on Linux. Earlier reports' Linux
  sealed-window test claims are superseded by this executed result.
- Source docs, the C3 contract report, changelog and generated embedded docs now
  describe the deadline and identity boundaries. No wire shape changed.

Production Go versus HEAD: **34 added, 15 removed, net +19**. W9-owned paths
account for **+28**. The shared W8 caller migration is net -9. W9 versus main
is **-8,830**, within the -2,900 budget. Tests, testdata, acceptance and contract
file paths are excluded. No legacy production file was deleted this session.
The committed host/raw-client cuts and W9-CLIENT-CUT-FILES.md remain applicable.

### Verification

All Go commands use GOWORK=off, GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-gocache and TMPDIR/GOTMPDIR=/tmp/sw-astra-W9. Tests use -p 4.

- Whole-repository go build -p 4 ./... passes. Vet passes for CLI, clisurface,
  controllerwire, controller/client, runners and runspec.
- Full clisurface, controllerwire, controller/client, runners, runspec and
  doccheck suites pass. Focused CLI local/standalone/here/trigger/owner tests,
  release-workflow checks and environment-inventory checks pass.
- TestOwnerHostsCannotOutliveAwardProperty and
  TestOwnerBindingsCannotMutateGrantedRunnerProperty each pass 128 generated
  combinations for both local and here hosting, including under -race.
  TestHostContextLeavesRenewableLeaseLivenessToKeeper passes.
- TestStepWindowsRemainSealedAcrossAcceptanceAndRetry actually runs and passes.
  Doctor's pruned-run regression, TestSurface components and TypeScript bridge
  packaging workflow/publication checks pass. Native Windows execution remains CI
  evidence. No native acceptance cell or archived T1 port is newly completed.
- Full cmd/sparkwing testing cannot finish in this sandbox. The existing
  TestResolveSparks_WithManifest_WritesOverlay panics when httptest.NewServer
  attempts a forbidden socket. The focused command suite above runs without it.
- Commentcheck against main passes. Sleepcheck against main still reports the
  unchanged tests/acceptance/session_flow_http_test.go:102 wall-clock comparison.
  This session adds no sleep or waiting loop. Formatting, whitespace and embedded
  documentation equality checks pass.

### Remaining integration and contract requests

The assignment audit below still applies. Lead decisions 07:40 items 1 and 6,
Windows packaging and doctor are implemented. The portable step-window coverage
correction above is new. No foreign contract change is requested this session.

W2 still needs the elected wingd.LocalController factory and its owned store,
queue, reconciliation, maintenance and authenticated HTTP/dashboard bindings.
W3 NewNativeHost exists, but RemoteOwnerStore does not implement NativeHostStore:
private RunSnapshot, acceptance, transitions, completion, steps and renewal need
an authorized transport. W2's step decoder still uses step_id/status and an empty
204 instead of StepReportRequest and StepRecorded. Public scheduling/detail
projections cannot replace the private execution snapshots.

Native local/here construction also needs captured-environment delivery,
process delegation, authorized data/log bindings and joined owner policies.
Numeric GitHub identity/source authority, source-bundle intake, a sealed here
reason, schedules/pins/ticks, publication result-copy and debug release remain
open. Publication failure must preserve the completed local verdict. Native
HTTP/api.sock, Windows/Darwin/backend/placement evidence and real-repository QA
remain with the lead and area owners. No unused factory or alternate authority
transport was added to hide these gaps.

### Shared files and friction

Shared implementation change: internal/runners/hosting.go only, replacing its
inline deadline selection with the W9 wire helper. Shared test change:
internal/runspec/step_windows_test.go is replaced by step_window_test.go with the
same property and valid embedded-field literals. Other implementation/tests are
W9-owned. C3, source docs, CHANGELOG.md and generated pkg/docs copies are updated.

Both new CLI properties failed before their production fixes. Enabling the
step-window test exposed a separate compile error, then passed after correction.
The socket denial and existing sleepcheck finding remain visible. The new helper
extracts W8's existing award deadline rule because the three callers must share
it. It adds no timeout value, limit, flag, environment variable or retry policy.
Git still resolves Korey's identity without a core.worktree override. Scratch
is removed at shutdown. The shared build cache is retained.


## Continuation from fad836987

This section supersedes earlier status. The worktree started clean. W9 remains
incomplete because the native factories, private remote execution-state transport
and placement acceptance are unfinished. No Git write or Sparkwing executable ran.
Lead-owned handoffs and amendments are unchanged.

### Changes

- Trigger preparation now chooses Go versus TypeScript from package.json at the
  selected pipeline commit, before source acquisition and engine resolution. The
  execution commit and dirty working tree cannot select another SDK. A malformed
  selected manifest fails. The shared pipelineentry classifier accepts bytes so
  the CLI does not duplicate its dependency rules or materialize another checkout.
- Here hosting closes a partially returned session even when the binder fails.
  Cleanup happens exactly once before return, and cleanup errors stay joined with
  binding/execution errors under incomplete-handoff exit 3.
- Removed 17 stale environment-inventory rows and two retired-host annotations
  from the environment-read checker. Removed the unused plumbing category.
  Deployment/artifact guides now describe the local blob store and explicit
  read-only remote-cache profile. The guide checker refuses the three removed
  cache transport variables. Migration history and protocol deny lists remain.
- Source docs and the changelog describe SDK selection and partial cleanup.
  Embedded docs were regenerated with bin/sync-docs.sh.

Production Go versus HEAD: **45 added, 19 removed, net +26**. W9-owned paths
account for **+14**. Shared pipelineentry is +12 and doccheck is net zero.
W9 versus main is **-8,858**, within the -2,900 budget. Tests, testdata,
acceptance and contract declaration paths are excluded. No legacy production
file was deleted in this continuation. The committed host/raw-client cuts and
[their coverage obligations](W9-CLIENT-CUT-FILES.md) remain applicable.

### Verification

All Go commands use GOWORK=off, GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-gocache and TMPDIR/GOTMPDIR=/tmp/sw-astra-W9. Tests use -p 4.

- Whole-repository build passes. Scoped vet passes for CLI, pipelineentry and
  doccheck. Complete pipelineentry, doccheck, clisurface, controllerwire,
  runspec and controller/client suites pass.
- Focused local/standalone/here/source/selected-engine CLI tests pass. New native
  Git regressions cover selected pipeline versus execution commits, absent,
  regular and dev SDK dependencies, malformed manifests and dirty checkouts.
  FuzzTriggerSDKIgnoresWorkingTree passes 99 generated executions with four
  workers and three requested seconds. The existing Git-environment contract
  fake is explicit. Engine resolution/acquisition remains a foreign boundary.
- The new here cleanup property passes 128 generated combinations of binding,
  host, cancellation, missing-host and cleanup outcomes. It reproduced missing
  cleanup before the fix. Here identity and credential properties still pass.
- TestSurface components, sealed-window tests, doctor-pruned-run regression and
  TypeScript native-bridge assembly/publication checks pass. Native Windows
  execution and publication remain lead/CI evidence. No native cell was unskipped
  and no archived T1 source-function port is newly claimed complete.
- The environment-read inventory check failed on the stale entries before their
  removal and passes afterwards. Its AST-walker and command-reference checks
  pass. Embedded-doc and whitespace checks pass. Commentcheck against main passes.
- Sleepcheck against main fails at the unchanged
  tests/acceptance/session_flow_http_test.go:102 wall-clock deadline comparison.
  This continuation adds no sleeps or waits. The lead owns that cross-area gate.

### Remaining contracts and native integration

The prior assignment audit applies to every W9 amendment/handoff. Lead decisions
07:40 items 1 and 6, the doctor handoff and Windows packaging remain implemented.
No foreign contract was changed or new user restriction introduced.

W2 must supply the elected wingd.LocalController factory with the owned store,
queue, reconciliation, maintenance and authorized HTTP/dashboard handlers.
W3's NewNativeHost exists, but RemoteOwnerStore still lacks NativeHostStore's
private RunSnapshot, acceptance, transitions, completion, step persistence and
renewal. A scheduling/public projection cannot substitute for those snapshots.
W2's step decoder still accepts step_id/status and an empty 204 rather than the
existing typed StepReportRequest and StepRecorded acknowledgement. The earlier
real-controller probe remains failing evidence, not a passing integration.

Local/here composition also needs private captured-environment delivery, process
delegation, authorized data/log bindings and joined owner policies. On-demand
startup must leave a surviving process when the CLI returns. Native GitHub
numeric-ID/source authority, pre-intake source bundles, persisted here reason,
schedule checkout/proof/pin/tick, publication result-copy and debug release
remain open. A publication failure must preserve the completed local verdict.
No unused factory wrapper or alternative authority transport was added.

### Shared files and friction

Shared production edits are internal/pipelineentry/pipelineentry.go (one byte
classifier, retained directory API) and internal/doccheck/bannedtokens.go (one
pattern). Their callers keep their existing API. CLI caller migration is
cmd/sparkwing/trigger_native.go. Other code/tests are W9-owned. Docs,
CHANGELOG.md and their generated pkg/docs mirrors are updated.

The new SDK regression first failed to compile because the selected-commit
selector did not exist. The cleanup property failed before the defer moved.
The environment inventory failed on stale deleted-host facts. All three causes
were fixed in place. The unrelated sleepcheck failure remains visible.
No new flag, environment variable, dependency, cache or retry mechanism was added.
Git resolves Korey's identity with no core.worktree override. Session scratch
is removed before the final report. The shared Go cache is retained.


## Continuation from caa62edf0

This section supersedes conflicting status below. The worktree started clean.
W9 remains incomplete because native controller/host integration and acceptance
ports remain open. The lead owns commits, pipelines, review, native QA and release.
No Git write or Sparkwing executable ran. Lead-owned handoffs and amendments are
unchanged.

### Raw client cut

Deleted `Client`, `New`, `NewWithToken` and all raw trigger, claim, state,
execution-secret, output, OIDC, concurrency-slot, metric/profile, live-log and
log-segment transports. Their separate error, token-death, capability-probe,
runner-identity and retry machinery is gone. The shared engine transport retains
its existing retry behavior with private helpers. No compatibility API or fallback
was added.

There was no production caller outside the client package. Production callers
already use `EngineOptions` and the final constructors. The nodehost event suite
retains its store-only branch and drops the old controller-client branch.
Removed raw-client fixture branches and orphan fixture files from controller tests.
The obsolete reflection check against the removed type is gone. The retained
unknown-route/method tests use HTTP recorders. The final run-reader example now
uses an explicit positive limit, the complete shared page schema and a fake
transport. The full client suite, including that example, passes.

[W9-CLIENT-CUT-FILES.md](W9-CLIENT-CUT-FILES.md) lists every deleted production
file and all **209 removed test entrypoints**, with their coverage obligations.
Those fixtures used retired routes, token tables, request bodies or host adapters.
They are removals, not completed ports. Their source identities remain available
at the base revision. Final capability-backed OIDC subject, audience, trust,
expiry, rotation and cross-team assertions particularly still need W2's ports.

Production Go delta versus HEAD is **4 added, 2,877 removed, net -2,873**.
All of that production change belongs to W9. W9 is **-8,872 versus main**, within
the -2,900 budget. Tests, testdata, acceptance and contract declaration paths are
excluded by the lead's budget convention.

### Verification and acceptance

All Go commands used GOWORK=off, GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-gocache and TMPDIR/GOTMPDIR under /tmp/sw-astra-W9.
Builds, vet and tests used -p 4 at most.

- Whole-repository build passes after the raw client deletion.
- Scoped vet passes for client, controller, nodehost, CLI, API generation,
  runners, launcher and acceptance.
- The full client, clisurface, controllerwire, runspec and doccheck suites pass.
- TestSurface components pass. No native cell was unskipped.
- Final controller cancellation/membership, job lifecycle/renewal/replay and
  owner reconciliation/completion tests pass against the real store.
- Retained EngineReporting, EngineStep, EngineAnnotation and EngineSummary HTTP
  components pass with their existing request shapes and fake foreign boundaries.
- Focused local, standalone, dashboard, doctor, here and foreground CLI tests
  and captured-home fuzz seeds pass.
- TestDoctorSweepSurvivesPrunedRun passes. TypeScript native-bridge assembly,
  exact-package publication and publication fuzz seeds pass. Actual native Windows
  execution and publication remain lead/CI evidence.
- Controller, nodehost, acceptance, API generation, runner, launcher and storage
  test packages compile after the final fixture deletions.
- Commentcheck against main passes across 2,021 files after the final orphan
  fixture cleanup. API snapshots and embedded docs match. Whitespace checks pass.

No archived T1 source-function port is marked complete. Existing final-store and
HTTP components do not replace the native placement matrix or real-repository QA.
The old store-only event branch remains compile-checked, not claimed as a final
runtime port.

### Contract and native integration points

No foreign signature was changed. W9's source/mirrored migration guide,
controller-wire notes, changelog and client API snapshot describe the hard cut.
The earlier assignment audit remains applicable. Lead decisions 07:40 items 1
and 6, doctor and Windows packaging are already implemented and reverified.
A1-51's raw-client retirement is now done. W3 still owns the retained
StateStoreOld scheduler.

**W2 step-report implementation gap.** The declared StepReportRequest carries
state and positive source_attempt, optional times and masked error. RecordStep
requires StepRecorded with recorded=true. EngineController's decoder still
accepts the former step_id/status body and returns empty 204. A temporary overlay
component sent the final client through the real EngineController with fake
authentication and persistence. It failed with `invalid engine request` before a
write. This failing probe is not committed or counted as passing evidence.
W2 must implement the existing typed request and acknowledgement, including
source-attempt fencing. W9 does not send the old body as a fallback.

**W2/W3 native hosting.** NewNativeHost and the granted identity fields exist.
The missing pieces are the elected wingd.LocalController factory and the remote
private execution-state boundary. RemoteOwnerStore supplies scheduling,
reconciliation and expansion. It does not implement NativeHostStore's private
snapshots, acceptance, transitions, completion, step persistence and renewal.
C3's ReadSnapshot transports scheduling state, not the private RunSnapshot.
W2/W3 and the lead must resolve that transport contract before W9 can bind a
remote native host. Public rows or a second cached state owner cannot substitute
for it.

Local/here composition still needs selected-engine/source/environment capture,
process delegation, authorized data/log services, complete owner policies and
joined cleanup. The CLI already calls the supplied controller/host contracts and
tests those boundaries with fakes. No unused constructor wrapper was added around
the missing factory. Detached lifecycle must leave a surviving process. Schedule
checkout/proof/pinning/tick, persisted here reason, publication and debug release
retain their earlier authority/transport gaps. A publication failure must preserve
the completed local verdict.

### Shared files and friction

There are no foreign production changes. Shared test edits are the controller
fixture paths listed in the deletion inventory, internal/nodehost/events_test.go,
and the two acceptance files. Source/mirrored migration docs, CHANGELOG.md,
controller-wire notes and .apidiff/pkg_controller_client.txt are also updated.
No new mechanism, environment variable, dependency or user restriction was added.

The first dependent compilation found raw calls nested under selectors and four
log fixtures whose uploader helper had been retired. Their obsolete branches and
orphan fixtures were removed, then dependent compilation passed. Running the full
client suite exposed the pre-existing example's omitted query limit and incomplete
page shape. Its corrected example passes. The typed-report overlay failure stays
open with W2. Neither failed run is counted as passing evidence.

Git still resolves Korey's identity and no core.worktree override. Session scratch
is removed before the final report. The shared Go build cache is retained.


## Continuation from 2a3a0970a

This section supersedes conflicting status below. The worktree started clean.
W9 remains incomplete. The lead owns commits, pipelines, independent review,
native QA and release. No Git write or Sparkwing executable ran. Lead-owned
handoffs and amendments are unchanged.

### Legacy local-host cut

Removed pkg/localws, the separate trigger-consumer election/queue/heartbeat
loop, RunHosted and HostClaimedTrigger. Removed their state/coordination
backends, API probes and retry transport, admission adapters, standalone-store
selection, child dispatch, source preparation, private submission-environment
files, log wrappers, resource/profile helpers, and unused runner interface and
result reconstruction package. The public CLI already calls the final controller
and host contracts. No legacy host was restored as a fallback.

The retained orchestrator root contains only the CLI's source/ref selectors,
profile-pin refusal, paths, run-handle publisher and concurrency label reader.
The receipt and nodemetrics subpackages retain real callers. The podnodefixture
remains because sdkserve's fixture-build test still uses it. Removed the unused
local socket/direct-store secret sources and the raw Client's StateStoreOld
assertion. Client's legacy routes have no production caller outside the client
package. Their fixture migrations and deletion remain work.

Production Go delta versus HEAD is 2 added, 10,372 removed, net **-10,370**.
Orchestrator accounts for -9,322, pkg/localws -912, localsecrets -97 and
controller/client plus controller docs -39. W9-owned paths are -5 this session
and **-5,999 versus main**, within the -2,900 budget. Tests, testdata, acceptance
and contract declaration paths are excluded by the lead's budget convention.

[W9-LEGACY-CUT-FILES.md](W9-LEGACY-CUT-FILES.md) lists all 74 deleted production
files and 125 removed test paths. One removed test path moves to a generic
run-handle test, and the paths-without-store assertion moves to paths_test.go.
The remaining tests in those removed files exercised the retired graph.
The nodehost log-sink/local-backend combination test and its private helpers
are removed with LocalBackends. Existing log-sink behavior tests remain.

### Verification and test ports

- Whole-repository `go build -p 4 ./...` passes after the final production cut.
- Scoped vet passes for CLI, orchestrator, localsecrets, nodehost,
  controller/client, controller and store.
- The complete remaining orchestrator suite passes. The formerly Windows-only
  run-handle test now runs on all platforms and preserves immutable first-receipt,
  existing-destination and temporary-file assertions. This run
  establishes Linux evidence only.
- Focused local, standalone, dashboard, doctor, here and foreground CLI tests pass.
  TestSurface acceptance components pass. No native acceptance cell was enabled.
- Existing NativeHost, ClaimedNodeHost, OwnerHost, OwnerPolicy, OwnerEvaluation,
  Keeper and native-process fuzz seeds pass. These use foreign contract fakes.
  Focused log-sink tests pass after removing the old backend dependency.
- Full clisurface, controllerwire, runspec, doccheck and localsecrets suites pass.
  This rechecks sealed step selections and launcher grammar. The pruned-run doctor
  regression and TypeScript bridge/publication tests and fuzz seeds also pass.
- Controller, storage, nodehost and localsecrets test packages compile. The typed
  default-team caller inventory passes after removing entries for deleted callers.
- The retained podnode fixture builds through TestPodNodeFixtureBuilds.
- Commentcheck against main passes across 2,039 files. All public API snapshots
  match generated source after deleting pkg_localws.txt. Whitespace checks pass.

No archived T1 source-function port is marked complete. Retired host/consumer
fixtures do not establish final source, process, cancellation, retention,
secret-release, child, admission, environment, log, retry or placement behavior.
The lead and W3 still need final runtime ports for those concerns. Native local
peer/origin/session enforcement, encrypted secret composition, queue/timer serving
and graceful shutdown also require W2/W8's real controller factory. The old
localws serve-token and readonly configuration are retired surfaces, not final
authority contracts. Native placements and the full QA gate remain open.

### Assignments and integration left open

Lead decisions 07:40 items 1 and 6, the doctor handoff and Windows packaging stay
implemented and their focused checks pass. No reimplementation was needed.
The previous assignment audit still applies, except A1-51's legacy local consumer
and host graph are now removed. The remaining raw Client and W3's retained
StateStoreOld scheduler prevent declaring the legacy cut complete.

There is no foreign contract-shape change. The required real-integration points
remain:

- W2 must supply the elected wingd.LocalController factory with shared HTTP,
  dashboard, store, queue and leased maintenance. CLI composition already takes
  that exact factory contract and does not open a store before election.
- W3's RemoteOwnerStore supplies scheduling, reconciliation and expansion, but
  not NativeHostStore's private execution snapshots, acceptance, transitions,
  completion and renewal. Public scheduling rows cannot replace those snapshots.
  The C3 Controller interface has no private snapshot operation to adapt. W2/W3
  and the lead must resolve that transport contract before remote native hosting.
- W9 still needs real selected-engine/source/environment capture, process
  delegation, authorized data/log storage and native local/here construction.
  Its resolvers continue to return named unavailable errors before intake.
- Detached local-controller startup/readiness must leave a surviving process.
  The existing in-process lifecycle does not supply that CLI process boundary.
- Publication, schedule checkout/proof/pinning/tick and debug release still need
  the missing authority-bearing contracts or native implementations described in
  the earlier audit. A publication error must preserve the completed local verdict.

### Shared files and friction

The deletion inventory names shared production/test files. Other shared edits are
internal/nodehost/logs_test.go, pkg/store/default_team_callers_test.go,
pkg/controller/doc.go, the deleted localsecrets source, the localws API snapshot,
CHANGELOG.md and its mirror. C3 wire notes and source/mirrored migration and local
execution docs describe the cut. The native host and controller implementations
are unchanged. No new mechanism, environment variable or dependency was added.

The first dependent compilation caught that StateStoreOld still serves W3's
scheduler. Its attempted deletion was reverted exactly to HEAD before the passing
build and checks. The inventory check then identified the deleted callers' stale
entries, which were removed. Neither failed check is counted as passing evidence.
Early checks set GOTMPDIR but left TMPDIR unset, so Go's test temporary directories
used /tmp and were automatically cleaned. Later checks bind both to the authorized
scratch directory. Scratch is removed before the final report, while the shared
Go cache remains. Git identity still resolves to Korey without core.worktree.
Overwing discovery worked with its stale-binary notice.

## Continuation from fac7dd6ae

This section supersedes conflicting status below. The worktree started clean.
W9 remains incomplete. No Git writes, release, installation or Sparkwing
executable ran. Lead-owned handoffs and amendments are unchanged.

### Implementation and deletion

Removed the unused orchestrator HostTriggerRun/TriggerHost and RunNodeOnce
entrypoints. Their claimed-node dispatcher, legacy keeper, remote fetch/host
assembly, invocation-based run-context reconstruction, raw pod host and pod
log wrapper are gone. Removed RunNodeOption and its claim/allowlist options,
the global MetricsHook, RunHostedCommand, WorkerOptions and NewLocalRunID.
The five old host source files and nine obsolete host test files are deleted.
These entrypoints had no production callers outside their unreachable graph.
The local consumer still uses RunHosted and its state/backend graph.

Retained source admission and binary preparation in runner_source.go. Retained
install/environment helpers and the local host's trigger-source projection in
runner_process.go, and its guard helper in hosted_guard.go. This is a move of
shared code, not a second implementation. The podnode fixture remains because
internal/sdkserve/fixture_build_test.go still compile-checks it.

Removed Client.CreateSecret, CreateSecretForPipeline, DeleteSecret and
DeleteSecretForPipeline. NewSecretManager already owns writes and deletes.
Removed the unauthenticated old controller round-trip test and its unsupported
route-table entry. Existing final secret transport and authenticated encrypted
mutation components retain the write/delete concerns. Execution secret reads
still serve the local consumer and remain for its final host cutover.

W9 now composes W8's NewLocalRuntime, the owner-aware lifecycle client,
NewManagedUnitManager and the private log reader under one captured home.
Composition takes wingd.LocalController's factory contract and does not open a
store. W2 has no elected factory in this checkout. openLocalController therefore
returns a named unavailable error when the elected runtime attempts that open.
No alternate store or legacy service is used. Foreground resolution receives
parsed LocalStartOptions before construction, so --home selects the runtime's
home as well as its Run arguments.

Production delta versus HEAD is 502 added, 1,369 removed, net -867. Orchestrator
accounts for -917, Client for -44, and CLI composition for +94. W9-owned paths
are +50 this continuation and -5,994 versus main, within the -2,900 target.
Tests, acceptance, testdata and contract declarations are excluded.

### Acceptance, ports and verification

Invariant: the local runtime, lifecycle client, managed units and log source
use one captured home, and composition cannot open the controller before election.
FuzzLocalServicesKeepOneCapturedHome passed three seeds and 2,668 generated cases
with two workers. It refuses different runtime/unit homes and checks the runtime's
bound-home refusal. The file-backed component reads the selected private log,
preserves executable/version metadata and refuses a cancelled read. The foreground
regression checks the parsed home, version and stay policy at the resolver.

The source-admission refusal test now calls the retained admission boundary
instead of the deleted fetch/host path. Its five source cases remain. The
protocol trigger-source test and checkout helpers used by envelope tests moved
without losing their assertions. No archived T1 port was completed. No native
placement cell was unskipped. The old host's integration coverage must still be
established through the final native placements.

All Go commands use GOWORK=off, GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-gocache and temporary directories under /tmp/sw-astra-W9.
Tests and builds use -p 4 at most.

- Whole-repository build passes after the final production deletions.
- Scoped vet passes for CLI, controller/client and orchestrator.
- Full clisurface, controllerwire, runspec and doccheck suites pass.
- TestSurface acceptance components pass, including the expanded removed-client
  API check. Focused engine/data/secret/run-reader/log-reader client tests pass.
- Local, standalone, dashboard, doctor, here and foreground CLI components pass.
- Focused retained source/environment/guard helpers pass. The profile guard
  passes after removing exceptions for deleted host functions.
- Existing TestClaimedNodeHost, TestNativeHost, TestOwnerHost, TestOwnerPolicy,
  TestOwnerEvaluation and TestKeeper components pass. These use the owners'
  contract fakes and native fixture boundaries, not a composed controller service.
- Changed controller and orchestrator test packages compile. The actual
  TestDefaultTeamTwinsAreCalledOnlyWhereTheDefaultTeamIsMeant inventory passes.
- TestDoctorSweepSurvivesPrunedRun passes. Full runspec tests retain the 256-case
  sealed StartAt/StopAt/Only acceptance/retry property. The launcher registry
  retains required --docker-image, removes launch --deadline and allows owner
  plan units without a synthetic job selector. No reimplementation was needed.
- TypeScript bridge assembly/publication tests and publication fuzz seeds pass.
  Native Windows execution and actual publication remain lead/CI evidence.
- Commentcheck passes across 2,107 files. Whitespace and embedded-doc drift pass.
  The controller/client API snapshot was regenerated from current source.

The first broad CLI selection exposed a test that invoked the real default
resolver against the sandbox's forbidden socket transport. It now tests the
missing-lifecycle failure with a fake, retaining the no-fallback assertion.
The corrected selection passes. No real API socket, detached lifecycle,
Postgres, Windows/Darwin SDK, Docker/Kubernetes or real-repository QA is claimed.
Pipelines and full-repository tests remain lead work.

### Assignment audit and remaining integration

The earlier audit below still applies, with these updates. Lead decisions 07:40
items 1 and 6, the doctor handoff and Windows packaging are implemented and
reverified. A1-03/08/09/13-18/22/38/55 retain their final wire/error/claim/failure/
expansion/step/launcher transports. A1-20/29/30/34/37/43/45/47/49 retain their
existing credential/data/cache/version/settings/guard components. A1-51's
remaining source/reference/runtime retirement and A1-53 native QA remain open.

Priority (1) remains incomplete. StateStoreOld, RunCoordination, RunHosted,
local consumer/backend/lease plumbing and the remaining raw Client still exist.
The unused pkg/localws entrypoint is the external production reference to
RunLocalTriggerConsumer. It needs its own retirement or migration before that
remaining graph is cut. No old host is restored by this continuation.

A1-10/11/19/39/40/41/50 still need real composition:

- W2 must supply the elected wingd.LocalController factory, including shared
  HTTP/dashboard handlers, the owned store, queue, reconciliation and maintenance.
  W9's construction now accepts that exact interface. No C2 signature changed.
- W3's NewNativeHost now exists, but RemoteOwnerStore only supplies scheduling,
  reconciliation and expansion projections. It does not implement NativeHostStore's
  private snapshots, acceptance, transitions, completion and renewal. Public
  scheduling projections cannot replace the private persisted execution snapshot.
- W9 still needs selected-engine/source/environment capture, process delegation,
  authorized storage/log bindings, native here hosting and publication. Publication
  failure must preserve the local verdict. No contract was invented to bypass a
  missing authority or persistence boundary.
- W8's lifecycle client starts a runtime in-process when no elected owner exists.
  A CLI invocation that returns must leave an on-demand controller in a surviving
  process. Detached foreground startup/readiness still needs its native binding.
  Foreground home selection is now correct, but this process-lifetime seam remains.
- Schedule checkout/environment/proof/pin, tick/dry-run/timer integration,
  persisted here reason and debug-release policy/acknowledgement remain open.

No foreign contract was changed. Missing constructors/adapters are integration
points above, not approval requests. Native acceptance and release remain open.

### Shared files and friction

Shared production files are the five deleted orchestrator host files, the three
retained-helper files, and hosted_run.go, worker.go and backends.go. Shared test
changes are repo_admission_test.go, source_url_test.go, the moved checkout/source
fixtures and the nine deleted old host tests. pkg/controller/secrets_test.go loses
only its obsolete raw-client round trip. tests/acceptance/surface_contract_test.go
checks the four retired mutation methods. The client API snapshot, CHANGELOG.md
and generated docs mirrors are updated. W9's wire notes describe the cuts.
There were no outside production callers of the deleted host entrypoints to migrate.

Xwing environment-context discovery failed because its session-index lock is
outside the writable roots. The user-selected environment 1087 was reused.
Overwing discovery worked with its stale-binary notice. The first inventory
selection matched no tests. It was corrected to the actual inventory test, which
ran and passed. No missing-test run is counted as evidence. Git still resolves
Korey's identity and has no injected core.worktree. Scratch is removed before the
final report, and the shared build cache is retained.

## Continuation from 725342711

Started with a clean worktree. This section supersedes conflicting status below.
W9 remains incomplete. The lead owns commits, independent review, pipelines,
native QA and release/publication. Lead-owned handoffs and amendments are untouched.

### Implementation and deletion

Removed the unused controller-client raw/SSE log reader and its NodeLogs type,
live-log read/stream methods and error/response types, and raw event listing.
Removed ListNodeSteps, ListDebugPauses and ListPendingApprovals from Client,
StateStoreOld and the localState wrappers. No production host calls those readers.
RunReader, DebugReader, RunReview and LogReader retain the final public APIs.

Removed WithWatchObserver, WatchReport, heartbeat watch decoding and the host's
unconnected NoteWatch bridge and process-global sink registry. No production
caller installed that observer. Segment uploaders still deliver watch changes
through OnWatched. Removed the unused KeepsLogSegments capability probe, Token
bearer getter and private put wrapper. No alias or forwarding helper was added.

Deleted client/logs.go and client/watch_test.go. Removed tests and fixture code
that exercised only the deleted watch bridge and capability probe. Controller
and host log-handler tests now make direct HTTP requests. Their existing handler
assertions remain. Step, pause and pending-approval callers use the typed readers.
The cross-run credential fixture retains its own claim input instead of retrieving
it through Client.Token.

Production Go delta is 0 added, 477 removed, net -477. W9-owned paths account for
-432. Shared production removals account for -45. W9 totals -6,044 production Go
lines versus main, within the -2,900 target. Existing host code and raw claim/state
wire still exist, so the budget does not establish completion.

The inventory check reported exactly three stale entries after the localState
reader deletion. Removed those entries. The real typed caller inventory passes.
No new default-team exemption was added.

### Verification

Go used GOWORK=off, GOFLAGS=-buildvcs=false, GOCACHE=/tmp/sw-gocache and
TMPDIR/GOTMPDIR under /tmp/sw-astra-W9. Tests used -p 4 at most.

- Whole-repository go build passes after the final source changes.
- Scoped vet passes for client, controller, storage, orchestrator and nodehost.
  CLI vet also passed earlier in this continuation.
- Dependent-package test compilation passes for controller, orchestrator,
  nodehost and storage after all caller migrations.
- Focused final engine, log-reader, run-reader/query and secret-management
  client tests pass. Existing TestSurface* acceptance components pass.
- Focused local-run/intake/preparation, standalone-host, dashboard and schedule
  CLI components pass. Full clisurface, controllerwire and doccheck suites pass.
- The Windows TypeScript bridge assembly/publication checks and publication
  fuzz seeds pass. That handoff was already implemented and needed no source
  changes. Native Windows execution and actual publication remain CI/lead work.
- Commentcheck passes across 2,111 files. The typed store-caller inventory,
  whitespace and embedded-doc mirror checks pass. Client/storage API snapshots
  are regenerated from source.

The migrated redirect test now exercises the final LogReader with a fake
transport. Invariant: redirect responses never make a log read leave its selected
controller or send another request. Five seeds and 6,959 generated cases pass,
including same-origin, foreign-origin and malformed Location values. The test
also checks that the caller's HTTP client remains unchanged. This ports the
existing redirect-boundary concern to the replacement reader. No new production
security mechanism was added.

No native acceptance cell was enabled and no archived T1 port was completed.
Dependent legacy controller/host runtime tests were compile-checked only. Their
old store setup and HTTP projections still require the owning areas' migration.
Full legacy suites, real HTTP/api.sock execution, Windows/Darwin execution,
Docker/Kubernetes and real-repository QA remain unrun. No Sparkwing binary,
pipeline, installer, release or publication ran.

### Remaining assignments and integration

The session-14 assignment audit below still applies. This pass completes only
the additional reader/watch/probe deletion above. Priority (1) remains open.

- Raw state/claim Client calls still reach W3's StateStoreOld/RunCoordination
  graph in trigger_host.go, run_node.go, pod_node_host.go, run_node_remote.go,
  backends.go and hosted_backends.go. Those hosts must use their final store,
  credential and signed data/log contracts before the remaining wire is cut.
- Queued raw trigger/job claim methods have legacy W2/W3 test-fixture callers.
  Their final claim/intake migration remains separate from this reader cut.
- W2's elected factory implementing wingd.LocalController is still absent.
  The final foreground/runtime/lifecycle interfaces exist. W9's native service
  resolver remains unfinished and must bind that factory without reopening a
  second store.
- W3's full remote NativeHostStore remains a real-integration point, including
  private execution snapshots, acceptance, transitions, finish and renewal.
  Public scheduling projections cannot replace private store snapshots.
- W9 still needs native source/environment capture, selected-engine host
  composition, process credentials, authorized byte/record/log bindings,
  here resume and publication. Publication failure must preserve the local
  verdict. Local schedule checkout/environment/proof/pin, timer/tick/dry-run,
  here-reason retention and debug-release policy/acknowledgement remain open
  as recorded in the earlier audit. Existing contract/fake components remain
  component evidence only.

No final foreign contract was changed. The legacy shared state interface loses
only its unused public readers. There is no new contract request in this pass.
The Windows packaging handoff and its existing verification remain complete at
the component level. A1-53's native QA is still unclaimed.

### Shared files, assumptions and friction

Shared production edits: internal/orchestrator/backends.go and segment_logs.go,
and pkg/storage/state.go. These delete wrappers and an unused bridge only.
Shared test caller edits: internal/orchestrator/trigger_host_test.go,
segment_logs_test.go, and pkg/controller/approvals_test.go, livelogs_test.go,
log_segments_internal_test.go, node_client_contract_test.go and
runner_claim_cross_run_test.go. The inventory loses three entries in
pkg/store/default_team_callers_test.go. Client/storage API snapshots,
CHANGELOG.md and generated documentation mirrors are updated. W9's own wire
contract notes the retirement.

Assumption: the typed public readers replace the unused legacy read surface.
No customer command or flag changed. No production mechanism, option, setting,
environment variable, timeout or limit was added. Reverting this cut would
restore the removed methods and their callers together.

Friction: dependent compilation found one host log-reader caller and the final
step projection's ID spelling. Both were migrated. Overwing matching/showing
worked and reported its installed-binary staleness notice. No Git writes.
Git still resolves Korey's identity and has no injected core.worktree.
Session scratch is removed before reporting. The shared build cache is retained.

## This continuation (session 15)

Started at 862a1e1a8 with a clean worktree. This section supersedes conflicting
status below. W9 remains incomplete. The lead owns commits, review, pipelines,
native QA and release/publication.

### Changes and deletions

Deleted the unused orchestrator run-list, status, log, summary and timeline
commands. The CLI already calls the final controller readers. Deleted their
pagination, pivot, rendering, raw/SSE log readers and standalone-store merge
code, plus the old direct SQLite/profile reader factories. Removed the now
unreachable ClientBackend, FromSpecs and OpenStateStoreFromSpec paths. Removed
the unused warm-runner cache sweep and its TerminalRunLister dependency.

Deleted Client.ListRuns, its old filter-version negotiation and duplicate query
encoding, and Client.ListApprovalsForRun. The example uses NewRunReader. The
remaining controller approval-test caller uses NewRunReview.ListApprovals and
the public job_id projection. There is no forwarding alias or test-only shim.
The retained host state resolver, excerpt writer and store backend remain.

Moved ParseLooseDate and its four date/lookback tests to internal/clisurface.
runs_list.go calls that owner. Deleted the rest of the unused client-side run
filter. The existing wingd end-to-end test checks its terminal status directly
instead of retaining the old CLI's helper. Doctor's comment no longer names the
deleted profile factory.

This pass deletes 48 files, including 16 production Go files. Net production Go
change is -4,939 lines across touched files: 42 added and 4,981 removed. The W9
path budget is -5,612 production lines against main, within its -2,900 target.
Most deletions in this pass are old CLI implementations under orchestrator,
which the lead's path-based budget counts under W3. No run-host implementation
was replaced or removed in this continuation.

The store-caller inventory initially reported 14 entries for the removed paths.
Those entries and their two unused rationale constants are deleted. The real
inventory check passes. No new default-team exemption was added.

### Verification

All Go commands used GOWORK=off, GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-gocache, TMPDIR/GOTMPDIR under /tmp/sw-astra-W9 and -p 4.

- Whole-repository go build passes.
- Scoped vet passes for CLI, client, clisurface, orchestrator, backend, storeurl,
  storage and opsview.
- Focused CLI listing/status/log, local-run, lifecycle, schedule and release
  tests pass. Final client engine, run-reader/query, log-reader and secret-manager
  component tests pass.
- Existing TestSurface* and TestEngine* acceptance components pass.
- Full clisurface, controllerwire, doccheck, apiref and apispec suites pass.
- Retained local-backend resolver tests pass. Changed/dependent package test
  compilation passes, including controller and the migrated approval caller.
- TypeScript Windows bridge assembly/publication tests and publication fuzz
  seeds pass in bin. Packaging was already implemented and needed no changes.
- The default-team caller inventory, commentcheck, whitespace and embedded
  migration/changelog mirror checks pass. Client/storeurl API snapshots are
  regenerated.

No native acceptance cell was enabled, and no archived T1 port was completed.
The four moved parser tests retain existing coverage. Obsolete tests exercise
only deleted implementations and are removed with them. Existing final CLI
suites cover listing, pagination, safe views, timelines, summaries and log
following. This is component evidence. Full legacy suites, HTTP/api.sock,
Windows/Darwin execution, Docker/Kubernetes and real-repository QA remain unrun.
No operational Sparkwing binary, pipeline, installer or release ran.

### Remaining assignments and real integration

The session-14 assignment audit below still governs unfinished work, except
that the old read/backend graph and storeurl/spec.go are now deleted.
The remaining legacy Client graph is rooted in W3's StateStoreOld/
RunCoordination, trigger_host.go, run_node.go, backends.go and hosted_backends.go.
Raw queued claims additionally have legacy W2/W3 fixture callers. They remain.
Priority (1) is incomplete.

W9 must finish native local-services construction against wingd.LocalController,
source/environment capture, selected-engine host composition, per-process
credentials, authorized data/log/result bindings, here resume and publication.
W2's elected LocalController factory and W3's full remote NativeHostStore
implementation remain real-integration points. The current contract adapters
and fakes do not establish native execution. Schedule checkout/environment/
proof/pin, timer/tick/dry-run, debug-release policy/acknowledgement and authorized
publication remain open as recorded below. Publication must preserve the local
verdict after an upload failure.

No foreign contract change was made. Existing contract requests remain open.
The Windows packaging handoff is implemented and reverified locally. Native
Windows execution and actual publication remain CI/lead evidence.

### Shared files and friction

Shared production deletions in internal/orchestrator:
jobs_cli.go, jobs_cli_remote.go, jobs_cli_logs_backend.go, runs_summary.go,
runs_timeline.go, runs_page.go, runs_pivot.go, status_pretty.go, log_tail.go,
run_stores.go, runs_filter.go, read_backend.go and warmgc.go.
Shared production edits there: profile_backend.go and failure_excerpt.go.
Shared backend changes: internal/backend/client_backend.go and from_specs.go
are deleted, and completeness.go drops only the deleted receiver.
Shared storage changes: pkg/storage/storeurl/spec.go is deleted and
pkg/storage/state.go drops obsolete factory documentation.
Other shared changes: internal/opsview/doctor.go removes the stale comment,
internal/orchestrator/wingd_e2e_test.go removes its old helper dependency,
pkg/controller/approvals_test.go migrates one reader call, and
pkg/store/default_team_callers_test.go removes the 14 stale entries.
Associated obsolete tests are deleted beside those implementations.
CHANGELOG.md, both generated documentation mirrors and the client/storeurl API
snapshots are updated. Lead-owned handoffs/amendments files are untouched.

Assumption: the final controller readers replace the unreachable CLI readers.
The old exports have no production callers. No customer command or flag was
removed in this pass. No new runtime mechanism, option or limit was added.

Friction: a controller test still called the removed approval method and was
caught by the direct-dependent compilation check, then migrated. Broader native
verification remains with the lead under the swarm brief. No Git writes.
Session scratch is removed. The shared build cache is retained.

Session 14 starts at edc98d9d4 with a clean worktree. This report supersedes
conflicting status below. W9 remains incomplete. The lead owns Git writes,
independent review, pipelines, native QA and release/publication.

## This continuation (session 14)

### Changes and deletions

Removed Client.CreateToken, ListTokens, LookupToken, RevokeToken, RotateToken
and ListSecrets. Typed token/secret managers already own their operations.
The request/response structs remain because the final transport consumes them.
Removed Client.EnqueueTrigger and localState.EnqueueTrigger. StateBackend now
requires EnqueueTriggerWithEnv, and hosted child dispatch calls it directly.
Deleted the optional interface, wrapper and environment-dropping fallback.
Both direct local fixtures pass nil explicitly. No compatibility helper replaces
these methods. strings.IndexByte replaces the private duplicate.

Removed the unused releaseAssetName and manifestSignedByTrustSet helpers.
Moved implemented engine constructors from engine_contract.go to engine_client.go.
Moved NewDataClient into data.go and deleted data_contract.go. These are real
implementations, not additional constructors or interfaces.

Net production Go reduction is 209 lines across all touched files: 153 in W9
and 56 in shared caller/diagnostic files. Physical source removes 306 lines and
adds 97, including relocation of 76 scaffolding-file lines as 73 implementation
lines. The lead's area-budget filter, including the untracked implementation
file, gives W9 -5,503 production lines against main, versus its -2,900 target.
Legacy code still exists, so meeting that budget does not establish completion.

### Doctor handoff

Invariant: the sweep's result determines success, while failure to record an
outcome remains visible in the report. A current-schema SQLite fixture creates
a run, registers a cleanup, deletes the run, and invokes the real sweep. Before
the fix, the completed cleanup failed with sql: no rows in result set.

The retained diagnostic helper now records each outcome independently and puts
its recording failure in DoctorStraySession.RecordError. Pretty output warns
beside the session, JSON carries record_error, and plain output counts
unrecorded_step_outcomes. Sweep errors still propagate. The regression proves
that the record is removed, the verdict remains reaped and all three formats
expose the recording failure. It uses a real command cleanup and store, with
no sleep or mock event writer. SQL fixture seeding isolates this diagnostic
from the already-broken legacy CreateRun API. This proves the retained sweep
adapter. Native lifecycle CLI cleanup remains unconnected.

### Verification

Go uses GOWORK=off, GOFLAGS=-buildvcs=false, GOCACHE=/tmp/sw-gocache,
GOTMPDIR and TMPDIR under /tmp/sw-astra-W9, and -p 4 at most.

- Whole-repository go build -p 4 ./... passes.
- Vet passes for cmd/sparkwing, controller/client, clisurface, controllerwire,
  orchestrator, opsview, storage and tests/acceptance.
- Focused CLI local intake/hosting, standalone hosting, foreground lifecycle,
  controller options, verified release and update tests pass.
- The new doctor pruning regression and existing report/render tests pass.
- Engine/credential/runner-enrollment/data/secret-management/surface components
  pass in controller/client and tests/acceptance, including enrollment seeds.
- Full clisurface, controllerwire, doccheck, apiref and apispec suites pass.
- TypeScript native-packaging/publication and release-target checks pass.
  Windows packaging was already implemented and needed no source change.
- The default-team caller inventory passes. There is no stale W9 entry to edit.
- Commentcheck passes across 2,113 files. Whitespace and embedded-doc drift
  checks pass. The client API snapshot is regenerated with cmd/apidiff.

The initial broader component run found stale acceptance inputs. Failure reads
now expect generation, matching client and server. Transition/replay fixtures
carry source_attempt and the structured JobFailure instead of result.error.
The projection assertion checks the ordinal. Those migrated tests pass. No new
scenario, harness registry entry or archived T1 port is claimed. No native
acceptance cell was enabled. Full legacy CLI/client/orchestrator suites, real
HTTP/api.sock, Windows/Darwin, Docker/Kubernetes and real-repo QA remain unrun.
No operational Sparkwing binary, pipeline, release or publication ran.

### Assignment audit and remaining integration

Earlier completed work remains in place. No foreign contract was changed.

| Assignments | Status |
| --- | --- |
| A1-03/08/09/13-18/22/38/55; attempt/step/launcher handoffs | Shared wire, source ordinal, typed step report and immutable award/Job-instance correlation are implemented. Native owner/step/launcher integration remains. The stale fixture migration above now passes. |
| A1-10/11/19; execution and pipeline identity handoffs | Selected-engine/source inspection and separate alias/entrypoint mapping remain. Full source-to-host-to-hello composition remains. |
| A1-20/39/40/41/49 | Typed enrollment, reserved-pool proposals, private-file rollback and lifecycle adapters remain. Real provisioning, runner startup, local controller factory and services remain. |
| A1-29/30/34/37/50 | Signed data, read-only remote cache opt-in, local/here prepared intake and publication adapters remain. Full authorized storage/result/log and host composition and old wire deletion remain. Publication must preserve the local verdict on upload failure. |
| A1-43/45/47 | Exact engine selection, notices, upgrade, setting precedence and shared guard grammar remain. Native selected-source hosting and scheduled intake remain. |
| A1-51 | Current schemas/docs and final public readers remain. Duplicate management methods are now removed too. Remaining raw state, trigger and named-claim Client methods still serve the W3 legacy graph. |
| A1-52; Windows packaging | Both native bridge architectures, version stamping, package assembly and publication dependency remain implemented. Native Windows execution/publication evidence belongs to CI/lead. |
| A1-53 | Native/platform/placement cells remain unclaimed. |
| Sealed step windows; launcher grammar | Previously implemented and retained. |
| Doctor missing-run outcome | Retained sweep fix and regression pass. Native lifecycle cleanup integration remains separate. |
| Acceptance harness cleanup | No new harness production code, catalog entry or scenario registry was added. W9's existing package tests remain in place. |

Concrete integration points still open:

- W2 must supply the elected factory implementing wingd.LocalController.
  NewLocalRuntime and NewLocalLifecycle exist. No NewLocalController constructor
  exists in this tree. W9's native local-services resolver remains unfinished.
- W3's RemoteOwnerStore implements reconciliation, expansion and scheduling,
  but not the full NativeHostStore. ReadSnapshot, AcceptPlan, TransitionRun,
  TransitionNode, FinishRunAndUnit, RenewOwner and RenewNodeClaim still need
  the remote implementation and necessary private snapshot transport. The
  public ScheduleSnapshot cannot substitute for store.RunSnapshot.
- NewNativeHost exists. Native source capture, owner policy/waiting, per-process
  delegation and authorized byte/record/log binding still need composition.
  No fake-only component result is evidence for those native bindings.
- Immutable initial here-reason retention, local schedule checkout/environment/
  proof/pin operations, native managed timer/tick/dry-run operations, debug-release
  policy/acknowledgement and authorized publication remain the previously
  recorded foreign contract/integration requests. No alternate route was added.
- The remaining legacy client graph reaches StateStoreOld/RunCoordination in
  trigger_host.go, run_node.go, backends.go, hosted_backends.go and storeurl/spec.go.
  Queued-claim methods additionally have W2/W3 legacy fixture callers. They are
  not copied into a test helper or replaced with forwarding compatibility code.

### Shared files, assumptions and friction

Shared caller migrations: internal/orchestrator/backends.go, hosted_run.go,
backends_coordination_test.go and hosted_backends_test.go. The obsolete
EnqueueTrigger note is removed from pkg/storage/state.go.
Other shared edits: internal/opsview/doctor.go and doctor_recording_unix_test.go,
tests/acceptance/{engine_amendments,engine_lifecycle_http,surface_contract}_test.go,
.apidiff/pkg_controller_client.txt, CHANGELOG.md and generated documentation
mirrors. Lead-owned handoffs.md and amendments files are untouched.

The doctor change adds one diagnostic field and one plain-report count because
a successful cleanup cannot disappear when its event write fails. It adds no
new service, interface, setting, environment variable, cap or timeout. The
trigger cleanup removes an optional-interface mechanism. Engine/data file
relocation removes completed scaffolding without changing their APIs.

Initial doctor fixture attempts hit the retired CreateRun args_json column and
a terminal-row constraint. The final fixture uses current-schema pending state,
then deletes it before the sweep. The real missing-run recording failure was
reproduced before the fix. Overwing reported its installed-binary staleness
notice but matching/showing worked. Git retains Korey's identity with no injected
core.worktree. No Git state was written. Scratch is removed before reporting,
and /tmp/sw-gocache is retained.

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
