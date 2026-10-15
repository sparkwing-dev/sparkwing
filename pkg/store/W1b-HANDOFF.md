# W1b handoff

W1b remains incomplete. This continuation closes checkout-to-refund lookup and
cross-path payment replay. It removes caller-directed checkout closure. W1's
admission, claim, transition, reaper, schema and guard files remain untouched.
No git writes or Sparkwing executable runs were made.

## Implemented in this continuation

- `payment_lookup.go` resolves provider payment IDs through the checkout's
  persisted grant association and configured test/live mode. Distinct card
  grants retain their provider payment reference. Empty IDs, ledger references
  presented as provider IDs, invalid associations and ambiguous grants cannot
  choose a payer or fund a refund.
- Console refunds use the original grant reference in `reverses`. Prior
  reversals count toward the cap. Partial and complete refunds replay after
  reopening the database, preserve negative balances after consumed credit,
  and roll back if their audit fails. Independent clients cannot refund more
  than one payment. Refund timestamps use the store clock.
- Payment warnings now find teams funded through verified checkouts. Card,
  checkout and paid-grant settlement share the payment identity lookup. Tests
  reproduced and closed double grants from card-to-checkout,
  checkout-to-paid-grant and paid-grant-to-checkout replay.
- Deleted `Tenant.CloseCreditCheckout`, `ErrCheckoutNotFound`, its controller
  handler/types/error constant, route registration and route inventory entry.
  Verified failure/expiry events own checkout outcomes. This removes 112
  obsolete production lines. The old 17-line raw payment lookup was replaced.
- Removed the obsolete closure-route test. Ported card/grant funding races and
  spent-balance refunds to final-schema fixtures. Verified checkout audits,
  failure/expiry, missing sessions and immutable paid credit already have
  store tests. Account/business-event tests retain their other assertions.

No other area's Go contract changed. C1 documentation now states the persisted
payment/grant association and the closure cut. Existing upload ticket, blob
record, tagged-log and metered-ledger implementations remain in place.

## Integration and remaining cuts

1. W1 must call `recordBillingAwardTx` with server-derived metering, resources
   and pricing. Issuance still creates unmetered awards here. Start compute
   intervals at execution, exclude customer-runner waits, charge renewals, and
   settle release/terminal/reap transactions at their persisted end or lapse.
   Reconciliation needs atomic memo materialization before memoized success.
   Data scopes need their permitted default/base refs.
2. W2 must implement C2 `SettleBilling`, provider verification and
   `VerifiedPaymentEvent.Event`, install payment-mode configuration, and wire
   `Store.SettlePayment`. Store tests fake provider facts and the calculator.
   They do not establish Stripe authentication or native billing acceptance.
3. Remove the legacy paid-grant routes with W2's payment cut, then delete
   `CreditGrantRequest.Checkout` and `markCreditCheckoutPaidTx` with their
   callers. Legacy blob/log/storage-credit and claim-time billing APIs still
   need their consumers migrated and their old implementations deleted.
4. W7 must wire upload reserve/lookup/commit/abort to physical backend evidence
   and trusted `ReplicaID`. Supply manifest reads and blob deletion. Shared
   record namespace/edge functions are stubs here and use contract fakes in
   store tests. Confirm the trusted-builder mapping for cloud provenance.
5. W1 must call both blob GC phases from leased maintenance. W2/W7 log signing
   must derive keys through `LogSegmentObjectKey` and persisted retention
   classes. Physical integrity, exact proxy egress, backend lifecycle rules
   and live-ring fallback remain external integration obligations.

## Contract changes needed

- W1 owns the schema and must coordinate mode identity for retained card
  attempts, payments, warnings, freezes and refund work with W2/W9. Those
  tables/API inputs still lack a test/live discriminator. Checkout lookup
  follows configured mode, but that cannot distinguish identical provider IDs
  in retained card state across a mode switch. No schema or foreign contract
  was changed to guess at this requirement.
- W1 owns `tenant_sql_scope_guard_test.go`. Move its cross-team exemptions from
  `(*Store).PaidGrantTeam` and `(*Store).RecordPaymentWarning` to
  `paidGrantForPaymentTx` and `paymentWarningTeamTx`. Provider payment IDs
  identify the team before the refund/warning can be scoped. The former
  validates the selected grant association before returning it. The latter
  resolves a card attempt when a grant has not arrived. The existing
  `(*Store).settlePaymentFacts` exemption is also missing. Other existing
  authority/retention guard findings remain in that file's owner scope.

## Verification

All Go checks use `GOWORK=off`, `GOFLAGS=-buildvcs=false`, `GOMAXPROCS=4`,
`-p 4`, and cache/temp directories beneath `/tmp/sw-astra-W1b/`.

- Final repository `go build -p 4 ./...` passed. `go vet -p 4` passed for
  `pkg/store/...`, `pkg/controller/...`, `internal/orchestrator/...`,
  `internal/acceptance/...` and `tests/acceptance/...`.
- Focused store tests passed 66 top-level tests / 131 including subtests, with
  no skips. Selection:
  `^(TestStoreDataMetered|TestLogPersistence|TestResult|TestBlob|TestPayment|TestReversePaymentTakesBack)`.
- `go test -p 4 ./internal/acceptance ./tests/acceptance -count=1` passed.
  The existing `storage-replica-safe-accounting/store-data-sqlite` boundary
  passes. No new native acceptance cell was activated. Native placements,
  PostgreSQL and real Stripe remain unproved.
- All `^TestPayment` tests passed under `go test -race -p 4`, including
  independent-client refund races and cross-path funding replay.
- The OpenAPI rewriter and API reference generator produced byte-identical
  output after the route cut.
  `bin/sync-docs.sh --check` passed. Removed exports are absent from generated
  API snapshots. Full snapshot drift remains in eight packages from the wider
  rewrite. Only this continuation's removed/changed entries were updated.
- The SQL tenant guard fails on the missing/moved exemptions above and
  existing authority/retention findings. The account/business-event fixture
  fails during sign-in with a foreign-key error. These are reported, not
  treated as successful checks. Prior full-store failures on legacy profile
  and removed `sparkwing_meta` fixtures remain outside this continuation.
- `git diff --check` passed. HEAD remains `20f3383ac`. No local Git
  identity/core.worktree override was found. All checks exited and the scratch
  directory was deleted.

## Shared files touched

The production caller cut changes only `pkg/controller/credit_reversals.go`,
`server.go` and `tenant_routes.go`. Matching test inventories change in
`pkg/controller/credit_reversals_test.go`, `operator_test.go` and
`internal/orchestrator/wingd_api_routes_test.go`. Documentation changes are
`api/openapi.yaml`, `docs/api-reference.md`, `docs/auth.md`, `CHANGELOG.md`,
their embedded mirrors, C1's contract notes, and the two affected `.apidiff`
files. All other edits are W1b-owned store files.

Mechanisms added: one private payment-to-grant lookup shared by existing
financial writers/readers. Separate lookups were the cause of cross-path
funding and missing checkout refunds. No setting, schema, compatibility API
or runtime service was added.

Friction: isolated Go caches require a cold dependency/race build. Legacy
fixtures and W1-owned guard exemptions prevent a full store-suite pass. The
remaining foreign implementations were tested through their contract fakes.
