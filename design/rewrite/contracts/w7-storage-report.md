# W7 storage handoff

<!-- design:begin -->

W7 remains incomplete. All 20 merge-conflicted paths have resolved file contents.
No conflict markers remain. The brief prohibits Git writes, so the index still
lists those paths as unmerged. The lead must stage the resolutions and finish
the merge. Root and pipeline-module builds pass.

## Implemented in this continuation

Invariant: dependency-cache keys use only files inside the workspace, and SDK
and host derive the same key from the declaration, platform and file content.

`internal/depcachekey.Files` now owns both calculations. It preserves W4's public
key format and uses `os.Root` for reads. The host no longer follows absolute,
parent-relative or escaping-symlink key inputs. Invalid inputs run cold. The host
accepts the protocol's `go-modules` resolver. There is no `gomod` alias or old-key
fallback. Local archives with old keys become cold misses.

Invariant: dependency-cache writes use the binding current after execution,
and a refused binding cannot fall back to an ambient local cache.

The executor cache layer now constructs its signed-data backend from the same
callback as artifacts and memo. It obtains a fresh binding after successful
execution, before saving, because retries can change the attempt ordinal.
Restore or save binding refusal keeps the job result and skips the cache work.
The generated test preloads an ambient local archive, varies attempt/generation
and refuses each binding boundary. It checks that no ambient restore or stale
receipt/record write succeeds. Final production host construction remains W3/W8.

Merge resolutions preserve W7's authenticated records, memo materialization and
prior deletions, and adopt W4's JobSpec, Artifacts and Memoized shapes. Legacy
controller direct uploads, cache-grant tests and memo-unix tests remain deleted.
Both direct Git-clone test sets remain. W4's docs win over the old protocol and
SDK descriptions, and the embedded copies are regenerated from source.

Deleted **50 lines** across the obsolete Segment, deriveKey and firstExisting
helpers, plus their unused pattern and old restore path. No additional whole
legacy subsystem is removed in this continuation. Prior cache-service, proxy,
grant and source-mirror deletions remain intact.

## Acceptance and verification

Three added component cells pass under `TestStorage/storage-components`:

- `dependency-key-contract`: 64 generated lockfiles across relocated workspaces,
  compared through the public SDK and real host restore entry.
- `dependency-key-containment`: 64 generated inputs with absolute, parent-relative
  and, on Unix, escaping-symlink paths. No backend lookup may occur.
- `go-modules-resolver`: the protocol resolver selects the configured Go module
  directory.

The key, containment, resolver and binding tests all failed before their fixes.
The cache-binding property passes 64 generated cases.

`TestStorage` passes **45 leaf checks**, with **64 passing test events** including
parent groups. **424 native cells remain skipped.** New completed native T1
inventory ports: **0**. The retained memo artifact/refusal coverage compiles and
passes with protocol v2. The earlier manifest-reader, upload, collection and
record-boundary proofs remain passing.

Checks use `GOWORK=off GOFLAGS=-buildvcs=false GOMAXPROCS=4`, `-p 4`,
`GOCACHE=/tmp/sw-astra-W7/cache`, `GOTMPDIR=/tmp/sw-astra-W7/gotmp` and
`TMPDIR=/tmp/sw-astra-W7/tmp`.

Passed:

- Root `go build -p 4 ./...`, `.sparkwing` module build and compile-only
  tests for all 172 root packages.
- Full tests for blob, blob/depcache, dependency caches, artifacts and logseg.
- Focused nodehost artifact, memo materialization and cache tests.
- Focused direct clone tests retaining both branches' coverage.
- `TestStorage`, focused docs tests and generated-document drift check.
- Vet for blob/depcache, artifacts, nodehost, acceptance, SDK/Git, storage and
  controller/client packages.
- `go run ./internal/commentcheck -base main .` is clean across 1081 files.
- Formatting and `git diff --check`.

The full Git-package test command has three failures outside the changed clone
surface: TestFilesetHashFilesystemFallback, TestFilesetHashFramesFileContents and
TestFilesetHashIncludesPermissions. Their filesystem fallback discovers the
sandbox's synthetic `/tmp/.git` and invokes Git on a directory that is not a
repository. The clone tests pass separately.

The retained `TestHostedRunEmitsTheDispatchersEvents/local` now compiles but fails
with `no such table: sparkwing_meta` while using the legacy state path. W3 owns
that migration. Its controller subtest needs a socket and was not run here.

## Amendment and handoff ledger

Each row records W7's component work and the remaining real integration.

| Entry | W7 result | Integration left |
| --- | --- | --- |
| A1-24 | Byte/manifest adapters implement the transactional owner seams. | W2 must bind W1's live data checker and the verified manifest reader. |
| A1-29 | Restricted cache reads retain verified-default checks and member metadata. | W1 must resolve repo-reader team/default-ref selectors for the W7 remote client. |
| A1-30 | `RemoteCacheOptions.Records` and logical memo/depcache reads are implemented. | W2/W9 must construct the authenticated transports/profile binding. |
| A1-31 | Exact job, owner-unit and publication upload identities are checked. Same-run receipts do not transfer. | W2 must derive the identity from authentication on each call. |
| A1-32 | Physical upload/commit/deletion and reservation/receipt boundary proofs are implemented. | W2 must bind reservation, reauthorization, physical commit, settlement and replay. |
| A1-33 | Filesystem/S3 implement `ObjectBackend`, including collection and verified manifest reads. | W2 must install the backend and manifest adapter. |
| A1-34 | Signed filesystem/S3 transfers are implemented and tested without sockets. | `DataController.DataAuthority`, `BindData` and `client.NewDataClient` remain stubs. |
| A1-35 | Output, memo, depcache, staging and collector components are implemented. | W1's output/manifest keys and memo layout still conflict with C1/W7. |
| A1-36 | Artifact, memo and depcache layers use common byte/record clients. Depcache now binds before restore and refreshes after execution before save. Refusal runs cold. | W3/W8 must construct the final StorageBinding and connect its fresh authority to the existing layer callback. |
| A1-37 | W4 public Cache helpers are merged. SDK and host now share workspace-contained key calculation; host accepts go-modules. Removed lint-cache transport stays deleted. | W3/W8 still own final production host construction. |
| A1-43 | Cache deletion leaves engine/releaseasset owners and historical-matrix declarations in place. | W8's version-aware asset functions remain stubs. |
| A1-50 | Read-only remote-cache/depcache adapters and source/binary byte components are implemented. | W9 owns CLI/profile/publish/engine binding. |
| A1-53 | Four additional physical/store boundary proofs pass. | T1/lead owns authenticated/native drivers and real-repository QA. |

No new W7 assignment appeared in handoffs.md. The verified-manifest and C1
layout handoffs remain open for W2/W1. Updated W3's retained-events handoff with
the post-merge runtime failure. No cross-area contract shape was changed.

## Remaining work and ownership

- W1 must align output/manifest keys with C1. W7 uses the run ID as Name and an
  empty Scope.Key. ResultBlobKey still uses Name=result and a run/attempt/generation
  key. W1 memo writes/lookups still use output and manifest instead of the agreed
  result record with optional manifest/members. Memo output caps follow that layout.
- W1/W2 must resolve tenant and verified-default-ref selectors for repo-bound
  remote reads. The W7 remote reader supplies neither as caller authority.
- W2 must implement DataAuthority, BindData and client.NewDataClient, install
  storage.BlobManifests, and bind upload admission, physical verification,
  settlement, reauthorization and receipt replay.
- W2 must bind tagged logs, upload admission for future S3 slots, uncertain-PUT
  verification, persisted segments, proxied reads and egress accounting.
- The maintenance owner must bind the physical backend to leased reachability
  collection and filesystem log expiry.
- W3/W8 must construct final storage/log bindings per attempt/generation.
  W9 still owns cache-profile opt-in and publication/CLI binding.
- T1/lead must exercise authenticated HTTP/api.sock, native S3/Postgres,
  placement, native OS and real-repository drivers.

Old output/log deletion remains: pkg/controller/node_output.go,
output_objects_s3.go, output_download_s3.go, log_segments.go, client/output.go,
client/logsegments.go, internal/orchestrator/segment_logs.go, their tests and
remaining raw artifact/log backends. Their consumers still need final bindings.

## Shared files and friction

All conflicted shared files were resolved as requested:

- .sparkwing/jobs/lint_contention.go
- design/rewrite/contracts/handoffs.md
- docs/node-protocol.md and docs/sdk.md, with embedded mirrors
- internal/cluster/runner_pool_cli.go
- internal/nodehost/artifacts_test.go, events_test.go, memo.go,
  memo_artifacts_test.go, memo_unix_test.go, podnode_test.go and scheduler.go
- internal/orchestrator/envelope_coverage_test.go
- pkg/controller/direct_upload.go and cache_grant_scope_internal_test.go
- sparkwing/depcache.go and sparkwing/git/clone.go, clone_test.go

Additional shared edits are internal/nodehost/caches.go and its binding test,
SDK key delegation, the acceptance key checks/registration, storage docs,
CHANGELOG.md and generated embedded copies. Other changes are W7's key helper,
dependency-cache implementation/tests and blob README. There are no new
out-of-area runtime API removals beyond preserving the merge's existing cuts.

The environment is the supplied Xwing 1084 worktree. `xwing env context` cannot
acquire its sessions-index lock in this sandbox. No Git write, candidate install,
pipeline or service ran. Scratch is removed before the final response. No new
contract request or product question remains. The unresolved authenticated/host
bindings, legacy output/log migration and native cells prevent completion.

<!-- design:end -->

STATUS: CONTINUE
