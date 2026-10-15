# W1b handoff

Continues 3b6125cb3. Earlier payment mutations and other completed work remain in
that commit and W1b-session1792078204.out. The worktree started clean. W1
implementation files and the lead-owned handoffs/amendments are unchanged.

Implemented:

- Cut controller persisted log reads over to Tenant.ReadRunView and
  logseg.CommittedReader with Tenant.ReadLogSegments. Job reads choose the newest
  ordinal/generation. Streams use that same ordering for their cursor. Whole-run
  reads include tagged job, trigger, plan and eval logs.
- Bound the browser log reader to its authenticated capability prefix or session
  digest. Each persisted-segment read and live-ring delivery rechecks authority.
  No listing or physical manifest grants access. Existing response-byte egress
  metering and log download-cap exemption remain at the HTTP boundary.
- Missing or corrupt committed bytes fail loudly. Incomplete seals emit a loss
  line or a JSON log_incomplete SSE event. Completeness verifies persisted bytes
  before reporting the database seal. It never changes the job verdict.
- Moved Segment, SegmentCommit and LogSegments into result_logs.go. RecordSegments
  and ReadLogSegments are the implementations there, with their forwarding
  methods removed from result_contract.go. Steps, annotations and summaries remain
  in that contract file. logIdentity uses blob.LogSubject.Validate instead of its
  separate shape validator.

Invariant: physical listings never authorize bytes, and log delivery requires
current persisted authority for the exact tagged subject. The generated reader
property covers arbitrary payloads, foreign actors and authority ending after
inventory selection. A real final-schema SQLite test drives award, start, segment
commit and terminal transition through the controller reader, then revokes its
capability and verifies refusal before object I/O. A live-ring test proves fresh
authority checks without sleeps or sockets.

Deleted and ported:

- Removed NodeLog, LatestNodeLog, RunNodeLogs, NodeLogSettled, their scanners and
  nodeLogColumns. node_logs.go loses 109 net lines. No production Go reader or
  writer queries node_logs or node_log_segments. W1's tenantTables catalog still
  names those retired tables; it was left untouched under the ownership rule.
- Removed the controller's logseg.Open/listing/manifest read path. It reads exact
  committed segment keys instead. The legacy claim grace constant and old writer
  wire structs remain because their callers have not cut over.
- Ported TestNodeLogSettlesOnlyWithNoRetryToCome from legacy handwritten SQL and
  its dispatch fixture to final RunView state. It keeps failure-until-run-finish,
  successful/skipped/cancelled terminal and unknown-job assertions. No archived
  finding is claimed closed.
- This session adds no acceptance cells, registry entries or harness files.

Contract changes and real integrations still needed, in priority order:

1. Lead 07:40 item 2 / A1-28: internal/capability/payment.go still exposes only
   checkout_paid, checkout_expired and checkout_failed. W2 must supply the opaque
   verified setup, refund, warning, dispute and collection facts and provider
   handoff. W1 must supply receipt schema and trusted setup/customer correlation.
   payment_events still requires a checkout FK and restricts kind to those three
   outcomes. SettlePayment remains checkout-only and refuses zero proof. Its
   transaction-local mutation owners from the prior session are ready. No shadow
   proof type, metadata-selected payer or unverified replacement was introduced.
2. A1-27: spend/grace needs a committing renewal-policy result and a durable
   exhaustion fence per immutable award. W1's accountUnitBillingTx returns only
   error, and LeaseBeat is built before accounting. The existing node-only
   credit_exhausted_anchor cannot represent independent host/plan/eval awards.
   The join must preserve prepaid runway, unit minima, customer-runner wait
   exclusion and holder drain. Ordinary award/start/finish accounting and C2's
   calculator are implemented. Legacy claim-credit callers still block deletion.
3. Output clients still use OutputReadGrant, NodeOutputObject and unbound
   Store.GetNodeOutput. W2/W3/W9 need the authenticated reader and persisted
   selected record key. This branch's ReadNode still has no persisted result
   selector. W1's newer result_records work is visible in its predecessor report
   but is not merged here. Attempt-free materialization still conflicts with
   positive destination-attempt keys/authority. W1/W7 must settle that contract
   before CopyMaterializedRecords can use the transaction-local mutation owner.
4. Log read persistence is cut over. The production HTTP/host cutover remains:
   W2 owns authenticated blob.LogTransport signing/verification and final tagged
   readers, W3 supplies persisted subjects, and W9 migrates the old client.
   LogSegmentUploader still calls retired writer routes from
   internal/orchestrator/segment_logs.go. This session migrates existing browser
   readers; it does not claim the new policy-router log endpoints are installed.
   The remote browser object adapter is still the configured S3 log object store.
   Native/provider/PostgreSQL and real HTTP transport evidence remain open.
5. Lead 09:40 cleanup still includes store_billing, store_cache_scope, store_cas,
   store_data, store_gc_fence, store_presence, store_presence_large,
   store_reachability and store_reference_edges harness files. Some seed helpers
   serve W7. They remain behind the requested runtime priorities.
6. Source-before-run binding, ReadBlob/ChargeDownload composition and physical
   allowance eviction remain open. A1-24 and A1-29 through A1-35 persistence stays
   implemented. Record mutation authorization and the two approved catalog edits
   were completed in earlier sessions.

Verification:

All Go commands used GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-gocache and GOTMPDIR/TMPDIR=/tmp/sw-astra-W1b/gotmp. Passed:

- go build -p 4 ./... for the whole repository.
- go vet -p 4 ./pkg/store ./pkg/controller ./pkg/storage/logseg ./tests/acceptance.
- Focused controller/store/logseg tests matching TestCommittedDashboard*,
  TestDashboardLogActor*, TestNodeLogSettles*, TestLogPersistence*,
  TestCommittedRead* and TestRunView*. Final timings were 0.211, 4.718 and 0.037
  seconds respectively, without skips.
- Existing storage-log-segments-and-fallback/filesystem-records and
  filesystem-sink acceptance cells, 12.510 seconds total, without skips. They use
  fake authenticated transports and direct signed filesystem handlers. They do
  not prove W2's production HTTP log binding.
- go run ./internal/commentcheck -base main . clean across 2,119 files.
- git diff --check.

No whole-repository test suite, provider, PostgreSQL, native execution or pipeline
pass is claimed. The lead owns independent review and commits.

Budget, shared files and assumptions:

The session reduces production Go by 77 lines across touched packages. pkg/store
falls by 102 lines and is +3,566 versus main, 7,816 over the combined -4,250
W1/W1b target. Measurement excludes tests, testdata and *_contract.go /
*_contracts.go scaffolds. The area is not within budget. Legacy
credit/output/claim cuts remain necessary. Moving implemented log declarations
out of excluded scaffolding accounts for result_logs.go's seven-line increase.

Caller migrations outside pkg/store:

- pkg/controller/browser_reads.go
- pkg/controller/log_reads.go
- pkg/controller/log_segments_internal_test.go
- pkg/controller/log_reads_test.go (new focused tests)

Shared documentation: design/rewrite/contracts/c1-data.md records the read cutover
and the new declaration locations. No foreign contract was changed. The local
segmentLogRecords interface combines two existing Tenant methods and has a
scripted test double. No new table, setting, cache, trust root or service was
added. Whole-run reads now include every committed tagged subject; job reads
still select the latest attempt.

Friction: the expanded verified-event contract and receipt schema remain absent
on this branch. Initial compilation caught the live-ring chunk type name, which
is corrected. The filesystem acceptance cells emit about 2,300 audit lines with
-v; bounded output hid the middle, but both cell verdicts and the final exit
status were retained. Final checks pass.

Scratch /tmp/sw-astra-W1b is removed after final checks. /tmp/sw-gocache is kept.
No service was started. Git author remains Korey Gambill and core.worktree is
unset. No Git metadata or harness memory was written.

STATUS: CONTINUE
