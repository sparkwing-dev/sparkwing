# W1 store handoff

Continues e4ca5877f. The current changes are uncommitted. The lead owns
committing, independent review and delivery pipelines. W1b's accounting,
billing, blob and log files are untouched. No Git write or Sparkwing executable
was run.

## Removed implementation

Deleted the store's unused assisted execution-policy writer and its policy
carrier, seal aliases, restoration and validation helpers. The only writer
had test callers. Its eight SQL columns are already absent from the final
schema. Legacy node insertion, reads and claims no longer reference those
columns. No schema change was needed.

Removed the nodeRecord wrapper. The remaining legacy readers scan Node
directly. The legacy UpdateNodeDeps refusal now uses the existing ErrLockHeld
when the dependency write cannot affect its unready node. The dead
triggerRequeuedSQL helper is also deleted.

The production Go diff deletes 193 lines and adds 16, reducing production by
177 lines. Test Go deletes 454 lines and adds 129. Those counts exclude this
handoff. Deleted files are execution_policy.go (66 lines) and
execution_policy_store_internal_test.go (314 lines).

Final acceptance already owns the second seal and accepted job settings.
TestAcceptedPlanAndSnapshotReturnIndependentValues ports the old SQL
immutable-copy check to AcceptPlan, ReadSnapshot and the final schema. It
mutates returned arguments, settings and plan/job bytes, then checks the
persisted request, spec and declarations are unchanged. Existing seal-refusal
and replay tests cover atomic refusal and differing seals. The obsolete
assisted-body tuple and its tampering tests were deleted with that model.

No new interface, setting, flag, table, environment variable, runtime service,
retry mechanism or compatibility API was added. No exported API changed.

## Matcher test ports

Six tests from t1-port-inventory.md's agent_match_test.go rows have final-store
package proofs. Their old bodies and compatibility comments are deleted.
Native acceptance destinations remain unchanged and unexecuted.

| Archived test | Final package proof |
| --- | --- |
| TestQueueClaimNeverTakesANodeLargerThanTheRunnersCapacity | TestClaimUnitNamedAndQueuedUseCapacityAndIdentity checks two-core refusal, busy refusal and an eight-core award. |
| TestQueueClaimBindsNameToTheTokenPrincipal | TestClaimUnitGrantedProfileKeys checks forged name labels and a correctly named capability. |
| TestNamedClaimAppliesTheClaimantsProfile | TestClaimUnitRepositoryEnrollmentAndMeteringDoNotGrantPlacement checks repository refusal and permitted enrollment. TestClaimUnitGrantedProfileKeys checks required labels. |
| TestNamedClaimOfAMeteredAgentObeysItsAcceptList | The repository test refuses both metered and unmetered roots before any billing award. |
| TestCloudPlacementMatchesThePreChangeBehavior | TestClaimUnitGrantedProfileKeys replaces the old location/metering exception with class=cloud from a granted launcher. A metered machine asserting that label is refused. |
| TestNamedClaimFromADispatcherThatSendsNoLabels | The profile-key test refuses a missing gpu requirement and awards when it is present. Old-dispatcher compatibility framing is deleted. |

The new profile and repository cases run through both named and queued
ClaimUnit. Rejections leave billing_awards empty. These are real SQLite
transactions with persisted capability envelopes and a fake enrollment
resolver. They do not establish enrollment transport or metered settlement.

The two legacy machine-name uniqueness/rotation tests and seedMatchNode helper
remain in agent_match_test.go. The helper still serves claim_attention_test.go.
Machine provisioning still refuses unavailable registration authority in
provisionCapabilityTx. This session did not invent registration or rotation
contracts to replace those tests.

## Verification

All Go commands used GOWORK=off GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-astra-W1/cache, GOTMPDIR=/tmp/sw-astra-W1/tmp and
TMPDIR=/tmp/sw-astra-W1/tmp. Test parallelism was at most -p 4.
The scratch directory is deleted before the final report.

- Whole-repository go build -p 4 ./... passes.
- go vet -p 4 ./pkg/store ./pkg/controller passes.
- W1 selection: 176 top-level tests and 515 cases pass, including subtests,
  with no skips or failures. The run took 10.887 seconds.
- All TestEngine controller adapter tests pass.
- Acceptance catalog, documentation, port destinations, port inventory and
  invariant-coverage guard tests pass. They execute no runtime acceptance cell.
- git diff --check passes. Git has no local user.name, user.email or
  core.worktree override.

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

The build and scoped vet cover production changes. Package tests cover final
state and authority invariants. The full legacy suite, PostgreSQL, races,
lint, pipelines and independent review remain lead checks. The previous
session reproduced a legacy admission failure on removed sparkwing_meta.
This session did not rerun or claim a pass for that legacy suite.
SPARKWING_TEST_PG_URL is unset. The source documentation and public API shape
are unchanged, so no changelog, mirror or API snapshot edit was needed.

## Remaining W1 work and other-area contracts

W1 remains incomplete. No native acceptance cell was activated.
internal/acceptance.New and Run still panic. T1 requires a process driver and
independent assertions before a skeleton skip can be removed. The brief also
prohibits this worker from running Sparkwing binaries.

Unchanged contract gaps:

- W9 ReuseSource cannot carry MaterializeSource.Result by job/state sequence.
- W9 ScheduleSnapshot cannot carry the sealed GuardRefusal.
- W2 Authorized/IntakeForStore cannot carry verified App delivery identity.
  Store tests use the IntakePolicy contract for that proof.
- EngineController.Claim still needs trusted enrollment-capacity and signed
  runner-policy dependencies. ClaimProfileResolver and engine.Resolver's
  Index/EvaluateRunner are the owning interfaces. Body labels and reported
  availability cannot supply that authority.

Real integration remains for W4/W6 plan validation, settings and seals;
W2/W3/W8/W9 routes, hosts, runners and keepers; and controller loops invoking
ReapUnits, ReconcileControllerRuns and completion delivery under their singleton
leases. W1b must join metered reservation and settlement to award, renewal,
result and owner completion, and implement materializationRecords and
nodeResultRecords within their calling transactions. Those remain contract
interfaces tested with fakes here.

Legacy StateStoreOld, plan/claim/trigger writers, offer rounds, coordinator
pins and avoid-window recovery still have old runtime callers. Offer-round
removal reaches internal/runners/warmpool's active fallback path, so this
session did not replace it with another temporary implementation. W1b's old
billing tests still use sweep/requeue, and RunReaperLeased remains in its
storage-lease file. Those files were left untouched.

No new cross-area contract change was made or requested. The list above
carries forward unresolved integration contracts from the previous session.

## Files and friction

All production and test edits are in pkg/store. The only shared file changed
is this handoff. No caller outside the package required migration because the
removed writer and helpers had no production consumers.

The first focused test compile was started before a source deletion and then
failed because execution_policy.go disappeared from its file list. A fresh
run passed after the edit completed. No product assertion failed in that run.
Subsequent final-contract tests passed. No socket-dependent test, provider
call, candidate runtime or Sparkwing pipeline was attempted.
