# W1b store-data continuation

Session base: d9ddb016c. The interrupted merge is already finalized. The worktree
was clean at entry, `git ls-files -u` is empty, and no Git writes were made.

The lead's 12:15 materialization assignment remains closed for memoized results.
The existing real-store/controller/host proof passes unchanged. It verifies typed
output and independent artifact bytes and mode after source run and memo deletion.
Missing output, corrupt output, corrupt manifest, missing member and corrupt member
remain executable misses. It uses an in-process controller transport and download
signer; provider middleware, S3 delivery and native placement remain unproved.

This session replaces the moved proofs' fabricated data rows with real mutations:

- CAS uses live executable claims and independent uploaders, and expires the
  interrupted ticket with the store clock before reopening and retrying.
- Presence and the 1,025-digest proof create same-team and foreign-team objects
  through reservation and verified upload commit. Only the caller's receipts
  appear, and duplicate bytes count once. Both check the resulting real usage.
- Reference-edge tests create output, manifest members and uploader receipts
  through their mutation owners, attach the output with TransitionNode, reopen,
  then check dependencies, optional and transitive dependencies, artifacts,
  generated groups and failure handlers. Unrelated, foreign and absent bytes
  remain unreadable.
- Reachability uses actual output/manifest commits and reconciliation to create
  the destination copy. Finishing the destination creates the success pin.
  Source deletion preserves copied provenance. Controlled time checks expired
  caches, receipt roots, physical-deletion failure, grace, pinned output, source
  records and takeover. A separate proof uploads and commits a new source record
  after marking; collection deletes only the object that stayed unreachable.
- Ledger retention creates every FinancialTables row and payment_events through
  the checkout, billing, card, refund, warning, upload and log mutation owners.
  It uses C2's production calculator, real node and owner claims, starts, finishes
  and completion acknowledgements. Retention and reopening preserve every
  financial row and all captured billing reports. Provider-only facts enter
  through the existing test-only transaction adapters, not a new verifier.
- Cache-scope cases use the existing W1b provenance seeder and reopen each case.
  The upload receipt is removed to isolate cache-edge authorization. This fixture
  must inject provenance that ordinary publication would refuse.

All W1b store boundary files are now in package store and reuse its existing
private fixtures. Those fixtures still seed intake/authority declarations while
exercising real data mutations. pkg/store/internal/storetest imports store, so
internal store tests cannot import it without a cycle. No second seed library was
added. W1b no longer uses the shared teststore publication/imported-attempt seeds;
W7's three blob harness files still use those helpers. The remaining store_*
files in internal/acceptance belong to W1 and were not edited. No catalog,
scenario registry, Harness or skeleton change was made.

Deleted RunStoragePassLeased and its unused metadata keys: **13 production lines**.
It has no callers or tests. The elected Operator maintenance pass remains the
implemented maintenance path. No other production API or cross-area caller was
changed. Across the eight changed test files: **539 added, 834 deleted, -295 net**.

The combined W1+W1b production Go delta against main is **+3,335**, using the
lead's exclusions for tests, testdata, acceptance and contract declarations.
That is **7,585 over** the -4,250 budget. This session's production delta is
**-13** (0 added, 13 deleted). No budget waiver or area completion is claimed.

Verification uses GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-gocache, TMPDIR=/tmp/sw-astra-W1b, GOTMPDIR beneath that directory,
and test parallelism at most 4.

- Whole-repository go build ./... passes.
- Scoped go vet passes for pkg/store and pkg/store/internal/storetest.
- The final focused store suite passes in 6.746 seconds. It selects all eight
  moved TestStore proofs, the late-reference GC proof, the materialization
  integration proof and the materialization property/rollback/miss tests.
- Commentcheck against main is clean across 2,069 files. Diff whitespace checks
  pass. The changed tests compile as part of the focused package run.
- Native and broad legacy suites were not rerun. This is not a whole-store or
  whole-repository test pass.

No native PostgreSQL, socket, pipeline, service, deployment or release was run.

No new native acceptance cell is claimed. The moved CAS, presence, large-presence,
cache-scope, reference-edge, reachability, ledger-retention and billing-start
proofs retain their plain TestStore names. The additional late-reference proof
ports the old reachability harness's marked-object resurrection check to real
upload and record writes. No archived provider-HTTP test was ported.

Remaining contracts and integration:

1. **W2/C2, 07:40 item 2.** PaymentEvent still has only checkout paid, failed and
   expired outcomes. W2 must declare verified card setup, collection, refund,
   warning and dispute facts and their durable setup/payment correlation.
   payment_events currently requires a checkout FK and three checkout kinds.
   C1 transaction mutation owners are ready, but SettlePayment cannot dispatch
   those events through the current foreign contract. No parallel proof type or
   caller-selected financial authority was added.
2. **W1/C1 coalescing.** SourceCoalesced still needs a persisted read edge. Equal
   specs and a source selector do not grant cross-run access. The memoized path
   satisfies the lead's memoized-or-coalesced acceptance requirement.
3. **W1 exhaustion.** accountUnitBillingTx returns only error. Every awarded unit
   still needs a persisted exhaustion/grace outcome and cancellation reason.
   The legacy node-only exhaustion policy cannot supply that contract.
4. **Legacy caller cuts.** credits.go still serves old claims in store.go,
   claim_run.go, node_attempt.go and launch_sync.go. node_output.go still serves
   the old scheduler, rehydration, child and runner-result paths. Those caller
   migrations need W1/W3/W7's remaining branch work before W1b can delete the
   implementations without restoring adapters. The newest W1 reports describe
   further cuts that are not present on this branch; no Git merge was attempted.
5. Production assembly still needs authenticated StorageBinding, real data
   authority/upload verification/download allowance bindings, tagged log
   transport, imported terminal-attempt seeding and physical allowance eviction.

Files changed this session: pkg/store/bucket_measure_lease.go, STORE-DATA.md,
store_billing_boundary_test.go, store_cas_boundary_test.go,
store_cache_scope_boundary_test.go, store_gc_fence_boundary_test.go,
store_presence_boundary_test.go, store_presence_large_boundary_test.go,
store_reachability_boundary_test.go, store_reference_edges_boundary_test.go,
and this report. No W1 implementation file or file outside pkg/store changed.

Cross-area materialization files retained from the earlier implementation:
internal/controllerwire/engine.go (ExecutionJob selectors),
pkg/controller/engine_snapshot.go (selector projection),
internal/nodehost/output_records.go, owner_evaluation.go, owner_schedule.go,
remote_contract.go, sdk_code.go and sdk_replay.go (host consumption),
internal/blob/authority.go, keys.go, object_contract.go and objects.go
(selected record and byte authorization), and pkg/storage/manifest.go.
The store owners remain blob_materialize.go, blob_read_authority.go,
blob_record_mutation.go, blob_record_runtime.go, blob_result_records.go,
state_materialize.go, state_reconcile.go, state_read.go, node_state.go and schema.sql.
No further edits to these materialization files were needed this session.
Lead-owned handoffs and amendments are unchanged.

Friction: C2's expanded provider event contract and W1's unit-exhaustion outcome
remain missing. Remaining legacy callers and the production budget prevent
completion. Worker scratch is deleted after checks; the shared build cache stays.

STATUS: CONTINUE
