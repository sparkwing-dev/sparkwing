# W7 storage handoff

<!-- design:begin -->

W7 remains incomplete. Earlier sessions implemented the signed byte backends,
artifacts, memo, depcache, binary/source transfers and tagged-log components.
This continuation removes another raw storage API and resolves the integration
merge's file contents. Legacy output/log retirement and native integration remain.

## This continuation

Resolved both conflicted files. `api/openapi.yaml` drops the retired orphan
reconciliation and run gitcache routes. `pkg/controller/doc.go` drops the retired
reconcile-hook reference without adding another laptop wiring claim. The amended
contracts remain intact. No Git write ran, so the index still lists these files
as unmerged. The lead must stage their resolved contents.

Removed `ArtifactStore.Put` and `Has`, their filesystem/S3 implementations, and
the unused `HeadObject`/`DeleteObjects` requirements from the raw S3 API. Raw
writers now use the existing error-only `PutIfAbsent`. No aliases remain.
The `pkg/storage` diff removes **194 lines** and adds 88, including fixture and
conformance changes. These are gross counts against the integration index.
The raw log/measurement backend still exists for its surviving consumers.

The invariant is: once a signed log key contains bytes, later requests cannot
replace them. The remaining filesystem log route creates the key once, verifies
an existing object's exact bytes and successful close for a retry, and returns
409 for a replacement. A generated controller property checks 32 payload pairs
with separately valid signed requests. An overlay that deletes before writing
fails on the first generated pair. The overlay never changed repository source.

The raw conformance suite now checks refusal of replacement rather than mutable
last-writer behavior. Fixtures use create-only writes and reads instead of the
removed APIs. The corruption fixture damages its real file directly. The S3
fake uses an in-process HTTP client, so the complete S3 package suite runs without
opening a socket. Its endpoint tests still exercise the AWS endpoint resolver.

## Amendment audit

| Row | W7 implementation and remaining integration |
| --- | --- |
| A1-29 | Remote memo/depcache clients request `VerifiedDefaultOnly`, validate returned scope/evidence and retain authorized member metadata. W1 must supply live lookup/read edges. |
| A1-30 | `RemoteCacheOptions.Records` supplies logical record lookups. Remote access remains opt-in, verified-default-only and read-only, with no binary fetch. Component properties pass. |
| A1-31 | W7 upload/edge helpers compare exact tagged uploader identity and owner/publication fences. W2 derives `Authority.Upload`; W1 rechecks it transactionally. Both remain integration points. |
| A1-33 | Filesystem/S3 implement `ObjectBackend` signing, verified immutable publication and deletion. Existing final bytes verify too. Physical commits grant no receipt or record. Backend suites pass with fakes at the service boundary. |

Consumer rows A1-24/32/34/35/36/37/43/50/53 retain the declared interfaces. Their
store, authorization, host, SDK, CLI and native-driver work remains with those
owners. W7 tests use contract fakes for those boundaries. This continuation
changes no final contract in another area and requests no product decision.

## Acceptance and ports

`TestStorage` reports **9 passing W7 storage component checks, 424 skipped native
cells and 0 failures**. No native cell was unskipped. The passing components cover
CAS integrity, reference-edge laundering, memo/depcache scope, binary provenance,
log segments, remote-cache opt-in, memo failures, owner receipts/stream checks and
bounded large presence queries.

New complete T1 inventory ports: **0**. Earlier ports remain in the existing
component tests. The new property closes the raw signed-log replacement path,
but does not establish controller authorization, store transactions or native
placement coverage.

## Verification

Go commands use `GOWORK=off GOFLAGS=-buildvcs=false GOMAXPROCS=4`, `-p 4`,
`GOCACHE=/tmp/sw-astra-W7/cache`, `GOTMPDIR=/tmp/sw-astra-W7/tmp` and
`TMPDIR=/tmp/sw-astra-W7/testtmp`.

Passed:

- Whole-repository `go build -p 4 ./...` and compile-only `go test -p 4 ./... -run '^$'`.
- `go vet -p 4 ./pkg/storage/... ./internal/blob/... ./internal/artifacts
  ./internal/depcache ./pkg/controller/... ./tests/acceptance ./internal/acceptance`.
- Full tests for `pkg/storage`, `pkg/storage/fs`, `pkg/storage/s3`,
  `pkg/storage/logseg`, `internal/blob/...`, `internal/artifacts`,
  `internal/depcache`, `tests/acceptance` and `internal/acceptance`.
- The generated controller property, focused bucket-usage tests, non-socket
  storeurl tests and API route/spec tests.
- The replacement-write negative control fails at its intended assertion.
- Formatting, scoped sleepcheck, `git diff --check`, storage API snapshots and
  embedded-doc sync.

`go run ./internal/commentcheck -base main .` reports only three comments outside
this W7 diff: `internal/controllerwire/contracts.go:47`,
`pkg/store/state_materialize.go:15` and `pkg/store/state_result_records.go:9`.
The HEAD-scoped run reports the two merged W1 comments. No W7 comment is flagged.

The full legacy controller and storeurl socket suites, real S3/Postgres, native
placements and pipelines remain for the lead. No Sparkwing executable, service,
pipeline, Git write or release ran. Scratch is deleted at the end.

## Remaining implementation and real integration

1. W1: `Tenant` record and upload lifecycle methods remain panic stubs in
   `blob_records.go` and `upload_contract.go`. Implement exact receipts/read sets,
   atomic memo copies, unique-byte quotas, newest-success pins and reachability GC.
2. W2: `DataController.DataAuthority`, `BindData` and `client.NewDataClient`
   remain panic stubs. Bind authenticated data/records, tagged-log signing,
   uncertain-upload verification, writer fencing, commits, proxy reads and metering.
3. W3/W8: refresh `StorageBinding` and log bindings per attempt/generation, attach
   output records and artifact/cache/source clients, and carry trigger/plan/eval
   log subjects through execution. The declared storage binding remains unused by
   the old runtime.
4. W4/W9: finish SDK Cache/lint and CLI cache/publication consumers. Preserve the
   engine release contracts and regenerate their final surface references.
5. W7: retire `pkg/controller/node_output.go`, `output_objects_s3.go`,
   `output_download_s3.go`, `log_segments.go`, `client/output.go`,
   `client/logsegments.go`, `internal/orchestrator/segment_logs.go`, their legacy
   tests and the remaining raw artifact/log backends as those consumers bind to
   the final contracts. Removing these now also requires the listed host and
   authorization integrations, rather than a replacement compatibility path.
6. T1/lead: implement `internal/acceptance.New`/`Run` and supply native
   HTTP/api.sock, SQLite/Postgres, filesystem/S3, OS/placement and real-repository QA.

## Shared files touched

- `api/openapi.yaml` and `pkg/controller/doc.go`: merge content resolution.
- `pkg/controller/log_segments.go` and its existing/internal property tests:
  create-only caller and corruption-fixture migration.
- `pkg/controller/bucket_ceiling_internal_test.go` and `deletion_review_test.go`:
  minimal removed-API caller migrations.
- Five `.apidiff/pkg_storage*.txt` snapshots, `VERSIONING.md`, `CHANGELOG.md`,
  the migration page and generated embedded copies: describe the removed APIs.
- `internal/blob/README.md` and this handoff: measured component and integration boundaries.

Other edits are in `pkg/storage`. No other package's contract changed. Friction
is the unimplemented native/authenticated wiring, the three out-of-area comment
violations and the merge index awaiting the lead. No new setting, limit,
environment variable, production interface or compatibility wrapper was added.

<!-- design:end -->

STATUS: CONTINUE
