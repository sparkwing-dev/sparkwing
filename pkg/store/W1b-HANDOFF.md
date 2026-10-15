# W1b handoff

Continues 9eb702f7a. The full output/log cutovers, expanded verified payment
settlement, fixture seeding and spend/grace join remain open.

Changed:

- Deleted Store.NodeIsAncestor and its deps_json traversal. The controller now
  calls Tenant.NodeIsAncestor, which uses the existing blob dependency resolver
  over sealed specs. This covers ordinary, optional, artifact and generated-group
  dependencies in one transaction and keeps the tenant predicate in that owner.
- Replaced TestNodeIsAncestor_FollowsDepsTransitively with a generated graph
  property against the final schema. It checks independent transitive closure,
  self exclusion and a foreign tenant. The old traversal failed graph=1 because
  it could not see dependencies in spec_json. All 20 seeds now pass. A three-second
  fuzz run passed 60 executions with two workers.
- Deleted the duplicate MaxOutputBytes and MaxRunOutputBytes constants. Remaining
  callers use limits.NodeOutputBytes (16 MiB) and limits.RunOutputBytes (1 GiB).
  File reads are bounded and close failures discard bytes. Signed output reads
  reject a body above the shared bound even with a matching size and digest.
- Deleted the unused controller metered-in-process guard, its refusal constant
  and private legacy-token lookup. No registered route called that guard.
- Shortened billing GoDoc and deleted narrative comments in five owned files.
  A Go parser comparison confirms their code ASTs are unchanged. Existing safety
  explanations retain their original formatting.

Line budget:

- Net production Go delta this session: -307 overall, -264 in pkg/store.
- Of that reduction, 240 lines are comment cleanup in credits.go, credit_rates.go,
  credit_billing.go, card_billing.go and storage_credits.go. This does not retire
  the remaining legacy billing implementation.
- pkg/store is +3,382 against main, 7,632 above the combined -4,250 target.
  Tests, testdata and contract declaration scaffolds are excluded. The remaining
  overage has no new justification from this session.

Evidence:

- Whole-repository go build -p 4 ./... passes.
- go vet passes for pkg/store, pkg/controller, pkg/controller/client and
  internal/nodehost.
- The focused store-data selection passes in 41.563s: blob, tagged-log, metered
  accounting, disruption, payment, both generated properties and the seven moved
  store-data acceptance cells. No new acceptance cell was unskipped.
- The preserved cells are TestStoreCASIntegrity, TestStorePresenceReadSet,
  TestStoreLargePresenceBudget, TestStoreCacheScope, TestStoreReferenceEdges,
  TestStoreReachabilityRetention and TestStoreBillingRetention.
- Client output transfer bounds and same-origin transport checks pass. The
  nodehost output reader passes at exactly 16 MiB and refuses 16 MiB plus one
  with otherwise valid JSON, size and digest. These checks use fake transports.
- Retired output and log upload route properties pass through in-process routers.
- commentcheck -base main and git diff --check pass.
- The tenant guard selection fails on untouched payment_mutations.go helpers and
  W1-owned catalogs. reviewedUnscopedSQL still exempts Operator.SettleCardPayment,
  Operator.FailCardAttempt and Operator.ReportCardRefund instead of their moved
  transaction owners settleCardPaymentTx, failCardAttemptTx and reportCardRefundTx.
  The writer catalog still requires node_logs, node_log_segments,
  storage_reservations and storage_commit_receipts, all retired writers.
  These files match HEAD. No full-store-suite pass is claimed.

All Go checks use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-gocache and GOTMPDIR/TMPDIR under /tmp/sw-astra-W1b.
The lead owns independent review, commits and pipelines. No provider, PostgreSQL,
native launcher or real HTTP integration was exercised.

Remaining work and contract/integration requests:

1. W1 result-selector commits must join this branch before raw output reads can
   retire. NodeRow/ReadNode here omit OutputRecord and ManifestRecord. W2/W3/W9
   must preserve those exact keys through authenticated output reads. The
   destination identity for materialization without an attempt remains undeclared.
2. The real log writer needs W2 authenticated LogTransport/SegmentRecorder
   adapters, W3 persisted subject binding and W9 transport. Legacy HTTP writers
   are absent, while LogSegmentUploader still calls them. W7 SubjectUploader
   component proofs with fakes do not provide a production host binding.
3. Spend/grace needs W1's per-award durable exhaustion fence and a committing
   renewal-policy result. accountUnitBillingTx returns only error and renewal
   assembles LeaseBeat before accounting. The legacy node-only anchor cannot
   fence trigger/plan/eval awards. W1 legacy claim and trigger callers still
   reference the old accounting helpers.
4. Lead 07:40 item 2 / A1-28 needs W2's verified setup/refund/warning/dispute
   facts and W1's payment_events schema extension. Both remain checkout-only.
   Existing transaction-local mutation owners are ready for the verified seam.
5. Lead 09:40 item 1 remains partial. The seven package tests are moved, but their
   fixture SQL remains. storetest has opening/rebind helpers, not publication or
   financial seed flows. A public terminal imported-attempt path is still needed.
   On this branch TransitionExtra.Attempt is accepted only for CauseStart, while
   CausePublish moves a pending job directly terminal.
6. Source-before-run binding, ReadBlob/ChargeDownload composition and physical
   allowance eviction remain open. W1 also needs the guard catalog updates named
   above. This session does not edit W1's files.

The local C1 contract change replaces Store.NodeIsAncestor with its tenant method
and removes the duplicate output-limit exports. No foreign contract signature,
lead handoff, amendment or schema changed. No new mechanism or archived test port
is claimed beyond replacing the existing ancestry regression.

Shared files changed outside pkg/store:

- pkg/controller/node_output.go
- pkg/controller/credits.go
- pkg/controller/client/output.go
- pkg/controller/client/output_transport_test.go
- internal/nodehost/routes_upstream.go
- internal/nodehost/routes_output_limit_test.go
- design/rewrite/contracts/c1-data.md

Friction: the large remaining cuts still require the branch joins and foreign
contracts named above. No source result key, payment proof or log binding was
invented to bypass them. An initial nodehost test-name selection matched no tests.
The explicit output reader regression then ran and passed. Commentcheck initially
rejected joined safety comments over 120 characters. Restoring their original
formatting fixed that failure.

Worker scratch /tmp/sw-astra-W1b is removed. The shared build cache stays.

STATUS: CONTINUE
