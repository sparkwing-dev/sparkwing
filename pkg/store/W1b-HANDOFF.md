# W1b handoff

Continues `14cbe2891`. W1b remains incomplete. Changes are uncommitted.
The lead owns review, committing, pipelines and native integration.
No Git writes, Sparkwing binary runs or W1 implementation edits were made.

## Implemented

Memo outputs now obey the existing 16 MiB output cap at record commit and lookup.
An oversized persisted output produces an integrity error for a required lookup
and a miss for memo lookup or copying. No destination edge is copied. A
boundary-sized output still copies. Manifest-only records keep their separate
artifact contract. W7 namespace and reference policy are tested through the
existing contract fake, with real SQLite transactions.

Removed the legacy source/upload pruning APIs and the controller calls that
used them. Controller cache listings have no age-expiry callback. The extended
reachability acceptance proof keeps a 120-day-old source bundle through deletion
of its originating run. Record expiry then makes it collectible only after the
persisted tombstone grace. Physical deletion uses a contract fake.

The final upload reservation, receipt, quota, record, GC, accounting, payment and
log implementations from earlier sessions remain in place.

## Deletion and ports

Removed `Store.ClaimExpiredSourceBundles`, `Store.DeleteSourceBundleRows`,
`Store.PruneExpiredUploads`, `DirectCacheMaxAge` and
`controller.CacheObjectMaxAge`: **102 removed production lines** in the store and
controller pruning files. Removed **249 old test/guard lines** and five API
snapshot entries. These are gross deleted-line counts. Caller replacements and
new tests are separate. The one-run source-binding test remains.

Legacy source lifetime and pending-expiry concerns now use the final
reachability and upload-quota proofs. Object-age deletion expectations are
reversed to record reachability. No archived T1 inventory row is newly claimed
fully ported or closed.

## Unit

Added three tests in `blob_output_limits_test.go`. The oversized write and read
cases failed before the fix, then passed. They verify the exact write refusal,
integrity errors, cache misses, absence of partial copies, the inclusive size
boundary and manifest-only behavior.

Nine SQLite persistence cells pass. No new native cell was unskipped:

- `billing-ledger-survives-retention/store-data-sqlite`
- `storage-replica-safe-accounting/store-data-sqlite`
- `storage-quota-settlement-crash-replay/store-data-sqlite`
- `storage-log-segments-and-fallback/store-data-sqlite`
- `storage-hasmany-read-set/store-data-sqlite`
- `storage-reachability-and-retention-tails/store-data-sqlite` (extended)
- `storage-memo-and-depcache-scope/store-data-sqlite`
- `storage-reference-edge-laundering/store-data-sqlite`
- `storage-publish-one-way/store-data-sqlite`

## Manual

Commands use `GOWORK=off GOFLAGS=-buildvcs=false GOMAXPROCS=4`, `-p 4`,
and GOCACHE/GOTMPDIR/TMPDIR beneath `/tmp/sw-astra-W1b/`. Run with
`go -C /home/bombuntu/.xwing/envs/1088/sparkwing`.

- Whole repository `go build -p 4 ./...` passed.
- `go vet -p 4 ./pkg/store/... ./internal/acceptance/... ./tests/acceptance/... ./pkg/controller ./cmd/sparkwing-controller` passed.
- Store selection pattern:
  `^(TestStoreData|TestLogPersistence|TestResult|TestBlob|TestPayment|TestReversePaymentTakesBack|TestCreditSettings|TestSetCreditSettings|TestUnsetRateTable|TestSetRateTable|TestAStoredRateTable|TestATableWithoutAFourCore|TestStorage|TestBilling)`.
  It passed with **131 top-level tests / 287 passes including subtests**, no skips.
- `go test -p 4 ./internal/acceptance/... ./tests/acceptance/... -count=1`
  passed with **20 top-level tests / 202 passes including subtests** and
  **2,995 existing native/platform skips**.
- Controller and controller-command test packages compile with `-run '^$'`.
- Documentation mirror and changelog drift tests passed. Commentcheck,
  sleepcheck and `git diff --check` passed.
- Both tenant writer guards passed. The SQL scope guard still reports the same
  **10 inherited W1 findings** in authority, awards and retention.
- No local Git identity or core.worktree override was found.

## Not tested or failing

The full `go test -p 4 ./pkg/store/... -count=1` run failed in **30 top-level
pkg/store tests** and `teststore.TestOpenKeepsStateSeparate`. These concern old
schema classification, execution policies, claims, concurrency, tokens, database
paths and the SQL guard. Failures include missing `sparkwing_meta` and `tokens`
tables, old profile fixtures and a token-test panic. The internal/storetest
package passed. No W1b-focused test failed. The full package run does not prove
all remaining tests because the token-test panic terminates that package.

No PostgreSQL, race, provider, daemon, native placement, browser, candidate,
pipeline or release check was run. The brief assigns runtime pipelines and
independent review to the lead because this sandbox cannot open sockets.
API snapshots remove only this session's five symbols. Integration must
regenerate the full snapshots after all areas finish their hard cuts.

## Contract changes needed and real integrations remaining

No foreign contract was changed or newly requested. No schema, setting, limit,
exported interface or runtime mechanism was added. The memo cap enforces the
existing frozen output limit. C1 notes and migration docs describe the hard cut.

1. W1/W2 must define imported trigger/plan/eval identities for published owner
   logs. Jobs use exact imported terminal attempts. Owner publication stays
   refused because publish intake has no executable unit lease.
2. W1 must join immutable billing awards, interval start/finish, periodic charges
   and settlement to issuance, starts, waits, renewal, release, finish and reaping.
   Memo edges and result state must materialize atomically. Published run, plan
   and attempt import precedes W1b blob/log publication.
3. W2 must supply the real billing calculator, verified payment handoff and
   data authorization/signing. Card attempts, warnings, refunds and freezes
   still need end-to-end test/live identity coverage.
4. W7 must supply namespace/edge policy, verified manifest reads, physical
   verification/deletion and trusted replica identity. W1/W7 must wire final
   retention, upload quota release and tombstone GC to the elected worker.
   The legacy storage pass still reconciles raw counters and prunes old outputs
   and node logs. Its source and upload age-pruning calls are gone.
5. W2/W7/W9 must wire final segment signing/transfer, publication, lifecycle
   expiry, proxy egress and ring fallback. Old node-log persistence remains used
   by controller log_segments.go, log_reads.go and storage_pass.go.
6. W1/W9 must define final custom-event append/sequence behavior before
   AppendEventCharged and chargeStorageTx can go. W1 owns removal of egress_day
   and remaining table classification/scope guard cleanup.
7. Legacy claim credits, paid grants, raw storage counters, direct upload and
   output persistence still have W1/W2/W3/W7/W9 consumers. W7/W9 must retire
   StateStoreOld manifest consumers. No compatibility shims were added.

No human decision is requested. Next work is final caller migration and atomic
integration through those contracts, with fakes where their implementations are
still unavailable.

## Shared files and friction

Caller changes outside pkg/store: `pkg/controller/storage_pass.go`,
`pkg/controller/storage_pass_test.go`, `pkg/controller/direct_upload_test.go`
and `cmd/sparkwing-controller/main.go`. They remove old pruning calls, retire
old expectations and drop the cache age-expiry callback.

Other shared changes: `internal/acceptance/store_reachability.go`,
`design/rewrite/contracts/c1-data.md`, `docs/security.md`,
`docs/migrations/_unreleased.md`, their embedded mirrors, `CHANGELOG.md`,
`pkg/docs/changelog.md`, `.apidiff/pkg_store.txt` and `.apidiff/pkg_controller.txt`.
Only W1b's obsolete SQL exemption changed in the shared store guard.

Friction: missing foreign runtime owners and legacy-schema fixtures remain.
The removed pruning paths had continued to reference retired tables. An unused
controller test import after deletion was fixed. Scratch is deleted after final
verification. No owned process remains.

STATUS: CONTINUE
