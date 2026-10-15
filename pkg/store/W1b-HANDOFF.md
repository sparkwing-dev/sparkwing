# W1b handoff

Continues e7964cd05 and W1b-session1792070578.out. W1b remains incomplete.
The lead owns staging, the merge commit, independent review and pipelines.

## Merge resolution

Resolved both migration-guide conflict bodies by preserving both branches'
entries. No contract-shape conflict was present. The two paths remain unmerged
in Git's index because the brief forbids Git writes. The lead must stage
`docs/migrations/_unreleased.md` and its `pkg/docs/mirror/` copy and finish
the integration merge. No Git write ran.

## Implemented and deleted

Removed `Store.ReserveUpload`, `CommitUpload`, `UploadForTeam`,
`ReleaseStorage`, `UploadRequest`, their pending-limit errors/constants and
unused small-output constants. Removed the two node output-upload/commit
HTTP writers and their S3 verify/copy implementation. Generic storage
reservation, release and commit-receipt helpers are removed. No upload/accounting
Go writer references `storage_reservations` or `storage_commit_receipts`.
The remaining old node-log signer uses an exact segment counter.

Production Go removes 787 lines and adds 49, including the new metadata
reader, for a net reduction of 738 lines. No schema, price, quota,
grace setting, alias or compatibility wrapper was added.

Added `Tenant.ReadBlob(ctx, authority, digest)` for C2's existing ReadBlob hook.
It checks current tenant authority and read edges inside its transaction and
returns only live, untombstoned metadata. Missing or unreadable content returns
no metadata. Reading creates no receipt or reference edge. This is a C1-local
contract addition, recorded in c1-data.md. C2 must compose the callback.

The output fixture now uses the retained RecordLocalOutput API. Six old direct
upload tests and five raw reservation tests were deleted with that API. No new
archived T1 source-function identity is claimed ported. Final upload, quota,
record-limit and isolation tests pass. Source-before-run binding and the
small-output free-slot exception remain unproved integration requirements.
They are not declared obsolete policy or closed findings.

## Invariants and evidence

- No tenant/run/job selector reaches a retired output-upload or output-commit
  writer. TestRetiredOutputUploadRoutesHaveNoWriter checks 128 generated URLs
  against the registered router without a socket or store.
- Blob metadata is returned only for a live digest readable by the selected
  tenant's current authority. TestBlobMetadataRequiresCurrentTenantAuthorityProperty
  checks 144 generated tenant/principal/repo/attempt/uploader/digest/revocation/
  edge/tombstone combinations. Each also checks a successful own read.
- TestBlobMetadataFollowsReadEdgesWithoutCreatingThem covers unrelated jobs,
  an authorized dependency and edge removal. Foreign bytes with the same digest
  do not make metadata readable. These tests use the real final SQLite schema.

Go commands used GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4, -p 4,
and GOCACHE/GOTMPDIR/TMPDIR under /tmp/sw-astra-W1b/.

- Whole-repository `go build -p 4 ./...` passes.
- Vet passes for pkg/store, pkg/controller and both acceptance trees.
- The final-schema focused selection passes 125 top-level tests / 432 cases.
- The two new metadata tests pass separately, including their properties.
- Billing/storage acceptance passes 287 cases and skips 562 native/platform
  cases. Fourteen store-sqlite/store-data-sqlite cells pass, including ledger
  retention, quota crash/replay, exact uploader receipts, reference-edge
  laundering, tagged logs, memo/depcache scope and remote-cache opt-in.
  The filesystem/S3 boundary cells in that selection also pass.
- Commentcheck against main passes across 2,117 Go files.
- API snapshot, docs mirror, changelog, gofmt and diff-whitespace checks pass.

Reproduce the focused selection with:

`go test -p 4 ./pkg/store ./pkg/controller -run '^(TestRetiredOutputUploadRoutes|TestDataStorage|TestBlobUpload|TestBlobRecords|TestLogPersistence|TestMeteredAccounting|TestMeteredUnit|TestPayment|TestStoreDataMetered)' -count=1`

Run metadata tests with `go test -p 4 ./pkg/store -run '^TestBlobMetadata' -count=1`.
Run acceptance with `go test -p 4 ./tests/acceptance ./internal/acceptance -run '^(TestBilling|TestStorage)' -count=1`.

An initial selector used TestMetered too broadly. It failed 13 legacy store
fixtures and one controller fixture against removed tokens, runs.args_json and
triggers.pipeline. A separate first compile found a stale strings import in the
edited output test. The import was removed. The recorded final selection targets
the final-schema billing implementation. No full-suite, PostgreSQL, provider,
native host/placement, race or pipeline pass is claimed.

## Amendment and handoff audit

- The approved record-mutation owner, output catalog deletions, expired-claim
  helper deletion and earlier Operator credit/card cuts remain done.
- A1-24: transactional blob authority, data selection and binding checks remain
  implemented. ReadBlob now supplies the missing C1 metadata owner. C2 still
  needs production ResolveAuthority, ReadBlob and ChargeDownload composition,
  plus the physical manifest reader on its default record binding.
  NewDataClient is implemented on the merged branch, replacing the prior stub.
- A1-27: real claim/start/renewal/retry/finish/release/reaper settlement joins
  remain implemented and their focused tests pass. Legacy claim/trigger credit
  windows and spend/grace enforcement remain open. W1's unit_billing.go and
  renewal files were not edited.
- A1-28 and lead decision 07:40 item 2: checkout settlement and paid-grant refusal
  remain done. Card setup, off-session collection, refunds, warnings and dispute
  events cannot yet enter SettlePayment through C2's checkout-only proof. The
  precise foreign contract request is below. Existing card-worker methods remain.
- A1-29 through A1-35: records/members/read sets, exact receipts, immutable
  tickets, memo/depcache pointers, tombstones and tagged logs remain implemented.
  The legacy direct-upload persistence and server writer cut is now done.
  Complete host/client, authenticated-log and physical/native integration remains.
- A1-53: PostgreSQL, provider and native acceptance evidence stays with T1/lead.

## Contract changes needed and remaining work

W2 must extend PaymentEvent/VerifiedPaymentEvent beyond CheckoutID/PaymentID
and the three checkout outcomes. Card setup needs verified setup/customer/method
and fingerprint facts. Collection needs persisted attempt/payment correlation.
Refunds need verified refund identity, amount and status. Warnings/disputes need
their provider identity, payment correlation and outcome. Settlement must derive
the tenant and financial amounts from committed rows and retain event idempotency.
W1b will extend SettlePayment against that C2 proof. No foreign contract was edited.
The lead decision says build pending ratification, so missing contract shape is
the blocker, not human approval.

Spend/grace needs durable exhaustion state per billing award and a renewal
result that carries its cancellation in the same committing reply. The final
billing_awards/billable_intervals tables have no such anchor. The old anchor is
node-only. W1's state_renewal.go and unit_renewal.go fill LeaseBeat before
accounting, while accountUnitBillingTx returns only error. W1 must expose the
join and approve the final schema state before W1b can implement one mechanism
for job, host, plan and eval. Do not add a raw terminal-state writer or a caller
clock. Preserve the existing grace and spend policies and holder drain rights.

This is a persistence/server cut, not a completed legacy host cutover.
Client.UploadNodeOutput still targets the removed writers and receives 404.
W3/W9 must remove that client path with its StateStoreOld/FinishNodeWithReason
callers and use final data/record bindings. Raw CommittedObject/NodeOutputObject,
local-output tables/files and old source binding remain until that migration.
The old node-log transport and legacy credit windows also remain. No old host
execution path is claimed operational after this cut.

The free-slot small-output exception still needs a server-verified intended-use
contract before signing. Final tickets cannot safely infer output from a digest.
The deleted old tests do not close this requirement. Allowance-driven physical
eviction remains open. W1's tenant/schema catalogs still name the two retired
generic storage tables. Those entries can retire in W1's catalog cut.

## Shared files and friction

Outside pkg/store, the only production edits are caller removals in
pkg/controller/node_output.go, output_objects_s3.go, server.go and tenant_routes.go.
The new controller regression is output_upload_removed_test.go.

Shared docs/generated files are design/rewrite/contracts/c1-data.md,
CHANGELOG.md, docs/migrations/_unreleased.md, pkg/docs/changelog.md,
pkg/docs/mirror/migrations/_unreleased.md and .apidiff/pkg_store.txt.
W1 production files, lead-owned handoffs/amendments and T1 inventories are untouched.

The lead must stage the resolved paths and review the incomplete client cutover.
No Sparkwing binary, candidate, pipeline, service, network listener, release,
subagent or harness-memory write ran. Local Git config has no identity/worktree
override. The session scratch directory is removed after verification.

STATUS: CONTINUE
