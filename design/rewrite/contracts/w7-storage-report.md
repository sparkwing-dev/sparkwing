# W7 storage handoff

<!-- design:begin -->

## Merge and integration

The three original conflict files have resolved working-tree contents:
internal/depcachekey/key.go, internal/nodehost/depcache_records.go and
tests/acceptance/skeleton_test.go. The executor keeps its shared archive import.
The skeleton keeps the integration branch's registration order. W3's deleted
orchestrator hosting files remain deleted. No conflict markers remain.

The index still reports those paths as unmerged because this worker made no Git
metadata writes. The lead must stage the resolutions and finish the merge.
HEAD remains 30ce79b4f and MERGE_HEAD remains d7e026df4.

MERGE_HEAD predates the materialization implementation named in the user handoff.
Its store only had an unconfigured CopyMaterializedRecords interface. The newer
integration tip, 9e151731d2cb0e2bee7e48cbb955695200de467c, contains the copier and
TestMaterializedResultCopiesBytesThroughControllerAndHost. I content-forwarded
that integration range into this tree, resolved the overlaps and kept W7's
shared capture/output/archive changes. This includes W1b's store, wire, host,
metadata-verification and harness cleanup changes. These are integration imports,
not newly authored W7 features. Newly imported files are untracked until staged.
The range d7e026df4..9e151731d identifies the imported integration files.
Lead-owned amendments and handoffs remain the integration versions.

## Implemented and deleted

Retained dashboard logs select tagged subjects with Tenant.ReadRunView and
read ranges with Tenant.ReadLogSegments through logseg.CommittedReader.
The request's actor accompanies each read and live-ring continuation. The
incoming implementation retains ordered attempts, offset cursors, incomplete
markers and store-clock completeness. Closing a stream cancels pending reads.

Server.WithDataObjects now supplies the log reader's physical backend for both
filesystem and S3. The standalone controller no longer constructs a second S3
log store. Local dashboards use the committed reader too.

Deleted the raw S3 ArtifactStore, its Get/List/PutIfAbsent methods and conditional
writer tests. MeasurementStore retains bounded physical usage reporting only.
Deleted WithS3Logs, its separate storage field, the duplicate lifecycle reader,
the object-listing log reader and its abandoned-manifest writer. The integration
range already supplies store-owned abandoned-stream sealing and its ports.

Deleted the backend's run-directory log fallback and the unused NodeLog and
EnvelopeLog path builders. The existing dashboard HTTP log tests now use an
in-memory handler and a declared backend fixture. They preserve content-format
checks without a socket or a legacy file layout. Deleted internal/teamblob and
its unused object-key builder. Its remaining slug validator moved unchanged to
blob.ValidLogTeam and both callers use it.

cmd/sparkwing-cache, internal/cache, gitcache, the package proxy and cache chart
remain absent. Historical signed release assets remain unchanged.

W7's production delta is **-5,779 lines against main**, including untracked
production files and excluding tests, testdata, acceptance and contract files,
using the lead's area prefixes. The target is -6,630, so **851 lines remain over
budget**. Against this session's starting HEAD, W7 is -552 production lines.
Five former production files totaling 691 lines are absent versus HEAD, with
shared decode and measurement behavior retained in smaller files. Across all
W7 sessions, 38 former production files totaling 7,965 lines are absent vs main.
These counts do not claim that every line in the imported integration range
was authored here.

## Evidence

All Go checks used GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4, -p 4,
GOCACHE=/tmp/sw-gocache and temporary files under /tmp/sw-astra-W7.

- Whole-repository go build ./... passes after the forward integration and cuts.
- Full suites pass for internal/blob/..., internal/artifacts, internal/depcache,
  internal/depcachekey and pkg/storage/....
- TestMaterializedResultCopiesBytesThroughControllerAndHost passes, including
  missing/corrupt output, manifest and member cases. Focused materialization and
  log-persistence store tests pass.
- Controller TestCommittedDashboard* and TestDataStorage* pass. New generated
  tests use a real claim/start, signed filesystem upload, tenant segment commit,
  offset read and SSE resume. Foreign or revoked actors emit no bytes. No native
  receipt or new acceptance cell is claimed.
- TestStorage passes in 86.740 seconds: 263 passing test events, 424 skips and no
  failures. Existing filesystem/S3 records, sinks, archives, reference-edge,
  remote-cache and memo cells pass. Skips remain missing native evidence.
- Focused host materialization, output, artifact and cache-key tests pass.
- The three existing dashboard log/static/format HTTP tests pass through handlers.
  Filesystem controller startup tests and the bound-log backend tests pass.
- Vet passes for the storage packages, controller, backend, nodehost, web,
  standalone controller and acceptance packages.
- commentcheck -base main is clean across 2,162 files. git diff --check and docs
  mirror checks pass. No local Git identity or core.worktree override was set.

A broader backend selection failed TestStoreBackend_RunsRoundTrip at legacy
CreateRun with `table runs has no column named args_json`. That old run-read
fixture was not converted or suppressed. The changed bound-log behavior passes
its focused test. No whole-repository test suite, pipeline, PostgreSQL, native
placement or independent review is claimed.

## Remaining W7 work and integration points

The area is unfinished. The old store-local output path remains in
pkg/store/node_output.go: outputDir, RecordLocalOutput, writeLocalOutput,
GetNodeOutput, ReadOutputFile and OutputRef-based finish operations. Its callers
remain in the older nodehost scheduler/memo/rehydration and local output routes,
plus StateStoreOld. The legacy Claim.fetchOutput and OutputReadGrant consumers
also remain. The new native host uses committed result selectors and ReadResult,
and its real materialization proof passes, so these cuts have no materialization
blocker. The next continuation must migrate or delete those old callers and
remove their byte APIs. Do not count the area complete while they remain.

W2's DataAuthority, BindData and NewDataClient are implementations in this tree;
the previous report's panic-stub claim is stale. Production construction still
needs the declared authority/access callbacks. The authenticated LogTransport
sign/verify/read routes, SegmentRecorder transport and fresh per-attempt host
bindings remain integration points. W7 uses those interfaces without copying
C2 authorization. WithLogRetention's override map still needs to feed the
store's sealed IntakePolicyFacts; its removed signer-side reader cannot do that.

Expired upload reservations release quota, but the store still exposes no
fenced selector returning expired staging keys for physical deletion. This is
an existing W1b handoff, not a new contract change. No foreign contract shape was
changed here. A1-24/29-37 are consumed through the merged tenant, uploader,
records, blob metadata and binding contracts. A1-43/50 remain W8/W9 composition;
A1-53 native evidence remains T1/lead work.

## Shared caller edits

Beyond imported integration files and the original conflict resolutions, caller
migration changes cmd/sparkwing-controller/main.go; pkg/controller/server.go,
browser_reads.go, log_reads.go, log_retention.go and cache_grant_cut_test.go;
internal/backend/store_backend.go and its test; internal/paths/paths.go;
internal/web/server_test.go and capacity_test.go; and
tests/acceptance/discovery_http_test.go. New controller files are log_objects.go
and log_reads_real_test.go. log_reads_test.go is an imported integration test.
The removed controller files are log_objects_s3.go, log_segments.go,
log_retention_internal_test.go and the obsolete log_segments_internal_test.go.

Source documentation changes are docs/storage.md,
docs/migrations/_unreleased.md, CHANGELOG.md, internal/blob/README.md and
pkg/storage/logseg/README.md. Their published mirrors are regenerated.
The lead owns staging, merge completion, API snapshot regeneration and review.
Scratch is removed at the end. The shared /tmp/sw-gocache is retained.

<!-- design:end -->

STATUS: CONTINUE
