# W7 storage handoff

<!-- design:begin -->

W7 remains incomplete. This continuation closes dependency-cache receipt and
record validation gaps and prevents output/cache record commits after upload
cancellation.
The W7 byte, artifact, memo, remote-cache, binary, source and tagged-log
components remain implemented. Legacy output/log runtime retirement and real
cross-area bindings remain open.

## Latest continuation

- Dependency-cache saves check uploader authority before reading workspace
  files. They require an exact receipt digest and uploader, then check the
  committed record's key, digest and source identity. A mismatch returns
  ErrIntegrity and zero saved bytes. A bad receipt never reaches record commit.
- Output and dependency-cache writes check cancellation after byte upload and
  before record commit. A cancelled upload cannot report a successful save.
- Four new tests contain 11 boundary cases. Ten cases failed before the fix.
  The owner-unit refusal case already passed and remains a regression check.
  Tests use real archive files and fake byte/record interfaces, including the
  declared signed store for the output cancellation case.
- Whole-repository build, scoped storage vet and the complete blob, artifact,
  depcache, logseg and acceptance package suites pass. Commands use GOWORK=off,
  GOFLAGS=-buildvcs=false, GOMAXPROCS=4 and -p 4, with GOCACHE, GOTMPDIR and
  TMPDIR under /tmp/sw-astra-W7.
- Resolved W7 lint findings. Archive spools and filesystem upload temporary
  files now return cleanup errors. Contract comment and test-format changes
  preserve signatures and behavior. The HTTP response-abort sentinel remains
  because net/http catches it and closes an incomplete response.
  Lint over internal/blob/... reports 0 issues. Scoped commentcheck and
  git diff --check pass. Scratch and the worktree-local lint cache are removed
  after checks. Race, native and pipeline checks remain with the lead.
- This continuation deletes **0 legacy files / 0 legacy lines** and completes
  **0 additional T1 inventory ports or native acceptance cells**. Native drivers
  remain unimplemented. TestStorage has 0 native passes, 424 skips and 0 failures.
  Its repository-identity component passes separately. The earlier deletion
  totals and amendment actions below still apply.
- Changed internal/blob/{contracts.go,filesystem.go,filesystem_test.go,
  http_test.go,log_lifecycle_test.go,log_retention.go,memo_test.go,
  object_commit_test.go,output.go,output_test.go,s3.go,s3_logs.go,s3_test.go,
  store_test.go,README.md}, internal/blob/depcache/{data.go,data_test.go,
  save_test.go} and this report. No shared file, other-area caller or contract
  signature changed. No mechanism, setting, limit, service, environment variable
  or compatibility surface was added.

All four unmerged files were marker-free at entry and retain the amended
signatures and W7 behavior. They remain unmerged in the index because the brief
forbids Git writes. The lead must stage them and finish the merge.

Real integration still requires W1's transactional upload/record/read-set/memo
and retention implementation, W2's DataController/NewDataClient and tagged
LogTransport adapters, W3/W8's per-attempt storage/log bindings and W4/W9's
SDK/CLI consumers. Components build against these declared interfaces and test
with fakes. Removing the live legacy output and individual-log paths also needs
those host/controller callers migrated. No new contract change or product
question is requested. The lead retains independent review, pipelines, native
acceptance, staging and landing.

## Previous artifact continuation

- Stage now applies the existing caller-supplied budget through record lookup,
  manifest/member downloads and file publication. Missing or expired budgets
  fail with ErrBudget. Cancellation before lookup, during lookup, and during
  manifest/member close preserves the destination. Stream close and temporary
  file cleanup errors remain errors.
- Capture checks upload authority before reading workspace files. Every file
  and manifest receipt must match the expected digest and exact uploader.
  Returned manifest records must match the requested key, digest and source.
  Cancellation after upload prevents the subsequent record commit.
- Three new tests add 14 cases. All failed before the production fix and pass
  afterward. They use real workspace files and fake byte/record interfaces.
  They do not claim transaction, HTTP or placement coverage.
- Removed 139 lines of retired package-proxy instructions from build-caching.md
  and 139 from its embedded mirror. Removed 12 stale cache-server lint entries
  and one binary ignore entry. **Legacy Go runtime deleted: 0 lines.**
- No complete T1 inventory port was added. Native TestStorage remains at
  **0 native passes / 424 skips / 0 failures**. Its repository-identity component
  passes separately. T1 New and Run are still panic stubs.
- No contract signature, customer limit, setting, environment variable, service,
  compatibility API or other production mechanism was added. Stage uses the
  Budget already required by its storage binding.

Files changed in this continuation:

- internal/artifacts/{capture.go,stage.go,contract_test.go,boundaries_test.go}
- internal/blob/README.md
- .gitignore and .golangci.yml
- docs/build-caching.md and pkg/docs/mirror/build-caching.md
- design/rewrite/contracts/w7-storage-report.md

The four config/document paths outside W7 are minimal retirement changes.
No caller in another package needed migration. All earlier incoming and W7
changes remain in place.

Verification used GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4 and -p 4,
with GOCACHE, GOTMPDIR and TMPDIR under /tmp/sw-astra-W7. Whole-repository build,
scoped vet, full storage suites, artifact race tests, focused docs mirror checks
and scoped commentcheck passed. Focused artifact lint passes with 0 issues after
fixing its cleanup-error, EOF-comparison and formatting findings. The lead retains
pipeline checks, independent review, staging, merge completion and landing.

No other-area contract change is requested. Real integration still needs W1's
upload/record/log transactions, W2's DataController/NewDataClient and LogTransport,
W3/W8's refreshed storage/log bindings and W4/W9's SDK/CLI consumers. These are
existing declared interfaces. The components test against those interfaces.
Removing the remaining output and individual-log runtime paths also requires
cutting their live host/controller callers. W7 has not replaced those callers
with a compatibility adapter or silently removed their behavior.

The merge's four files remain marker-free with amended signatures and W7
behavior. The index still needs lead staging because the swarm forbids Git writes.
No new product question was added. Scratch and the artifact lint cache are
removed after the final checks.

## Previous archive continuation

- Archive extraction now consumes the gzip trailer before publishing. Damaged
  CRC, damaged length and a truncated trailer each published files before the
  fix. Each now fails while preserving the destination and removing staging.
  A signed blob with a valid SHA-256 but an invalid gzip trailer also fails.
- ExtractArchiveStaged now takes context.Context. Cancellation during extraction
  preserves the destination. Its two production callers and existing test were
  migrated together. This is a W7-local contract change, with no alias.
- A failed dependency-cache restore now counts as a miss, so successful work can
  replace the failed archive. The new local-file regression failed before this
  change and now verifies a subsequent restore reads the repaired content.
- Signed uploads check cancellation before signing, before transfer and after
  transfer. None of those cancelled cases reaches ticket commit. Download
  verification retains the caller's context and refuses a cancelled EOF. The
  new upload and download regressions failed before these changes.
- The bound dependency-cache probe now rechecks cancellation after record lookup.
  Its fake transport previously returned a successful hit after cancelling the
  caller. That regression now passes. Failed download setup also preserves a
  stream-close error alongside the transfer error.
- No legacy file was deleted in this continuation: **0 files / 0 legacy lines**.
  The earlier 16-file / 4,147-line retirement is retained. No complete T1 inventory
  port was added. New regressions exercise the final contracts and real archives
  or local files, with fake controller/record transports where needed.
- No other area's contract change or product question was added. No new setting,
  limit, service, compatibility API or background mechanism was added.

Files changed in this continuation:

- internal/blob/{store.go,store_cancellation_test.go,objects.go,filesystem.go,README.md}
- internal/blob/depcache/{data.go,data_test.go}
- internal/depcache/{archive.go,archive_integrity_test.go,backend.go,depcache.go,depcache_test.go}
- design/rewrite/contracts/w7-storage-report.md

All are W7-owned files. No shared file or external package caller changed in this
continuation. Earlier shared changes already present at entry remain below.

Verification uses GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4 and -p 4,
with GOCACHE, GOTMPDIR and TMPDIR under /tmp/sw-astra-W7. Passed:

- Whole-repository go build -p 4 ./....
- go vet -p 4 for internal/blob/..., internal/artifacts, internal/depcache and
  pkg/storage/logseg.
- Full go test -p 4 suites for those packages and tests/acceptance.
- go test -p 4 -race for internal/blob/... and internal/depcache.
- Formatting, scoped commentcheck and git diff --check.

The build, vet and race results cover the final source. Native HTTP/api.sock,
Postgres, S3 service and placement acceptance, repository gates and independent
review remain with the lead.

TestStorage still has **424 skipped native cells and 0 native passes**. Its
existing storage-repository-identity/runspec-config component passes separately.
The new fake-backed regressions are not native acceptance evidence. The whole
acceptance package passed with those skips retained. The T1 native harness is
still a panic stub.

Commentcheck passes for both changed package trees. Sleepcheck from the repository
root with -base HEAD still reports only the two incoming
internal/limits/deadline_test.go failures. It reports no new W7 wait.
The lead retains repository pipelines, review, staging, commits and landing.

## Merge resolution

All four unmerged paths already contained resolved, marker-free contents when
this continuation started. They preserve the amended signatures and W7 behavior:

- internal/agentconfig/agentconfig.go and agentconfig_test.go retain the private
  credential directory, runner/repos spellings and removed gitcache setting.
- internal/blob/contracts.go retains Authority.Upload, VerifiedDefaultOnly and
  RemoteCacheOptions.Records. object_contract.go is the single ObjectBackend.
- internal/orchestrator/pod_node_host_test.go retains the implemented binary
  binding signature and its bincache import.

The index still lists these four paths as unmerged. The lead must stage them
and finish the merge because the worker brief prohibits Git writes.

## Previous implementation and deletion

- Deleted swcg minting, verification, wire types, credential name and grant TTLs.
- Deleted cache-grant issuance and the old data upload, commit, download and
  capability-probe handlers. Removed their route registrations, claim allowance,
  tenant inventory entry and test exports.
- Removed the controller's grant credential loading and the e2e deployment's
  unused cache grant Secret. Services and discovery no longer carry
  data_download_url or direct_data for those removed routes.
- The team-deletion recheck now follows the remaining signed-log URL lifetime
  plus its existing one-hour margin, rather than the deleted six-hour grant.
- Retired **16 Go files containing 4,147 lines**. Of that code, **227 lines** of
  S3 output/log helpers remain in output_objects_s3.go and output_download_s3.go.
  They still have real callers in the existing output and job-log handlers.
  The team build-trust test was moved out of the deleted upload-test file.
- Deleted stale HTTP artifact-store/cache-grant tests. The scheme test now
  rejects http and https along with ftp. Signed transfers remain covered through
  blob.TransferClient with injected transports.
- Migrated the trigger-loop concurrency test's blocking request from the removed
  grant route to run finish, and removed the retired local-controller route entry.
- Updated current source docs, migration guidance, changelog, generated API
  inventory/spec, embedded mirrors and the controller API snapshot. Historical
  migration documents and signed release assets were left intact.

Previous continuations removed the cache service, package proxy, shared Git
mirrors, cache accounting client/routes, AttemptPrefix and flat log identities.
The final byte and tagged-log components continue to build against the amended
interfaces, with fakes where the owning area's adapter remains a stub.

## Amendment actions

| Row | W7 result and remaining real integration |
| --- | --- |
| A1-24 | Components use declared authority/read-edge seams. W1 owns transaction-time resolution and W2 binds authentication. |
| A1-29 | Restricted lookup uses VerifiedDefaultOnly and authorized Members. W1 resolves sealed default-ref facts and read sets. |
| A1-30 | RemoteCacheOptions.Records supplies logical lookup before verified reads. NewRemoteReader now restores dependency archives through that read-only interface. Opt-in, scope and provenance have component tests. W2/W9 bind transport. |
| A1-31 | Authority.Upload binds exact owner and publication identities. Unrelated-job receipts and altered generations fail component checks. Dependency-cache saves also check the exact receipt and refuse mismatched authority before reading files. W1/W2 recheck live authority. |
| A1-32 | Both physical backends implement upload publication. W1 owns reservation, settlement and abort. Physical success alone returns no receipt. |
| A1-33 | Filesystem and S3 implement the single ObjectBackend and verify stored bytes, including an existing final object. S3 fences its copy to the verified source ETag. |
| A1-34 | Components use DataSigner/RecordStore. Old grant handlers and credentials are now deleted. DataController.DataAuthority, BindData and NewDataClient remain W2 stubs. |
| A1-35 | Memo now also refuses a found copy with no output digest. Explicit empty output remains valid. Artifact and depcache components enforce their failure policies. W1 owns transactional edges, memo copies, quota, pins and retention. |
| A1-36 | Components accept bound byte/record clients. W3/W8 supply and refresh StorageBinding for each attempt/generation. |
| A1-37 | Removed legacy HTTP artifact-store tests. W4 still owns final SDK Cache helpers and public workdir-safe keys. The repository lint consumer must use that public surface. |
| A1-43 | Historical signed release assets are untouched. |
| A1-50 | The dependency-archive read-through adapter keeps saves local. W3/W9 must install it from the selected read-only profile. Publication wiring remains with W9. |
| A1-53 | No fake-backed component result was counted as native acceptance. T1/lead must connect real drivers. |

## Previous verification

Go commands used GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4 and -p 4.
GOCACHE, GOTMPDIR and TMPDIR were under /tmp/sw-astra-W7.

Passed:

- Whole-root-module go build ./... before and after the cut.
- Whole-root-module test compilation with go test ./... -run '^$'.
- Scoped vet for blob, artifacts, depcache, logseg, controller/client, controller
  command, discovery, cluster, storeurl and orchestrator.
- Full suites for internal/blob/..., internal/artifacts, internal/depcache,
  pkg/storage/logseg and tests/acceptance.
- Controller grant/counter removal, service-announcement, route-guard and tenant
  route checks. The grant-removal test failed before the production cut.
- Controller credential tests, internal/credentials, apiref and apispec suites.
- Focused storeurl scheme/filesystem and embedded-document tests.
- API generation, all three affected API snapshot comparisons, docs mirror drift,
  shell syntax, formatting and git diff --check.

TestCommitAfterInterruptedUpload in internal/blob/store_test.go carries the
physical portion of the old interrupted-copy regression onto the final contract.
It uses a real filesystem backend and TransferClient with a fake controller
signer. A physically committed object with failed settlement yields no receipt.
A fresh uploader/ticket can retry it after the old pending object is removed.
Corrupt final bytes fail verification and yield no new receipt. The existing S3
matrix also rejects corrupt staging/final bytes and failed closes.

The original interrupted-copy and checksum-mismatch inventory cases also require
real reservation expiry, quota and record/provenance transactions. Those remain
open under W1/W2. **New complete T1 inventory ports: 0.**

Native storage cells remain **0 passing, 424 skipped, 0 failing**. W6's
repository-identity component check passes separately. Scenario parent passes
with skipped children are not native evidence and were not counted.

The broader storeurl and docs suites could not finish because their existing
httptest listeners are prohibited by the sandbox. Focused checks above avoid
listeners. The changed trigger-loop test was compiled, not executed.

Source-policy checks still report the incoming failures outside W7:

- commentcheck: detached HereBearer comment in internal/controllerwire/contracts.go.
- sleepcheck: two wall-clock deadline assertions in internal/limits/deadline_test.go.

## Remaining implementation and integration

1. W1: real upload reservation/commit/abort, existence/receipts, read sets, memo
   copies, unique-byte quota, newest-success pins and reachability retention.
2. W2: authenticated DataController and NewDataClient adapters, record/data route
   binding and transactional authorization. Tagged logs need batch signing,
   VerifyUpload, current-writer checks, record commit and exact egress accounting.
3. W3/W8: runtime binding refresh for outputs, artifacts, memo, depcache, binary,
   source and logs. Owner-unit subjects must reach plan/eval logs throughout.
4. W4/W9: public Cache helper/lint consumer and CLI cache/publication bindings.
5. W7: replace the remaining legacy node-output and individual-segment
   signing/sealing/live-ring paths. Remaining files include node_output.go,
   log_segments.go, client/output.go, client/logsegments.go,
   orchestrator/segment_logs.go and the retained S3 helpers. Their host/controller
   callers still need the final byte/record and tagged-log bindings.
6. T1/lead: real HTTP/api.sock/store/backend/OS acceptance and repository QA.

No new product question or other-area contract change is requested. The previous
local W7 changes to logseg.Manifest and Options remain documented in its README.
No new production mechanism, setting, limit or compatibility adapter was added.

## Shared files touched in the previous continuation

Caller, setup and route changes outside the blob package:

- cmd/sparkwing-controller/{main.go,credentials.go,keys_test.go}
- internal/discovery/discovery.go
- internal/cluster/trigger_loop_test.go
- internal/orchestrator/wingd_api_routes_test.go
- pkg/controller/{server.go,runner_claim.go,tenant_routes.go,services.go,
  deletion.go,github_app_webhook.go,export_test.go,claim_source_test.go,
  tenant_scope_internal_test.go,route_scope_guard_test.go}
- pkg/controller/{output_objects_s3.go,output_download_s3.go,build_trust_test.go,
  cache_grant_cut_test.go}, plus the retired grant/data implementation/tests
- pkg/storage/storeurl/storeurl_test.go
- bin/k8s-e2e.sh

Source/generated prose changes are CHANGELOG.md, docs/api.md, auth.md,
data-uploads.md, github-actions-runners.md, security.md, self-hosting.md,
threat-model.md, migrations/_unreleased.md, docs/api-reference.md,
api/openapi.yaml, their embedded mirrors and .apidiff/pkg_controller.txt.
Earlier shared changes already present at entry were retained, not redone.

Friction is the unmerged index under the no-Git-write rule, prohibited listeners,
incoming source-policy failures and the unimplemented cross-area adapters.
No candidate binary, pipeline, service, deployment or release was run.
The lead retains review, pipelines, staging, commits and landing. Scratch is
removed after checks finish.

<!-- design:end -->

STATUS: CONTINUE
