# W1b billing-policy writer continuation

Base: 600de7bc34a0d341009d16cf8c84c61fbc67f2e1.
Net production Go delta in pkg/store: **-36 lines** (3 added, 39 deleted).
The controller caller changes one line with no net production growth.
Combined store delta against main: **+3,022**, **7,272 over** the -4,250 budget.
The remaining overage is not justified. This area is incomplete.

`Operator.SetCreditSettings` is the sole billing-policy writer. It retains the
existing Operator HTTP rule that an installed rate table refuses a scalar price
update. The separate `SetOperatorCreditSettings` entry point and the permissive
writer are removed. Explicit tables supply their own prices. The scalar only
selects the four-core price when deriving an unstored default ladder. The reader
override for older binaries is deleted. Card standing uses `creditBalanceTx`;
its duplicate ledger query is deleted.

Invariant: once a rate table is installed, a scalar update cannot change it or
partially apply sibling settings. The generated public-writer regression failed
against the previous code on its first input, which installed a table without
a four-core entry. All 32 generated cases pass through the retained writer.
The existing independent-connection writer race also passes. This is the same
operator pricing rule for every caller, with no new price, schema, setting,
environment variable, interface or service.

Ported twelve legacy controller credit-settings tests from their deleted-token
fixture and retired team write route. Two in-process HTTP tests cover defaults,
scalar updates, unnamed fields, case folding, both rate-table encodings, invalid
ranges, empty/null/duplicate-null bodies, unusable tables, installed-table refusal
and complete rollback. They exercise the registered final Operator PUT endpoint
and real SQLite settings. The existing C2 authority fixture supplies a synthetic
console and CSRF transport. The store's independent-writer test covers the former
HTTP race assertion at the serialization owner. Removed 320 legacy test lines
and added 115 lines for the port. The separate old GET authorization test remains
for W2, with its obsolete token fixture still failing before handler entry.

Verification uses GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-gocache and GOTMPDIR=/tmp/sw-astra-W1b, with -p 4:

- Whole-repository build passes. Scoped store/controller vet and test compilation pass.
- The focused store suite passes in 81.304s: TestStoreData*, TestStoreBilling*,
  TestMeteredUnit*, TestPayment*, FuzzPayment* seeds, TestBlob*, FuzzBlob* seeds,
  TestLogPersistence*, TestResultRecords*, TestTenantSQLScope_*, credit-settings
  and rate-table tests. No whole-repository test run was made.
- The local store boundary proofs for CAS, presence, large presence, cache scope,
  reference edges, reachability, ledger retention and billing start pass. No new
  acceptance cell or native-placement evidence is claimed.
- Final Operator HTTP write tests pass with the existing authorization fake.
  Native console/session authentication is not proved by this port.
- Commentcheck against main, git diff --check, changelog check and embedded
  migration/changelog tests pass. API snapshots are regenerated. That also
  corrects existing NodeLogCompleteness signature drift in pkg_controller.txt.
- A broad docs-name selection hit TestWebClient_MigrationFetch and failed because
  the sandbox refuses listeners. The embedded-only docs selection passes.
- The prior W1 launcher-root metering failure is not rerun or claimed fixed.
  No Sparkwing binary, pipeline, native PostgreSQL or Git write was run.

Local contract cleanup: C1 names the single SetCreditSettings writer and removes
the separate setter contract. STORE-DATA.md, migration notes and the port inventory
are updated. Prices and the existing Operator HTTP rule are unchanged.

Contract changes and real integration points still open:

- W2/C2 must declare verified card setup, refund, warning and dispute events,
  including setup/payment correlation. PaymentEvent and payment_events still
  carry checkout-only facts. W1b's private mutation owners remain available.
- W1 owns Cloud award metering even when the root marker is false. Its issuance
  and eligibility files remain untouched.
- W1/C1 needs per-award exhaustion persistence and a renewal cancellation outcome
  for node, plan, eval and owner awards. accountUnitBillingTx returns only error;
  renewal builds LeaseBeat before accounting. Legacy node exhaustion cannot
  implement this contract for every unit.
- W1 must persist committed result selectors in NodeRow/nodeReadColumns before
  exact materialized-result copies and output readers can retire. W2/W3/W9 must
  carry those selectors. No selector is inferred from another record or digest.
- The remaining legacy credit owner in credits.go is still called by legacy
  claims in store.go, claim_run.go, node_attempt.go and launch_sync.go. Legacy
  output bytes still serve scheduler, rehydrate, child and runner-result reads.
  Their replacement would require changing W1-owned implementations, beyond the
  permitted caller migrations. No silent free billing or empty-output fallback
  was introduced to remove those calls.
- W1 imported-terminal-attempt seeding, W2/W3 authenticated log transport and host
  Data binding, physical allowance eviction, pre-run source binding and combined
  ReadBlob/ChargeDownload remain open from the preceding handoff.
- W2 still owns GET credit-settings authorization and the retained legacy test's
  token-fixture migration. Its route registration was not changed.

Shared files: pkg/controller/credits.go (single writer caller),
pkg/controller/credits_test.go (twelve retired tests),
pkg/controller/credit_settings_http_test.go (the port), C1 and T1 port inventory,
CHANGELOG.md, docs/migrations/_unreleased.md, both embedded mirrors and both
store/controller API snapshots. W1 implementation files and lead-owned handoffs
and amendments are unchanged.

Friction: checkout-only C2 declarations and missing C1 result/exhaustion contracts
remain concrete gaps, rather than implementation stubs that a fake can replace.
The legacy controller fixture additionally calls the deleted tokens table before
reaching the changed writer. The final write port uses the existing policy
fixture instead. Worker scratch is deleted at completion; /tmp/sw-gocache stays.

STATUS: CONTINUE
