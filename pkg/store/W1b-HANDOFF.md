# W1b handoff

This continuation starts at `01a6b033c`. Earlier accounting, payment, upload,
record, GC and tagged-log work remains. W1b is incomplete. W1's schema,
admission, claims, state, reaper, generic quota and SQL-guard files are untouched.
No Git writes were made.

## Changes

- `LookupRecord` now honors `VerifiedDefaultOnly` before reading required bytes
  or manifest members. Persisted evidence must contain the verified default ref,
  matching branch, App delivery key and SHA. A bound reader's sealed default
  branch must agree when present. Excluded records return a miss. Ordinary
  authorized reads keep their required-byte errors. Remote-cache authority stays
  restricted when the request flag is false.
- Removed the cache service's egress-total store methods and endpoint, including
  the client bridge, cache startup restore/flush loop and fake route. The cache
  no longer calls the removed route. This cuts its high-water-mark counter family.
  The controller's separate legacy egress meter remains for its owning migration.
- Activated `storage-hasmany-read-set/store-data-sqlite`. It checks own versus
  hidden foreign/unrelated existence, duplicate queries, expired budgets,
  uncommitted reservations, failed verification, independent exact receipts,
  replay after reopening and unique-blob charging across replicas. Metadata
  queries consume no download or egress bytes.
- Updated source docs, changelog, C1 notes, API reference, OpenAPI and embedded
  mirrors. The Go API snapshot loses only the removed type and two methods.

The new acceptance proof uses real SQLite persistence with preseeded publication
capabilities and imported attempts. Physical verification is trusted fixture
input. Holder transports, signed byte transfer and native placements remain open.
The default-branch regressions use the existing contract fakes for W7's namespace,
edge rules and physical manifest reader. No competing implementation was added.

## Deletion and ports

Deleted **171 gross legacy runtime Go lines**, **18 test-fixture implementation
lines** and **75 obsolete test lines**. The four replaced lines in `blob_lookup.go`
are excluded from these retirement totals. Removed the orphan OpenAPI schema and
endpoint, plus nine store API snapshot lines.

Added the inventory's presence-budget and metadata-does-not-charge-download cases.
No additional complete archived T1 inventory row is claimed closed. The removed
high-water-mark and cache restart tests describe the retired service bridge.
No table, runtime interface, setting, environment variable or customer limit was
added. No contract declaration changed.

## Unit evidence

- Store selection: **115 top-level tests / 215 including subtests**, no skips.
  Pattern: `^(TestStoreData|TestLogPersistence|TestResult|TestBlob|TestPayment|TestReversePaymentTakesBack|TestCreditSettings|TestSetCreditSettings|TestUnsetRateTable|TestSetRateTable|TestAStoredRateTable|TestATableWithoutAFourCore)`.
- Acceptance packages: **20 top-level tests / 198 passes**, with **2,995 existing
  native/platform skips**. The new presence cell passed again after fixture cleanup.
- Controller/orchestrator route selection: **10 top-level tests**, no skips.
  Pattern: `^(TestLegacyEgressTotalsEndpointIsGone|TestRouteGuard_|TestTenantRoutes_|TestEveryControllerRouteIsClassified|TestReadRouteListCarriesNoDuplicates|TestHostedRetryPolicy_)`.
- Removing the HasMany read-set condition through a Go overlay makes the new
  SQLite cell fail because a foreign-existing digest becomes visible. The
  production source is unchanged by the negative control.
- The endpoint test failed with 401 before the cut and passes with 404 after it.
  Default-only lookup tests failed on manual/feature records before the filter.

## Manual verification

All Go commands used `GOWORK=off`, `GOFLAGS=-buildvcs=false`, `GOMAXPROCS=4`,
`-p 4` and `GOCACHE`, `GOTMPDIR`, `TMPDIR` under `/tmp/sw-astra-W1b/`.
Use this worktree with `go -C <worktree>`.

- Whole-repository `go build -p 4 ./...` passed on the final production tree.
- `go vet -p 4` passed for `pkg/store/...`, `pkg/controller/...`,
  `internal/acceptance/...`, `tests/acceptance/...`, `internal/storagequota/...`,
  `internal/cache/...` and `internal/orchestrator/...`.
- `go test -p 4 ./internal/storagequota/... ./internal/cache/... -run '^$'`
  compile-checks all retired-bridge callers and their remaining tests.
- `commentcheck -base HEAD <worktree>`, `sleepcheck -base HEAD <worktree>`,
  OpenAPI generation/drift comparison, embedded-doc sync and `git diff --check`
  passed. SQL scope guard still reports **28 inherited findings** in untouched
  files. The broader Go API snapshot retains inherited rewrite drift.
- No local Git identity or `core.worktree` override was found.

## Not tested

No native placement, PostgreSQL, daemon, candidate, pipeline, external payment
provider or release was run. Native acceptance and full legacy store tests are
not represented as passing. Race tests were not repeated for a read filter and
retired service bridge. Independent landing review remains with the lead.

## Contract and integration work remaining

1. W1 must join `recordBillingAwardTx`, start/finish intervals and final/periodic
   settlement to issuance, starts, waits, renewals, release, finish and reaping.
   These helpers still have no external production callers on this branch.
   Memo materialization must join result state atomically.
2. W2's billing calculator remains a stub here. W2 must wire verified payment
   events. Test/live identity for retained cards, attempts, warnings, freezes and
   refunds remains a cross-area concern.
3. W7 must implement `RecordNamespace` and `AuthorizeEdge`, verified manifest
   reading, physical upload verification/deletion and trusted replica identity.
   W2's data controller remains a stub here. W7/W9 must connect final record/result
   publication and retire the old `StateStoreOld` manifest callers.
4. W1/W9 need the final custom-event append/sequence boundary before
   `AppendEventCharged` and `chargeStorageTx` can be deleted. The charged route
   returns a sequence. The raw append returns only an error and does not retain
   all event limits. No competing event contract was added.
5. W1 owns removal of the now-unused `egress_day` baseline table, its schema
   classification and operator deletion-list entry. Those shared files were left
   untouched. No store or transport caller for that table remains.
6. Remaining W1b legacy families include claim-time credits, paid grants, raw
   storage counters, direct uploads and old node logs. Their remaining consumers
   cross W1/W2/W7/W9 ownership. Elected maintenance, allowance/nonpayment eviction,
   record/pin policy, GC, receipt pruning, log lifecycle, egress and ring fallback
   still require the declared owners' integrations.

No new human question or architectural decision is introduced. Missing joins,
foreign stubs and legacy families prevent a COMPLETE claim.

## Shared files

- `internal/acceptance/store_data.go`, new `store_presence.go`.
- `pkg/controller/server.go`, `storage_counters.go`, `storage_pass.go`,
  `storage_counters_test.go`, `route_scope_guard_test.go`, new
  `store_data_retirement_internal_test.go`.
- `internal/cache/cache.go`, `egress_budget_test.go`, deleted `egress_day.go`.
  These are only the removed route's caller and obsolete restart assertions.
- `internal/storagequota/client.go`, `storagequotatest/controller.go`.
- `internal/orchestrator/wingd_api_routes_test.go`.
- `.apidiff/pkg_store.txt`, `api/openapi.yaml`, `CHANGELOG.md`,
  `design/rewrite/contracts/c1-data.md`, `docs/api-reference.md`, `auth.md`,
  `limits.md`, `observability.md`, `security.md` and their generated `pkg/docs`
  mirrors/changelog.

Owned store files are `blob_lookup.go`, new `blob_default_lookup_test.go`,
`team_storage.go`, `team_storage_test.go` and this handoff.

Friction: the OpenAPI generator requires removed schemas and routes to be cut
before it can verify drift. A missing-byte fixture initially violated a real
foreign key and was replaced with a failing verified-manifest reader. Compile
checks caught an obsolete fake accessor and unused imports, which were removed.
The first overlay mutated the condition to a literal and left unused variables.
The corrected overlay builds and fails on the intended foreign-existence assertion.
Scratch is deleted after all checks finish.

STATUS: CONTINUE
