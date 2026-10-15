# W1b store-data continuation

Session base: 3d8dac68d. The merge was already committed. The starting tree was
clean, and the index has no unmerged paths. No Git write ran.

## Implemented and deleted

Removed the uncalled Tenant.FailNodeForUnpricedClass writer, its private
cancelMeteredNode implementation and EventKindCreditsUnpriced. This deletes
59 production Go lines, including its unused JSON import. There is no replacement
wrapper. Terminal writes use the existing fenced TransitionNode owner.

TestMeteredUnpricedClaimLeavesWorkUnchanged replaces the legacy unpriced-node
cancellation test. Four cases cover named and queued claims, on runner and Cloud
paths. The real store refuses pricing without changing the job, balance, attempts,
events, awards or intervals. The former writer's automatic terminal mutation is
retired with its unused API.

TestPaymentDeclineHoldsPrepaidClaimsUntilCollectionSucceeds replaces
TestADeclineHoldsATeamWithPrepaidBalanceAtNew. The old fixture failed against the
removed tokens table. Trusted provider failure facts now settle with their
receipt, a free grant leaves the payer held, and real ClaimUnit refuses without
an award. The collection retry uses RecordAttemptIntent and the same settlement
owner. Success clears the hold, restores trust and permits a real awarded claim.
Provider verification and the final payment schema remain contract fixtures.

The two replaced tests delete 90 test lines. The new tests live in clearly
owned store files. The API snapshot and source documentation reflect the cut.
No new mechanism, setting, limit, schema fragment or contract type is added.

Production Go delta for this session: **-59 lines** (0 added, 59 deleted).
Combined W1+W1b against main: **+3,550 lines** (14,834 added, 11,284 deleted),
excluding tests, testdata, acceptance and paths containing contract. This remains
**7,800 lines over** the -4,250 target. There is no budget waiver or COMPLETE
claim. Remaining legacy claim and output callers prevent the larger cuts below.

## Priorities and acceptance

12:15 remains closed through memoization. All six cases of
TestMaterializedResultCopiesBytesThroughControllerAndHost pass. The real store
selects a memo, projects committed output and manifest selectors through the
controller wire and host, deletes the source run and memo, then reads exact
output and artifact bytes through persisted authorization and filesystem signing.
Missing or corrupt output, manifest and members become executable misses.
Charging, substituted reader identity and revocation remain covered.

07:40 item 2 retains all seven non-checkout provider outcomes. The new decline
port reaches the real claim transaction before and after collection. Production
provider composition and final schema integration remain open.

09:40 item 1 remains closed for W1b. Its nine store boundary proofs already live
in pkg/store tests and pass in the final scoped run. Remaining acceptance/store_*
files belong to W1. No scenario catalog, Harness or boundary registry changed.
No new native acceptance cell or archived T1 source identity is claimed.

## Required handoffs and remaining work

- W1 billing defect: TestMeteredRootPolicyCannotRewriteAwardProperty fails in
  eight Cloud cases with RootMetered=false. authority_issue.go constructs the
  Cloud award's Billing.Metered only from the root flag, violating A59's launcher
  metering rule. The existing property reproduces this before these edits.
  W1 must derive metering from the launcher service authority as well as the root
  and apply the same rule in queuedUnitTx's billing eligibility test. These are
  W1-owned files and remain unchanged. This is an implementation defect under
  the existing contract, not a request for a new contract.
- W1 must install C1's payment_events and payment_correlations schema. The
  provider tests use the existing schema fake. W1 also owns mode isolation for
  retained fingerprints, warnings and collection rows.
- W2 owns trusted setup correlation and production provider route composition
  under the lead's 14:40 decision. The collection binding writer already exists.
  Provider configuration/network behavior and setup persistence remain unproved.
- Renewal-time credit/spend exhaustion and grace remain open. The accounting
  join has no declared per-award exhaustion anchor covering all unit kinds.
  W1's renewal/state owners must expose the policy boundary. The legacy node-only
  anchor cannot supply owner-unit semantics.
- Coalesced copies need a declared persisted cross-run read edge. Memoization
  satisfies 12:15. Spec equality and the legacy waiter table grant no such read.
- Legacy claim-credit methods still serve Store's old claim/trigger writers,
  claim_run.go, launch_sync.go and node_attempt.go. Their W1 lifecycle migration
  must precede removal of the backing helpers.
- Legacy local output helpers still serve store.go/node_attempt.go and W3's
  scheduler, rehydration and local routes. Their final-record migration must
  precede deleting node_output.go's remaining implementation.
- The old controller TestCredits_AClaimAboveTheLargestClassFailsTheNode still
  targets the removed /nodes/claim route and expects its old automatic failure.
  Its token/paid-grant fixture needs retirement with that route's retained tests.

No foreign contract change is proposed. Lead review, pipelines, PostgreSQL,
native runtime and provider-network evidence remain open.

## Files

Production: pkg/store/credits.go only. W1's files are unchanged.
Tests: pkg/store/metered_price_test.go, payment_decline_test.go,
credit_rates_test.go and card_billing_test.go.
Documentation: pkg/store/STORE-DATA.md, this report,
design/rewrite/contracts/t1-port-inventory.md and .apidiff/pkg_store.txt.
The inventory and API snapshot are the only shared files changed this session.

The earlier materialization integration files remain unchanged:

- W9: internal/controllerwire/engine.go and pkg/controller/engine_snapshot.go.
- W3: internal/nodehost/output_records.go, owner_evaluation.go,
  owner_schedule.go, remote_contract.go, sdk_code.go and sdk_replay.go.
- W7: internal/blob/authority.go, keys.go, object_contract.go and objects.go,
  plus pkg/storage/manifest.go.
- Controller binding: pkg/controller/data.go.

## Verification and friction

All Go commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-gocache, TMPDIR/GOTMPDIR=/tmp/sw-astra-W1b and -p 4.

- Whole-repository go build -p 4 ./... passes.
- Scoped go vet -p 4 ./pkg/store passes.
- commentcheck -base main . passes across 2,167 Go files.
- The six real-byte materialization cases pass in 0.354 seconds.
- The final scoped blob/log/materialization/payment/binding/billing-boundary and
  ledger-total run passes in 56.302 seconds with a five-minute test deadline.
  Its selection excludes the known failing root-policy property and legacy
  metered-token fixtures. The final strengthened decline test separately passes
  in 0.046 seconds, including restored claim issuance after collection.
- The unpriced claim port passes all four cases in 0.103 seconds.
- pkg_store API output is regenerated. git diff --check passes.

The initial broad TestMetered selection failed the root-policy property above,
plus inherited token/schema fixtures in claim, credit-rate, trigger-credit and
execution-attribution tests. Those failures are not passing evidence and are
not new regressions. The root-policy failure is retained as an actionable W1
handoff. No whole-repository test suite was run.

Xwing context inspection failed because it tried to create a lock directory
outside the writable worktree. The explicitly assigned environment was reused.
An initial materialization selection named pkg/controller, which contains no
matching test. The pkg/store run above is the actual materialization evidence.
No Sparkwing executable, service, deployment or release ran. Scratch is deleted
after checks finish. The shared build cache is retained.

STATUS: CONTINUE
