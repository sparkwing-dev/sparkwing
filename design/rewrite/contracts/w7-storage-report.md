# W7 storage continuation

<!-- design:begin -->

## Merge and implementation

Resolved the working contents of all 49 conflicts between HEAD 826174e1f and
integration 793c59b19. No conflict marker remains. No Git metadata was written,
so all 49 paths still need the lead to stage them before completing the merge.
[The file list](w7-storage-merge-files.txt) records every resolution and edit.

Kept integration's seven production node-host deletions, totaling 353 lines:
attempt.go, closures.go, maskstate.go, memo.go, podnode.go, routes_children.go and
scheduler.go. The deleted orchestrator hosting files remain absent. W3's moved
native-host tests and shared scripted storage fakes remain in their new package.

Adapted those tests to W7's exact ResultRecordKey layout. Output tests retain
foreign-layout and foreign-name refusals. Artifact publication refreshes the key
with each awarded attempt. The native masking test now uses blob.ErrNotFound
instead of the deleted store.ErrOutputNotStored. The complete nodehost suite
passes, including these tests and their existing fuzz seeds.

Removed the unused RunHandle.LogPath field and NewRunHandle's log-path argument.
Its production caller passed an empty string. The CLI caller and handle test use
the reduced signature. Removed the stale LogStore comment promising a polling
fallback. Migration notes explain controller log reads, and the generated docs
mirror matches the source.

The existing real-store wiring remains intact. The materialization integration
passes through the real tenant, controller wire, host and exact output bytes.
Filesystem and S3 archive, receipt, record, collection and log acceptance checks
pass. No new component acceptance cell or replacement fake was added.

The cache command/service, gitcache proxy, package proxy, cache chart, raw S3
output/log backends and run-directory log fallback remain absent. Historical
signed release matrices remain unchanged.

## Production line count

W7 has **-5,134 net production Go lines against main**, versus its -6,630 budget.
The overage is **1,496 lines**. Its current production tree has 10,505 lines,
compared with 15,639 on main. Thirty-nine former production files totaling
**8,050 lines** are absent. Counts use the lead's W7 prefixes and exclude tests,
testdata, acceptance and contract-named files. They include current worktree
contents rather than treating an unresolved index as the completed tree.

This continuation trims three W7 production lines and two shared production
lines, besides retaining integration's 353 lines of node-host deletions. It does
not claim those W3 deletions as new W7 storage work.

The previous budget justification remains for lead review. The estimate did not
size the complete physical signing, publication and verification backends and
tagged upload protocol required by the authority amendments. Moving implemented
scaffolding into types.go and logs.go also made 677 declaration lines count as
production. Both physical backends and exact uploader/current-authority checks
are required. The overage has not been waived, and W7 is not COMPLETE.

## Checks

All Go commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-gocache and GOTMPDIR=/tmp/sw-astra-W7/go. Tests use -p 4.

| Check | Result |
| --- | --- |
| Whole-repository go build ./... after the final edits | Pass |
| Full internal/nodehost suite | Pass, 21.805 s |
| internal/blob/..., artifacts, depcache, depcachekey and pkg/storage/... | Pass |
| TestMaterializedResultCopiesBytesThroughControllerAndHost in pkg/store | Pass, 0.369 s |
| Controller TestCommittedDashboard* and TestDataStorage* | Pass, 3.810 s |
| Focused backend and web log/API tests | Pass |
| TestPublishRunHandleKeepsFirstReceipt | Pass |
| Vet on storage, nodehost, orchestrator, CLI, backend, web, controller and store | Pass |
| TestStorage acceptance | Pass, 93.047 s, 69 passing test events, 424 skips, zero failures |
| commentcheck -base main . | Clean across 2,152 files |
| internal/doccheck, docs mirror check and git diff --check | Pass |

Existing storage acceptance cells remain green for filesystem/S3 records,
manifests, archives, reference-edge isolation, reachable-byte collection, tagged
log commits and reads, sink replay, memo failure behavior, read-only remote
cache and publication. The acceptance event count changed with integration's
harness cleanup. Skipped native targets remain skipped. No whole-repository
suite, pipeline, native OS/placement, PostgreSQL, deployment or release is claimed.

No new test port was required in this continuation. W3's moved native tests were
preserved and adapted. Earlier storage ports remain in the existing acceptance
suite. No security policy changed. The retained binding invariant is that an
output or manifest can be published only under its exact awarded result key.

## Contract changes needed and open integration

**W1/W2 reporting contract:** the remaining legacy OutputRef metadata path in
pkg/store/node_output.go, node_attempt.go and store.go still reaches data_objects
and node_outputs. The receipt builder consumes Node.OutputRef.SHA256. The final
NodeRow exposes OutputRecord selectors, but no result-digest reporting projection.
The lead should assign one tenant-scoped projection that resolves each persisted
selector to its committed digest in the reporting snapshot. It must include
materialized results and refuse a selector for another tenant or result. Then
migrate the receipt reader and remove OutputRef, its report/finish API, the old
validation and node_outputs queries together. Using a raw digest from a caller
or granting data.read to a runs.read-only reader would change the authority
contract. Neither was added here. No foreign frozen contract was changed.

**W2:** authenticated blob.LogTransport signing, verification and proxied reads,
and the SegmentRecorder adapter, remain open. W7's logseg.Data uses those
interfaces and the real tenant segment APIs. DataController.BindData and
NewDataClient are implemented, but the standalone construction still calls only
WithDataObjects. DataController.Access, download charging and authenticated
serving composition must be supplied by W2's declared production composition.

**W3/W8:** connect fresh per-attempt and owner log/storage bindings to the native
host. W7 supplies the byte/record and tagged-log implementations. This report
claims no native placement evidence from contract fixtures.

**W1 policy/maintenance:** operator log-retention changes must enter sealed
IntakePolicyFacts. Expired-upload physical staging cleanup needs a fenced
selector from the store. Existing lease and retention contracts remain intact.

A1-29/30/31/33 remain implemented. A1-24/32/34/35/36/37 consumers keep their real
store or declared foreign-interface wiring. A1-43's historical release assets
remain intact. A1-50 composition and A1-53 native evidence remain with their
assigned owners. The 15:40 materialization closure no longer blocks byte cuts.

## Shared edits and friction

Shared continuation edits are cmd/sparkwing/run_host.go,
internal/orchestrator/run_handle.go and its test, docs/migrations/_unreleased.md
and its generated mirror. Conflict resolutions also touch internal/nodehost,
internal/web/server_test.go and the migration documents. The file list records
all paths. Lead-owned handoffs and amendments were not edited.

The prompt named three conflicts, but the actual index contained 49. All working
contents are resolved, and every integration-deleted conflict path is absent.
The no-Git-write rule leaves staging and merge completion with the lead. The
legacy result-digest projection and the unwaived budget overage remain open.
W7 scratch is removed at exit. The shared build cache is retained.

<!-- design:end -->

STATUS: CONTINUE
