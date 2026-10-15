# W7 storage handoff

<!-- design:begin -->

W7 remains incomplete. Filesystem and S3 now supply the store's verified
manifest reader. Authenticated runtime binding, the remaining legacy
output/log cut and native acceptance remain.

## Implemented in this continuation

Invariant: record members come only from digest-verified bytes in the selected
tenant's CAS, after a successful close.

`ObjectBackend.ReadManifest(ctx, team, digest)` derives the tenant CAS key,
verifies stored length and SHA-256 through EOF, closes the object and validates
its member metadata. Missing objects, corruption, malformed members,
cancellation and close failures return no members. It creates no receipt,
existence row or read edge.

`storage.BlobManifests` implements W1's existing `BlobManifestReader`. It opens
no database transaction or authenticated data request, so W1 can use it inside
its record transaction after authorizing the manifest. W1 still checks every
member's existence, size and reference authority. W2 must supply this adapter to
`Tenant.BlobRecords` in its authenticated record binding.

The generated filesystem and S3 proofs each run 128 cases with empty and
multi-file manifests, tenant substitution, invalid tenant components, altered
bytes, truncation, extra bytes and cancellation. Dedicated S3 tests cover
incorrect stored lengths, close errors and cancellation after the object opens.

The SQLite proofs each exercise twelve generated manifest/member pairs with
real signed transfers, upload reservations, physical publication and receipt
settlement. Forged metadata and another same-run job's receipt cannot create
member rows. An independent verified upload succeeds. Stored metadata survives
reopening the database. The authority/imported attempts are seeded fixtures.
These boundaries use W1's existing `ResultBlobKey`; they do not resolve its
conflict with C1's output/manifest key layout.

The collection fixture's signed-upload helper is shared with the new receipt
proof. Its staging cleanup remains after settlement in the receipt proof.

Legacy code deleted in this continuation: **0 lines**. The previous continuation's
879-line net production deletion and earlier cache-service/proxy/grant cuts remain
committed. Remaining legacy route consumers still require their final bindings.

## Acceptance and verification

Four new boundary cells pass under `TestStorage/storage-reference-edge-laundering`:

- `filesystem-manifests`
- `s3-manifests`
- `filesystem-manifest-records`
- `s3-manifest-records`

`TestStorage` has 26 passing boundary/component checks. Its 424 native
placement/backend/OS cells remain skipped. New complete native T1 inventory
ports: **0**. The record and physical-reader proofs extend the inventory's
manifest/member and unrelated-job obligations without claiming native coverage.

All Go checks use `GOWORK=off GOFLAGS=-buildvcs=false GOMAXPROCS=4`, `-p 4`,
`GOCACHE=/tmp/sw-astra-W7/cache`, `GOTMPDIR=/tmp/sw-astra-W7/tmp` and
`TMPDIR=/tmp/sw-astra-W7/testtmp`.

Passed:

- Whole-repository `go build -p 4 ./...`.
- Whole-repository compile-only `go test -p 4 ./... -run '^$'`.
- Full tests for blob/depcache, artifacts, dependency caches, raw storage,
  filesystem, S3, logseg, teamblob and both acceptance packages.
- Focused vet for blob/depcache, artifacts, dependency caches, acceptance,
  storage subpackages and controller.
- Embedded storage-doc/changelog checks, formatting and `git diff --check`.

The broader `pkg/storage/...` test command fails in the unchanged
`storeurl.TestOpenArtifactStoreCapsTheSDKRetryer`, because its httptest server
cannot open a socket in this sandbox. Other selected suites in that command pass.
`go run ./internal/commentcheck -base main .` reports only the unchanged
`internal/controllerwire/contracts.go:47` comment. The two store comment
violations recorded previously have been corrected by W1.

Negative controls use temporary Go overlays. Substituting a foreign tenant in
the physical reader fails both generated backend proofs. Replacing the record
fixture's W1 key with the declared C1 key reproduces W1's
`result key differs from attempt fence` refusal. Neither overlay changes the tree.

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
| A1-36 | Common byte/record/cache components consume the declared storage binding. | W3/W8 must construct and refresh it for every attempt/generation. |
| A1-37 | Removed lint-cache transport and internal cache clients stay deleted. | W4 owns public SDK Cache helpers and their final runtime consumer. |
| A1-43 | Cache deletion leaves engine/releaseasset owners and historical-matrix declarations in place. | W8's version-aware asset functions remain stubs. |
| A1-50 | Read-only remote-cache/depcache adapters and source/binary byte components are implemented. | W9 owns CLI/profile/publish/engine binding. |
| A1-53 | Four additional physical/store boundary proofs pass. | T1/lead owns authenticated/native drivers and real-repository QA. |

`handoffs.md` contained no W7 assignment at this wake. Its W4 review entries name
other workers. Added the verified-manifest binding handoff for W2 and restated the
unresolved C1 layout integration for W1. No W4 review mechanism was changed.

## Contracts and real integration left

The local W7 contract addition is
`ObjectBackend.ReadManifest(context.Context, string, Digest) ([]Member, error)`.
W1's `BlobManifestReader` and C2 authority signatures are unchanged. No new
cross-area contract shape is requested.

- W1 must align output/manifest keys with C1. W7 uses the run ID as `Name`
  and an empty `Scope.Key`. W1 requires `Name=result` and
  `<run>/<attempt>/<generation>` in `Scope.Key`. The overlay probe reproduces it.
- W1 memo commit/lookup still uses `output` and `manifest`, while C1/W7 use
  `MemoRecordName=result` with optional manifest and members. Its output cap must
  follow the agreed memo result layout.
- W1's exact record lookup still requires a nonempty tenant/ref key. W7's
  repo-bound remote client leaves tenant and verified default-ref resolution
  to the authenticated server. The narrower repo-reader edge proof passes.
- W2 must bind upload admission, physical verification, quota settlement and
  reauthorized receipt replay. Staging cleanup follows successful settlement.
- The maintenance coordinator must pass the final backend into W1's leased
  tombstone collector and invoke filesystem log retention under that lease.
- W2 must bind tagged log signing, uncertain-PUT verification, records,
  controller-proxied reads and egress. Future S3 slots need upload admission
  before their payload size/digest are known.
- W3/W8 must bind final log clients and fresh storage authority per
  attempt/generation. W4/W9 own SDK/CLI consumers and remote-cache opt-in.
- T1/lead must run HTTP/api.sock authentication, native S3/Postgres, placement,
  native OS and real-repository evidence. Boundary proofs cannot remove those skips.

## Remaining hard cut and shared files

Retire `pkg/controller/node_output.go`, `output_objects_s3.go`,
`output_download_s3.go`, `log_segments.go`, `client/output.go`,
`client/logsegments.go`, `internal/orchestrator/segment_logs.go`, their legacy
tests and the remaining raw artifact/log backends after the final bindings replace
their consumers. Age-based output pruning was already removed.

Shared additions/edits in this continuation:

- `pkg/storage/manifest.go` implements W1's existing reader interface.
- `internal/acceptance/blob_collection.go` shares its signed-upload fixture.
- `internal/acceptance/blob_manifests.go` adds real record/receipt proofs.
- `tests/acceptance/storage_manifests_test.go`, `storage_s3_test.go` and
  `storage_test.go` add/register the backend properties and transaction boundaries.
- `c1-data.md`, this report and `handoffs.md` record the local contract and handoffs.
- `docs/storage.md`, `CHANGELOG.md` and generated embedded copies document the reader.

There are no migrated or removed out-of-area runtime callers in this continuation.
Other implementation edits are W7's `internal/blob` files and README.

The added mechanism is the physical manifest reader plus the typed store adapter.
Reading through the authenticated signed store inside a record transaction could
open another transaction, so the backend supplies this trusted physical read after
W1 authorizes it. Removing the reader method and adapter undoes this addition.
It is exercised by real physical/store boundary tests. Production binding remains
with W2. No setting, credential, environment variable, limit or compatibility path
was added.

No Sparkwing binary, candidate, pipeline, service, release or Git write ran.
Scratch is removed before the final response. Open product questions: none.
Friction remains the W1 layout/selector conflicts, missing authenticated/host
bindings, native-driver requirements, socket-requiring storeurl test and one
out-of-area commentcheck violation.

<!-- design:end -->

STATUS: CONTINUE
