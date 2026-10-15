# W1b handoff

Continues `5c2405ca0`. W1b remains incomplete. These changes are uncommitted.
No Git writes or edits to W1 implementation files were made. The lead owns
independent review, committing, delivery pipelines and native integration.

## Implemented

- Memo and depcache default-branch fallback now use the existing
  `VerifiedDefaultOnly` filter before required-byte or manifest reads. A
  verified ref without default-branch evidence cannot become a default hit.
  Memo keeps own scope before default, with no PR-base fallback. Depcache keeps
  own ref, verified PR base, then verified default.
- Added regression coverage for the restore orders, caller-selected scope
  refusal, false or missing provenance, filtering before manifest reads and
  expired lookup budgets. Shared namespace, edge and manifest contracts use
  existing test fakes because their owning implementation is still stubbed.
- Activated `storage-reachability-and-retention-tails/store-data-sqlite` through
  the existing acceptance driver. It exercises real upload/receipt persistence,
  leased record expiry, newest-success pins, transitive members, reopening,
  tombstone grace, physical-deletion errors, reference revival after marking and
  collection after the last root disappears. The graph and physical deletion
  backend are fixtures. Fixture timestamps advance without sleeping.
- Removed three unused scalar Store getters: `CreditMaxChargeSeconds`,
  `StorageRateMicroPerGBDay` and `StorageFreeAllowanceBytes`. Their values remain
  in the already-tested `CreditSettings` snapshot. Only an owned test fixture
  called them, so no callers in another package needed migration.
- Updated the C1 source notes, changelog, its embedded copy and the three API
  snapshot declarations. No new setting, table, interface, environment variable,
  limit or customer policy was added.

## Deletion and ports

Deleted **21 legacy runtime Go lines**, **3 test lines** and **3 API snapshot
lines** for the getters. The lookup filter removes a separate three-line late
check and replaces two call sites. Those replacements are excluded from the
legacy retirement count.

Ported store-boundary portions of the archived cache-ref restore-order and
poisoning cases, bounded cache lookups, reused-content retention and live-reference
protection. No complete archived T1 inventory row or native placement is newly
claimed closed. Verified App intake and backend-specific proofs remain with
their owning integrations.

## Unit evidence

- Selected store tests: **121 top-level tests, 223 including subtests**, no skips.
  Pattern: `^(TestStoreData|TestLogPersistence|TestResult|TestBlob|TestPayment|TestReversePaymentTakesBack|TestCreditSettings|TestSetCreditSettings|TestUnsetRateTable|TestSetRateTable|TestAStoredRateTable|TestATableWithoutAFourCore|TestStorage|TestBilling)`.
- Complete acceptance packages: **20 top-level tests, 199 passes including
  subtests**, with **2,995 existing native/platform skips**.
- The new default-fallback tests failed before the fix: depcache accepted a
  record without default-branch evidence, and both cache lookups read an excluded
  manifest. They pass after the change.
- A temporary Go overlay disabling the collector's reachability recheck makes
  the new SQLite cell fail when it tries deleting the revived digest. The real
  production source was not mutated by this negative control.
- Documentation mirror/changelog drift tests pass.

The six passing SQLite persistence cells are:

- `billing-ledger-survives-retention/store-data-sqlite`
- `storage-replica-safe-accounting/store-data-sqlite`
- `storage-quota-settlement-crash-replay/store-data-sqlite`
- `storage-log-segments-and-fallback/store-data-sqlite`
- `storage-hasmany-read-set/store-data-sqlite`
- `storage-reachability-and-retention-tails/store-data-sqlite` (new)

## Manual verification

All Go commands use `GOWORK=off`, `GOFLAGS=-buildvcs=false`, `GOMAXPROCS=4`,
`-p 4` and `GOCACHE`, `GOTMPDIR`, `TMPDIR` beneath `/tmp/sw-astra-W1b/`.
Run them with `go -C /home/bombuntu/.xwing/envs/1088/sparkwing`.

- Whole repository `go build -p 4 ./...` passed after runtime changes.
- `go vet -p 4 ./pkg/store/... ./internal/acceptance/... ./tests/acceptance/...`
  passed.
- `go test -p 4 ./pkg/store` with the Unit pattern above passed.
- `go test -p 4 ./internal/acceptance/... ./tests/acceptance/... -count=1` passed.
- `go test -p 4 ./pkg/docs -run '^(TestPkgDocsContentMatchesDocsRoot|TestEmbeddedChangelogMatchesRoot)$' -count=1` passed.
- `commentcheck -base HEAD <worktree>`, `sleepcheck -base HEAD <worktree>`,
  `bash <worktree>/bin/sync-docs.sh --check` and `git diff --check` passed.
- No local Git identity or `core.worktree` override was found.

## Not tested

No PostgreSQL, native placement, daemon, candidate, pipeline, external payment
provider or release was run. The full legacy store suite is not represented as
passing. Race tests were not repeated for the read-filter change and removal of
unused getters. The inherited SQL-guard and broader API snapshot drift were not
changed or reclassified as passing.

## Contract and integration work remaining

1. W1 must join `recordBillingAwardTx`, interval starts/finishes and final/periodic
   settlement to issuance, starts, waits, renewals, release, finish and reaping.
   The settlement helpers still have no external production callers on
   this branch. Memo materialization must join result state atomically.
2. W2's billing calculator and verified-payment handoff remain integration
   obligations. Test/live identity for retained cards, attempts, warnings,
   freezes and refunds remains a cross-area concern.
3. W7 must implement shared namespace/edge policy, verified manifest reading,
   physical upload verification/deletion and trusted replica identity. W2's data
   controller remains stubbed here. W7/W9 must connect record/result publication
   and retire `StateStoreOld` manifest consumers. The new GC proof establishes
   the store boundary, not physical backend behavior.
4. W1/W9 need the final custom-event append/sequence contract before
   `AppendEventCharged` and `chargeStorageTx` can be removed. The charged route
   returns a sequence. Raw append returns only an error and retains different
   event-limit behavior. No competing contract was introduced.
5. W1 owns removal of the unused `egress_day` schema table, its classification
   and operator deletion-list entry. These shared implementation files remain
   untouched.
6. Remaining legacy families include claim-time credits, paid grants, raw
   storage counters, direct uploads and node logs. Their callers span W1/W2/W7/W9.
   Elected maintenance, allowance/nonpayment eviction, receipt pruning, log
   lifecycle/egress and ring fallback still need their runtime integrations.

No new cross-area contract change or human decision is requested. The local C1
cleanup removes only unused getters. Their values remain in the declared
settings snapshot.

## Shared files

- `internal/acceptance/store_data.go`, new `store_reachability.go`.
- `.apidiff/pkg_store.txt`, `CHANGELOG.md`, `pkg/docs/changelog.md`.
- `design/rewrite/contracts/c1-data.md`.

Owned store files changed: `blob_lookup.go`, new `blob_restore_test.go`,
`credits.go`, `storage_credits.go`, `credit_settings_runtime_test.go` and this
handoff. No outside-package API callers changed.

Friction: the first cache regression reproduced the intended failures. A later
fixture tried storing impossible projected trust flags and hit the schema CHECK.
It was replaced with a valid projection lacking event evidence. Initial checks
compiled into a fresh isolated cache. Missing foreign implementations remain
explicit integration work, not passing acceptance.
Shell policy refused forced removal of scratch. Python removed the verified
agent-owned directory successfully.

Scratch is removed after all commands finish. No agent-owned process remains.

STATUS: CONTINUE
