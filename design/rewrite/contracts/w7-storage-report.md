# W7 storage handoff

<!-- design:begin -->

## This continuation

All 16 conflict paths have resolved working-tree contents or are deleted. The
lead-owned amendments and handoffs match MERGE_HEAD unchanged. The Git index
still lists conflicts because this worker cannot stage or commit. The lead must
stage the resolutions and finish the merge.

The merge preserves W3's host, entrypoint, lease and claim-path cuts without
restoring cache grants, raw artifact stores or the removed memo-source API.
The retained pod-log loss test uses its existing uploader interface because
W3 deleted its old launched-controller fixture.

The common host output publisher now calls blob.OutputData.Put. Its artifact
publisher calls artifacts.Capture. Both require the final ResultRecordKey for
the exact run, job, attempt and generation. The artifact publisher's separate
files-format manifest was incompatible with the store's entries-format reader.
The existing filesystem archive cell reproduced blob integrity mismatch through
real tenant record commit before the fix. Both backends now exercise the shared
publisher through real upload reservation, byte verification, receipt settlement
and record publication. Empty output retains its direct OutputData proof.

Invariant: a host manifest uses the store's format and describes exactly its
authorized members. Existing generated archive cells now drive the host
publishers. The artifact binding fuzz property also varies result name and
attempt-layout selectors. Altered selectors must fail before upload. The
existing output cell refuses the same forged selectors. No acceptance cell was
added and no native receipt was claimed.

The common executor and SDK now hash dependency keys through depcachekey.Bytes.
The generated directory-key property compares the host directly to the public
SDK helper. The executor restores through depcacheblob.RestoreArchive, sharing
verified EOF, successful close and staged extraction with both archive backends.
The dependency writer uses the incoming shared tarsafe writer. The incoming
cache-directory fixtures now use the final go-modules spelling.

Shared artifact and output publication retain a cancellation cause, including
cancellation during a successful backend call. The existing artifact cancellation
case and the output commit-cancellation regression pass.

## Deletion

Consolidation removes 228 net production lines across these four files:

| File | Before | After |
| --- | ---: | ---: |
| internal/nodehost/artifact_publish.go | 198 | 63 |
| internal/nodehost/depcache_archive.go | 97 | 62 |
| internal/nodehost/output_records.go | 191 | 190 |
| internal/depcache/archive.go | 106 | 49 |

The first three baselines are the incoming merged files. The fourth is W7 HEAD.
The deleted duplicate capture, manifest writer, archive writer and extractor
have shared production replacements.

W3's conflicting claim_exec.go, claim_node.go, run_node_verb.go and
run_node_claim_test.go remain deleted. Those files contained 478 production and
225 test lines. The prior operator_ssh_source_test.go deletion remains deleted.
W3's launched-node test deletion is retained, with the log-loss assertion kept.

cmd/sparkwing-cache, internal/cache, gitcache, the package proxy and cache chart
remain absent. Remaining cuts are the legacy controller S3 output/log handlers
and clients, orchestrator log sinks, store-local output files, run-directory log
readers and listing-based S3 log access. These are unfinished deletions.

## Amendment and handoff audit

| Entries | Status and remaining integration |
| --- | --- |
| A1-24/29 | Byte, manifest and archive paths consume tenant APIs and authorized member metadata. W2 still resolves authenticated repository and default-ref facts. |
| A1-30/31/32/34 | Record transport and exact uploader/ticket contracts remain in use. W2 DataAuthority/BindData and W9 NewDataClient remain panic stubs. No authentication implementation was copied into W7. |
| A1-33 | Both physical backends and leased collection remain implemented. W1b still needs a fenced expired-ticket staging selector. |
| A1-35 | The record-mutation redesign remains merged. Host output and artifact publication now share W7's verified writers and final layouts. W1's transactional materialization copier remains assigned only by tests. |
| A1-36 | The common host consumes StorageBinding for publication and restore. W3/W8 must construct a fresh authenticated binding for every attempt and generation. Materialized results still need committed output/manifest selectors. |
| A1-37 | SDK/local/host keys now share the same captured-byte hash. The prior lint-cache consumer and removed HTTP-path cuts remain complete. |
| A1-43/50 | Historical signed release assets remain unchanged. W8/W9 still own profile construction, publication and CLI integration. |
| A1-53 | Authenticated HTTP/api.sock, placement and real-repository QA remain with T1/lead and integrating areas. Component evidence does not close native cells. |

No new W7 handoff row appeared. The prior record-mutation design entry remains
owned by W1b and is consumed through its merged store APIs.

## Contract changes needed and open integration

W2/W9 must provide the authenticated tagged-log binding and transactional
segment-commit transport for blob.LogTransport and logseg.SegmentRecorder.
Their absence does not justify retaining the old manifest route as an adapter.
The W7 physical signer, SubjectUploader and CommittedReader already consume
those contracts. Production route registration remains outside this worker.

W1 must bind CopyMaterializedRecords inside its owning state transaction.
Public CopyMemo before reconciliation would commit records separately from the
terminal state. W1/W3/W9 must expose committed output/manifest selectors for
materialized results without inventing an execution attempt. W9/W3 still need
SourceAttempt on the transition wire and its store conversion.

W1b must expose fenced expired-upload staging selection for physical cleanup.
Its quota-expiry helper releases reservations without returning staging keys.
Allowance-driven physical eviction remains open.

No foreign contract shape changed. The two added W7-local helper APIs are
Bytes in depcachekey and RestoreArchive in blob/depcache. They extract
existing behavior into the shared owners. No service, setting, compatibility
alias or customer limit was added.

## Shared files

Production caller migration touches internal/nodehost/artifact_publish.go,
output_records.go, depcache_archive.go and depcache_records.go. Fixture updates
are internal/nodehost/cache_directory_test.go, cache_directory_unix_test.go,
internal/acceptance/blob_depcache.go, host_artifact_publish.go, host_depcache.go,
host_output.go and tests/acceptance/artifact_publish_test.go.

Merge resolutions cover internal/cluster/operator_ssh_source_test.go,
internal/depcache/archive.go and depcache.go, internal/nodehost/memo.go,
memo_artifacts_test.go, podnode_test.go and scheduler.go,
internal/orchestrator/claim_exec.go, claim_node.go, hosted_run.go,
pod_node_host.go, pod_node_host_test.go, run_node_claim_test.go,
run_node_verb.go and trigger_host.go, plus tests/acceptance/skeleton_test.go.
The incoming acceptance registrations are retained. None was newly added here.

The W7 implementation and source documentation changes are
internal/artifacts/capture.go, internal/blob/output.go and output_test.go,
internal/blob/depcache/data.go and remote.go, internal/depcachekey/key.go and
internal/blob/README.md. No archived test-inventory row was newly closed.

## Verification

All Go commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4 and -p 4.
GOCACHE, GOTMPDIR and TMPDIR are under /tmp/sw-astra-W7/.

- Whole-repository go build ./... passes on the final tree.
- Full tests pass for internal/blob/..., internal/artifacts, internal/depcache,
  internal/depcachekey and pkg/storage/.... The blob and artifact suites also
  pass after the cancellation-cause change.
- Vet passes for those packages and internal/nodehost, internal/orchestrator,
  internal/acceptance and pkg/controller.
- TestStorage passes in 158.562 seconds with 274 passing test events, 424 skips
  and no failures. This includes filesystem-archives and s3-archives in
  storage-memo-and-depcache-scope, plus the merged host artifact/depcache cells.
  Skips remain missing native evidence.
- The lifecycle-result-commit-atomicity output cells and artifact/depcache fuzz
  seeds pass. Artifact result-name/layout seeds are included.
- Host/SDK key parity, captured cache-directory resolution, pod-log loss and
  memo-materialization tests pass. The controller's real claim/start/upload/
  record/result property TestDataStorageAttachesVerifiedResultRecords passes.
- commentcheck -base main is clean across 1,512 files. git diff --check and
  bin/sync-docs.sh --check pass. Lead-owned contract files still match the
  integration branch. No local Git identity or worktree override was introduced.

No independent review, full legacy suite, PostgreSQL, native placement, pipeline
or release is claimed. The lead owns merge completion and the broader gates.
Friction is the missing foreign bindings and contract shapes listed above.
Scratch is deleted after verification.

<!-- design:end -->

STATUS: CONTINUE
