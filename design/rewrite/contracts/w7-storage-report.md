# W7 storage handoff

<!-- design:begin -->

## Implemented

Continued from `0c89da941`. W7 uses W1's persisted result and memo layouts.
`blob.ResultRecordKey` is the single output/manifest key builder. Names are
`result`, and the scope key contains run/attempt/generation. Output, artifact,
node-host memo, admission, memo copy and terminal result attachment use it.
`MemoRecordName` is `output`, matching persisted memo lookup. W1's manifest-only
memo keeps its separate `manifest` name.

This reconciles W7's own C1 paragraphs with W1's real layout. It changes no
foreign interface shape or schema. The old `store.ResultBlobKey` helper is
removed with every caller migrated. No alias remains.

`Tenant.RecordSegments` now consumes `blob.LogObjectKey`. Stored segments start
at one. The duplicate store key builder and zero-based fixture layouts are gone.
The filesystem physical-log cell is unskipped. The existing S3 component cell
now uses real tenant segment records and is named `s3-records`.

Deleted **23 legacy helper definition lines**, plus their unused imports and
snapshot entries. The cache service, gitcache, package proxy and cache chart
remain deleted from earlier sessions. No new component-only cell or archived
T1 test port was added. Old output/log transport deletion remains incomplete.

## Invariants and evidence

Invariant: each result key identifies its exact tenant/run/job/attempt/generation,
and caller-selected run/job/scope substitutions cannot select a different result.
A seeded property generates 96 cases for both output and manifest keys.

The existing filesystem and S3 archive cells now join OutputData, artifact
capture, memo commit/materialization and staging to real Tenant.BlobRecords,
real upload reservations/receipts and verified physical manifests. They reopen
SQLite before required reads, including explicit empty output. The existing
corrupt/missing archive, scope refusal and cold-replacement checks remain.

Physical-log properties join signed transfers to Tenant.RecordSegments and
ReadLogSegments. They reopen SQLite, verify offset reads and retained bytes,
replay final manifests and refuse a revoked reader. Lost PUT responses require
stored-byte verification. Filesystem bytes are real. S3 uses the real signer and
SDK with a fake object service. Authority/award intake and the unfinished W2
binding remain fixtures. Native placement and authenticated route coverage are
not claimed.

## Verification

Recreate cache, gotmp and tmp directories beneath `/tmp/sw-astra-W7`.
Go commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-astra-W7/cache, GOTMPDIR=/tmp/sw-astra-W7/gotmp,
TMPDIR=/tmp/sw-astra-W7/tmp and `go -C` on the supplied worktree. Tests use -p 4.

Passed:

- Whole-repository `go build -p 4 ./...`.
- `go test -p 4 ./... -run '^$'`, all 173 package compilation results.
- Full tests for internal/blob/..., internal/artifacts, internal/depcache and pkg/storage/....
- TestStorage, **81 passing events and 424 native/platform skips**.
- Scoped store selection, **195 passing events**, with the known red mutation-fence test excluded.
- Node-host memo/artifact/cache selection, **26 passing events**.
- Scoped vet over blob, artifacts, depcache, storage, store and acceptance packages.
- Embedded documentation/changelog drift checks, formatting and diff whitespace.
- Required commentcheck against main, clean across 1,236 files. Sleepcheck is clean.
- All generated API snapshot contents match. The inherited orphan pkg_runner snapshot remains.

The store selection is `^(TestLogPersistence|TestLogSegments|TestRecordSegments|TestBlobRecords|TestBlobResult|TestBlobCopied)`
with `-skip '^TestBlobRecordsMutationCannotOutliveAuthority$'`.
The host selection is `^(TestMemo|TestArtifacts|TestCaches)`.

Reproduced separately: TestBlobRecordsMutationCannotOutliveAuthority fails all
six commit/copy by lease/leaf/ancestor cases. A namespace callback can cross
expiry before persistence. It leaves one output and exposes the result.
DESIGN: record mutation authorization. W1b already recorded this as the second
failure of the mechanism. Its transaction-local publication redesign belongs
to the lead under the brief's two-strikes rule. No repair or waiver is claimed.

## Amendment and handoff audit

| Entry | W7 result | Real integration left |
| --- | --- | --- |
| A1-24 | Verified manifests use real tenant records. | W2 installs transactional authority bindings. |
| A1-29 | Members retain verified path/size/mode metadata. | Tenant/default-ref selection for repo-reader exact lookups. |
| A1-30 | Remote logical cache reads use the persisted memo name. | W2/W9 authenticated profile transport and selector resolution. |
| A1-31/32 | Exact uploader and upload persistence proofs remain passing. | W2 authenticated reservation, settlement and replay. |
| A1-33 | Physical publication, verification, deletion and measurement remain implemented. | Maintenance construction for collection and expiry. |
| A1-34 | W7 consumes declared signer and record interfaces. | DataAuthority, BindData and NewDataClient remain stubs. |
| A1-35 | Output/memo/artifact/log formats now join real store APIs. | Known W1 mutation-fence redesign and final transport cut. |
| A1-36/37 | Artifact/memo/cache layers consume the shared layout. | Fresh host StorageBinding construction. |
| A1-43/50 | Prior cache-service cut and profile components remain. | Engine assets, CLI profiles and separate publication failure. |
| A1-53 | Real SQLite/physical proofs pass. | Native drivers/platforms and real-repository QA. |

The older physical-log and result/memo-layout handoffs are closed by this work.
Other completed handoffs were checked without reimplementation.

## Remaining hard cut and contracts

Delete controller node_output.go, output_objects_s3.go, output_download_s3.go,
log_segments.go, client output.go/logsegments.go, orchestrator segment_logs.go,
their legacy tests and remaining raw artifact/log backends after migrating their
live callers. These files are not deleted in this continuation.

W2/W3/W8/W9 need to connect authenticated data bindings and final host inputs.
The declared LogTransport and logseg.SegmentRecorder still need bound controller
and client construction. Controllerwire.Controller declares no segment-commit
operation. No new foreign contract shape was invented here. The lead must assign
that missing adapter seam so W7 can retire the old log uploader without another
protocol. PolicyRouter also remains a stub. Physical collection and filesystem
expiry need their maintenance construction. No product question is raised.

## Shared files

Runtime caller migrations outside W7 are internal/nodehost/memo.go and
pkg/store/blob_record_runtime.go, blob_lookup.go, blob_result_records.go and
result_logs.go. Store fixture migrations are blob_records_runtime_test.go,
log_persistence_test.go, log_publication_test.go and result_runtime_test.go.
The store documentation updates its removed key helper name.

Shared acceptance edits are internal/acceptance/blob_depcache.go,
blob_manifests.go, store_logs.go, store_abandoned_logs.go and store_published_logs.go,
plus tests/acceptance/storage_test.go, storage_logs_test.go, storage_s3_test.go,
storage_materialization_test.go and storage_publication_test.go. Contract/report
edits are c1-data.md, handoffs.md and this report. The store API snapshot removes
the two deleted helper exports. W7 also updates its blob/artifact tests and README.

Friction is the unfinished authenticated adapters, older host callers and the
known W1 authorization failure. The supplied Xwing worktree was reused. No Git
write, Sparkwing executable, pipeline, service, deployment or release ran.
Git identity is unchanged. Scratch is deleted after final inspection.

<!-- design:end -->

STATUS: CONTINUE
