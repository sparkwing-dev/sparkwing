# W7 storage handoff

<!-- design:begin -->

## Continuation from 8d646ab4a

The earlier real-store byte, record, manifest, tagged-log and leased-maintenance
joins remain implemented. No runtime code changed. The available
`bw-3095-rewrite` ref is an ancestor of this checkout, with no newer integration
changes to consume. No component-only acceptance cell was added or unskipped.

Removed the unused OpenAPI `GitcacheRepo` parameter. No registered operation
references it. Removed cache-service deployment, network, health, ceiling,
metric and egress instructions from architecture and observability docs, and
synced their embedded copies. The storage page now reflects the existing
standalone filesystem startup binding. Historical release matrices and
migration guides remain intact.

Runtime code deleted: **0 lines**. Legacy docs/schema cleanup removes
**239 lines and adds 90**, including embedded copies, before this report.
This adds no mechanism, interface, credential format, configuration or restriction.
No test was ported.

## Verification

Commands used GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-astra-W7/cache, GOTMPDIR=/tmp/sw-astra-W7/gotmp and
TMPDIR=/tmp/sw-astra-W7/tmp. Tests used -p 4.

Passed:

- Whole-repository `go build -p 4 ./...`.
- All tests for internal/blob/..., artifacts, depcache, teamblob and
  pkg/storage/..., plus internal/apispec.
- Vet on those packages and pkg/docs.
- Controller TestDataStorage*, including physical result attachment and
  leased maintenance, and standalone TestFilesystemDataStartup*.
- Existing `TestStorage`: 107.216 seconds, 80 passing test events plus the
  package pass, 424 skips and no failures. Real SQLite byte/record, manifest
  admission, publication, scope and physical log cells remain passing.
- Embedded-doc and changelog drift guards, sync-docs --check,
  commentcheck against main (1,244 files), and git diff --check.

Native authentication, placement and skipped-platform acceptance remain open.
No new acceptance receipt is claimed for these doc/schema cuts.

The broader pkg/docs run, excluding its Web tests, fails
TestShellCommandQueriesReachTheRightSection and TestDocsNameOnlySymbolsThatExist.
The first ranks unrelated sections for two shell-command queries. The second
finds references to removed SDK exports in untouched pages. Both failures
reproduce using a Go overlay with the original three embedded pages from HEAD.
W9 owns those pages and search fixtures. The focused drift guards pass.

## Amendment audit and remaining joins

Every W7 entry in amendments-1.md and handoffs.md was checked against the
current callers. Earlier completed cuts were retained.

| Entry | Remaining integration |
| --- | --- |
| A1-24/29 | W2 authenticated data binding and server-derived tenant/default ref for repository-only lookups. |
| A1-30/31/32/34 | DataAuthority, BindData and NewDataClient still panic. PolicyRouter remains a stub. Existing W7 consumers use the declared byte and record interfaces. Bind manifests through Tenant.BlobRecords(storage.BlobManifests{Objects: objects}). |
| A1-33 | W1b must expose leased expired-upload staging selection. releaseExpiredBlobUploadQuotasTx releases quota holds but returns no physical staging keys. |
| A1-35 | W1 must install its transaction-local materialization copier. The only assignments to Store.materializations remain in materialize_runtime_test.go. W1b owns the approved transaction-local record-mutation redesign. |
| A1-36 | W3/W8 must construct fresh per-attempt/generation StorageBindings and finish local-controller construction. W1/W3/W9 must expose committed output/manifest record keys for materialized results whose attempt may be zero. |
| A1-37 | SDK cache-helper and lint-cache cuts remain complete. |
| A1-43/50 | W8/W9 own remaining profile, host, publication and historical signed-asset integrations. |
| A1-53 | T1/lead own native authenticated transport, placement and real-repository QA. |

W2/W9 must declare the authenticated log segment-commit wire operation and
construct bindings for blob.LogTransport and logseg.SegmentRecorder.
SubjectUploader already consumes these interfaces. W7 did not invent another
credential protocol or change a foreign contract.

The W1 materialization seam still returns unavailable when its private copier
is absent. Calling public CopyMemo before reconciliation would commit the
records separately from the terminal transition. The prior failing probe was
not repeated. The previous record-mutation expiry finding remains assigned to
W1b under the approved redesign and the brief's two-strikes rule.

Remaining legacy code includes S3 output/log handlers and clients, host sinks,
raw S3 log objects, store-local outputs and run-directory log readers. Their
callers require the joins above. Their deletion and native acceptance are
unfinished. No new design question was introduced.

## Shared files and cleanup

Shared edits: api/openapi.yaml, docs/architecture.md, docs/observability.md,
docs/storage.md and the three corresponding pkg/docs/mirror files.
This report is W7-owned. No shared Go caller changed.

Friction: missing cross-area joins and the two reproduced baseline docs failures.
No Git command wrote metadata. No Sparkwing executable, service or pipeline ran
after initial discovery. The lead retains committing and landing. Scratch is
removed after final verification.

<!-- design:end -->

STATUS: CONTINUE
