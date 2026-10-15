# W1b handoff

W1b remains incomplete. This continuation implements final-schema credit settings,
removes caller-selected checkout payment marking, and ports billing-cap and usage
proofs. It starts at `025adae72`. W1 admission, claim, transition, reaper, schema
and SQL-guard files are untouched. No Git writes or Sparkwing binaries were run.

## Implemented in this continuation

- `credit_settings.go` owns typed JSON reads and writes in `settings`. Missing
  keys select defaults. Invalid JSON, invalid integer/rate bounds and sealed
  values return errors. A rejected batch rolls back all writes. Stored prices
  survive reopening the database. Operator rate-table authority uses the shared
  advisory-lock helper. Public rate-table reads use one consistent snapshot.
- Removed `CreditGrantRequest.Checkout`, the HTTP grant request's `checkout`
  field and `markCreditCheckoutPaidTx`. Generic grants cannot mark a provider
  checkout paid or release its hold. Verified payment settlement owns the paid
  timestamp and grant association. HTTP requests carrying the removed field
  return 400. Grant timestamps use the store clock.
- Ported balance-cap, open-checkout, concurrent-checkout, late-payment and
  per-run-usage tests onto the final schema. Cap races and paid-event races use
  independent database clients. Verified paid events preserve funds above the
  balance cap. Expired checkouts keep purchase room during the settlement
  window. A settled payment counts once. Free-grant replay and team isolation
  remain covered. Usage totals net job/host reservations and refunds, exclude
  storage and other teams, and honor the result limit.
- The usage port reproduced a storage-charge insert naming the removed
  `credit_charges.token_prefix`. It now writes `capability_prefix`.
- Ported settings rollback and operator-writer races. Added corruption, restart
  and sealed-value coverage. Updated the unreadable-rate-table fixture to write
  the final settings table.

The patch replaces/removes 98 production lines, including 22 obsolete checkout
payment-marker lines. It removes 391 legacy test lines and ports their assertions.
`credit_billing_test.go` is deleted. Settings implementation now lives in the
owned `credit_settings.go`; it is not counted as a net code reduction.

Earlier sessions already implemented upload tickets and receipts, blob records,
reference authorization, tombstone GC, tagged logs, metered awards/intervals,
checkout payment settlement, checkout-aware refunds/warnings and cross-path
payment deduplication. Those implementations were retained and regression-tested.

## Verification

All Go checks use `GOWORK=off`, `GOFLAGS=-buildvcs=false`, `GOMAXPROCS=4`,
`-p 4`, and cache/temp directories beneath `/tmp/sw-astra-W1b/`.

- Final repository `go build -p 4 ./...` passed.
- `go vet -p 4` passed for `pkg/store/...`, `pkg/controller/...`,
  `internal/acceptance/...` and `tests/acceptance/...`.
- Store selection passed 85 top-level tests / 168 including subtests, no skips:
  `^(TestStoreData|TestLogPersistence|TestResult|TestBlob|TestPayment|TestReversePaymentTakesBack|TestCreditSettings|TestSetCreditSettings|TestUnsetRateTable|TestSetRateTable|TestAStoredRateTable|TestATableWithoutAFourCore)`.
- Race selection passed 30 top-level tests / 79 including subtests, no skips:
  `^(TestStoreDataCreditSettings|TestStoreDataMeteredUsage|TestPayment)`.
- `TestCreditGrantRequestRefusesCallerSelectedCheckout` passed in the controller.
- Acceptance packages passed. The existing
  `storage-replica-safe-accounting/store-data-sqlite` proof passes. No new native
  acceptance cells were activated because the placement driver remains unbuilt.
- API documentation was regenerated. `check-api-spec.sh`, `sync-docs.sh --check`
  and `git diff --check` passed. Generated `CreditGrantRequest` matches the updated
  snapshot. Wider snapshot drift remains in eight packages: controller,
  controller/client, projectconfig, match, store, storage/storeurl, sparkwing and
  storage. Only the changed grant field was cut from the committed snapshot.
- The first settings run failed against the removed `sparkwing_meta` table.
  The usage port failed against `token_prefix`. Both passed after their fixes.
- The full legacy suite and SQL tenant guard were not rerun. Prior legacy-profile
  fixtures and missing/moved SQL exemptions still need their owners' verification.
  PostgreSQL, native placements and real Stripe were not exercised.

## Integration and remaining cuts

1. W1 must call `recordBillingAwardTx` with server-derived metering, resources
   and pricing. Issuance still creates unmetered awards here. Start compute
   intervals at execution, exclude customer-runner waits, charge renewals, and
   settle release/terminal/reap transactions at their persisted end or lapse.
   Reconciliation needs atomic memo materialization before memoized success.
   Data scopes need their permitted default/base refs.
2. W1 owns the retained compute/admission settings callers. `SetComputeLimits`
   now writes through the final `setCreditSettingTx`, but `computeLimitsSQL`
   still selects `sparkwing_meta`. `SetFreeTeamSlots` still uses the legacy
   `upsertCreditSettingSQL` while its reader uses final settings. Migrate those
   callers without reintroducing a metadata table. That remaining SQL constant
   and the legacy `parseCreditSetting` cannot be deleted until their callers
   move. These W1 files were left untouched under the split ownership.
3. W2 must implement C2 `SettleBilling`, provider verification and
   `VerifiedPaymentEvent.Event`, install payment-mode configuration, and wire
   `Store.SettlePayment`. Store tests fake verified provider facts and the billing
   calculator. They do not establish Stripe authentication or native acceptance.
4. Remove the legacy paid-grant routes with W2's payment cut. Legacy claim-time
   billing, blob/log and storage-credit APIs still need their consumers migrated
   and implementations deleted. Retained storage charging/sweeping still uses
   principal-based operational rows and metadata watermarks. Fixing its ledger
   insert does not port that sweep to unique blobs or leased maintenance.
5. W7 must wire upload reserve/lookup/commit/abort to physical backend evidence
   and trusted `ReplicaID`. Supply manifest reads and blob deletion. Shared
   record namespace/edge functions are stubs here and use contract fakes in
   store tests. Confirm the trusted-builder mapping for cloud provenance.
6. W1 must call both blob GC phases from leased maintenance. W2/W7 log signing
   must derive keys through `LogSegmentObjectKey` and persisted retention
   classes. Physical integrity, exact proxy egress, backend lifecycle rules
   and live-ring fallback remain external integration obligations.

## Contract changes needed and open questions

No new foreign Go contract or schema was changed. C1 documents final settings
persistence and the removal of caller-selected checkout settlement.

- W1 must coordinate test/live identity for retained card attempts, payments,
  warnings, freezes and refund work with W2/W9. Those tables/API inputs lack a
  mode discriminator. Checkout lookup follows configured mode, which cannot
  distinguish identical provider IDs in retained card state across a mode switch.
- W1 owns `tenant_sql_scope_guard_test.go`. Move its cross-team exemptions from
  `(*Store).PaidGrantTeam` and `(*Store).RecordPaymentWarning` to
  `paidGrantForPaymentTx` and `paymentWarningTeamTx`. Provider payment IDs
  identify the team before refunds/warnings can be scoped. The existing
  `(*Store).settlePaymentFacts` exemption is also missing. Other previously
  reported authority/retention guard findings remain in that owner's scope.

## Shared files touched

- `pkg/controller/credits.go` removes the grant field and its store argument.
- `pkg/controller/credit_grant_request_test.go` checks the HTTP refusal.
- `api/openapi.yaml`, `.apidiff/pkg_store.txt`,
  `design/rewrite/contracts/c1-data.md`, `CHANGELOG.md` and the generated
  `pkg/docs/changelog.md` describe the changed surface.

All other changes are W1b-owned store implementation, tests and this handoff.

Mechanisms added: none beyond replacing the existing settings persistence in its
own file. No schema, setting key, environment variable, compatibility API or
runtime service was added. Friction remains in legacy consumers, foreign stubs
and wider snapshot drift. No local Git identity/core.worktree override was found.
All checks exited and the scratch directory was deleted.
