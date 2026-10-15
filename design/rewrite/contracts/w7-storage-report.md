# W7 storage handoff

<!-- design:begin -->

W7 remains incomplete. Physical collection now connects to the shared store
contract. Authenticated runtime binding, the legacy output/log cut and native
acceptance remain.

## Implemented in this continuation

Invariant: reachable blobs survive collection regardless of age, and newly
unreachable blobs survive the full tombstone grace period.

Filesystem and S3 implement W1's existing `BlobDeleter` through `DeleteBlob`.
The backend derives the tenant CAS key from the blob selected by the leased
collector. Missing objects succeed on replay. Reachability and the 24-hour
grace period stay in the store.

The generated proof uses twelve graph pairs with sixteen objects per tenant.
Tenants store identical digests under independent reference graphs. Cases cover
empty bytes, old referenced objects, reachable and unreachable cycles, references
added after marking, grace and removal of the last reference. An independent
graph walk determines which physical bytes remain. Interruptions before and
after physical deletion preserve inventory for replay.

Both proofs use real SQLite collection transactions and seeded reference graphs.
The filesystem proof uses signed HTTP handlers and real files. The S3 proof uses
the real presigner, transfer client and AWS SDK with a fake object service.
Controller authentication, record admission, maintenance scheduling and native
S3 remain separate integrations.

Bucket measurement counts objects without deleting them. The retained helper
now exposes only measurement, key construction and team validation. Removed its
unused cache uploads, multipart transfers, reads, deletes, prefix/directory
listing, write breaker and mutable byte-delta accounting. Its client interface
permits only S3 listing. This removes the mutation class from measurement.

Retired bucket/pruning production code is **879 lines smaller**, including the
entire 152-line write breaker. Deleted the 39-line
`TestPruneExpiredOutputs_DeletesBytesAndRowsPastRetention`. Replaced the old
bucket-cache tests with generated namespace-isolation properties and real-SDK
measurement tests for old objects, exact counts, partial failure and truncation.
The tests use an in-process S3 HTTP handler because the sandbox refuses sockets.
Final CAS tests retain upload, integrity, reference and interrupted-deletion
coverage. Earlier cache-service, proxy, grant and transport deletions remain
committed.

## Acceptance and verification

Two additional boundary cells pass:

- `TestStorage/storage-reachability-and-retention-tails/filesystem-store`
- `TestStorage/storage-reachability-and-retention-tails/s3-store-boundary`

`TestStorage` now has 22 passing boundary/component checks. Its 424 native
placement/backend/OS cells remain skipped. New complete native T1 inventory
ports: **0**. These proofs extend live-reference and reference/GC obligations
without claiming native coverage.

Commands used `GOWORK=off GOFLAGS=-buildvcs=false GOMAXPROCS=4`, `-p 4`,
`GOCACHE=/tmp/sw-astra-W7/cache`, `GOTMPDIR=/tmp/sw-astra-W7/tmp` and
`TMPDIR=/tmp/sw-astra-W7/testtmp`.

Passed:

- Whole-repository `go build -p 4 ./...` and compile-only `go test -p 4 ./... -run '^$'`.
- Full tests for `internal/blob/...`, artifacts, depcache, teamblob, raw storage,
  filesystem, S3, logseg and both acceptance packages.
- Focused vet for blob, artifacts, depcache, teamblob, acceptance and controller.
- Embedded storage-doc/changelog drift checks, formatting and `git diff --check`.
- Negative controls through temporary Go overlays. A valid but wrong tenant
  deletion key leaves an unreachable object and fails the filesystem proof.
  Removing the collector's reachability recheck deletes newly referenced bytes
  and fails its independent expected count.

`go run ./internal/commentcheck -base main .` still fails only at
`internal/controllerwire/contracts.go:47`, `pkg/store/state_materialize.go:15`
and `pkg/store/state_result_records.go:9`. These unchanged out-of-area comments
were reported in the prior handoff.

## Contracts and real integration left

The local W7 addition is `ObjectBackend.DeleteBlob(context.Context, Blob) error`,
matching W1's existing interface. No W1/C2 signature or record layout changed.

- W1's `checkBlobWriteKey` still conflicts with C1/W7 output and manifest keys.
  W7 uses the run ID as `Name` and an empty `Scope.Key`. W1 requires
  `Name=result` and `<run>/<attempt>/<generation>` in `Scope.Key`.
- W1 memo commit/lookup uses `output` and `manifest`, while C1/W7 use
  `MemoRecordName=result` with its optional manifest and members.
- The prior handoff reproduced W1 refusing restricted remote-cache lookup when
  the repo-bound reader leaves team/default-ref resolution to the server.
  That integration remains open.
- W2's `DataController.DataAuthority`, `BindData` and `client.NewDataClient`
  remain stubs. Bind upload reservation, physical commit, receipt settlement
  and reauthorized receipt replay before staging cleanup.
- The shared maintenance coordinator must pass the final backend to
  `MarkBlobTombstones`/`CollectBlobTombstones` under its maintenance lease.
  The physical adapters now satisfy the existing deletion interface directly.
- W2 must bind tagged log signing, uncertain-PUT verification, records, proxied
  reads and egress. Future S3 slots still need upload admission before their
  payload size and digest are known.
- W3/W8 must construct and refresh `StorageBinding` per attempt/generation and
  bind final logs. W4/W9 own SDK/CLI consumers and remote-cache opt-in.
- T1/lead must run authenticated HTTP/api.sock, native S3/Postgres, placement,
  OS and real-repository QA. Component passes cannot remove those skips.

## Remaining hard cut and shared files

Retire `pkg/controller/node_output.go`, `output_objects_s3.go`,
`output_download_s3.go`, `log_segments.go`, `client/output.go`,
`client/logsegments.go`, `internal/orchestrator/segment_logs.go`, their legacy
tests and the remaining raw artifact/log backends after final bindings replace
their consumers. The output-pruning portion is now removed.

Shared runtime callers changed minimally in `pkg/controller/server.go` and
`pkg/controller/storage_pass.go`. The associated legacy test is in
`pkg/controller/node_output_internal_test.go`. Shared acceptance edits are
`internal/acceptance/blob_collection.go`, `tests/acceptance/storage_test.go`,
`storage_filesystem_test.go` and `storage_s3_test.go`. The contract edit is in
`c1-data.md`. Source docs are `docs/storage.md`, `CHANGELOG.md` and the blob
README, with generated embedded copies synced. Other code edits are W7's blob
and retained bucket helper.

No git write ran, and no unmerged paths remain. Installed discovery ran only
`sparkwing info --for-agent`. No candidate, stateful Sparkwing command, pipeline,
service or release ran. Scratch is removed before the final response.

Open product questions: none. Friction is the three W1 record integrations,
unimplemented authenticated/host bindings, native-driver requirements and three
existing commentcheck violations. No new setting, credential, environment
variable, customer limit or compatibility path was added.

<!-- design:end -->

STATUS: CONTINUE
