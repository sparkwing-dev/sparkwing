# W7 storage handoff

<!-- design:begin -->

## Merge continuation

Started at `f22027d7a` during the merge of `bw-3095-rewrite`.
All seven conflicted paths are resolved in the working files. C1 retains the
amended contract and both areas' implementation evidence. Both migration copies
retain W7's storage cuts and the incoming W1 changes. The controller snapshot
retains the incoming version policy and removes already deleted storage APIs.
The SDK snapshot matches the merged implementation.

The incoming versions of `data_download_claim_provenance_test.go` and
`direct_upload_test.go` exercise deleted grant and upload APIs. Their existing
W7 deletions are preserved. These are 813 incoming test lines, not a new deletion
from W7's HEAD. No Git write ran. The index still reports seven unmerged paths.
The lead must stage the resolutions and finish the merge.

## Completed work

The merged W1 publication checker requires an imported attempt's persisted
finish time. W7's physical manifest fixtures still inserted pending jobs with
unfinished attempts. Both filesystem and S3 cells failed with `holder_rights`.
The fixture now imports terminal jobs and finishes each attempt before upload.

Invariant: a terminal node row cannot authorize publication while its exact
imported attempt is unfinished, and another job's receipt cannot authorize a
manifest member. Twelve generated cases per backend first refuse the unfinished
attempt and check that no upload reservation remains. They then finish the
attempt and retain the existing substituted-metadata, borrowed-receipt,
independent-upload and reopen assertions. No production authority check changed.

The retained build-trust controller test now obtains a tenant before calling
`TrustLocalBuilds`. This repairs compilation after W1 removed the raw Store API.
Its execution still fails in the older app fixture's setup because the final
schema has no `tokens` table. It does not reach the build-trust handler.

Regenerated W7 snapshot entries that the integration merge restored after their
runtime APIs were deleted: filesystem append logs, log key helpers, raw artifact
factory/measurement types, storage counting and the daemon artifact-store error.
Every generated API snapshot matches its counterpart in the merged tree.
The preexisting `.apidiff/pkg_runner.txt` has no generated counterpart and
remains W8/lead cleanup, so the whole-directory snapshot check still fails.

Deleted **0 new runtime lines**. No new parked-test port or native acceptance cell
is claimed. Earlier cache-service, gitcache, package-proxy and filesystem
append-log cuts remain deleted. No compatibility transport or runtime mechanism
was added.

## Acceptance and verification

`TestStorage` passes with **79 passing test events and 425 skips**, including 44
passing implementation cells. Both physical manifest-record cells pass after
the fixture migration. The skips remain 424 native skeleton cells and the known
W1 physical log-slot mismatch. The new negative cases are in the existing
manifest-record cells, rather than a substitute native driver.

Go commands use `GOWORK=off GOFLAGS=-buildvcs=false GOMAXPROCS=4`,
`GOCACHE=/tmp/sw-astra-W7/cache`, `GOTMPDIR=/tmp/sw-astra-W7/gotmp` and
`TMPDIR=/tmp/sw-astra-W7/tmp`. Tests use at most `-p 4`.

Passed:

- `go build -p 4 ./...` for the whole repository.
- `go test -p 4 ./... -run '^$'`: all 173 package results compile.
- `go test -p 4 ./internal/blob/... ./internal/artifacts ./internal/depcache ./pkg/storage/...`.
- `go test -json -p 4 ./tests/acceptance -run '^TestStorage$' -count=1`.
- Both filesystem/S3 manifest-record cells with `-count=1` and `-race`.
- `go vet -p 4` for those storage packages and `./tests/acceptance`.
- `TestPkgDocsContentMatchesDocsRoot` and `TestEmbeddedChangelogMatchesRoot`.
- `go run ./internal/commentcheck -base main .`: clean across 1,236 files.
- `go run ./internal/sleepcheck -base HEAD .`.
- Formatting, all generated snapshot-content comparisons and diff whitespace checks.

Reproduced rather than waived:

- A temporary Go overlay removing only the physical-log skip fails with
  `persist physical segment 1: log segment object belongs to another signing slot`.
  The repository skip remains. W1's final key/index implementation is still open.
- `TestTeamOwnerCanChooseWhetherCloudTrustsLocalBinaries` compiles, then fails
  in `newAppFixture` with `no such table: tokens`. W2 owns that fixture cutover.

## Amendment and handoff ledger

Only `amendments-1.md` exists on this branch. All rows naming W7 and all W7
handoffs were checked against the merged tree. Existing completed work was not
reimplemented.

| Entry | W7 status | Real integration left |
| --- | --- | --- |
| A1-24 | Verified bytes and manifest adapter implemented. | W2 installs W1's transactional checker and manifest reader. |
| A1-29 | Restricted reads retain authorized member metadata. | W1 resolves repo-reader tenant/default-ref selectors. |
| A1-30 | Logical remote-cache transport and ref-free selectors implemented. | W2/W9 construct authenticated profile transports. |
| A1-31 | Tagged job, owner and publication identities checked. Terminal imported-attempt fixture now matches W1. | W2 derives the binding from current authentication. |
| A1-32 | Upload, receipt/replay and archive/store proofs pass. | W2 binds authenticated reservation, settlement and receipt replay. |
| A1-33 | Physical publication, collection, manifest reads and measurement implemented. | W2/maintenance bind collection and log expiry. |
| A1-34 | Signed filesystem/S3 components implemented. | DataAuthority, BindData and NewDataClient still panic as unimplemented. |
| A1-35 | Output, memo, depcache, staging, collection and tagged-log components implemented. | W1 aligns C1 result/memo keys and physical log slots. |
| A1-36 | Artifact, memo and depcache layers use common callbacks and refresh after execution. | W3/W8 construct fresh StorageBinding values per attempt/generation. |
| A1-37 | Contained cache keys and go-modules resolver implemented. Removed lint transport stays deleted. | W3/W8 finish host construction. |
| A1-43 | Cache-service cut retains historical release assets and two-image release fixtures. | W8 finishes version-aware asset functions. |
| A1-50 | Read-only cache and publication byte components implemented. | W9 wires explicit profiles, publication and engine clients. |
| A1-53 | Component, physical/store and sink/backend proofs pass. | T1/lead owns native drivers, platforms and real-repository QA. |

## Remaining implementation and deletion

W1's `ResultBlobKey` still uses `Name=result` and a run/attempt/generation
scope key. C1 requires the run ID as name and an empty scope key. Memo
persistence still uses `output`/`manifest`, while W7 uses `MemoRecordName=result`.
Its physical log key differs from `blob.LogObjectKey` and its segment records
start at zero, while signed segments start at one. These need implementation
alignment with the existing contract, not a new cross-area contract change.

W2's `DataController.DataAuthority`, `BindData` and `client.NewDataClient` remain
stubs. Authentication, per-operation reauthorization, exact egress, log routes
and final upload bindings remain W2's work. W3/W8 must construct live bindings.
W9 must construct explicit remote-cache and publication profiles. Maintenance
must bind physical collection and filesystem log expiry.

After those bindings, delete controller `node_output.go`, `output_objects_s3.go`,
`output_download_s3.go`, `log_segments.go`, client `output.go`/`logsegments.go`,
orchestrator `segment_logs.go`, their tests and the raw artifact/log backends.
They still have live callers. W7 components already use the final interfaces
with contract fakes at the unimplemented boundaries. Native coverage and these
remaining legacy deletions prevent declaring W7 complete.

## Shared files and friction

Shared edits are the seven conflicted paths, `pkg/controller/build_trust_test.go`,
`internal/acceptance/blob_manifests.go`, the W7 snapshot corrections in
`.apidiff/pkg_store.txt` and `.apidiff/pkg_wingwire.txt`, `handoffs.md` and this
report. W7 snapshot corrections also touch `pkg_storage_fs.txt`,
`pkg_storage_logseg.txt` and `pkg_storage_storeurl.txt`.
No caller in another runtime package changed.

The supplied Xwing worktree was reused. No Sparkwing binary, pipeline, deployment
or external service ran. Git staging/commit and native QA remain with the lead.
Scratch is deleted before the final report. No new product question, contract
change, dependency, setting, limit or environment variable is requested.

<!-- design:end -->

STATUS: CONTINUE
