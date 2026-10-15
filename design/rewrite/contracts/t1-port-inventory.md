The final unit-billing suite in `pkg/store/unit_billing_test.go` ports the
unfunded-claim, last-reservation race, unstarted-refund and consumed-minimum
assertions from the retained `pkg/store/credits_test.go` tests. It uses final
ClaimUnit, TransitionNode, RenewUnit, ReleaseUnit, FinishRunAndUnit and ReapUnits
with W1b's real ledger helpers and a C2 calculator fake. Two properties cover
stale reports against replacement awards and expiry during issuance. The new
`TestBilling/billing-start-award-versus-start/store-sqlite` cell proves award,
exchange, price and compute-start persistence through reopening. Native billing
and provider cells remain pending. No additional archived source identity below
is claimed closed by these retained-suite ports.

The retained `pkg/store/crons_test.go` suite now uses Tenant for schedule
operations and Operator for tick metadata. Fired history fixtures seed the final
same-team run reference. Non-run entries store NULL. Existing cursor, override,
pause, retention, deduplication and tenant assertions remain active. The SQLite
`surface_test.go:TestSurface/surface-schedules-and-managed-timers/store-sqlite`
cell adds rollback and reopening evidence. Native timer and HTTP cells remain
pending. This adds no archived source-function port identity.

# Acceptance test port inventory

The raw `RequeueUnstartedClaim` suite and its controller/orchestrator callers are
retired. `TestOwnerUnstartedRecoveryFencesPriorAwardsProperty` varies initial
generations, pending/running runs, runner/launcher owners and repeated recovery.
It retains claimant clearing, no execution/disruption consumption, no repeated
refund, stale renewal/release/finish refusal and terminal absorption. Existing
owner-reaper and cancellation tests retain started-local failure, remote recovery,
cancellation precedence, team scope and the elected fence. The impossible
trigger-without-run fixture is removed because final owner rows require a run.
C2's at-award compute keeps its charge. Unstarted pod reservations refund.
The old metered tests that refunded already-started host compute are removed
with their raw API. The final unit-billing suite retains immutable prices,
settlement and generation-specific reservations.

`recovery-unstarted-lapse/store-sqlite` now also uses real intake and claiming
for a trigger owner. It proves three unstarted requeues and reclaims through
SQLite reopening, stable intake identity, stale plan/renewal refusal and final
plan acceptance by the replacement. Intake and enrollment remain contract fakes.
The removed legacy HTTP tests do not count as final HTTP integration evidence.
No additional archived source-function identity is claimed closed by this port.

`TestPlacement/placement-ordered-matching/store-sqlite` adds final-store evidence
for scoped trigger order, job requirements and soft preferences. The claim queue
property generates 32 selectors and checks that failed polls change no row or
event. Push properties vary repository/ref/SHA across named and queued node and
trigger claims, delegated credentials and awarded hosts. Direct issuance also
refuses an out-of-scope award. The old offer-window, idle-registry and concurrent
finalization tests are deleted with the warm dispatcher. Their matching,
tenant-scope and no-fallback concerns move to the final claim proofs. Enrollment
remains a fake and native Actions/launcher transport is still required.

`pkg/store/concurrency_maintenance_fence_test.go` ports expired-holder promotion,
aged-waiter liveness, abandoned-work cleanup and repeated-pass assertions from the
old maintenance suite. The fence property generates forged names, holders and
generations. Rollback tests cover statement failure and lease expiry during the
pass. The SQLite `recovery-concurrency-no-revival/store-sqlite` cell also checks
reopening and returning-owner queue order. Obsolete concurrency-cache sweeps are
removed because blob records own memo retention. Missing-run capacity is prevented
by cascading run foreign keys. The isolated zero-age no-op helper is removed with
the unfenced API. A zero age on the final pass retains its existing default.
`TestLeasedConcurrencyMaintenanceRemovesTerminalWorkInEveryScope` ports the old
local-repair liveness and idempotency assertions. Terminal global holders now
release with local holders. The separate repair-count API is removed.


This repository-owned inventory records every parked source function and its required port. Acceptance destinations name existing skeletons; package destinations name required focused proofs to implement with their owning area. A scenario is complete only when every mapped case is implemented. Product skeletons remain skipped until their required areas exist; a wrong result must fail once activated.

Branch labels below retain the descriptive suffix of each source branch. Tracker prefixes are omitted because repository files cannot contain tracker identifiers. The full Git revision identifies the exact branch snapshot independently of ref movement. Resolve source files with `git show <revision>:<file>`; removed predecessors are present before the branch comparison range. This inventory requires no personal filesystem path or live branch ref.

The primary inventory has 74 changed or added functions: 6 lifecycle, 6 state-machine, 4 placement, 1 credential matrix, 23 run-input, 24 storage, and 10 state/security. Placement also contributes 10 existing functions. Six removed predecessors bring the total to 90 unique revision/file/function identities. Subcases do not increase that count. Source objects were inspected statically; this is not evidence that old or target product tests pass.

## Source snapshots

`pkg/store/download_accounting_runtime_test.go` ports
`TestChargeDownloadHoldsATeamToItsDailyCap` and
`TestChargeDownloadRaceForTheLastBytesAdmitsExactlyOne` from the retired raw
Store method. Final-schema tests preserve cap crossing, delivered-byte counting,
funding changes, operator exemption and UTC-day reset. Independent SQLite
connections race the last capacity and reopen the exhausted budget. A generated
property varies byte counts and hostile replica strings while checking tenant
isolation and conservation. Expired generic download holds release without
double subtraction. Failed counter writes and overflow roll back admission.

Event accounting ports the five charged-storage tests and seven event-cap tests
from `pkg/store/storage_test.go` and `event_limits_test.go` to
`event_accounting_runtime_test.go` and `event_policy_runtime_test.go`.
The no-quota assertion now requires additive usage because final counters record
all tenant writes. The ports retain byte/month/run/count caps, kind validation,
kind byte cost, concurrent admission, uncharged event counters and failed-write
rollback. They add shared upload capacity, reopening, month rollover, funding,
free share and a generated tenant-isolation/conservation property.
The replica SQLite acceptance cell now races the real event writer and verifies
its remaining capacity after reopening. No native source row is marked complete.

The existing `storage-replica-safe-accounting/store-data-sqlite` cell now includes
real tenant download charges, their last-capacity race and exhausted-budget
reopening. The controller proof exercises delivered-byte accounting, trusted
replica identity, tenant isolation and log/live-log exemptions without a socket.
No new native acceptance cell or archived source row is marked complete.

`pkg/store/storage_standing_runtime_test.go` ports retained event-byte release
after deletion and the funded/free/held storage-standing matrix to final rows.
It checks unique blob/log inventory, commit/abort/expiry holds, corrupt totals
and reopening. `FuzzStorageStandingTenantInventory` generates own and foreign
inventory sizes and replica identities, with stale raw counters present. Reports
must stay tenant-scoped and leave accounting unchanged. The controller proof uses
the same final tenant snapshot. The existing upload crash/replay SQLite cell now
checks live holds before commit and retained bytes after reopening. The raw
standing/usage reads, event-byte repair API and duplicate event cache are removed.
No archived source row or native acceptance cell is newly closed.

The unused unleased `Store.ReleaseExpiredStorage` scan and its two legacy tests
are removed. `TestBlobUploadExpiredReservationFreesCapacity` ports zero-byte
expiry and capacity release to final tickets. Empty uploads reserve object
capacity, expired tickets refuse late commits, and aborting an expired ticket
cannot release its successor's hold. Committed replay remains covered separately.

The raw paid-credit route and its handler tests are retired.
`pkg/store/credit_grant_runtime_test.go` ports free-grant idempotency, team
scoping, validation, balance/history accounting and payment-linked reversals
to the final schema. Paid fixtures use committed checkout settlement facts.
`payment_refunds_test.go` retains refund replay and ports pre-settlement fraud
warning refusal. Generated properties exercise every raw store grant surface
and the registered retired HTTP route. The existing ledger-retention SQLite
cell also refuses raw paid grants. Native provider and controller adapters
remain open. No archived source row or native acceptance cell is newly closed.

The service-token storage and download counter handlers and their rolling-block
tests are retired. `pkg/controller/store_data_retirement_internal_test.go` adds
`FuzzLegacyStorageCountersAreGone` over generated credentials, tenant strings and
byte counts. Every retired route returns 404. Final ticket tests retain scoped
quota admission, immutable replay, concurrent-client and abort coverage.
The two signup source tests use the final SQLite schema without a listener.
They preserve full-tier and unreadable-source refusal and the server's own gate
projection. Slot policy persistence and sealed-write refusal are covered in
`pkg/store/credit_settings_runtime_test.go`. Native signup and data adapters
remain open. No archived source row or native acceptance cell is newly closed.

The independent bucket-listing storage pass and its tests are retired.
`pkg/controller/storage_inventory_internal_test.go:TestStorageMaintenanceUsesFinalInventoryAndLeavesReplicaUsage`
checks the replacement maintenance path against the final SQLite schema. It
requires a clean maintenance pass, billing from committed blob inventory despite
stale raw counters, and unchanged replica usage. Existing retained-storage and
replica-accounting proofs cover rollback, concurrent clients and reopening.
Listing reconciliation, receipt replay after one-day pruning, and free-share
log deletion tests are deleted with those mechanisms. Allowance-driven physical
eviction and native transport remain open. No archived source row is newly closed.

The unfenced node/trigger reapers and their dispatcher-expiry helpers are removed.
`pkg/controller/reaper_test.go:TestReaperRecoversExpiredOwnerAndKeepsLiveOwner`
replaces its private ticker-based reaper with the real elected controller pass.
It reverses the old assertion that a remote owner loss fails the run.
`pkg/store/reaper_fence_property_test.go:TestReaperFenceProperty` generates forged
holders and generations, exact expiry and takeover. Refusals change no snapshot.
`recovery_test.go:TestRecovery/recovery-local-orphans/store-sqlite` ports local
orphan persistence through repeat passes and reopening. Existing same-run owner,
attempt, cancellation and bounded-reaper tests retain the other recovery assertions.
Native heartbeat and process-termination proofs remain required.
The two legacy `TestLease_Reaper*` tests are replaced by that controller pass and
the final owner/reclaim tests. `TestExpiry_ALapsedClaimRetriesThroughSettle` is
deleted because disruptions no longer consume application retries. Its ordinal,
stale-report and exhaustion checks live in the unstarted/started-lapse SQLite
cells and final unit-reaper tests. The private-pass error-suppression test
`TestExpiry_ADispatchFailureDoesNotBlockTheOrchestratorPass` is deleted with those
passes. `TestOwnerRequeueRollsBackWhenEventCannotCommit` instead requires a loud
error and transactional rollback through the sole reaper.

The SQLite store proofs
`recovery_test.go:TestRecovery/recovery-cancel-beats-loss/store-sqlite` and
`recovery_test.go:TestRecovery/recovery-reconciler-waits-and-reclaims/store-sqlite` port
`TestNodeRules_TheReaperEndsACancelledRunsLostNodeAsCancelled` and both orders of
`TestNodeRules_AnAdoptedRowIsNotFinishedTwice` from `node-state-machine`.
They check the final store transactions, persisted attempts and events, reopening,
and racing reconciliations.
Enrollment is a contract fake. Native owner processes and HTTP remain required
by the existing placement cells.

`recovery_test.go:TestRecovery/recovery-unstarted-lapse/store-sqlite` exercises
five unstarted job lapses, reopening after each requeue. Attempts, disruptions
and application retries stay zero. Each next award advances generation. The
first execution then starts attempt one. Old holders cannot finish the job,
and the five lapses create no sibling run. This adds coverage to the existing
recovery ports without completing the native process cells.

`recovery_test.go:TestRecovery/recovery-started-lapse-cap/store-sqlite` exercises
three started job lapses with reopening after each loss. Generations advance,
attempts retain ordinals one through three, and application retries stay zero.
Each lost attempt closes with agent_lost. The third loss fails the job and
reconciliation keeps it terminal without another claim. The proof checks exact
events and refuses stale reports after every loss. It adds coverage without a
new archived source-function identity or native process evidence.

`recovery_test.go:TestRecovery/recovery-concurrency-continuous-adoption/store-sqlite`
checks node and plan capacity through owner loss, remote renewal, reopening and
successor adoption. Node and run terminal transactions release their own groups.
Each release admits exactly one queued competitor. Enrollment is a contract fake.
Native processes and placement transports remain required.

`recovery_test.go:TestRecovery/recovery-application-retry-ordinal/store-sqlite`
adds persistence evidence for two identical failed attempts followed by success.
It checks the unchanged holder, generation and lease, zero disruptions, distinct
source ordinals, delayed-report refusal, retry replay after reopening, terminal
replay and exactly two retry events. `TestHolderReportsAffectOnlyTheirSourceAttemptProperty`
generates repeated failures, replay counts and invalid source ordinals at the
same store boundary. Native process continuity remains required. The old local
`TestReapExpiredNodeClaims_KeepsTheClaimGeneration` fixture is deleted because
the active unstarted-lapse and reconciler cells already check exact generation
preservation on reaping and advancement on each next award.

`pkg/store/maintenance_identity_test.go` ports the preservation assertions from
`TestPruneSpentIdentityKeepsWhatStillAdmits` to the final leased API. It covers
bounded invitation cleanup, expiry and repeated passes. The generated session
property preserves live sessions and capability-parent rows. Rollback and expired
maintenance fences leave both tables unchanged. Token-table assertions are
removed with that table. Capability expiry remains covered by the final retention
and ancestry tests. The invitation API proof checks nanosecond persistence for
creation, listing, email delivery, acceptance and withdrawal.

`pkg/store/concurrency_admission_runtime_test.go:TestHolderLivenessUsesStoreClock`
replaces `TestConcurrencyGuard_CanonicalSQLSitesOnly` from design-found-hotfixes.
Acquisition uses the injected clock. Expired capacity stops counting, heartbeat
and adoption cannot revive it, and reacquisition creates a new lease. The
96-case `TestConcurrencyExpiredCapacityNeverRevivesProperty` varies lease length
and exact-expiry boundaries. The old source-count assertion is deleted.

`placement_test.go:TestPlacement/placement-granted-identity-and-capacity/store-sqlite`
extends the existing package ports into active SQLite acceptance. It checks
named and queued claims for capacity, authenticated name, self-asserted labels,
repository enrollment, metering and granted pool toleration. Matcher, authority,
claims, events and billing-award persistence are real. Enrollment is a contract
fake. Refusals must come from matching, leave no events or billing awards, and
permit a subsequent claim when the missing requirement is supplied. Metered
success and launcher settlement remain outside this proof.

The archived cases exercised by that cell are
TestQueueClaimNeverTakesANodeLargerThanTheRunnersCapacity,
TestQueueClaimBindsNameToTheTokenPrincipal,
TestNamedClaimAppliesTheClaimantsProfile,
TestNamedClaimOfAMeteredAgentObeysItsAcceptList,
TestNamedClaimFromADispatcherThatSendsNoLabels,
TestPlacementToday_TriggerClaimMatchesNoLabels and
TestPlacementToday_SelfAssertedLabelsSatisfyRequires from the placement snapshot.
The package ports already replaced their old fixtures. This continuation adds
no new archived source-function identity to the inventory.

`credentials_test.go:TestCredentials/credentials-operator-console-split/store-sqlite`
checks trusted bootstrap/service issuance, rejection of console grants and
independent root revocation through the final store API. Origin verification is
a contract fake. It adds a store proof without completing the native route
matrix or changing the archived-test port count.

| Branch label | Exact revision | Comparison |
|---|---|---|
| `node-lifecycle` | `136fbf58144d9bd6c300b0ecc08a9925b468c95e` | This commit's additions |
| `node-state-machine` | `7b1fc6d111c272612d4c970d061063a369598e30` | This commit's additions |
| `placement` | `8f114747eba050739f6ce8b775e6eb84edb2e1b0` | This commit's additions |
| `credential-matrix` | `1e16668363ecbd99ba922436ab1a6f9cae63335b` | This commit's additions |
| `run-input-divergences` | `5ba4cb0ec1ec222defc58f5eaee6ca332a058303` | `073a533c3ef17f63794df99cfabf2e110f740d08..5ba4cb0ec` |
| `storage-hotfixes` | `80ce329157569a0d0d1cf1d3f67e3e06060512ed` | `694dbfcf6d3bead5954c640462f45ecea7ebba43..80ce32915` |
| `design-found-hotfixes` | `c5aa3b88f1b6debf856e887bfc6df0894355b6cd` | `694dbfcf6d3bead5954c640462f45ecea7ebba43..c5aa3b88f` |

The final three comparisons include all changes since the recorded merge base. Earlier branches use the named commit's additions. Helper changes travel with their owning tests. Final architecture contracts supersede old behavior: remove bug-dependent skips, duplicate events, cache-server credentials, arbitrary artifact reads and the here runner's one-claim restriction.

## Function inventory

<!-- port-inventory:begin -->
| Branch label | Revision | Source file | Source function | Cases | Disposition | New home and expected behavior |
|---|---|---|---|---|---|---|
| `node-lifecycle` | `136fbf58144d9bd6c300b0ecc08a9925b468c95e` | `internal/nodehost/lifecycle_test.go` | `TestLifecycleLayersPerPlacement` | `TestLifecycleLayersPerPlacement`, table rows `local`, `run host`, `dispatched`, `claimed`, `claimed retry_auto` | Replace | `internal/nodehost/lifecycle_test.go:TestLayerOwnership` and `protocol_test.go:TestProtocol/protocol-host-layers-risk-cleanup`. One owner/executor layer table governs every placement. Delete the `retry_auto` row and obsolete bounce/workers layers. Approvals remain owner-side. Application retry remains executor-side under the same claim. Proves §3.1-3.3 and §6.6. |
| `node-lifecycle` | `136fbf58144d9bd6c300b0ecc08a9925b468c95e` | `internal/nodehost/lifecycle_test.go` | `TestLifecycleLocalNodeStartsAndEndsOnce` | `TestLifecycleLocalNodeStartsAndEndsOnce` | Keep | `lifecycle_test.go:TestLifecycle/lifecycle-transition-replay-and-fencing/local` and `/retry`. Ordinary and retried jobs have one `job_started` and one terminal event. A retried attempt adds `job_retrying`, retains its claim and generation, and increments the attempt ordinal. The store emits events. Proves §3.3. |
| `node-lifecycle` | `136fbf58144d9bd6c300b0ecc08a9925b468c95e` | `internal/nodehost/lifecycle_test.go` | `TestLifecycleAFailedRecordEndsTheNodeOnce` | `TestLifecycleAFailedRecordEndsTheNodeOnce/refused output`; `TestLifecycleAFailedRecordEndsTheNodeOnce/store refuses the row` | Replace/Reverse | `lifecycle_test.go:TestLifecycle/lifecycle-result-commit-atomicity/refused_output`. An invalid output cannot become a successful result. Use the final 16 MiB cap instead of the fixture's old 64 MiB error. A committed failure has one store event. Proves §3.3, §7.6, §8.1. `lifecycle_test.go:TestLifecycle/lifecycle-result-commit-atomicity/store_unavailable`. Recording failure is loud. Do not retain the host-written `node_failed` event when the store refused the transition. State and lifecycle event commit together or neither commits. Proves §3.3. |
| `node-lifecycle` | `136fbf58144d9bd6c300b0ecc08a9925b468c95e` | `internal/nodehost/lifecycle_test.go` | `TestLifecycleADispatchedNodeEndsOnce` | `TestLifecycleADispatchedNodeEndsOnce/node host wrote the row`; `TestLifecycleADispatchedNodeEndsOnce/a writer that emits no event wrote the row` | Reverse/Replace | `lifecycle_test.go:TestLifecycle/lifecycle-transition-replay-and-fencing/adopt_finished`. Change the expected terminal-event count from two to one. Adoption emits no second event. Proves §3.3/A8. `lifecycle_test.go:TestLifecycle/lifecycle-transition-replay-and-fencing/executor_completion`. Every successful terminal transition emits its event in the store. Remove the run host's fallback event emission. Proves §3.3. |
| `node-lifecycle` | `136fbf58144d9bd6c300b0ecc08a9925b468c95e` | `internal/nodehost/lifecycle_test.go` | `TestLifecycleRunLeaseRejectedAfterRestart` | `TestLifecycleRunLeaseRejectedAfterRestart` | Reverse | `recovery_test.go:TestRecovery/recovery-keeper-causes-and-backoff/rejected_owner`. Rejected reattach cancels the unit with the same cause as rejected node reattach. The old expected successful continuation is forbidden. Proves §3.3/A9. |
| `node-lifecycle` | `136fbf58144d9bd6c300b0ecc08a9925b468c95e` | `internal/nodehost/memo_unix_test.go` | `TestMemo` | `TestMemo/ALeaderEndsOnceAndAHitWithItsCacheHit` | Replace | `storage_test.go:TestStorage/storage-memo-materialization-failures`. Keep one terminal event for the leader. Replace the hit's old `cache_hit`-only lifecycle assertion with one store-owned `job_memoized` transition. Proves §3.3/A25. |
| `node-state-machine` | `7b1fc6d111c272612d4c970d061063a369598e30` | `internal/nodehost/node_end_event_pin_test.go` | `TestDispatchedNodeEndsWithOneEndEvent` | `TestDispatchedNodeEndsWithOneEndEvent` | Keep | `lifecycle_test.go:TestLifecycle/lifecycle-transition-replay-and-fencing/adopt_finished`. One store event, with a failing assertion instead of a defect-dependent skip. Proves §3.3. |
| `node-state-machine` | `7b1fc6d111c272612d4c970d061063a369598e30` | `pkg/store/node_state_pins_test.go` | `TestNodeRules_ATriggerFenceCannotEndALiveHoldersRow` | `TestNodeRules_ATriggerFenceCannotEndALiveHoldersRow` | Keep | `credentials_test.go:TestCredentials/credentials-holder-mutation-races/owner_cannot_finish_executor` and `pkg/store/node_state_test.go:TestTransitionHolderRights`. A trigger capability cannot overwrite a live competing node holder. The refused transaction changes no row or event. Proves §3.3, §5.1. |
| `node-state-machine` | `7b1fc6d111c272612d4c970d061063a369598e30` | `pkg/store/node_state_pins_test.go` | `TestNodeRules_TheReaperEndsACancelledRunsLostNodeAsCancelled` | `TestNodeRules_TheReaperEndsACancelledRunsLostNodeAsCancelled` | Keep | `recovery_test.go:TestRecovery/recovery-cancel-beats-loss/cancel_requested`. A cancel-requested unit ends cancelled. No retry run is created. Proves §3.4. |
| `node-state-machine` | `7b1fc6d111c272612d4c970d061063a369598e30` | `pkg/store/node_state_pins_test.go` | `TestNodeRules_AnAdoptedRowIsNotFinishedTwice` | `TestNodeRules_AnAdoptedRowIsNotFinishedTwice/reaper_first=false` and `/reaper_first=true` | Replace | `recovery_test.go:TestRecovery/recovery-reconciler-waits-and-reclaims/host_first` and `/reaper_first`. The stale host report is refused in either order. The reaper requeues within the same run, and the reconciler follows the persisted state. Proves §3.3-3.4. |
| `node-state-machine` | `7b1fc6d111c272612d4c970d061063a369598e30` | `pkg/store/node_state_pins_test.go` | `TestNodeRules_SettleRunsARecoveryNodeThatListsItsParentAsADep` | `TestNodeRules_SettleRunsARecoveryNodeThatListsItsParentAsADep` | Keep | `lifecycle_test.go:TestLifecycle/lifecycle-dependency-verdicts/parent_failure`. A failed recovery parent releases recovery once its other dependencies permit it. The parent's failure does not count as an ordinary failed prerequisite. Proves §3.3, §3.6. |
| `node-state-machine` | `7b1fc6d111c272612d4c970d061063a369598e30` | `pkg/store/lifecycle_guard_test.go` | `TestLifecycleGuard_StatusWritersAreCounted` | `TestLifecycleGuard_StatusWritersAreCounted` | Replace | `pkg/store/lifecycle_guard_test.go:TestStateWriterOwnership`. Restrict state writers to the final owner files instead of pinning today's counts. Creation goes through `AcceptPlan` or `AcceptExpansion`. Keep this as a package architectural check, not a runtime acceptance case. Proves §4.1. |
| `placement` | `8f114747eba050739f6ce8b775e6eb84edb2e1b0` | `pkg/store/placement_characterization_test.go` | `TestPlacementToday_HereTriggerIsStrandedAfterItsLeaseLapses` | `TestPlacementToday_HereTriggerIsStrandedAfterItsLeaseLapses` | Reverse | `recovery_test.go:TestRecovery/recovery-here-resume-window/lease_lapse`. The here capability reclaims the same run before expiry through `pipeline trigger --resume`. Other runners cannot claim it. Expiry fails with the documented reason. Proves §3.4, §5.1, §9.3. |
| `placement` | `8f114747eba050739f6ce8b775e6eb84edb2e1b0` | `pkg/store/placement_characterization_test.go` | `TestPlacementToday_TriggerClaimMatchesNoLabels` | `TestPlacementToday_TriggerClaimMatchesNoLabels` | Reverse | `placement_test.go:TestPlacement/placement-granted-identity-and-capacity/trigger_requirements`. A claimant missing the trigger's requirements cannot host it. Replace the old fixture, which puts `gpu` only on a node, with explicit run/trigger demand. Proves §3.5. |
| `placement` | `8f114747eba050739f6ce8b775e6eb84edb2e1b0` | `pkg/store/placement_characterization_test.go` | `TestPlacementToday_SelfAssertedLabelsSatisfyRequires` | `TestPlacementToday_SelfAssertedLabelsSatisfyRequires` | Replace | `placement_test.go:TestPlacement/placement-granted-identity-and-capacity` and `pkg/match/evaluate_test.go:TestGrantedKeys`. Ordinary labels such as `prod` remain self-asserted. `pool=`, `name=`, and `class=` come from authority, not supplied labels. Replace `Requires("local")` and `location=coordinator` with `Inline`. Proves §3.5, §6.6. |
| `placement` | `8f114747eba050739f6ce8b775e6eb84edb2e1b0` | `pkg/store/placement_characterization_test.go` | `TestPlacementToday_CoordinatorPinnedNodeIsInvisibleToClaims` | `TestPlacementToday_CoordinatorPinnedNodeIsInvisibleToClaims` | Drop | `placement_test.go:TestPlacement/placement-granted-identity-and-capacity/eligible_recovery_work`. Delete coordinator-pin SQL and the pin mechanism. Matching pending work remains visible to claims and attention. Proves §3.4-3.5. |
| `placement` | `8f114747eba050739f6ce8b775e6eb84edb2e1b0` | `pkg/store/here_runner_test.go` | `TestCreateHereTriggerWithRun_PinsTheTriggerToItsCredentialOnce` | `TestCreateHereTriggerWithRun_PinsTheTriggerToItsCredentialOnce` | Replace | `recovery_test.go:TestRecovery/recovery-here-resume-window/run_binding`. Keep confinement to one run and the recorded reason. Reverse the second-claim refusal and remove `runner_pin`. |
| `placement` | `8f114747eba050739f6ce8b775e6eb84edb2e1b0` | `pkg/store/here_runner_test.go` | `TestCreateHereTriggerWithRun_RefusesAClaimAfterTheWindow` | `TestCreateHereTriggerWithRun_RefusesAClaimAfterTheWindow` | Keep | `credentials_test.go:TestCredentials/credentials-here-award-parent/initial_claim_window`. Keep the initial ten-minute claim window. Subsequent reclaim is governed by the established owner's expiry. |
| `placement` | `8f114747eba050739f6ce8b775e6eb84edb2e1b0` | `pkg/store/agent_match_test.go` | `TestQueueClaimNeverTakesANodeLargerThanTheRunnersCapacity` | `TestQueueClaimNeverTakesANodeLargerThanTheRunnersCapacity` | Keep | `placement_test.go:TestPlacement/placement-granted-identity-and-capacity/capacity`. A two-core runner cannot claim a four-core job. An eight-core runner can. |
| `placement` | `8f114747eba050739f6ce8b775e6eb84edb2e1b0` | `pkg/store/agent_match_test.go` | `TestQueueClaimBindsNameToTheTokenPrincipal` | `TestQueueClaimBindsNameToTheTokenPrincipal` | Keep | `placement_test.go:TestPlacement/placement-granted-identity-and-capacity/name`. Supplying another runner's name does not grant that identity. |
| `placement` | `8f114747eba050739f6ce8b775e6eb84edb2e1b0` | `pkg/store/agent_match_test.go` | `TestNamedClaimAppliesTheClaimantsProfile` | `TestNamedClaimAppliesTheClaimantsProfile` | Keep | `placement_test.go:TestPlacement/placement-granted-identity-and-capacity/named_claim`. Repository acceptance and job demand apply to named claims as they do to queue claims. |
| `placement` | `8f114747eba050739f6ce8b775e6eb84edb2e1b0` | `pkg/store/agent_match_test.go` | `TestRunnerTokenRefusesADuplicateAgentName` | `TestRunnerTokenRefusesADuplicateAgentName` | Replace | `pkg/store/capability_test.go:TestRunnerNameUniqueness`. Preserve team-scoped active runner identity and reuse after revocation through the final issuance API. |
| `placement` | `8f114747eba050739f6ce8b775e6eb84edb2e1b0` | `pkg/store/agent_match_test.go` | `TestNamedClaimOfAMeteredAgentObeysItsAcceptList` | `TestNamedClaimOfAMeteredAgentObeysItsAcceptList` | Keep | `placement_test.go:TestPlacement/placement-granted-identity-and-capacity/metered_runner`. Billing provenance confers no repository or placement authority. |
| `placement` | `8f114747eba050739f6ce8b775e6eb84edb2e1b0` | `pkg/store/agent_match_test.go` | `TestCloudPlacementMatchesThePreChangeBehavior` | `TestCloudPlacementMatchesThePreChangeBehavior` | Replace | `placement_test.go:TestPlacement/placement-granted-identity-and-capacity/launcher`. Use the granted launcher profile. Delete the special case where metering plus a named claim manufactures cloud placement. |
| `placement` | `8f114747eba050739f6ce8b775e6eb84edb2e1b0` | `pkg/store/agent_match_test.go` | `TestNamedClaimFromADispatcherThatSendsNoLabels` | `TestNamedClaimFromADispatcherThatSendsNoLabels` | Replace | `placement_test.go:TestPlacement/placement-granted-identity-and-capacity/missing_labels`. An unmet requirement is refused with its cause. Remove old-dispatcher compatibility framing. |
| `placement` | `8f114747eba050739f6ce8b775e6eb84edb2e1b0` | `pkg/store/agent_match_test.go` | `TestAgentNameIsUniqueOnEveryMintPath` | `TestAgentNameIsUniqueOnEveryMintPath` | Replace | `pkg/store/capability_test.go:TestRunnerNameRotation`. Preserve uniqueness during authorized rotation. Replace old token-kind APIs with capability issuance. |
| `run-input-divergences` | `5ba4cb0ec1ec222defc58f5eaee6ca332a058303` | `internal/api/event_test.go` | `TestPublicEventsRemoveClaimGenerationFromAttemptLifecycle` | `TestPublicEventsRemoveClaimGenerationFromAttemptLifecycle`, subtests `executor_selected`, `execution_attempt_started`, `execution_attempt_finished` | Replace | `internal/api/event_test.go:TestPublicEventsHideFences`. Preserve private fencing-data exclusion under the final event vocabulary. The old branch change only adds the masking argument to the projection call. |
| `run-input-divergences` | `5ba4cb0ec1ec222defc58f5eaee6ca332a058303` | `internal/api/event_test.go` | `TestPublicEventsDropMalformedProtectedLifecyclePayload` | `TestPublicEventsDropMalformedProtectedLifecyclePayload` | Keep | `internal/api/event_test.go:TestMalformedProtectedEvent`. A malformed protected payload is not returned. |
| `run-input-divergences` | `5ba4cb0ec1ec222defc58f5eaee6ca332a058303` | `internal/orchestrator/receipt/receipt_test.go` | `TestBuildReceipt_AnUnclassifiedRunHasNoInputsHash` | `TestBuildReceipt_AnUnclassifiedRunHasNoInputsHash` | Keep | `internal/orchestrator/receipt/receipt_test.go:TestUnclassifiedInputsHaveNoHash` and `credentials_test.go:TestCredentials/credentials-secret-display-classification/receipt`. An unclassified secret cannot become a guessable inputs hash. |
| `run-input-divergences` | `5ba4cb0ec1ec222defc58f5eaee6ca332a058303` | `internal/orchestrator/run_inputs_test.go` | `TestTriggerHost_TheRunAPIRedactsASecretArgument` | `TestTriggerHost_TheRunAPIRedactsASecretArgument` | Replace | `credentials_test.go:TestCredentials/credentials-secret-display-classification/runner_hosted`. Display reads redact secrets. Execution reads use a live bound capability rather than a machine-root token. Finished claims lose execution authority. |
| `run-input-divergences` | `5ba4cb0ec1ec222defc58f5eaee6ca332a058303` | `internal/orchestrator/run_inputs_test.go` | `TestLaunchedNode_TheRunRecordsItsSecretArguments` | `TestLaunchedNode_TheRunRecordsItsSecretArguments` | Keep | `credentials_test.go:TestCredentials/credentials-secret-display-classification/launcher`. The planner records the complete classification before ordinary reads can expose arguments. |
| `run-input-divergences` | `5ba4cb0ec1ec222defc58f5eaee6ca332a058303` | `internal/orchestrator/run_inputs_test.go` | `TestTriggerHost_KeepsTheIntakesInvocationKeys` | `TestTriggerHost_KeepsTheIntakesInvocationKeys` | Replace | `lifecycle_test.go:TestLifecycle/lifecycle-request-and-plan-seals/intake_fields`. The host cannot overwrite intake values. Replace invocation upsert with the two sealed records. |
| `run-input-divergences` | `5ba4cb0ec1ec222defc58f5eaee6ca332a058303` | `internal/orchestrator/run_inputs_test.go` | `TestHostedRun_ADefaultBranchGuardReadsTheCheckoutsOriginHead` | `TestHostedRun_ADefaultBranchGuardReadsTheCheckoutsOriginHead` | Keep | `environment_test.go:TestEnvironment/environment-origin-head-and-guards/origin_head`. Discover `trunk` from origin HEAD. The fixture's rejecting-default guard admits feature and refuses trunk. Add unresolved-default refusal. |
| `run-input-divergences` | `5ba4cb0ec1ec222defc58f5eaee6ca332a058303` | `internal/orchestrator/run_inputs_test.go` | `TestHostedRunContext_CarriesThePullRequest` | `TestHostedRunContext_CarriesThePullRequest` | Replace | `lifecycle_test.go:TestLifecycle/lifecycle-request-and-plan-seals/pull_request` and `internal/runspec/spec_test.go:TestPullRequestFields`. Carry number, action, base/head refs and SHAs through the sealed spec. |
| `run-input-divergences` | `5ba4cb0ec1ec222defc58f5eaee6ca332a058303` | `internal/orchestrator/run_inputs_test.go` | `TestLaunchedNode_RefusesASecretDefaultInAGroupName` | `TestLaunchedNode_RefusesASecretDefaultInAGroupName` | Keep | `credentials_test.go:TestCredentials/credentials-secret-identifiers/sdk_default_group`. The planning host knows SDK defaults and refuses the group before persistence, without echoing its value. |
| `run-input-divergences` | `5ba4cb0ec1ec222defc58f5eaee6ca332a058303` | `internal/orchestrator/run_inputs_test.go` | `TestHosts_RefuseASecretDefaultInANodeID` | `TestHosts_RefuseASecretDefaultInANodeID`, subtests `trigger host`, `launcher` | Keep | `credentials_test.go:TestCredentials/credentials-secret-identifiers/sdk_default_job`. Both hosts refuse through the shared validator and insert no job. Extend to every supported placement. |
| `run-input-divergences` | `5ba4cb0ec1ec222defc58f5eaee6ca332a058303` | `pkg/controller/claim_run_test.go` | `TestClaimRun_PodRoutesServeOnlyTheirOwnClaim` | `TestClaimRun_PodRoutesServeOnlyTheirOwnClaim` | Replace | `credentials_test.go:TestCredentials/credentials-matrix-authentication-status/arguments_and_binding`. Planning receives real values for its own run. Replace old trigger routes and 401-on-live-denial answers with final route bindings and 403. |
| `run-input-divergences` | `5ba4cb0ec1ec222defc58f5eaee6ca332a058303` | `pkg/controller/run_inputs_test.go` | `TestAcceptPlan_NeverDropsAClassifiedSecretArgument` | `TestAcceptPlan_NeverDropsAClassifiedSecretArgument` | Keep | `credentials_test.go:TestCredentials/credentials-secret-display-classification/classification_union` and `pkg/store/run_spec_test.go:TestClassificationNeverShrinks`. A plan cannot remove an intake classification. |
| `run-input-divergences` | `5ba4cb0ec1ec222defc58f5eaee6ca332a058303` | `pkg/controller/run_inputs_test.go` | `TestTriggerReads_RedactTheRunsSecretArguments` | `TestTriggerReads_RedactTheRunsSecretArguments` | Replace | `credentials_test.go:TestCredentials/credentials-secret-display-classification/run_list_and_detail`. Move deleted `/triggers` list/detail assertions to run views. Preserve classified public arguments and hide every unclassified value. |
| `run-input-divergences` | `5ba4cb0ec1ec222defc58f5eaee6ca332a058303` | `pkg/controller/run_inputs_test.go` | `TestSecretArgs_AFinishedClaimReadsRedacted` | `TestSecretArgs_AFinishedClaimReadsRedacted`, subtests `node`, `run` | Replace | `credentials_test.go:TestCredentials/credentials-secret-display-classification/finished_job` and `/finished_run`. Authority ends at terminal state before lease expiry. A dead bound credential gets 401. A separate account display read remains redacted. |
| `run-input-divergences` | `5ba4cb0ec1ec222defc58f5eaee6ca332a058303` | `pkg/controller/run_inputs_test.go` | `TestApprovalReads_MaskSecretArgumentsInTheirText` | `TestApprovalReads_MaskSecretArgumentsInTheirText` | Keep | `credentials_test.go:TestCredentials/credentials-secret-display-classification`. Mask message, approver and comment across decorations, approval detail/list, pending approvals and events. Include SDK defaults at the write boundary. |
| `run-input-divergences` | `5ba4cb0ec1ec222defc58f5eaee6ca332a058303` | `pkg/controller/run_inputs_test.go` | `TestAcceptPlan_RefusesASecretInANodeID` | `TestAcceptPlan_RefusesASecretInANodeID` | Keep | `credentials_test.go:TestCredentials/credentials-secret-identifiers/controller_accept`. Refuse before insertion with the shared error object and no secret value. |
| `run-input-divergences` | `5ba4cb0ec1ec222defc58f5eaee6ca332a058303` | `pkg/controller/secret_args_display_test.go` | `TestSecretArgs_ControllerRedactsEveryArgOfAnUnclassifiedRun` | `TestSecretArgs_ControllerRedactsEveryArgOfAnUnclassifiedRun`, loop values `pending`, `success` | Keep | `credentials_test.go:TestCredentials/credentials-secret-display-classification/unclassified`. The display fails closed in both phases. |
| `run-input-divergences` | `5ba4cb0ec1ec222defc58f5eaee6ca332a058303` | `pkg/store/plan_secret_names_test.go` | `TestSecretInPlanName_RefusesEveryRenderedName` | `TestSecretInPlanName_RefusesEveryRenderedName` | Replace | `internal/nodeproto/validate_test.go:TestSecretIdentifiers` and `credentials_test.go:TestCredentials/credentials-secret-identifiers`. Use the exact rows below, with the short-value question recorded. |
| `run-input-divergences` | `5ba4cb0ec1ec222defc58f5eaee6ca332a058303` | `pkg/store/retry_team_test.go` | `TestManualRetryKeepsActorAndPullRequest` | `TestManualRetryKeepsActorAndPullRequest` | Replace | `lifecycle_test.go:TestLifecycle/lifecycle-retry-sealed-lineage/actor_and_pull_request`. Retain sealed actor, PR, original SHA and verified branch facts. Remove trigger-env copying and agent-loss retry-run framing. |
| `run-input-divergences` | `5ba4cb0ec1ec222defc58f5eaee6ca332a058303` | `pkg/store/run_inputs_test.go` | `TestCreateRun_TheIntakesInvocationKeysWin` | `TestCreateRun_TheIntakesInvocationKeysWin` | Replace | `pkg/store/run_spec_test.go:TestRequestSealIsImmutable` and `lifecycle_test.go:TestLifecycle/lifecycle-request-and-plan-seals/intake_precedence`. Fill resolved fields at the second seal without rewriting the request. |
| `run-input-divergences` | `5ba4cb0ec1ec222defc58f5eaee6ca332a058303` | `pkg/store/run_inputs_test.go` | `TestCreateRun_ConcurrentWritersNeverShrinkTheSecretArgs` | `TestCreateRun_ConcurrentWritersNeverShrinkTheSecretArgs` | Replace | `pkg/store/run_spec_test.go:TestConcurrentClassification`. Interleave final API writes on Postgres and prove classification never shrinks. Replace the pending-upsert hook with the owning transaction boundary. |
| `run-input-divergences` | `5ba4cb0ec1ec222defc58f5eaee6ca332a058303` | `pkg/store/secretargs_test.go` | `TestRedactedForDisplay_RedactsEveryArgOfAnUnclassifiedRun` | `TestRedactedForDisplay_RedactsEveryArgOfAnUnclassifiedRun` | Keep | `pkg/store/secretargs_test.go:TestUnclassifiedDisplay`. Absent or malformed classification hides all argument values and their reproducer/invocation copies. |
| `run-input-divergences` | `5ba4cb0ec1ec222defc58f5eaee6ca332a058303` | `pkg/store/secretargs_test.go` | `TestRedactedForDisplay_AnEmptyClassificationShowsEveryArg` | `TestRedactedForDisplay_AnEmptyClassificationShowsEveryArg` | Keep | `pkg/store/secretargs_test.go:TestExplicitEmptyClassification`. Explicit empty classification differs from absent classification and survives inheritance. |
| `storage-hotfixes` | `80ce329157569a0d0d1cf1d3f67e3e06060512ed` | `internal/artifacts/retention_test.go` | `TestCaptureRewritesStoredContentSoItsAgeRestarts` | `TestCaptureRewritesStoredContentSoItsAgeRestarts` | Replace | `storage_test.go:TestStorage/storage-reachability-and-retention-tails/reused_content`. Referenced bytes survive GC. Delete the exact two-PUT assertion because reachability and pinned records replace object-age refresh. |
| `storage-hotfixes` | `80ce329157569a0d0d1cf1d3f67e3e06060512ed` | `internal/artifacts/retention_test.go` | `TestPresentIsBoundedAndAnswersAMissPastItsBudget` | `TestPresentIsBoundedAndAnswersAMissPastItsBudget` | Replace | `internal/blob/store_test.go:TestHasManyBudget` and `storage_test.go:TestStorage/storage-memo-materialization-failures/budget`. Presence lookup respects its budget. A timed-out memo lookup is a miss. Do not pin the old worker count. |
| `storage-hotfixes` | `80ce329157569a0d0d1cf1d3f67e3e06060512ed` | `internal/cache/scope_test.go` | `TestArtifactsAreScopedToTheGrantsRepository` | `TestArtifactsAreScopedToTheGrantsRepository`, subtests `volume`, `bucket` | Replace | `storage_test.go:TestStorage/storage-reference-edge-laundering/filesystem` and `/s3`. Preserve foreign read/existence refusal and own-upload authority. Repository membership alone grants no read. Delete cache-server paths and credentials. |
| `storage-hotfixes` | `80ce329157569a0d0d1cf1d3f67e3e06060512ed` | `internal/cache/scope_test.go` | `TestStoredArtifactRewritePassesAFrozenCeiling` | `TestStoredArtifactRewritePassesAFrozenCeiling`, subtests `volume`, `bucket` | Replace | `storage_test.go:TestStorage/storage-hasmany-read-set/deduplicated_put`. Existing authorized content adds no quota bytes. New content is still refused beyond the ceiling. Use controller accounting instead of the mutable global ceiling. |
| `storage-hotfixes` | `80ce329157569a0d0d1cf1d3f67e3e06060512ed` | `internal/nodehost/memo_artifacts_test.go` | `TestMemoUnclaimedFollowerWithoutLeaderOutputRuns` | `TestMemoUnclaimedFollowerWithoutLeaderOutputRuns` | Keep | `storage_test.go:TestStorage/storage-memo-materialization-failures/follower_output`. A follower executes when its leader's output is gone. It cannot return successful empty output. |
| `storage-hotfixes` | `80ce329157569a0d0d1cf1d3f67e3e06060512ed` | `internal/nodehost/memo_artifacts_test.go` | `TestMemoCopyWhoseManifestWriteFailsRunsAsAMiss` | `TestMemoCopyWhoseManifestWriteFailsRunsAsAMiss`, subtests `Hit`, `Follow` | Keep | `storage_test.go:TestStorage/storage-memo-materialization-failures/manifest_commit`. A failed manifest-edge transaction becomes a memo miss for hits and followers. |
| `storage-hotfixes` | `80ce329157569a0d0d1cf1d3f67e3e06060512ed` | `internal/nodehost/rehydrate_test.go` | `TestARetryRunsAProducerWhoseArtifactsCannotBeCopied` | `TestARetryRunsAProducerWhoseArtifactsCannotBeCopied`, subtests `absent`, `refused` | Keep | `storage_test.go:TestStorage/storage-memo-materialization-failures/missing_bytes` and `/refused_edge`. A retry reruns the producer when bytes or authorized records cannot be copied. |
| `storage-hotfixes` | `80ce329157569a0d0d1cf1d3f67e3e06060512ed` | `pkg/controller/cache_ref_internal_test.go` | `TestCacheRefs_FollowTheGrantsSignedRefs` | `TestCacheRefs_FollowTheGrantsSignedRefs` | Replace | `internal/capability/data_scope_test.go:TestDataRefScope` and `storage_test.go:TestStorage/storage-memo-and-depcache-scope`. Use server-derived sealed scopes, manual namespaces and original retry trust. Memo reads its own scope with verified-default fallback. Depcache restores own ref, PR base, then verified default. Delete signed cache grants and unscoped/empty-repo fallback. |
| `storage-hotfixes` | `80ce329157569a0d0d1cf1d3f67e3e06060512ed` | `pkg/controller/claim_run_test.go` | `TestClaimRun_InputsFromAnotherRunFollowTheAcceptedPlan` | `TestClaimRun_InputsFromAnotherRunFollowTheAcceptedPlan` | Keep | `storage_test.go:TestStorage/storage-reference-edge-laundering`. Memo, coalesced and declared prior-run references work only through authorized edges. Preserve the full case inventory below. |
| `storage-hotfixes` | `80ce329157569a0d0d1cf1d3f67e3e06060512ed` | `pkg/controller/claim_run_test.go` | `TestClaimRun_MemoEntriesAreScopedToTheRunsRef` | `TestClaimRun_MemoEntriesAreScopedToTheRunsRef` | Replace | `storage_test.go:TestStorage/storage-memo-and-depcache-scope/poisoning_and_fallback`. Manual runs cannot poison verified default entries. Own scope precedes trusted fallback. Retry preserves trust. A planted false-provenance entry is refused. Seed trust through verified App intake instead of SQL `trigger_source=github`. |
| `storage-hotfixes` | `80ce329157569a0d0d1cf1d3f67e3e06060512ed` | `pkg/controller/claim_run_test.go` | `TestNodeClaimMemoKeysFollowTheRunsRef` | `TestNodeClaimMemoKeysFollowTheRunsRef` | Replace | `storage_test.go:TestStorage/storage-memo-and-depcache-scope/server_owned_key`. The server computes the scope. Naming main's scope or another job's key grants no authority. |
| `storage-hotfixes` | `80ce329157569a0d0d1cf1d3f67e3e06060512ed` | `pkg/controller/data_download_claim_provenance_test.go` | `TestDataDownloadClaimUsesCloudProvenance` | `TestDataDownloadClaimUsesCloudProvenance` | Replace | `storage_test.go:TestStorage/storage-binary-provenance` and `placement_test.go:TestPlacement/placement-direct-source`. Preserve trusted binary execution and source binding. Artifact access follows the read set. See the exact seven cases below and the provenance question. |
| `storage-hotfixes` | `80ce329157569a0d0d1cf1d3f67e3e06060512ed` | `pkg/controller/data_download_output_boundary_test.go` | `TestDataDownloadSharesCommittedArtifactsWithinTheRepository` | `TestDataDownloadSharesCommittedArtifactsWithinTheRepository` | Reverse | `storage_test.go:TestStorage/storage-reference-edge-laundering/same_repo_unrelated_run`. Repository membership alone cannot read another run's artifact. Require a dependency, declared reference, memo edge or own receipt. |
| `storage-hotfixes` | `80ce329157569a0d0d1cf1d3f67e3e06060512ed` | `pkg/controller/data_download_test.go` | `TestLegacyBinarySignerRefusesArtifactNames` | `TestLegacyBinarySignerRefusesArtifactNames` | Drop | `storage_test.go:TestStorage/storage-reference-edge-laundering/object_kind`. Delete the legacy signer. Retain object-kind and read-set refusal on final `/data` signing routes, with no legacy fallback. |
| `storage-hotfixes` | `80ce329157569a0d0d1cf1d3f67e3e06060512ed` | `pkg/controller/direct_artifact_scope_test.go` | `TestDirectArtifactReadsStayInsideTheGrantsRepository` | `TestDirectArtifactReadsStayInsideTheGrantsRepository` | Replace | `storage_test.go:TestStorage/storage-reference-edge-laundering/foreign_repository`. Foreign authority is refused. A verified own upload creates a receipt. Add same-team and same-run unrelated-job cases. |
| `storage-hotfixes` | `80ce329157569a0d0d1cf1d3f67e3e06060512ed` | `pkg/controller/direct_artifact_scope_test.go` | `TestDirectArtifactRedeclarationRenewsItsAge` | `TestDirectArtifactRedeclarationRenewsItsAge` | Replace | `storage_test.go:TestStorage/storage-reachability-and-retention-tails/live_reference`. A live reference prevents collection. Delete assertions about mutable `committed_at` and the object's `renewed` metadata. |
| `storage-hotfixes` | `80ce329157569a0d0d1cf1d3f67e3e06060512ed` | `pkg/controller/direct_artifact_scope_test.go` | `TestNodeArtifactManifestMustBeStoredByTheRunsRepository` | `TestNodeArtifactManifestMustBeStoredByTheRunsRepository` | Replace | `storage_test.go:TestStorage/storage-reference-edge-laundering/record`. Check existence and authority for the manifest and every member. Repository-scoped names alone are insufficient. |
| `storage-hotfixes` | `80ce329157569a0d0d1cf1d3f67e3e06060512ed` | `pkg/controller/direct_artifact_scope_test.go` | `TestClaimedAttemptRecordsOnlyAManifestItsRepositoryStored` | `TestClaimedAttemptRecordsOnlyAManifestItsRepositoryStored` | Replace | `storage_test.go:TestStorage/storage-reference-edge-laundering/result_transaction`. Apply existence and edge authorization atomically with the attempt result. |
| `storage-hotfixes` | `80ce329157569a0d0d1cf1d3f67e3e06060512ed` | `pkg/controller/direct_artifact_scope_test.go` | `TestRunnerNodeManifestMayLiveInItsOwnStore` | `TestRunnerNodeManifestMayLiveInItsOwnStore` | Reverse | `storage_test.go:TestStorage/storage-reference-edge-laundering/unseen_remote_manifest`. Remote result commit refuses a manifest the controller cannot validate. Remove `SPARKWING_CACHE_URL`. Local-run files remain local until an explicit publication. |
| `storage-hotfixes` | `80ce329157569a0d0d1cf1d3f67e3e06060512ed` | `pkg/controller/direct_artifact_scope_test.go` | `TestClaimedAttemptManifestIsCheckedAgainstTheCacheBucket` | `TestClaimedAttemptManifestIsCheckedAgainstTheCacheBucket` | Drop | `storage_test.go:TestStorage/storage-reference-edge-laundering/filesystem` and `/s3`. Delete cache-bucket HEAD fallback. Authoritative blob rows and authorized members govern both final backends. |
| `storage-hotfixes` | `80ce329157569a0d0d1cf1d3f67e3e06060512ed` | `pkg/controller/direct_upload_test.go` | `TestDirectCommitAdoptsVerifiedCopyAfterOldReservationExpires` | `TestDirectCommitAdoptsVerifiedCopyAfterOldReservationExpires` | Replace | `internal/blob/store_test.go:TestCommitAfterInterruptedUpload` and `storage_test.go:TestStorage/storage-cas-integrity/retry`. Adopt only verified immutable bytes, preserve uploader provenance, and issue receipts only to authorized uploaders. |
| `storage-hotfixes` | `80ce329157569a0d0d1cf1d3f67e3e06060512ed` | `pkg/controller/direct_upload_test.go` | `TestDirectCommitRefusesExistingObjectWithDifferentChecksum` | `TestDirectCommitRefusesExistingObjectWithDifferentChecksum` | Keep | `storage_test.go:TestStorage/storage-cas-integrity/checksum_mismatch`. Refuse conflicting bytes and publish no successful record. |
| `storage-hotfixes` | `80ce329157569a0d0d1cf1d3f67e3e06060512ed` | `pkg/controller/direct_upload_test.go` | `TestClaimBinaryUploadIsReservedUnderTheRunsRef` | `TestClaimBinaryUploadIsReservedUnderTheRunsRef` | Replace | `storage_test.go:TestStorage/storage-binary-provenance/ref_scope`. Manual writes use the manual namespace. Retry retains its original awarded scope. Replace the hand-minted cache-grant fixture. |
| `storage-hotfixes` | `80ce329157569a0d0d1cf1d3f67e3e06060512ed` | `pkg/store/generated_node_test.go` | `TestAGeneratedNodesRecordedSpecIsAdmitted` | `TestAGeneratedNodesRecordedSpecIsAdmitted` | Replace | `pkg/store/expansion_test.go:TestGeneratedSpecAuthority` and `lifecycle_test.go:TestLifecycle/lifecycle-generator-reconstruct-code/declared_references`. Persisted spec/hash controls generated-job references. Forged hash and undeclared references are refused. |
| `design-found-hotfixes` | `c5aa3b88f1b6debf856e887bfc6df0894355b6cd` | `internal/nodehost/onfailure_test.go` | `TestRecoveryWaitsOnItsOwnDeps` | `TestRecoveryWaitsOnItsOwnDeps` | Keep | `lifecycle_test.go:TestLifecycle/lifecycle-dependency-verdicts/own_dependencies`. Recovery waits for its own dependencies and generated groups as well as its parent. Only the failed parent is exempt from ordinary failure propagation. |
| `design-found-hotfixes` | `c5aa3b88f1b6debf856e887bfc6df0894355b6cd` | `internal/nodehost/onfailure_test.go` | `TestRecoveryIsCancelledByAFailedDepOfItsOwn` | `TestRecoveryIsCancelledByAFailedDepOfItsOwn` | Keep | `lifecycle_test.go:TestLifecycle/lifecycle-dependency-verdicts/failed_own_dependency`. An ordinary failed prerequisite prevents recovery. Use reason `upstream_failed`. |
| `design-found-hotfixes` | `c5aa3b88f1b6debf856e887bfc6df0894355b6cd` | `internal/runners/k8s/k8s_test.go` | `TestResolveResources_StaysQuietUnderTheCeiling` | `TestResolveResources_StaysQuietUnderTheCeiling` | Replace | `internal/launcher/sizing_test.go:TestSizingBelowCeiling`. Missing measurements are normal and under-ceiling work emits no warning event. Delete the old k8s dispatcher and use the shared error envelope. |
| `design-found-hotfixes` | `c5aa3b88f1b6debf856e887bfc6df0894355b6cd` | `pkg/controller/github_runners_test.go` | `TestGitHubRunnerCredentialReadsOnlyAClaimedPipelinesProfile` | `TestGitHubRunnerCredentialReadsOnlyAClaimedPipelinesProfile` | Replace | `credentials_test.go:TestCredentials/credentials-holder-mutation-races/github_actions`. No claim and another pipeline are denied. An authorized missing sizing record returns not-found. Use `pipelineOfBoundRun` on the final sizing route. |
| `design-found-hotfixes` | `c5aa3b88f1b6debf856e887bfc6df0894355b6cd` | `pkg/controller/log_segments_internal_test.go` | `TestTriggerFencedLogWritesRefuseALiveHolder` | `TestTriggerFencedLogWritesRefuseALiveHolder` | Replace | `credentials_test.go:TestCredentials/credentials-holder-mutation-races/log_mutations`. A competing holder cannot append, sign or seal logs. Delete the unfenced-admin bypass. Recheck after request-body consumption and before mutation. |
| `design-found-hotfixes` | `c5aa3b88f1b6debf856e887bfc6df0894355b6cd` | `pkg/controller/runner_scopes_test.go` | `TestRunnerScopes_ProfileWritesAreScopedToTheClaimedPipeline` | `TestRunnerScopes_ProfileWritesAreScopedToTheClaimedPipeline` | Replace | `credentials_test.go:TestCredentials/credentials-holder-mutation-races/read_and_write`. Exact sealed repo and pipeline bind observations and pins. Delete shared bare-key authority. |
| `design-found-hotfixes` | `c5aa3b88f1b6debf856e887bfc6df0894355b6cd` | `pkg/store/concurrency_guard_test.go` | `TestConcurrencyGuard_CanonicalSQLSitesOnly` | `TestConcurrencyGuard_CanonicalSQLSitesOnly` | Replace | Implemented in `pkg/store/concurrency_admission_runtime_test.go:TestHolderLivenessUsesStoreClock` and `pkg/store/concurrency_scope_property_test.go:TestConcurrencyExpiredCapacityNeverRevivesProperty`. Canonical liveness uses the injected store clock. Expired capacity cannot renew or adopt. The old source-count test is deleted. |
| `design-found-hotfixes` | `c5aa3b88f1b6debf856e887bfc6df0894355b6cd` | `pkg/store/controller_dispatch_test.go` | `TestReportAttempt_ARecoveryNodeWaitsOnItsOwnDeps` | `TestReportAttempt_ARecoveryNodeWaitsOnItsOwnDeps` | Keep | `lifecycle_test.go:TestLifecycle/lifecycle-dependency-verdicts/controller` and `pkg/store/decide_test.go:TestRecoveryDependencies`. Success of an own dependency releases recovery. Failure cancels it. A successful recovery does not erase the original run failure. |
| `design-found-hotfixes` | `c5aa3b88f1b6debf856e887bfc6df0894355b6cd` | `pkg/store/node_claim_fence_holder_test.go` | `TestTriggerFencedNodeWritesRefuseALiveHolder` | `TestTriggerFencedNodeWritesRefuseALiveHolder` | Replace | `credentials_test.go:TestCredentials/credentials-holder-mutation-races/node_mutations` and `recovery_test.go:TestRecovery/recovery-reconciler-waits-and-reclaims`. Refuse competing live writes and terminal reversal. Reverse the last old assertion allowing the host to finish after lease lapse. The reaper and reconciler own that recovery. Delete host lifecycle-event append and `ResetNodeForAutoRetry`. |
| `design-found-hotfixes` | `c5aa3b88f1b6debf856e887bfc6df0894355b6cd` | `pkg/store/profile_claim_test.go` | `TestPrincipalHoldsProfileClaimMatchesTheClaimedRunsExactKey` | `TestPrincipalHoldsProfileClaimMatchesTheClaimedRunsExactKey`, subtests `build`, `ci/build`, `3:abcbuild` | Replace | `pkg/store/capability_test.go:TestSizingBinding` and `credentials_test.go:TestCredentials/credentials-holder-mutation-races/pipeline_names`. Preserve full pipeline names and exact repo identity. Delete authorization through bare shared profile keys. |
| `credential-matrix` | `1e16668363ecbd99ba922436ab1a6f9cae63335b` | `pkg/controller/credential_matrix_test.go` | `TestCredentialMatrix` | `TestCredentialMatrix`; 8,466 route/target/credential cells | Replace | `credentials_test.go:TestCredentials/credentials-matrix-authentication-status` and `pkg/controller/route_policy_test.go:TestEveryRouteHasPolicy`. Independently specify permissions, cover final registrations and binding alternatives, isolate controller/store per cell, and use reference-token lookup. |
| `run-input-divergences` | `5ba4cb0ec1ec222defc58f5eaee6ca332a058303` | `pkg/controller/secret_args_display_test.go` | `TestSecretArgs_ControllerPendingTriggerRowIsNotYetClassified` | `TestSecretArgs_ControllerPendingTriggerRowIsNotYetClassified`; removed predecessor | Reverse | `credentials_test.go:TestCredentials/credentials-secret-display-classification/unclassified`. Refuse plaintext exposure; unclassified display fails closed. |
| `run-input-divergences` | `5ba4cb0ec1ec222defc58f5eaee6ca332a058303` | `pkg/controller/secret_args_display_test.go` | `TestSecretArgs_ControllerGrandfathersOldRuns` | `TestSecretArgs_ControllerGrandfathersOldRuns`; removed predecessor | Drop | `credentials_test.go:TestCredentials/credentials-secret-display-classification/unclassified`. Remove grandfathering; unclassified display fails closed. |
| `run-input-divergences` | `5ba4cb0ec1ec222defc58f5eaee6ca332a058303` | `pkg/store/secretargs_test.go` | `TestRedactedForDisplay_GrandfathersRunsWithoutClassification` | `TestRedactedForDisplay_GrandfathersRunsWithoutClassification`; removed predecessor | Reverse | `credentials_test.go:TestCredentials/credentials-secret-display-classification/unclassified`. Refuse plaintext exposure; unclassified display fails closed. |
| `storage-hotfixes` | `80ce329157569a0d0d1cf1d3f67e3e06060512ed` | `pkg/controller/cache_ref_internal_test.go` | `TestCacheRefs_FollowTheRunsRefAsGitHubActionsDoes` | `TestCacheRefs_FollowTheRunsRefAsGitHubActionsDoes`; removed predecessor | Replace | `storage_test.go:TestStorage/storage-memo-and-depcache-scope`. Use immutable sealed scope instead of mutable trigger-derived scope. |
| `storage-hotfixes` | `80ce329157569a0d0d1cf1d3f67e3e06060512ed` | `pkg/controller/data_download_output_boundary_test.go` | `TestDataDownloadSharesCommittedArtifactsWithinTheTeam` | `TestDataDownloadSharesCommittedArtifactsWithinTheTeam`; removed predecessor | Reverse | `storage_test.go:TestStorage/storage-reference-edge-laundering`. Refuse unrestricted team sharing; require authorized read-set edges. |
| `design-found-hotfixes` | `c5aa3b88f1b6debf856e887bfc6df0894355b6cd` | `internal/nodehost/onfailure_test.go` | `TestRecoveryWaitsOnItsParentAlone` | `TestRecoveryWaitsOnItsParentAlone`; removed predecessor | Reverse | `lifecycle_test.go:TestLifecycle/lifecycle-dependency-verdicts/own_dependencies`. Recovery waits on its own dependencies as well as its failed parent. |
<!-- port-inventory:end -->

## Required case details

### Complete old credential dimension

| Exact old label | Final fixture and decision |
|---|---|
| `anon` | Keep anonymous request coverage. |
| `open` | Replace network-open trust with local-mode authority and the loopback/socket boundary. Test local identity separately. |
| `root` | Replace blanket admin with the distinct deployment-admin and operator-console subjects. Bound routes have no admin bypass. |
| `service-credits` | Replace with the final billing/service authority. It must not grant unrelated operator-console powers. |
| `service-launch` | Keep a launcher service root that can Award across teams through the fixed table. |
| `service-storage` | Drop `storage.count`. There is no cache service or cache audience. |
| `session` | Store/component evidence: `TestSessionMembershipChangesApplyWithoutCachedGrantsProperty`, `TestSessionRoutePreservesCapabilityScopeProperty` and `TestCredentials/credentials-session-digest-csrf/store-sqlite` check current roles, capability ceilings, digest lookup and sealed CSRF persistence. Native route-matrix evidence remains required. |
| `member` | Keep team-member grants. A member does not gain work-claim grants directly. |
| `machine` | Keep machine root claim scopes. Secret/source/data/OIDC access requires a live award. |
| `machine+trigger` | Replace caller-supplied trigger fence with the awarded trigger capability. A root plus forged headers gains nothing. |
| `machine+node` | Replace caller-supplied node fences with the awarded node capability. A root plus forged headers gains nothing. |
| `trigger-claim` | Keep the trigger award with `runOfTrigger`, bounded by the store's holder rights. |
| `node-claim` | Keep a node-bound award with live generation/holder binding. |
| `plan-claim` | Keep a plan-unit award with its fixed grants. Add an eval-unit award for expansion. |
| `work-claim` | Keep a work award and add its delegated nodehost bearer with the separate audience. |
| `here` | Replace with the here case of Award. Its parent must retain `runs.write`, its first claim has a ten-minute window, and it can reclaim only its run before expiry. |
| `github` | Keep push-bound GitHub Actions exchange authority. It cannot manufacture App-verified branch trust. |

The old fixture is multi-team and licensed except for `open`, which is single-team. Preserve cross-team cases without treating a license as authorization.

### Complete old target dimension

| Old route class | Exact variants |
|---|---|
| `/api/v1/runs/{id}` routes containing `{nodeID}` | `home`, `otherNode`, `otherRun`, `otherTeam` |
| Other `/api/v1/runs/{id}` and `/api/v1/triggers/{id}` routes | `home`, `otherRun`, `otherTeam` |
| `/api/v1/concurrency/*`, `/api/v1/triggers`, `/api/v1/runs`, `/api/v1/triggers/spawned-child`, `/api/v1/secrets/{name}` | `home`, `otherNode`, `otherRun`, `otherTeam` |
| `/api/v1/pipelines/{name}*` | `home`, `otherPipeline` |
| Every other registered authenticated route | `home` |

The old request builder names the target in concurrency bodies, run creation, child submission/query, and secret queries. Other request bodies are `{}`. Positive final cells need valid minimal requests, because a handler's 400 from an empty body does not prove successful authorization.

### Final matrix obligations

Keep the old golden as source evidence in Git. Do not copy its expected statuses into the final suite. The tables above map credential presentations and target variants. [The route appendix](t1-credential-routes.md) enumerates all 229 old routes, their dispositions, scenario IDs, independent permission rules and unresolved rights. This completes the source inventory, not the expected permission matrix. Removed routes go to `surface-api-and-detail-parity`. Surviving cells remain launch-blocked until their expected rights are independently specified.

| Old behavior or omission | Final expected behavior |
|---|---|
| `/pipelines/{name}/profile`, `otherPipeline`, credentials `github`, `machine`, `machine+node`, `machine+trigger` are the four annotated gap cells | Reverse permission. The sizing route requires authority for the exact bound repo and pipeline. |
| Trigger claim reaches every node in its run | Keep `runOfTrigger`, but a competing holder still blocks writes in the transaction. |
| Admin/open bypasses concurrency slot binding | Reverse permission. `slotOfBoundNode` applies without an admin bypass. |
| `claim/input` ignores the node named in the path | Reverse permission. Require `ownNode` for the path and operation. |
| Here and GitHub credentials disagree on `/services` | Unresolved. Frozen §10/L72 records the disagreement but gives neither permission answer. The lead must resolve `U-SERVICES` in the route appendix. These cells block launch acceptance. Do not derive their expected answers from the production policy table. |
| Deps route converts fence failures to 500 | Replace with the shared `lock_held` or `illegal_transition` error. |
| Live trigger/plan/work claims get 401 on 339 disallowed routes each | Reverse status. Live credentials lacking grant or binding get 403. Dead credentials get 401. |
| Trigger fence loses a race after middleware | Keep transaction recheck and return 409 with the shared error object. |
| Handler authorizes create-run or parent binding | Move the rule to `bodyNamesRun` and the shared policy. Store mutations still recheck authority. |
| Matrix derives route discovery from `server.go` | Test the complete final registration surface against the policy registry. Independently specify expected permissions so the table under test is not its own answer key. |

Add dimensions for expired, revoked, terminal-bound, stale-generation, wrong-audience, wrong-holder and revoked-parent capabilities. Add Delegate narrowing, fixed Award grant tables, bootstrap Exchange and single-use revocation, here parent liveness, and expansion authority.

The old matrix excludes public routes, browser routes, the wingd socket, `/node/v1`, cache grants, GitHub exchange, webhooks, signed output/log URLs, and public data upload. Cover their surviving boundaries explicitly. Test `/data` signing with capability grants and signed URLs with their separate expiry contract. Test Unix peer identity and Windows SID/private-file behavior through the local fixture. Proves §5.1-5.6, §6.5, §8.1, §9.5.

The exact rows in `TestSecretInPlanName_RefusesEveryRenderedName` are `node id`, `group`, `step id`, `step group`, `concurrency group`, `runs_on label`, `below the floor`, and `clean`. Translate node to job and the removed `runs_on` field to the corresponding placement declaration. Add approval IDs and generated expansion IDs. The old `below the floor` row accepts secret `42` in `node-42`. The frozen rule has no minimum length, so the proposed target refuses this case. See open questions.

`TestClaimRun_InputsFromAnotherRunFollowTheAcceptedPlan` has four allowed table entries: a memo hit from `run-origin`, a coalesced leader read from `run-leader`, a coalesced cache fallback from `run-leader`, and a declared last-run reference from `run-last`. Its complete named refusal cases are:

- `a cache entry on a node that declares no memoization`
- `a sibling node's key for a known hash`
- `a bare memo key for a known hash`
- `an entry another node wrote under its key`
- `an entry another pipeline wrote under its key`
- `a leader another repository wrote under its key`
- `a cache entry another repository wrote under its key`
- `an undeclared pipeline reference`
- `a pipeline reference on a node that declares none`
- `another node's coalesce waiter`
- `a key with no waiter and no cache entry`
- `another team's cache entry`
- `another team's run of the node under its key`
- `another team's entry for the same node and hash`

Retain each authorization/miss distinction through the final API. Use final `apierr` codes rather than mechanically preserving every old HTTP status.

`TestCacheRefs_FollowTheGrantsSignedRefs` has no named subtests. Its table covers main scope, PR/base/default restore order, manual-main scope with default fallback, `manual:` with an empty ref, a claim with no scope, a non-claim legacy grant, and a separate other-team rejection. Replace the manual-empty and non-claim unscoped assumptions with final sealed repo/ref validity and authority rules. Do not preserve empty namespace sharing. The old PR-base row is source evidence, not a new memo fallback requirement. Frozen §8.1 specifies own-ref/PR-base/default order for depcache, while memo has its own scope and verified-default fallback. A PR-base memo fallback needs a separate resolution.

`TestDataDownloadClaimUsesCloudProvenance` has these exact subtests: `local artifact`, `cloud artifact`, `local binary`, `cloud binary`, `legacy binary`, `bound source`, `unbound source`. Cloud binaries require trusted provenance unless `trust_local_builds` is enabled. Legacy binaries have no fallback. Source access remains bound. The frozen architecture explicitly makes artifacts depend on read-set authority. It does not state the same cloud-only rule for every artifact.

## Cases the parked reports require beyond their tests

The source reports describe residual failures that their committed tests do not close. Add these cases without treating the reports as passing tests.

| Evidence and reproducible case | New home and expected behavior |
|---|---|
| state/security re-review: a Widgets claimant changes its pipeline to `23:github.com/acme/gadgetsbuild`, then reaches Gadgets sizing through bare/scoped key confusion | `credentials_test.go:TestCredentials/credentials-holder-mutation-races/scoped_key_impersonation`. Sealed pipeline identity cannot change. A typed pipeline string cannot become another repo's sizing authority. |
| state/security re-review: a log request passes its fence, stalls while its body is read, then completes after another holder claims the job | `credentials_test.go:TestCredentials/credentials-holder-mutation-races/stalled_log_body`. Check append, segment-signing and manifest-sealing mutations after the stall. All refuse stale authority. |
| state/security re-review: source A generates deploy and rollback, while rollback waits on independent generated group B. B's expansion drops rollback's recovery-parent edge and ancestry to A | `lifecycle_test.go:TestLifecycle/lifecycle-generator-reconstruct-code/recovery_ancestry`. Expansion keeps required ancestry. Code replay can read declared A output and execute rollback after B resolves. |
| storage report: bare memo writers include trigger hosts and local runs, while input-side checks cover only claim paths | `storage_test.go:TestStorage/storage-memo-and-depcache-scope/all_writers`. Every writer uses server-derived repo/ref/pipeline/job scope. Untrusted bare keys are refused. |
| storage report: a stored entry can name an origin from another ref, and retry/default lookup misses parent-run ancestry | `storage_test.go:TestStorage/storage-memo-and-depcache-scope/origin_and_lineage`. Check recorded origin authority at read time and retain the sealed original trust through child/retry lineage. |
| storage report: no-artifact-store retry copies digests without checking existence | `storage_test.go:TestStorage/storage-memo-materialization-failures/server_existence`. Remote materialization checks authoritative existence even when no local artifact backend is configured. |
| Frozen §8.1/A66: unrelated jobs A and B share a run. A uploads D, then B names D in a manifest | `storage_test.go:TestStorage/storage-reference-edge-laundering/unrelated_jobs`. Refuse with `forbidden` and reason `digest_not_readable` unless B gains an allowed edge or uploads its own verified copy. |
| run-input report: SDK defaults do not reach the controller, and generated IDs are not checked after initial planning | `credentials_test.go:TestCredentials/credentials-secret-identifiers/defaults_and_expansion`. Planning hosts register defaults. The shared validator checks both plans and expansions. |
| storage report: presence checks charge the object's full size against download caps | `storage_test.go:TestStorage/storage-hasmany-read-set/presence_query`. `HasMany` is a metadata query limited to the read set and transfers no object bytes. |
| storage report: reused/copied bytes can race collection | `storage_test.go:TestStorage/storage-reachability-and-retention-tails/reference_and_gc`. Keep referenced records, apply the 24-hour reachability/tombstone rule, and fail loudly or miss if bytes are gone. Do not require power-loss durability beyond §1.11. |

## Open questions for the lead

1. **Short secret values in identifiers.** The old `below the floor` case accepts a two-character registered value. Decision 90 and §6.3 name no length exemption. The proposed port refuses every registered nonempty value in an ID. If the final registry excludes short values, the test must distinguish an unregistered value from an exemption in validation.
2. **Artifact provenance.** `TestDataDownloadClaimUsesCloudProvenance` refuses local artifacts as well as local binaries. Frozen §8.1 explicitly restricts binary execution by provenance and governs artifact reads through the read set. The proposed port uses read-set authority for artifacts and `trust_local_builds` for binaries. It does not add a new artifact restriction.
3. **Canonical writer checks.** The old source-grep tests pin implementation counts. The architecture requires one state writer and shared holder rules. The proposed port keeps the writer-owner assertion as a focused package architectural check and replaces helper call counts with transaction behavior. The lead can choose the repository's existing static-check mechanism without changing acceptance behavior.
4. **Coverage availability.** No required source is absent or unreadable. The old Postgres classification test needs a real Postgres run because the source report says SQLite did not establish that race. Skipping that backend must be reported as unverified coverage, not a pass.
5. **Unspecified route rights.** The frozen design fixes common capability boundaries but does not enumerate every retained API's account, service or operator grants. The route appendix lists the affected rights, including `/services`, as C2 assumptions requiring a source or lead decision. Those matrix cells remain launch blockers until their expected answers exist independently of the production policy.

## Review conditions

- Every old function and predecessor above has a disposition. A dropped mechanism still names the surviving behavioral concern when one exists.
- No test imports or invokes the old cache server, recreates agent-loss retry runs, depends on caller-supplied claim headers, or accepts duplicate lifecycle events.
- Final positive permission tests use valid requests. Expected grants come from independent architecture cases, not from the policy table's own output.
- Race cases coordinate at a real transaction or request-body boundary. A sleep that merely hopes to produce an ordering is insufficient evidence.
- Run package tests for local invariants and acceptance scenarios for cross-process behavior. The parked test counts are inventory counts, not launch coverage claims.

## W1 legacy planner retirement

The legacy dispatchRun fixture and its planner tests are removed with
`Store.AcceptPlan`. The retained concerns use the final contracts below.
These are SQLite or package proofs. No native placement or HTTP cell is closed
by this cut, and no additional archived T1 source identity is claimed closed.

| Retired tests or mechanism | Retained evidence |
| --- | --- |
| AcceptPlan insertion, one-plan replay and invalid-plan rollback | `TestPlanSealPersistsReplayAndExecutionSettings`, `TestPlanControllerHandoffKeepsExactReplay`, `TestAcceptedPlanUsesSharedStrictValidator` and `TestLifecycle/lifecycle-request-and-plan-seals/store-sqlite`. The SQLite cell also refuses the second insert and proves full rollback. |
| Required-secret scope diagnostics and optional absence | `TestPlanSecretPreflightNamesUnreadableScopes` checks every unreadable scope, the persisted detail, canonical error, one diagnostic, store time and successful admission of the readable required subset. Optional names are absent from that subset by the resolved contract. |
| Identifier secrecy and stale diagnostic writes | `TestAcceptedPlanIdentifierSecretsProperty` generates short, escaped and Unicode values across job, step and group IDs. `TestPreflightDiagnosticCannotOutlivePlanOwnerProperty` varies the owner's deadline across acceptance and diagnostic transactions. |
| Source depth validation and silent resource/retry clamping | Strict validation refuses negative source depth and explicit zero resources. Shared settings tests retain ceilings and resolved winners. Silent clamps, caller-selected spec hashes, v1 modifiers and RetryAuto are removed by the frozen design. |
| ReportAttempt output ownership and failed-attempt reuse | `TestBlobResultRecordsRejectForgedResultKeys` covers record team, namespace, job, run, attempt and generation. `TestResultRecordsFromAnEarlierAttemptCannotFinishARetry` refuses the earlier record before and after the retry ordinal advances. W1b's result fixture supplies SourceAttempt. |
| Child invocation deduplication and live-parent rights | `TestChildIntakePersistsLineageAndInvocationReplay` uses explicit intake keys. `TestChildIntakeCannotEscapeItsLiveParentProperty` varies parent run, job, team, generation, cancellation and terminal state. New invocation keys replace implicit live-child reuse. |
| Dependency verdicts, cancellation and application retry | Existing scheduling, cancellation-intent, holder-ordinal, leased-recovery and unit-billing tests retain these concerns through the final interfaces. The old settle-based automatic retries are removed. |
| Old launcher Job synchronization tests | `ReleaseUnit`, renewal and unit billing retain the store concerns. Safe real Job synchronization remains open on the W8/W9 award-generation and Job-instance contract gap. |

The selected package files are in `../w1-session19-tests.txt`. The session report
lists the remaining legacy surfaces and shared caller changes.

## W1 run-row heartbeat retirement

`TouchRunHeartbeat`, `TouchNodeHeartbeat`, `Run.LastHeartbeatAt`, and the
independent scheduler and Kubernetes dispatcher timestamp loops are removed with
Doctor's heartbeat-age finalizer. The following ports replace
those mechanisms' retained concerns. They close no additional archived T1
source-function identity or native placement cell.

| Retired concern | Retained evidence |
| --- | --- |
| Owner liveness, heartbeat expiry and unrelated job activity | `TestOwnerLeaseAloneDeterminesLivenessProperty` varies 24 renewal timelines through real claims. The exact owner lease alone controls recovery. Expired renewal refuses without changing work. Remote renewal preserves only the remote claim. |
| Doctor orphan finalization and recent-run grace | `TestRecovery/recovery-local-orphans/store-sqlite` uses the elected reaper, store clock and owner execution state through reopening. Started local orphans fail in place. Unstarted owners requeue. A separate heartbeat-age grace no longer governs recovery. |
| Owner renewal persistence and stale-host refusal | `TestRecovery/recovery-unstarted-lapse/store-sqlite` checks committed owner renewal before expiry, then recovery/reclaim and stale renewal/plan refusal through reopening. |
| Viewer notifications | Existing unit renewal fields and `EngineController.Renew` retain the owner transport contract. The legacy node-claim observer test retains node watch delivery. Real owner watched-job delivery and W2's production route binding remain integration obligations. |

The two Kubernetes dispatch-loop tests retire with that loop. The final store
renewal tests retain revoked, expired and stale-fence refusal. Native host shutdown
on renewal loss still needs W8's lease-keeper integration. The PostgreSQL
execution-start contention fixture now races its existing activity mutation
against starts. It remains a legacy fixture and has not been run here. The node
ordering fixture still updates rows in reverse order through StartNode. The local
API contention fixture uses its existing activity route instead of the removed
touch route. These are caller migrations, not final native proofs.

The package selection is in `../w1-session21-tests.txt`.

## W1 raw node status and withdrawal retirement

`SetNodeStatus` and `RevokeNodeReady` are removed from Store, Tenant and Client
with their HTTP handlers. Debug pauses use activity detail. The raw readiness
publisher remains pending its caller migrations.

| Retired coverage | Final proof or remaining integration |
| --- | --- |
| `TestNodeClaim_RevokeOnlyWhenUnclaimed` and the HTTP revoke assertions | `TestReadinessWithdrawalPreservesHolderAndAuthorityProperty` varies 32 sequences of withdrawal, replay and republication before probing expired or forged owners and claimed, running or terminal jobs. Refused transactions preserve the complete snapshot and event list. |
| Ready withdrawal, holder exclusion and terminal replay | `TestLifecycle/lifecycle-transition-replay-and-fencing/store-sqlite` uses real admission, plan acceptance, claims and transitions. It checks withdrawal after reopening, stale-start refusal, cleared terminal claims, absorbing terminal state and one continuous event sequence. Enrollment is a contract fake. |
| Raw status writes in client, scope and tenant fixtures | Redundant status writes retire. Existing activity writes carry debug detail. W3's debug fixture still calls CreateRun against the final schema and fails on the removed args_json column before reaching a pause. Production debug lifecycle evidence remains open with that integration. |
| `TestRunNode_MissingJobFinalizesDoneNodeWithEmptyOutcome` | Retired with the raw setter's two-write terminal state. Final completion records outcome and terminal state atomically. The adjacent missing-Job tests retain the Kubernetes dispatch behavior. Native Job and launcher correlation remain W8/W9 obligations. |

The package selection is in `../w1-session22-tests.txt`. Native route and
platform cells remain pending.

## Egress persistence cutover

The three tests in `pkg/store/egress_test.go` are replaced by final-schema proofs
in `egress_accounting_runtime_test.go`. Round-trip, monotone usage and expiration
now use additive tenant and replica rows, public day/month reports and the leased
pruner. Invalid writer identity and negative increments fail through `AddUsage`.
The controller's old flush/retry/restore tests are replaced by request delivery,
tenant attribution, nil-meter persistence, short-write, restart-alarm and loud
write-failure proofs in `egress_accounting_internal_test.go`. Timer-flush and
closing-month backlog expectations retire with that implementation.
The SQLite replica acceptance cell reads public egress totals after reopening.
Native/platform cells remain unclaimed.

The raw default-team `Store.GrantCredits`, `Store.ListCreditCharges` and
`Store.AppendEventOnce` wrappers are retired. Remaining fixtures select the
intended Tenant explicitly. The raw-paid-grant property now probes the retained
tenant entry points. Deployment price and policy fixtures use Operator.
Existing final-schema settings proofs retain defaults, validation, corruption,
restart, atomic rollback and serialized table-update checks through that owner.
`TestStoreDataUnmeteredSettlementNeedsNoCalculator` closes the shared settlement
helper's dependency on a calculator for unmetered work. No native cell is newly
marked complete by these caller migrations.
