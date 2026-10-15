# W1 store handoff

Continues 5912f1cfb. These changes are uncommitted. No Git write or Sparkwing
executable was run. W1b's accounting, billing, blob and log files are untouched.
The lead owns committing, independent review and delivery pipelines.
Earlier session details remain in the branch history and rewrite logs.

## Implemented

Verified App redelivery now returns the original admission for that verified
delivery and pipeline, including without a client idempotency key. Lookup and
insertion use the existing team admission lock. Replay reads the persisted
request and run number. It consumes no new number and rechecks live authority.
Client-key and delivery-key replays share the existing result projection.

The regression first failed on the signed replay-key uniqueness constraint.
After the replay lookup was implemented, its second-pipeline assertion exposed
the separate raw-delivery uniqueness constraint. Both behaviors now pass,
including four concurrent redeliveries of an existing admission.

Client-key replay also rechecks authority after input hashing. The new test
advances the injected store clock to the credential's expiry during
hashing. It returned the prior run before the fix and now returns
unauthenticated. Ordinary admission and verified-delivery replay already
performed a later check.

ReleaseUnit's existing cancellation refusal is covered for unstarted trigger,
plan and eval work. The test cancels through TransitionRun, checks ReleaseUnit
returns lock_held, and observes no extra state event. The suspected missing
check was disproved: loadAwardRowTx already rejects a cancelled run, so no
production release change was needed.

## C1 contract repair and deletions

Removed idx_triggers_webhook_delivery from the final schema because a single
verified delivery can start several pipelines. The existing unique
(team, webhook_replay_key) index remains the replay fence. The provider's
unsigned delivery header remains audit data. No migration or intermediate
schema version was added.

Removed TriggerWebhookDeliveryIndexName, its private column constant and the
legacy raw-header collision branch. FindTriggerByWebhookReplay now accepts
only the signed replay key. Its unsigned-header fallback is deleted. All four
controller callers were migrated by removing that argument. No alias or
forwarding API was added.

Updated C1's intake invariant, source API comments, the two affected API
snapshot entries, and the changelog. The documentation mirror was generated
with bin/sync-docs.sh. No other area's contract was changed.

The Go diff deletes 58 production lines and 2 test lines, and adds 44 production
lines and 109 test lines. Production Go is 14 lines smaller. The schema loses
two lines. These counts exclude documentation, snapshots and this handoff.
The obsolete unsigned-header uniqueness case was removed from the mixed legacy
trigger-team test. No archived test was newly ported in this session.

No new public interface, setting, flag, table, environment variable, runtime
service or retry mechanism was added. The private admission result helper
shares the existing replay conversion between the two admission identities.

## Verification

Go commands use GOWORK=off GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-astra-W1/cache and GOTMPDIR/TMPDIR=/tmp/sw-astra-W1/tmp.
Test parallelism is at most -p 4. The scratch directory is deleted at handoff.

- Whole-repository go build -p 4 ./... passes.
- go vet -p 4 ./pkg/store ./pkg/controller passes.
- The W1 selection passes 174 top-level tests and 505 cases including subtests,
  with no skips or failures. The final run took 10.455 seconds.
- All TestEngine controller adapter tests pass. This also compiles the changed
  legacy webhook caller and the controller's test package.
- Acceptance catalog, port inventory/destinations, final credential-route
  declarations and invariant coverage checks pass. They execute no runtime cell.
- Documentation mirror and git diff --check pass.
- No repository-local user.name, user.email or core.worktree override exists.

To reproduce the W1 selection, extract top-level Test names from these files
under pkg/store, join them into an anchored -run expression, then run
`go test -p 4 ./pkg/store -count=1 -run "$expression"`:

    authority_runtime_test.go authority_adapters_test.go rewrite_authority_test.go
    rewrite_schema_test.go state_runtime_test.go scheduling_contract_test.go
    scheduling_guard_test.go concurrency_adoption_test.go maintenance_rewrite_test.go
    expansion_runtime_test.go expansion_success_test.go owner_reaper_runtime_test.go
    unit_deadlines_test.go unit_release_test.go result_runtime_test.go
    intake_plan_runtime_test.go unit_claim_test.go unit_claim_lock_test.go
    reconcile_runtime_test.go materialize_runtime_test.go state_result_records_test.go
    retry_recovery_runtime_test.go publication_runtime_test.go unit_renewal_test.go
    queue_recovery_test.go tenant_sql_scope_guard_test.go controller_recovery_test.go
    owner_liveness_test.go owner_dialect_test.go finish_unit_runtime_test.go
    approval_reaper_runtime_test.go owner_reclaim_runtime_test.go
    node_reaper_dialect_test.go

The tests use real SQLite transactions, capability persistence, admission and
state APIs. W4/W6 plan validation, sealing, hashing and settings services remain
contract fakes. The verified App delivery policy is a fake. These checks do not
prove webhook HMAC validation, HTTP middleware, native hosts, billing settlement
or blob persistence. Concurrent redelivery here targets an existing admission;
it is not a PostgreSQL first-insert race proof.

The full legacy suite, PostgreSQL, race checks, lint, pipelines and independent
review remain lead checks. SPARKWING_TEST_PG_URL is unset. Full API snapshots
have pre-existing rewrite drift. Only this change's entries were adjusted.

## Remaining W1 work and other-area contracts

No native acceptance cell was activated. internal/acceptance.New and Run still
panic. T1 requires a driver and assertions before removing a skeleton skip.
Final package proofs pass for admission, acceptance, claims, reconciliation,
recovery and the state machine. W1 remains incomplete until the remaining
legacy callers disappear and native acceptance runs.

Unchanged contract gaps:

- W9 ReuseSource cannot carry MaterializeSource.Result by job/state sequence.
- W9 ScheduleSnapshot cannot carry the sealed GuardRefusal.
- W2 Authorized/IntakeForStore cannot carry the verified App delivery identity.
  The new store redelivery proof uses the existing IntakePolicy contract.
- EngineController.Claim lacks trusted enrollment-capacity and signed
  runner-policy dependencies in its adapter contract. ClaimProfileResolver
  and engine.Resolver.Index/EvaluateRunner are the owning contracts. Body
  labels or reported availability cannot substitute for those dependencies.

Real integration remains for W4/W6 plan validation and seals; W2/W3/W8/W9
routes, hosts, runners and keepers; and the controller loops calling ReapUnits,
ReconcileControllerRuns and completion delivery under their singleton leases.
W1b must join metered reservation/settlement to award, renewal, result and owner
completion, and implement transactional materializationRecords and
nodeResultRecords. Those interfaces remain the boundary for W1 tests.

StateStoreOld and old plan/claim/trigger APIs still have legacy callers.
W1b billing tests retain sweep/requeue callers. RunReaperLeased remains in
W1b's storage-lease file. No W1b caller or implementation was changed.

## Shared files touched

- pkg/controller/github_app_webhook.go: four caller argument removals.
- .apidiff/pkg_store.txt: removed constant and changed lookup signature only.
- CHANGELOG.md and its generated pkg/docs/changelog.md mirror.
- design/rewrite/contracts/c1-data.md: the local schema/admission correction.
- This handoff.

All remaining source and test edits are in pkg/store. The schema change only
removes the admission index; W1b's schema definitions are unchanged.

## Friction

The first cancellation test fixture contained a running job without a complete
claim. It was corrected to an already terminal job before exercising the public
cancellation API. The corrected test passed without a production change.

A broad -run '^TestAdmission' also selected the pre-existing legacy
TestAdmission_PlanningUntilThePlanIsAccepted, which fails on the removed
sparkwing_meta table. The final W1 selector names the final-contract tests
explicitly. The legacy test was not retried or reported as a pass.

The redelivery and authority-expiry failures described above were reproduced
before their fixes. No socket-dependent test, provider call, candidate runtime,
Git write or Sparkwing pipeline was attempted.
