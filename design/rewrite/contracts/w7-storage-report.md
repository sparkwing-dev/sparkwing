# W7 storage handoff

<!-- design:begin -->

## Implemented

Continued from c734b65f7. Earlier cache-service, Git-cache, package-proxy and
cache-chart cuts remain complete. Existing real-store upload receipts, result
layouts, blob records, manifests, memo records and tagged log persistence remain
in use. No new acceptance cell or native skip change was added.

Run deletion and team purge/recheck now use the configured common data backend
through `blob.LogDeleter.DeleteLogs(ctx, team, run)`. An empty run selects the
whole tenant. Filesystem and S3 remove physical logs across stored retention
classes, including retired classes. They return removed bytes and replay absent
objects successfully. Physical deletion no longer reads legacy node-log tables.
The controller retains its existing team-deletion lease.

Invariant: deleting one tenant's logs, optionally one run, cannot remove another
tenant's logs, another run's logs or CAS objects. Generated filesystem and S3
properties cover 64 scopes each, prefix collisions, manifest/segment objects,
retired classes, replay and CAS preservation. Filesystem traversal follows no
symlink. S3 rejects out-of-scope listed keys, service failures and stalled pages.
The listing is a cleanup selector, never accounting or read authority.

The common filesystem router also proves late signed log uploads are removed
by another team purge. This ports the physical storage assertion from the old
team-deletion fixture. It does not prove authenticated team deletion or its
final-schema operator workflow. The old fixture fails in setup while creating a
removed `tokens` row.

## Deleted and ported

Removed **677 tracked Go lines: 353 runtime lines and 324 test lines** in the
diff. This count includes changed lines, rather than claiming a 677-line net cut.

- Removed controller `deleteRunSegments`, the raw S3 prefix deleter and retention
  class listing, and the record-dependent physical cleanup adapter.
- Removed the unused sharded filesystem `PutIfAbsent`, `Delete`, temporary writer,
  conditional-write locks and both platform lock implementations. Read-only
  filesystem fixtures and usage measurement remain.
- Removed the obsolete writer conformance, lock and write-roundtrip fixtures.
  The usage test now seeds real files. Existing read-path validation stays.
- Ported competing-handle immutability to signed common filesystem PUTs, with
  64 generated payload pairs and actual concurrent writers. One writer wins,
  and committed bytes equal that winner. No filesystem lock remains.
- Replaced class-list enumeration assertions with scoped deletion across three
  stored classes, including one absent from the controller configuration.
- The pod-host fixture now returns the read-only `logseg.Objects` interface.

No archived T1 inventory row is newly claimed complete. No removed authentication
fixture is represented as authenticated final-schema evidence.

## Verification

All Go commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-astra-W7/cache, GOTMPDIR=/tmp/sw-astra-W7/gotmp and
TMPDIR=/tmp/sw-astra-W7/tmp. Tests use -p 4. All commands use this worktree.
No Sparkwing binary, candidate, pipeline, deployment or Git write ran.

Passed:

- Whole-repository `go build -p 4 ./...`.
- Compile-only tests for all repository packages.
- Full tests for internal/blob/..., pkg/storage/..., internal/artifacts,
  internal/depcache and internal/teamblob.
- Controller TestDataStorage and the existing tagged-log maintenance test.
- Race tests for new scoped deletion, concurrent immutability and controller
  physical purge properties.
- Vet for blob, storage, controller and orchestrator packages.
- Required commentcheck against main. Sleepcheck against HEAD.
- Embedded storage-doc and changelog drift checks and git diff --check.
- Generated API contents match, including the removed filesystem writer methods.
  The inherited orphan pkg_runner snapshot remains for W8/lead removal.

`go test -p 4 ./tests/acceptance -run '^TestStorage$' -count=1 -json`
passes in 295.375 seconds with **81 passing events and 424 native/platform
skips**, and no failures. This retains the existing real-store filesystem/S3
log-record, manifest-record and archive proofs. No new acceptance cell passes
because no cell was added or unskipped in this session.

No PostgreSQL, native placement, Docker, live S3, deployment or full launch-QA
evidence is claimed. The S3 deletion tests use the real AWS client with an
injected HTTP service.

## Known red regression

`go test -p 4 ./pkg/store -run
'^TestBlobRecordsMutationCannotOutliveAuthority$' -count=1` still fails all six
CommitRecord/CopyMemo lease, leaf and ancestor expiry cases. Every case exposes
a persisted record after authority expires during namespace preparation.
W7 changed no part of that mechanism. The approved transaction-local record
publication redesign remains assigned to W1b under the two-strikes rule.

## Amendment and handoff audit

| Entry | W7 result | Integration left |
| --- | --- | --- |
| A1-24 | Existing real tenant persistence remains in use. | W2 transactional authenticated bindings. |
| A1-29 | Authorized member metadata is retained. | Server-derived tenant/default-ref selection for repo-only exact lookup. |
| A1-30 | Logical remote memo/depcache transport remains. | W2/W9 profile authentication and transport. |
| A1-31/32 | Real ticket and receipt persistence remains. | Authenticated reservation, settlement and replay. |
| A1-33 | Common physical cleanup now serves run deletion and team purge/recheck. Existing leased CAS collection remains. | Leased expired-staging cleanup contract. |
| A1-34 | Signed filesystem routes and C2 proof callback remain implemented. | DataAuthority, BindData, NewDataClient and PolicyRouter remain stubs. |
| A1-35 | Real layouts and tagged commits remain. Duplicate physical cleanup and the unused filesystem writer are cut. | W1b mutation redesign and legacy S3/client/store-local cuts. |
| A1-36 | Bound artifact/memo/cache components remain. | W3/W8 fresh attempt/generation bindings and wingd construction. |
| A1-37 | Earlier SDK/cache helper and lint-path cuts remain complete. | None in W7. |
| A1-43/50 | Historical assets remain. | W8/W9 final host/profile/publication integration. |
| A1-53 | Existing storage acceptance is rerun without new cells. | Native authenticated transports, placements and real-repository QA. |

All W7 amendments and handoffs were checked. Earlier completed work was retained.
The physical deletion and unused-writer cuts above close independent remaining
work without inventing an authentication protocol.

## Contract changes needed and real integration left

W2/W9 must construct the declared blob.LogTransport and logseg.SegmentRecorder
bindings. The controller wire still lacks a segment-commit operation. W7 has
not added a competing wire shape. DataAuthority, BindData, NewDataClient and
PolicyRouter also remain unimplemented.

W1b must declare leased cleanup that returns expired upload staging keys or calls
ObjectBackend.DeleteObject. Reachability collection cannot discover objects that
never became live blobs. W7 has not copied uploads SQL or used listings as quota
authority. W1b also owns the approved record-mutation redesign above.

W3/W8 still need wingd filesystem construction and fresh host StorageBindings.
W1/W2 need repo-reader exact lookup with server-derived tenant/default ref.
Legacy S3 output/log handlers, clients, host sinks, raw log readers and store-local
output files remain live. Their hard cut remains unfinished. The new cleanup
operates on the final common layout, not the retired prefixed S3 layout.

## Shared files and assumptions

Runtime caller changes outside the physical packages are
pkg/controller/data_storage.go, deletion.go, handlers.go, log_reads.go and
log_segments.go. Shared tests are pkg/controller/deletion_review_test.go,
log_classes_internal_test.go, the new log_delete_test.go and
internal/orchestrator/pod_node_host_test.go.

Shared prose/generated changes are CHANGELOG.md, docs/storage.md,
docs/migrations/_unreleased.md, their embedded copies, .apidiff/pkg_storage_fs.txt,
c1-data.md, handoffs.md and this report.

Mechanism added: the W7-local LogDeleter contract on the existing common backends.
The old cleanup required removed node-log records and a second physical layout,
so it cannot clean final filesystem/S3 logs. The new operation keeps physical
cleanup with the byte owner. Undo is to revert this scoped cleanup cut. No new
setting, environment variable, service, authority protocol or customer limit
was added. No foreign contract declaration changed.

Friction is the unfinished authenticated/log construction and staging contract,
the known W1b regression, and legacy fixtures using removed tables. No product
decision is requested. The supplied worktree was reused. Scratch is deleted after
checks.

<!-- design:end -->

STATUS: CONTINUE
