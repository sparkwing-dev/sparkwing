# W1b handoff

Continues b2a908a86 after a clean worktree. W1 implementation files and the
lead-owned handoffs/amendments are unchanged. Earlier completed work and the
harness deletion mappings remain in the previous report and branch history.

Implemented:

- Extracted transaction-local owners for card setup, collection success/failure,
  payment reversal, dispute hold/release and queued refund outcomes into
  payment_mutations.go. Existing Tenant/Operator methods use those owners.
  They commit once, and return no successful outcome when the transaction fails.
  recordPaymentWarningTx now owns validation and the ledger lock too.
- These owners can run in SettlePayment's receipt transaction without nested
  transactions. TestPaymentMutationsRollBackWithReceipt drives all eight mutation
  families against a failing receipt fake, checks every financial row is unchanged,
  then commits and reopens the final-schema database. This is transaction evidence,
  not evidence that expanded verified events reach SettlePayment yet.
- Refund queue updates read retry state and update the exact attempt in one
  transaction under the ledger lock. Database failures propagate. Stale attempts
  remain no-ops. Negative persisted attempts fail loudly.
- Collection amounts use checked integer multiplication before changing the ledger.
  Generated unrepresentable amounts cannot grant credit or queue refunds.
- Card setup ordering stores the exact nanosecond completion time, falling back
  to receipt time only when completion time is absent. Older saves fail even when
  the existing timestamp has fractional seconds. Equal completion times replay.

Invariant: payment facts resolve to a persisted payer, and financial mutations,
audits and event receipts commit or roll back together. Generated refund tests
cover test/live modes, partial refunds, repeated refunds, failed receipt writes,
conservation and foreign-team balances. Generated setup tests cover fractional
completion times, late delivery, replay, stale receipt-time saves and reopening.

Deleted and ported:

- Deleted the 9-line refund-backoff helper that hid read failures and performed
  a separate read before the update. applyCardRefundTx owns that read now.
- Removed the 11-line card-setup timestamp marker block, its math import and the
  SQL modulo exception. This was a migration rule for old unordered saves.
- Removed 66 lines containing TestASetupCompletedBeforeTheCardOnFileIsRefused and
  TestASetupAfterAnUnorderedSaveIsNotStale. Their current ordering/replay/retained
  card assertions move to TestPaymentMutationCardSetupOrderingProperty. The
  assertions allowing an older unordered save or grandfathering its marker are
  retired with that compatibility rule. No archived finding is claimed closed.
- Existing mutation bodies moved, rather than being copied. No legacy credit,
  output or log runtime API is claimed retired in this session.

Local contract change:

Card.AddedAt and C1 now describe exact completion ordering, with receipt time as
its fallback. No timestamp encodes migration provenance. This follows the final
schema's nanosecond times and the prelaunch hard-cut rule. A caller that supplies
an older receipt time now receives ErrStaleCardSetup. W2 should always supply the
verified setup completion time. No other area's contract changed.

Contract changes and real integrations still needed, in priority order:

1. Lead 07:40 item 2 / A1-28 remains open. internal/capability/payment.go still
   exposes only checkout paid/expired/failed. W2 must provide the opaque verified
   setup, refund, warning, dispute and collection facts and provider handoff.
   W1 must supply matching payment_events schema and trusted setup/customer
   correlation. The current event table requires a checkout FK and restricts kind
   to the three checkout outcomes. SettlePayment still refuses zero proof and
   remains checkout-only. No metadata-selected payer or substitute public proof
   was introduced. Its future dispatcher can now call saveCardTx,
   reversePaymentTx, holdCreditFreezeTx, releaseCreditFreezesTx,
   recordPaymentWarningTx, settleCardPaymentTx, failCardAttemptTx and
   reportCardRefundTx under its receipt transaction. Setup and dispute routing
   must derive the payer from persisted correlation before calling tenant mutations.
2. A1-27 spend/grace needs W1's committing renewal/policy result and an exhaustion
   fence per award. accountUnitBillingTx still returns only error. The join must
   preserve each award's prepaid runway, customer-runner waits, unit minima and
   holder drain. C2's production calculator and W1's ordinary lifecycle accounting
   are already implemented. Legacy claim-credit callers still block their deletion.
3. Output clients still use OutputReadGrant/NodeOutputObject and unbound
   Store.GetNodeOutput. W2/W3/W9 must bind the authenticated reader and selected
   record key. Attempt-free materialization sources still conflict with positive
   destination-attempt keys/authority. W1/W7 must settle that edge contract and
   connect CopyMaterializedRecords to the transaction-local mutation owner.
4. Legacy LogSegmentUploader calls retired writer routes. Controller readers
   select node_logs, absent from the final schema. W2/W9 still owe authenticated
   blob.LogTransport signing/verification and final readers. W3 owes persisted
   tagged subjects. Final store persistence and logseg byte implementations exist.
5. Lead 09:40 cleanup still has W1b's store_billing, store_cache_scope, store_cas,
   store_data, store_gc_fence, store_presence, store_presence_large,
   store_reachability and store_reference_edges harness files. Some seed helpers
   also serve W7. No test-only acceptance cells were added while the runtime
   priorities above remain open.
6. Source-before-run binding, ReadBlob/ChargeDownload composition, physical
   allowance eviction and native/provider/PostgreSQL evidence remain open.
   A1-24 and A1-29 through A1-35 persistence remains implemented.

Verification:

All Go commands used GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-gocache and GOTMPDIR/TMPDIR=/tmp/sw-astra-W1b/gotmp.
Passed:

- go build -p 4 ./... for the whole repository.
- go vet -p 4 ./pkg/store ./pkg/controller ./tests/acceptance.
- 23 selected store tests / 50 passing cases, 6.726 seconds, no skips.
  Selection covers TestPaymentMutation*, TestPaymentSettlement*,
  TestPaymentWarning*, TestSharedFraudWarningHoldsEveryTeam,
  TestCardCollectionPaymentKeepsPersistedPayerProperty and the six final-schema
  debt, concurrent collection, decline, expired attempt and queued-refund tests
  in operator_card_billing_test.go.
- Existing billing-start-award-versus-start/store-sqlite and
  billing-ledger-survives-retention/store-data-sqlite acceptance cells, 0.243
  seconds, no skips. No new acceptance cell or registry entry.
- go run ./internal/commentcheck -base main . clean across 2,118 files.
- git diff --check.

No provider, PostgreSQL, native execution, whole-store suite, Sparkwing binary
or pipeline pass is claimed. The lead owns independent review and commits.

Budget and shared files:

Net production Go change this session is +23 lines. pkg/store is +3,668 lines
versus main, 7,918 over the combined -4,250 target. The added transaction seams
restore the required financial composition without new tables, settings, caches
or a framework. Legacy lifecycle/credit/output/log cuts remain necessary before
completion. The area is not within budget.

Shared edit: design/rewrite/contracts/c1-data.md documents the transaction seam
and setup ordering. All other edits are W1b-owned files in pkg/store. No W1
implementation file, foreign runtime caller, lead ledger or Git metadata changed.

Friction: the first broad TestADecline selector also ran the preexisting legacy
TestADeclineHoldsATeamWithPrepaidBalanceAtNew fixture, which fails on the removed
tokens table. The final selector names the final-schema collection test exactly.
The initial setup property advanced beyond UnixNano's date range. Its generated
seconds are now bounded while preserving arbitrary fractional timestamps.
Final selected checks pass. Sandbox/provider limitations remain unchanged.

Scratch /tmp/sw-astra-W1b is removed after final checks. /tmp/sw-gocache is kept.
No service or child process was started. Git author remains Korey Gambill and
core.worktree is unset. No harness memory was written.

STATUS: CONTINUE
