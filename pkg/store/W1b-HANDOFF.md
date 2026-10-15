# W1b handoff

W1b is incomplete. The new persistence code is ready for integration testing,
but the legacy cut and acceptance driver remain open. No W1 admission, claim,
state, reaper, schema or retention file was edited. No git writes were made.

## Implemented

- Immutable billing awards, reservation and rate snapshots, interval start/pause,
  renewal checkpoints, final settlement, original-card/day refunds, replay and
  ledger locking. Settlement survives claimant pruning. Overlapping intervals,
  future timestamps and conflicting replays fail.
- Payment settlement uses the credit ledger lock and refuses a provider payment
  already associated with another checkout, including another team's checkout.
- Verified blob commits retain exact uploader identity. Records validate every
  digest and manifest member. Read sets follow receipts, dependencies, generated
  groups, declared pinned pipeline references and authorized cache scopes.
- Memo lookup/copy, depcache pointers, server-owned ref checks, cold/miss failure
  handling, and source provenance that survives source-run deletion.
- Leased tombstone marking and collection with shared blob locks, a grace period,
  recursive reachability, a final reachability recheck and failure replay.
- Public tagged log persistence, strict subject shape, derived segment object
  keys, contiguous immutable segments, final seals, terminal tail grace, owner
  and eval fencing, and reads under current authority.

## W1 integration

1. In `authority_issue.go`, replace the metered-award refusal and the unmetered-only
   `persistBillingAwardTx` call with derived metering and `recordBillingAwardTx`.
   Pricing and reservation size must come from server settings and the accepted
   resource envelope. The helper accepts those resolved values, never wire input.
2. Call `startBillableIntervalTx` at actual execution start for launcher awards.
   Pause/resume metered hosts around customer-runner waits. Use a new interval key
   per compute span. `at_award` opens the first span when the award is stored.
3. Call `chargeBillingAwardTx` inside renewal and `settleBillingAwardTx` inside
   terminal, release and reaper transactions. Pass the persisted lapse when a
   lease expires. Retain existing credit exhaustion and stop decisions.
4. Wire memo-copy records into reconciliation and terminal result authorization.
   The exposed copy path presently requires a live job attempt. Owner-side
   materialization before execution still needs a transaction path aligned with
   W1's attempt and claim decisions. Do not start compute just to copy a memo.
5. Award data scopes must include permitted verified default/PR-base read refs.
   The predecessor's `awardDataScope` populates only the write ref.
6. Invoke the two blob GC phases from the existing leased maintenance pass.
   No separate service or maintenance lease was added.

## W2 and W7 integration

- C2 `SettleBilling` and `VerifiedPaymentEvent.Event` are still stubs in this
  checkout. Accounting tests inject the calculator. Payment tests exercise the
  transactional verified-facts boundary. Signed-provider integration is unproved.
- `blob.RecordNamespace` and `blob.AuthorizeEdge` are still stubs. Tests inject
  implementations of the existing contract functions. Default record methods
  call the shared implementation and therefore need W7's implementation.
- Supply `BlobVerifier`, `BlobManifestReader` and `BlobDeleter` from the signed
  byte backend. `Tenant.BlobRecords(reader)` implements the record interface with
  verified manifest metadata. The plain Tenant methods have no manifest reader.
- Signing must use `LogSegmentObjectKey` and the run's persisted retention class.
  Proxy egress, live-ring fallback and actual byte integrity remain W2/W7 work.
- Connect upload quota reservation/settlement to verified blob commit. Blob rows
  deduplicate bytes, but these new methods do not settle a signing reservation.

## Local contract additions and assumptions

`STORE-DATA.md` documents the added integration methods and record naming.
`ResultBlobKey` isolates result keys by attempt. Memo names are `output` or
`manifest`; depcache scope excludes pipeline/job. These conventions need adoption
by W1/W7 callers. No other area's contract file was changed.

Manifest path/mode metadata stays in verified manifest bytes because the frozen
`blob_members` table stores only digest edges. Live receipts remain GC roots until
run retention removes them, because they grant the uploader continued read access.
Cloud provenance derives from the awarded executor class for non-publication
records. Confirm that mapping against C2/W7's trusted-builder path during integration.

## Verification and limits

Final verification passed: repository `go build -p 4 ./...`,
`go vet -p 4 ./pkg/store/...`, and 34 focused top-level tests (63 passing
cases including subtests). `git diff --check` passed. Focused tests use real SQLite
transactions and fake only the missing calculator, namespace/edge functions and
byte backend. The repository build and store vet use the prescribed isolated Go
cache. PostgreSQL, native signers, payment signatures and the end-to-end acceptance
matrix have not been proved.

The broader legacy metering selection fails on removed `tokens`, `sparkwing_meta`
and trigger columns. The tenant SQL guard also fails on predecessor authority,
retention and payment lookup statements. Provider checkout lookup and payment-ID
deduplication intentionally cross teams under provider authority and need the
`(*Store).settlePaymentFacts` exemption in the shared guard. W1's guard was left
unchanged. Its writer checks pass.

No acceptance cells were unskipped. New tests cover portions of the T1 manifest,
reference-edge, upload-conflict, memo-copy and retention obligations, but no full
port-inventory item is claimed closed. Legacy accounting/blob/log callers and
implementations still need deletion. Legacy code deleted in this session: 0 lines.

Existing files touched are `blob_records.go`, `payment_contract.go`,
`result_contract.go`, `result_logs.go` and the log cases in `result_runtime_test.go`.
All other additions are W1b-owned files in `pkg/store`. No files outside that
package were changed.

Reproduce the focused test selection with `go test -p 4 ./pkg/store -count=1`
and this `-run` value:

```text
^(TestStoreDataMetered|TestLogPersistence|TestResult|TestBlobPersistence|TestBlobGC|TestBlobRecords|TestPaymentSettlement)
```

The commands used `GOWORK=off`, `GOFLAGS=-buildvcs=false`, and private
`GOCACHE`, `GOTMPDIR` and final-run `TMPDIR` beneath `/tmp/sw-astra-W1b/`.
All verification processes exited before that directory was removed.
