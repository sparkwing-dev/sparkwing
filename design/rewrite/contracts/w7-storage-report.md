# W7 storage handoff

<!-- design:begin -->

## Implemented in this continuation

Continued from `2a1fab836`. Earlier output/artifact/memo layouts, real upload
receipts and physical log-record integration remain in place.

The controller now supplies its configured `ObjectBackend` to W1's real
`MarkBlobTombstones` and `CollectBlobTombstones` APIs. The retained-data worker
renews its maintenance fence before this work. A configured filesystem backend
also runs `ExpireLogs` after another renewal. A physical collection failure logs
an error and leaves the billing pass active. Standalone S3 construction installs
that backend through `WithDataObjects`, using the declared `DataController`
store/backend fields. Authenticated data methods remain W2's implementation.

Removed the separate bucket-reconciliation worker, its advisory lease invocation,
listing-derived tenant counts, free-share log pruner and `storage_pass` health
projection. Unique blob and committed tagged-log accounting remain with W1's
store. Read-only deployment bucket measurement remains available.

Removed the now-unused team-bucket measurement API, listing client, page budget
and S3 fixture. The retained teamblob helper constructs keys for the older output
downloader. Its key-containment property now needs no object service.

Deleted **881 tracked Go lines: 449 implementation and 432 test lines**.
This includes the 209-line `pkg/controller/storage_pass.go`, 130-line
`internal/teamblob/measure.go` and 175-line old storage-pass test file.
The cache service, gitcache, package proxy and cache chart remain deleted from
prior sessions. The remaining old output/log transport files are still live.
No new acceptance cell or archived T1 test port was added.

## Invariants and evidence

Invariant: CAS collection deletes only an unreachable tenant/digest after the
store's grace period and under its current maintenance fence. A foreign
maintenance holder cannot start the controller's physical maintenance pass.

The controller integration property uses real SQLite and filesystem bytes.
It generates 64 objects across two tenants, with matching cross-tenant digests,
independent reference roots, new/young/old tombstones, and replay. Referenced
bytes and grace-period bytes survive. Eligible unreachable bytes disappear.
The lease test preserves an expired physical log while another holder owns the
lease, then expires it when a new local fence can be acquired. A signed-license
regression injects physical deletion failure and verifies the real zero-rate
billing watermark still resets. It failed with the skipped reset before the
error paths were separated. No sleep or network listener is used.

## Verification

Go commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-astra-W7/cache, GOTMPDIR=/tmp/sw-astra-W7/gotmp,
TMPDIR=/tmp/sw-astra-W7/tmp and `go -C` on the supplied worktree. Tests use -p 4.

Passed:

- Whole-repository `go build -p 4 ./...`.
- Full tests for internal/blob/..., internal/teamblob, internal/artifacts,
  internal/depcache and pkg/storage/....
- `go test -p 4 ./pkg/controller -run '^TestDataStorage' -count=1`.
- Vet over controller, controller command, teamblob, blob and storage packages.
- Required commentcheck against main, clean across 1,235 files.
- Sleepcheck against HEAD, including the untracked tests added in this session.
- Embedded documentation and changelog drift tests.
- All generated API contents match. The inherited orphan pkg_runner snapshot
  remains for W8/lead removal.
- Regenerated controller API snapshot and diff whitespace checks.

Full TestStorage passes with **81 passing events and 424 native/platform skips**.
The retained collection, archive and physical-log cells pass. No native cell was
unskipped or counted as runtime acceptance.

Sleepcheck against main fails on three inherited lines outside this change:
internal/limits/deadline_test.go:58 and :66, and
sparkwing/sparkwingtest/host_test.go:214. An unscoped sleepcheck also reports
379 pre-existing waits. No W7 wait was added. Those foreign tests were not edited.
No PostgreSQL, native platform, socket, deployment or launch-QA pass is claimed.

## Amendment and handoff audit

| Entry | W7 result | Real integration left |
| --- | --- | --- |
| A1-24 | Existing physical/tenant record wiring is retained. | W2 transactional authority bindings. |
| A1-29 | Authorized members retain path/size/mode metadata. | Server-derived tenant/default-ref selectors for repo-reader exact lookup. |
| A1-30 | Remote logical reads retain persisted memo naming and verified-default scope. | W2/W9 authenticated profile transport. |
| A1-31/32 | Real ticket/receipt persistence and exact uploader checks remain wired. | W2 authenticated reservation, settlement and replay. |
| A1-33 | Real tombstone collection is now installed in controller maintenance. | Filesystem startup construction and expired staging cleanup contract. |
| A1-34 | Server backend configuration uses the declared DataController fields. | DataAuthority, BindData, NewDataClient and PolicyRouter remain stubs. |
| A1-35 | Layouts and physical readers remain on real tenant APIs. | W1b record-mutation redesign and final output/log transport cut. |
| A1-36 | Existing host artifact/memo/cache bindings remain in place. | Fresh attempt/generation host construction. |
| A1-37 | SDK Cache/DirCache.Key and pipeline lint declarations are implemented. The old HTTP lint path is already removed. | None in W7. |
| A1-43/50 | Earlier cache-service delivery cuts remain in place. | Final W8/W9 profiles, engine assets and separate publication result. |
| A1-53 | Existing real-store storage cells are retained. | T1 native HTTP/api.sock/placement/platform and real-repository QA. |

All W7 entries in amendments and handoffs were checked. Completed record-layout,
manifest and physical-log entries were not reimplemented.

## Contract changes needed and remaining hard cut

W2/W9 need a bound log-client constructor implementing the existing
`blob.LogTransport` and `logseg.SegmentRecorder`. The shared controllerwire API
still declares no segment-commit operation. W7 has not added a foreign wire
contract or another authentication protocol.

W1b needs to declare leased cleanup of expired upload staging keys. The real
marking transaction releases expired upload quota holds, but W7 has no operator
API that returns those keys or invokes `ObjectBackend.DeleteObject` for them.
CAS collection cannot discover objects that never became live blobs. W7 has not
copied the uploads SQL into another owner.

W2/W3/W8/W9 still need authenticated data binding, signed filesystem route mounting,
filesystem backend startup and fresh host StorageBinding construction. Until that
cutover, controller node_output.go, output_objects_s3.go, output_download_s3.go,
log_segments.go and log_reads.go, client output.go/logsegments.go, orchestrator
segment_logs.go and the remaining raw artifact/log backends are still live.
Their deletion is unfinished. The new maintenance path adds no compatibility shim.

The prior W1b record-mutation authorization DESIGN remains open. Its approved
transaction-local publication owner is assigned to W1b. This session neither
changes nor re-tests that separately reported mechanism.

No product decision is requested. The remaining contract seams belong to the
listed owners.

## Shared files and friction

Caller/configuration migrations outside W7's physical packages are
cmd/sparkwing-controller/main.go and pkg/controller/server.go, storage.go,
handlers.go, log_segments.go and log_segments_internal_test.go. The new
controller data_storage.go/test consumes existing store/backend contracts.
Removed worker files are storage_pass.go and storage_pass_test.go.

Shared prose/generated edits are CHANGELOG.md, docs/limits.md, docs/storage.md,
docs/migrations/_unreleased.md, their generated embedded copies,
.apidiff/pkg_controller.txt, handoffs.md and this report. W7 also updates its
blob README and teamblob code/tests.

Mechanism added: WithDataObjects replaces the removed bucket-pass option with
the already-declared ObjectBackend. The smaller existing bucket-listing hook
cannot perform reachability-safe deletion. No setting, environment variable,
service or new authority interface was added.

Friction is the missing authenticated/log transport construction and expired
staging cleanup contract, plus the inherited sleepcheck failures. The supplied
worktree was reused. No Git write, candidate install, pipeline, service,
deployment or release ran. Scratch is deleted.

<!-- design:end -->

STATUS: CONTINUE
