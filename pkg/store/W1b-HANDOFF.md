# W1b output cleanup

Base: ca4b80846. Net production Go delta this session: -135 lines
(29 added, 164 deleted), including -125 in pkg/store. The combined store
production delta against main is +3,225, or 7,475 above the -4,250 budget.
The overage remains unjustified. Legacy store-data retirement is incomplete.

Deleted the legacy WriteOutputFileDurably API, syncIfMatches, both platform
sync/rename implementations and their two durability tests. The local writer
uses a fresh attempt key and ordinary file writes because recoverable output
may be lost on a crash. Reads retain bounded size and digest verification.
No alias or replacement durability abstraction was added.

The legacy controller output client's PUT/GET now uses W7's existing
blob.TransferClient. Deleted its duplicate request/response handling and old
node-protocol transfer header. Controller transport selection still compares
scheme and full host/port. Signed requests carry no controller credential or
claim fence. Transfer close errors fail before upload commit or read success.
The old output upload/read/report APIs remain until the result-selection join.

Invariants checked: a read returns bytes only when they equal the committed
size and SHA-256; signed output transfers receive no controller bearer or
claim headers. FuzzOutputReadRequiresExactBytes passed 4,587 generated cases.
FuzzOutputTransferKeepsControllerCredentialsPrivate passed 5,424 cases.
The deleted sync tests asserted a stronger crash guarantee than the frozen
recoverable-data contract. No additional historical acceptance port is claimed.

Verification:

- Whole-repository go build -p 4 ./... passes.
- go vet -p 4 ./pkg/store ./pkg/controller/client passes.
- Seven preserved store-data acceptance tests pass: TestStoreCASIntegrity,
  TestStorePresenceReadSet, TestStoreLargePresenceBudget, TestStoreCacheScope,
  TestStoreReferenceEdges, TestStoreReachabilityRetention and
  TestStoreBillingRetention. These cells were green before this session.
- Focused output read/transport tests and both fuzz seed suites pass.
- The broader TestStoreData/TestBlob/TestLogPersistence/TestPayment selection
  passes in 68.304s, including the complete record-mutation authority property.
- W7 internal/blob output tests pass in 0.407s.
- commentcheck -base main is clean across 2,125 files.
- The existing socket output-upload integration cannot run: sandbox sockets
  are refused. The same-origin credential property uses a fake RoundTripper.
- TestFinishNode_KeepsLocalOutputBytesBesideTheStore fails before output work:
  legacy CreateRun inserts the removed runs.args_json column. That W1 lifecycle
  dependency was not changed. No native or provider-HTTP proof is claimed.

Contract changes needed in other areas:

- W1: persist/project committed output and manifest selectors, including
  materialized results without an attempt. NodeRow still has neither selector.
  W2/W3/W9 must carry them to authorized readers before raw output APIs can go.
- W2: PaymentEvent still declares only checkout paid/expired/failed. Supply
  verified card-setup, refund, warning and dispute facts and correlation.
  W1 must extend payment_events beyond its checkout foreign key and three-kind
  constraint. W1b's transaction-local mutation owners already exist.
- W1: provide the per-award exhaustion anchor and committed renewal result
  for spend/grace. accountUnitBillingTx returns only error; node, owner and
  eval renewals construct LeaseBeat before accounting. Existing legacy node
  anchors cannot fence all billing units.
- W1: provide imported terminal-attempt seeding for the fixture conversion.
  Correction to the prior report: CausePublish has a legal pending-to-terminal
  edge. TransitionExtra.Attempt is still start-only, so that edge cannot seed
  imported attempt identity. Seven store-data tests are already relocated,
  but teststore.SeedPresence still inserts the imported rows directly.
- W2/W3: supply the authenticated LogTransport/SegmentRecorder and persisted
  logseg.Data subject to the actual host. Tagged persistence and SubjectUploader
  exist, but production podNode.logs still has no constructor assignment.

Other open integration: physical allowance eviction, source-before-run data
binding and ReadBlob/ChargeDownload composition. The two authorized output
catalog removals remain present. No new pricing or restriction was added.

Shared files touched: pkg/controller/client/output.go and
pkg/controller/client/output_transport_test.go. No W1 file or lead-owned
handoff/amendment was edited. No git writes or Sparkwing binaries were run
after the required orientation. Worker scratch is removed at completion;
/tmp/sw-gocache is retained.

STATUS: CONTINUE
