# W1b handoff

Continues 3cb5fb9c9. This session removes legacy log compatibility handling and
one obsolete store lookup. It does not finish the output/log writer cutovers,
payment-event expansion, API-based fixture seeding or spend/grace join.

Changed:

- Deleted Client.KeepsLogSegments and the remote-follow capability probe.
  Follow opens its stream directly and preserves stream refusal errors.
- Deleted ErrLogSegmentsUnavailable, its old-controller response classification
  and the unused unavailable result from openNodeLogSinkLive. A signing refusal
  now remains its own error and produces a warning. No endpoint or signing
  transport was invented.
- Deleted Store.TriggerRepoID. Its sole test caller reads the repository ID
  from the tenant trigger it already loaded.
- Converted TestFollowLogsRemote_EndsOnARefusedStream to a socket-free transport
  under testing/synctest. It rejects a capability probe and preserves a forbidden
  stream response. Its fixture now serves the current /jobs route and array.

Line budget:

- Net production Go delta this session: -80 overall, -12 in pkg/store.
- pkg/controller/client loses 47 lines. internal/orchestrator loses 21 lines.
- pkg/store is +3,646 against main, 7,896 above the combined -4,250 target.
  Tests, testdata and *_contract.go/*_contracts.go scaffolds are excluded.
- This is partial cleanup. It does not justify the remaining store overage.

Evidence:

- Whole-repository go build -p 4 ./... passes after the final production changes.
- go vet passes for pkg/store, pkg/controller/client and internal/orchestrator.
- The seven moved cells pass: TestStoreCASIntegrity, TestStorePresenceReadSet,
  TestStoreLargePresenceBudget, TestStoreCacheScope, TestStoreReferenceEdges,
  TestStoreReachabilityRetention and TestStoreBillingRetention.
- The focused run passed 139 store tests, selecting TestBlob*, TestLogPersistence*,
  TestMeteredAccounting*, TestMeteredDisruption* and TestPayment* in 44.282s.
- TestLogUploader* passed in pkg/controller/client.
- TestFollowLogsRemote_EndsOnARefusedStream passed in internal/orchestrator.
- commentcheck -base main is clean across 2,120 files. git diff --check passes.
- The initial wider selection failed 14 legacy tests before source edits.
  They still use tokens, runs.args_json or triggers.pipeline, all absent from
  the final schema. The output named claim_team_boundary_test.go,
  credit_rates_test.go, credits_test.go, execution_attribution_test.go,
  tenant_node_state_test.go and trigger_claim_credits_test.go. This session
  does not claim a green full store suite or delete those tests.
- The first socket-free stream test exposed its stale /nodes fixture and
  deadlocked under synctest. Updating the fixture to /jobs and its current
  response shape fixed the test. No production retry or timer was added.

All Go checks use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-gocache and GOTMPDIR/TMPDIR under /tmp/sw-astra-W1b.
No provider, PostgreSQL, native launcher, real HTTP or full-suite pass is claimed.
The lead owns independent review, commits and pipelines.

Remaining work and contract/integration requests:

1. Output cutover needs W1's terminal result-selector commits joined into this
   branch. NodeRow/ReadNode here still omit OutputRecord and ManifestRecord.
   W1's latest reports describe that implementation on its branch. W2/W3/W9
   must preserve those exact keys through authenticated output reads before
   NodeOutputObject, OutputReadGrant and Store.GetNodeOutput can be retired.
   The destination identity for a materialized result with no attempt remains
   undeclared for the W1b copier. No source key was guessed from current counters.
2. The real log writer still needs W2's authenticated blob.LogTransport and
   recorder adapters, W3's persisted subject binding and W9's transport.
   The legacy POST log-segments/log-manifest routes are already absent here.
   LogSegmentUploader still calls them. W7's SubjectUploader already consumes
   the final LogTransport/SegmentRecorder contracts with fakes. Those component
   proofs do not provide a production host binding.
3. Spend/grace still needs a per-award durable exhaustion fence and a committing
   renewal-policy result from W1. accountUnitBillingTx returns only error, and
   renewal assembles LeaseBeat before accounting. The existing node-only anchor
   cannot fence trigger/plan/eval awards. Legacy Store claims and trigger writers
   still call the old accounting helpers. They cannot be deleted independently
   while preserving those callers' behavior.
4. Lead 07:40 item 2 / A1-28 remains blocked on foreign contracts. W2's
   PaymentEvent has only checkout kinds and lacks verified setup/refund/warning/
   dispute facts. W1's payment_events schema requires checkout_id and constrains
   kind to checkout outcomes. SettlePayment remains checkout-only. The existing
   transaction-local payment mutation owners are ready. No fabricated proof or
   metadata-selected payer was added.
5. Lead 09:40 item 1 is partly complete from the preceding session. Seven
   store-data checks are package tests, and their harness dispatchers are gone.
   Raw fixture SQL remains. storetest currently supplies targets/opening/rebind,
   not publication or financial seed flows. Publication fixtures require terminal
   imported attempts. On this branch TransitionExtra.Attempt is accepted only
   for CauseStart, while CausePublish moves a pending job directly terminal.
   A public imported-attempt path is needed before SeedPresence can replace its
   node_attempts INSERT with the real publication transaction. This is a W1
   contract/integration request, not permission to copy its state writer here.
6. Source-before-run binding, ReadBlob/ChargeDownload application composition
   and physical allowance eviction remain open.

No new test port closes an archived finding. No foreign contract signature,
W1 implementation file, schema, lead handoff or amendment was changed.
The only W1 test edit is the TriggerRepoID caller migration in retry_team_test.go.

Shared files changed outside pkg/store:

- pkg/controller/client/logsegments.go
- internal/orchestrator/jobs_cli_remote.go
- internal/orchestrator/segment_logs.go
- internal/orchestrator/sse_follow_test.go
- design/rewrite/contracts/c1-data.md (log-follow behavior only)

Friction: the assigned large cuts need branch integration and the foreign
contracts named above. The remaining old paths still have production callers.
Replacing those calls with empty success, deriving result keys, or fabricating
payment proofs would lose behavior or authority checks. No such adapter was added.

Worker scratch /tmp/sw-astra-W1b is removed. The shared build cache stays.

STATUS: CONTINUE
