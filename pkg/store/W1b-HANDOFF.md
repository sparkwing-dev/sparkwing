# W1b continuation audit

Base: c45420a8b. This session changes this report only. No implementation,
legacy deletion or test port is claimed. Net production Go delta: 0 lines.
The combined store delta remains +3,350 against main, 7,600 above the -4,250
budget, using the lead's exclusions. The overage remains unjustified.

The requested cuts still cross missing foreign contracts on this checkout:

- Output: NodeRow and state_read.go expose neither OutputRecord nor
  ManifestRecord. schema.sql has no persisted result selectors. NodeResult
  accepts those keys, and AttachResultRecords validates them, but the terminal
  read model cannot return the selection. W1 must persist/project the committed
  selectors, including materialized results without an execution attempt.
  W2/W3/W9 must carry those keys to authorized readers. Raw output consumers
  still include Client.FinishNodeWithReason, the nodehost scheduler/memo/retry
  paths, localState.GetNodeOutput and the controller's node/child output reads.
  Replacing those reads with a digest or a newest-record guess would discard
  the committed selection. Deleting these callers also requires replacing
  W3's active legacy host flow, beyond a minimal store caller migration.
- Payments (07:40 item 2): C2 PaymentEvent still has only checkout paid,
  expired and failed kinds. It has no verified card-setup, refund, warning or
  dispute facts. payment_events still requires a checkout foreign key and
  restricts event_kind to those three outcomes. W2 owns the expanded verified
  facts and provider correlation. W1 owns the receipt/schema join. W1b's eight
  transaction-local mutation owners and rollback/property proofs already exist.
  No unverified event entry point or foreign event fields were added.
- Spend/grace: accountUnitBillingTx still returns only error. Node, owner and
  eval renewals construct LeaseBeat before accounting. billing_awards has no
  per-award exhaustion anchor. W1 must supply the durable fence and a renewal
  result whose policy cancellation can commit with charges. A node-only legacy
  anchor cannot fence trigger, plan and eval awards. The existing immutable
  issuance/start/checkpoint/final/reap accounting join is present here.
- Logs: tagged persistence and SubjectUploader already replaced the old upload
  client. No production code constructs podNode.logs. W2 supplies the declared
  authenticated LogTransport and SegmentRecorder, and W3 supplies the persisted
  logseg.Data subject. Fakes already exercise the declared binding. This session
  did not invent another transport or subject resolver.
- Harness seeding (09:40 item 1): the seven store-data files already live in
  pkg/store as tests. internal/storetest supplies opening/rebinding, while
  teststore.SeedPresence supplies imported terminal attempts with SQL. The
  current CausePublish transition has no legal publication edge, and
  TransitionExtra.Attempt is start-only. W1 must provide terminal import seeding
  before those fixtures can exercise the publication API instead of inserting
  imported rows. Moving the same INSERTs into another helper would not prove
  the missing import path.

Remaining W1b integration work after those joins: retire raw output and legacy
claim-credit helpers with their callers, dispatch expanded verified events in
SettlePayment, complete the fixture conversion, and consume the committed
spend/grace result. ReadBlob already rechecks persisted authority and edges.
Its controller callback composition with ChargeDownload, source-before-run
binding, and physical allowance eviction remain open. The two authorized
output catalog removals are already present. No W1 file was edited.

Fresh evidence:

- Whole-repository go build -p 4 ./... passes.
- go vet -p 4 ./pkg/store passes.
- All seven preserved store-data acceptance tests pass in 1.839s:
  TestStoreCASIntegrity, TestStorePresenceReadSet, TestStoreLargePresenceBudget,
  TestStoreCacheScope, TestStoreReferenceEdges, TestStoreReachabilityRetention
  and TestStoreBillingRetention. These were already green before this session.
- commentcheck -base main is clean across 2,125 files.
- Checks use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
  GOCACHE=/tmp/sw-gocache and temporary directories under /tmp/sw-astra-W1b.
  No pipelines, native placement, provider webhook or PostgreSQL proof ran.

Shared files touched: none. No git writes or Sparkwing binaries ran. No new
mechanism, policy, schema or public API was added. The source-level contract
checks above are the reason for stopping this session. Worker scratch is
removed and /tmp/sw-gocache is retained.

STATUS: CONTINUE
