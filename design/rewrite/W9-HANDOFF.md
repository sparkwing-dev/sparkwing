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
