# W1b handoff

Continues 674815bd5 after integration merge 67e6fe79b. W1b remains incomplete.
The worktree started clean. No W1 implementation file changed.

## Implemented and deleted

The implemented payment API moves from payment_contract.go to
payment_settlement.go. Upload request, ticket and verified-evidence types move
from upload_contract.go to blob_upload.go. ReserveBlobUpload, LookupBlobUpload,
CommitBlobUpload and AbortBlobUpload now own their implementations directly in
upload_runtime.go. Their four private forwarding targets and public wrapper
bodies are gone. Public signatures and runtime behavior are unchanged.

Deleted two scaffolding files containing 287 lines. The real files retain 239
lines, and upload_runtime.go adds four GoDoc lines. Net production reduction
this session is 44 lines, including 12 executable forwarding lines. No legacy
credit, output or log consumer is claimed retired in this session.

The C1 document follows the new file ownership and retains the trusted ticket
and configured payment-mode requirements. No new mechanism, schema, pricing
rule, authority rule, compatibility adapter or environment variable was added.
No test-only cell or archived test port was added.

## Priority work and contract changes needed

1. Lead decision 07:40 item 2 remains blocked on W2's verified-event contract.
   internal/capability/payment.go still declares only checkout paid, expired and
   failed. The W2 branch at 3656dec97 has the same three outcomes. The opaque
   VerifiedPaymentEvent cannot carry setup, collection, refund, warning or
   dispute facts yet. W2 must supply the verified setup/customer/method/card
   correlation, collection payment/attempt correlation, refund identity/amount/
   status, warning identity/payment/card/actionability and dispute identity/
   payment/outcome. Provider metadata must not select a payer.
2. W1 must provide the matching financial schema contract. payment_events still
   requires checkout_id and a same-team/mode credit_checkouts foreign key. Its
   event_kind CHECK allows only the three checkout outcomes. Initial card setup
   has no committed setup/customer-to-team correlation. W1b will dispatch the
   verified events and their business mutations within one receipt transaction
   once these contracts exist. Existing operator methods with their own commits
   cannot be invoked from that transaction.
3. W1's lifecycle join is now present in this checkout. The earlier report's
   claim that unit_billing.go is absent is superseded. Issuance, start, renewal,
   finish, release and reaping call W1b's helpers. Spend/grace is still open:
   accountUnitBillingTx returns only error and discards BillingSettlement.
   billing_awards has no per-award exhaustion anchor. W1 must supply the
   committing policy-result/renewal contract and schema fence before W1b joins
   the policy. Preserve prepaid runway, customer-runner waits, unit-specific
   minima and holder drain. W1's claim/trigger writers still call the legacy
   credit helpers, so deleting those helpers would break the surviving callers.
4. Output reads still use OutputReadGrant, NodeOutputObject and the unbound
   Store.GetNodeOutput path. W9's NewDataClient exists, but W2/W3/W9 still need
   authenticated reader binding and a selected final record key. MaterializeSource
   now accepts a committed terminal result by StateSeq without a synthetic
   attempt. The blob ResultRecordKey/write authority still requires a positive
   execution attempt. The transaction-local CopyMaterializedRecords interface
   remains assigned only by tests. W1/W7 must settle the destination identity
   and read edge for an attempt-free materialized result. W1b then implements
   that existing copy seam with its shared record mutation owner. Calling
   public CopyMemo first would commit records outside the state transaction.
5. LogSegmentUploader still posts to retired writer routes. Controller reads
   still call LatestNodeLog/RunNodeLogs over node_logs, which is absent from
   the final schema. Production SignSegments and VerifyUpload implementations
   of blob.LogTransport are absent. W2/W9 must bind that authenticated transport,
   and W3 must carry the persisted tagged subject. RecordSegments,
   ReadLogSegments, SubjectUploader and CommittedReader already implement the
   final persistence/byte boundary. No substitute route or authority is added.
6. The lead-owned amendments-1.md A1-32 source link still names
   pkg/store/upload_contract.go. The lead should point it to blob_upload.go
   and upload_runtime.go. The amendment file was left unchanged as instructed.

A1-24 and A1-29 through A1-35 persistence work stays implemented. A1-27
spend/grace, A1-28 expanded events and the consumer cutovers above stay open.
Source-before-run binding, ReadBlob/ChargeDownload composition and physical
allowance eviction still need their production adapters. A1-53 native/provider/
PostgreSQL evidence remains with T1/lead. These are unimplemented joins, not
requests to restore retired APIs. Foreign-contract changes stop at the swarm
brief's ownership boundary.

## Verification

All Go checks used GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-gocache and GOTMPDIR/TMPDIR=/tmp/sw-astra-W1b/gotmp.
Build, vet and tests used -p 4. Passed:

- Whole-repository go build ./....
- go vet ./pkg/store.
- Existing TestBlobUpload, TestUpload, TestPayment, TestAWarnedPurchase,
  TestStoreDataMetered, TestStoreDataUnmetered and TestMeteredUnit selections
  in pkg/store, 24.876 s.
- Fourteen existing SQLite acceptance cells, 2.279 s: billing-start-award-versus-
  start, billing-ledger-survives-retention, storage-cas-integrity,
  storage-replica-safe-accounting, storage-quota-settlement-crash-replay,
  storage-log-segments-and-fallback, storage-hasmany-read-set,
  storage-hasmany-large-budget, storage-reachability-and-retention-tails,
  storage-memo-and-depcache-scope, storage-reference-edge-laundering,
  storage-publish-one-way, storage-owner-receipts-and-stream-verification
  and storage-local-remote-cache-opt-in.
- Commentcheck against main, clean across 2,119 files.
- git diff --check.

No new cell was enabled. No whole-store suite, native placement, provider,
PostgreSQL, Sparkwing binary or pipeline pass is claimed.

## Budget, shared files and friction

Using the lead's production-Go exclusions, pkg/store is +3,645 net lines
against main in this worktree. The combined W1+W1b target is -4,250, so it is
7,895 lines over budget. The cleanup removes 44 actual lines but exposes 243
previously excluded lines by moving real implementation out of contract-named
files. It adds no behavior that needs an overage exception. Legacy credit,
output/log and W1 lifecycle cuts remain necessary before completion.

Shared file touched: design/rewrite/contracts/c1-data.md only. No caller in
another package needed migration because the exported signatures are identical.
W1 files, lead handoffs/amendments and the port inventory are untouched.
The stale dependencies above were checked against the merged code and branch
contracts. No implementation or archived-test failure was encountered in the
selected checks. Native integration is unverified.

The lead owns review, commits and pipelines. No Git metadata, harness memory,
service, provider, deployment or release was written. Scratch
/tmp/sw-astra-W1b/ is deleted after final checks. /tmp/sw-gocache is retained.

STATUS: CONTINUE
