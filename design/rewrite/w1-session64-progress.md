# W1 session 64

Starting store suite: **15 failures, 1,199 passing top-level tests**
(201.313 seconds). Ending store suite: **15 failures, 1,204 passing top-level tests** (204.028 seconds). The failing set is unchanged. The supplied
214-failure inventory is stale. W1b's production and fixture files are unchanged.

## Implementation and cuts

Store implements all four methods in C2's AccountWriter. Account routes use the
concrete default without a supplied writer. Each transaction reloads the same
live direct account session, its ancestry, membership and account. Changed
identity, issuance time, selected team, route alternative, revoked/expired
sessions and capability-parented sessions refuse mutations. Existing team rows
lock lexically before the account and authority rows.

Team selection commits the account's active team and session selection together.
Creation and invitation acceptance reuse the existing transaction helpers and
commit their membership/admission, audit and selected session together. A failed
session update rolls everything back. Invitation acceptance has one winner
across two direct sessions. Unlinking rechecks the ten-minute sign-in window on
the store clock, preserves the selected hashed session and last sign-in method,
and ends the account's other sessions.

The timestamp regression first failed because the account's creation time lost
123 nanoseconds. Account, membership and identity writers and readers now use
nanoseconds. Signup windows subtract duration nanoseconds, and waitlist and
account-deletion inventory readers decode the same unit. The retained account,
signup, linking and deletion selection passes after the port.

SetActiveTeam and SwitchSessionTeam are deleted. Four fixture calls migrate to
SelectAccountTeam. Unused AccountIdentity, GitHubRepo.CanonicalURL,
MetricSample.OneShot, IsBusyErr and Tenant.CacheExcludedCounts are deleted.
The last removes 40 production lines and its 71-line fixture-only test.
Profile folding retains its memoized-job exclusion checks.

Production delta: **+69 lines**, with **199 added and 130 deleted**, against
starting HEAD under the lead's filter. Combined store production is **-3,183
against main**, still **1,067 lines over** the -4,250 target. This is not a
completion or budget-waiver claim. The addition implements the previously
missing AccountWriter contract and shares account logic through transaction
helpers. It adds no interface, schema, setting, environment input, retry,
cache or compatibility fallback. Raw CreateTeam, AcceptInvitation and
UnlinkIdentity fixture APIs still need caller ports and deletion.

## Evidence

Whole-repository build, store/controller/acceptance vet, account transaction
properties, focused race, retained account/signup/linking/deletion tests and
socket-free HTTP checks pass. The HTTP probe drives creation, selection,
invitation acceptance and identity unlinking through the real default store
adapter. It asserts interface conformance. Invalid-body, method, identity and
transaction-error HTTP tests pass. Their obsolete missing-writer expectation
retires because the default now exists.

The SQL-scope guard initially failed after the helper extraction. Its inventory
now follows the two extracted statements and the invitation lookup, with their
cross-team reasons. The guard passes without changing its enforcement. API
snapshot, generated API documentation, embedded-doc drift, commentcheck against
main and whitespace checks pass. The new transaction property generates ended
sessions, stale identity/team, lost membership, wrong route rules and actual
capability ancestry. The precision check inspects public and persisted facts.

The broader controller selection has eight inherited failures, all before the
relevant handler because fixtures still insert into the removed tokens table.
Its original failures and the store failures are retained in
[w1-session64-store-failures.txt](w1-session64-store-failures.txt).
Existing StoreSessions, StoreTransitions, StorePlanSeals, StoreClaimMatching,
StoreEnrolledClaims and StoreLauncherSync cells remain green. No new native
matrix cell is unskipped. Native PostgreSQL contention, sockets, Windows and product pipelines remain
lead checks. No native matrix cell or release is claimed.

## Open contracts and remaining work

Trusted schedule/App admission still needs C4's tagged committing origin in
IntakeFacts and SealedRequest, plus W2's transaction-readable verified delivery
contract. AuthorityRef cannot represent an arming account or a verified delivery.
The current IntakePolicy/capability seams remain tested with fakes. No credential
is synthesized and no foreign contract is extended. cronLauncher and
startGitHubAppRun still reach triggerIntake/triggerRows and raw combined insertion.

Ordered placement, rotation grace, the SyncLauncher fence and Cloud metering
remain implemented. C2/C4 maximum-concurrency and claim queue-depth inputs,
publisher metric binding, additive usage retry identity and sealed GitHub
completion facts remain open. Raw account, trigger and token API retirement and
W1b's billing/blob/log cuts still prevent completion. No foreign contract
signature changed. The new account methods match C2's existing declaration.

The only shared caller file outside pkg/store is
tests/acceptance/account_mutations_http_test.go. C1, the port inventory,
changelog, migration guide, embedded copies and API snapshot are updated.
The exact list is [w1-session64-files.txt](w1-session64-files.txt).
Lead-owned handoffs and amendments are unchanged. No Git write ran. HEAD is
e80264d0e, and local Git config has no user identity or core.worktree override.
The W1 scratch directory is removed after final verification. The shared build
cache is retained.

STATUS: CONTINUE
