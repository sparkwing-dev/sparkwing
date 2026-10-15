# W1 store handoff

Continues 52f7be2d3. These changes are uncommitted. The lead owns committing,
independent review and delivery pipelines. W1b's accounting, billing, blob and
log files are untouched.

## Implemented and deleted

Removed coordinator pins and the executor avoid window from the legacy node
model, insertion, scanning, queue filters, award predicates, offer finalization,
attention and recovery SQL. Removed their projection scrubbing and fixtures.
The final schema already excludes those columns. No schema change was needed.
Deleted the eleven-line unkeyed Node compatibility test.

Runner observations now persist in capabilities. RecordAgentLabels checks live
ancestry and current machine claim grants inside its write transaction. It
records last_used_at in nanoseconds. ListRegisteredAgents uses a consistent
snapshot, excludes ended ancestry and lost claim grants, and reports malformed
authority or label bytes as errors. It combines ClaimProfileResolver enrollment
with the same granted identity helper used by claims. RegisteredAgent now names
CapabilityPrefix, with its controller consumer migrated.

ListWaitingNodes reads sealed execution settings, request runner selection,
repository display and allowed pools from the final schema. It pages ready
host-owned work and excludes withdrawn, claimed, cancelled, published and terminal
work. SetNodeAttention refuses stale updates after withdrawal, claim, cancellation
or completion. WaitingNode.AllowedPools reaches the controller's advisory matcher.
Online presence updates labels and platform while preserving enrolled capacity,
repository acceptance, location and granted pools.

SelfAsserted now removes pool labels alongside the other granted keys. Evaluate
uses that filter instead of separately filtering pools. Eligibility is unchanged.
Saved observations and diagnostics no longer retain forged pool identity.

Production Go adds 202 lines and deletes 168. Test Go adds 376 and deletes 139.
Those counts include new test files and exclude Markdown and OpenAPI.
No interface, setting, flag, table, environment variable, runtime service,
compatibility alias or retry mechanism was added. The shared private profile
helper keeps advisory and transactional identity construction together.

## Tests and ports

TestRequeuedJobRemainsEligibleForEveryMatchingRunner proves that a started
disruption requeues within the same run, remains visible to attention, and can
be claimed immediately by the prior runner or another matching runner. It covers
named and queued claims, the next generation, unchanged application retry budget
and exactly one requeue/release/claim event sequence. This is the package proof
for the archived coordinator-pin test's replacement. Its native acceptance
destination remains unexecuted.

Five legacy test functions were ported to final-schema fixtures and deleted:

| Removed function | Final proof |
| --- | --- |
| TestRegisteredAgentsCarryTheirLastLabels | TestRegisteredAgentsUseLiveCapabilityIdentityAndSavedLabels |
| TestRegisteredAgentsKeepAnExpiringTokenAndItsLastUse | The same test retains a future expiry and checks exact nanosecond last use. |
| TestWaitingNodesPage | TestWaitingNodesReadSealedDemandAndPageReadyWork |
| TestSetNodeAttentionSkipsANodeRevokedSinceTheRead | TestNodeAttentionUpdatesOnlyWaitingJobsInTheNamedTeam |
| TestWaitingNodesSkipAControllerDispatchedNode | TestWaitingNodesExcludeWithdrawnAndUnclaimableWork |

Additional tests cover revoked/expired ancestry, narrowed parent grants,
nonclaimants, corrupt metadata, enrollment identity, request runner selection,
sealed resources and pool allowances. A controller regression proves online
observations cannot replace enrollment. Before the fixes, the new tests reproduced
missing tokens/trigger columns and the controller's profile overwrite.

## Verification

Go commands used GOWORK=off GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-astra-W1/cache, GOTMPDIR=/tmp/sw-astra-W1/tmp and
TMPDIR=/tmp/sw-astra-W1/tmp. Test parallelism was at most -p 4.

- Whole-repository go build -p 4 ./... passes.
- go vet -p 4 ./pkg/store ./pkg/controller ./pkg/match ./internal/api passes.
- The W1 selection passes 187 top-level tests and 558 cases, with no skips or
  failures, in 19.573 seconds.
- All TestEngine controller adapters and the new advisory-profile test pass.
  The claim-response projection and Cloud attention tests also pass.
- Full pkg/match, internal/api and internal/apispec tests pass.
- Acceptance catalog, documentation, inventory and invariant checks pass.
  They establish no runtime acceptance cell.
- bin/check-api-spec.sh and git diff --check pass. New Go files are formatted.

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
    node_reaper_dialect_test.go recovery_matching_test.go registered_agents_test.go
    waiting_nodes_test.go

The full legacy suite, PostgreSQL, races, lint, pipelines and independent review
remain lead checks. The legacy suite still has callers of removed schema.
API snapshot generation into scratch succeeds, but the checked-in baseline
predates the rewrite in eight packages, including 840 additions and 51 deletions
for pkg/store. The lead must regenerate aggregate snapshots with the merged
rewrite. No generated baseline was hand-edited. The OpenAPI source and its owning
drift check are updated. The aggregate rewrite changelog remains lead work.

## Remaining W1 work and integration contracts

W1 remains incomplete. No native acceptance cell was activated.
internal/acceptance.New and Run still panic. T1 requires the process driver and
independent assertions before removing a skeleton skip.

Local C1 contract changes are documented in c1-data.md: capability-based advisory
discovery, RegisteredAgent.CapabilityPrefix and WaitingNode.AllowedPools. No
other area's contract was changed. Existing cross-area gaps remain:

- W9 ReuseSource cannot carry MaterializeSource.Result by job/state sequence.
- W9 ScheduleSnapshot cannot carry the sealed GuardRefusal.
- W2 Authorized/IntakeForStore cannot carry verified App delivery identity.
  IntakePolicy remains the tested server-authority boundary.
- EngineController.Claim still needs trusted enrollment-capacity and signed
  runner-policy dependencies. ClaimProfileResolver and engine.Resolver's
  Index/EvaluateRunner are the owning contracts. Advisory enrollment uses a fake
  resolver here. It does not establish production registration transport.

Real integration remains for W4/W6 validation, settings and seals, W2/W3/W8/W9
routes, hosts, runners and keepers, and controller loops invoking ReapUnits,
ReconcileControllerRuns and completion delivery under singleton leases.
W1b must join metered reservation and settlement to award, renewal, result and
owner completion, and implement materializationRecords and nodeResultRecords
within their calling transactions. Those boundaries remain tested with fakes.

Legacy StateStoreOld, plan/claim/trigger writers and offer rounds still have
runtime callers. Offer-round removal reaches warmpool's active fallback path.
W1b's old billing tests still use sweep/requeue, and RunReaperLeased remains in
its storage-lease file. None was replaced with a compatibility path. Removing
these APIs and migrating their callers is still W1 work.

## Shared files and friction

Outside pkg/store, these files changed:

- internal/api/node.go and node_test.go remove deleted node fields.
- pkg/controller/node_projection_test.go and node_claim_ownership_test.go remove
  deleted node fields and SQL fixture columns.
- pkg/controller/claim_attention.go migrates CapabilityPrefix, passes allowed
  pools and preserves enrollment. claim_attention_profiles_test.go tests it.
- pkg/match/match.go, contract.go and match_test.go share granted-key filtering.
- api/openapi.yaml removes required_executor_location.
- design/rewrite/contracts/c1-data.md and this handoff record the local contract
  and verification boundaries.

The first full W1 run found the stale unscoped-SQL exemption for RecordAgentLabels.
Its write now includes team. Removing the exemption made the fresh selection pass.
The first recovery fixture lacked the owner deadline needed by an independently
awarded runner. The fixture now supplies it.

The initial mandatory sparkwing info discovery ran before the swarm prohibition
was loaded. No runtime pipeline, candidate, provider call or socket-dependent
test followed. No Git writes were made. Git has no local user.name, user.email
or core.worktree override. Scratch is removed before the final report.
