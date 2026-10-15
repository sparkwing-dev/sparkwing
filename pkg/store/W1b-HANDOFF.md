# W1b handoff

Continues `1333bceb3` and W1b-session1792071630.out. W1b remains incomplete.
The lead owns review, commits and pipelines. This session made no Git writes.
The worktree started clean with no merge in progress.

## Implemented and deleted

Removed the legacy `Store.OpenNodeLog`, `SignNodeLogSegment`, `NodeLogSegments`,
`MarkNodeLogSealed`, `GetNodeLog` and `CurrentNodeAttempt` APIs, their segment
and error types, the node `log-segments` and `log-manifest` HTTP writers, and
their S3 presigner. Removed the generic legacy log-storage counter writer.
No production Go writer references `node_logs` or `node_log_segments`.

Legacy reads no longer write abandoned-log seals. The elected
`Operator.SealAbandonedLogStreams` pass owns that operation on tagged streams.
The legacy capabilities response no longer advertises segment upload support.
The controller's read-only object helper now returns the shared `logseg.Objects`
interface directly. Team deletion uses the existing shared log URL lifetime.
No schema, quota, grace, price, setting, alias or compatibility wrapper was added.

Production Go removes 603 lines and adds 14, for a net reduction of 589 lines.
Sixteen tests for the retired transport are removed, totalling 825 test lines.
The three unrelated heartbeat, live-ring and node-settlement tests remain.
The old transport tests are not final HTTP or native integration evidence.
No new archived T1 source-function identity is claimed ported or closed.

Earlier completed work remains: immutable metered award/interval accounting,
A1-27 settlement joins, verified checkout settlement, operator credit/card
handles, tenant blob upload tickets, exact uploader receipts, record/member
and memo/depcache authorization, tombstones, result attachment, tagged logs,
leased retention, replica accounting and the direct-upload writer cut.

## Invariants and verification

Invariant: no request to a retired node-log segment or manifest route reaches
an upload or persistence writer. `TestRetiredLogPersistenceRoutesHaveNoWriterProperty`
checks 128 generated run/job/operation paths against the real router without a
socket or store. Final signed-object immutability and leased tagged-log sealing
also pass their controller regressions after the cut.

Go commands used GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4, -p 4,
and GOCACHE/GOTMPDIR/TMPDIR under /tmp/sw-astra-W1b/.

- Whole-repository `go build -p 4 ./...` passes.
- Vet passes for pkg/store, pkg/controller and both acceptance trees.
- Focused store/controller selection passes 70 top-level tests / 320 cases.
- Billing/storage acceptance passes 287 cases and skips 562 native/platform cases.
- Fourteen existing SQLite store cells pass. These cover billing start and
  ledger retention, blob integrity, quota replay, replica accounting, tagged
  logs, read sets, budgeted presence checks, reachability, memo/depcache scope,
  reference-edge authorization, publication, exact receipts and remote-cache opt-in.
- Final signed log-object immutability, tagged maintenance, retention-class and
  retired-writer controller regressions pass.
- Final commentcheck against main passes across 2,117 Go files. API-spec tests,
  API snapshot, docs mirror and whitespace checks pass. The API-reference
  selection compiled but matched no tests.

Reproduce the focused selection with:

`go test -p 4 ./pkg/store ./pkg/controller -run '^(TestRetired(OutputUpload|LogPersistence)Routes|TestLogPersistence|TestLogPublication|TestLogMaintenance|TestMeteredAccounting|TestMeteredUnit|TestPayment|TestStoreDataMetered)' -count=1`

Acceptance: `go test -p 4 ./tests/acceptance ./internal/acceptance -run '^(TestBilling|TestStorage)' -count=1`.

One snapshot check detected the deleted read-only S3 wrapper after the first
snapshot generation. Regenerating after the final source cut fixed that drift.
No full-suite, PostgreSQL, provider, native placement, race or pipeline pass is claimed.

## Amendment and handoff audit

- A1-24, A1-29 through A1-35: tenant authority, metadata, read sets, records,
  members, exact receipts, ticket settlement, depcache pointers, tombstones and
  tagged persistence remain implemented. C2 must compose live data resolution,
  ReadBlob, ChargeDownload and the physical manifest reader in production.
- A1-27: real award/start/renewal/retry/finish/release/reaper settlement joins
  are present and their focused tests pass. Spend/grace and legacy credit
  retirement remain open. W1's billing and renewal files were not edited.
- A1-28 and lead decision 07:40 item 2: committed checkout settlement and raw
  paid-grant refusal remain done. Expanded provider settlement still requires
  C2's verified-event contract. No foreign contract shape was changed.
- The approved record-mutation owner, two approved catalog deletions and earlier
  output/raw-storage/direct-upload cuts remain done. This session adds the
  legacy log writer cut. It does not complete the old reader or client cutover.
- A1-53: native, provider and PostgreSQL evidence remains with T1/lead.
  No native acceptance cell was unblocked or marked passed.

## Contract changes needed and remaining work

W1 must expose the spend/grace join and its durable per-award exhaustion state.
The final billing_awards/billable_intervals tables have no exhaustion anchor.
The existing old anchor is node-only. Renewal fills LeaseBeat before accounting,
and accountUnitBillingTx returns only error. Job, host, plan and eval need one
policy result delivered in the same committing renewal reply. Preserve existing
grace/spend policy, prepaid runway, customer-wait exclusions and holder drain
rights. Do not add a raw terminal-state writer or a caller clock.

W2 must extend PaymentEvent/VerifiedPaymentEvent beyond checkout identity and its
three outcomes. Card setup needs verified setup/customer/method/fingerprint facts.
Collection needs persisted attempt/payment correlation. Refunds need verified
identity, amount and status. Warnings/disputes need provider identity, payment
correlation and outcome. W1b then extends SettlePayment, deriving teams/amounts
from committed rows and retaining event idempotency. The lead already authorized
building pending ratification. Missing contract shape remains the blocker.

W1/W7 must complete the materialized-result record contract before the missing
transactional copier can bind safely. CopyMaterializedRecords accepts memo,
retry and coalesced results without requiring destination execution, while public
CopyMemo requires a live positive destination attempt. The final state contract
forbids synthetic attempts. Define the destination record identity and read edge
for an unexecuted persisted result, then W1b can share its transaction-local
mutation owner with reconciliation. Calling public CopyMemo separately cannot
satisfy atomic state/record rollback. No speculative identity format was added.

W2/W3/W9 must compose the final blob.LogTransport signing, verification, commits,
reads and metering. The retained Client.LogSegmentUploader targets the removed
node writers. Legacy NodeLog/LatestNodeLog/RunNodeLogs and segmentLogs still read
retired tables. They are not final transport adapters. WithLogRetention remains
available for the final signing composition. This cut makes no claim that old
host execution or log streaming works against the final schema.

W1's old claim/trigger and lifecycle consumers still call legacy credit windows.
Those callers must migrate before their billing helpers can be deleted. W3/W9's
old output clients and local-output persistence also remain. Server-verified
small-output intended use, source-before-run binding and allowance-driven physical
eviction remain open. The two retired generic-storage and old log table catalog
entries belong to W1's catalog cut and were not edited.

## Shared files and friction

Outside pkg/store, production caller changes are pkg/controller/deletion.go,
identity.go, log_reads.go, log_segments.go, output_objects_s3.go, server.go and
tenant_routes.go. Tests are log_segments_internal_test.go,
node_claim_token_log_segments_internal_test.go (deleted), and
log_upload_removed_test.go (added).

Shared docs/generated files are design/rewrite/contracts/c1-data.md,
CHANGELOG.md, docs/migrations/_unreleased.md, pkg/docs/changelog.md,
pkg/docs/mirror/migrations/_unreleased.md, .apidiff/pkg_store.txt and
.apidiff/pkg_controller.txt. Lead-owned handoffs/amendments and T1 inventories
are untouched. No W1 production file changed.

No Sparkwing binary, candidate, pipeline, service, release, subagent or harness
memory write ran. Git config has no local identity/worktree override. The only
scratch root is /tmp/sw-astra-W1b/ and is deleted after the final checks.
Mechanisms added: none. Independent review remains with the lead.

STATUS: CONTINUE
