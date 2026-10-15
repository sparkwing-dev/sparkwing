# W1b handoff

Continues committed session 4, 665d47e18. W1b remains incomplete. The lead owns
commits, independent review, pipelines and native integration.

## This continuation

The first two lead handoffs remain done. The record-mutation owner and two
catalog removals were not repeated. The third lead item advances by retiring
raw storage reads and an unused unleased reservation sweep. The raw paid-credit
and service-counter route cuts remain intact.

`Tenant.StorageStanding` replaces `Store.StorageStandingFor` and `Store.TeamStorage`.
The controller now reads one tenant snapshot for policy, retained event bytes,
unique blob bytes, tagged log bytes and live blob-upload holds. It shares the
inventory reader with storage billing. Committed, aborted, deduplicated and expired
holds are reported without modifying quota or financial records. Corrupt or
overflowing inventory fails with no partial report. PostgreSQL uses the existing
repeatable-read snapshot helper. SQLite uses its existing transaction snapshot.

Removed `TeamStorageUsage`, `Store.ReconcileFreeEventBytes`, its duplicate
`free_slots.event_bytes` column and the event-cache writer. Standing and free-event
admission read retained `runs.event_bytes`. Admission keeps its existing free-tier
lock. Run deletion therefore needs no repair pass. Historical financial usage
still survives deletion. Funding reads use the store clock. Tier, allowance and
free-slot rules are unchanged.

Removed `Store.ReleaseExpiredStorage`, `expiredReservationRows` and their
`storageRow` type. Final upload admission and leased blob maintenance already own
expiry. The legacy direct-upload test now releases its exact reservation, rather
than invoking a deployment-wide sweep. Other raw upload/log writers and their
internal reservations remain for their consumer cutovers.

Invariant: a tenant's standing contains only its retained inventory and live
upload holds, and reporting never changes accounting. The generated property
uses own and foreign sizes, shared digests, adversarial replica strings, stale
raw counters and reopening. It verifies tenant isolation, unchanged usage and no
slot or ledger writes. An initial behavioral test failed before implementation:
standing reported 900 cached event bytes when the retained run held 17.

Deleted 156 production Go lines and added 117, a 39-line net reduction. Deleted
98 test lines and added 273. The final schema loses one net line. These figures
exclude documentation and generated API snapshots.

## Ports and acceptance

`storage_standing_runtime_test.go` ports retained-event release after deletion
and the funded/free/held standing matrix to the final schema. It also checks
pending, committed, duplicate, aborted and expired upload reports, corrupt totals,
tenant isolation and reopening. The controller proof consumes that final snapshot.
The old free-tier deletion test no longer calls a repair method. Remaining legacy
storage/log fixtures read their own SQL assertions after the raw usage reader cut.
Those fixtures are caller migrations, not a claim that their legacy scenarios pass.

The two legacy reservation-sweep tests are removed. The final
`TestBlobUploadExpiredReservationFreesCapacity` now covers empty and nonempty
uploads. Empty uploads reserve object capacity, expired tickets cannot commit,
and aborting an expired ticket leaves its successor's hold intact. The old
assertion that a late raw commit counts bytes is reversed by final ticket expiry.
Committed-ticket replay remains covered by the existing final tests.

The existing upload crash/replay SQLite proof checks a live hold before commit
and unique retained bytes after reopening. No archived T1 source row or native
acceptance cell is newly closed. Native transports remain unclaimed.

## Verification

All Go commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4 and
GOCACHE/GOTMPDIR/TMPDIR under /tmp/sw-astra-W1b/. Commands use go -C with this
worktree's absolute path and -p 4. The scratch directory was removed after all owned check processes exited.

- Whole-repository go build ./... passed after the final embedded-doc sync.
- Scoped go vet passed for store, controller and both acceptance package trees.
- The final store selection passed 200 top-level tests / 618 cases, without
  skips or failures, in 51.851 seconds. This includes final record-mutation,
  metered-accounting, payment, upload, inventory, result and tenant-scope proofs.
- Four controller top-level tests / seven cases passed without skips, including
  the final standing adapter, maintenance inventory and retired-counter checks.
- FuzzStorageStandingTenantInventory passed 149 generated executions with four
  workers. Its final schema and production inventory implementation were in place.
- TestStorage and TestBilling passed two top-level tests / 43 cases, including
  all 12 existing store-data-sqlite proofs. The 563 native/platform skips remain.
- The focused embedded changelog tests passed. sync-docs.sh --check passed.
- commentcheck -base main passed across 1019 Go files. sleepcheck -base HEAD,
  gofmt and git diff --check passed. The store API snapshot matches generation.

The store command uses go test -json -p 4 ./pkg/store -count=1 -timeout=480s
with this selection:

```text
^(TestStoreData|TestLogPersistence|TestResult|TestBlob|TestPayment|TestAWarnedPurchaseIsNotGranted|TestCreditSettings|TestSetCreditSettings|TestUnsetRateTable|TestSetRateTable|TestAStoredRateTable|TestATableWithoutAFourCore|TestStorage|TestBilling|TestGrantCredits|TestCreditGrant|TestCreditReversal|TestCreditHistory|TestCreditsBalanceIs|TestCreditsDefaultTenant|TestTenantSQLScope|TestTenantWriters|TestTenantTwins|FuzzPaymentRawPaidGrantsCannotFundTeam|FuzzStorageStandingTenantInventory)
```

Acceptance uses ./internal/acceptance/... ./tests/acceptance/... with
-run '^(TestStorage|TestBilling)$'. The generated property uses -run '^$'
-fuzz '^FuzzStorageStandingTenantInventory$' -fuzztime=128x -parallel=4
-timeout=120s. The fuzz runner reports executions above its requested target.
No full-store, full-controller, race, PostgreSQL, provider, physical-backend or
native-platform pass is claimed.

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
  old reservation tables. The controller standing report now reads final inventory. Raw upload/log writers still use those rows. W7's old
  internal/storagequota and internal/cache RPC clients have no counter fallback.
  The source/artifact/binary data_download adapter still uses cache grants.
  Earlier materialization and imported-owner-log identity contracts remain open.
- A1-53: native placements, provider/backend and PostgreSQL evidence remain with
  the lead and integrating areas. The first two lead handoffs are done. The
  third remains in progress. No new foreign contract change is requested.

## Shared files, assumptions and friction

Shared production caller: `pkg/controller/storage_standing.go`.
Shared test caller migrations: `pkg/controller/log_segments_internal_test.go` and
`pkg/controller/direct_upload_test.go`. `internal/acceptance/store_upload.go`
extends the existing persistence proof. `pkg/store/schema.sql` removes only the
duplicate free-slot event cache. `tenant_sql_scope_guard_test.go` removes only
the two entries naming deleted APIs. W1's implementation files remain untouched.

Owned production files: `free_tier.go`, `storage.go`, `storage_quota.go`,
`retained_storage_billing.go`, `team_storage.go`, and new `storage_standing.go`.
Owned test files: `free_tier_test.go`, `team_storage_test.go`,
`upload_runtime_test.go`, and new `storage_standing_runtime_test.go`.
The new controller proof is `storage_standing_internal_test.go`.

Shared docs: `contracts/c1-data.md`, `contracts/t1-port-inventory.md`,
`CHANGELOG.md`, `docs/migrations/_unreleased.md`, `.apidiff/pkg_store.txt`, and
sync-docs-generated copies under `pkg/docs`. `STORE-DATA.md` describes the new
persistence read path. The C1-local contract adds the tenant standing method and
three inventory fields, removes four raw Store APIs and one unused result type,
and drops the duplicated free-slot column. No foreign contract change is needed.

Mechanisms added: none beyond the tenant read replacing the two raw reads.
The existing snapshot and retained-inventory owners are reused. No new policy,
quota, price, setting, environment variable, compatibility path or service is added.
Undoing this cut means restoring the removed reads/cache and their callers.

Friction: the one-letter fake tenant cannot pass the public slug validator, so
its standing-policy test seeds its slot as fixture data. An intermediate targeted
check compiled while its obsolete test functions were being removed and failed
with undefined test names. Final checks run after those edits and replace that
result. Legacy storage/claim/token fixtures outside the final selection remain
for their consumer cutovers. Missing C2 calculation/verification, W1 timing joins
and W7 adapters still prevent real execution/billing/data integration.
No Sparkwing binary, service, deployment, release or Git write ran.

Git HEAD remained 665d47e18. The shared repository config has no core.worktree
or repository-local user identity override. Scratch cleanup is complete.
