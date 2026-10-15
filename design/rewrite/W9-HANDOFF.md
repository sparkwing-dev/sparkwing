# W9 continuation handoff

W9 remains incomplete. This session continues committed base `6ffb10ca4`.
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

## Completed in the preceding session

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

## Completed in this continuation

- Run listing uses `RunReader` for local and remote connections. Every filter
  reaches the controller before pagination. JSON emits compact selection rows
  plus explicit continuation, and plain emits IDs. Failure groups use jobs or
  job/step identities. Pipeline totals consume every matching page.
- List follow drains all newer pages, keeps the initial number boundary and
  deduplicates emitted IDs. A run that fails after a later run remains visible.
  Wait/follow interruptions during either a request or a poll return exit 3.
- Prune uses `RunPruner`. Age selection finishes every page before the first
  delete, excludes live runs and preserves structured refusals. Dry-run never
  deletes. No local store handle or hard-coded 10,000-row selection remains.
- Logs and grep use `LogReader`. Tagged job attempts and trigger/plan/eval
  subjects survive output, pagination and search. Follow waits for persisted
  final logs and drains truncated pages without sleeping. Both trigger follow
  callers use the same typed transport. No implicit tail limit was introduced.
- Annotations and approvals use `RunReview`. Annotation levels and source
  locations survive reads. Approval actors come from authentication, and
  decisions retain idempotency keys and replay results. Local SQLite handlers,
  environment-derived actors and the replaced pflag parsers are deleted.
- The existing day/week duration parser moved into clisurface. The final grammar
  and old duration callers now agree on values such as 7d. Debug parser tests now
  use the final --job selector.
- C3 records these W9-owned read and mutation shapes. Command references,
  observability guidance and embedded mirrors describe the new adapters.

The continuation removes 2,290 tracked Go lines, including obsolete list/group
handlers, prune, local annotation/approval handlers, grep clients and their old
fixtures. This is a gross deletion count, not net size or complete legacy removal.
New contract clients and behavioral tests account for the added code.

## Work still required

1. Migrate retry and statistics/sizing, plus the remaining management handlers
   and old typed clients. Retry --failed/--all-jobs still needs the W1/W2 contract
   decision recorded below. Other handlers retain old pflag execution paths.
2. Wire W3 run hosting and W6 sealed requests/settings. Publication, cache-from,
   target selection, here-resume and pipeline-ref selection remain unfinished.
   Detached execution still launches the removed consumer entrypoint and must
   move to the local controller queue. Then delete the consumer and its helpers.
3. Compose W8 lifecycle/runtime/managed units/logs. Finish hidden wingd arguments,
   update refresh/restart, doctor integration, schedules selectors/pause/resume
   and stable timer startup through __tick.
4. Implement pools, cache warm, deployment tokens and remaining management reads
   and writes. Team-member credentials, reserved pools and restricted secrets
   need the authoritative typed clients.
5. Replace profile.Load/Save with W6 final machine parsing/persistence and W2
   credential metadata. Shared connection selection needs the final W6 resolver.
6. Finish source-guide and executable-caller migration. Older guides, scripts,
   examples and runtime tests still contain removed vocabulary and old schemas.
7. Run real HTTP/api.sock, daemon/service, Windows, engine-upgrade and complete
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

## Verification for this continuation

All Go commands used GOWORK=off and GOFLAGS=-buildvcs=false, with GOCACHE,
GOTMPDIR and TMPDIR under /tmp/sw-astra-W9/main. Parallelism was at most -p 4.

Passed after the source changes:

- Whole-repository `go build -p 4 ./...`.
- Vet for cmd/sparkwing, clisurface, controllerwire, controller/client,
  orchestrator and tests/acceptance.
- Full clisurface, controllerwire and tests/acceptance package tests. Product
  scenario skeletons remain skipped; that package result is not runtime proof.
- Focused listing, prune, logs/search, annotations/approvals, query transport,
  engine client, status, stdin-ID and final debug-parser tests.
- Focused orchestrator date/duration regressions after extracting the parser.
- Generated command-reference and embedded-doc/changelog drift checks.
- Default-team caller inventory and git diff --check.

The new supplemental acceptance test is TestSurfaceRunQueryAndPruneTransport.
The previous grammar, detail and observation transport slices still pass. No
full W9 product acceptance cell was unskipped. Full CLI/API parity, runtime
lifecycle, version-policy and real log completion remain open.

Ported regressions cover repository/root filtering, filtered pages, trimmed SHA,
quiet output, grouping, missing versus empty observations, annotation ordering
and locations, logs bounds/search/follow, and approval decisions. New cases cover
bursts across pages, delayed mutable-filter matches, interrupted controller reads,
repeated pagination, prune selection failure and preserved structured errors.
No source test in the T1 parked-test inventory is assigned directly to W9.

An independent read-only review found the delayed-match follow bug and the wait
request exit-code mismatch. Both were fixed, covered by regression tests and
rechecked. Code volume review retained the new wire interfaces because the CLI
must use controller-owned state; no new package, service, env variable or retry
framework was added.

A broad test expression also selected legacy orchestrator tests
TestRunStatus_ResolvesAStandaloneRun, TestRunStatus_ReadsThroughProfileNotLocalStore
and TestRunStatus_FailedRunKeepsItsStatus. They fail on the missing old
sparkwing_meta schema and are not reported as passing. Earlier broad selections
also hit old debug --node fixtures and a legacy CLI JobLogs fixture. The debug
fixtures were migrated and the replaced JobLogs test was ported and deleted.

Real error responses still depend on W2's apierr.DecodeInner stub. Client error
regressions inject a decoder fake, as the earlier engine tests do. Production
clients call the shared decoder. W7's storage and final-segment completeness
implementation also remain real-integration requirements. Display-log validation
checks tagged wire shapes without treating those checks as storage authority.

The installed sparkwing info command ran once for initial repository orientation
before the swarm brief was loaded. No candidate executable, daemon or pipeline
was run. No git writes were made. The effective Git identity remains Korey
Gambill and core.worktree is unset. Scratch and the temporary docs-generation
test were removed at handoff.

## Additional contract changes needed

- W2 must implement the query/deletion/log/review projections documented in C3
  and its shared error decoder. These are W9-owned wire additions on existing
  routes; no C2 route or grant changed.
- The frozen annotations add grammar permits a run-scoped write, but C2 only
  supplies job/step writes with NodeReport or publisher authority. W2 needs a
  run-scoped route and a decision for ordinary member annotation grants. The
  adapter returns PolicyMissing for the missing run-scoped route and preserves
  authorization refusals for existing routes. This is unfinished support, not
  an approved customer restriction.
- W7 must supply persisted tagged logs, cursor continuity and final completeness.

## Shared files touched in this continuation

- internal/orchestrator/runs_filter.go and runs_filter_test.go: moved duration
  parsing to clisurface and migrated the remaining callers/tests.
- internal/orchestrator/runs_grep.go and runs_grep_test.go: deleted after the CLI
  became their last production caller. No interleaved W3 log backend was changed.
- pkg/store/default_team_callers_test.go: removed five deleted caller entries.
- tests/acceptance/surface_contract_test.go: added query/delete transport coverage.
- pkg/docs/mirror/cli-runs.md, mirror/observability.md and changelog.md: regenerated
  embedded copies of their source documentation.
- design/rewrite/contracts/c3-controller-wire.md, this handoff and CHANGELOG.md.

Within W9, pipeline_trigger.go and pipeline_trigger_here.go now use the typed log
follower. Legacy Client annotation/approval APIs remain required by the old
storage.StateStoreOld interface and W3 callers; deleting them needs that migration.

Friction: concurrent source edits briefly interrupted shared-tree compilation.
Final checks ran after the workers finished. Socket-based integrations remain
outside the swarm sandbox. No unresolved test failure was hidden as a pass.
