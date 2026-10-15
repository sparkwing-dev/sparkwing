# W1b store-data continuation

Session base: 2ce2a43be. The worktree was clean at entry. The interrupted merge
was already finalized. No unmerged entries remain. No Git writes were made.

Materialization now uses the data controller's default persisted capability
resolver. The previous proof supplied a test-only authority projection.
`Tenant.ResolveBlobAuthority` exposes the existing transaction owner for data
read/write grants. The controller resolves the tenant and current scope through
that method when no resolver override is configured. Other authority kinds,
including sessions, retain their explicit resolver contract.

Invariant: caller hints cannot change the persisted tenant, repo, trust-tagged
ref, attempt or uploader, and revoked authority cannot resolve. The store's
128-case property checks forged projections, foreign tenants, invalid grants
and revocation. The controller integration adds 96 generated identity/scope
substitutions and refuses a bound record read after revocation. Every refusal
returns no authority. The integration failed with incomplete route policy before
the default resolver was connected and passes with it.

The memoized-result proof still drives real selection, transactional record
copies, committed selectors, controller-wire serialization and host consumption.
After deleting the source run and memo, it restores the exact typed output and
independent artifact bytes and mode. Missing or corrupt output, manifest or
member bytes remain executable misses. Production record authorization, live blob
metadata, filesystem signing and the exact download ledger are exercised.

The materialization path is closed for the lead's memoized-or-coalesced proof.
W7 can proceed with its separately owned legacy S3 output and log cut. The test
still supplies an in-process reconciliation-controller adapter and transfer
transport. Provider middleware, session/repo-reader resolver assembly, S3 delivery
and native placement remain integration points. No new native acceptance cell
or archived test-port identity is claimed.

Deleted the ten-line test-only authority resolver and its three-line callback.
No legacy production API was retired this session. Production Go delta against
session HEAD is **+57 lines**, 61 added and four deleted. The lead's metric excludes
four net implementation lines in data_contract.go, giving **+53**. The store-only
delta is **+29**. Combined W1+W1b store production against main is **+3,357**, or
**7,607 over** its -4,250 budget. No waiver is claimed. The added method exposes
an existing transaction because production data binding had no persisted resolver.
It adds no new policy algorithm, cache, retry or interface. Removing the method
and controller default reverses this addition.

Files changed:

- pkg/store/blob_authority.go owns the public transaction entry point.
- pkg/store/blob_authority_runtime_test.go checks the generated invariant.
- pkg/store/blob_materialize_integration_test.go removes custom data authority,
  checks identity substitution and checks revocation after binding.
- pkg/store/blob_materialize_test.go loses the test-only resolver.
- pkg/controller/data_authority.go and data_contract.go connect the default
  persisted resolver. These are the minimal shared production edits under 12:15.
- tests/acceptance/data_binding_test.go migrates the ObjectBackend fake to
  VerifyBlob and fixes three raw RefScope values that contained the manual tag.
  The fake refuses verification because it stores no physical bytes.
- design/rewrite/contracts/c1-data.md and pkg/store/STORE-DATA.md document the
  public resolver and its integration. This file records the handoff.

Earlier materialization files remain unchanged: internal/controllerwire/engine.go,
pkg/controller/engine_snapshot.go, internal/nodehost/output_records.go,
owner_evaluation.go, owner_schedule.go, remote_contract.go, sdk_code.go and
sdk_replay.go, internal/blob/authority.go, keys.go, object_contract.go and
objects.go, pkg/storage/manifest.go and the existing store materialization owners.
No W1 implementation, lead-owned amendment, handoffs.md or scenario catalog changed.

Verification uses GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-gocache, TMPDIR=/tmp/sw-astra-W1b, GOTMPDIR beneath it and test
parallelism at most four.

- Whole-repository go build ./... passes.
- Scoped go vet passes for pkg/store, pkg/controller, internal/blob,
  internal/nodehost and tests/acceptance.
- Commentcheck against main is clean across 2,072 files.
- Focused store tests pass in 89.890 seconds. They cover blobs, logs, final
  metered units, payment mutation/refund transactions, storage policy/accounting,
  materialization and the nine moved W1b boundary proofs.
- Final authority and materialization proofs pass in 0.623 seconds.
- The controller's ref-trust property passes in 0.013 seconds.
- All 13 data-binding test entry points pass in tests/acceptance (0.025 seconds).
  These are fake-boundary tests, including opaque-session identity preservation.
- The broader store selection fails on the inherited cloud-award metering
  property and legacy token/run/trigger schema fixtures. The same metering defect
  was reproduced against session HEAD by the preceding session. No W1 fix is
  attempted here.
- Formatting, whitespace and merge-state checks pass. Local git config has no
  user identity or core.worktree override.

Remaining contracts and handoffs:

1. **07:40 item 2, W2/C2 plus W1 schema:** PaymentEvent still declares only paid,
   expired and failed checkouts. It lacks verified setup, collection, refund,
   warning and dispute facts. payment_events requires a checkout foreign key
   and CHECKs only those three kinds. Durable setup/payment correlation is also
   absent. W2 must declare the expanded proof and its provider association.
   W1 must supply the matching receipt/correlation schema. W1b can then dispatch
   SettlePayment into the existing private transaction owners and persist the
   receipt atomically. Those owners and fake-provider properties already pass.
   No foreign contract or W1 schema was changed.
2. **W1 issuance:** authority_issue.go derives metering solely from RootMetered.
   A cloud launcher award must be metered when that flag is false.
   TestMeteredRootPolicyCannotRewriteAwardProperty still catches this.
3. **W1 exhaustion:** accountUnitBillingTx returns only error. A durable per-award
   exhaustion/grace result and cancellation reason remain needed across node,
   trigger, plan and eval renewals. The node-only legacy mechanism cannot cover
   those units. This needs the W1 renewal/schema join before its legacy cut.
4. **Legacy cuts:** credits.go still serves old claim/scheduler/controller callers.
   node_output.go and direct_upload.go retain old controller and host read paths.
   W1's separate branch has caller cuts absent here. Do not recreate its deleted
   APIs or merge its branch from this worker. W7 owns the remaining legacy S3
   output and log transport cut after materialization closure.
5. **Coalescing:** SourceCoalesced still requires a declared persisted read edge.
   A matching spec and selector cannot grant cross-run reads. Memoization meets
   the 12:15 proof requirement without inventing that authority.
6. **09:40 item 1:** W1b's nine harness proofs already live in pkg/store tests and
   pass. Remaining internal/acceptance/store_* files concern W1's claims, plans,
   credentials, schedules, capacity, transitions and recovery. None was moved again.

No pipeline, service, native PostgreSQL, deployment or release was run. The lead
owns independent review and landing. Worker scratch is removed at completion.
The shared build cache stays.

STATUS: CONTINUE
