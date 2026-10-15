# W9 continuation handoff

W9 remains incomplete. This session continues committed base `ac48b61a6`.
The worktree builds. The completed CLI adapters use frozen interfaces and fake
controllers where their owners still have stubs. They are not evidence of a
working deployment or full surface acceptance.

## Completed before this session

- Final grammar validation precedes command side effects. Help, completion and
  discovery derive structure from `clisurface.Registry`.
- Run/trigger/plan/lint parsing preserves pipeline arguments after `--`, explicit
  output and pause settings. Removed command and flag vocabulary is rejected.
- Profiles are machine-only, redact tokens, and retain a connection on failed
  revocation. Cloud dispatch and project credential fallback are deleted.
- Cancellation uses `controllerwire.Controller.Cancel` through one transport.
  Applied state, replay markers and structured failures survive batch output.
- Engine HTTP transport, response validation, retry policy, API compatibility
  windows and shared CLI error formatting are implemented.

## Completed in this session

### Run observations

`runs status` now calls the shared public controller reader for local and remote
connections. It no longer opens SQLite or calls the old orchestrator status,
summary, timeline, receipt, tree or error renderers. Numeric selectors reach the
controller unchanged. Status and its exit code come from the same observation.

The summary, timeline, errors, receipt and tree views preserve public job and
attempt data. `--steps` fetches step history. Tree traversal reads every child
page and rejects repeated identities and cursors. It refreshes descendants on
every follow poll, even if the parent has not changed. Policy notices are part
of the observed detail, so a read-time notice can appear without a state change.
Receipts preserve the controller's hash, costs and settlement facts.

Follow emits changed observations. Incomplete observations, interrupted waits,
lookup failures and receipt identity mismatches exit 3. Invalid usage exits 2.
Terminal failure exits 1, unless `--exit-zero` is set. Plain output is a status
word, failed job IDs, tree run IDs, or a receipt hash. Explicit steps add job/step
IDs. Pretty output retains the detailed tables and timeline.

The local W9 wire repair adds `RunReader`, `RunQuery`, `RunPage`, `RunSteps`,
`StepDetail`, the typed receipt records, and optional `RunDetail.Steps`.
`NewRunReader` shares the authenticated engine HTTP implementation. Existing
C2 routes carry these reads. The contract document names pagination, parent
filter and display-only fields. W2 still needs to serve those shapes.

The unused `client.GetRunReceipt` API is deleted. Its remaining controller tests
now call the server handler through a response recorder. The other legacy client
APIs still have callers and remain part of the unfinished migration.

### Dashboard

Dashboard commands delegate to `wingd.LocalLifecycle` and
`wingd.ManagedUnitManager`. Tests exercise start/stay, stop, status, installation,
uninstallation, Windows service refusal and failure propagation. Logs retain
bounded tailing, complete-line streaming, JSON framing and truncation markers.

The separate supervisor, PID records, locks, process identity machinery,
platform implementations, restart command and associated tests are deleted.
Consumer process helpers remain in `consumer_process.go` because the old detached
run consumer still calls them. The obsolete PID-based dashboard info tip is gone.

Production dashboard composition returns a structured unavailable error because
W8 provides no runtime/unit constructors or canonical log source yet. Nondefault
access-policy options and install-force also fail explicitly because their frozen
interfaces have no fields to receive those settings. These are integration gaps,
not completed feature restrictions.

### Upgrade

The new upgrade command detects the repository SDK and calls the engine upgrade
contract. It prints previews, selected pins, changed files, verification results
and notices. Plain output is one target-version line. Policy and verification
failures retain their structured errors. It does not claim success for an
unverified or unapplied result.

Its verifier operates in the staged checkout, resolves dependencies, builds Go
or type-checks TypeScript, then runs describe and plan with the selected engine.
It never executes job bodies. Native npm invocation is shared through
`pipelineentry.NPMCommand`, extracted from depcache's existing Windows policy.
Windows batch shims run through Node and npm-cli.js without a shell. Depcache,
TypeScript installation and upgrade now use that same helper.

Engine resolution/staging/policy and `procenv.Build` remain owner stubs. The
verifier is fake-tested. Real engine execution and real Windows execution remain
open. Pipelines that require arguments unavailable from their defaults fail
verification under the final upgrade grammar, leaving original pins unchanged.

## Work still required

1. Migrate the remaining legacy CLI handlers and typed clients. Run listing,
   logs/search, retry, prune, annotations, approvals and statistics still use
   old APIs or models. Other management handlers retain old pflag execution
   behavior behind the final top-level grammar.
2. Wire W3 run hosting and W6 sealed requests/settings. Publication, cache-from,
   target selection, here-resume and pipeline-ref selection remain unfinished.
   Detached execution still launches the removed consumer entrypoint and must
   move to the local controller queue. Then delete the consumer and its helpers.
3. Compose W8 lifecycle/runtime/managed units/logs. Finish hidden wingd arguments,
   update refresh/restart, doctor integration, schedules selectors/pause/resume
   and stable timer startup through `__tick`.
4. Implement pools, cache warm, deployment tokens and remaining management reads
   and writes. Team-member credentials, reserved pools, restricted secrets and
   final management routes need the authoritative typed clients.
5. Replace profile.Load/Save with W6 final machine parsing/persistence and W2
   credential metadata. The profile path is machine-only but still uses the old
   loader schema. Shared connection selection also needs the final W6 resolver.
6. Finish source-guide and executable-caller migration. Generated run-status
   references and embedded mirrors were updated here. Older guides, scripts,
   examples and runtime tests still contain removed vocabulary and old schemas.
7. Run actual HTTP/api.sock, daemon/service, Windows, engine-upgrade and complete
   acceptance integrations after the owning implementations land.

## Contract changes needed in other areas

- W2: one ordered mixed trigger/job claim route. Separate queue scans cannot
  substitute for a mixed award.
- W2: credential enrollment for `profiles add --scope` and restricted secret
  writes for `secrets set --only-on`.
- W1/W2: retry request selection for failed/all jobs, or an explicit rule deriving
  the CLI selections from intake.
- W8: dashboard runtime/unit composition, canonical log source, access-policy
  values in LocalStartOptions, install-force semantics and effective endpoint
  reporting. No W8 contract was edited in this session.

The new public read models are W9-owned additions, recorded in
`contracts/c3-controller-wire.md`. W2 implementation must consume them. No new
route or authorization grant was introduced.

## Verification

All Go commands use `GOWORK=off GOFLAGS=-buildvcs=false`, at most `-p 4`, and
GOCACHE/GOTMPDIR/TMPDIR under `/tmp/sw-astra-W9/main`. Tests use `-p 2`.

- Full repository `go build -p 4 ./...` passed after the final source changes.
- Vet passed for cmd/sparkwing, clisurface, controllerwire, controller/client,
  controller, depcache, pipelineentry and tests/acceptance.
- Full clisurface and controllerwire tests passed.
- Focused status, dashboard, upgrade, engine client, observation reader, npm
  platform selection, cancellation, error-formatting and dispatch tests passed.
- Acceptance slices passed: `TestSurfaceGrammarContract`,
  `TestSurfaceWireDetailTransport`, `TestSurfaceRunObservationTransport`.
- Generated-reference drift, documentation mirror drift, default-team caller
  inventory and `git diff --check` passed.

No full surface acceptance cell was unskipped. In particular,
`surface-api-and-detail-parity`, `surface-cli-vocabulary-and-flags`, dashboard
lifecycle and version-policy scenarios still require real implementations.
The added transport tests distinguish fake transport evidence from those cells.
No W9-owned source test is directly named by the T1 port inventory.

Ported regressions cover terminal/missing observations, follow, profile-independent
read behavior, receipt integrity and failure rendering. Dashboard log regressions
cover bounds, captured snapshots, partial lines, cancellation, zero tail and
oversize-line markers. npm platform tests were moved with their implementation.
The former bare-ID status test now asserts the final kind-named selector rule.

An early broad test filter also selected existing debug-parser tests and an old
SQLite reader test. Those failed on removed --node vocabulary and missing legacy
sparkwing_meta. Another broad Upgrade filter selected two legacy secret-import
tests, which attempted Unix sockets and failed under the sandbox. Final checks
use exact adapter test names. Those failures are not reported as a full-suite
pass. The legacy fixtures also choose hard-coded /tmp socket paths despite
TMPDIR. No daemon process remained after the test cleanup.

No Sparkwing CLI executable or pipeline was invoked directly. The old selected
secret tests attempted their daemon helper, which the sandbox refused. Native
Windows runtime behavior and full HTTP/service integration were not exercised.
No git writes were made. The effective Git identity remains Korey Gambill and
local core.worktree is unset. Temporary generation tests and scratch/cache files
were removed at handoff.

## Shared edits and mechanisms

Shared files touched outside the W9 runtime directories:

- `pkg/store/default_team_callers_test.go`: removed the two deleted view callers.
- `pkg/controller/receipts_test.go`: direct handler tests after deleting the old
  receipt client API.
- `internal/depcache/depcache.go`, `depcache_npm_test.go`, and
  `internal/pipelineentry/pipelineentry.go`, `npm.go`, `npm_test.go`: extracted and
  reused the existing npm platform policy. No frozen contract changed.
- `tests/acceptance/surface_contract_test.go`: public observation transport slice.
- C3 wire contract, this handoff, changelog and generated documentation mirrors.

Mechanisms added: public observation reader/types, contract-based dashboard and
upgrade CLI adapters, and reuse of the existing npm launch policy through an
existing package. No environment variable, credential grant, compatibility alias,
new package or runtime service was added. The smaller option of keeping local
SQLite renderers was rejected because the controller must own public state.

Read-only review found and corrected Windows npm invocation, missed descendant
updates during tree follow, and the receipt-mismatch exit code. Shared-tree
compilation briefly raced file deletions. Final checks ran after those edits.
The session removed 2,990 Go lines before the handoff edit. This is a gross
tracked deletion count, not a claim that the whole W9 legacy surface is gone.
