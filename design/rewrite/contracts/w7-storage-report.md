# W7 storage handoff

<!-- design:begin -->

## Latest continuation

Started at `a06e2a6a7` with a clean worktree and no merge in progress.
Earlier W7 work remains committed. All W7 entries in `amendments-1.md` and
`handoffs.md` were checked. No additional amendments file exists on this branch.

Invariant: a local ref cannot select remote bytes, failed verification preserves
the destination, and a local save cannot publish into the shared cache.

New signed filesystem and S3 archive acceptance checks reproduced
`empty or invalid record namespace component`. `NewRemoteReader` leaves the
ref unset because the server selects the verified default branch. The remote
client incorrectly validated that read selector as a write namespace. Memo and
depcache now validate their logical fields and keep the ref absent in the
verified-default-only request. Returned record evidence is still checked before
bytes become readable. The `RemoteCache` comment clarifies this existing
contract. No type or method signature changed.

`FuzzRemoteCacheSelectsServerDefaultRef` passes 1,000 generated cases. Empty,
manual, path-like and control-character local refs cannot affect the server's
selector. Memo and depcache both resolve the verified default record.

Two new acceptance cells each pass 128 generated cases through signed upload,
immutable physical publication, default-only lookup, verified download and
archive extraction. They refuse forged repository/ref/key records, preserve
existing files after equal-length corruption or missing objects, and leave
empty destinations absent on failure. Failed work saves no local archive.
Successful cold work saves locally and restores with the remote object still
missing. Read bindings have no upload or record-write implementation.

The filesystem proof uses real files and signed HTTP handlers. S3 uses the real
signer and SDK with a fake object service. Record and authentication callbacks
are contract fakes. These proofs close the W7 adapter gap without waiting for
W2's authenticated transport. They do not claim native controller or host
integration.

## Deleted

Removed `LogStore.Append`, `LogStore.DeleteRun`, `NodeLogs.Append`,
`NodeLogs.DeleteRun`, the matching `segmentLogs` methods and the unused
`storage.SafeLogIDs`. These four runtime files delete **41 lines** and add two
replacement GoDoc lines. The controller's internal run-deletion path still
owns physical log cleanup. Production callers compile. No compatibility method remains.

The storage API snapshot is regenerated, including earlier W7 manifest-adapter
and usage changes that were missing from it. The shared client snapshot removes
only the two deleted methods. Its unrelated stale `ResetNodeForAutoRetry` entry
remains for W3/W9/lead. Source docs, the migration note and embedded copies are
updated. Earlier filesystem log, cache-service and raw-factory cuts are not
repeated or recounted as new deletions.

## Acceptance and ports

New passing cells:

- `TestStorage/storage-local-remote-cache-opt-in/filesystem-archives`
- `TestStorage/storage-local-remote-cache-opt-in/s3-archives`

The full `TestStorage` run passes with **74 passing test events** and **425
skips**. The skips remain 424 native skeleton cells and the known W1 physical
log-slot mismatch. Both new archive cells also pass with the race detector.
No new parked source function is claimed ported. Earlier output, artifact,
memo, retention, receipt, archive and segmented-log ports remain passing.

## Verification

Go commands used `GOWORK=off GOFLAGS=-buildvcs=false GOMAXPROCS=4`,
`GOCACHE=/tmp/sw-astra-W7/cache`, `GOTMPDIR=/tmp/sw-astra-W7/gotmp` and
`TMPDIR=/tmp/sw-astra-W7/tmp`. All tests used at most `-p 4`.

Passed:

- `go build -p 4 ./...` for the whole repository.
- `go test -p 4 ./internal/blob/... ./internal/artifacts ./internal/depcache ./pkg/storage/...`.
- `go test -json -p 4 ./tests/acceptance -run '^TestStorage$' -count=1`.
- `go test -race -p 4 ./tests/acceptance -run '^TestStorage/storage-local-remote-cache-opt-in/(filesystem|s3)-archives$' -count=1`.
- `go test -p 4 ./internal/blob -run '^$' -fuzz '^FuzzRemoteCacheSelectsServerDefaultRef$' -fuzztime=1000x`.
- Vet for those storage packages, acceptance, `pkg/controller` and `pkg/controller/client`.
- `TestPkgDocsContentMatchesDocsRoot` and `TestEmbeddedChangelogMatchesRoot`.
- `go run ./internal/commentcheck -base main .`, clean across 1114 files.
- `go run ./internal/sleepcheck -base HEAD .`.
- Formatting and diff whitespace checks.

The first archive run failed on both backends before the ref-selector fix.
After the fix, both cells passed, followed by the full storage suite and race
run. A Go overlay restoring only `remote.go` from HEAD makes the new fuzz seeds
fail on empty and control-character refs. One combined regex ran no acceptance
tests and is not counted as evidence.
The full acceptance run above exercised the final refactored tests.

Bootstrap discovery ran the installed `sparkwing info --for-agent`. No candidate
runtime, pipeline, Git write, release, native platform, PostgreSQL or external
service ran. The lead owns native integration and independent
review. Scratch is deleted before the final response.

## Amendment and handoff ledger

| Entry | W7 status | Integration left |
| --- | --- | --- |
| A1-24 | Physical byte and verified manifest adapters implemented. | W2 installs W1's transactional checker and manifest reader. |
| A1-29 | Restricted reads retain authorized member metadata. | W1 resolves repo-reader tenant/default-ref selectors. |
| A1-30 | Logical remote-cache transport and ref-free read selectors implemented. | W2/W9 construct authenticated profile transports. |
| A1-31 | Tagged job, owner and publication upload identities checked. | W2 derives the binding from current authentication. |
| A1-32 | Filesystem/S3 upload, receipt and replay proofs implemented. Archive uploads now also reach real reservation/settlement transactions. | W2 binds the authenticated lifecycle and receipt replay. |
| A1-33 | Physical owner, collection, manifest reads and measurements implemented. | W2/maintenance bind collection and log expiry. |
| A1-34 | Signed filesystem/S3 components implemented. | DataAuthority, BindData and NewDataClient remain stubs. |
| A1-35 | Output, memo, depcache, staging, collection and tagged-log components implemented. New archive/store proofs pass. | W1 aligns C1 result/memo keys and physical log slots. |
| A1-36 | Artifact, memo and depcache layers use common callbacks and refresh after execution. | W3/W8 construct fresh StorageBinding values per attempt/generation. |
| A1-37 | Shared contained keys and go-modules resolver implemented. Removed lint transport stays deleted. | W3/W8 finish production host construction. |
| A1-43 | Cache-service cut retains historical release assets. Current image fixtures now match the two-image release scripts. | W8 finishes version-aware asset functions. |
| A1-50 | Read-only cache and publication byte components implemented. | W9 wires explicit profile opt-in, publication and engine clients. |
| A1-53 | Two sink/backend acceptance properties and their race run pass. | T1/lead owns native drivers, platforms and real-repository QA. |

## Real integration and remaining deletion

W1's `ResultBlobKey` still uses `Name=result` and a run/attempt/generation
scope key. C1 requires the run ID as name and an empty scope key. W1 memo
persistence still uses `output`/`manifest` instead of `MemoRecordName=result`.
Its physical log key differs from `blob.LogObjectKey`, and its records start
at zero while signed segment indexes start at one. These implementations were
rechecked. Earlier executed failures and the explicitly skipped SQLite log
proof remain recorded in `handoffs.md`.

W2's `DataController.DataAuthority`, `BindData` and `client.NewDataClient` still
panic as unimplemented. W2 must bind authenticated records, signing, upload
reservation/settlement/replay, log transport and exact egress. W1 must resolve
repo-bound readers' tenant/default ref. W3/W8 must construct fresh storage and
log bindings per attempt/generation. W9 must install explicit remote-cache and
publication profiles. Maintenance must bind physical collection and filesystem
log expiry.

After those bindings, delete controller `node_output.go`, `output_objects_s3.go`,
`output_download_s3.go`, `log_segments.go`, client `output.go`/`logsegments.go`,
orchestrator `segment_logs.go`, their tests and the raw artifact/log backends.
Those files still have live callers. No replacement compatibility transport was
added. W7 remains incomplete while these real integration and deletion points
are open.

## Shared files and friction

Shared runtime caller edits are only `pkg/controller/log_reads.go` and
`pkg/controller/client/logs.go`. Both remove the unused methods.

Shared acceptance edits are `tests/acceptance/storage_test.go` and new
`tests/acceptance/storage_remote_archives_test.go`. Shared generated API files
are `.apidiff/pkg_storage.txt` and `.apidiff/pkg_controller_client.txt`.

Shared docs are `CHANGELOG.md`, `VERSIONING.md`, `pkg/docs/changelog.md`,
`docs/migrations/_unreleased.md`, its embedded mirror, `handoffs.md` and this
report. Other edits belong to W7's blob and storage packages.

No cross-area contract change, product question, customer limit, setting,
dependency, environment variable or new runtime mechanism is requested.
Friction remains the W1 layouts and W2's unimplemented authenticated adapters.
The full API snapshot also retains unrelated drift outside W7's owned surface.
The supplied Xwing environment was reused. The lead retains committing,
landing and the remaining integration work.

<!-- design:end -->

STATUS: CONTINUE
