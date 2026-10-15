# W7 storage continuation

<!-- design:begin -->

## Implemented and deleted

Resolved the five conflict contents in browser_reads.go, log_reads.go,
log_segments.go, log_segments_internal_test.go and logseg/reader.go. The three
integration deletions stay deleted. Dashboard logs keep the committed reader,
request actor and stream cancellation. No conflict markers remain. The index
still lists the five paths as unmerged because this worker made no Git metadata
writes. The lead must stage them and finish the merge. HEAD is 8a188d36f and
MERGE_HEAD is 9e151731d.

Removed the store-local output directory, RecordLocalOutput, writeLocalOutput,
GetNodeOutput, ReadOutputFile, output key generators and output URL grants.
Removed ResolveClaimInput and its unused output selectors. Store.FinishNode and
FinishNodeWithReason now accept metadata only. The obsolete scheduler, Attempts,
its layers and local/remote route server had no external production caller.
They are deleted with their output consumers and StateStoreOld. Shared helpers
used by the native host remain. Native hosts retain StorageBinding,
OutputRecords.ReadResult and committed materialized selectors.

Removed the separate backend LiveLogReader, StreamLiveLog, controller adapter and
web fallback. The bound segment reader owns persisted and live-ring delivery.
A backend without a stream returns HTTP 501. There is no alternate log byte path.

Invariant: a revoked reader cannot open or emit a later committed segment.
The new generated real-SQLite property revokes the capability after the first
segment. It reproduced the second segment escaping before the fix. The reader
now rechecks current records and authority before each later segment, opens only
the first object, preserves its partial byte count and reports no completeness.

Moved implemented declarations from blob/contracts.go to types.go and from
log_contract.go to logs.go. ObjectBackend lives in objects.go. Removed nine
forwarding wrappers and exported their existing implementations in place.
Deleted the unused StateStore contract, which had no consumer or test double.
The frozen interface shapes are unchanged.

The cache command/service, gitcache, package proxy, cache chart, raw S3 output
and log stores, and run-directory log fallback remain absent. Historical signed
release matrices remain unchanged.

## Production line counts and budget

W7's net production delta against main is **-5,131 lines** using the lead's area
prefixes, excluding tests, testdata, acceptance and remaining contract files, and
including untracked production files. W7 is **1,499 lines over** its -6,630 budget.
Across the rewrite, 39 former W7 production files totaling 8,050 lines are absent.

This continuation removes **6,041 net production lines** across W7 and the shared
caller cuts, counting declaration moves without a filename exclusion. It deletes
24 former production files totaling 3,521 lines and trims the other dead callers.
Raw W7 production shrank 117 lines. The lead's filename-based scanner instead
shows +648 against this session's HEAD because the former 765-line scaffold set
is no longer excluded. Types and implementations are now counted together.

Budget justification for lead review: the estimate did not size the complete
physical signer/publication/verification implementations and tagged upload
protocol added by the authority amendments. The real filesystem and S3 backends
need separate physical operations. The tagged uploader is 311 lines, batch signer
69 and committed reader 138. types.go and logs.go account for 677 lines of the
now-counted declarations. Removing either physical backend, exact upload identity,
immutable verification or current-authority checks would drop a required C1
behavior. No compatibility adapter, new product limit or acceptance cell was added.
The overage remains for the lead to take to Korey.

## Verification and retained ports

Go checks used GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4, -p 4 and
GOCACHE=/tmp/sw-gocache. Final tests used TMPDIR=/tmp/sw-astra-W7 and
GOTMPDIR=/tmp/sw-astra-W7/go.

- Whole-repository go build ./... passes after the final cuts.
- Full internal/nodehost suite passes in 8.852 seconds.
- Storage unit suites pass for internal/blob/..., internal/artifacts,
  internal/depcache, internal/depcachekey and pkg/storage/.... Blob and logseg
  suites pass again after moving their declarations.
- TestMaterializedResultCopiesBytesThroughControllerAndHost passes in 0.379
  seconds through the real tenant, controller wire, host and exact output bytes.
- Focused controller TestCommittedDashboard* and TestDataStorage* pass in 6.302
  seconds. This includes the new between-segment revocation property.
- Focused web stream/format tests and the backend bound-log refusal test pass.
  The former live-ring fallback test now checks the bound stream and resume cursor.
- Final TestStorage passes in 124.377 seconds with 263 passing test events,
  424 skips and zero failures. Filesystem/S3 records, archives, reference edges,
  memo, remote cache and tagged-log cells remain green. No new cell was added.
- Vet passes for storage, artifacts, depcache, controller, store, nodehost,
  backend, web and acceptance packages. commentcheck is clean across 2,107 files.
  git diff --check, the docs mirror check and internal/doccheck pass.

Removed local roundtrip tests are covered by the real materialization integration.
Missing/corrupt/oversized output remains covered by TestOutputReadRefusals,
TestDownloadRequiresVerifiedEOF and the existing required-output acceptance proof.
Publication refusal remains covered by TestNodeExecutorMissingOutputPublisherFailsLoudly.
Native Owner, Executor and ProcessServices tests remain. Obsolete scheduler/route
fixtures are deleted. Existing metadata-only store/profile test calls were migrated.

No whole-repository test suite, pipeline, native OS/placement, PostgreSQL, release
or independent review is claimed. Legacy raw-lifecycle fixtures still fail at
CreateRun because runs.args_json does not exist. This affects the prior backend
roundtrip fixture and old finish/step fixtures. No legacy schema was recreated.

## Remaining contracts and integration

W1/W2 still own the legacy OutputRef metadata path in pkg/store/node_output.go,
node_attempt.go and store.go. Legacy billed attempt completion and launcher
cancellation reach it, and internal/orchestrator/receipt reads Node.OutputRef.
It still references data_objects and node_outputs. Removing it needs an
authorized result-digest reporting projection or receipt construction from the
committed result records, then removal of the old attempt/finish/read plumbing.
No second byte adapter or synthetic result was added to preserve it.

W2 still supplies authenticated LogTransport signing/verification/read and
SegmentRecorder adapters. W3/W8 supply fresh per-attempt and owner log bindings.
W7 consumes those declared interfaces. DataController.BindData and NewDataClient
are real implementations in this tree, not panic stubs. Production construction
still needs the declared access/authority composition.

Operator log-retention overrides must reach the sealed IntakePolicyFacts through
its policy owner. The unused WithLogRetention map is not a retention selector.
W1's expired-upload cleanup still needs a fenced selector for physical staging
keys. These are existing handoffs. No foreign frozen contract was changed.

A1-29/30/31/33 are implemented with the real store and physical backend seams.
The related A1-24/32/34/35/36/37 consumers remain on the merged tenant, uploader,
record and binding interfaces. A1-43's historical assets are preserved. A1-50
production composition and A1-53 native evidence remain with their assigned areas.
The 15:40 materialization closure is verified and no longer blocks byte-path cuts.

## Shared files and friction

The exact changed/deleted file list, including shared tests, is in
[w7-storage-cut-files.txt](w7-storage-cut-files.txt). Shared production changes
are confined to dead output callers and the log reader/backend/web wiring.
Source docs, migration notes and changelog are updated, and their mirrors match.
Lead-owned amendments and handoffs were not edited.

A final parallel check attempt could not compile because shared-cache archives
for standard-library and project packages disappeared. Neither worker cleaned
the cache. The archives reappeared, the serial build passed, and the final checks
above then passed. The shared cache is retained. No local Git identity or
core.worktree override was set. The lead owns staging, merge completion, API
snapshot regeneration, review and budget ratification. Scratch is removed at exit.

<!-- design:end -->

STATUS: CONTINUE
