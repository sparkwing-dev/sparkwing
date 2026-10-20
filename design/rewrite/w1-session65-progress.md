# W1 session 65

Starting store suite: **15 failures, 1,205 passing top-level tests**
(206.692 seconds). Ending store suite: **15 failures, 1,206 passing top-level
tests** (212.853 seconds). The failing set is unchanged and belongs to W1b's
billing and storage split. No invariant was relaxed and no failing test was
skipped or deleted. The original 214-failure inventory is stale.

## Implementation and cuts

Removed `Store.CreateTeam`, `Store.AcceptInvitation` and `Store.UnlinkIdentity`.
Their fixture callers use the final account transactions under authenticated
direct sessions. The shared storetest helpers authenticate member and roleless
accounts and can retain an existing session for unlink checks. Creation and
invitation helpers no longer update the account selection twice. The single
selection transaction owns that update and its session update together.
Unlinking reads only the provider subject needed for deletion and returns only
the ended-session count.

The retained expiry tests now advance the store clock, keep the account session
live and assert unchanged rows at the invitation's exact expiry and one second
later. The seven-day lifetime assertion remains. Unlink fixtures retain last
method protection, session revocation, stale identity refusal and relinking.
PostgreSQL contention fixtures create authority before taking their gates and
recognize the final transaction's team lock. Their native execution remains a
lead check.

The initial merged checkout could not compile because log-tree traversal still
called the removed `Tenant.ListSpawnedChildrenByRun`. Its caller now pages the
retained tenant run listing by exact parent. The HTTP property generates more
than 100 siblings, a nested child, mixed start ordering, a cycle and a foreign
tenant's child naming the same parent. Each selected descendant appears once
and foreign events remain absent.

Production delta against starting HEAD: **-22 lines**, with **85 added and 107
deleted** under the lead's filter. That filter counts the 49-line test-only
storetest helper. Excluding that fixture helper gives **-71 runtime lines**.
Combined store production is **-3,145 against main**, still **1,105 lines over**
the -4,250 budget. This is not a completion or budget-waiver claim. No runtime
interface, schema, flag, setting, retry or compatibility path was added.

## Evidence

Whole-repository build and scoped store/controller/acceptance vet pass. Focused
account ports, account race tests, real account HTTP acceptance and the log-tree
and log-page tests pass. Existing StoreSessions, StoreTransitions,
StorePlanSeals, StoreClaimMatching, StoreEnrolledClaims and StoreLauncherSync
checks pass in the ending suite. No new native matrix cell is unskipped.

Commentcheck against main, API snapshot, generated API documentation,
embedded-document drift and whitespace checks pass. Snapshot regeneration also
captures the merged RunView.Failures and ListRunViews declarations. Sleepcheck
against this session's HEAD passes. Against main it reports unchanged waits in
internal/source/checkout_test.go and tests/acceptance/session_flow_http_test.go.

The controller invitation lifecycle probe still fails in its inherited fixture
before reaching invitation acceptance because it inserts into the deleted tokens
table. The real account HTTP acceptance test reaches the concrete store adapter
and passes. PostgreSQL contention, native sockets, Windows and product pipelines
remain lead checks. Exact failures and focused evidence are recorded in
[w1-session65-store-failures.txt](w1-session65-store-failures.txt).

## Remaining contracts and integration

Ordered placement, rotation grace, Cloud metering and the SyncLauncher store
fence remain implemented. Trusted schedule/App intake still needs C4's tagged
committing origin in IntakeFacts and SealedRequest and W2's transaction-readable
verified delivery contract. AuthorityRef cannot represent an arming account or
verified delivery. The existing intake policy seams remain tested with fakes.
No credential is synthesized or foreign contract extended. cronLauncher and
startGitHubAppRun still reach triggerIntake/triggerRows and raw combined insertion.

C2/C4 maximum-concurrency and claim queue-depth inputs, publisher metric binding,
additive usage retry identity and sealed GitHub completion facts remain open.
C2's QueuePriorityWriter still lacks its W1 committing adapter and C1 persisted
rank contract. Raw trigger/token API retirement and W1b's billing/blob/log cuts
still prevent completion. No contract change in another area was made.

## Shared files and friction

The production caller change outside store is internal/controller/log_pages.go.
Shared controller tests are log_pages_test.go, team_boundary_test.go and
invitation_lifecycle_team_test.go. Three billing-related files receive only raw
account caller substitutions under the lead's scope clarification:
internal/store/business_events_test.go, credit_freeze_standing_test.go and
deletion_billing_test.go. W1b production and billing/storage fixtures beyond those
caller substitutions are unchanged.

C1, the port inventory, changelog, migration guide, embedded copies and API
snapshot are updated. The exact file list is
[w1-session65-files.txt](w1-session65-files.txt). Lead-owned handoffs and
amendments are unchanged. No Git write ran. HEAD remains 2bd91453d, and local Git
config contains no user identity or core.worktree override. Scratch is removed
after verification. The shared build cache is retained.

STATUS: CONTINUE
