# W1 store handoff

This session continues cf58676f6. Its changes are uncommitted. W1b's
accounting, billing, blob and log implementation and tests are untouched.
The lead owns committing, independent review and delivery pipelines.

## Dispatch snapshot hard cut

Deleted NodeDispatch, MaxNodeDispatchEnvelope, WriteNodeDispatch,
GetNodeDispatch and ListNodeDispatches from Store, Tenant, the legacy host
interface and the controller client. The final schema already has no
node_dispatches table. Account-deletion inventory no longer names it.

Removed the controller's three snapshot routes, handlers and environment
response filter. Removed the local host wrappers and both daemon route
classifications. Deleted debug rerun, its registry/help entry, shell/pod
implementation and tests. Removed the unused profile rerun_image field.
This is the feature deletion required by frozen architecture C15 and section 9.

The legacy plan byte ceiling remains 4 MiB. MaxPlanBytes now owns that
literal and the plan-snapshot handler/test use it directly. This removes its
accidental dependency on the deleted dispatch-envelope constant.

Deleted 819 production Go lines and 622 test lines. Eleven replacement
lines leave 1,430 fewer Go lines. These counts exclude docs and snapshots.
No new setting, interface, service, wrapper or retry mechanism was added.

Updated source prose, CLI help/reference, API source/reference, scoped API
snapshot entries and documentation mirrors. Historical migration and released
changelog entries retain their account of the old feature. CLI references
were generated through a temporary Go test overlay calling the real renderer,
then compared with that renderer. No Sparkwing CLI was needed for regeneration.
The API generator and bin/sync-docs.sh regenerated their own artifacts.

## Verification

Go commands use GOWORK=off GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-astra-W1/cache and GOTMPDIR/TMPDIR=/tmp/sw-astra-W1/tmp.
Test parallelism is at most -p 4. The scratch directory is deleted at handoff.

- Whole-repository go build -p 4 ./... passes.
- Scoped go vet -p 4 passes for pkg/store, pkg/storage, pkg/controller,
  pkg/controller/client, internal/orchestrator, internal/profile,
  internal/userconfig and cmd/sparkwing.
- The established W1 selection below passes 171 top-level tests and 499
  cases including subtests, with no skips or failures.
- All TestEngine controller tests and tenant-route checks pass.
- Controller, CLI, orchestrator and storage test packages compile with -run '^$'.
- Default-team caller guard passes after removing the deleted callers.
- TestEveryControllerRouteIsClassified and
  TestReadRouteListCarriesNoDuplicates pass after removing both snapshot
  reads from the local controller's route list.
- Selected CLI registry, dispatch, help, command-index and flag checks pass.
  Every CLI reference page exactly matches the temporary renderer check.
- internal/apispec checks pass, including deterministic generation, route
  coverage and committed Go-type schemas.
- Acceptance catalog, port inventory/destination, final credential-route
  declarations and invariant coverage checks pass. They execute no runtime cell.
- Full internal/profile and internal/userconfig tests pass.
- Documentation mirror and git diff --check pass. No repository-local
  user.name, user.email or core.worktree override is present.

No native acceptance cell was activated and no archived test was ported in
this session. Dispatch snapshots and debug rerun are explicit feature cuts.
The final admission, plan, claim, reconciliation and recovery package proofs
remain passing. T1 New and Run still panic, so native cells cannot pass yet.
The legacy full suite, PostgreSQL, race checks, lint, pipelines and independent
review remain lead checks. Full API snapshots have pre-existing rewrite drift.
Only this cut's entries were updated.

## Contract changes needed and remaining integration

No other area's contract changed. Existing gaps remain:

- W9 ReuseSource cannot carry MaterializeSource.Result by job/state sequence.
- W9 ScheduleSnapshot cannot carry the sealed GuardRefusal.
- W2 Authorized/IntakeForStore cannot carry the verified App delivery identity.
- EngineController.Claim still lacks trusted enrollment-capacity and signed
  runner-policy dependencies in its adapter contract. ClaimProfileResolver
  and engine.Resolver.Index/EvaluateRunner are the owning contracts. Add an
  injection point in the controller contract rather than trusting body labels
  or turning reported availability into registered total capacity.

W4/W6 must replace plan-validation and sealing/settings fakes. W2/W3/W8/W9
must connect final routes, hosts, runners and keepers. Controller loops must
call ReapUnits, ReconcileControllerRuns and completion delivery under their
leases. W1b must join metered reservation/settlement to award, renewal, result
and owner completion, and implement transactional materializationRecords and
nodeResultRecords. Package tests fake those record boundaries and do not prove
blob persistence, byte integrity or billing integration.

StateStoreOld and old plan/claim/trigger APIs still have legacy callers.
W1b billing tests also retain sweep/requeue callers, and RunReaperLeased lives
in W1b's storage-lease file. Those files remain with their owner. W1 is not
complete until these callers migrate, old paths disappear and native cells run.
No new product or architecture question was introduced by this deletion.

## Shared files touched

Production outside pkg/store:

- cmd/sparkwing/commands.go, debug.go and help_registry.go.
- cmd/sparkwing/debug_rerun.go (deleted).
- internal/orchestrator/backends.go and wingd_api.go.
- internal/profile/profile.go (unused rerun_image field only).
- pkg/controller/handlers.go, server.go and tenant_routes.go.
- pkg/controller/client/dispatches.go (deleted) and client/doc.go.
- pkg/storage/state.go and doc.go.

Shared tests:

- cmd/sparkwing/debug_rerun_test.go (deleted).
- internal/orchestrator/wingd_api_routes_test.go.
- pkg/controller/dispatch_env_scope_test.go (deleted).
- pkg/controller/node_claim_ownership_test.go, node_client_contract_test.go
  and plan_snapshot_limit_test.go.
- pkg/controller/client/unsupported_test.go.

Shared docs/artifacts:

- .apidiff/pkg_store.txt, pkg_storage.txt and pkg_controller_client.txt.
- CHANGELOG.md and api/openapi.yaml.
- docs/api-reference.md, auth.md, cli-debug.md, cli-runs.md,
  local-execution.md and machine-config.md, plus their generated mirrors.
- pkg/docs/changelog.md and this handoff.

## Friction

An overbroad -run '^TestWingd.*Route' selector included
TestWingdAPI_UnknownRouteAnswersUnsupportedWithNoStore. It attempted to start
a native daemon and failed at the sandbox's Unix-socket restriction, then
reported daemon did not stop. The process sweep found no scratch-owned child
left running. That native test is not a passing proof and was not retried.
The corrected in-process route tests initially caught two stale read entries,
which were removed from wingd_api.go before their successful rerun.

Bootstrap discovery invoked the installed sparkwing info --for-agent before
the swarm brief's no-binaries rule was read. Apart from that discovery and
the unintended native-test attempt, no Sparkwing executable was invoked.
No candidate installation, pipeline or Git write was performed.

The first temporary CLI-doc renderer omitted the generator's path sort. It
was corrected to the same ordering before comparison, eliminating unrelated
reference-page churn. The overlay is scratch only.

## Prior committed handoff

The following report is preserved as historical context. Its then-uncommitted
work is now in cf58676f6. The dispatch-snapshot cut above supersedes its
remaining-work description where applicable.

This session continues f5e05c1b8. Its changes are uncommitted. W1b's
accounting, blob and log files were not edited. The lead owns committing,
independent review and the full delivery checks.

## Owner recovery fix

A stale reaper candidate could reopen a completed planner after another pass
recovered it and its successor accepted the plan. The candidate scan excluded
accepted controller runs, but the per-unit transaction did not repeat that
check. It wrote the planner back to pending and emitted run_requeued after
ownership had passed to controller.owner.

reapOwnerUnitTx now checks plan_accepted_generation under the run lock before
recovery. It leaves an accepted controller plan alone with a live, lapsed or
missing controller lease. A carried retry spec still does not count as acceptance.

TestOwnerReaperRechecksControllerHandoff captures an expired planner candidate,
recovers it with ReapUnits, awards and exchanges its successor with ClaimUnit
and ExchangeCapability, then accepts its plan with AcceptPlan. It executes the
saved candidate's transaction afterward and checks that the owner unit remains
done and the complete run projection is unchanged. The test reproduced the
pending planner and extra requeue before the fix. Its three lease cases pass.
The test schedules committed phases directly without sleeps or timing guesses.
It exercises SQLite transactions, not concurrent PostgreSQL lock acquisition.

The plan validator and input/settings services are the existing contract fakes.
The test uses the real capability, claim, acceptance and recovery transactions.
It makes no billing, blob-integrity or native-process claim.

## Bounce hard cut

Removed RequestNodeBounce, PendingNodeBounce, ConsumeNodeBounce and
ListNodeBounces from both Store and Tenant. Deleted NodeBounce, its outcomes,
errors and SQL scanner. The final schema already has no node_bounces table.
The shared terminal-run helper moved to store.go because retained legacy
claim and accounting readers still use it.

Removed the controller handlers, client methods, three route registrations,
route classifications, CLI command/help, host polling layer and hosted-run
injection. Account deletion and its fixture no longer name the missing table.
The nodehost debug pause behavior remains in place.

Deleted bounce-specific tests and removed their cases from mixed tests. Kept
metric isolation, event-sequence concurrency and unrelated authorization cases.
No archived test was ported in this session because bounce is an explicit
feature deletion. Existing application-retry, lease-loss, disruption-limit and
cancellation proofs continue to pass through the final APIs.

The Go diff removes 690 production lines and 679 test lines, adding 24
production lines and 80 test lines. That is 1,265 fewer Go lines. These counts
include the recovery fix and exclude documentation, snapshots and this report.

Updated the source docs, CLI reference, OpenAPI source, API reference, scoped
API-snapshot entries and generated documentation mirrors. A pre-existing
AnnotateOld spelling in CLI help prevented exact reference regeneration. The
help now names the final Annotate API, matching the existing reference.

## Verification

All Go commands use GOWORK=off GOFLAGS=-buildvcs=false. GOCACHE is
/tmp/sw-astra-W1/cache and GOTMPDIR/TMPDIR is /tmp/sw-astra-W1/tmp.
Test parallelism is at most -p 4. The scratch directory is deleted at handoff.

- Full repository go build -p 4 ./... passes.
- The W1 selection documented below passes 171 top-level tests, 499 including
  subtests, with no failures or skips. It includes the new handoff test.
- go vet -p 4 passes for pkg/store, pkg/controller, pkg/controller/client,
  internal/nodehost, internal/orchestrator and cmd/sparkwing.
- Tests compile for pkg/controller, cmd/sparkwing, internal/nodehost and
  internal/orchestrator with -run '^$'. Their full legacy suites are not claimed.
- All TestEngine tests and the controller route-classification checks pass.
- The default-team caller guard passes after removing the retired CLI caller.
- The selected CLI help/registration/honesty checks pass. A temporary Go test
  overlay compares every CLI reference page with splitCommandsMarkdown and
  passes. It calls the renderer directly and runs no Sparkwing executable.
- The complete internal/apispec suite passes, including committed Go-type,
  route inventory and deterministic-generation checks.
- Acceptance catalog, port inventory/destination, credential-route declaration
  and invariant coverage checks pass. These execute no runtime acceptance cell.
- Documentation mirror and whitespace checks pass. The repository-local Git
  configuration has no user.name, user.email or core.worktree override.

The full API snapshot already differs from the rewrite contracts in this
branch. Only the removed bounce entries were changed. Full snapshot regeneration,
PostgreSQL, race tests, lint, pipelines and independent review remain lead checks.
SPARKWING_TEST_PG_URL is unset. No candidate runtime or pipeline was run.
No Git write was performed.

## Contract changes needed and remaining integration

No other area's contract was changed. The previous controller-adapter gaps
remain: W9 ReuseSource cannot carry MaterializeSource.Result, W9 ScheduleSnapshot
cannot carry GuardRefusal, and W2 Authorized/IntakeForStore has no verified App
delivery identity. Do not substitute body labels for those authenticated facts.
EngineController.Claim still needs the signed runner-policy and enrolled-capacity
adapters. Snapshot remains blocked on its declared wire gap. RunDetail and
Compatibility remain W9 implementation work.

W4/W6 must replace the plan/sealing/settings fakes. W2/W3/W8/W9 must wire the
final authorization, lifecycle and keeper routes. The controller must run
ReapUnits, ReconcileControllerRuns and completion delivery under their leases.
W1b must join metered award/renewal/result/plan-end settlement and implement
materializationRecords and nodeResultRecords inside the state transactions.

Legacy StateStoreOld and old plan/claim/trigger APIs still have callers. Some
remaining sweep/requeue APIs have W1b billing-test callers, and RunReaperLeased
lives in W1b's storage-lease file. Those files were left to their owner.
The earlier report below preserves the specific runtime boundaries and ports.

No native acceptance cell was activated. internal/acceptance.New and Run still
panic. T1's native driver and the host/runner integrations are required before
those cells can truthfully pass. W1 remains incomplete until the remaining
legacy callers migrate and the acceptance cells execute.

## Shared files touched

Production caller changes outside pkg/store:

- cmd/sparkwing/commands.go, help_registry.go and main.go.
- cmd/sparkwing/runs_bounce.go (deleted).
- internal/nodehost/debug.go and scheduler.go.
- internal/orchestrator/hosted_run.go and run_node_verb.go.
- pkg/controller/auth.go, server.go and tenant_routes.go.
- pkg/controller/bounce.go and client/bounce.go (deleted).

Shared test changes:

- cmd/sparkwing/help_test.go and runs_bounce_test.go (deleted).
- internal/nodehost/debug_test.go.
- internal/orchestrator/wingd_api_routes_test.go.
- pkg/controller/bounce_test.go (deleted), node_claim_ownership_test.go and
  team_boundary_test.go.

Shared docs and artifacts:

- .apidiff/pkg_store.txt and .apidiff/pkg_controller_client.txt.
- CHANGELOG.md and api/openapi.yaml.
- docs/api-reference.md, auth.md, cli-runs.md, cli.md and local-execution.md.
- Corresponding pkg/docs/mirror files and pkg/docs/changelog.md, generated
  through bin/sync-docs.sh.
- This handoff.

## Friction and assumptions

Removing the hosted bounce adapter exposed an unused client import, which was
removed before the successful repository build. The first CLI renderer check
found the unrelated AnnotateOld help spelling. Its one-word correction closes
that drift without publishing a scaffolding API name. The full API-snapshot
drift predates this session and was left outside this cut.

The recovery regression exposed a missing transaction recheck, fixed in nine
production lines. No new setting, flag, interface, runtime service or retry loop
was added. The temporary renderer overlay is verification scratch only.

## Previous controller-adapter handoff

The following records the work committed in f5e05c1b8. Its remaining contract
boundaries still apply, except that the bounce surface described above is gone.

## Controller adapters

Implemented eleven declared entry points in pkg/controller/engine_contract.go:
IntakeForStore, CreateRun, AcceptPlan, ExpansionForStore, AcceptExpansion,
ReconcileJob, Renew, TransitionJob, Finish, Cancel and Retry. They use the
final Tenant and capability-derived Store APIs. The HTTP router is still
the other area's integration work.

The adapters retain tagged session authority. They derive actors and fences
from C2 Authorized values, and the store rechecks them in each transaction.
Plan acceptance loads the persisted request seal rather than accepting one
from the body. This preserves retries whose carried seal names the original
invocation. Restricted checkout declarations pass through unchanged.

Expansion retains the source generation separately from its evaluator lease
generation. Structured failures pass apierr.DecodeInner before conversion.
The conversion tests fake that decoder because its C2 implementation still
panics. The real success adapter handles an explicit empty jobs array.

Reconciliation returns the committed self-claim bearer only in the private
claim response. Its binding comes from the persisted capability. Execution
settings come from the returned node, and accepted inputs come from the run.
Public job detail omits the bearer. A competing live claim returns WaitHolder.
A finished job returns KeepTerminal without another lifecycle event.

Job results preserve authorized output and manifest record keys, retry time,
exit code and masked error. Run finish uses FinishRunAndUnit for an owner and
TransitionRun with RunPublish for a publisher. Cancel stays a cooperative
request. Retry sends the source ID and idempotency key to AdmitRun, which
loads the source seals and records the retrier.

## Removed APIs

Deleted Store.ClaimAttemptOrdinal and Tenant.OwnedRunIDs, plus their API
snapshot entries. They had no Go callers or tests. The first queried removed
attempts_consumed and claim_generation columns. Final hosts read NodeRow's
persisted attempt ordinal.

The two source deletions remove 40 Go lines, including the unused import and
spacing. Eleven adapter panic lines were replaced by implementations. Broader
legacy claim and plan removal remains dependent on callers, including W1b's
billing tests. Those files were left to W1b.

## Package proofs and verification

Nine new top-level controller tests pass, eleven including subtests. They
cover session/team identity, persisted request seals, retry seal identity,
expansion fences and decoder refusal, record-key and retry conversion,
publisher versus owner finish, and private bearer delivery.

SQLite integration exercises a real award, renewal, job start, result and
exact replay. A second integration walks owner award and execution start,
self-claim, WaitHolder, executor start/report, terminal adoption and run finish.
An injected owner-write failure rolls back the finish. The retry commits the
run and owner unit together, and an exact repeat replays. Cancellation tests
prove that membership removal after route authorization refuses the write.
Live cancellation writes one event and one due GitHub effect revision.

The profile resolver is a fake. The run/spec/plan fixtures are seeded final
schema rows because W4/W6 validation, sealing and resolution remain stubs.
These tests prove adapter/store transactions. They execute no SDK process,
HTTP authorization middleware or blob persistence. No additional parked-source
function was ported in this session. The new integration extends the existing
package proofs for lifecycle replay, terminal adoption and reconciliation.

Final checks use GOWORK=off GOFLAGS=-buildvcs=false, with GOCACHE and
GOTMPDIR/TMPDIR under /tmp/sw-astra-W1. Go test uses -p 4.

- Full repository go build -p 4 ./... passes after the API removals.
- go vet -p 4 ./pkg/store ./pkg/controller passes.
- The established W1 selection below passes 170 top-level tests, 495 including
  subtests, with no skips. The new controller selection is -run '^TestEngine'.
- Acceptance catalog, port inventory/destination, credential-route declaration
  and invariant-coverage checks pass. These checks execute no runtime cell.
- git diff --check passes. The repository-local Git configuration has no
  user.name, user.email or core.worktree override.

PostgreSQL is unverified because SPARKWING_TEST_PG_URL is unset. The full
legacy suite, race suite, lint, Sparkwing pipelines and independent review
remain the lead's checks. The scratch directory is removed before handoff.
No Sparkwing executable or Git write was run.

## Contract changes needed and remaining integration

These other-area contracts were left unchanged:

- W9's controllerwire.ReuseSource cannot represent C1 MaterializeSource.Result
  by job and state sequence. The adapter supports the declared attempt/record
  variant. Chained materialized-result reuse still needs that wire change.
- W9's controllerwire.ScheduleSnapshot has no GuardRefusal. Returning a
  scheduling graph without the sealed refusal would change its verdict.
  EngineController.Snapshot remains unimplemented until that field exists.
- W2's Authorized/IntakeForStore signature carries no verified App delivery
  identifier. App-specific intake must supply the authenticated delivery ID
  to the store. A client-supplied origin or git label cannot replace it.

Real integration also remains for C2 route authorization and DecodeInner,
W4 ValidatePlan, W6 sealing/settings, and W1b metered reservation/settlement
and result-record attachment. EngineController.Claim still needs W8's signed
runner policy and enrolled capacity. RunDetail and Compatibility remain W9
work. The controller route registration must call these adapters and run
ReapUnits, ReconcileControllerRuns and completion delivery under their leases.

No native acceptance cell was activated. internal/acceptance.New and Run
still panic. The native harness and host/runner implementations are required
before a cross-placement cell can be claimed as passing.

## Shared files and friction

The shared production file is pkg/controller/engine_contract.go. New controller
files are engine_store.go, engine_store_test.go and engine_store_runtime_test.go.
Other edits are pkg/store/claim_run.go, pkg/store/tenant_boundary.go,
.apidiff/pkg_store.txt and this handoff. There were no callers to migrate for
the two removed APIs.

The first integration fixture expected cancellation to finish immediately.
The final store contract keeps it request-only until owner/controller draining.
The fixtures now assert that behavior, include an explicit owner deadline for
awards, and supply every column required by the sealed-schema constraint.

The existing EngineController contract is now backed by adapter functions.
No exported contract shape, flag, setting, table, runtime service or retry loop
was added. The private decoder parameter lets tests fake C2's declared boundary.

## Previous recovery handoff

The following records the prior session based on c8481cf9c, now committed in
1b3588e78. Its tests and unresolved broader-store work remain relevant.
The earlier admission, AcceptPlan, matching, claims, reconciliation,
materialization, publication, owner renewal and controller recovery work remains.
W1b accounting, blob and log implementation files were not edited.
No Git write, Sparkwing executable, candidate installation or pipeline ran.

## Implemented

Approval timeout resolution records the validated reaper lease holder in
approvals.approver. Previously checkApprovalEdgeTx used Actor.Authority.ID,
which is empty for the elected reaper. The audit assertion failed with an empty
resolver before the four-line fix. It passes afterward. A second test proves
that a supplied authority ID cannot impersonate the timeout resolver.

The new approval tests use the final APIs and sealed execution settings. They
prove exact deadline behavior, approve/deny/fail/default policies, indefinite
waits, preservation of an existing decision, one winner between a human and the
reaper, and one terminal state sequence. Database failure at the approval,
node, event or completion-outbox write rolls back the timeout. A retry succeeds.

New owner tests award, start, lose and reclaim the same run through ClaimUnit,
RenewUnit and ReapUnits. Generation advances. The lost host cannot finish or
release the successor's claim. FinishRunAndUnit closes the successor atomically,
and the completed run cannot be claimed or reaped again. A bounded sweep also
recovers unstarted work across teams without consuming a disruption.

The node recovery tests use the suite's selected database dialect and the final
schema. They prove a bounded two-unit pass continues on the next call. Started
loss records an agent_lost attempt and preserves generation, ordinal and the
application retry budget. It requeues on the first loss and fails at disruption
three. Claims are cleared in both cases.

## Deleted and ported

Removed these legacy paths:

- Store.ListExpiredClaims and Store.FinishLapsedClaim.
- The local consumer's requeueExpiredClaims and claimOwner functions,
  inFlightSet.has, NoStaleClaimSweep option and maintain field.
- Maintenance.ReapTimedOutApprovals, reapTimedOutApprovals,
  expiredControllerApprovals, timeOutControllerApprovals and their private
  timeout-only branch in resolveApprovalTx.
- Maintenance.FailExpiredNodeClaims and failExpiredNodeClaims.

The four sweep implementation files delete 279 Go lines and add 16, for 263
net lines removed. Obsolete test blocks delete 672 lines. These counts exclude
new tests, source documentation, the audit fix and scope-guard entries.

Port mapping:

| Previous cases | Final proof |
| --- | --- |
| reap_approvals_test.go's three cases | TestApprovalReaperUsesDeadlineAndPolicyOnce and TestApprovalReaperLeavesIndefiniteAndResolvedGates |
| TestApproval_ResolveAndTimeoutHaveOneWinner | TestApprovalResolutionAndReaperHaveOneWinner |
| TestApproval_TimeoutHonoursOnTimeout | TestApprovalReaperUsesDeadlineAndPolicyOnce, including dependent release or cancellation |
| The lapsed-lease team case and unstarted consumer recovery | TestOwnerReaperLimitRecoversUnstartedWorkAcrossTeams and the existing unstarted-owner tests |
| Expired consumer claims with a running or in-flight process | Existing owner-lease liveness tests and TestOwnerReclaimFencesLostHostAndKeepsTerminalAbsorbing. A live owner lease is the liveness proof. A started local owner whose lease expires fails under the final policy. |
| Consumer repair of a claim after its run finished | Existing FinishRunAndUnit atomicity/rollback/replay tests plus the new reclaim-and-finish test. Final state is absorbing, so the historical redispatch interpretation is removed. |
| TestFailExpiredNodeClaims_TerminatesWithAgentLost and TestRunnerObservesExpiredClaimFailure | TestUnitReaperPreservesAgentLostAttempt. Immediate failure on the first lapse is replaced by same-run requeue and the three-disruption cap. Native runner observation still needs W8 integration. |
| TestExpiredClaimReaper_RecoversABoundedBatchPerPass | TestUnitReaperBoundsNodeRecovery. The final contract accepts a caller-selected bound rather than a fixed 500-run cap. |
| TestLockOrder_PostgresReaperTakesTheRunBeforeTheNode | TestPostgresUnitReaperLocksRunBeforeNode. The test observes a real blocked PostgreSQL transaction and checks the node remains lockable. It was not executed against PostgreSQL here. |

The deleted test files are reap_approvals_test.go, lock_order_postgres_test.go
and internal/orchestrator/lapsed_lease_team_test.go. Unrelated tests in edited
files remain. The PostgreSQL test now joins and cancels its recovery goroutine
on every exit. No new test uses a sleep.

Store package documentation names the single schema, SchemaTables and elected
recovery. The API snapshot drops the four removed entries. Historical changelog
and migration prose remains history.

## Verification

All Go commands used GOWORK=off GOFLAGS=-buildvcs=false, GOCACHE under
/tmp/sw-astra-W1/cache and GOTMPDIR/TMPDIR under /tmp/sw-astra-W1/tmp.
Test parallelism was at most -p 4. The scratch directory is deleted at the end.

- Full repository go build -p 4 ./... passed after the source cuts and audit fix.
- The full W1 store selection passed 170 top-level tests, 495 with subtests,
  without skips. The final run took 11.452 seconds.
- go vet -p 4 passed for pkg/store, internal/orchestrator and
  internal/runners/warmpool.
- The internal/orchestrator and internal/runners/warmpool tests compiled with
  -run '^$'. Their full legacy runtime suites were not run.
- The PostgreSQL lock-order test was selected explicitly. It skipped because
  SPARKWING_TEST_PG_URL is unset. PostgreSQL behavior remains unverified.
- Acceptance catalog, port inventory/destination and credential-route catalog
  checks passed. These checks execute no runtime acceptance cell.
- git diff --check passed. Git's repository-local config has no user.name,
  user.email or core.worktree override.

To reproduce the W1 selection, extract top-level Test names from these files
under pkg/store, join them into an anchored -run expression, then run
`go test -p 4 ./pkg/store -count=1 -run "$expression"`:

    authority_runtime_test.go authority_adapters_test.go rewrite_authority_test.go
    rewrite_schema_test.go state_runtime_test.go scheduling_contract_test.go
    scheduling_guard_test.go concurrency_adoption_test.go maintenance_rewrite_test.go
    expansion_runtime_test.go expansion_success_test.go owner_reaper_runtime_test.go
    unit_deadlines_test.go unit_release_test.go result_runtime_test.go
    intake_plan_runtime_test.go unit_claim_test.go unit_claim_lock_test.go
    reconcile_runtime_test.go materialize_runtime_test.go state_result_records_test.go
    retry_recovery_runtime_test.go publication_runtime_test.go unit_renewal_test.go
    queue_recovery_test.go tenant_sql_scope_guard_test.go controller_recovery_test.go
    owner_liveness_test.go owner_dialect_test.go finish_unit_runtime_test.go
    approval_reaper_runtime_test.go owner_reclaim_runtime_test.go
    node_reaper_dialect_test.go

The separate PostgreSQL test is TestPostgresUnitReaperLocksRunBeforeNode in
unit_reaper_postgres_test.go. node_reaper_dialect_test.go and
owner_dialect_test.go run through storetest.Open, so the lead can also run
those tests with SPARKWING_TEST_STORE=postgres and a configured server.

The full legacy store suite retains removed-schema fixtures, including
triggers.pipeline and sparkwing_meta. It was not run. Public API snapshots
retain existing drift from earlier rewrite work. Only the four removed entries
were adjusted here. No full snapshot gate is claimed. Race instrumentation,
native SDK/runner processes, cluster execution, providers, performance and
Sparkwing pipelines remain unverified. The lead owns independent review and
pipeline execution.

## Acceptance and remaining work

No native runtime acceptance cell was activated. internal/acceptance.New and
Run still panic. Focused store proofs support the approval, owner recovery,
reconciliation and started/unstarted loss scenarios. They do not replace T1's
native process targets or prove runner event handling.

Real integration points remain:

- W6 must implement seals, canonical hashes and settings resolution. W4 must
  implement ValidatePlan. Store tests continue to fake those contract services.
  The new approval fixture explicitly supplies the sealed ApprovalWait winner.
- W2/W3/W8/W9 must wire intake policy, enrolled profiles, secret opening,
  lifecycle routes and renewal. The elected controller must call ReapUnits,
  ReconcileControllerRuns and completion delivery. The local consumer no
  longer runs a parallel claim sweep. Local recovery must be driven by the
  controller independently of CLI reads or consumer in-flight bookkeeping.
- W8's final runner must observe same-run requeue/reclaim and the terminal
  disruption cap through the new state/event contracts. The removed warm-pool
  test covered the obsolete first-lapse-fails path, not that integration.
- W1b must connect metered reservation/settlement to award, renewal, result and
  plan-end transactions and supply materializationRecords and nodeResultRecords
  within the state transactions. No new billing, blob or log integration is
  claimed in this session.
- Broad legacy removal remains: StateStoreOld, old plan/claim paths and trigger
  expiry still have callers. RecoverExpiredNodeClaims, ReapExpiredNodeClaims
  and ReapExpiredTriggers retain W1b test callers. RunReaperLeased remains in
  W1b's shared storage-lease file. Their accounting and persistence files remain
  outside this worker's edits while those callers migrate.

No new contract change is needed in another area. The reaper resolver fix
implements the existing resolver-audit invariant. Existing contract questions
remain: C2 local credential lifetime without a deadline, lost-response recovery
for Here issuance without storing raw bearers, publication expansion authority,
imported output-attempt identity across W1b/W7, and W9 representation of
MaterializeSource.Result. No new setting, flag, interface or retry loop was added.

## Shared files

Changes outside pkg/store:

- internal/orchestrator/trigger_consumer.go
- internal/orchestrator/trigger_consumer_test.go
- internal/orchestrator/lapsed_lease_repro_test.go
- internal/orchestrator/lapsed_lease_team_test.go (deleted)
- internal/runners/warmpool/warmpool_test.go
- .apidiff/pkg_store.txt
- This handoff

The only production caller edit outside pkg/store is trigger_consumer.go.
The warm-pool edit removes its obsolete caller test and unused import.

## Friction

The generic fake settings resolver did not populate per-job ApprovalWait, so
the first timeout tests correctly saw an indefinite wait. The new fixture now
supplies that sealed value through the existing interface. The resolver-audit
test then reproduced the production defect and passed after its fix.

Removing legacy tests exposed unused imports, which were removed. One initial
compile overlapped a test-file deletion and had to be rerun after edits stopped.
The final checks ran with no concurrent source mutation. Existing schema-fixture
and API-snapshot drift still prevents a full legacy-suite claim.

STATUS: CONTINUE
