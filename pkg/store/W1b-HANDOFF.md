# W1b handoff

Continues `dd33fc542`. W1b remains incomplete. This continuation is uncommitted.
The lead owns review, commits, pipelines and native integration. W1 implementation
files are unchanged. No Git write or candidate Sparkwing runtime was used.

## Current continuation

Fixed HasMany completing its final read after the deadline and returning success.
`readableBlobsTx` checks the deadline after the full scan and discards presence
on `blob.ErrBudget`. A generated test first failed on the old implementation.
It advances the store clock through 160 generated deadline/duplicate cases.
The invariant is that a successful presence response finishes within its budget.
This adds three production lines and no new mechanism or configuration.

The new `storage-hasmany-large-budget/store-data-sqlite` acceptance cell passes.
It queries 1,025 distinct digests plus duplicates, in forward and reverse order
after reopening. All 257 readable digests appear once in request order. Unrelated,
foreign and absent digests remain hidden. Upload counters stay at 257 bytes and
objects, with no download or egress charge. Physical verification and native
placements remain separate integrations.

Added a trigger/plan owner-receipt property and job-read regression. The invariant
is that owner upload authority cannot transfer through a forged identity or an
unrelated job. Each owner uploads before any job attempt, reopens, retains its
tagged receipt without a synthetic job, rejects 96 generated identity forgeries,
and refuses presence/download/reference access to a newly awarded job in its run.

Removed `Store.CreditRateMicroPerSecond` and `Store.CreditGraceSeconds`,
**12 production Go lines**, including their comments and blank lines. Their
remaining test callers read the existing `CreditSettings` snapshot. Deleted obsolete
scalar-reader assertions. No whole test or archived T1 inventory row was removed.
No additional T1 row is claimed closed. No foreign contract shape, schema, limit
or environment variable changed. No human question or new mechanism was added.

Verification uses the environment listed below. Whole-repository build and vet
over store, controller, both acceptance trees and their subpackages pass.
The store selection passed **143 top-level tests / 382 cases** without skips.
A final focused run passed the new properties, changed settings tests and rate
tests after the final test edit. Both acceptance packages passed **20 top-level
tests / 203 cases**, including **10 SQLite cells**, with **2,995 native/platform
skips**. The new cell ran its actual assertions.

Documentation mirror tests, `sync-docs.sh --check`, session-only commentcheck,
session-only sleepcheck and diff whitespace checks pass. Required
`commentcheck -base main` still reports the inherited detached HereBearer comment
in `internal/controllerwire/contracts.go`. The SQL-scope guard still reports the
same ten inherited findings below. Both tenant writer guards pass in that run.

Three selected legacy controller tests fail in fixture setup at
`pkg/controller/credits_test.go:42` because they insert into removed `tokens`.
The changed assertions are not reached. They are
`TestCreditSettings_ReadsTheDefaultsAndSetsEachValue`,
`TestCreditSettings_RefusesAWriteWhoseOtherFieldIsBad` and the unchanged
`TestCreditSettings_LeavesUnnamedValuesAlone`. Store tests cover the settings
behavior through the final schema. Full legacy store tests, PostgreSQL, race,
providers, native transports, candidates and pipelines were not run.

Shared Go changes are `internal/acceptance/store_data.go`, new
`internal/acceptance/store_presence_large.go` and two caller replacements in
`pkg/controller/credits_test.go`. Other shared changes are `c1-data.md`, the
migration guide, `CHANGELOG.md`, generated documentation mirrors and the two
deleted API snapshot declarations. Owned files are the two new property-test
files, `blob_read_authority.go`, `credits.go`, three billing test files and
`STORE-DATA.md`.

Remaining work is the integration and legacy retirement listed below. The local
W2 billing calculator and payment verifier remain stubs. W1 issuance still writes
unmetered billing rows directly. W1/W2 must connect their real paths to the
existing transactional billing owner before the old claim-credit code can go.
No W1 file was edited. Friction was cold compilation, the foreign fixture/guard
failures and a typed-principal compile correction in the new test. Scratch is
removed after verification. Git identity and worktree configuration are unchanged.

## Previous continuation (committed in dd33fc542)

## Unit

Implemented C2's existing `DataAccessChecker` on the owning store transaction.
`Tenant.CheckDataAccess` uses that implementation. Both it and `ReadableBlobs`
share one transaction-aware presence reader. Receipt, record, revocation and
holder changes in the owning transaction govern authorization. The checker opens
no second transaction and leaves commit or rollback to its caller.

The invariant is that caller-supplied authority cannot widen persisted rights,
and data checks must see changes in their owning transaction. Three new tests
first failed because the transaction did not implement the contract. They now
prove receipt deletion/reinsertion, newly declared read edges, removed records,
rollback ownership, live rights and refusal after leaf/ancestor revocation,
generation replacement, lease expiry or a foreign team. A property generates
96 forged principals and checks all six data operations for each input.

The existing HasMany SQLite proof now also checks download, HasMany and lookup
route authorization before and after receipt commit, failed verification and
reopening. Missing, unrelated and foreign bytes remain indistinguishable. The
proof uses the final persisted publication capability as a contract fixture.

Removed the unused `Store.CreditRateTableSet` getter, **8 production Go lines**.
Its two test callers now read `CreditSettings.RateTableSet` in the settings
snapshot. The tests preserve unset-table and table-without-base-class behavior.
Removed **2 narrative comment lines** from the owned accounting file. Moved
checker code is a refactor and is excluded from those deletion counts. No test
was deleted and no new archived T1 inventory row is claimed closed.

Fixed an owned disk-fixture mismatch exposed by the final store selection.
`blobAccountingDB` now opens its connection before returning, matching production
Open's initialized database. A new regression test first observed DELETE journal
mode instead of WAL, then passed with eager connection initialization. The
original concurrent-settings test reproduced SQLITE_BUSY once in 300 repetitions
before that correction. No production locking or retry behavior changed.

The final upload reservation, receipt, quota, record, GC, accounting, payment,
output-limit and tagged-log implementations from earlier sessions remain.
`STORE-DATA.md` describes their transaction seams. The earlier abandoned-stream
maintenance integration remains in place.

## Manual

Commands use `GOWORK=off GOFLAGS=-buildvcs=false GOMAXPROCS=4`, `-p 4`,
and GOCACHE/GOTMPDIR/TMPDIR beneath `/tmp/sw-astra-W1b/`. Run Go commands with
`go -C /home/bombuntu/.xwing/envs/1088/sparkwing`.

- Whole-repository `go build -p 4 ./...` passed.
- `go vet -p 4 ./pkg/store/... ./internal/acceptance/... ./tests/acceptance/...`
  passed.
- The focused store selection passed **141 top-level tests / 378 cases**,
  without skips. The expanded three-test transaction selection also passed.
- The concurrent credit-settings test passed 300 repetitions after the fixture
  correction. Reproduce with `go test -p 4 ./pkg/store -count=300
  -run '^TestStoreDataCreditSettingsIndependentWritersPreserveRateAuthority$'`.
- `go test -p 4 ./internal/acceptance/... ./tests/acceptance/... -count=1`
  passed **20 top-level tests / 202 cases**, with **2,995 native/platform skips**.
  The changed storage proof was rerun after extension and passed.
- Both tenant writer guards, documentation mirror/changelog drift tests,
  session-only commentcheck/sleepcheck and diff whitespace checks passed.

Store selection:

```text
^(TestStoreData|TestLogPersistence|TestResult|TestBlob|TestPayment|TestReversePaymentTakesBack|TestCreditSettings|TestSetCreditSettings|TestUnsetRateTable|TestSetRateTable|TestAStoredRateTable|TestATableWithoutAFourCore|TestStorage|TestBilling)
```

Nine SQLite persistence cells pass. The HasMany cell is extended. No native
cell was activated:

- `billing-ledger-survives-retention/store-data-sqlite`
- `storage-replica-safe-accounting/store-data-sqlite`
- `storage-quota-settlement-crash-replay/store-data-sqlite`
- `storage-log-segments-and-fallback/store-data-sqlite`
- `storage-hasmany-read-set/store-data-sqlite`
- `storage-reachability-and-retention-tails/store-data-sqlite`
- `storage-memo-and-depcache-scope/store-data-sqlite`
- `storage-reference-edge-laundering/store-data-sqlite`
- `storage-publish-one-way/store-data-sqlite`

Branch-wide checks retain foreign failures:

- Required `go run ./internal/commentcheck -base main .` reports the detached
  HereBearer comment in `internal/controllerwire/contracts.go`. The owned
  accounting-comment finding is removed. `-base HEAD` passes this continuation.
- `go run ./internal/sleepcheck -base main .` reports two wall-clock deadline
  tests in `internal/limits/deadline_test.go`. `-base HEAD` passes.
- The SQL-scope guard reports the same ten inherited W1 findings in authority,
  awards, retention and stale exemptions. No new W1b finding appears.

## Not tested

The full legacy store suite was not rerun. Earlier full-suite failures concern
removed-schema/token/claim fixtures, including a token-test panic. No PostgreSQL,
race, provider, daemon, native placement, browser, candidate, pipeline or release
check was run. The brief assigns pipelines and independent review to the lead.
The API snapshot removes only this continuation's deleted symbol. The lead must
regenerate aggregate snapshots after the remaining area cuts.

## Contracts and real integrations remaining

No foreign interface, schema, setting, limit or environment variable changed.
The C1 contract documents the existing transaction checker implementation and
removes the unused presence getter. No new foreign contract shape is requested.
No human decision is requested.

1. W1's binding adapter can use the transaction's `DataAccessChecker` after
   integration. Test it through the real adapter, including PostgreSQL. W1's
   separate branch opens a read-only snapshot in `Store.CheckBinding` and then
   invokes locking authority resolution. PostgreSQL disallows those row locks
   in a read-only transaction. W1 must resolve that transaction-mode mismatch.
   The final blob mutation paths retain their locking authority rechecks.
2. W1 must join immutable billing awards, interval starts/finishes, periodic
   charges and settlement to issuance, starts, waits, renewal, release, finish
   and reaping. Memo edges and result state must materialize atomically.
   Published run, plan and attempt import precedes blob/log publication.
3. W1/W2 must define imported trigger/plan/eval identities for published owner
   logs. Job publication uses exact imported terminal attempts. Owner publication
   remains refused without that contract.
4. W2 must supply the real billing calculator, verified payment handoff and data
   authorization/signing. Card attempts, warnings, refunds and freezes still
   need provider test/live integration coverage.
5. W7 must supply namespace/edge policy, verified manifest reads, physical
   verification/deletion and trusted replica identity. W1/W7 must wire final
   retention, upload quota release and tombstone GC to the elected worker.
   The legacy storage pass still reconciles raw counters and prunes old outputs
   and node logs. Its source and upload age-pruning calls are gone.
6. W2/W7/W9 must wire final log signing, transfer, publication, lifecycle expiry,
   proxy egress and ring fallback. Transport must register an empty stream
   through `RecordSegments` before its first transfer so abandoned empty tails
   have persisted identities. Maintenance seals existing streams only. Legacy
   controller log readers/writers and read-time physical sealing remain until
   that transport replaces them. The old periodic sweep is gone.
7. W1/W9 must define final custom-event append/sequence behavior before
   `AppendEventCharged` and `chargeStorageTx` can go. W1 owns removal of egress_day
   and remaining table classification/scope guard cleanup.
8. Legacy claim credits, paid grants, raw storage counters, direct upload and
   output persistence still have W1/W2/W3/W7/W9 consumers. W7/W9 must retire
   StateStoreOld manifest consumers. No forwarding shim was added.

## Shared files and friction

Shared Go caller changes outside pkg/store: only
`internal/acceptance/store_presence.go`. The removed getter has no production
caller, so its removal required no cross-package migration.

Other shared files: `design/rewrite/contracts/c1-data.md`,
`docs/migrations/_unreleased.md`, its generated embedded mirror, `CHANGELOG.md`,
`pkg/docs/changelog.md` and `.apidiff/pkg_store.txt`. The owned persistence
explanation and this handoff are updated. `bin/sync-docs.sh` generated mirrors.

Friction: cold compilation, strict final-schema fixtures, missing foreign runtime
owners and the inherited branch-wide guard failures above. The first read-edge
fixture used an invalid terminal row, and the first receipt-deletion query named
an absent table. Both fixtures were corrected before verification. Bootstrap ran the installed
`sparkwing info --for-agent` before the swarm brief was read. No other Sparkwing
binary command ran. No production
workaround or SQL guard exemption was added. Scratch was deleted after checks.
No owned service or background process remains. Git identity and worktree
configuration were not changed.

STATUS: CONTINUE
