# W7 storage handoff

<!-- design:begin -->

## Integration merge and attempt-fenced result verification

All twelve conflicted paths have resolved working-tree contents. The lead-owned
handoffs.md is byte-identical to incoming index stage 3. Both migration sections
survive. Generated API docs and OpenAPI were rebuilt, then embedded docs synced.
No conflict markers remain. The index still has twelve unmerged entries because
the swarm brief prohibits Git writes. The lead must stage the resolved paths and
finish the merge, including the deletion of internal/nodehost/memo_unix_test.go.

W7's cache-grant and output-signer removals survive. Incoming removal of the warm
fallback, concurrency-cache maintenance, run-row heartbeat and launched planner
also survives. Removed stale references to DefaultConcurrencyCacheCap and
planLaunchedRun that otherwise broke the whole-repository build. Auth docs no
longer claim the removed Git proxy or run-row heartbeat is available.

The existing TestDataStorageAttachesVerifiedResultRecords property failed after
the merge because holder transitions now require SourceAttempt. It now uses the
real Tenant.TransitionNode API with ordinal zero at start and Applied.Attempt at
report. Twelve generated cases still prove corrupt physical manifests cannot
commit a terminal result, valid bytes can, and identical completion replays.
Server.WithDataObjects still installs the real result verifier. This test proves
the controller's store construction, not W9's transition wire adapter.

No additional legacy storage runtime was deleted: **0 storage lines**. Earlier
cache service, internal/cache, gitcache, package proxy and cache chart deletions
remain intact. No new acceptance cell or archived test port was added. No new
credential, schema, restriction or foreign contract was introduced.

## Amendment and handoff audit

All W7 rows in amendments-1.md and the incoming handoffs.md were checked against
current definitions and callers. Earlier completed work remains complete.

| Entries | Current integration and remaining owner action |
| --- | --- |
| A1-24/29 | W1 transactional data-access, exact result layouts and verified-default record APIs are consumed by W7's existing byte/record/archive integrations. W2 must bind authenticated authority and server-derived tenant/default ref for repository-only reads. |
| A1-30/31/32/34 | W7 consumers use DataSigner, RecordStore and resolved UploadIdentity. DataAuthority, BindData and NewDataClient still panic. W2/W9 must implement those adapters and PolicyRouter. |
| A1-33 | Both physical backends, manifest verification and leased collection remain wired. W1b must expose leased expired-staging selection. releaseExpiredBlobUploadQuotasTx releases holds without returning physical staging keys. |
| A1-35 | W1 must install the transaction-local materialization copier. Store.materializations is assigned only in materialize_runtime_test.go. W1b retains the approved record-mutation authorization redesign. |
| A1-36 | W3/W8 must construct fresh attempt/generation StorageBindings. W1/W3/W9 must expose committed output/manifest selectors for materialized jobs, which may have attempt zero. |
| A1-37 | SDK helper, lint-cache and removed HTTP consumer cuts remain complete. |
| A1-43/50 | W8/W9 retain host/profile construction, publication, historical release assets and CLI integration. |
| A1-53 | T1/lead retain native authenticated transport, placement and real-repository QA. |

W1's new SourceAttempt contract also needs W9/W3 transport and caller migration.
JobTransitionRequest still has no source-attempt field, and nodeTransitionForStore
cannot populate it. The repaired W7 property uses the tenant transaction rather
than inventing a foreign wire field. W9 must complete that interface amendment.

W2/W9 must declare and implement authenticated log segment commit and binding
construction for blob.LogTransport plus logseg.SegmentRecorder. The existing
SubjectUploader already consumes those interfaces. Reusing the old per-node
log-manifest endpoint would retain its incompatible persistence contract.

The private materialization seam still returns unavailable without its copier.
Public CopyMemo before ReconcileNode would commit edges separately from terminal
state. MaterializedResultSource can identify a state sequence, but NodeRow still
has no committed result-record selectors. The prior record-expiry regression was
not patched under the brief's two-strikes rule. Its approved redesign remains W1b.

## Remaining deletion

Controller S3 output/log handlers and clients, orchestrator sinks, store-local
output files, run-directory log readers, raw S3 log access and the listing-based
reader remain live. Every remaining pkg/storage/s3 method has a production caller
or supports bucket measurement. Removing them now would remove behavior without
its authenticated replacement. These are unfinished cuts, not completed work.

Shared files touched by merge resolution or caller adaptation:
pkg/controller/server.go, tenant_routes.go and data_result_test.go;
internal/orchestrator/claim_exec.go; internal/cluster/runner_pool_cli_test.go;
internal/nodehost/memo_unix_test.go (deletion retained); docs/auth.md,
docs/api-reference.md and docs/migrations/_unreleased.md; their embedded mirrors;
api/openapi.yaml; and the lead-owned handoffs.md conflict resolution only.

## Verification

Commands used GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-astra-W7/cache, GOTMPDIR=/tmp/sw-astra-W7/gotmp and
TMPDIR=/tmp/sw-astra-W7/tmp. Tests used -p 4. No Sparkwing binary ran.

Passed:

- Whole-repository go build -p 4 ./....
- All tests for internal/blob/..., internal/artifacts, internal/depcache,
  internal/teamblob and pkg/storage/....
- Vet for those packages plus pkg/controller and internal/orchestrator.
- Controller TestDataStorage* and TestFilesystemDataStartup*.
- Test compilation for controller, cluster, nodehost and orchestrator.
- Existing TestStorage: 211.734 seconds, 80 passing test events, 424 skips,
  no failures. Real tenant byte/record, manifest, archive and physical-log joins
  remain among these cells. Component proofs do not establish native transport.
- API route coverage and embedded-document drift checks.
- Commentcheck against main: 1,310 files. git diff --check.

Independent read-only review found no defect in the bounded merge resolutions
or result-fixture migration. It did not independently run tests or establish
wire behavior. No new design decision is requested.

Friction: the brief reserves staging and committing to the lead, so Git still
reports index conflicts. Remaining runtime joins cross ownership or require a
foreign contract amendment. No substitute authentication or SQL implementation
was added. Scratch is removed after checks. The lead owns merge completion.

<!-- design:end -->

STATUS: CONTINUE
