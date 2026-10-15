# W1b credit snapshot and billing harness continuation

Base: 5f80623f3. Net production Go delta in pkg/store: **-55 lines**
(59 added, 114 deleted). The relocated acceptance harness removes another
**107 ordinary-package Go lines**, excluded from the lead's area-budget count.
Combined store delta against main is **+3,058**, which is **7,308 lines over**
the -4,250 budget. The remaining overage is not justified. This area is incomplete.

`Tenant.CreditState` reads grants, charges, storage spend, burn, exhaustion and
prices in one existing snapshot transaction. The old separate Store read owner,
its exhausted-at reader and its separate balance-query owner are deleted.
`CreditBalanceMicro` uses the existing row-query balance owner. No schema,
price, restriction, environment variable, interface or configuration is added.

Invariant: one tenant credit report describes one committed financial snapshot.
The generated regression uses a real read-only SQLite connection and commits a
ledger/price update between the aggregate and burn reads. Against the previous
implementation it returns old charges with the new burn and rate and fails.
The new reader retains the original snapshot. The next report sees the complete
update. Sixteen generated amounts pass without sleeps or timing races.

Removed `Tenant.GrantCredits`, its generic callback helper and an unused legacy
settings-upsert SQL constant. The operator console already used the retained
`Tenant.RecordCreditGrant`. All 30 test-file callers now pass typed requests to
that method. Their ignored return values preserve fixture behavior. Legacy tests
that ask for unverified paid credit still fail that request as before. The paid
credit fuzz property now exercises the one retained request owner.

Moved the billing-start-award-versus-start/store-sqlite proof out of
`internal/acceptance/store_unit_billing.go` into the plain store test
`TestStoreBillingStartAwardVersusStart`. Removed its acceptance-wrapper call.
The test uses existing meteredUnitFixture and blobAccountingDiskStore seeding,
with no new SQL INSERT fixture. It verifies runner versus cloud start, bootstrap
exchange provenance, immutable reservation pricing and execution after reopening.
The package-internal test uses these existing store fixtures because importing
pkg/store/internal/storetest there would create an import cycle through store.

Verification:

- Whole-repository go build -p 4 ./... passes with GOWORK=off and VCS stamping off.
- Scoped go vet -p 4 passes for pkg/store, pkg/controller and tests/acceptance.
  Tests in those three packages compile after caller migration.
- The focused W1b suite passes in 87.182s: TestStoreData*, TestStoreBilling*,
  TestMeteredUnit*, TestPayment*, FuzzPayment* seeds, TestBlob*, FuzzBlob* seeds,
  TestLogPersistence*, TestResultRecords* and TestTenantSQLScope_*.
- All eight local store boundary proofs pass: CAS, presence, large presence,
  cache scope, reference edges, reachability, billing retention and the relocated
  billing-start proof. No native placement proof is claimed.
- The existing launcher-root policy property remains red in the same eight
  RootMetered=false cloud cases. The other 24 cases pass. No test was skipped.
- commentcheck -base main is clean across 2,134 files. API snapshot regenerated.
  git diff --check and the embedded changelog/migration checks pass.
- No whole-repository test run, native PostgreSQL run, Sparkwing binary or
  pipeline was run. The lead still owns those checks and independent review.

Contract changes needed and real integrations still open:

- W2/C2 must declare verified card setup, refund, warning and dispute events,
  including persisted setup/payment correlation. PaymentEvent and payment_events
  still carry checkout-only facts. SettlePayment cannot dispatch undeclared
  provider payloads. The existing private mutation owners remain ready.
- W1 owns the launcher issuance regression: Cloud awards must be metered even
  when the root marker is false. Its issuance and eligibility files are untouched.
- W1/C1 needs a persisted per-award exhaustion anchor and renewal cancellation
  outcome for job, plan, eval and owner awards. Current accountUnitBillingTx
  returns only error and renewal constructs LeaseBeat before accounting.
  Node-only legacy exhaustion cannot implement the final award policy.
- W1 needs committed result selectors in NodeRow/nodeReadColumns before exact
  CopyMaterializedRecords selection can replace legacy outputs. W2/W3/W9 carry
  the selected records to readers. StateStoreOld output, claim-input, scheduler,
  rehydrate and child-read integrations remain. Legacy claim-credit retirement
  remains open and is not counted as done by the convenience-API removal.
- W1's imported-terminal-attempt seeding remains open. The earlier relocated
  storage proofs still use the existing SeedPresence fixture.
- W2/W3 still own authenticated log transport/recorder and native host Data
  binding. Physical allowance eviction, source-before-run binding and the
  ReadBlob/ChargeDownload composition remain production joins.

Shared files: internal/acceptance/store_unit_billing.go (deleted),
tests/acceptance/billing_test.go (removed its moved proof), c1-data.md, CHANGELOG.md,
docs/migrations/_unreleased.md, their embedded mirrors and .apidiff/pkg_store.txt.
The six controller fixture migrations are compute_limits_test.go, credits_test.go,
node_claim_named_test.go, prom_internal_test.go, refund_authority_test.go and
storage_allowance_test.go. Other store test edits only migrate the removed grant
caller, except credit_reads_runtime_test.go and payment_authority_test.go.
W1 implementation files and lead-owned handoffs/amendments are unchanged.

Friction: the remaining schema and issuance joins cross the explicit W1 ownership
boundary. C2 has not declared the approved non-checkout provider payload. The
read-only snapshot test also avoids the regular SQLite connection's immediate
write lock so a real concurrent commit can occur inside the read transaction.
No Git writes occurred. Worker scratch is deleted at completion. The shared
/tmp/sw-gocache is retained.

STATUS: CONTINUE
