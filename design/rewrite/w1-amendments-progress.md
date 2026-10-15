# W1 store handoff

W1 remains incomplete. This continuation starts from 5ede4a650. The inherited
work is committed, and this continuation is left for the lead to review and
commit. There are no unmerged index entries. W1b implementation files are
unchanged. Earlier implementation evidence remains in Git and
[w1-store-progress.md](w1-store-progress.md).

## This continuation

Added store enforcement for bootstrap, machine registration, service registration
and Actions roots. The existing member-root path remains. Provisioning requires
trusted origin verification, rechecks current authority and expiry after that
verification, and refuses execution fences, foreign scope, unknown/deleting teams
and console grants. Deployment token authority controls metering and service
issuance. Actions roots retain the verified repository/ref/SHA/workflow binding.
Issued roots have no revocation parent. Machine roots record their account
authorizer in created_by, so the existing consent API recognizes the actual owner.

The security invariant is that provisioning cannot exceed its verified origin or
current authorizer, and machine consent belongs to the recorded account owner.
New tests cover all four origins, provider refusal, authority changes during
verification, expiry, failed inserts, independent root lifetime and consent.
Two property tests generate 384 forged scopes and execution fences.

The local C1 contract adds ProvisionPolicy.AuthorizeProvision and
Store.SetProvisionPolicy. The configured provider verifies bootstrap/enrollment
or Actions evidence through the issuing transaction's authority resolver. A
missing provider returns unavailable. This boundary is needed because W2 owns
the external proof mechanisms. Hard-coding their evidence handling in Store
would duplicate that owner. Removing the policy field, setter and call reverses
this addition. No foreign contract type changed. W2 must supply the real provider.

Added the passing SQLite cell
TestCredentials/credentials-operator-console-split/store-sqlite. It checks trusted
operator/service issuance, rejection of console grants, rollback of refused
issuance, and independent root revocation. Origin verification is a fake. The
native route matrix remains unimplemented. No archived test was ported in this
continuation. Earlier ports remain listed in T1.

Retired seven raw Store entry points. ListRegisteredAgents now belongs to Tenant.
ListWaitingNodes, SetNodeAttention and CountConcurrencyCache belong to Operator.
Deleted the unused SetRetriedAs, ActiveConcurrencyHolder and
ListSpawnedChildrenByRun wrappers: **9 implementation lines**, plus their
obsolete documentation. Their tenant methods remain for legacy callers. No
forwarding shim was added. The 22-line member-only provisioning implementation
was replaced by the origin-aware issuer.

The production caller migration outside pkg/store is
pkg/controller/claim_attention.go. Its fixture change is
pkg/controller/claim_attention_internal_test.go. Other shared files are
internal/acceptance/store_credentials.go, tests/acceptance/credentials_test.go,
design/rewrite/contracts/c1-data.md and t1-port-inventory.md. Inside pkg/store,
store.go gains only the policy dependency and wrapper removal. concurrency.go
and claim_attention.go carry the handle changes. Their existing tests and the
SQL-scope guard follow those changes. W1b files are unchanged.

Final verification uses GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-astra-W1/cache, GOTMPDIR=/tmp/sw-astra-W1/tmp and
TMPDIR=/tmp/sw-astra-W1/tmp. Package parallelism is -p 4.

- Whole-repository go build -p 4 ./... passes.
- go vet -p 4 on pkg/store, pkg/controller, internal/acceptance and
  tests/acceptance passes.
- The earlier exact store selection plus provision_origins_test.go passes
  254 top-level tests / 716 cases, without skips. The filenames for that
  selection remain under Earlier verification.
- Controller -run '^TestEngine' passes 33 top-level tests / 56 cases.
- Full tests/acceptance passes 20 top-level tests / 205 executed cases, with
  2,995 native/platform skips. internal/acceptance has no package tests.
  Full pkg/match passes 14 top-level tests / 42 cases.
- go run ./internal/commentcheck -base HEAD . is clean across 17 Go files.
  The required -base main run still reports the pre-existing detached HereBearer
  comment in internal/controllerwire/contracts.go. That W9 file is unchanged.
- Edited Go files are formatted. git diff --check passes. No Git writes ran.

Native PostgreSQL, HTTP/api.sock, Windows/Darwin, Docker, race, lint, delivery
pipelines and independent review remain lead checks. The legacy full store suite
is still not claimed green. The existing final-schema/legacy-fixture mismatch
remains outside this selection. No extra compatibility path or schema was added.
The provisioning API has component coverage through its origin-policy fake.
The real bootstrap gate, persisted enrollment/OIDC evidence and transport
registration are unverified integration points.

## Earlier continuation

Fixed authorization reads that requested PostgreSQL row locks inside read-only
snapshots. The invariant is that reads use one consistent snapshot, while writes
lock current ancestry through commit. `storeTx` now records its snapshot mode.
Its existing lock helpers omit row locks in that mode. Write transactions keep
all three lock forms. A SQL-driver regression first failed with a row-lock error,
then passed through the public binding adapter. It also verifies that mutation
authorization locks both ancestors. This simulates PostgreSQL's read-only lock
refusal over real SQLite data. Native PostgreSQL remains untested.

W1's binding adapter now calls W1b's implemented transaction DataAccessChecker
directly. The unavailable type-assertion fallback is removed. Tests check receipt
deletion in the owning transaction, rollback ownership and successful public
snapshot checks. A property generates 96 forged principals and varies foreign
teams, unrelated digests, leaf/parent revocation, generation and lease expiry.
Both data binding rules must honor the persisted authority and read edges.

Added two active SQLite store acceptance cells:

- `TestRecovery/recovery-cancel-beats-loss/store-sqlite`
- `TestRecovery/recovery-reconciler-waits-and-reclaims/store-sqlite`

They use final-schema authority fixtures and a contract fake for enrollment.
Actual store transitions, reaping, reconciliation, attempts and events supply
the results. Cancellation keeps a live holder, wins over subsequent loss and
rejects late success. Both host-first and reaper-first orders refuse stale host
and holder reports. Reconciliation waits before reaping, survives reopening,
returns Dispatch for a memo miss and awards one self-claim under two concurrent
reconciliations. Attempt history and application retry budget survive requeue.
Exact job-event lists and state sequences check duplicate emission. The native
placement cells stay skipped because these proofs start below HTTP and processes.

Ported the two archived state-machine tests from 7b1fc6d111c272612d4c970d061063a369598e30:
TestNodeRules_TheReaperEndsACancelledRunsLostNodeAsCancelled and both orders of
TestNodeRules_AnAdoptedRowIsNotFinishedTwice. Their old bug-dependent skips and
retry-run behavior are absent from the new proofs. T1's inventory names the cells.

Removed the unused egress_day table and both classifications: **11 legacy
schema/classification lines**. There are no runtime callers to migrate for that
table. The final baseline has 83 tables, 101 explicit indexes, 66 tenant tables
and 17 operator tables. Tenant egress stays in egress_usage. Also removed seven
narrative comment lines from two W1 private interfaces. No legacy runtime API
was retired in this continuation.

## Existing W1 implementation

The earlier committed work includes intake and both seals, plan/expansion
acceptance, the pure scheduling projection and decider, fenced node/run
transitions, node-wide attempts, claims and matching, reconciliation, owner/unit
reaping, continuous concurrency adoption, completion intents, singleton recovery
and operational retention. Membership, ancestry and source credential release
are rechecked in the owning transaction. Source release commits its audit before
returning the sealed credential. Owner consent requires the current machine owner.

The controller adapters cover intake, acceptance, private execution declarations,
claims, exchange, renewal, expansion, owner snapshots, persisted public detail,
failure reads, compatibility, signed version notices and elected controller
recovery at startup and maintenance ticks. Instance identities fence controllers
on the same machine. Public detail uses shared display helpers and excludes
private seals, credentials and storage locations. Engine and wire policy owners
remain external integrations where their implementation is absent.

## Remaining W1 work

1. Integrate lifecycle accounting with W1b's reservation, interval and settlement
   owner. Metered roots still fail closed at award. persistBillingAwardTx is the existing
   unmetered path. Launcher awards must become metered regardless of root settings
   when that integration replaces the path. Award, start, waits, renewal, release, terminal reports, owner
   finish and reaping must use the shared transaction contracts. C2 still owns
   the billing calculator. Do not edit W1b's implementation files.
2. Supply materializationRecords and nodeResultRecords from W1b's record owner
   inside their calling transactions. Existing component tests use fakes. Public
   record methods that start another transaction cannot satisfy this requirement.
3. Install W2's real ProvisionPolicy for bootstrap, machine/service enrollment
   and Actions evidence. The store implementation and account-owner persistence
   are implemented and tested through contract fakes.
4. Retire StateStoreOld, the legacy plan/claim/trigger writers, offer rounds and
   the remaining raw Store twins after migrating or deleting their callers.
   W3/W8 runtime callers and W1b billing tests still reference legacy APIs.
   RunReaperLeased remains in W1b's storage-lease file. No forwarding shim exists.
5. Complete controller and maintenance integrations for completion delivery,
   record retention, upload quota release and physical GC through their owners.
   Owner reaping and controller-owned run reconciliation already run under
   elected leases. W7 owns physical storage and W2/W9 own transport registration.
6. Activate native placement cells after the HTTP, host and process drivers exist.
   The acceptance New and Run driver bodies remain stubs. The two SQLite cells
   establish store behavior only. Main still contains 2,995 native skeleton skips.

## Contract changes needed in other areas

These are existing gaps. This continuation changes no foreign contract shape.

- W9 ReuseSource cannot name MaterializeSource.Result by job/state sequence.
  ScheduleSnapshot cannot carry GuardRefusal. JobResult cannot carry the recorded
  JobFailure stage. The implemented adapters refuse missing required state.
- W2 must carry the authenticated exact request-body digest and operation into
  result transactions for terminal HTTP replay. StateDigest is a transition
  digest and cannot replace it. Binding.Result remains unfilled.
- W2 must supply trusted console, peer and configured-anonymous persistence and
  origin evidence. ReadAuthentication supports persisted bearer/session lookup.
  Node-host process liveness belongs to C3.
- W2 Target cannot carry the resolved concurrency key for BindSlotOfBoundNode.
  That rule continues to return unavailable. Transactional data binding is now
  integrated and no longer belongs on the missing-contract list.
- W8/W9 must specify launcher observation correlation across claim generations
  and what LauncherSyncResponse.Remove names. UnitID, JobName and NodeUID do not
  include a generation, and C1 has no committed Kubernetes identity mapping.
  SyncLauncher remains a declaration.
- Generation-zero materialized generator sources still need a consistent fence
  contract. Materialization may retain zero before any award, while eval units,
  claims, owner logs and ExpansionForStore require positive source generations.
  This remains an inspection finding, without a native runtime reproduction.

W2 must install the intake policy, source policy/decryptor, plan secret opener,
enrollment resolver, provisioning origin policy and real route authorization. The separate GitHub App
credential mint/audit path remains open. W8 must supply the real engine resolver
for SignedRunnerAdmission and SignedEngineVersionPolicy. W9 must implement the
CompatibilityFor calendar helper. W6 concurrency resolution is tested through
its contract fake. Other W6 settings/seals have real integration coverage.

The lead still needs to ratify the existing status-read choice: unavailable
version policy returns an error rather than partial detail. No new customer
restriction or human decision was introduced in this continuation.

## Earlier verification

Commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-astra-W1/cache, GOTMPDIR=/tmp/sw-astra-W1/tmp and
TMPDIR=/tmp/sw-astra-W1/tmp. Test package parallelism is at most -p 4.

- Whole-repository `go build -p 4 ./...` passes.
- `go vet -p 4 ./pkg/store ./pkg/controller ./pkg/match ./internal/acceptance
  ./tests/acceptance` passes.
- The exact store selection passes 248 top-level tests / 695 cases, without skips.
- Controller `-run '^TestEngine'` passes 33 top-level tests / 56 cases.
- Full internal/acceptance, tests/acceptance and pkg/match pass. Acceptance has
  20 top-level tests / 204 executed cases and 2,995 native/platform skips.
- SQL-scope and tenant-writer guards pass. OpenAPI drift and diff whitespace
  checks pass. Edited Go files are formatted.
- Required `go run ./internal/commentcheck -base main .` reports one pre-existing
  detached HereBearer comment in internal/controllerwire/contracts.go. That W9
  file is unchanged. The two W1 findings from the prior report are removed.
  `-base HEAD` is clean for this continuation.

To reproduce the exact store selection, extract top-level Test names from these
files under pkg/store, join them into an anchored alternation, and run
`go test -p 4 ./pkg/store -count=1 -run "$expression"`:

    authority_runtime_test.go authority_adapters_test.go rewrite_authority_test.go
    rewrite_schema_test.go state_runtime_test.go scheduling_contract_test.go
    scheduling_guard_test.go concurrency_adoption_test.go maintenance_rewrite_test.go
    expansion_runtime_test.go expansion_success_test.go owner_reaper_runtime_test.go
    unit_deadlines_test.go unit_release_test.go result_runtime_test.go intake_plan_runtime_test.go
    unit_claim_test.go unit_claim_lock_test.go reconcile_runtime_test.go materialize_runtime_test.go
    state_result_records_test.go retry_recovery_runtime_test.go publication_runtime_test.go
    unit_renewal_test.go queue_recovery_test.go tenant_sql_scope_guard_test.go
    controller_recovery_test.go owner_liveness_test.go owner_dialect_test.go
    finish_unit_runtime_test.go approval_reaper_runtime_test.go owner_reclaim_runtime_test.go
    node_reaper_dialect_test.go recovery_matching_test.go registered_agents_test.go
    waiting_nodes_test.go amendment_reconcile_test.go amendment_claim_test.go
    failure_runtime_test.go authentication_runtime_test.go plan_settings_integration_test.go
    provision_runtime_test.go delegation_runtime_test.go source_intake_test.go
    unit_local_claim_test.go binding_runtime_test.go classification_race_test.go
    source_release_test.go run_view_test.go maintenance_retention_test.go retention_run_test.go
    authorization_snapshot_test.go binding_data_integration_test.go blob_data_access_test.go

The broader prefix selection also ran a legacy TestOwnerLookupsKeepARunAndATriggerSharingAnIDApart
fixture. It fails while creating a legacy run because sparkwing_meta is absent
from the final schema. The exact W1 selection above excludes that old fixture.
The full legacy suite is not claimed green. Native PostgreSQL interleavings,
HTTP/api.sock, Windows/Darwin, Docker, race, lint, delivery pipelines and independent
review remain lead checks. Aggregate API snapshots and the rewrite changelog
remain integration work.

## Earlier shared files, mechanisms and friction

Shared runtime support changed only in pkg/store/dialect.go, capabilities_contract.go,
schema.sql, schema_contract.go and tenant.go. Other owned store changes are the
binding adapter, its tests and private-interface comment cleanup. Outside
pkg/store, this continuation adds internal/acceptance/store_recovery.go and
changes tests/acceptance/recovery_test.go. No other package's production caller
was migrated or deleted. C1, T1 and this handoff document the evidence.

The only runtime mechanism added is the private transaction snapshot-mode bit.
Changing the binding entry point to an ordinary transaction would lose its
consistent snapshot and leave owner/data snapshot readers with the same error.
The bit preserves the existing read contract. No setting, environment variable,
service, compatibility alias or billing policy was added.

The PostgreSQL lock regression ran red before the fix. New recovery fixtures
needed nullable empty capability projections, a reaper actor for run cancellation
and an enrollment fake for self-claim. The first port-reference note also needed
its scenario suffix to satisfy the existing drift check. These were test/doc
corrections. No production workaround was added for a fixture.

No Sparkwing binary, provider call, daemon, Git write, deployment or release ran.
W1b source files are unchanged. Scratch is deleted before the final report.

STATUS: CONTINUE
