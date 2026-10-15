# W1b store-data continuation

Session base: 9dc5f609a. The interrupted merge was already committed. The starting
worktree was clean, and no unmerged paths remain. No Git write ran.

## Implemented

`Operator.RecordAttemptIntent` now commits the collection payment correlation
and exact attempt binding in one ledger-locked transaction. Team and charge come
from the persisted attempt, with their same-team relation checked. The trusted
worker passes the customer used in its provider request. A saved-card replacement
during that request cannot substitute the new customer. Mode comes from the
configured store setting. Currency is USD, matching the declared ledger contract.

Exact replay writes nothing. Changed payer, customer, attempt, charge, kind,
currency, mode or payment fails without changing the attempt or finances. A
payment already assigned to a checkout cannot also fund a collection. Failure
at either the correlation insert or attempt update rolls back both writes.
The provider collection fixture now calls this writer instead of inserting its
correlation by hand.

The invariant is: a provider payment settles only its persisted collection
attempt, charge and payer, and a refused binding changes no financial state.
Four new tests cover both rollback boundaries, reopening, immutable replay,
saved-card replacement before binding, checkout-payment refusal, and eight
explicit plus 32 generated association substitutions.

The customer argument changes W1b's existing Operator API. Its production caller
and test callers are migrated. No other area's declared contract is changed.
No schema fragment, new interface, environment variable or policy is added.

Removed four unused exports: ErrMeteredInProcessNodes, LiveLogOffsetHeader,
LiveLogAttemptHeader and LogWatchHeader. Their declarations and comments account
for 20 deleted production lines. The raw, unassociated attempt update is also
replaced. Across production Go files the diff adds 56 lines and deletes 35,
for a net **+21 lines**. Of these, pkg/store adds 55 and deletes 34. The shared
controller caller adds and deletes one line.

## Existing priorities and acceptance

12:15 remains closed through memoization. All six cases of
TestMaterializedResultCopiesBytesThroughControllerAndHost pass again. The real
store selects a memo, copies committed output/manifest selectors through the
controller wire and host, deletes the source run and memo, then reads the exact
output and artifact bytes through persisted authorization and filesystem signing.
Missing or corrupt output, manifest and members become executable misses.
The proof also checks charging, substituted reader identity and revocation.
W7 can proceed with its legacy output/log cut.

07:40 item 2 retains all seven non-checkout settlement outcomes. The new binding
writer closes the collection-association implementation gap, against the schema
fake. Receipt persistence, provider mode isolation and setup association still
need the integrations below. No production provider-readiness claim is made.

09:40 item 1 remains closed for W1b. Its nine store boundary proofs already live
in pkg/store tests. Remaining internal/acceptance/store_* files belong to W1's
claim, plan, credential, schedule, capacity and recovery work. No scenario catalog,
Harness or boundary registry changed. No new native acceptance cell or archived
T1 test port is claimed in this continuation.

## Required integration and contract handoffs

- W1 must install the existing C1 payment_events and payment_correlations schema
  contract in its final schema.sql. RecordAttemptIntent now requires that table
  and fails without it. There is no fallback to the unassociated legacy update.
- W1 still owns mode isolation for retained card fingerprints, warnings and
  collection rows. Raw legacy financial rows do not become provider authority.
- W2 owns the trusted billing-page setup correlation and production route
  composition under the lead's 14:40 decision. W1b's collection writer now exists.
  Provider-network/configuration composition and setup reservation persistence
  remain unproved. The collection worker supplies its actual request customer.
- Renewal-time credit/spend exhaustion and grace remain open. The final accounting
  join has no declared per-award exhaustion anchor or cancellation result for
  every unit kind. W1's renewal/state owners must expose that policy boundary.
  Reusing the legacy node-only anchor for owner units would not satisfy it.
- Coalesced copies still need a declared persisted cross-run read edge. Memoization
  satisfies 12:15. Spec equality and the legacy waiter table grant no such read.
- Legacy claim-credit and local output helpers still have W1/W3 production callers.
  Their lifecycle migrations remain required before the backing code can go.

The combined W1+W1b production delta against main is **+3,609 lines**:
14,836 added and 11,227 deleted, excluding tests, testdata, acceptance and paths
containing contract, matching the lead's budget script. This is **7,859 lines**
over the -4,250 target. The added transaction checks implement the required
provider correlation, replay and rollback contract inside the existing owner.
The old single UPDATE cannot enforce that contract. No budget waiver or COMPLETE
claim is made. Larger legacy retirement still depends on the caller migrations
above.

## Files

Production edits: pkg/store/operator_card_billing.go, credits.go and node_logs.go.
Tests: payment_collection_binding_test.go, payment_provider_settlement_test.go,
operator_card_billing_test.go and card_billing_test.go.
Source documentation: pkg/store/STORE-DATA.md and this report.
Shared files: pkg/controller/card_billing.go (one customer argument),
design/rewrite/contracts/c1-data.md and .apidiff/pkg_store.txt.
W1's production files and the lead-owned handoffs/amendments are unchanged.

The earlier materialization integration files remain:

- W9: internal/controllerwire/engine.go and pkg/controller/engine_snapshot.go.
- W3: internal/nodehost/output_records.go, owner_evaluation.go,
  owner_schedule.go, remote_contract.go, sdk_code.go and sdk_replay.go.
- W7: internal/blob/authority.go, keys.go, object_contract.go and objects.go,
  plus pkg/storage/manifest.go.
- Controller binding: pkg/controller/data.go.

None of those materialization files changed again in this continuation.

## Verification and friction

Go commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-gocache, TMPDIR/GOTMPDIR=/tmp/sw-astra-W1b and -p 4.

- Whole-repository go build -p 4 ./... passes on the final production source.
- Scoped go vet passes for pkg/store, pkg/controller, internal/nodehost and
  internal/blob.
- Provider, binding, payment-mutation, current collection and tenant SQL scope
  tests pass in 7.817 seconds. The final rollback-boundary addition passes in
  the separate TestCollectionBinding run, 0.224 seconds.
- The six real-byte materialization cases pass in 0.388 seconds.
- Final blob/log/materialization/metered boundary selection passes in 84.070
  seconds. It includes the real-store byte proof and the moved W1b store proofs.
- commentcheck -base main . passes across 2,165 Go files.
- pkg_store API output matches its regenerated snapshot. The full snapshot check
  finds pre-existing W9 client drift: RotatedToken.New now uses
  capability.Metadata and TokenInfo/TokenSubject are absent. That other snapshot
  remains unchanged.
- git diff --check passes. Local Git user identity/core.worktree overrides are
  absent.

Initial new tests failed because the old writer committed no correlation and
accepted a conflicting persisted payer. The added checkout-payment test also
failed before its refusal was implemented. Those tests now pass.

Two overly broad test selections reached inherited legacy fixtures. The first
failed TestADeclineHoldsATeamWithPrepaidBalanceAtNew on the removed tokens table.
The second failed TestCreditLedgerTotals_SplitsGrantsAndCharges and
TestCreditLedgerTotals_SettledSecondsNeverFall because their raw paid grants are
refused. Those are not passing evidence or new regressions. The final selection
excludes those legacy fixtures. No whole-repository test suite was run.

The lead still owns independent review, pipelines, PostgreSQL, native runtime
and provider-network evidence. No Sparkwing executable, service, deployment or
release ran. Scratch is deleted after checks complete. Shared cache is retained.

STATUS: CONTINUE
