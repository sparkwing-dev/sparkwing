# W1 continuation 19

W1 remains incomplete. This continuation removes legacy plan acceptance and
ports its retained checks to the final interfaces. The lead owns Git writes,
independent review and pipelines.

## Implemented and deleted

`Tenant.AcceptPlan` is the sole accepting store API. `Store.AcceptPlan`, the
separate submitted-plan decoder, validator, node inserter and resource clamps
are removed. `Client.SubmitPlan`, the controller's claim-token acceptance
branch and the old `planLaunchedRun` caller are removed with them. The legacy
snapshot route remains pending its lifecycle migration.

Required-secret preflight now returns canonical `plan_invalid`. Its detailed
diagnostic survives the failed acceptance for run status. The diagnostic
transaction checks current owner authority, locks the tenant's run, records
the detail once with the store clock and rechecks authority before commit.
The original refusal survives a diagnostic-storage error. Refused acceptance
commits neither a seal nor jobs.

The legacy planner's file contained 568 lines. Its remaining bootstrap and
constants live in plan_bootstrap.go because current callers still use them.
CreatePlanNode and the internal repository-dispatch bootstrap remain. The
legacy claim reader retains only the six modifier fields it reads. It
contains no accepting validator.

Across the final Go diff, 674 existing source lines and 1,592 existing test
lines are removed. Added source and acceptance helpers total 248 lines, and
added test code totals 283 lines. The net Go reduction is **1,735 lines**.
These counts include file moves and the new acceptance proof. They exclude
Markdown and generated documentation mirrors.

The removed fixture is dispatchRun. Its obsolete plan, settle, auto-retry and
claim tests are removed or reduced. RetryAuto, silent clamps and v1 plan
fields are removed behavior under the frozen design. The port inventory records
the retained concerns and the launcher integration that remains open.

## Tests and acceptance

The new passing cell is
`TestLifecycle/lifecycle-request-and-plan-seals/store-sqlite`. It exercises real
admission, owner claiming, shared strict validation, shared settings resolution
and sealing. It refuses malformed declarations, fails the second job insert
to prove full rollback, reopens SQLite, replays the accepted body and refuses
changed arguments with spec_drift. Replay and refusal preserve the persisted
snapshot and event count. Intake policy and enrollment are contract fakes.

Retained ports include:

- AcceptPlan insertion, one-plan replay and invalid-input rollback now run
  through final seal tests and the SQLite cell.
- SecretPreflightNamesEveryUnreadableSecret and the optional-secret case now
  run through TestPlanSecretPreflightNamesUnreadableScopes. It checks all
  unreadable scopes, the persisted detail, the canonical error and successful
  acceptance of the readable required subset.
- Negative source depth and invalid resources run through
  TestAcceptedPlanUsesSharedStrictValidator. Shared settings tests retain
  winner and ceiling checks.
- The two ReportAttempt output tests now use W1b's result-key forgery property
  and TestResultRecordsFromAnEarlierAttemptCannotFinishARetry. Earlier output
  cannot finish either the old ordinal or the advanced retry ordinal.
- Child invocation and live-parent checks now use explicit admission keys in
  TestChildIntakePersistsLineageAndInvocationReplay and
  TestChildIntakeCannotEscapeItsLiveParentProperty.
- Existing scheduling, cancellation, ordinal, recovery, source-release and
  unit-billing tests retain dependency, holder, lease and payment concerns.

Three generated properties cover the security boundaries. Registered secrets
cannot enter accepted job, step or group identifiers or their refusal text.
The identifier property covers short, escaped and Unicode values. Child intake
cannot leave its live parent run, job, team or generation. Preflight diagnostics
cannot commit after their plan owner's deadline. No test sleeps were added.

No additional archived T1 source identity is claimed closed. The existing
TestRecoveryDependencies port remains. HTTP, native placements, providers and
PostgreSQL remain separate evidence.

## Verification

All Go commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4 and caches,
temporary files and test scratch beneath /tmp/sw-astra-W1. Tests use -p 4.

- Whole-repository `go build -p 4 ./...` passes.
- `go vet -p 4` passes for internal/store/..., internal/acceptance/...,
  tests/acceptance/..., internal/controller/... and internal/orchestrator/....
- The final W1 selection passes **398 top-level tests / 1,093 cases**, with
  no skips or failures. Select Test names from the files in
  w1-session19-tests.txt, omit TestPostgres names and run their anchored
  alternation against ./internal/store.
- internal/acceptance and tests/acceptance with `-skip '^TestProtocol$'`
  pass **19 top-level tests / 200 cases**, with **2,422 native/platform skips**.
  Protocol process coverage is not claimed.
- Required commentcheck against main passes across 1,057 files. Sleepcheck
  against HEAD passes.
- The embedded docs/changelog guards, migration-list/read checks,
  sync-docs --check and git diff --check pass.

The preflight diagnostic test ran red before its acceptance join. Its store
clock assertion also ran red before the diagnostic writer moved off the old
AppendEventOnce path. The final tests pass both checks.

No full legacy suite, PostgreSQL, race, performance, native placement, provider,
candidate, pipeline or release pass is claimed. Git configuration still names
Korey and contains no core.worktree override. No Git write or Sparkwing binary
was used. Scratch is deleted after the final checks.

## Amendment and handoff audit

| Entries | State and remaining integration |
| --- | --- |
| A1-07, A1-11, A1-44, A1-45 | Prior sealing, settings, source and classification implementation remains. The new cell uses the real shared validator and resolver. Trusted production intake and checkout providers remain their owners' integrations. |
| A1-08, A1-18, A1-55 | Ordered matching, ancestry, ordinals and here reclaim remain. W9/W3 still need SourceAttempt transport and consumption. The W1b result fixture now supplies the source ordinal. |
| A1-13 through A1-17 | Reconciliation, failure, cancellation and expansion persistence remain. The previously recorded W9 failure-stage, guard-refusal, materialized-result and generation-zero expansion gaps remain. W1b owns record-copy and attachment implementation. |
| A1-20, A1-25, A1-26 | Provisioning, plan secret validation and enrollment interfaces remain. This continuation preserves detailed required-secret diagnostics on final acceptance. W2 owns the production origin/profile providers and installation. |
| A1-21 through A1-24 | Session, CSRF, authority, binding and source-release implementation remains. C2 BindingState still lacks cancellation reason. Exact operation/digest transport, concurrency selectors and teamless-session/origin adapters remain foreign work. |
| A1-27 | The prior W1 lifecycle join remains tested. Production C2 SettleBilling is still a stub. W3 must report current owner compute. W1b owns spend-exhaustion/grace policy and legacy credit retirement. |
| A1-28 | Committed-checkout settlement remains. W2 owns its verified provider handoff and old grant-route cut. |
| A1-29, A1-31 through A1-35 | W1b retains blob, upload, log, accounting and retention implementation. Only the authorized test-caller migrations described below touch its files. |
| A1-38 | Safe SyncLauncher remains blocked on W8/W9 immutable award-generation and Kubernetes Job-instance correlation. No foreign wire shape was changed. |
| A1-40, A1-53 | W8/T1/lead retain readiness, HTTP/api.sock, native platform/provider/Postgres and real-repository evidence. |
| A1-51 | Legacy plan acceptance is cut. StateStoreOld, raw lifecycle, readiness, legacy claims/triggers and old HTTP fixtures remain W1 work. |

No new foreign contract change is requested. The cancellation-reason,
source-attempt, execution-projection and launcher-correlation gaps remain as
recorded in earlier reports and handoffs.

The next legacy cut must migrate the StateStoreOld users and claim/trigger
writers. CreatePlanNode still has four fixture callers:
internal/controller/claim_dispatch_internal_test.go,
internal/controller/data_download_claim_provenance_test.go,
internal/controller/log_segments_internal_test.go and
internal/orchestrator/segment_logs_test.go. They have not been converted into
passing final-contract HTTP proofs. Keeping their constructor avoids a new
package compilation failure while that migration remains open.

## Shared files and friction

Shared production caller changes are:

- internal/controller/claim_dispatch.go removes the legacy handler and binding.
- internal/controller/server.go removes that branch from the plan route.
- internal/controller/client/claim.go removes SubmitPlan.
- internal/orchestrator/claim_exec.go removes the old launched planner.

Shared acceptance files are internal/acceptance/store_plan_seals.go and
tests/acceptance/lifecycle_test.go. Shared prose is c1-data.md, handoffs.md,
t1-port-inventory.md, the migration note and changelog with generated mirrors.

The lead's caller-migration exception authorizes both W1b test changes.
internal/store/node_output_test.go loses the two tests tied to dispatchRun and
retains the digest helper its other tests use. One SourceAttempt field is added
to internal/store/blob_result_records_test.go. W1b persistence, billing and log
implementations are unchanged.

Friction came from legacy fixtures still calling CreatePlanNode, missing steps
in the first strict-validator fixture, and raw parent fixtures that had not
advanced next_run_number or cleared terminal claim fields. Those fixture issues
are corrected or explicitly retained for the next migration. No deployed state,
new service, schema, setting or public limit changed.

STATUS: CONTINUE
