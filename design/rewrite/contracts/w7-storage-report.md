# W7 storage continuation

<!-- design:begin -->

## Implemented and deleted

Resolved the two actual remaining conflict paths. `browser_reads.go` uses the
blob-backed dashboard reader and its tenant-scoped backend signature.
`log_segments.go` stays deleted. Its 19-line integration version would restore
the removed S3 log fallback. Existing `log_objects.go` selects only the configured
physical blob backend. No deleted hosting file was restored. No conflict marker
remains. The brief prohibits Git writes, so the lead must stage these resolutions
and complete the merge.

The merged log-signing handler still called the deleted `Server.logClassReady`.
The private physical log binding now requires `LogClassReady`. Filesystem checks
the canonical class handled by its expiry pass. S3 checks the bucket lifecycle.
The handler uses that backend method with the real tenant's persisted retention
class. Whole-repository builds pass with the legacy helper absent.

Replaced the log-submission test's fake byte/signing backend with real filesystem
storage. The existing test now obtains a real award, starts the job, signs log
slots through authenticated routes, PUTs compressed bytes through the controller
Handler, commits segments and replays the final commit. It checks the real tenant
records, verified bytes and the authenticated controller GET. Its generated
subject-substitution checks remain active. The separate egress tests retain only
the read-counting fake they use. Filesystem retention checks cover invalid classes
and cancellation. No acceptance cell was added.

The earlier cache command/service, gitcache proxy, package proxy, cache chart,
raw S3 output/log backends and run-directory log fallback remain deleted.
Historical signed release matrices remain intact. W1/W1b's real reservations,
receipts, record mutations, materialized selectors, segment records and collection
APIs remain the persistence owners. W2's merged `configureDataStore` now supplies
real authority, access, readable-blob and download-accounting adapters.

## Production line delta

W7 has **-5,122 net production Go lines against main**. The worktree has 10,517
lines versus 15,639 on main. **39 former production files totaling 8,050 lines**
are absent. Counts use the lead's W7 prefixes, include depcachekey and exclude
tests, testdata, acceptance and contract-named files. They count working contents
because the index is still unmerged. This continuation adds 12 W7 production
lines and removes the 19-line shared merge helper.

The -6,630 budget is exceeded by **1,508 lines**. The previous justification
remains unwaived. The estimate did not size the complete filesystem/S3 signing,
immutable publication and byte verification, or the tagged upload protocol added
by the authority amendments. The implemented types now count as production
rather than contract scaffolding. Both physical backends and current-authority
checks are required. W7 is not COMPLETE while the overage is unresolved.

## Verification

All Go commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-gocache and GOTMPDIR=/tmp/sw-astra-W7/go. Tests use -p 4.

| Check | Result |
| --- | --- |
| Whole-repository go build ./... after production edits | Pass |
| internal/blob/..., artifacts, depcache, depcachekey, pkg/storage/... | Pass |
| TestMaterializedResultCopiesBytesThroughControllerAndHost | Pass, 0.398 s |
| Controller log submission, committed dashboard, data-storage and egress tests | Pass |
| Vet on blob, storage, controller, backend and web | Pass |
| Existing TestStorage acceptance suite | Pass, 80.439 s, 69 passing test events, 424 skips, zero failures |
| commentcheck -base main . | Clean across 2,190 files |
| internal/doccheck and git diff --check | Pass |
| Repository Git configuration | Only the expected global user.name matched. No core.worktree override |

Passing storage cells include filesystem/S3 upload isolation, manifests, archives,
record persistence, reachable-byte collection, log records/sinks, materialization,
remote reads and publication. Native targets remain skipped. These checks do not
claim native OS/placement, PostgreSQL, deployment, pipeline or release evidence.
No new historical test port was needed. Existing ports remain active.

## Contracts and remaining integration

A1-29, A1-30, A1-31 and A1-33 remain implemented. Consumers of A1-24, A1-32,
A1-34, A1-35, A1-36 and A1-37 use the real store or their declared foreign
interfaces. A1-43's historical assets remain intact. A1-50 production host
composition and A1-53 native evidence remain with their assigned owners.
Lead-owned handoffs and amendments were not edited. No frozen foreign contract
was changed.

**W1/W2 receipt projection remains needed.** `NodeRow.OutputRecord` persists the
selected record, including a materialized result without an attempt. The receipt
needs a tenant-scoped reporting projection resolving that selector to its
committed digest within the snapshot. It must refuse foreign tenant/result
selectors and preserve the runs.read authority boundary. Then remove OutputRef,
its report/finish API, validation, and node_outputs/data_objects queries together.
The merged receipt adapter now builds nodes from ReadSnapshot but supplies no
output hashes. Using LookupRecord would require data.read and change this
boundary. No raw caller digest or stronger read grant was added here.

**W2 log transport remains needed.** Authenticated signing, segment commits and
controller GET now have a real filesystem route proof. The blob.LogTransport
client, authenticated uncertain-upload verification route and SegmentRecorder
client adapter remain absent. Hosts already consume those declared interfaces.
W7's UploadVerifier and signed physical backends supply byte verification.

**W3/W8 native bindings remain external.** Supply fresh storage/log bindings per
attempt and owner generation. The materialization proof still passes through the
real store, wire and host. It does not establish native placement evidence.

**W1 maintenance inputs remain external.** Seal operator log-retention policy at
intake. Physical staging cleanup for expired reservations needs a fenced store
selector. Existing retention and lease contracts remain intact.

## Shared files and friction

Shared edits in this continuation are pkg/controller/browser_reads.go,
pkg/controller/log_submission_routes.go, pkg/controller/log_submission_routes_test.go,
pkg/controller/egress_test.go and deletion of pkg/controller/log_segments.go.
W7 edits are internal/blob/log_retention.go, its test and
pkg/storage/logseg/README.md. The README describes the lifecycle and route proof.
Earlier shared-file manifests remain beside this report.

The prompt named three conflicts. The actual index had two. Their working
contents are resolved, but Git still lists unmerged stages until the lead stages
them. The initial build failure named the deleted lifecycle helper and is fixed
at its remaining caller. The egress fake is now local to its tests because the
submission proof uses real bytes. No new service, setting or retry mechanism was
added. Review and merge completion remain with the lead.

Scratch /tmp/sw-astra-W7 is removed at exit. Shared /tmp/sw-gocache is retained.

<!-- design:end -->

STATUS: CONTINUE
