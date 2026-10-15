# W1b handoff

Continues 0aca55e2f and W1b-session1792074610.out. W1b remains incomplete.
The worktree started clean. The lead owns review, commits and pipelines.

## Implemented

Log persistence separates decoded ranges from physical byte counts. The first
regression failed with 20,000 retained bytes for a 109-byte gzip payload.
SubjectUploader supplied decoded Segment.Size, and RecordSegments used that
value for both cursor ranges and physical inventory.

C1 Segment adds StoredBytes for the immutable compressed payload. Size and
Offset retain decoded cursor semantics. Digest covers the stored payload.
The schema already has start/end offsets and stored_bytes, so no schema change
is needed. The uploader supplies both sizes on append and final sealing.
ReadLogSegments reconstructs decoded Size from the range. CommittedReader
verifies stored length and digest before emitting decoded bytes. Omitted stored
size is refused for a segment. Empty final seals remain valid.

Invariant: retained inventory counts each compressed segment once, while read
cursors follow decoded bytes. Replay cannot change either size. The regression
checks compressed accounting, decoded selection, immutable replay and invalid
stored sizes. Existing generated physical-upload acceptance checks compressed
inventory after sealing, replay and reopening. Filesystem and S3 objects,
lost upload responses, resumed reads and revocation remain covered.

Deleted legacy APIs/code: zero lines. The single-size accounting expressions
are replaced. No archived T1 test was ported or retired. No new acceptance cell
was enabled, and no pricing setting, authority rule or compatibility path changed.

## Verification

Go commands used GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-astra-W1b/gocache, GOTMPDIR=/tmp/sw-astra-W1b/gotmp and
TMPDIR=/tmp/sw-astra-W1b/gotmp. Build, vet and test used -p 4.

Passed:

- Whole-repository `go build ./...`.
- go vet ./pkg/store ./pkg/storage/logseg ./internal/acceptance.
- Store TestLogPersistence and TestResultSegments tests.
- All pkg/storage/logseg tests, including stored-length and partial-read refusals.
- TestStorage/storage-log-segments-and-fallback: existing store-data-sqlite,
  filesystem-records, s3-records, filesystem-sink, s3-sink and storage-components
  checks pass. Native placement skeletons skip. internal/acceptance has no
  top-level test under this selection.
- Commentcheck against main: clean across 2,119 files.
- Both pkg/docs mirror guards, changelog check and git diff --check.

The expanded store selection also ran TestResultFinalLogs successfully but
failed two untouched legacy tests in execution_attempt_log_claim_test.go.
TestNodeExecutionAttemptBelongsToLiveClaimOutlivesTheAttempt seeds removed
runs.args_json. TestTriggerExecutionAttemptBelongsToLiveClaimOutlivesTheAttempt
seeds removed triggers.pipeline. Both fail before log accounting. The full
pkg/docs probe failed in TestWebClient_Versions_HappyPath because the sandbox
refuses httptest's TCP listener. The socket-free mirror guards pass.
No full store-suite, native, provider, PostgreSQL or pipeline pass is claimed.

## Remaining contracts and work

1. Decision 07:40 item 2 needs W2's expanded verified PaymentEvent. This checkout
   and bw-3095-w2-credentials still declare only checkout paid/expired/failed,
   with checkout/payment IDs, currency, amount and mode. W2 must declare verified
   setup/customer/method/fingerprint/completion facts, collection attempt/payment
   correlation, refund identity/amount/status, warning identity/payment/card/
   actionability and dispute identity/payment/outcome. Metadata cannot select
   the payer. Existing transaction helpers remain ready for the dispatcher.
2. W1 owns payment schema changes. payment_events requires checkout_id,
   a same-team/mode credit_checkouts foreign key and one of three checkout
   outcomes. Initial setup has no durable setup/customer-to-tenant correlation.
3. W1's spend/grace join remains open, including on bw-3095-w1-store.
   accountUnitBillingTx returns only error, renewal builds LeaseBeat before
   accounting and billing_awards has no exhaustion anchor. W1 must expose the
   committing policy result for job/host/plan/eval. W1b then supplies spend/grace
   policy while preserving prepaid runway, customer waits and holder drain.
4. Output consumers still use OutputReadGrant, and localState forwards
   GetNodeOutput without final authority/record keys. Controller log reads
   still query node_logs. LogSegmentUploader still calls removed writer routes.
   W2/W3/W9 must supply authenticated blob/log bindings and final keys. This
   slice fixes final tagged storage/uploader/reader accounting, while consumer
   cutovers and legacy claim-credit retirement remain open.
5. W1's private CopyMaterializedRecords has no production implementation.
   Materialized memo/retry/coalesced destinations need a declared record identity
   and read edge without a synthetic attempt. Public CopyMemo requires a live
   positive attempt. The prior configured manifest-reader join is implemented.

A1-24 and A1-29 through A1-35 retain implemented persistence behavior. Live
data-authority resolution, ReadBlob/ChargeDownload composition, source-before-run
binding, verified small-output use and allowance-driven physical eviction remain
integration work. A1-27 lifecycle accounting joins remain implemented, with
spend/grace and legacy credit retirement open. A1-28 remains checkout-only.
A1-53 native/provider/PostgreSQL evidence stays with T1/lead. No lead-owned
handoff, amendment or port inventory was edited.

## Shared files and friction

No W1 implementation file changed. The local C1 change adds Segment.StoredBytes.
C2 can retain the RecordSegments signature when it implements log transport.
Outside pkg/store:

- pkg/storage/logseg/subject.go supplies stored size at two construction sites.
- pkg/storage/logseg/committed.go and committed_test.go consume both lengths.
- internal/acceptance/blob_logs.go checks compressed physical inventory.
- internal/acceptance/store_logs.go, store_abandoned_logs.go and
  store_published_logs.go supply explicit sizes in existing fixtures.
- design/rewrite/contracts/c1-data.md documents the local contract and GoDoc.
- CHANGELOG.md and its generated pkg/docs/changelog.md mirror.

The changelog fixer initially regrouped duplicate categories. Those unrelated
changes were removed, and sync-docs regenerated only the one-line mirror update.
The first focused rerun exposed a fixture that changed decoded sizes but kept
its old stored size. It now supplies both. The broader legacy fixture and socket
failures above remain visible. No foreign event or renewal contract was guessed.

No Git write, Sparkwing candidate, pipeline, service, release, subagent or
harness-memory write ran. Git identity remains Korey Gambill with no
core.worktree override. Scratch /tmp/sw-astra-W1b/ is deleted after verification.

STATUS: CONTINUE
