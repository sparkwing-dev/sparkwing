# W7 storage handoff

<!-- design:begin -->

## This continuation

Continued from 5549c868e. Earlier cache-service, Git-cache, package-proxy,
cache-chart, filesystem output, record-layout and real-store wiring remains.
No acceptance cell was added or unskipped. No foreign contract declaration changed.

Removed the legacy output-copy mechanism because the host memo layer already
calls blob.RecordStore.CopyMemo. The cut removes AttemptReport.OutputFrom and
its output_from payload, Store.FinishNodeCopyingOutput, sourceOutputRefTx,
copySource and both store copy branches. It also removes the unused
localState.FinishNodeCopyingOutput wrapper, copiedOutput type and the two
backend GetNodeOutput forwarding methods. No runtime caller needed a replacement.

Deleted the old claim/input test's output-copy assertions. Existing real-store
CopyMemo tests cover replay, atomic destination rollback, dependency refusal and
source collection. No archived T1 inventory row is newly marked ported.

The tenant-call guard removed the retired wrapper's exemption. Running it also
reported seventeen stale exemptions left by earlier profile/concurrency cuts.
Those exemptions are removed, and the guard now passes. No new exemption was added.

This removes **110 Go lines: 74 production and 36 test lines**, with two changed
signature/call lines added, for a net reduction of 108 Go lines. The store API
snapshot, source docs, changelog and embedded copies agree with the cut. OpenAPI
and generated API references also drop the two already removed filesystem output
endpoints and document the registered signed filesystem data transfers. The
dependency-cache example no longer directs users to the deleted cache service.

## Verification

Commands used GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-astra-W7/cache, GOTMPDIR=/tmp/sw-astra-W7/gotmp and
TMPDIR=/tmp/sw-astra-W7/tmp. Tests used -p 4 from this worktree.

Passed:

- Whole-repository go build -p 4 ./... and compile-only go test ./... -run '^$'.
- Full pkg/storage/..., internal/blob/..., internal/artifacts, internal/depcache
  and internal/teamblob tests.
- All controller TestDataStorage* tests, including real claim/start/upload,
  physical manifest verification and terminal result replay.
- Real-store output replay/scope, unrelated same-run/dependency checks, CopyMemo
  and depcache pointers, atomic destination rollback and source collection tests.
- TestDefaultTeamTwinsAreCalledOnlyWhereTheDefaultTeamIsMeant after removing
  the stale exemptions it reported.
- Vet for store, controller, backend and orchestrator.
- Required commentcheck against main and current-diff sleepcheck against HEAD.
- OpenAPI/API generation, internal/apispec and internal/apiref tests, embedded
  document drift guards, sync-docs --check and git diff --check.

Existing TestStorage passes in **100.921 seconds**, with **81 passing events,
zero failures and 424 skips**. Passing groups include CAS integrity, reference
edge laundering, HasMany read sets, memo/depcache scope, retention, log segments,
signing, replica accounting, remote-cache reads, publication, owner receipts and
quota crash replay. The filesystem/S3 record and physical-byte joins remain green.
These results do not prove native authenticated placements or platform cells.

## New real-integration finding

Invariant: authorized materialization records and terminal state commit together.
Store.materializations has assignments only in materialize_runtime_test.go.
A temporary Go overlay removed only materializationFixture's test-copier
assignment. TestReconcileMaterializesPersistedOutcomeOnce then failed all three
manual_retry, memo and coalesced cases with
`transactional materialization records are not configured`.

These are seeded store fixtures. The probe does not assert native authentication
or valid memo-byte materialization. It proves the missing transactional binding.
The ordinary CopyMemo record tests pass independently. W1 owns implementing the
existing private CopyMaterializedRecords seam. W7 cannot install an implementation
that requires storeTx. Calling public CopyMemo before reconciliation would commit
the records separately from the terminal state. The overlay is not in the repo.

W3's artifact layer also still assembles Name=runID with an empty Scope.Key.
For executed results, ReadNode can provide the persisted attempt and generation.
For materialized results, that is insufficient: the store's
TestReconcileRetryCanReuseMaterializedResult preserves attempt zero, and NodeRow
exposes no committed result-record keys. W1/W3 need a result-reader contract that
names those records without inventing an executed attempt. This source finding
was not run through a native artifact stage. The new handoff corrects the earlier
recommendation that ReadNode alone resolves every producer.

## Amendment and handoff audit

All W7 entries in amendments-1.md and handoffs.md were checked. Completed work
was retained. No W7-owned amendment declaration is left as a panic stub.

| Entry | Remaining integration |
| --- | --- |
| A1-24/29 | W2 authenticated bindings and server-derived tenant/default ref for repo-only lookup. |
| A1-30/31/32/34 | DataAuthority, BindData, NewDataClient and PolicyRouter remain stubs. Bind manifest-capable records through Tenant.BlobRecords(storage.BlobManifests{Objects: objects}). |
| A1-33 | W1b must expose leased expired-staging cleanup. Reachability cannot find bytes that never became live blobs. |
| A1-35 | W1 transactional materialization binding, W1b's approved mutation redesign and remaining live legacy output/log callers. |
| A1-36 | W3/W8 fresh host bindings, result identity for materialized artifact producers and wingd filesystem construction. |
| A1-37 | Earlier helper/lint cuts remain complete. |
| A1-43/50 | W8/W9 profile, host, publication and historical-asset integrations. |
| A1-53 | T1/lead native authenticated transport, placement and real-repository QA. |

W2/W9 still need to declare the segment-commit wire operation and construct
blob.LogTransport plus logseg.SegmentRecorder bindings. W7 uses those interfaces
and has added no competing credential protocol. Legacy S3 output/log handlers,
clients, host sinks, store-local outputs and disk-log readers remain live until
those contracts and host cutovers connect. This continuation does not claim their
deletion or complete runtime storage integration.

The prior report's six CommitRecord/CopyMemo authority-expiry failures remain an
open W1b finding under the two-strikes rule. Their mechanism did not change here,
so the unchanged failing check was not rerun or counted as a fresh result.

## Shared files and friction

Shared Go files touched outside W7 physical storage:

- internal/backend/client_backend.go and store_backend.go: remove unused reads.
- internal/orchestrator/backends.go and concurrency_dispatch.go: remove unused copy plumbing.
- pkg/store/node_output.go, node_attempt.go and store.go: delete the legacy copy mechanism.
- pkg/store/default_team_callers_test.go: narrow stale exemptions.
- pkg/controller/claim_run_test.go: delete the obsolete copy assertions.

Other changes are the store API snapshot, OpenAPI, generated API reference,
source storage/migration docs, dependency-cache example, changelog, C1 notes,
handoffs and their embedded copies.

The OpenAPI generator first refused output_from and then the two stale output
routes. Their source descriptions were removed before regeneration. The tenant
call guard first failed on seventeen stale exemptions and passes after deletion.
The materialization overlay is an expected red diagnostic, separate from the
passing ordinary tests. No new mechanism, interface, setting, environment
variable, quota or credential format was added.

No candidate, service, pipeline, native placement or Git write ran. Only the
required initial installed sparkwing info discovery ran. Scratch was deleted after the checks. Final git diff --check passes.

<!-- design:end -->

STATUS: CONTINUE
