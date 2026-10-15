# W1b handoff

Continues c212c89ff and the integration merge 94e9f2d83. W1b remains incomplete.
The lead owns commits, independent review, pipelines and native integration.

## This continuation

The first two lead handoffs remain done. The transaction-local record mutation
owner and the two catalog removals were not repeated. The third lead item advances
with the raw paid-credit cut. The previous service-counter route cut stays intact.

Removed POST /api/v1/credits/grants, createGrantReq, handleCreditsGrant and its
route-resource registration. Deleted the two retired paid-route test files.
Store.GrantCredits, Tenant.GrantCredits and Tenant.RecordCreditGrant refuse the
paid kind before opening a ledger transaction. Their free grant and payment-linked
reversal paths remain. Deleted the unreachable raw-paid validation, global
payment-reference lookup and warning check from that generic grant writer.

Invariant: no caller-selected paid grant can change any team's ledger or checkout,
before or after verified checkout settlement. FuzzPaymentRawPaidGrantsCannotFundTeam
exercises all three raw store entry points, generated references, actors, amounts
and both tenants. FuzzLegacyPaidCreditRouteIsGone exercises the registered router
with generated credentials, tenants, references and amounts. The store property
failed against the original implementation. The HTTP property was first run after
its handler removal, so no pre-removal failure is claimed for it.

Porting TestAWarnedPurchaseIsNotGranted exposed the final checkout writer accepting
an actionable early fraud warning. That test failed before the fix. The existing
warning checker now runs in the same settlement transaction before the grant or
provider-event receipt commits. An informational warning still permits settlement.
No pricing, purchase cap, quota, schema, setting or foreign Go contract changed.

Deleted 116 production Go lines and added 19, a 97-line net reduction. Deleted
782 test lines and added 266, including the two new files, a 516-line net
reduction. These figures exclude prose and generated documentation.

## Ports and acceptance

credit_grant_runtime_test.go replaces credits_reference_test.go and
credit_grant_reference_test.go. It retains free-grant reference replay and changed
terms, independent tenant references, empty references, free/paid kind separation,
request bounds, grants-minus-charges, reversal replay, state/history/deployment
totals, foreign/unfunded/excessive reversal refusal and refunds of spent credit.
Paid fixtures use committed checkout facts through the existing private settlement
boundary. The opaque C2 verification implementation is not replaced or claimed.

The prior raw-paid refund test is retired in favor of payment_refunds_test.go's
committed-checkout refund/restart/replay and partial-reversal proofs. The warned
purchase test moved from card_billing_test.go to that final-schema file. The card
identity matrix now covers card/card, card/checkout, checkout/card and
checkout/checkout. Raw grant funding is refused by the generated property instead.

The existing billing-ledger-survives-retention/store-data-sqlite cell additionally
refuses raw paid grants for the operator and adversarially named foreign team.
The default-team ledger fixture uses an operator free grant because it tests
tenant selection and balance projection, not provider settlement.
No new native acceptance cell or archived T1 source row is closed.

## Verification

All Go commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4 and
GOCACHE/GOTMPDIR/TMPDIR under /tmp/sw-astra-W1b/. Run go -C with this worktree's
absolute path. Builds, vet and tests use -p 4.

- Whole-repository go build ./... passed after the final embedded-doc sync.
- Scoped go vet passed for store, controller and both acceptance package trees.
  A final store-only vet also passed after the added assertions.
- The final store regression selection passed 195 top-level tests /
  605 cases, without skips or failures, in 239.720 seconds. The full generated
  record-mutation authority property remains included. This is a scoped pass.
- Nine controller top-level tests / 14 cases passed without skips, including the
  two retired-route generated properties' seed cases and retained maintenance
  and signup checks.
- Both generated properties passed. The final store run executed 128 inputs,
  including refusal before and after valid checkout settlement. The router run
  executed 137 inputs.
- TestStorage and TestBilling passed 2 top-level tests / 43 cases, including all
  12 existing store-data-sqlite proofs. The 562 native/platform skips remain.
- All apiref/apispec tests passed: 28 top-level tests / 46 cases.
  The generated OpenAPI output equals the curated file after operation/schema
  removal. The generated API reference lost the retired row.
- Eight focused embedded changelog tests passed. sync-docs.sh --check passed.
- commentcheck -base main passed across 1016 Go files. sleepcheck -base HEAD,
  gofmt and git diff --check passed.
- The final warned-purchase assertion passed independently and confirms that
  refused settlement leaves the checkout's payment/grant fields untouched.

Store selection:

```text
^(TestStoreData|TestLogPersistence|TestResult|TestBlob|TestPayment|TestAWarnedPurchaseIsNotGranted|TestCreditSettings|TestSetCreditSettings|TestUnsetRateTable|TestSetRateTable|TestAStoredRateTable|TestATableWithoutAFourCore|TestStorage|TestBilling|TestGrantCredits|TestCreditGrant|TestCreditReversal|TestCreditHistory|TestCreditsBalanceIs|TestCreditsDefaultTenant|TestTenantSQLScope|TestTenantWriters|TestTenantTwins|FuzzPaymentRawPaidGrantsCannotFundTeam)
```

Use go test -json -p 4 ./pkg/store -run '<selection>' -count=1 -timeout=480s.
Acceptance uses ./internal/acceptance/... ./tests/acceptance/... with
-run '^(TestStorage|TestBilling)$'. Each generated property uses -run '^$'
-fuzz '^<property>$' -fuzztime=128x -parallel=4 -timeout=120s.

No full-store, full-controller, race, PostgreSQL, provider, physical-backend or
native-platform pass is claimed. Older legacy fixtures outside the selection
still call raw paid grants or use removed tokens/claim/storage schemas. They
need final-path ports with their consumer cutovers, not a paid-grant fallback.

## Remaining assignments and real integration

- A1-24: transaction-owned data authorization and binding proofs remain intact.
  W2 DataController.DataAuthority and BindData still need real adapters.
- A1-27: issuance/start/renew/release/reap/finish billing joins remain open.
  authority_issue.go still refuses metered roots and persists unmetered awards
  through W1's separate writer. C2 SettleBilling remains a panic stub. The
  execution-start/customer-wait transport is still missing. The at_award helper
  alone cannot distinguish an unstarted refund from compute or customer-runner
  wait. W1/W2/W3 must expose those facts before that join preserves BillingRules.
  Accounting proofs continue to use contract calculator fakes. W1 files were
  not edited or supplied a second transition owner.
- A1-28: committed-checkout settlement and raw paid-grant refusal are implemented.
  W2 still owns provider verification and its HTTP adapter.
  VerifiedPaymentEvent.Event remains a stub. Card billing keeps its separate
  payment-linked settlement contract.
- A1-29 through A1-35: upload tickets, tagged receipts, provenance, record/member
  persistence, read-set authorization, memo/depcache, tombstones and tagged logs
  remain implemented behind contracts. W7 RecordNamespace and AuthorizeEdge
  remain stubs. Store proofs use contract fakes. Physical verification/deletion,
  manifest-reader configuration, GC, trusted replica identity, binary execution
  policy, signed transfers and exact proxy egress remain integration work.
  Allowance-driven physical eviction remains open.
- Legacy claim-credit, principal event/download counters, raw storage helpers,
  output objects, direct uploads and node-log transport still need consumer
  cutovers. Direct-upload/log paths retain reserveStorageTx, commitStorageTx and
  old reservation tables. StorageStanding still reads those rows. W7's old
  internal/storagequota and internal/cache RPC clients have no counter fallback.
  The source/artifact/binary data_download adapter still uses cache grants.
  Earlier materialization and imported-owner-log identity contracts remain open.
- A1-53: native placements, provider/backend and PostgreSQL evidence remain with
  the lead and integrating areas. The first two lead handoffs are done. The
  third remains in progress. No new foreign contract change is requested.

## Shared files, assumptions and friction

Production caller removals outside pkg/store are controller/credits.go,
controller/server.go and controller/tenant_routes.go. The final HTTP refusal
property replaces controller/credit_grant_request_test.go. The retired
controller/credits_grant_reference_test.go and credits_grant_team_test.go are
deleted. internal/acceptance/store_billing.go extends the existing store proof.

Owned store production changes are credits.go and payment_contract.go. Test
ports touch card_billing_test.go, card_payment_replay_boundary_test.go,
credit_reversals_test.go, credits_test.go, credits_per_team_test.go,
payment_checkout_caps_test.go and payment_refunds_test.go. The two new files
are credit_grant_runtime_test.go and payment_authority_test.go. The two deleted
store files are credits_reference_test.go and credit_grant_reference_test.go.
STORE-DATA.md and this handoff record the persistence and work boundaries. Shared docs are contracts/c1-data.md,
contracts/t1-port-inventory.md, api/openapi.yaml, CHANGELOG.md, docs/auth.md,
docs/migrations/_unreleased.md and the generated docs/api-reference.md.
bin/sync-docs.sh generated their embedded copies and pkg/docs/changelog.md.

No new production mechanism, environment variable, compatibility surface or
configuration default was added. The existing paid-credit authority boundary
moved to its declared checkout transaction. Reverting this cut means restoring
the removed handler and raw-paid branches, not adding a compatibility alias.

Friction: the old grant tests used the retired payment entry point and schema.
The replacement proofs use final checkout facts. The initial scoped run also
found four card-identity matrix cases still expecting raw paid grants. They
were ported to final settlement pairs. The broad run then exposed one default-team
ledger fixture still using a raw paid grant. That fixture now uses a free operator
grant. The earlier 240-second broad run exhausted its budget after 184 top-level
tests. Its lock-order test passes alone in 6.07 seconds. The rerun uses 480 seconds.
Those failed and timed-out runs are not counted as passes.
The missing C2 verifier, calculator and execution/wait transport still prevent
real billing integration. The lead retains review, committing and pipelines.
No Sparkwing binary, service, provider call, deployment, release, Git write or
harness-memory write ran. Scratch was deleted after checks. Every owned check process exited. Git HEAD remained 94e9f2d83. The shared config
has no core.worktree or repository-local identity override.
