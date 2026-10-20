# W1b raw billing API retirement

This continuation removes the raw billing entrypoints after the authorized
writer cutover. The production-page implementation and its provider tests were
being completed by another continuation in the same checkout. Those edits are
preserved. Its full implementation report is `W1b-HANDOFF.md`.

## Change and tests first

Invariant: the production tenant API offers no raw checkout, owner-budget or
pay-now mutation that bypasses the authorized billing writer.

`TestStoreRetiresTokenMeteredBilling` first failed on all five raw methods.
`OpenCreditCheckout`, `AttachCreditCheckout`, `DropCreditCheckout`,
`SetSpendBudget` and `StartRecoveryAttempt` are now absent. Production mutations
use the authorized page and budget writers. The generated current-authority
and immutable-reservation proofs exercise those writers.

The raw unattached-checkout abort path is deleted. Its test retires because
ambiguous delivery retains the financial reservation. The replacement is
`TestBillingPagesFailureRetainsReservationAndProviderBinding`. The existing
financial-identity test now uses the authorized page writer before and after
settlement. It no longer tests a fixture-only attachment method.

Store ledger and collection setup uses `billing_fixture_test.go` transaction
adapters. The remaining controller prepay/chargeback proof prepares and binds
its checkout through the real authorized writer. The unrelated deletion HTTP
fixture seeds a pending checkout directly. No runtime compatibility alias was
added. The operator credit-grant path and owner budget remain.

This continuation deletes **96 production Go lines, adds 0, net -96**, measured
against `89da585fa`. Other concurrent production edits are excluded from that
number. Policy numbers and the single policy table are unchanged.

## Verification

Commands use `GOWORK=off GOFLAGS=-buildvcs=false GOCACHE=/tmp/sw-gocache`,
`GOMAXPROCS=4`, `TMPDIR=/tmp/sw-astra-W1b` and
`GOTMPDIR=/tmp/sw-astra-W1b`. Go test concurrency is at most `-p 4`.

- Whole-repository `go build -p 4 ./...` passes after the raw API cut.
- `go vet -p 4 ./internal/store ./internal/controller` passes.
- The focused sweep passes 120 store tests and 11 controller tests, including
  all 26 billing-model tests, six real-store page tests, signed production
  card/prepay/recovery settlement, failure replay, identity binding and raw API
  retirement. Store billing start and ledger-retention proofs also pass.
- Docs mirror, embedded changelog and migration checks pass. The billing policy
  drift test passes. Commentcheck against main and `git diff --check` pass.

The focused selection was:

```text
^TestBilling|^TestCollectionBinding|^TestPayment|^TestProvider|^TestCard|^TestMetered|^TestStoreBilling|^TestStoreRetiresTokenMeteredBilling$|^TestHoldsAndRevocation$|^TestMonthEndBillsASmallDebt$|^TestSettlementChecksTheAttemptAndTheFrozenAmount$|^TestABudget|^TestAWarning|^TestAWarned|^TestADecline|^TestADebt|^TestACheckout|^TestAnAttempt|^TestConcurrentDebt|^TestTeamDeletion|^TestTeamMutations|^TestTeamInvitation
```

Use `go test -p 4 ./internal/store ./internal/controller -run <selection> -count=1` with
`-skip '^TestCardBilling_OwnerSetsABudget$|^TestBillingTrust_OperatorGrantsAndRevokes$'`.
The second socket fixture initially failed with `operation not permitted`.
The corrected controller selection passes. Store results from that same sweep
pass. These two listener fixtures require the lead's unsandboxed execution.

`TestBilling` in `tests/acceptance` still skips native target cells as contract
skeletons. Its successful package exit is not native product evidence. No
native cell is promoted. Existing SQLite start/settlement and financial
retention proofs remain passing. The inherited missing W1 port destinations
remain outside decision 112.

`go test -race -p 2 ./internal/store ./internal/controller -count=1` passes with
`-run '^TestBillingModel|^TestBillingPages|^TestCollectionBinding|^TestBillingPageHTTPAuthorityAndReplayProperty'`.
It covers standing admissions, collection holds, hosted-page authority and
reservation replay through real transactions and handlers.

## Contract and caller changes

C1 records the raw API removal. The port inventory maps retired checkout tests
to the authorized writer proofs. Migration notes name the replacement methods
and preserve ambiguous-delivery replay. The docs mirror is regenerated with
`bin/sync-docs.sh`. No lead-owned handoff or amendment is changed.

Production files owned by this continuation: `internal/store/card_billing.go` and
`internal/store/credit_checkouts.go`.

Caller migrations: `internal/store/billing_model_test.go`, `card_billing_test.go`,
`credit_exhaustion_runtime_test.go`, `deletion_billing_test.go`,
`deletion_test.go`, `operator_card_billing_test.go`,
`payment_checkout_caps_test.go`, `payment_checkout_test.go`,
`store_billing_boundary_test.go` and `team_mutations_test.go`.
The new fixture is `internal/store/billing_fixture_test.go`.

Shared controller files: `internal/controller/billing_model_internal_test.go`,
`internal/controller/deletion_review_test.go` and the new
`internal/controller/billing_model_fixture_test.go`. The production-route probe in
`billing_native_test.go` is the other continuation's work.

Shared documentation: `design/rewrite/contracts/c1-data.md`,
`design/rewrite/contracts/t1-port-inventory.md`,
`docs/migrations/_unreleased.md` and its embedded mirror.
Other concurrent billing docs, schema, provider and startup edits are preserved.

## Remaining decisions and integration

The lead's exact size counter excludes every path containing `contract`, but
includes the store fixture-helper packages. A preliminary count that excluded
those packages was wrong. The durable production-page writer leaves the
combined area over budget. At the final count it is **21,679 added, 25,849
deleted, net -4,170**, which is **80 lines over** the -4,250 budget.
`W1b-HANDOFF.md` carries the durable writer's justification for lead
ratification. The raw API cut reduces the overage by 96 lines. The final count
also includes the concurrent startup payment-mode persistence.

Live checkout-service delivery, Stripe test-key execution, PostgreSQL, browser
and Kubernetes proofs remain lead-owned. External checkout transport and Stripe
reads are faked in the passing constructor proofs. The real store, controller,
authority checks, signature verification and settlement run in-process.
No additional contract change is requested by this cleanup.

The shared cache lost files during one compilation, then repopulated. A fresh
invocation passed. Neither this continuation nor its cleanup deletes the shared
cache. Concurrent startup payment-mode persistence appeared after the focused
sweep. Its owner must verify that final change. No Git writes were made.

The final repository build passes with startup payment-mode persistence present.
Commentcheck is clean across 2,584 files. Scratch `/tmp/sw-astra-W1b` is removed.
The shared build cache `/tmp/sw-gocache` is retained.

STATUS: CONTINUE
