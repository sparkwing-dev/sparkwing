# W7 storage handoff

<!-- design:begin -->

## Implemented and deleted

Continued from 5f98e6445. The earlier cache-service, Git-cache, package-proxy,
cache-chart, real-store byte/record, receipt, manifest, memo, tagged-log and
physical cleanup work remains. No acceptance cell was added or unskipped.

Removed the remaining sharded filesystem object reader, listing, path codec and
raw fixture storage. Filesystem deployment measurement now uses
`fs.NewUsageReporter`. `storeurl.OpenMeasurementStore` constructs it and callers
receive only `storage.UsageReporter`. It counts physical regular files without
shard decoding or the removed writer's lock and temporary-file exclusions.

Removed `storage.ArtifactStore`, `ErrListNotSupported`,
`pkg/storage/conformance`, its obsolete S3 invocation and unused
`s3.ArtifactStore.Delete`. Raw S3 log Get/List/PutIfAbsent and bucket measurement
still have runtime callers and remain for their final authenticated cutover.

The Go diff removes **664 tracked lines and adds 156**, including the two new
measurement files. The net Go cut is **508 lines**. This count includes code
moved out of the deleted filesystem files. It does not claim every removed
line was runtime implementation.

The orchestrator pod/trigger and launcher e2e masking fixtures now read through
`client.NodeLogs`. Their completeness assertions use the existing controller
query. They no longer open an output directory or read legacy node-log rows.
The dropped-line fixture exercises the real sink with an uploader fake and
checks that two lost lines seal incomplete. It is unit evidence, not authenticated
controller or physical persistence evidence.

Invariant retained: byte reads in the final storage path consume verified common
backend bytes under record authority. Removing the raw plug-in and sharded reader
adds no reference edge or bypass. Existing generated filesystem/S3 scope,
immutability, manifest, receipt and committed-reader proofs remain in place.

## Verification

Go commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-astra-W7/cache, GOTMPDIR=/tmp/sw-astra-W7/gotmp and
TMPDIR=/tmp/sw-astra-W7/tmp. Tests use -p 4. Commands target this worktree.

Passed:

- Whole-repository `go build -p 4 ./...`.
- Compile-only tests for every repository package. The first attempt found one
  remaining launcher fixture caller. It was migrated, and the full rerun passed.
  Both changed fixture packages were recompiled after their last assertion edit.
- Full tests for `pkg/storage/...`, `internal/blob/...`, `internal/artifacts`,
  `internal/depcache` and `internal/teamblob`.
- `TestPodNodeLog_SealsIncompleteAfterALoss` with the real sink and uploader fake.
- Vet for storage, blob, controller/client, orchestrator and launcher packages.
- Required commentcheck against main and current-diff sleepcheck against HEAD.
- Both embedded-document/changelog drift guards and `git diff --check`.
- API contents regenerated for storage, filesystem and S3. The deleted
  conformance snapshot was removed. The inherited orphan `pkg_runner` snapshot
  remains for W8/lead and is the only snapshot-set mismatch.

`go test -p 4 ./tests/acceptance -run '^TestStorage$' -count=1 -json` passes in
**161.198 seconds**, with **81 passing events and 424 native/platform skips**.
Those are the existing cells, including filesystem/S3 real-store records and
archive proofs. No new cell passes because none was added or unskipped.

Focused race tests for the filesystem measurement and dropped-line sink fixture
pass. The fake uploader is observed only after sink shutdown joins its writer.

The retired host fixtures were compiled, not executed as native integrations.
Their controller/setup still uses removed legacy tables. No archived T1 inventory
row is newly marked ported. The old raw conformance cases are removed with their
interface. Their byte-integrity and immutable-write behaviors remain covered by
the existing common filesystem/S3 and log suites.

## Known red regression

`go test -p 4 ./pkg/store -run
'^TestBlobRecordsMutationCannotOutliveAuthority$' -count=1` still fails all six
CommitRecord/CopyMemo lease, leaf and ancestor expiry cases. Each persists a
record after authority expires during namespace preparation. W7 changed none of
that mechanism. The approved transaction-local publication redesign remains
assigned to W1b under the brief's two-strikes rule.

## Amendment and handoff audit

| Entry | W7 result | Integration left |
| --- | --- | --- |
| A1-24 | Existing real tenant persistence remains. | W2 transactional authenticated bindings. |
| A1-29 | Authorized member metadata remains. | Server-derived tenant/default ref for repo-only exact lookup. |
| A1-30 | Logical remote memo/depcache transport remains. | W2/W9 profile authentication and transport. |
| A1-31/32 | Real ticket and receipt persistence remains. | Authenticated reservation, settlement and replay. |
| A1-33 | Both physical backends, common cleanup and collection remain. | Leased expired-staging cleanup contract. |
| A1-34 | Signed filesystem routes and C2 proof callback remain. | DataAuthority, BindData, NewDataClient and PolicyRouter still panic. |
| A1-35 | Real layouts and tagged commits remain. Raw artifact and sharded filesystem surfaces are now cut. | W1b mutation redesign and remaining S3/client/store-local cuts. |
| A1-36 | Bound artifact/memo/cache components remain. | W3/W8 fresh attempt/generation bindings and wingd construction. |
| A1-37 | Earlier SDK/cache helper and lint-path cuts remain complete. | None in W7. |
| A1-43/50 | Historical assets remain. | W8/W9 final host/profile/publication integration. |
| A1-53 | Existing storage acceptance rerun without new cells. | Native authenticated transports, placements and real-repository QA. |

All W7 entries in amendments-1.md and handoffs.md were checked. Earlier completed
work was retained. This session changes no foreign contract declaration.

## Contract changes needed and remaining real integration

W2/W9 must construct the declared `blob.LogTransport` and
`logseg.SegmentRecorder` bindings. The controller wire still has no segment-commit
operation or binding factory. W7 has not invented another authenticated wire
shape. DataAuthority, BindData, NewDataClient and PolicyRouter remain stubs.

W1b must declare leased cleanup that returns expired upload staging keys or calls
ObjectBackend.DeleteObject. Reachability collection cannot discover objects
that never became live blobs. W7 has not copied uploads SQL or inferred quota or
read authority from object listings. W1b also owns the approved mutation redesign.

W3/W8 still need wingd filesystem construction and fresh host StorageBindings.
W1/W2 need repo-reader exact lookup with server-derived tenant/default ref.
Legacy S3 output/log handlers, their clients and host sinks, the raw S3 log
reader/writer and store-local output APIs remain live. Their hard cut remains
unfinished. The old authenticated wrappers cannot supply the final transactional
binding merely by retaining their old bearer or output key.

## Shared files and assumptions

Minimal fixture callers outside the physical storage packages:

- internal/orchestrator/pod_node_host_test.go
- internal/orchestrator/segment_logs_test.go
- internal/orchestrator/trigger_host_test.go
- internal/runners/launcher/e2e_test.go

Runtime construction changed only in pkg/storage/storeurl/storeurl.go.
Shared prose/generated changes are CHANGELOG.md, VERSIONING.md, docs/storage.md,
docs/migrations/_unreleased.md, their embedded copies, three storage API snapshots,
the removed conformance snapshot, c1-data.md, handoffs.md and this report.
The logseg README's stale blocked-store claim now matches the passing existing
filesystem/S3 record cells.

Mechanism changed: filesystem measurement is named UsageReporter and exposes no
object methods. Keeping ArtifactStore would retain an obsolete byte-read surface
for a caller that needs only totals. Undo is to revert this retirement. No new
setting, environment variable, service, authority protocol or customer limit
was added.

Friction remains the unfinished authenticated/log construction, absent staging
cleanup seam, known W1b regression, retired host fixtures and inherited orphan
runner API snapshot. No product decision is requested.

The supplied worktree was reused. Only the required initial installed
`sparkwing info --for-agent` discovery ran. No candidate, service, pipeline,
deployment or Git write ran. Scratch was deleted after the final checks.

<!-- design:end -->

STATUS: CONTINUE
