# W1b handoff

Continues 396aee074. Legacy log upload callers now use the declared tagged-log
binding. Output cutover, expanded verified payments, fixture seeding and
spend/grace remain open.

Changed:

- Deleted Client.LogSegment, SealLog, NewLogSegmentUploader and
  LogSegmentUploader. The remaining watch observer lives in logwatch.go.
  The old 276-line client file becomes a 54-line watch file.
- Deleted the unused 46-line EngineNodeLog wrapper and the 32-line
  LogSegmentRequest/LogSegmentGrant declarations. No Go caller names those APIs.
- Migrated the pod sink to logseg.Data and SubjectUploader. The binding supplies
  the subject, authenticated transport, transfer client, actor and recorder.
  The sink never infers team or attempt from a signed prefix. An absent binding
  produces a warning and the pod keeps its stdout log.
- Preserved live attempt attribution and heartbeat watch delivery. Owner-unit
  subjects use segment persistence without pretending to be job attempts.
- Updated C1, STORE-DATA, the existing changelog entry and migration section.
  Regenerated the affected API snapshots and embedded documentation.

Invariant: every signed upload and segment commit retains the bound tagged
subject, including when the caller mutates its original identity afterward.
FuzzNodeLogKeepsBoundSubject covers job, trigger, plan and eval identities and
varies attempt/source ordinals and generations. Its fake boundary checks the
actor, retention class, object key, segment append and complete seal.

Line budget:

- This session adds 58 and deletes 378 production Go lines, net -320.
- pkg/store deletes 32 production Go lines, net -32.
- pkg/store is +3,350 against main, 7,600 above the combined -4,250 target.
  Tests, testdata, acceptance code and contract declarations are excluded.
- The store overage remains unjustified. This cut adds no new public contract,
  policy, schema, setting or service. The pod's private binding field consumes
  the existing logseg.Data contract because the retired HTTP writers cannot
  supply an authenticated subject. W3 supplies its production value.

Evidence:

- Whole-repository go build -p 4 ./... passes.
- go vet -p 4 passes for pkg/store, pkg/controller/client and
  internal/orchestrator.
- Tagged-log store tests and all seven moved store-data acceptance tests pass
  in 5.943s. The preserved cells are TestStoreCASIntegrity,
  TestStorePresenceReadSet, TestStoreLargePresenceBudget, TestStoreCacheScope,
  TestStoreReferenceEdges, TestStoreReachabilityRetention and
  TestStoreBillingRetention. No additional placement cell is claimed.
- The migrated sink tests pass normally and with -race. A three-second fuzz run
  passes 4,734 executions with two workers. No sleeps or listening sockets.
- pkg/storage/logseg passes its full package suite in 0.174s.
- The full client package suite cannot run here. Its first unrelated
  TestObserveSlotMapsMissingHolder panics when httptest opens a TCP listener:
  socket: operation not permitted. No full client-suite pass is claimed.
- commentcheck -base main, API snapshot drift, embedded docs drift and
  git diff --check pass. API generation also caught up two snapshots from
  earlier W1b work. The initial drift failure and correction are recorded here.
- Git metadata is untouched. core.bare is false and core.worktree has no entry.
  No git command wrote state. No installed or candidate Sparkwing binary ran
  after the required initial discovery command.

All Go checks use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-gocache and GOTMPDIR/TMPDIR under /tmp/sw-astra-W1b.
The lead owns review, commits and pipelines. PostgreSQL, provider webhooks and
real HTTP/native placement integration were not exercised.

Remaining work and contract/integration requests:

1. W2 must bind authenticated LogTransport signing/verification and a
   SegmentRecorder. W3 must construct the persisted logseg.Data subject and
   supply podNode.logs. No production constructor supplies that field here.
   The old uploader already called deleted routes. This session removes that
   path and proves the replacement against the declared interfaces with fakes.
   Live-ring HTTP writes remain separate from persisted segment uploads.
2. W1 result-selector commits must join before raw output readers can retire.
   NodeRow/ReadNode here omit OutputRecord and ManifestRecord. W2/W3/W9 must
   carry those exact keys through authorized output reads. Materialization
   without an attempt still needs its destination identity contract.
3. A1-27 lifecycle accounting is joined on this checkout, but spend/grace still
   needs W1's durable per-award exhaustion fence and committing renewal-policy
   result. accountUnitBillingTx returns only error. Renewal assembles LeaseBeat
   before accounting. Legacy node anchors cannot fence trigger/plan/eval awards.
   Legacy W1 claim/trigger callers still use the old credit helpers.
4. Lead 07:40 item 2 / A1-28 remains pending W2's verified setup, refund,
   warning and dispute facts and W1's payment_events/schema correlation. C2
   PaymentEvent and the receipt table remain checkout-only. Existing financial
   transaction owners are implemented and tested. No foreign fact fields or
   unverified dispatch entry point were invented.
5. Lead 09:40 item 1 remains partial. The seven package tests are moved, but
   their SQL seeding remains. storetest supplies opening/rebinding rather than
   public publication and financial seed flows. CausePublish cannot import a
   terminal attempt on this checkout. TransitionExtra.Attempt is start-only.
   Existing teststore.SeedPresence still inserts imported attempts directly.
6. Source-before-run binding, ReadBlob/ChargeDownload composition and physical
   allowance eviction remain integration work. Existing tenant SQL/writer guard
   catalog debt from the prior report is untouched. W1 implementation and its
   catalogs were not edited.

Tests ported: the old client subject-copy and grant-substitution tests retire
with that client. The generated sink property and the existing SubjectUploader
identity/retention tests cover the retained identity concern. The live-watch
regression is preserved through fake transport. No new archived T1 source
identity is claimed closed.

Shared files touched outside pkg/store:

- internal/orchestrator/pod_node_host.go
- internal/orchestrator/segment_logs.go
- internal/orchestrator/segment_logs_test.go
- pkg/controller/client/logsegments.go (deleted)
- pkg/controller/client/logsegments_subject_test.go (deleted)
- pkg/controller/client/logwatch.go (new)
- design/rewrite/contracts/c1-data.md
- CHANGELOG.md and docs/migrations/_unreleased.md
- pkg/docs/changelog.md and pkg/docs/mirror/migrations/_unreleased.md (generated)
- .apidiff/pkg_store.txt, pkg_controller_client.txt, pkg_controller.txt and
  pkg_store_teststore.txt (generated)

Friction: socket-backed client tests are unavailable in the sandbox. The first
new sink test compile also caught a reversed LogObjectKey argument order, which
was corrected before the component proof. The remaining cuts depend on the
specific foreign contracts named above. No W1 file, lead handoff or amendment
changed. Worker scratch is removed and the shared cache is retained.

STATUS: CONTINUE
