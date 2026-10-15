# W7 storage handoff

<!-- design:begin -->

## Implemented

Continued from b57d9b76b. Earlier real-store result keys, upload receipts,
manifest admission, memo records, physical log commits and leased CAS collection
remain in place. The cache service, gitcache, package proxy and cache chart
remain deleted.

Standalone controllers without S3 now construct the common filesystem backend
under home/data and pass it to WithDataObjects. The same backend serves signed
transfers and performs leased maintenance. It closes after server shutdown.
The public router mounts C2's signed data upload, object download and log upload
paths. They require their signed proof independently of bearer middleware.

FilesystemOptions.Endpoint now names the controller base URL. Signatures bind
method, exact object, byte checks and expiry. Generated URLs retain an external
proxy prefix. Verification uses the registered controller path after the proxy
strips that prefix. Filesystem.VerifyTransfer supplies C2's Endpoint.Verify
callback. This is a W7-local contract addition. No foreign declaration changed.

Existing filesystem acceptance fixtures now transfer through the production
controller Handler. Their real SQLite upload, blob-record, manifest, archive and
log-record boundaries remain real. Authentication and issuance remain seeded or
contract fixtures. No new acceptance cell was added or native skip removed.

## Deleted and migrated

Removed **528 tracked Go lines: 361 implementation and 167 test lines** in the
diff. The filesystem cut removes fsOutputs and its file moves, outputSigner,
WithOutputSignKey, the old /api/v1/outputs PUT/GET handlers, fsLogObjects, its raw
sharded files and the old log PUT handler. The orchestrator API no longer owns
the removed signing key or read route. No compatibility handler was added.

Ported TestSignedLogKeyCannotBeReplaced to the common backend and registered
log route, with 64 generated payload pairs. Changed the existing signature
property to mutate the token while retaining a registered PUT route, so it
reaches proof verification. Cross-method and cross-path refusal are also tested
through the production router. No archived T1 inventory port is newly claimed.

Removed two obsolete filesystem fixtures. Lost/corrupt segment behavior remains
covered by TestCommittedReadsStopBeforeCorruptBytes, including emitted-byte
accounting and an incomplete result. Commit/retry and quota behavior remain
covered by TestFilesystemSignedCommit and the real BlobUpload reservation,
rollback, quota and deduplication tests. The old free-slot physical-move behavior
is not part of the common upload contract.

## Verification

Go commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-astra-W7/cache, GOTMPDIR=/tmp/sw-astra-W7/gotmp and
TMPDIR=/tmp/sw-astra-W7/tmp. Tests use -p 4. Commands run with go -C on this
worktree. No Sparkwing binary, candidate, pipeline, deployment or Git write ran.

Passed:

- Whole-repository go build -p 4 ./....
- Compile-only tests for all 173 repository packages.
- Full tests for internal/blob/..., pkg/storage/..., internal/artifacts,
  internal/depcache and internal/teamblob.
- Controller TestDataStorage and TestSignedLogKey, plus standalone
  TestFilesystemDataStartup, including IPv4, IPv6, external origin and proxy
  prefix construction and actual published filesystem bytes.
- Race tests for both registered-route properties.
- Focused real-store BlobUpload replay, rollback, quota and deduplication tests.
- Vet over blob, controller, controller command, logseg, orchestrator and
  acceptance packages.
- Required commentcheck against main, clean across 1,238 files. Sleepcheck
  against HEAD is clean. No new wait was added.
- Embedded docs/changelog drift checks and git diff --check.
- All generated API snapshot contents match. The inherited orphan
  pkg_runner snapshot remains for W8/lead removal.

Full TestStorage passes with **81 passing events and 424 native/platform skips**.
The final source passes after proxy-prefix handling. The missing-route
regression fails with 404 when a temporary Go overlay removes the three physical
registrations. The normal router passes. No listener is used by these proofs.

A broader acceptance invocation also reached TestProtocol and failed because
sdk/typescript/node_modules/typescript/bin/tsc is missing. It is not claimed as
a full acceptance pass. No passing native platform, PostgreSQL, Docker, live S3,
placement, deployment or launch-QA evidence is claimed.

## Amendment and handoff audit

| Entry | W7 result | Integration left |
| --- | --- | --- |
| A1-24 | Real tenant persistence remains in use. | W2 transactional authenticated bindings. |
| A1-29 | Existing authorized member metadata is retained. | Server-derived tenant/default-ref selection for repo-only exact lookup. |
| A1-30 | Existing logical remote memo/depcache transport is retained. | W2/W9 profile authentication and transport. |
| A1-31/32 | Real ticket and receipt persistence remains in use. | Authenticated reservation, settlement and replay. |
| A1-33 | Standalone filesystem startup and signed mounting now join the configured maintenance backend. | Leased expired-staging cleanup contract. |
| A1-34 | Signed filesystem routes and C2 proof callback are implemented. | DataAuthority, BindData, NewDataClient and PolicyRouter remain stubs. |
| A1-35 | Real layouts and tagged commits remain in use. Legacy filesystem physical transport is cut. | W1b mutation redesign, S3/store-local output and old host/client cuts. |
| A1-36 | Existing bound artifact/memo/cache layers remain. | W3/W8 fresh attempt/generation bindings and wingd construction. |
| A1-37 | SDK/cache helper and old lint-path cuts remain complete. | None in W7. |
| A1-43/50 | Historical assets are retained and earlier cache delivery cuts remain. | W8/W9 final host/profile/publication integration. |
| A1-53 | Existing filesystem cells now traverse the real physical router. | T1 native authenticated transports, placements and real-repository QA. |

All W7 amendments and handoffs were checked. Completed earlier work was retained.
The approved record-mutation authorization redesign remains with W1b. It was not
patched again or represented as fixed by this continuation.

## Contract changes needed and real integration left

W2/W9 must construct the existing blob.LogTransport and logseg.SegmentRecorder
bindings. The controller wire still lacks a segment-commit operation. W7 has not
invented one or added another authentication protocol. DataAuthority, BindData,
NewDataClient and PolicyRouter also remain unimplemented.

W1b must declare leased cleanup that returns expired upload staging keys or calls
ObjectBackend.DeleteObject. CAS reachability collection cannot find objects that
never became live blobs. W7 has not copied uploads SQL or used bucket listings
as authority.

W3/W8 still need new wingd filesystem construction and fresh host StorageBindings.
W1/W2 need repo-reader exact lookup with server-derived tenant/default ref. Until
these joins land, S3 output/log handlers, old clients and host sinks, raw S3/log
readers, and store-local output files remain live. This is unfinished W7 work.

## Shared files and assumptions

Runtime caller changes outside W7's physical packages are
cmd/sparkwing-controller/main.go and its new data.go, pkg/controller/server.go,
node_output.go, log_segments.go, and internal/orchestrator/wingd_api.go. Updated
controller fixtures are log_immutable_test.go, log_segments_internal_test.go,
node_output_internal_test.go and the new data_transfers_test.go. Startup tests
are in cmd/sparkwing-controller/data_test.go.

Existing acceptance transport callers changed in storage_test.go and the
storage_filesystem, storage_depcache, storage_logs, storage_log_sink,
storage_manifests, storage_materialization, storage_publication, storage_remote
and storage_remote_archives test files. Logseg/filesystem and blob fixture
endpoints now use the controller base URL. No acceptance inventory changed.

Shared prose/generated changes are CHANGELOG.md, docs/storage.md,
docs/self-hosting.md, docs/migrations/_unreleased.md, their embedded copies,
.apidiff/pkg_controller.txt, c1-data.md, handoffs.md and this report.

Mechanisms added: standalone construction and signed mounting of the already
contracted filesystem backend. Existing output moves and raw log serving cannot
supply shared verified objects, so those paths were removed. No setting,
environment variable, service, authority interface or customer limit was added.
The standalone signer uses a per-start random key, matching the supported single
replica topology. Restart invalidates outstanding signed links, while bytes and
records remain. Shared-key injection remains available through FilesystemOptions.

Remote filesystem clients use the existing external-url setting. An empty value
uses the listener address, with localhost for a wildcard. New wingd bindings and
request-derived signing origins remain integration work for W2/W8.

Friction is the missing authenticated/log construction and staging contract,
plus the missing TypeScript compiler in the broad invocation. No product decision
is requested. The supplied worktree was reused. Scratch is deleted after checks.

<!-- design:end -->

STATUS: CONTINUE
