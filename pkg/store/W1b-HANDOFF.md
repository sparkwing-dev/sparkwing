# W1b handoff

This continuation starts at `0273b4571`. W1b remains incomplete. Earlier
metering, payment, blob/record, policy, reporting and tagged-log implementations
are retained. W1's schema, admission, claims, state, reaper, quota-admission and
SQL-guard files were not edited. No Git writes were made.

## Implemented

- Blob upload tickets enforce configured monthly byte, lifetime run byte and
  lifetime run object limits. Pending transfers reserve all three dimensions.
  Empty new blobs reserve one object. Authorized existing content reserves zero
  bytes and objects. Existing tier values and zero-as-unlimited semantics stay
  the same.
- `upload_quota.go` owns three durable dimensions in the existing quota tables.
  The aggregate keys are `storage`, `storage.run.bytes:<run>` and
  `storage.run.objects:<run>`. Reservation IDs are the existing `upload:<ticket>`
  plus `run-bytes:` and `run-objects:` children. No schema change was needed.
- Reserve, commit and abort acquire monthly, run-byte and run-object quota locks
  in that order. Commit acquires all three before the blob lock. Settlement,
  receipt creation, unique-byte/object counts and replica usage remain atomic.
  Exact commit replay adds no usage. Abort releases all dimensions once.
- Monthly rollover resets only monthly capacity. Run dimensions and pending
  holds survive calendar changes and reopening. Historical reports keep their
  reservation period and sum run usage across periods.
- Leased expiry releases child holds after run/upload cascades, even when a
  generic monthly reservation already marked the parent hold released.
- Upload admission resolves effective default-tier policy for every new ticket.
  It creates no quota override row. An absent policy is unlimited. Changing the
  deployment default affects the next ticket. W1's separate generic quota path
  remains an integration point described below.
- The SQLite `storage-quota-settlement-crash-replay/store-data-sqlite` cell is
  active. Its fixture starts child test processes at before/after commit and
  before/after abort checkpoints. Each child exits with its store open. The
  parent waits, reopens, checks pending/committed/aborted state, replays operations,
  checks deduplicated usage and verifies every aggregate and hold. It also proves
  that pending uploads cannot exceed the configured run limit. Publication
  authority is preseeded and verified-upload evidence is a contract fake. Native
  signing, byte verification and controller checkpoints are not claimed.

## Deleted and ported

Replaced the single-budget upload reservation/settlement implementation:
**69 gross removed implementation Go lines**, including replaced declarations,
and **4 obsolete test lines** that counted one primitive reservation per ticket.
No legacy API family was fully removed in this continuation. Store implementation
Go grows by 82 lines including contract comments. The new quota owner is 74 lines.

Seven store regression tests cover pending run bytes, empty objects and aborts,
rollover/reopening/replay, default policy, independent clients, orphan expiry and
missing empty duplicates. The last two incorporate independent review probes.
Assertions use the tenant API and real SQLite databases. The expiry test uses a
read-only SQL oracle for orphan holds. No additional complete row of the archived
T1 port inventory is claimed closed.

## Verification

Go commands used `GOWORK=off`, `GOFLAGS=-buildvcs=false`, `GOMAXPROCS=4`,
`-p 4`, and `GOCACHE`, `GOTMPDIR` and `TMPDIR` under `/tmp/sw-astra-W1b/`.
The scratch tree is removed at handoff. The only installed Sparkwing invocation
was read-only `info --for-agent` during orientation. No candidate, daemon,
pipeline, external service, release or Git write was run.

- Whole-repository `go build -p 4 ./...` passed.
- `go vet -p 4 ./pkg/store/... ./internal/acceptance/... ./tests/acceptance/...`
  passed.
- Owned regression: **111 top-level tests / 206 including subtests**, no skips.
  Selection: `^(TestStoreData|TestLogPersistence|TestResult|TestBlob|TestPayment|TestReversePaymentTakesBack|TestCreditSettings|TestSetCreditSettings|TestUnsetRateTable|TestSetRateTable|TestAStoredRateTable|TestATableWithoutAFourCore)`.
- Race: **57 top-level tests / 108 including subtests**, no skips. Selection:
  `^(TestStoreData|TestBlobUpload|TestBlobGC)`.
- Acceptance packages: **20 top-level tests / 196 passes**, with **2,995 existing
  test skips**. SQLite cells now include storage replica accounting, storage
  settlement/crash/replay, and billing-ledger retention. Native targets remain
  skipped. `internal/acceptance` has no package-local tests.
- The new pending-byte and empty-object tests failed before implementation. A
  Go overlay restoring the prior upload implementation also made the new SQLite
  cell fail because the second upload bypassed the run limit.
- Independent snapshot review found lock ordering and missing process crashes
  in the first proof. Both were corrected. The second review found no remaining
  concrete issue. Its separate upload/GC tests and crash cell passed. Initial
  usage backfill was removed after review because the frozen design requires a
  fresh final store. There is no per-request historical scan or migration.
- `commentcheck -base HEAD` passed across 10 Go files. `sleepcheck -base HEAD`,
  embedded-doc sync checking and `git diff --check` passed. Public production
  API signatures are unchanged, so no API snapshot regeneration was needed.
- SQL scope guard still fails with **28 inherited findings**: 14 unscoped SQL
  statements and 14 stale exemptions. None concerns the new quota owner. W1's
  guard was not edited. PostgreSQL, native placements and pipeline gates were
  not run in this socket-restricted worker.
- No local Git identity or `core.worktree` override was found. Test subprocesses
  are joined by the fixture driver. No scratch-owned test process remains.

## Assumptions and mechanisms

Two run quota dimensions use the existing reservation mechanism because monthly
bytes alone cannot reserve per-run capacity or count a zero-byte object. This
adds no table, environment variable, service or customer limit. Removing those
run dimensions would undo this implementation and restore the enforcement gap.
The final store starts empty, as the frozen hard-cut policy requires. Existing
prototype upload tickets are not backfilled or adapted.

## Remaining work and real integration

1. W1 must integrate immutable billing awards into issuance with server-derived
   resources/pricing, open/close billable intervals at execution/wait boundaries,
   charge renewals and settle terminal/release/reaper transactions at persisted
   finish/lapse. Reconciliation still needs atomic memo materialization.
2. W1's generic `quotaPolicyTx` still requires an explicit storage quota row.
   It must consume effective policy without freezing deployment defaults. The
   owned upload path now resolves defaults and enforces per-run bytes/objects.
   W1's maintenance coordinator must retain the upload hold/GC ordering.
3. W2 still owns billing calculation, verified opaque payment events, payment
   configuration and final operator routes. W1/W2/W9 must settle test/live mode
   identity for retained cards, attempts, warnings, freezes and refund work.
4. W1b still needs legacy cuts in claim-time credits, paid grants, node logs,
   old event/manifest charges, raw storage counters and legacy uploads.
   `chargeStorageTx` still writes principal columns. Its old consumers and
   fixtures remain. Allowance/nonpayment eviction still needs record/pin policy.
5. W7 must wire upload reserve/lookup/verified commit/abort to physical integrity
   and trusted ReplicaID, manifest reads, blob deletion and cloud provenance.
   Shared edge/namespace contract stubs remain covered by existing fakes.
6. Final maintenance must coordinate allowance/nonpayment eviction, both GC
   phases and the remaining retention tasks. Legacy receipt/identity pruning
   and the separate storage pass still need final elected-worker ordering.
7. W2/W7 must wire tagged log subjects, canonical keys, retention classes,
   proxy egress, lifecycle rules, physical expiry and live-ring fallback.

No new foreign contract change or human decision is requested. The C1 document
records local quota keys, lock order and the SQLite proof boundary. Outstanding
cross-area contracts and old code prevent a COMPLETE claim.

## Shared files

- `internal/acceptance/store_data.go`: dispatch the new proof and require its
  crash driver. Its sole caller in `tests/acceptance/storage_test.go` is migrated.
- `internal/acceptance/store_upload.go`: new SQLite proof and child fixture.
- `tests/acceptance/storage_test.go`: subprocess crash driver and checkpoint mode.
- `design/rewrite/contracts/c1-data.md`: local implementation/verification notes.
- `docs/self-hosting.md`, `CHANGELOG.md` and their generated `pkg/docs` mirrors.

Owned store files are `blob_accounting.go`, `blob_persistence.go`,
`upload_runtime.go`, `upload_contract.go`, `upload_runtime_test.go`, the new
`upload_quota.go`, the new `upload_quota_runtime_test.go` and this handoff.
There are no production caller migrations outside `pkg/store`.

Friction remains in foreign integration stubs, inherited SQL-guard debt,
old-schema fixtures and native/PostgreSQL execution. No new tooling blocker.

STATUS: CONTINUE
