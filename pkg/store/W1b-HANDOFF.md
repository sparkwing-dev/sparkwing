# W1b handoff

W1b remains incomplete. Upload ticket persistence is implemented. W1 integration,
native acceptance and the remaining legacy cuts are open. No claim, state,
admission, reaper or schema implementation file was edited. The duplicate
`plan_secret_contract.go` stub was deleted because the merged tree declared
`PlanSecretOpener` and its setter twice. W1's `plan_contracts.go` implementation
stays in place. No git writes were made.

## This continuation

- Implemented `ReserveBlobUpload`, `LookupBlobUpload`, `CommitBlobUpload` and
  `AbortBlobUpload` against the final uploads table. Tickets bind the exact
  uploader, digest, size, expiry, provenance, sealed ref and quota reservation.
  Ticket/reservation creation commits together. Commit atomically records
  existence, receipt, ticket completion, unique-byte usage and quota settlement.
- Replay is immutable. Abort is durable and idempotent. Released tickets cannot
  reopen. Committed tickets replay after expiry while authority is live. Owner
  units and imported publication attempts round-trip through SQL.
- Readable duplicates reserve no additional bytes and work at the quota ceiling.
  Unreadable duplicates still require a full reservation. Zero reservations
  cannot fund bytes that disappear before commit. Upload admission shares the
  quota lock with generic reservations and independent store clients.
- The leased tombstone-mark phase releases expired upload reservations, including
  those whose upload rows disappeared through run retention.
- `Authority.Upload` hints must match live identity. Publication uploads retain
  local provenance and cannot manufacture an unimported job output.
- Payment settlement writes checkout audit events once per transition and clears
  exhaustion when the resulting balance is positive. Audit failure rolls back
  the financial write.
- Added a SQLite acceptance boundary for replica sums, quota races, release replay
  and singleton ownership. It uses public store APIs and an independent SQL
  oracle. Native placement targets remain skipped.

Deleted the 40-line provisional `BlobVerifier`/`CommitBlob` preamble and the
15-line duplicate plan-secret stub. `BlobAccounting` is private. Replaced 349
lines of provisional tests with public ticket tests, moving the surviving
helpers into `upload_helpers_test.go`. Legacy direct-upload, log, storage-credit
and claim-time accounting implementations remain.

New tests cover the store portions of T1's interrupted-upload replay, checksum
conflict, deduplication at a quota ceiling, uploader isolation and quota restart
obligations. No complete native port-inventory item is claimed.

## Existing implementation retained

Immutable billing awards, price/card snapshots, reservations, interval starts and
pauses, renewal checkpoints, settlement and original-card/day refunds survive
run/capability pruning. Tests fake C2's missing calculator. Pauses cannot rewind
charged checkpoints.

Payment-facts settlement derives teams/credits from checkouts, deduplicates events
and provider payments, handles late payment, and rejects cross-team payment reuse.
Checkout creation persists mode, USD cents and credits. Attachment is immutable.

Blob records check each primary, manifest and member digest for existence and
edge authority. Receipts, dependencies, generated groups, declared pinned pipeline
references and cache scopes supply read edges. Memo declarations and input hashes
are required. Record metadata requires authority over that named record. Memo
copies commit both result records together. Bad cache bytes become misses/cold
restores. Source provenance survives source-run deletion. Leased tombstones use
shared locks, recursive reachability, grace and deletion-failure replay.

Tagged logs retain derived object keys, contiguous immutable segments, empty
seals, exact final replay, bounded job tail grace and current read authority.
Incomplete logs do not alter job outcome.

## Integration still required

1. W1 must use `recordBillingAwardTx` with server-derived metering, resources and
   pricing. Issuance still refuses metered roots and calls unmetered
   `persistBillingAwardTx` here. Start launcher intervals at execution, pause
   around customer-runner waits, charge renewals, and settle terminal/release/reap
   transactions at their persisted end/lapse. Reconciliation needs atomic memo
   materialization before memoized success, without starting compute merely to
   copy. Award data scopes need permitted default/base refs.
2. W2 must implement C2 `SettleBilling`, payment authentication and
   `VerifiedPaymentEvent.Event`. Store tests exercise verified facts, not provider
   signatures. Controller configuration must persist payment mode.
3. W7 must use ticket APIs for signing, physical commit and abort. Pass observed
   bytes after `ObjectBackend.CommitObject` and fill the required
   `VerifiedUploadCommit.ReplicaID` from controller configuration. Supply the
   verified manifest reader and blob deleter. Namespace/edge functions remain
   stubs here and are faked in record tests.
4. W1 must invoke both blob GC phases from leased maintenance. Mark now releases
   expired ticket reservations. W2/W7 log signing must use
   `LogSegmentObjectKey` and persisted retention classes. Proxy egress, backend
   lifecycle rules, actual byte integrity and live-ring fallback remain external.
5. Remove `CloseCreditCheckout`, `CreditGrantRequest.Checkout` and legacy paid
   grant routes with W2's payment-route cut. Console reversal/payment lookup
   still expect a raw grant reference. New grants use
   `payment:<mode>:<checkout>`, linking provider IDs through checkout/event rows.
   Move those lookups to the persisted payment/grant association.
6. Remove remaining legacy blob/log/storage-credit and claim-time billing APIs
   with their consumers. W1-owned writer/SQL guard bookkeeping remains open:
   storage usage tables are now written, provider payment lookups cross teams
   under provider authority, and existing authority/retention exemptions remain.

No other area's contract changed. The local C1 addition is the required trusted
`VerifiedUploadCommit.ReplicaID`. Ticket IDs use one ASCII
letter/digit/underscore/hyphen segment. Ref facts come from the immutable sealed
request after checking persisted repo/ref projections. Cloud provenance keeps the
existing awarded-executor-class mapping. Confirm it against C2/W7 trusted builders.

## Verification

All commands used `GOWORK=off`, `GOFLAGS=-buildvcs=false`, and `GOCACHE`,
`GOTMPDIR` and `TMPDIR` beneath `/tmp/sw-astra-W1b/`. Test parallelism was at most
four. No Sparkwing binary, pipeline, installed daemon or provider was run.

- Repository `go build -p 4 ./...` passed.
- `go vet -p 4 ./pkg/store/... ./internal/acceptance/... ./tests/acceptance/...`
  passed.
- Focused store selection passed 56 top-level tests and 102 cases. The final
  missing-bytes deduplication test also passed, for 57 tests/103 cases total.
  Selection: `^(TestStoreDataMetered|TestLogPersistence|TestResult|TestBlob|TestPayment)`.
- Upload tickets and payment settlement passed under `-race -p 4`. The final
  deduplication failure test also passed under the race detector.
- `go test -p 4 ./internal/acceptance ./tests/acceptance -count=1` passed.
  The new cell is
  `TestStorage/storage-replica-safe-accounting/store-data-sqlite`. It also passed
  under `-race`. Native targets remain skipped. This boundary checks aggregate
  runs, not principal admission through authenticated routes.
- Full store tests still fail on `TestRecordProfileObservation_IgnoresLegacySamples`
  (`pipeline_profiles.team`), `ExampleOpen` and `teststore.TestOpenKeepsStateSeparate`
  (removed `sparkwing_meta`). Those fixtures were left to their owners.
- `git diff --check` passed. HEAD stayed `1793d602b`. No local Git identity or
  `core.worktree` override appeared. PostgreSQL/native integrations are unproved.

Shared files: deleted `pkg/store/plan_secret_contract.go`, corrected its references
in `design/rewrite/contracts/c1-data.md` and `amendments-1.md`, added
`internal/acceptance/store_data.go`, and activated its boundary in
`tests/acceptance/storage_test.go`. All other changes are W1b-owned store files.
No production caller outside pkg/store changed.

Friction: duplicate secret declarations broke the initial merged build. One race
selector ran no tests and was replaced with separate selectors before claiming
evidence. Legacy fixtures and external implementation stubs remain. The scratch
directory was deleted after every check process exited.
