# W1b transactional materialization continuation

Base: ddcd277aa. No Git writes were made.

Implemented the lead's 12:15 materialization assignment for memo and manual retry.
`SetResultBlobRecords` binds the real copier to the state transaction. The copier
uses exact source selectors or a scoped memo record, verifies every physical
object and manifest member, retains source provenance, and gives each destination
its own operational lifetime. It creates no execution attempt. Destination keys
use the committed result sequence. Copies, terminal selectors, state and event
share the reconciliation transaction.

Missing, expired or corrupt sources roll back to a savepoint. A materialize-only
miss returns Dispatch. An ordinary miss continues to execution admission.
Authority failures remain errors. The record mutation owner rechecks current
writer ancestry and holder authority after physical verification. Conflicting
copies cannot assign two digests to the same destination key.

`NodeRow` now persists output and manifest selectors. `ExecutionJob` and the
controller projection retain them. RemoteOwnerStore preserves terminal result
identities instead of rejecting the already-declared wire representation.
Native host generator evaluation and reconstruction consume the committed
selectors through their storage binding. The explicit output callback remains
available for placement adapters and tests. A missing selector under a storage
binding never falls through to that callback.

The filesystem and S3 ObjectBackend contract now includes VerifyBlob. Both reuse
their existing physical size/digest verifiers. storage.BlobManifests supplies that
verification to the store. This is a shared contract addition required to keep
missing output bytes from becoming a successful materialization. No new service,
setting, environment variable, timeout, quota, pricing rule or customer limit was
introduced. The savepoint is required because a failed copy must not leave either
records or state behind when reconciliation continues to execution.

Read authorization now requires the exact committed record selector for dependency
and pinned-reference reads. Trigger and plan owners read selected terminal results
in their own run. Original uploader facts survive the copy, while read authority
and retention use the destination record. Another record with the same digest
grants no metadata access.

Net production Go delta this session, using the lead's exclusions: **+384 lines**
(415 added, 31 deleted). pkg/store contributes **+326**. The combined store delta
against main is **+3,348**, which is **7,598 over** the -4,250 budget. This overage
is not waived or justified as completion. The removed lines replace attempt-only
read authorization and the host rejection of terminal result identities. No large
legacy subsystem was deleted in this session. The remaining old paths still need
the hard cuts listed below.

Verification uses GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-gocache and GOTMPDIR=/tmp/sw-astra-W1b, with -p 4. Final checks
also put TMPDIR under the worker scratch directory.

- Whole-repository build and scoped vet pass.
- Focused blob, result, reconciliation and tenant SQL-scope store tests pass.
- The real SQLite/controller/host/filesystem proof passes for a memoized result.
  After source-run deletion it reads the copied JSON and manifest members.
  Missing output, corrupt output and corrupt manifest cases remain executable
  misses. The controller wire adapter and signer transport are in-process test
  adapters. Provider middleware and native placements are not proved here.
- Generated properties cover source repo/ref/pipeline and result fences (80
  cases), owner expiry during physical preparation (64), and record aliases with
  an authorized digest (96). Event failure rolls both destination records back.
- Host owner-evaluation, executor, reconstruction, remote-reconciliation and
  controller/client selections pass. A host test consumes an unexecuted memoized
  source through its committed selector with no output callback installed.
- The eight local store proofs pass: CAS, presence, large presence, cache scope,
  reference edges, reachability, ledger retention and billing start. The reference
  fixture needed a real encoded record namespace and committed selector after
  read authorization stopped accepting every record of a terminal attempt.
- Commentcheck against main, diff whitespace, changelog and embedded changelog
  checks pass. API snapshots, API reference, OpenAPI and embedded mirrors are
  regenerated. API-spec and API-reference generator tests pass. No new acceptance registry or scenario catalog entry was added.
- A broader host selection failed only in the old routes_output listener fixtures
  because the sandbox refuses sockets. The in-process selections pass. No native
  PostgreSQL, Sparkwing pipeline, service, deployment or release was run.

Tests ported: three existing blob reader fixtures now finish through the real
terminal transition and committed selector instead of manually marking a node
terminal. The existing reference-edge boundary fixture now persists its exact
selector. The host wire refusal test now proves preservation of the declared
terminal result source. No historical billing test port is claimed this session.

Contract changes needed and remaining integration:

1. **W1/C1 coalesced read authority.** The contract supplies a source result and
   spec hash, but neither grants a cross-run read. The final concurrency_waiters
   schema excludes coalesce, so its legacy leader columns cannot establish the
   authority. The real copier refuses SourceCoalesced with Unavailable. W1 must
   declare a persisted coalescing read edge or a scoped memo proof correlated to
   the selected source, then W1b can validate it transactionally. No authority was
   inferred from spec equality. The unproven coalesced path is not complete.
2. **W2/C2 payment events beyond checkout.** PaymentEvent and payment_events
   still carry checkout-only facts. Verified setup, refunds, warnings and disputes
   need the declared provider proof and setup/payment correlation. W1b's private
   mutation owners remain ready. No foreign C2 contract was changed.
3. **W1 per-award exhaustion.** accountUnitBillingTx still returns only error,
   and renewal constructs LeaseBeat before accounting. Persist an exhaustion
   outcome for every unit and carry its cancellation reason in renewal. The
   legacy node exhaustion path cannot implement that for owner/plan/eval awards.
4. The legacy credit owner in credits.go still serves old claims in store.go,
   claim_run.go, node_attempt.go and launch_sync.go. Old node_outputs readers still
   serve the legacy scheduler, rehydration, children and runner-result handlers.
   Those callers require migration and deletion. No free-billing or empty-output
   fallback was introduced to remove them.
5. The committed selectors, native host reader and physical verifier are now
   available for W7's legacy S3 output/log cut. Deployment assembly must provide
   the authenticated StorageBinding. C2 DataController authority resolution,
   upload verification and combined download allowance accounting remain native
   integration points. The proof uses an in-process signer adapter.
6. W1 imported-terminal-attempt seeding, W2/W3 authenticated log transport and
   subject construction, physical allowance eviction, pre-run source binding,
   and W2's legacy GET credit-settings authorization fixture remain open.

Shared production files touched: pkg/store/node_state.go, state_read.go,
state_materialize.go, state_reconcile.go and schema.sql for selector persistence
and the copier call; internal/controllerwire/contracts.go;
pkg/controller/engine_snapshot.go; internal/nodehost/output_records.go,
owner_evaluation.go, owner_schedule.go, remote_contract.go, sdk_code.go and
sdk_replay.go; internal/blob/authority.go, keys.go, object_contract.go and
objects.go; pkg/storage/manifest.go. W1b's blob files contain the implementation.
The new end-to-end proof and property tests live in pkg/store. The host consumption
proof is internal/nodehost/materialized_output_test.go. Existing fixture/caller
changes are blob_data_access_test.go, blob_metadata_test.go,
blob_mutation_fence_test.go, blob_records_runtime_test.go, blob_restore_test.go,
store_reference_edges_boundary_test.go and remote_contract_test.go.

Shared prose/generated files: C1, the controller wire contract, CHANGELOG.md,
STORE-DATA.md, API snapshots, API reference, OpenAPI and their embedded mirrors.
Lead-owned handoffs and amendments are unchanged. All W1b store-data harness
moves from the 09:40 decision remain completed from earlier sessions.

Friction: coalesced source authority and expanded provider event declarations
are concrete missing contracts. Native listener tests require the lead's socket
lane. Git config has no user identity or core.worktree override. Worker scratch
is deleted after checks finish. The shared build cache is retained.

STATUS: CONTINUE
