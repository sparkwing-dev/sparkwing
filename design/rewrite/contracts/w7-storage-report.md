# W7 storage handoff

<!-- design:begin -->

W7 remains incomplete. This continuation starts from `33f6be8eb` with a clean
worktree and a completed integration merge. Earlier byte backends, receipt,
manifest, collection, depcache, binary/source and tagged-log work remain in place.

## Implemented

Invariant: a failed record transaction, missing result or unverified byte stream
cannot produce a successful memo hit.

Two new acceptance component cells join `OutputData.Put`, artifact capture,
`NewSignedStore`, physical publication, `MaterializeMemo` and artifact staging.
Filesystem and S3 each receive 128 generated payloads, including empty output.
They check the C1 result layout, successful materialization and restored files.
Missing output records, missing output/member bytes, corrupt output/manifests/
members and refused manifest-copy or read-edge transactions produce misses.
Required artifact reads fail before replacing the affected destination.
Controller authorization and record transactions use contract fakes. The S3
backend uses the real signer and SDK with an external-service fake.

Invariant: capture never publishes bytes outside its workspace, and staging
never changes files outside its workspace.

Two generated artifact properties vary payloads, directory depth and file versus
directory symlinks. They exercise wildcard capture and staging through nested
symlinks, absolute paths and traversal paths. Outside bytes stay unchanged and
no staging temporary escapes. These properties run on Unix. Native Windows
filesystem and symlink evidence remains open.

Bucket measurement now consumes `storage.UsageReporter` directly. The controller
no longer accepts a raw read/write object store solely to measure its totals.
Filesystem measurement, partial totals, failed measurements, the S3 page cap,
measurement outside serving request budgets and capped SDK retries pass. The
retry proof uses `testing/synctest` and an injected HTTP client.

## Deleted

Removed `storeurl.OpenArtifactStore`, `storage.Usage`, the unused
`Server.WithArtifactStore` field/setter/fallback, the redundant S3 factory wrapper
and its global limiter override. Removed their raw-object examples and factory
round-trip tests, plus the retired real-bucket fixture and its leak exclusions.
The replacement measurements retain their relevant assertions.

The code diff removes **460 lines**: **106 production**, **316 test/example**
and **38 package-documentation** lines. These are removed lines, not net savings.
New properties and replacement tests are additional lines. Earlier cache-service,
gitcache, package-proxy and cache-chart deletions were already committed and are
not included in this count.

## Acceptance and ports

New passing cells:

- `TestStorage/storage-memo-materialization-failures/filesystem-components`
- `TestStorage/storage-memo-materialization-failures/s3-components`

The complete storage acceptance run has **68 passing test events**, including
parent summaries, and **425 skips**. There are 424 native skeleton cells and the
known real-store physical-log mismatch. No new native cell is claimed complete.

The connected properties carry the storage failure assertions from
`TestMemoUnclaimedFollowerWithoutLeaderOutputRuns`,
`TestMemoCopyWhoseManifestWriteFailsRunsAsAMiss` and
`TestARetryRunsAProducerWhoseArtifactsCannotBeCopied` from the T1 inventory.
Their native host, transition and transaction ports remain open. The deleted
factory retry test is ported to `TestMeasurementStoreCapsSDKRetries`.
The list-budget test now exercises injected HTTP without a global override.

## Amendment and handoff ledger

| Entry | W7 status | Real integration left |
| --- | --- | --- |
| A1-24 | Byte/manifest adapters implemented. | W2 binds W1's transactional checker and verified manifest reader. |
| A1-29 | Restricted reads retain authorized member metadata. | W1 resolves repo-reader tenant/default-ref selectors. |
| A1-30 | RemoteCacheOptions.Records and logical reads implemented. | W2/W9 construct authenticated profile transports. |
| A1-31 | Exact job, owner-unit and publication upload identities checked. | W2 derives identity from current authentication. |
| A1-32 | Physical/SQLite receipt and replay proofs cover both backends. | W2 binds reservation, reauthorization, physical commit and replay. |
| A1-33 | Filesystem/S3 physical owner, collection and manifest reader implemented. Raw factory cut advances deletion. | W2 and maintenance bind the physical owner. |
| A1-34 | Signed filesystem/S3 byte components implemented. | DataAuthority, BindData and NewDataClient remain panic stubs. |
| A1-35 | Output, memo, depcache, staging, collection and log components implemented. Connected materialization proofs pass. | W1 result/memo layout and physical log-slot alignment remain open. |
| A1-36 | Artifact, memo and depcache layers use common callbacks and refresh bindings after execution. | W3/W8 construct fresh StorageBinding values per attempt/generation. |
| A1-37 | Shared contained keys and go-modules resolver implemented. Removed lint transport stays deleted. | W3/W8 finish production host construction. |
| A1-43 | Cache deletion preserves engine/releaseasset ownership. | W8 finishes version-aware asset functions. |
| A1-50 | Read-only cache, depcache and source/binary byte components implemented. | W9 wires explicit profile opt-in, publication and engine clients. |
| A1-53 | Two more backend component checks pass. | T1/lead owns native drivers, platform matrix and real-repository QA. |

The W7 manifest-admission, physical-log and host-layer entries in `handoffs.md`
remain open in their owning areas. The new entry records the completed raw
factory cut and the connected materialization evidence. No other area's contract
shape changed. No new product question, customer limit, setting or runtime
mechanism was added.

## Real integration and remaining deletion

W1's `ResultBlobKey` still uses `Name=result` and a run/attempt/generation scope
key. C1 requires the run ID as name and an empty scope key. Memo persistence
still selects separate `output`/`manifest` names rather than `MemoRecordName=result`.
W1's physical log keys and zero-based records also remain different from W7's
shared physical owner, which reserves zero for the manifest and starts segments
at one. The prior executed reproductions and skipped proof remain in the handoff.
This session did not repeat those unchanged failures.

W2 must implement and bind `DataController.DataAuthority`, `BindData` and
`client.NewDataClient`, including final log transport, reservation/settlement and
exact egress. W3/W8 must construct fresh bindings for each attempt and owner
fence. W9 must bind explicit remote-cache and publication profiles. Maintenance
must bind reachability collection and filesystem log expiry.

After those bindings, remove controller `node_output.go`, `output_objects_s3.go`,
`output_download_s3.go`, `log_segments.go`, client `output.go` and
`logsegments.go`, orchestrator `segment_logs.go`, their tests and the raw
artifact/log backends. Replacing those live callers with compatibility transports
would violate the hard cut. Their replacements are tested through the declared
interfaces and fakes while those real integrations remain open.

## Verification

Commands use `GOWORK=off GOFLAGS=-buildvcs=false GOMAXPROCS=4`, `-p 4`,
`GOCACHE=/tmp/sw-astra-W7/cache`, `GOTMPDIR=/tmp/sw-astra-W7/gotmp` and
`TMPDIR=/tmp/sw-astra-W7/tmp`.

Passed:

- Whole-repository build and compile-only tests for all 172 root packages.
- Full tests for blob, blob/depcache, artifacts, depcache and all pkg/storage packages.
- Focused controller bucket-measurement tests.
- Complete `TestStorage` acceptance run.
- Vet for storage, controller and acceptance packages.
- `go run ./internal/commentcheck -base main .`, clean across 1092 files.
- Sleep checking for the uncommitted W7 diff (`-base HEAD`).
- Source/embedded documentation drift, formatting and diff whitespace checks.

An initial broad controller test selector also selected `ExampleNew`, which
could not open its httptest listener in the sandbox. The focused measurement
selection passes. A measurement fixture initially omitted S3's required bucket
selector. Adding the fixture bucket made it exercise the intended budget refusal.
Whole-branch sleep checking (`-base main`) reports three existing W6/W4 test
violations in `internal/limits/deadline_test.go` and
`sparkwing/sparkwingtest/host_test.go`. They are recorded in `handoffs.md`.
Native service, PostgreSQL, Windows/Darwin, placement and real-repo QA remain
unexecuted here. The lead owns pipelines, independent review and those lanes.

## Shared files and friction

Minimal caller migrations are `pkg/controller/server.go`, `objectstore.go` and
`bucket_ceiling_internal_test.go`. They narrow measurement to its existing
read-only interface and remove the dead artifact fallback. No route registration
or controller authorization changed. W7-owned callers and docs changed in
`pkg/storage`, `pkg/storage/fs/store_test.go` and `pkg/storage/storeurl`.
Shared acceptance edits are `tests/acceptance/storage_test.go`,
`storage_remote_test.go` and new `storage_materialization_test.go`.
The amendment/handoff report files are updated. Other new tests live in
`internal/artifacts/containment_test.go`.

Friction remains the cross-area result/log layout mismatch and the unimplemented
authenticated data adapters. The supplied Xwing environment was reused. No Git
write, Sparkwing binary, pipeline or service ran. Scratch is removed before the
final report.

<!-- design:end -->

STATUS: CONTINUE
