# W1b decision 112 production composition

Decision 112 now reaches the production controller constructor. `Tenant`
implements `BillingWriter`, and `New(...).Handler()` resolves it without an
injected writer. Checkout, saved-card setup, pay-now and owner budgets use
current member authority inside their committing transactions. Hosted-page
reservations persist before provider delivery. Completion saves the page and
provider correlation atomically. Replays reuse the reservation and saved URL.
An ambiguous delivery retains its provider key. An authority change during the
provider call prevents completion.

The production-route tests use real account sessions, CSRF, provider signature
verification, payment correlation and ledger settlement. They cover setup,
prepay raising standing, a declined automatic collection moving the team to
Held, and pay-now clearing that hold after successful collection. Repeated
requests and signed events retain one financial effect. External checkout and
Stripe HTTP transports are the only fakes in those flows.

Generated store tests refuse stale authority and changed financial facts.
Concurrent requests reserve once. A failed page write rolls back its provider
binding. Payment-mode changes refuse replay. Account removal and tenant purge
handle the financial replay rows. Existing standing capacity, hard deadline,
monthly/weekly collection, signup cap and running-work completion proofs remain.
No native Stripe or PostgreSQL acceptance cell is promoted.

## Contract changes made

C1 records the tenant-owned `billing_pages` table and its unique caller/team/key
identity, immutable reservation facts, failed-delivery state and bounded expiry.
It records the account-removal and tenant-purge behavior. C1 and C2 replace the
obsolete purchase-cap/trust gate wording with decision 112's funding rules.

C2 and `BillingPage` add `IntentID` separately from the hosted-session
`ProviderID`. Card setup responses supply `id`, `url`, `customer` and
`setup_intent`. Recovery responses supply `id`, `url` and `payment_intent`.
A returned recovery customer must match the reservation. Credits retain `id`,
`url` and optional `expires_at`. Provider identities correlate signed callbacks
without trusting webhook metadata. The checkout service must persist `key` and
`setup_id` idempotently. A webhook delivered before completion retries after the
correlation transaction commits.

`PaymentConfig.HTTPTransport` supplies an optional external HTTP transport.
Provider URL, credential validation, signature verification, timeout and
redirect policy remain owned by the verifier. Production-constructor tests use
this seam instead of replacing the verifier or the billing writer.

The raw `OpenCreditCheckout`, `AttachCreditCheckout`, `DropCreditCheckout`,
`SetSpendBudget` and `StartRecoveryAttempt` methods are removed. Package ledger
tests use test-only transaction fixtures. The controller's existing prepay proof
now uses the authorized writer. The unattached-checkout abort test retires
because ambiguous delivery must preserve the financial reservation. Its
replacement is `TestBillingPagesFailureRetainsReservationAndProviderBinding`.
No contract change remains blocked on another worker.

`Operator.SetPaymentMode` supplies the missing production writer for the existing
mode setting. Controller startup calls it from its validated `--payment-mode`
flag before serving. The constructor proofs configure it through this operator
method. Invalid values leave the persisted mode unchanged.

## Size and deletion

Against the session starting point `b0b8331c7`, store production Go is **288
added, 148 deleted, net +140**. The shared billing/capability production changes
are **39 added, 6 deleted, net +33**. Removed raw APIs account for 96 production
lines relative to the intermediate controller-writer commit. There are no
compatibility aliases or production test-fixture entrypoints.

Combined W1+W1b store production Go against main is **21,679 added, 25,849
deleted, net -4,170**, measured using the lead's exclusions for tests, testdata,
acceptance harnesses and paths containing `contract`.

Budget justification: this is **80 lines over the -4,250 budget**. The missing
production hosted-page implementation requires durable reservation/replay facts,
atomic provider correlation and startup mode persistence, which the frozen line
estimate did not size.
The implementation shares existing team authority, checkout, recovery and
collection transaction owners. A controller-only wrapper cannot provide atomic
replay or recheck authority after provider delivery. Removing the raw APIs cut
96 lines. The remaining overage needs the lead's budget ratification.

## Verification

All Go commands use `GOWORK=off GOFLAGS=-buildvcs=false GOCACHE=/tmp/sw-gocache`,
`GOMAXPROCS=4`, `TMPDIR=/tmp/sw-astra-W1b` and
`GOTMPDIR=/tmp/sw-astra-W1b`. Test concurrency is at most `-p 4`.

- Repository-wide `go build ./...` passes.
- `go vet ./internal/store ./internal/controller ./internal/capability ./cmd/sparkwing-controller` passes.
- Store billing-model, hosted-page, retained-billing and retired-API proofs pass.
  The table classification and tenant-column guards pass.
- Controller billing-model and HTTP page authority/replay proofs pass, including
  all three new production-constructor tests.
- Collection, payment, card and metered controller checks pass with the socket
  budget fixture excluded. Provider capability tests pass.
- Race checks pass for hosted pages, collection binding and production
  constructor flows. The final mode-configuration and production-constructor
  race rerun passes too.
- `TestPaymentConfiguration` passes in `cmd/sparkwing-controller`.
- Documentation policy/mirror/migration checks and commentcheck against main
  pass. `git diff --check` passes.

Reproduce the production billing proofs with:

```sh
go -C /home/bombuntu/.xwing/envs/1088/sparkwing test -p 4 ./internal/store ./internal/controller -run '^TestBillingPages|^TestBillingModel|^TestBillingPaymentMode|^TestStoreBilling|^TestBillingPageHTTPAuthorityAndReplayProperty' -count=1
```

Race selection is `^TestBillingPages|^TestBillingModelProduction|^TestCollectionBinding`,
with `-race -p 2`. The additional mode-property selection is
`^TestBillingPaymentMode|^TestBillingModelProduction`. Existing SQLite acceptance
proofs `TestStoreBillingStartAwardVersusStart` and `TestStoreBillingRetention`
pass. No native acceptance registry or cell is changed.

## Shared files and caller migrations

Production: `internal/capability/billing_pages.go`,
`internal/capability/payment.go`, `cmd/sparkwing-controller/main.go`,
`internal/controller/billing_checkout.go`,
`internal/controller/billing_routes.go`, `internal/store/team_mutations.go` and the store
billing/schema owners. `tenant_sql_scope_guard_test.go` follows the shared
collection helper's new name without widening its existing scope exception.

Tests: `internal/controller/billing_native_test.go`,
`internal/controller/billing_model_fixture_test.go`,
`internal/controller/billing_model_internal_test.go`,
`internal/controller/deletion_review_test.go` and
`internal/controller/team_write_routes_test.go`. Raw caller migrations also touch
`internal/store/billing_model_test.go`, `card_billing_test.go`,
`credit_exhaustion_runtime_test.go`, `deletion_billing_test.go`,
`deletion_test.go`, `operator_card_billing_test.go`,
`payment_checkout_caps_test.go`, `payment_checkout_test.go`,
`store_billing_boundary_test.go` and `team_mutations_test.go`.

Docs: C1 and C2 contracts, the proof inventory, `docs/billing.md`,
`docs/migrations/_unreleased.md` and their embedded mirrors. Lead-owned
`handoffs.md` and `amendments-*.md` remain unchanged.

## Friction and remaining real integration

The shared checkout advanced to `89da585fa` while this continuation was running.
An overlapping cleanup pass retired the raw billing APIs and migrated fixtures.
One intermediate check saw a temporary fixture compile error. A later race run
caught a test fixture changing an event timestamp between duplicate deliveries.
The fixture now retains the event creation time while signing each delivery.
The corrected race run passes. Final checks use
the combined implementation. No Git writes were made by this worker.

The native socket fixtures `TestCardBilling_OwnerSetsABudget` and
`TestBillingTrust_OperatorGrantsAndRevokes` cannot run inside the sandbox.
Owner-budget behavior is covered by the socket-free production
constructor test with real session authentication. Live Stripe/test-key checkout
service delivery, PostgreSQL and native full pipelines remain lead-owned
execution evidence. The external checkout service source is outside this repo.
Its response and idempotency contract is explicit above and exercised through
transport doubles. No controller or store billing composition remains stubbed.

Scratch `/tmp/sw-astra-W1b` is removed after checks. `/tmp/sw-gocache` is retained.

STATUS: COMPLETE
