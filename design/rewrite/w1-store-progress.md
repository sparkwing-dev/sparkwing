# Store rewrite handoff

This session continues commit b18cc139f. Changes are uncommitted.
The earlier admission, plan acceptance, matching, claims, reconciliation,
materialization, publication, owner renewal and controller recovery work remains.
W1b implementation files were not edited. No Git writes, candidate installation,
stateful Sparkwing command or pipeline ran.

## Implemented

FinishRunAndUnit now refuses a nonterminal verdict before opening a transaction.
A new test reproduced the previous failure: passing RunStart closed the trigger
while leaving its run running. The terminal guard closes that path.

The new finish tests also prove that the run verdict, event, completion outbox
and owner closure commit together. A database error on either the owner update
or outbox insert rolls back the operation. An identical retry commits once and
then replays without another lifecycle event.

Owner recovery tests establish these store behaviors:

- A renewed owner lease keeps a local run live without jobs or job activity.
  The original lease expiry cannot reap the renewed owner.
- Expiry of the renewed lease fails a started local owner.
- Fresh job heartbeats cannot substitute for the owner's expired lease.
  Pending jobs are cancelled once and the run records agent_lost.
- The tenant-scoped owner reaper case uses the suite's selected database engine.
  SQLite passed. PostgreSQL remains unexecuted.

## Deleted and ported

Removed the heartbeat-based orphan sweep and its direct status writers:
reconcileOrphanedLocalRuns, orphanedRunsQuery and cascadeOrphanedNodes.
Removed reapStalePendingRuns and both Maintenance registrations.
These complete store implementation blocks total 154 lines.

Removed the old orphan HTTP handler, client RPC, orchestrator adapter,
read-triggered reconciliation hook, daemon startup call, CLI read calls and
cron inspection call. The old /api/v1/maintenance/reconcile-orphans route is gone.
The separate C2 operator maintenance route remains a contract declaration.
The API snapshots, OpenAPI, generated API reference and source/mirrored
API prose were updated for these cuts. The store snapshot also drops the four
sweep registrations removed in the previous session.

The complete Go diff deletes 327 implementation lines and 757 test lines.
These counts include edited call sites and guard cleanup. New tests are in
owner_liveness_test.go, owner_dialect_test.go and finish_unit_runtime_test.go.

Ported the surviving concerns from these old cases:

- The eight internal/orchestrator/orphan_reconcile_test.go cases now use owner
  lease liveness, local failure and pending-job cancellation through ReapUnits.
  A recent job heartbeat no longer proves owner liveness.
- TestPostgresReconcileOrphanedLocalRuns moves to
  TestUnitReaperUsesTenantOwnerLease, which selects SQLite or PostgreSQL through
  the existing storetest fixture. The obsolete MAX/GREATEST query pin is removed
  with the query.
- The tenant-wide orphan sweep concern already has its final proof in
  TestOwnerReaperRecoversEveryTeamWithoutFailingRemoteRuns.
- The stale-pending repair/grace cases are replaced by the atomic
  FinishRunAndUnit guarantees. The final writer cannot close an owner while
  starting its run, and failed persistence cannot strand half of a finish.

Tests for the removed hook and RPC were deleted. Their helper used by prom
checks moved to prom_test.go. Controller route coverage also exposed one stale
entry for the previously removed auto-retry reset endpoint. That entry is gone.

## Verification

All Go commands used GOWORK=off GOFLAGS=-buildvcs=false. GOCACHE, GOTMPDIR and
TMPDIR were under /tmp/sw-astra-W1/. Test parallelism was at most -p 4.
The worker scratch directory is deleted after verification.

- Full repository go build -p 4 ./... passed after the final source changes.
- go vet -p 4 passed for pkg/store, pkg/controller, pkg/controller/client,
  internal/orchestrator, pkg/localws, cmd/sparkwing and internal/apiroutes.
- The W1 store selection passed 161 top-level tests, 474 including subtests,
  without skips. Its runtime was 28.164 seconds.
- Tests compile for internal/orchestrator, pkg/controller, pkg/controller/client,
  pkg/localws and cmd/sparkwing with -run '^$'.
- Tenant route ownership, complete controller-route classification and duplicate
  read-route checks passed after removing the stale auto-retry map entry.
- Full pkg/match and internal/apiroutes tests passed.
- Acceptance catalog, port inventory/destinations and final credential route
  declaration checks passed. These are catalog checks, not runtime cells.
- TestPkgDocsContentMatchesDocsRoot passed. internal/apispec and internal/apiref
  output exactly matched api/openapi.yaml and docs/api-reference.md.
- git diff --check passed. Git's local config has no user.name, user.email or
  core.worktree overrides.

To reproduce the W1 selection, extract top-level Test names from these files
under pkg/store, join them into an anchored -run expression, and run
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
    owner_liveness_test.go owner_dialect_test.go finish_unit_runtime_test.go

The full legacy store suite was not run. It retains fixtures for removed schema,
including triggers.pipeline and sparkwing_meta. Public API snapshots still have
pre-existing drift from the frozen declarations and earlier rewrite work.
Only the removed surfaces were adjusted here. No full snapshot gate is claimed.
PostgreSQL, race instrumentation, native processes, cluster execution, providers,
performance and Sparkwing pipelines remain unverified. The lead owns pipeline
execution and independent review.

## Acceptance and remaining work

No runtime acceptance cell was activated. internal/acceptance.New and Run still
panic. The native catalog requires real process recovery. Store fakes prove the
store portions of recovery-local-orphans and atomic lifecycle completion.

Real integration points remain:

- W6 must implement the real seals, canonical hashes and settings resolvers.
  W4 must supply the plan validator. Their declarations still panic here.
  The store tests fake these contract boundaries.
- W2/W3/W8/W9 must wire intake policy, enrolled profiles, secret opening,
  lifecycle routes and renewal. The elected controller must drive ReapUnits,
  ReconcileControllerRuns and completion outbox delivery. The removed legacy
  orphan callers do not perform those jobs. Local recovery needs that controller
  loop, independently of reads and CLI commands.
- W1b must connect metered reservation/settlement to award, renewal, result and
  plan-end transactions, and supply materializationRecords and nodeResultRecords
  inside state transactions. This session proves no blob, log or billing
  integration.
- Broad legacy removal remains: StateStoreOld, old plan/claim paths, trigger
  expiry, approval timeout and claim-loss sweep APIs still have callers.
  RecoverExpiredNodeClaims, ReapExpiredNodeClaims and ReapExpiredTriggers retain
  W1b test callers. RunReaperLeased remains in W1b's shared storage-lease file.
  Keep those files outside W1 edits while their callers are migrated.

No new contract change is requested in another area. Existing questions remain:

- C2 local credential lifetime without an owner deadline, and lost-response
  recovery for a Here credential whose raw bearer is not stored.
- C2 publication expansion authority without an executable owner, plus imported
  output-attempt identity across W1b/W7's upload contract.
- W9 reuse source representation for MaterializeSource.Result, which names a
  committed terminal result and state sequence.

## Shared files

Production caller removals outside pkg/store:

- cmd/sparkwing/crons.go
- internal/orchestrator/backends.go, hosted_retry.go, jobs_cli.go,
  orphan_reconcile.go (deleted), wingd_daemon.go
- pkg/controller/client/coordination.go
- pkg/controller/server.go, maintenance.go (deleted), tenant_routes.go
- pkg/localws/localws.go

Other shared files:

- internal/apiroutes/apiroutes_test.go
- internal/orchestrator/backends_coordination_test.go,
  orphan_reconcile_test.go (deleted), wingd_api_routes_test.go
- pkg/controller/capabilities_test.go (deleted), chunked_body_test.go,
  client/coordination_test.go, prom_test.go, doc.go
- api/openapi.yaml, docs/api.md, docs/api-reference.md and their pkg/docs/mirror
  copies
- .apidiff/pkg_store.txt, .apidiff/pkg_controller.txt,
  .apidiff/pkg_controller_client.txt
- This handoff

Shared store files are store.go, maintenance.go, run_state.go and the existing
scope guard and old tests listed by the diff. W1b accounting, blob and log source
files are untouched.

## Friction and assumptions

The integration stubs and old-schema fixtures limit the evidence. The first
route check failed on the obsolete auto-retry entry and passed after its
removal. The finish guard test failed before the implementation change and
passed afterward. No new service, interface, setting, retry loop or environment
variable was added. The hard cut uses the existing elected-reaper contract.

STATUS: CONTINUE
