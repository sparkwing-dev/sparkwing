# W7 storage handoff

<!-- design:begin -->

## Continuation from 847d193f7

No runtime implementation changed. Runtime code deleted: **0 lines**.
No test was added, ported, enabled or changed. The only edited file is this report.

The real tenant byte, record, manifest, tagged-log and leased-maintenance joins
from earlier sessions remain in place. The cache service, internal/cache,
gitcache, package proxy and cache chart remain deleted. Their cuts were not redone.
The available integration ref is still an ancestor of this checkout and supplies
no newer implementation to consume.

The remaining live output/log callers cannot be migrated with the declared
interfaces until their owners complete the joins below. Replacing an authorized
record lookup with a guessed digest, synthetic attempt or public CopyMemo call
would violate the frozen contracts. No substitute authentication or persistence
mechanism was added.

## Amendment and handoff audit

All W7 entries in amendments-1.md and handoffs.md were checked against current
definitions and callers. Earlier completed work remains complete.

| Entries | Status and next owner action |
| --- | --- |
| A1-24/29 | W1's transactional data-access and verified-default record APIs exist. W2 must bind authenticated authority and server-derived tenant/default ref for repository-only reads. |
| A1-30/31/32/34 | W7 byte, record, remote-cache and upload consumers use the declared interfaces. DataAuthority, BindData and NewDataClient still panic. W2 must implement them and PolicyRouter, supplying storage.BlobManifests to Tenant.BlobRecords. |
| A1-33 | The common filesystem/S3 backend, result-manifest verification and leased collection are wired. W1b must expose leased expired-staging selection. releaseExpiredBlobUploadQuotasTx releases quota holds without returning physical staging keys. |
| A1-35 | W1 must install its transaction-local materialization copier. Store.materializations is assigned only in materialize_runtime_test.go. The approved record-mutation authorization redesign remains with W1b. |
| A1-36 | W3/W8 must construct fresh StorageBinding values for each attempt/generation. W1/W3/W9 must expose committed output/manifest record keys for materialized jobs, whose persisted attempt can be zero. |
| A1-37 | SDK cache-helper, lint-cache and removed HTTP consumer cuts remain complete. |
| A1-43/50 | W8/W9 retain ownership of host/profile construction, publication, historical release assets and CLI integration. |
| A1-53 | T1/lead retain native authenticated transport, placement and real-repository QA. |

W2/W9 must also declare the authenticated log segment-commit wire operation and
construct bindings for blob.LogTransport and logseg.SegmentRecorder.
SubjectUploader already consumes those interfaces. The current wire client
declares neither a segment-commit method nor a log-binding factory.

The materialization seam still returns unavailable without its private copier.
Calling public CopyMemo before ReconcileNode would commit record edges separately
from the terminal transition. NodeRow exposes attempt/generation but no committed
result-record keys. The existing materialized-retry test preserves attempt zero.
Those missing implementations and contracts belong to the state, host and wire owners.
The previously reproduced record-mutation expiry regression was not rerun or
patched under the brief's two-strikes rule.

## Remaining deletion

The live paths still include controller S3 output/log handlers and clients,
orchestrator host sinks, store-local output files, run-directory log readers,
the raw S3 log backend and listing-based log reader. Their current callers use
the old output refs and node-log records. The authenticated replacements and
persisted result selectors above are required before those callers can move.

No shared Go caller, foreign contract, schema, credential format, configuration
or restriction changed in this continuation. No new design question was opened.

## Verification

Commands used GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-astra-W7/cache, GOTMPDIR=/tmp/sw-astra-W7/gotmp and
TMPDIR=/tmp/sw-astra-W7/tmp. Tests used -p 4.

Passed in this continuation:

- Whole-repository `go build -p 4 ./...`.
- All tests and vet for `internal/blob/...`, `internal/artifacts`,
  `internal/depcache`, `internal/teamblob` and `pkg/storage/...`.
- Controller `TestDataStorage*` and standalone `TestFilesystemDataStartup*`.
- Commentcheck against main, covering 1,244 files.
- Existing `TestStorage`: 88.629 seconds, 80 passing test events plus the package
  pass, 424 skips and no failures. These include real tenant byte/record,
  manifest-admission, archive, publication, scope and physical-log joins.
- `git diff --check`.

Native authentication and placement evidence remain open. No new acceptance
cell or archived test port is claimed.

Friction: the remaining integrations cross worker ownership or require a foreign
contract declaration. The brief requires stopping those pieces instead of
changing another area's contract. No Sparkwing binary, pipeline, service or Git
write ran. The lead retains committing and landing. Scratch was deleted after
final verification.

<!-- design:end -->

STATUS: CONTINUE
