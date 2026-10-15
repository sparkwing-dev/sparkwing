# Store rewrite handoff

This session continues commit f55136267. Its changes are uncommitted.
The earlier admission, plan acceptance, claims, matching, reconciliation,
materialization, publication, renewal and reaper implementation remains.
W1b's billing, blob and log implementations were not edited. No git writes
or Sparkwing executables were run.

## Implemented

- Operator.ReconcileControllerRuns(ctx, lease, after, limit) resumes
  settlement after controller election. It pages across tenants by global
  run ID and rechecks the exact live ownership fence in each run transaction.
  Each run commits separately. An error returns the committed prefix.
  The caller starts each new sweep with an empty cursor.
- A report committed during an ownership gap leaves its dependents pending.
  The successor's reconciliation releases them. Expected generator expansions
  create eval units exactly once. Existing indefinite approvals remain open.
  Completion events and outbox revisions commit with the terminal verdict.
  Failed outbox writes roll back the run transition and event.
- The sweep renews live concurrency holders and waiters to the persisted
  controller lease expiry. Expired capacity stays expired.
- A carried retry spec no longer establishes controller ownership. Ownership
  projections, authorization, settlement and reaper selection use the positive
  accepted-plan generation. A retry planner can lose an unstarted lease for
  free, be reclaimed, and hand off the unchanged plan. A started planner fails
  at the disruption limit. The controller cannot release its jobs before
  handoff.
- Added focused proofs for tenant-wide owner recovery and job queue expiry.
  Started local owners fail. Remote owners requeue within the same run.
  Queue expiry preserves live claims and fresh releases and records
  queue_timeout for the expired job.

The local C1 contract additions are ControllerRun and
Operator.ReconcileControllerRuns, also added to OperatorStore.
The handoff fix uses the existing runs.plan_accepted_generation column.
No schema change or other area's contract change was made.

## Deleted and ported

Removed these unused legacy sweep APIs and their maintenance registrations:

- reapStaleRunningRuns / ReapStaleRunningRuns
- reapQueueExpiredRuns / ReapQueueExpiredRuns
- failNodesInRun / FailNodesInRun
- failStaleQueuedNodes / FailStaleQueuedNodes

The complete removed blocks total 219 implementation lines and 550 legacy
test lines. This excludes registration and guard cleanup, imports, replaced
lines and prior sessions' deletions. The removed test file is
pkg/store/reap_running_test.go. Removed cases in claim_attention_test.go
and failure_reason_test.go used the deleted queue sweeper.
reaper_team_test.go retains its orphan-sweep case.

The surviving queue guarantees are covered through ReapUnits in
unit_deadlines_test.go and queue_recovery_test.go.
The owner recovery tests preserve remote work and fail started local orphans.
The old expectation that an unclaimed trigger escapes queue expiry is reversed.
The old expectation that a silent remote run fails is replaced by same-run
owner recovery, bounded by the existing disruption and queue limits.
Prior T1 port coverage remains unchanged.

There were no production callers of the four removed APIs. No caller outside
pkg/store needed migration. Controller fixtures in concurrency_adoption_test.go,
expansion_runtime_test.go and owner_reaper_runtime_test.go now identify a
completed plan handoff.

## Verification

All Go commands used GOWORK=off GOFLAGS=-buildvcs=false, with GOCACHE,
GOTMPDIR and TMPDIR under /tmp/sw-astra-W1/. Test parallelism was at most 4.
The scratch directory is removed after checks.

- Full repository go build -p 4 ./... passed.
- go vet -p 4 passed for pkg/store, pkg/match, pkg/controller,
  internal/nodehost, internal/orchestrator and internal/runners/warmpool.
  Store vet also passed after the last source change.
- The W1 selection passed 156 top-level store tests, 464 including subtests,
  without skips. The final run took 11.38 seconds.
- Full pkg/match tests and acceptance catalog checks passed.
  internal/acceptance has no test files.
- git diff --check passed. Local Git config contains no user identity or
  core.worktree overrides.

To reproduce the store selection, extract top-level Test names from these
files under pkg/store, join them into an anchored -run expression, and use
go test -p 4 ./pkg/store -count=1 with that expression:

    authority_runtime_test.go authority_adapters_test.go rewrite_authority_test.go
    rewrite_schema_test.go state_runtime_test.go scheduling_contract_test.go
    scheduling_guard_test.go concurrency_adoption_test.go maintenance_rewrite_test.go
    expansion_runtime_test.go expansion_success_test.go owner_reaper_runtime_test.go
    unit_deadlines_test.go unit_release_test.go result_runtime_test.go
    intake_plan_runtime_test.go unit_claim_test.go unit_claim_lock_test.go
    reconcile_runtime_test.go materialize_runtime_test.go state_result_records_test.go
    retry_recovery_runtime_test.go publication_runtime_test.go unit_renewal_test.go
    queue_recovery_test.go tenant_sql_scope_guard_test.go controller_recovery_test.go

The full legacy store suite was not run. Prior sessions found fixtures using
removed schema, including triggers.pipeline and sparkwing_meta.
PostgreSQL, race instrumentation, native processes, cluster execution,
provider integrations and Sparkwing pipelines remain unverified.
The lead owns pipeline execution and independent review.

## Acceptance and remaining work

No native acceptance cell was activated. internal/acceptance.New and Run still
panic as unimplemented. The catalog requires real entry points and native
process recovery. Store tests with contract fakes establish only the store
portions of recovery-controller-ownership, lifecycle-retry-sealed-lineage,
recovery-local-orphans and queue/loss recovery.

Real integration points remain:

- W6 must implement the real run seals, canonical hashes and setting resolvers.
  W4 must supply the real plan validator. Their declarations still panic.
  Store tests fake these boundaries.
- W2/W3/W8/W9 must wire intake policy, enrolled profiles, secret opening,
  lifecycle routes and renewal. The controller must acquire and renew
  controller.owner and drive the paged reconciliation adapter.
  Completion outbox delivery still needs its elected controller worker.
- W1b must connect metered reservation and settlement to award, renewal,
  result and plan-end transactions. It must implement materializationRecords
  and nodeResultRecords inside the state transactions. These tests do not
  establish output, manifest, log or billing integration.
- Broad legacy API removal remains. StateStoreOld, old plan/claim paths and
  the old orphan consumer still have callers. RecoverExpiredNodeClaims and
  ReapExpiredNodeClaims retain callers in W1b's tests. RunReaperLeased remains
  in W1b's shared storage-lease file.

Previously reported contract gaps remain for the lead:

- C2 needs credential lifetime for local execution without an owner deadline,
  and lost-response recovery for a Here credential whose raw bearer is not stored.
- C2 needs publication expansion authority because publication has no live
  executable owner. Imported output-attempt identity needs agreement with
  W1b/W7's upload contract.
- W9's reuse source needs the committed-terminal-result/state-sequence
  alternative represented by MaterializeSource.Result.

No new change to another area's contract is requested by this session.

## Shared files and friction

Outside pkg/store, only design/rewrite/contracts/c1-data.md and this handoff
changed. Shared store files changed are store.go for the sweep deletions,
maintenance.go for their registrations and claim_contract.go for the operator
adapter. W1b implementation files are untouched.

Mechanism added: one explicit paged operator adapter. A private settle helper
could not let the elected controller resume committed work, so the public
store boundary is needed. There is no new service, setting or environment
variable. The existing decider, transitions and capacity renewal own the work.

The integration stubs, old-schema legacy fixtures and sandbox's prohibition
on native Sparkwing execution limit the evidence. Focused Go checks work
with the worker-owned cache. No permission or external service was needed.

STATUS: CONTINUE
