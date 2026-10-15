# W1b handoff

Continues 7cf7e987c. This session handles the lead's 09:40 harness move and
removes one unused storage helper. W1 implementation files, schema and lead-owned
handoffs/amendments are unchanged. No git writes or Sparkwing binaries were used.

Implemented and deleted:

- Moved store_billing, store_cache_scope, store_cas, store_gc_fence,
  store_presence, store_presence_large, store_reachability and
  store_reference_edges from internal/acceptance to pkg/store/*_test.go.
  Seven plain Test functions execute their checks. They use
  pkg/store/internal/storetest.NewSQLite targets and retain reopen assertions.
- Deleted store_data.go, both exported boundary catalogs, both exported Check
  dispatchers and their loops in tests/acceptance. No scenario, Harness or
  boundary registry was added. All prior assertions in these checks remain.
- Moved the publication, imported-attempt and cache fixtures used by both the
  store checks and W7 components into pkg/store/teststore/publication.go.
  Deleted internal/acceptance/blob_publication_fixtures.go. The three shared
  seed functions have one implementation. W7's component callers use it.
  The existing public teststore package is needed because Go's internal import
  rule prevents internal/acceptance from importing pkg/store/internal/storetest.
- Removed takeStorageSlotTx from team_storage.go. It had no caller, including
  tests. Current blob quota admission and storage standing remain the owners.
- Moved W7's physical-deletion function adapter into blob_collection.go because
  it previously depended on the moved reachability harness file.

The SQL fixture replacement part of the 09:40 decision remains open. These tests
now use the store's target/schema helpers, but some graph and financial fixtures
still insert rows directly. Moving or renaming that SQL does not prove real
issuance, publication or settlement. The shared publication seeder also retains
its prior SQL. No API-driven fixture conversion is claimed.

Evidence:

- Whole-repository go build -p 4 ./... passes.
- Scoped go vet passes for pkg/store, pkg/store/teststore,
  internal/acceptance and tests/acceptance.
- All seven moved package tests pass without skips: TestStoreCASIntegrity,
  TestStorePresenceReadSet, TestStoreLargePresenceBudget, TestStoreCacheScope,
  TestStoreReferenceEdges, TestStoreReachabilityRetention and
  TestStoreBillingRetention. The final rerun took 1.293 seconds.
- Six affected W7 components pass: reference-edge laundering with the filesystem
  store, filesystem manifests and filesystem manifest records, remote cache
  store-data and filesystem archives, and filesystem reachability/retention.
  Native launcher placeholders selected by the parent harness remain skipped.
- commentcheck -base main is clean across 2,118 files. git diff --check passes.

All Go commands used GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-gocache and GOTMPDIR/TMPDIR under /tmp/sw-astra-W1b. No provider,
PostgreSQL, native launcher, real HTTP or whole-repository test pass is claimed.
The initial component filter selected no tests. The corrected filter ran the six
components above, and their JSON verdicts were inspected.

Line budget:

- Net production Go delta this session is -1,289 across touched packages.
- internal/acceptance loses 1,381 net production lines.
- pkg/store changes by +92: the shared fixture adds 117 lines and the dead
  storage helper removes 25. Moving checks into test files removes their
  production classification without changing their assertions.
- pkg/store is +3,658 against main, 7,908 above the combined -4,250 target.
  This remains over budget. The fixture move is not a store runtime reduction.
  Tests, testdata and *_contract.go/*_contracts.go scaffolds are excluded from
  these counts. The session total includes internal/acceptance because its
  ordinary Go files are the subject of the lead's harness audit.

Remaining work and contract/integration requests, in priority order:

1. Output cutover: this branch's NodeRow/ReadNode still lacks the committed
   OutputRecord/ManifestRecord projection. W1's later report implements it on
   its branch. W2/W3/W9 must carry that selector and authenticated record reads
   before NodeOutputObject, OutputReadGrant and unbound Store.GetNodeOutput
   can be removed with working callers. Attempt-free materialization still
   needs agreement on destination record identity before the copier joins.
2. Log uploads: persisted reads were cut over in the preceding session.
   LogSegmentUploader still calls the old writer routes. W2 owns the real
   authenticated blob.LogTransport signing and verification endpoints, W3
   supplies persisted tagged subjects, and W9 owns the transport client.
   There is no production LogTransport adapter in this checkout. Its existence
   must not be inferred from the fake filesystem components.
3. Spend/grace: W1's accountUnitBillingTx returns only error and LeaseBeat is
   assembled before accounting. The durable node-only exhaustion anchor cannot
   represent host/plan/eval awards. W1 must provide the per-award exhaustion
   fence and committing renewal-policy result so W1b can join prepaid runway,
   minima, customer-runner wait exclusion and drain without another legacy path.
   Legacy Store claim and trigger writers still call the old credit helpers.
4. Lead 07:40 item 2 / A1-28: W2's PaymentEvent still declares only checkout
   outcomes and the verified proof has no setup/refund/warning/dispute facts.
   W1's payment_events schema still requires a checkout and restricts kind to
   those outcomes. W2 must provide verified facts/provider correlation and W1
   must supply the matching receipt/setup correlation schema. The earlier
   transaction-local payment mutation owners remain ready. SettlePayment is
   still checkout-only. No fake proof or metadata-selected payer was added.
5. Finish API-based fixture seeding for the moved checks. Raw financial/graph
   SQL remains visible in tests. No acceptance catalog or skip loop was added.
6. Source-before-run binding, ReadBlob/ChargeDownload composition and physical
   allowance eviction remain integration work. Previously completed A1-24 and
   A1-29 through A1-35 persistence and mutation authorization are unchanged.

No new foreign contract change is made. No new test port closes an archived
finding. This session relocates existing checks and preserves their assertions.

Shared files changed outside pkg/store:

- internal/acceptance/blob_collection.go
- internal/acceptance/blob_manifests.go
- internal/acceptance/blob_remote_edges.go
- internal/acceptance/blob_upload_isolation.go
- tests/acceptance/billing_test.go
- tests/acceptance/storage_test.go
- The ten deleted internal/acceptance files named above.

Friction: the payment proof/schema and result-selector gaps remain absent on
this branch. W1's later result-selector work was found in its predecessor report
and was not copied into its owned files. No production behavior was replaced by
a stub. The build and focused checks pass, but the area is incomplete and over
budget. The lead owns review and commits.

Scratch /tmp/sw-astra-W1b is removed after checks. The shared build cache is kept.

STATUS: CONTINUE
