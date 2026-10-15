# W7 storage handoff

<!-- design:begin -->

## Implemented

Continued from 9c765f242 and integration merge 50d00e50b. Earlier cache-service,
Git-cache, package-proxy, cache-chart, real-store byte/record, receipt, manifest,
memo, tagged-log, cleanup and filesystem artifact cuts remain.

`Server.WithDataObjects` now installs the configured physical backend's manifest
reader with W1b's `Store.SetResultBlobRecords`. Both standalone S3 and filesystem
startup paths already call this constructor. No authenticated factory is replaced.
Before this hook, terminal output/manifest attachment failed with
`transactional result record attachment is not configured`.

Invariant: a terminal result may attach only its authorized exact-attempt records
whose physical manifest bytes verify. The new twelve-case generated property
uses real tenant handles, awarded claims, execution starts, upload reservations,
signed filesystem PUTs, physical publication, receipt settlement and record
commits. It corrupts manifest bytes, observes refusal with the job still running,
restores the bytes, completes and replays the same terminal result. The test
failed on the missing hook before the fix and passes after it, including under
the race detector. It uses no fake record or result store. HTTP authentication
and S3 result attachment are not exercised by this property.

## Deleted

Removed `storage.SafeRelPath`, `storage.SafeArtifactKey`, their test and the
obsolete log-key assertion. These validators had no production callers.
`SafeSegment` remains because the controller calls it. Structured blob/log
helpers retain their own generated namespace and path proofs.

Removed unused `Store.PrunableNodeLogs`, its private `nodeLogColumnsOf` helper
and `Store.DeleteRunNodeLogs`. The latter's only caller was the obsolete
`TestTeamDeletionReachesARetiredRetentionClass` fixture, which was deleted.
The existing `TestDataStorageTeamPurgeDeletesLateSignedLogs` exercises the
surviving retired-class purge/recheck behavior through the common backend.

This removes **122 Go lines: 71 production and 51 test lines**. The runtime hook
adds four lines and its new integration test adds 135, for a net Go increase of
17 lines. The two public API snapshots are regenerated. Source docs, migration
notes, changelog and embedded copies describe the removed APIs and startup hook.
No archived T1 source-function inventory row is newly marked ported.

## Verification

Commands used GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-astra-W7/cache, GOTMPDIR=/tmp/sw-astra-W7/gotmp and
TMPDIR=/tmp/sw-astra-W7/tmp. Tests used -p 4 and ran from this worktree.

Passed:

- Whole-repository `go build -p 4 ./...` after the runtime change.
- Compile-only tests for all repository packages, `go test -p 4 ./... -run '^$'`.
- Full `pkg/storage/...`, `internal/blob/...`, `internal/artifacts`,
  `internal/depcache` and `internal/teamblob` tests.
- All controller `TestDataStorage*` tests, including the new real-store property.
- The new result-attachment property under the race detector.
- Vet for storage, blob, artifacts, controller and store.
- Required commentcheck against main and current-diff sleepcheck against HEAD.
- Both embedded-document drift guards, sync-docs --check and git diff --check.

Existing `TestStorage` acceptance passes in **221.096 seconds**, with
**81 passing events, zero failures and 424 native/platform skips**. The passing
cells include filesystem/S3 real-store upload, manifest-record, log-record,
collection and archive joins. No acceptance cell was added or unskipped.

Generated API contents match every current package snapshot. The inherited
orphan pkg_runner.txt remains the only snapshot-set mismatch and belongs to
W8/lead. Only the required initial installed sparkwing info discovery ran. No candidate,
service, pipeline or native placement ran.

## Known red regression

`go test -p 4 ./pkg/store -run
'^TestBlobRecordsMutationCannotOutliveAuthority$' -count=1` still fails all six
CommitRecord/CopyMemo lease, leaf and ancestor expiry cases. Each accepts a
mutation after authority expires during namespace preparation. The approved
transaction-local publication redesign remains with W1b under the brief's
two-strikes rule. W7 changed none of that mechanism.

## Amendment and handoff audit

All W7 entries in amendments-1.md and handoffs.md were checked. Earlier completed
work was retained. No foreign contract declaration changed.

| Entry | W7 result | Integration left |
| --- | --- | --- |
| A1-24 | Real tenant persistence and transactional store interfaces remain. | W2 authenticated bindings. |
| A1-29 | Authorized record/member metadata remains. | W1/W2 server-derived tenant/default ref for repo-only exact lookup. |
| A1-30 | Logical remote memo/depcache transport remains. | W2/W9 profile authentication and transport. |
| A1-31/32 | Exact ticket, reservation and receipt persistence remains. | Authenticated reservation, settlement and replay. |
| A1-33 | Common physical backends and cleanup remain. | Leased expired-staging cleanup seam. |
| A1-34 | Signed filesystem routes and contract clients remain. | DataAuthority, BindData, NewDataClient and PolicyRouter remain stubs. |
| A1-35 | Controller startup now installs W1b result-record verification. Unused raw validators and log pruning APIs are deleted. | W1b mutation redesign and remaining live legacy output/log callers. |
| A1-36 | Bound artifact/memo/cache components remain. | W3/W8 fresh attempt/generation bindings and wingd construction. |
| A1-37 | Earlier SDK/cache helper and lint-path cuts remain complete. | None in W7. |
| A1-43/50 | Historical assets remain. | W8/W9 host/profile/publication integration. |
| A1-53 | Existing storage acceptance rerun without new cells. | Native authenticated transports, placements and real-repository QA. |

## Contract changes needed and remaining real integration

W2/W9 must construct the declared `blob.LogTransport` and
`logseg.SegmentRecorder` bindings and declare the segment-commit wire operation.
W7 continues to implement those contract interfaces without another credential
protocol. DataAuthority, BindData, NewDataClient and PolicyRouter remain stubs.
W2 must bind manifest-capable record calls through
`Tenant.BlobRecords(storage.BlobManifests{Objects: objects})`. The raw Tenant
record methods omit a manifest reader. The result hook now handles terminal
verification, not those authenticated record transports.

W1b must declare leased cleanup returning expired staging keys or calling the
common backend. Reachability cannot find objects that never became live blobs.
W7 has not copied upload SQL or inferred authority/quota from object listings.
The approved mutation redesign remains W1b's work.

W3/W8 still need fresh host storage bindings and wingd filesystem construction.
A source audit found `internal/nodehost/artifacts.go` still constructs consumed
manifest keys with Name=runID and an empty Scope.Key. The final layout is
Name=result and Scope.Key=run/attempt/generation. Its legacy StateStoreOld view
cannot identify the producer attempt. W3's final StateStore.ReadNode interface
can supply the persisted identity. W7 did not invent a competing host resolver
or change W3's contract. This source finding was not exercised as a native host
failure. It is recorded in handoffs.md.

Legacy S3 output/log handlers, clients and host sinks, raw S3 log readers/writers,
store-local output APIs and disk-log readers remain live. Their callers still
need the authenticated and host cutovers above. Deleting those implementations
alone would break runtime behavior. No larger unused W7 surface was found by
the bounded read-only audit.

## Shared files and assumptions

Shared Go files touched outside physical storage:

- pkg/controller/data_storage.go: installs the existing store result hook.
- pkg/controller/data_result_test.go: new real-store/physical-byte property.
- pkg/controller/log_segments_internal_test.go: deletes the obsolete fixture.
- pkg/store/node_logs.go: deletes unused old log methods and private helper.

Other changes are pkg/storage/segment.go, its deleted artifact test,
pkg/storage/logseg/keys_test.go, two API snapshots, c1-data.md, handoffs.md,
this report, CHANGELOG.md, docs/storage.md, docs/migrations/_unreleased.md and
their embedded copies.

No new mechanism, interface, setting, environment variable, service, quota or
credential format was added. The constructor installs the existing store hook
before sharing the store. Undo is to revert this wiring and the unused API cuts.
The default Git identity/configuration was not changed. No Git writes ran.
Scratch was deleted after the final checks.

<!-- design:end -->

STATUS: CONTINUE
