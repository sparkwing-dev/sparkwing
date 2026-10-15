# W1 store handoff

This session continues commit c8481cf9c. Its changes are uncommitted.
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
