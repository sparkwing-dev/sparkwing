# W1b store-data continuation

Session base: d7899b31a. The worktree was clean at entry. The interrupted merge
was already finalized. No unmerged entries or conflict markers remain. No Git
writes were made.

Materialization now passes through the production DataController binding.
The previous proof bypassed that binding with a custom signer. Replacing the
signer exposed a ref comparison failure: sealed manual grants contain
`manual:main`, while the controller compared them with raw `main`.

Invariant: record authorization compares both the ref and its trust level through
the same scope encoding. The store's private seven-line encoder moves to
`blob.RefScope.CapabilityScope`. Store callers, controller write projection,
record reads and logical targets use it. Wire and schema values stay the same.
The generated controller property checks 128 refs and trust tags, permits the
exact grant and refuses changed refs and trust tags for reads and writes.
Both the property and the production-binding integration proof failed before
the fix and pass after it.

The memoized result proof retains the real selection, transactional copies,
committed selectors, controller-wire round trip and host consumption. It deletes
the source run and memo, then restores typed output and an independent artifact
with its mode. Record lookups, access checks, live blob metadata, download charging
and filesystem signing now use production owners. The exact signed output,
manifest and artifact byte total reaches the real download ledger. Missing and
corrupt source bytes still produce executable misses.

The proof retains an in-process controller adapter, HTTP transfer transport and
a test-only projection into the existing private store authority resolver.
Provider middleware, production authority-resolver assembly, S3 delivery and
native placement remain real-integration points. No native acceptance cell is
newly claimed. No archived test port identity is added.

Deleted the seven-line private ref encoder and the 20-line signer fake.
All encoder callers migrate in this change. No legacy production API was retired
this session. Production delta against the session base is **+1**: 22 added,
21 deleted. Within pkg/store it is **-7**. The combined W1+W1b delta against main
is **+3,328**, or **7,578 over** its -4,250 budget. No waiver is claimed.

Files changed:

- pkg/store/blob_lookup.go, blob_materialize.go, blob_persistence.go,
  blob_read_authority.go, blob_record_mutation.go, blob_record_runtime.go and
  upload_runtime.go migrate to the shared encoder.
- pkg/store/blob_materialize_integration_test.go uses the production binding and
  checks its download ledger. blob_materialize_test.go supplies the test-only
  projection into the private resolver.
- internal/blob/authority.go owns the existing encoder.
- pkg/controller/data_authority.go and data_record_binding.go use tagged refs.
  data_ref_scope_test.go adds the generated regression.
- pkg/store/STORE-DATA.md and this report describe the final behavior and limits.

Earlier materialization files retained unchanged: internal/controllerwire/engine.go,
pkg/controller/engine_snapshot.go, internal/nodehost/output_records.go,
owner_evaluation.go, owner_schedule.go, remote_contract.go, sdk_code.go and
sdk_replay.go, internal/blob/keys.go, object_contract.go and objects.go, and
pkg/storage/manifest.go. No W1 implementation, lead-owned amendment, handoff,
acceptance registry or scenario catalog was edited.

Verification uses GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-gocache, TMPDIR=/tmp/sw-astra-W1b, GOTMPDIR beneath it and test
parallelism at most four.

- Whole-repository go build ./... passes.
- Scoped go vet passes for pkg/store, internal/blob, pkg/controller and
  internal/nodehost.
- Commentcheck against main is clean across 2,070 files.
- Full internal/blob tests pass. Focused native-host materialization, artifacts,
  memo-miss, oversized-output and reconciliation tests pass.
- The final materialization proof passes in pkg/store (0.501 seconds). The
  controller's generated ref property passes (0.014 seconds).
- The nine named store boundary proofs pass in 3.479 seconds. They cover billing
  retention/start, CAS, presence, large presence, cache scope, reference edges,
  reachability retention and a reference added after marking.
- The broader store/controller selection fails. Legacy token, trigger and run
  fixtures still expect deleted schema columns/tables. Its final-store
  TestMeteredRootPolicyCannotRewriteAwardProperty also fails for cloud=true,
  RootMetered=false. These failures are outside the ref-scope change.

Remaining contracts, integration and friction:

1. **W2/C2 payment events, 07:40 item 2:** PaymentEvent still declares only paid,
   expired and failed checkouts. Verified setup, collection, refund, warning and
   dispute facts plus durable setup/payment correlation remain undeclared.
   SettlePayment cannot dispatch those events without changing a foreign
   contract. The private C1 mutation owners and their fake-provider tests remain.
2. **W1 issuance:** authority_issue.go derives Billing.Metered solely from
   parent.Capability.RootMetered, despite identifying claims.launch as launcher
   authority. Cloud awards must be metered when the root flag is false. The
   existing generated regression catches this. An overlay restoring every changed
   tracked Go file to session HEAD reproduces the same failure. W1 owns that file,
   and the lead
   forbids further W1 edits beyond catalog changes and caller migrations.
3. **W1 exhaustion:** accountUnitBillingTx still returns only error. A persisted
   per-award exhaustion/grace result and cancellation reason remain needed for
   node, trigger, plan and eval renewals. The legacy node-only policy is not a
   substitute. No W1 renewal or schema implementation was changed.
4. **Legacy cuts:** credits.go, node_output.go and raw storage still have live
   old claim, scheduler, rehydration, controller and runner-result callers.
   W1's newer reports describe cuts on its branch that are absent here. No merge
   was attempted. W7's legacy output/log transport cut remains separate.
5. **Coalescing:** SourceCoalesced needs a persisted read edge. Matching specs
   and a selector cannot grant cross-run reads. Memoization satisfies the lead's
   memoized-or-coalesced proof requirement.
6. **Harness move, 09:40 item 1:** W1b's move and real data mutations were finished
   before this session. Remaining internal/acceptance/store_* files belong to W1.

No service, pipeline, native PostgreSQL, deployment or release was run. The lead
still owns independent review and landing. Worker scratch is deleted. The shared
build cache stays. Whitespace and formatting checks pass. The worktree config
has no user identity or core.worktree override.

STATUS: CONTINUE
