# W7 storage handoff

<!-- design:begin -->

## Integration merge and retained storage

All 24 conflicted paths have resolved working-tree contents. No conflict markers
remain. The index still reports those paths because the brief prohibits Git
writes. The lead must stage the resolutions and finish the merge. The incoming
handoffs.md is unchanged from MERGE_HEAD.

The controller's storage report now reads ForTeam followed by
Tenant.StorageStanding. It reports retained events, unique blobs, tagged logs
and live blob-upload holds from W1b's snapshot. It no longer calls the removed
StorageStandingFor or TeamStorage APIs. The merged implementation referenced
the deleted internal/storagequota package. It now uses store.TeamStorageTier.
W7's collection/billing test selects AsOperator for SetCreditSettings.

The merge retains W7's cache-grant, filesystem-output, raw-filesystem-log and
raw artifact plug-in deletions. It also retains W1b's paid-grant retirement,
leased egress pruning, replica accounting, retained inventory and record-mutation
owner. No deleted API or cache credential was restored to satisfy a caller.

Deleted 99 lines of duplicate legacy free-tier tests in storage_standing_test.go.
Their replacements are in free_tier_source_test.go and use final store rows.
Deleted the incoming, unused direct_upload_bucket_test.go helper, another 31
lines. The deleted legacy egress_internal_test.go remains deleted, as selected
by the integration branch. No additional output/log runtime was retired.
The cache service, internal/cache, gitcache proxy, package proxy and cache chart
remain absent. No new acceptance cells or archived-test ports were added.

Source limits documentation describes retained inventory and quota expiry.
regen-all.sh rebuilt derived references, snapshots and embedded docs. It also
removed the stale generated sdk-planguard.md page. The vendored chart matched
its source and was not rebuilt.

## Amendment and handoff audit

| Entries | W7 implementation and remaining integration |
| --- | --- |
| A1-24/29 | Existing byte, manifest and archive paths consume tenant upload/record APIs and authorized member metadata. W2 still resolves authenticated repo/default-ref authority. |
| A1-30/31/32/34 | Remote cache consumes Records. Uploads retain the exact tagged uploader. W2 DataAuthority/BindData and W9 NewDataClient remain panic stubs. W7 continues to consume those interfaces rather than implementing authentication or SQL. |
| A1-33 | Both physical backends, result-manifest verification and leased collection remain implemented and tested. W1b still needs a leased expired-staging selector. Its private quota-expiry helper returns no staging keys. |
| A1-35 | The approved W1b record-mutation redesign is merged. Its full generated authority-expiry regression passes. W1's separate transaction-local materialization copier is still assigned only by tests. |
| A1-36 | W3/W8 must construct fresh StorageBindings for each attempt/generation. W1/W3/W9 still need readable committed result selectors for materialized jobs. |
| A1-37 | SDK cache/key helpers, lint-cache consumer migration and the removed HTTP path remain complete. |
| A1-43/50 | W8/W9 retain engine/profile construction, publication and CLI integration. Historical release assets were not changed. |
| A1-53 | Native authenticated HTTP/api.sock, placement and real-repository QA remain with T1/lead and the integrating areas. |

## Contract changes needed and remaining cuts

W2/W9 must declare the authenticated log binding and segment-commit adapter that
supplies blob.LogTransport and logseg.SegmentRecorder. The physical signer,
SubjectUploader and CommittedReader already consume these interfaces. There is
no controller adapter for the new transactional segment records. Reusing the
old per-node manifest route would preserve the replaced persistence contract.

W1 must bind CopyMaterializedRecords inside its state transaction. Public
CopyMemo before reconciliation would commit result edges separately from the
terminal state. W1/W3/W9 must expose the committed output/manifest record keys
for materialized results, including jobs with no execution attempt. NodeRow
does not expose these selectors. W9/W3 also still need to carry SourceAttempt
through JobTransitionRequest and its store conversion.

W1b must expose fenced expired-ticket staging selection for physical cleanup.
releaseExpiredBlobUploadQuotasTx releases holds but returns no staging keys.
Allowance-driven physical eviction remains open. No foreign contract was
changed and no new product restriction or mechanism was introduced.

Remaining legacy cuts are controller S3 output/log handlers and clients,
orchestrator sinks, store-local output files, run-directory log readers and
listing-based S3 log access. Their callers still use the old paths. They are
unfinished work, not completed deletions.

## Shared files

Production caller adaptation: pkg/controller/storage_standing.go.
Fixture migration: pkg/controller/data_storage_test.go.
Removed fixtures: pkg/controller/storage_standing_test.go and the incoming
pkg/controller/direct_upload_bucket_test.go. server.go loses the orphaned
comment for the removed egressPrunedMonth field.

Merge resolutions cover .apidiff/pkg_store.txt, contracts/c1-data.md,
pkg/controller/server.go, log_segments.go, log_segments_internal_test.go,
storage_standing.go, the deleted data_download.go, direct_source_test.go,
direct_upload_test.go and egress_internal_test.go, and source/embedded auth,
gitcache, limits, migrations, observability, security and self-hosting docs.
Both migration sections survive.

Regeneration also updates source/embedded config-reference.md, sdk-reference.md,
sdk-cleanup.md, sdk-git.md and sdk-inputs.md, deletes sdk-planguard.md, and
updates API references, OpenAPI and API snapshots. These are generator output.
The lead-owned amendments and handoffs files were not edited.

## Verification

Commands used GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4 and -p 4.
GOCACHE, GOTMPDIR and TMPDIR were under /tmp/sw-astra-W7/.

- Whole-repository go build ./... passes.
- All tests pass for internal/blob/..., internal/artifacts, internal/depcache,
  internal/teamblob and pkg/storage/.... Vet also passes for these and controller.
- Controller data-storage, filesystem-startup, retained-inventory, tenant
  standing, egress, event/download accounting, final storage-route and free-tier
  replacement tests pass. The result property still uses real claim/start,
  reservation, receipt, record and terminal-state transactions.
- Store result-record tests, TestBlobRecordsMutationCannotOutliveAuthority,
  standing tests and FuzzStorageStandingTenantInventory seeds pass.
- Existing TestStorage passes in 202.107 seconds with 80 passing test events,
  424 skips and no failures. Real SQLite upload/record/log, manifest, archive
  and physical-backend joins remain covered. Skips are not native evidence.
- regen-all.sh, sync-docs.sh --check, commentcheck -base main and
  git diff --check pass. Commentcheck inspected 1,343 files.

No independent review, full legacy suite, PostgreSQL, native placement, pipeline
or release is claimed. The lead owns those decisions and merge completion.
Friction is the unavailable foreign adapters and missing contract shapes above.
The Git index remains unmerged because this worker cannot stage or commit.
Scratch is deleted after verification.

<!-- design:end -->

STATUS: CONTINUE
