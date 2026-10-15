# W7 storage handoff

<!-- design:begin -->

W7 remains incomplete. The merge contents are resolved, two byte/authority defects
are fixed, and two additional SQLite acceptance boundaries pass. Cross-area
record contracts and authenticated host wiring still block the remaining hard cut.

## Implemented

The first invariant is: an uncommitted upload ticket cannot mint a receipt without
its own verified staged bytes. Filesystem and S3 `CommitObject` now return
`ErrNotFound` for missing staging even when the final digest already exists.
Both still verify existing final content. Before settlement, retries retain and
reverify staging. After settlement, the controller must reauthorize and replay the
persisted receipt without calling physical publication. Cleanup follows settlement.
`ObjectBackend` and C1 document this local precondition. No signature changed.

The second invariant is: a reader without a job attempt can reference only its
server-derived, repository-matching verified cache edges. `AuthorizeEdge` accepts
these `memo_scope` edges for output, manifest, memo and depcache. It requires an
opaque principal/capability, an empty reader attempt and no upload identity.
Binary/source records, owner uploads, other repos, manual refs and job read sets
are refused. The store still resolves live authority and verified-default evidence.

The filesystem/SQLite proof generates 32 payloads, including empty bytes. It
checks physical publication before settlement, independent same-run job receipts,
missing staging, successful independent uploads, unique-byte/object accounting,
and receipt replay after staging cleanup. The S3 proof generates the same payload
sizes against its AWS HTTP boundary. The reader property generates 64 identities
and mutates thirteen authority/edge fields. Its SQLite proof uses the real shared
record rules and refuses records without verified-default evidence.

Removed **7 executable fallback lines**. Preserved deletion of **7 resurrected
legacy files, 2,524 lines on the incoming side**: `internal/cache/cache.go`,
`internal/cache/egress_budget_test.go`, `internal/storagequota/client.go`,
`internal/storagequota/storagequotatest/controller.go`,
`pkg/controller/direct_upload_test.go`, `pkg/controller/storage_counters.go`, and
`pkg/controller/storage_counters_test.go`. The remaining legacy output/log code
has not been retired.

## Merge and amendment actions

All 23 conflicted paths have resolved file contents or the intended deletion.
Contract prose keeps both the W7 byte guarantees and W1 amendments. Retired cache
routes, credentials and accounting code stay removed. The controller API snapshot
was regenerated and embedded docs were synced from their sources.

No Git write ran. The index still reports 23 unmerged paths. The lead must stage
the resolved files and deletions and finish the merge.

| Row | W7 result and remaining integration |
| --- | --- |
| A1-24 | The new boundaries use W1's real upload, receipt, lookup and read-set transactions. C2 binding remains separate. |
| A1-29 | W7 requests restricted lookup, checks returned verified evidence and preserves member metadata. W1/W2 must resolve remote lookup team/default ref. |
| A1-30 | `RemoteCacheOptions.Records` and the read-only memo/depcache adapters remain implemented. The repo-reader edge check now works without a job attempt. |
| A1-31 | Exact tagged upload/receipt checks remain. The new physical precondition closes a receipt-minting path across unrelated jobs. |
| A1-32 | W1 upload admission, commit and receipt replay are exercised with real filesystem publication and SQLite settlement. |
| A1-33 | Filesystem/S3 implement the declared backend. Missing staging cannot be replaced by another ticket's final object. |
| A1-34 | W7 transfers remain behind the declared binding. `DataController.DataAuthority`, `BindData` and `client.NewDataClient` remain panic stubs. W2 must implement settled-ticket replay as described above. |
| A1-35 | W1 persistence is implemented. The record-layout/name conflicts below prevent W7 output/memo writers from using it. |
| A1-36 | `StorageBinding` still appears only in the host declarations. W3/W8 must construct and refresh it per attempt/generation. |
| A1-37 | W7's removed cache transport stays deleted. SDK Cache/lint consumer completion remains W4/W9 work. |
| A1-43 | This continuation changes no engine assets, trust roots, version resolution or release support. |
| A1-50 | W7's remote cache is opt-in and read-only. W9 still owns CLI cache/publication binding. |
| A1-53 | Native placement/backend/OS cells remain skipped pending their drivers. These boundary proofs do not satisfy them. |

## Contract changes needed in other areas

No new cross-area signature is proposed. Three W1 implementation conflicts with
C1 were reproduced through a temporary Go overlay, using W1's fixtures and real
methods. The overlay was not written into the repository.

| Input required by C1/W7 | W1 result |
| --- | --- |
| Output/manifest key with run ID as `Name` and empty `Scope.Key` | `CommitRecord` returns `result key differs from attempt fence`. `ResultBlobKey` instead uses `Name=result` and `Scope.Key=<run>/<attempt>/<generation>`. |
| Memo key with `Name=blob.MemoRecordName` (`result`) | `CommitRecord` returns `memo record must name output or manifest`. W1 memo lookup also searches those other names. |
| Repo-bound restricted lookup with team/default ref left for server resolution | `LookupRecord` returns `digest_not_readable`. The same reader succeeds with a fully specified authorized key after the W7 edge fix. |

The owning files are `pkg/store/blob_record_runtime.go` and
`pkg/store/blob_lookup.go`. W1/W2 must implement C1, or the lead must amend the
shared layouts and migrate W3/W7 writers and readers together. W7 has not silently
changed its keys to match the conflicting implementation.

## Acceptance, ports and checks

`TestStorage` has **19 passing boundary/component checks and 424 skipped native
cells**. This includes nine existing W7 component checks, eight merged W1 SQLite
proofs and these two additions:

- `storage-reference-edge-laundering/filesystem-store`
- `storage-local-remote-cache-opt-in/store-data-sqlite`

New complete T1 native inventory ports: **0**. Existing CAS interrupted-upload,
checksum and reference-edge ports remain, with their physical retry assertions
updated to retain ticket-specific staging. The whole native transport/placement
matrix is still required.

Go commands use `GOWORK=off GOFLAGS=-buildvcs=false GOMAXPROCS=4`, `-p 4`,
`GOCACHE=/tmp/sw-astra-W7/cache`, `GOTMPDIR=/tmp/sw-astra-W7/tmp` and
`TMPDIR=/tmp/sw-astra-W7/testtmp`.

Passed:

- Whole-repository `go build -p 4 ./...` and compile-only `go test -p 4 ./... -run '^$'`.
- Focused vet over `internal/blob/...`, artifacts, depcache, `pkg/storage/...`, and both acceptance packages.
- Full tests for blob/depcache, artifacts, depcache, raw storage, filesystem, S3, logseg and both acceptance packages.
- Controller route guards, API route/spec checks, formatting and `git diff --check`.
- Negative controls: the old filesystem behavior fails the new receipt proof, the old S3 implementation fails the generated property, and the old edge helper fails the verified remote-reader SQLite proof.

`go run ./internal/commentcheck -base main .` still fails only at
`internal/controllerwire/contracts.go:47`, `pkg/store/state_materialize.go:15`,
and `pkg/store/state_result_records.go:9`. None is in W7's edits.

Five merged API snapshots remain stale outside this continuation:
`pkg_controller_client.txt`, `pkg_projectconfig.txt`, `pkg_match.txt`,
`pkg_store.txt`, and `sparkwing.txt`. W7's resolved controller snapshot matches
the generator. No Sparkwing executable, socket service, pipeline or release ran.
Independent review and native S3/Postgres/placement checks remain with the lead.
Scratch is removed at the end.

## Shared files and remaining work

Merge resolution touched `.apidiff/pkg_controller.txt`, `api/openapi.yaml`,
`design/rewrite/contracts/c1-data.md`, `internal/orchestrator/wingd_api_routes_test.go`,
`pkg/controller/server.go`, `pkg/controller/route_scope_guard_test.go`, the seven
deletions listed above, and the source/mirror pairs for `api-reference.md`,
`auth.md`, `migrations/_unreleased.md`, `observability.md`, and `security.md`.

New shared acceptance files are `internal/acceptance/blob_upload_isolation.go`,
`internal/acceptance/blob_remote_edges.go`, `tests/acceptance/storage_test.go`, and
`tests/acceptance/storage_filesystem_test.go`. Source docs are `docs/storage.md`
and `CHANGELOG.md`, with their generated embedded copies. Other edits are in
`internal/blob` and this handoff. No out-of-area runtime caller was migrated.

Remaining real integration includes W2's authenticated byte/record/log adapters,
W1's conflicting record keys and names, W3/W8 storage/log binding construction,
W4/W9 SDK/CLI consumers, and T1's native harness. Once these bind to the final
contracts, retire `pkg/controller/node_output.go`, `output_objects_s3.go`,
`output_download_s3.go`, `log_segments.go`, `client/output.go`,
`client/logsegments.go`, `internal/orchestrator/segment_logs.go`, their legacy
tests, and the remaining raw artifact/log backends.

Open product questions: none. Friction is the cross-area contract drift, the
unimplemented authenticated bindings, the three existing comment violations,
five stale merged API snapshots and the merge index awaiting the lead. No new
limit, setting, credential, environment variable or compatibility path was added.

<!-- design:end -->

STATUS: CONTINUE
