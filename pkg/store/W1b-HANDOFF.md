# W1b store-data continuation

Base: c461f5715, with the interrupted integration merge still in the index.
No Git writes were made. All 18 unmerged paths have resolved content or the
integration branch's intended deletion. There are no conflict markers. The lead
must stage those resolutions and finish the merge because the swarm brief forbids
worker Git writes and this sandbox cannot write the shared Git metadata.

The lead's 12:15 materialization assignment is closed for memoized results.
The committed implementation already copies exact records, verifies physical
bytes, persists result selectors without creating an attempt, and carries them
through the controller and host. This session strengthens that proof:

- Output, manifest and a separate artifact member are registered through upload
  reservation/commit and record commit APIs. The proof no longer inserts blobs,
  members or records directly, or rewrites their digests.
- After copying, the source run and source memo are deleted. A fresh controller
  reconciliation returns the persisted destination selectors. The host reads
  JSON and stages the artifact from those records, preserving bytes and mode.
- Missing output, corrupt output, corrupt manifest, missing member and corrupt
  member each leave an executable materialization miss.

The proof uses the real SQLite store, EngineController, RemoteOwnerStore, signed
filesystem backend, OutputRecords and ArtifactRecords. The controller transport
and download signer are in-process test adapters. This does not prove provider
middleware, native placement or S3 delivery.

The W1b billing-start boundary now clears the test calculator override and uses
C2's real SettleBilling. Runner and Cloud cases claim, start, reopen SQLite,
finish and replay. Both retain their original 600-micro reservation price after
the price table changes. C1's stale calculator-stub description is corrected.
This closes that earlier A1-27 calculator integration gap.

The merge left pkg/controller/client/logwatch.go referring to the removed Client.
It is deleted with its unused WatchReport and observer methods: 38 production
lines removed. The merge also dropped the store import needed by the test-only
seedLegacyMeteredCredential helper. The import is restored. The whole repository
builds, and controller plus client tests compile.

Net production Go delta for this session: **-38** (0 added, 38 deleted).
The pkg/store production delta is **0** this session. Using the lead's exclusions,
combined W1+W1b against main remains **+3,348**, which is **7,598 over** the -4,250
budget. No overage waiver or completion is claimed.

Verification uses GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-gocache, TMPDIR=/tmp/sw-astra-W1b, GOTMPDIR beneath that directory,
and test parallelism at most 4.

- Whole-repository go build ./... passes.
- Scoped go vet passes for store, host, controller and client. Earlier scoped vet
  also covered blob, storage and controllerwire.
- The materialization integration proof and its generated scope, expiry and
  selector-alias properties pass. The real-calculator billing proof passes
  separately.
- The eight moved store proofs passed: CAS, presence, large presence, cache scope,
  reference edges, reachability, ledger retention and billing start.
- internal/blob, pkg/storage, internal/controllerwire and pkg/controller/client
  pass their full package tests. Focused host materialized-output, remote,
  output-record and artifact-record tests pass.
- Commentcheck against main and diff whitespace checks pass.
- The broad store/host run fails in retained legacy fixtures using args_json,
  tokens, sparkwing_meta and other removed schema. Native listener tests fail
  because sockets are denied. W1's TestMeteredRootPolicyCannotRewriteAwardProperty
  also fails for Cloud roots whose metered marker is false. Its implementation
  and fixture are outside W1b ownership and are unchanged.
- A broader StoreData/Blob/Payment selection exceeded its 120-second test budget.
  It is not counted as a pass. No native PostgreSQL, pipeline, service, deployment
  or release was run.

Tests ported this session: the materialization fixture now uses real upload and
record writes, and the billing-start proof now settles with the production C2
calculator. No archived provider-HTTP port or new native acceptance cell is claimed.
The earlier W1b store_* harness moves remain in pkg/store/*_test.go. No W1b-owned
store_* harness remains in internal/acceptance. Some moved retention/reference
fixtures still contain direct SQL seeding, so the seeding cleanup from 09:40 is
not fully closed. The remaining internal/acceptance/store_* files are W1's claim,
plan, recovery, capacity, schedule and credential checks and were not edited.

Remaining contracts and integration:

1. **W2/C2, 07:40 item 2.** PaymentEvent still declares only checkout outcomes.
   It has no setup, refund, warning or dispute facts. payment_events likewise
   requires a checkout FK and permits only three checkout kinds. SettlePayment
   cannot settle the extra events until W2 declares verified provider facts and
   setup/payment correlation. Private C1 mutation owners remain available.
   No parallel provider contract or caller-selected financial authority was added.
2. **W1/C1 coalescing.** SourceCoalesced still needs a persisted read edge. A source
   selector and matching spec do not grant cross-run access. The memoized path
   satisfies the lead's memoized-or-coalesced proof. Coalescing remains closed.
3. **W1 exhaustion.** accountUnitBillingTx returns only error. Renewal still needs
   a persisted per-award exhaustion outcome and cancellation reason for every
   unit. W1b's legacy node-only exhaustion policy cannot supply that contract.
4. Legacy credits.go still serves old claims in store.go, claim_run.go,
   node_attempt.go and launch_sync.go. node_outputs still serves the old scheduler,
   rehydration, child and runner-result handlers. W1/W3/W7 must finish those caller
   cuts before W1b can remove the remaining data implementations without changing
   their behavior or reintroducing adapters.
5. Production assembly must supply the authenticated StorageBinding and real
   data authority, upload verification and download allowance bindings. Tagged
   log transport, imported terminal-attempt seeding and physical allowance
   eviction remain integration work.

Files changed this session: pkg/store/blob_materialize_test.go,
blob_materialize_integration_test.go, store_unit_billing_boundary_test.go and this
report. Shared files: pkg/controller/client/logwatch.go (deleted),
pkg/controller/git_credentials_team_test.go (import restored), and
design/rewrite/contracts/c1-data.md (calculator evidence only).

Materialization implementation files retained from session 4: pkg/store/
blob_materialize.go, blob_read_authority.go, blob_record_mutation.go,
blob_record_runtime.go, blob_result_records.go, state_materialize.go,
state_reconcile.go, state_read.go, node_state.go and schema.sql;
internal/controllerwire/engine.go (formerly contracts.go);
pkg/controller/engine_snapshot.go; internal/nodehost/output_records.go,
owner_evaluation.go, owner_schedule.go, remote_contract.go, sdk_code.go and
sdk_replay.go; internal/blob/authority.go, keys.go, object_contract.go and
objects.go; pkg/storage/manifest.go. No additional production edit was needed
in those files this session. Lead-owned handoffs and amendments are unchanged.

Friction: the merge index cannot be finalized here. Expanded provider events,
coalesced read authority and unit exhaustion need the contracts above. Worker
scratch is deleted after checks finish. The shared build cache is retained.

STATUS: CONTINUE
