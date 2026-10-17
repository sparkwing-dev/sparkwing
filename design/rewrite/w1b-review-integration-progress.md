# W1b review integration continuation, 2026-10-17

Implemented the four blocker fixes from W1b-opus-review.md and the seven
should-fix entries. PostgreSQL lock correctness still needs the lead's native
run of this tree. The RepoInherited display assertion and earlier integration
and legacy-cut work remain open.

## Changes and invariants

- Slot calls use the authenticated capability's unit kind. A controller-dispatched
  run no longer looks for a trigger-kind owner row. Mutations take authority
  before owner, run and node locks. The helper states the common order.
- Schedule firing takes FOR UPDATE on its schedule before the intake lock.
  Transaction aborts leave its cursor and fire history unchanged for retry.
- Child intake uses the real ResolveIntake policy. It returns 422 outside the
  parent's current repository scope and accepts an explicit source covered by
  that scope. Generated cases exercise both decisions. childIntakePolicy is gone.
- Tick launches retain the listed declaration for the committing snapshot check.
  Paused rows cannot fire unattended. A row needs stored arming consent or a live
  App installation and automation grant. Raw declarations grant no execution.
- Public cron tests use the real writer and controller policy. Cadence refusal
  maps through writeComputeLimitRefusal to 429. Both caps return 409 and use
  internal/limits. A store test commits ten repositories and refuses the eleventh.
  The PUT handler bounds entries before source lookup. Verified local peers
  retain their cap exemption, proved through their real transport and store.
- Group and holder reads use a read-only snapshot. The foreign leader test uses
  a real accepted declaration, authentication, acquisition and store refusal
  rather than waiterLeaderFake. Foreign and missing leaders return 403 alike.
- API submissions and retries use per-token flood buckets, including multiple
  tokens for one account. App deliveries retain their team bucket. C1 replay
  lookup precedes flood charging. Explicit and content replay cannot exhaust
  the bucket. A store refusal remains retryable inside the dedupe window.
- Unpriced jobs fail with unpriced_cpu_class without an award, attempt or charge.
  This covers named and queued work, a small poller, and matching labels.
  Unmetered work can still run above the priced classes. Failure goes through
  the existing state owner with current authority and price checks.
- A failed local-registration commit removes new files and restores prior pins
  and registration metadata. Release no longer uses the caller's outcome to
  delete legacy coalesce waiters. That policy is already forbidden by the final
  schema. Host-name and canonical credential_unconfirmed assertions are retained.

No integration-deleted file was restored. OpenAPI conflict contents were
resolved by regeneration. Its index entry remains unmerged because the swarm
brief prohibits Git writes. The lead must stage the resolution and commit.
Lead-owned amendments and handoffs are unchanged. HEAD remains ba495a99a and
MERGE_HEAD is aece4e39e.

## Tests and evidence

Regressions ran red before their fixes for controller-dispatch slots,
unauthorized child repositories, consumed aborted schedule instants, paused or
unconsented firing, cadence mapping, the entry bound, stale tick listings,
per-token buckets, charged replays, unpriced termination, matching evidence,
registration rollback, credential reasons and the local cap exemption.
The three PostgreSQL regressions were written first but skipped here because
SPARKWING_TEST_PG_URL is unset. No pre-fix or post-fix PostgreSQL execution is
claimed.

The PostgreSQL tests use overlapping real transactions and pg_blocking_pids
barriers, without sleeps:

- TestPostgresConcurrencyLocksAuthorityBeforeWork
- TestPostgresConcurrencyReadsDoNotTakeWorkWriteLocks
- TestPostgresScheduleConcurrentFiresCommitOneRunWithoutDeadlock

Passed local selections before the final checks: the broader store
Concurrency/Schedule/Unpriced/Metered slice in 36.951s, the Node/Plan/State/Unit/Claim
slice in 44.727s, controller cron/child/credential/flood checks in 1.530s, and
all internal/crons tests in 3.486s. The focused package matrix passed store in
23.126s, controller in 3.625s, crons in 0.489s, apiref in 0.017s and apispec in
0.189s. Focused race checks passed store in 16.397s, controller in 2.462s and
crons in 2.183s. Later narrow checks cover the private pricing proof, SQL scope
guards and verified local cap exemption. Final check results are recorded below.

Final checks passed: whole-repository build, scoped vet, unfiltered apiref and
apispec drift suites, commentcheck across 2,802 Go files and whitespace checks.
The final race selection passed store in 16.018s, controller in 3.049s and crons
in 2.109s. It includes the private pricing proof and the extended local transport
case. SQL scope, declaration and final pricing/local-exemption properties pass.
The same-account per-token test passes. The final renamed controller-dispatch regression also passes (0.04s).
The three PostgreSQL cases remain skipped locally for the missing URL. Their
pools allow eight connections so the lock barriers cannot serialize through a
single connection.

Tests use GOWORK=off, GOFLAGS=-buildvcs=false, GOCACHE=/tmp/sw-gocache,
GOMAXPROCS=4, -p 4 and scratch under /tmp/sw-astra-W1b. Reproduce the broad store
selections with go test -run 'Concurrency|Schedule|Unpriced|Metered' and
-run 'Node|Plan|State|Unit|Claim'. Run the three PostgreSQL names above against
the lead's configured server. The local transport proof uses net.Pipe and a
verified peer. Native socket proof remains the lead harness's responsibility.

TestStoreScheduleBoundary, the retained schedules/store-sqlite boundary, passes.
No acceptance cell was newly unskipped. The native report read at session start
has no FAIL, but covers the previous committed state, not these changes.

## Contract changes and remaining work

C1 records IntakeQueueDepth and the claimant/unpriced state edge. Its private
transaction proof preserves the failed poller's matching inputs and prevents
public TransitionNode callers from manufacturing pricing failures. It replaces
the deleted raw failure writer. No new service, setting, limit or environment
variable was added. C2 records the restored flood semantics, child source scope,
schedule statuses, snapshot rules and the local exemption. Existing interface
signatures are unchanged. Generated API snapshots and references were refreshed.

Remaining work, in addition to the native PostgreSQL and socket run:

- RepoInherited: current tests prove exact sealed parent Git and Source inheritance
  and preservation of an authorized explicit source. The literal legacy display
  bit is still unset. Restoring its implicit-versus-explicit distinction needs a
  sealed lineage marker and native-client propagation, rather than restoring the
  deleted SQL column. This nit is not claimed closed.
- The native client's cross-repository source resolution remains a refusal stub.
  The controller policy and store boundary are implemented and tested against
  broader source authority, but do not supply that client resolver.
- Private branch following still needs audited controller source credentials
  before revision lookup. HTTP box identity and accepted memo namespaces remain
  the C1 integration gaps already listed in the contract. Their remaining legacy
  branches still need retirement. No new foreign contract blocker is introduced.
- The combined store area remains over budget. No budget waiver or engineering
  completion is claimed.

## Assumptions and mechanisms added

- A private matching proof and guarded pricing edge replace raw terminal SQL.
  Raw SQL was smaller but bypassed the state owner, current scope and event
  transaction. Removing the edge and proof would restore pending price refusals.
- Local registration temporarily preserves existing files. Deleting files on
  failure was smaller but would destroy a valid pin on failed re-registration.
  Removing the preservation helper would restore the old cleanup behavior.
- The existing flood check runs inside intake policy after replay lookup.
  Merely moving the HTTP calls could not distinguish a replay from an uncommitted
  dedupe key. The transaction queue reader also avoids a nested store connection.
  Removing that reader and restoring the handler calls would undo this change.

## Files and volume

Shared production edits are internal/crons/{local,tick}.go and controller
{crons,engine_routes,engine_lifecycle_routes,flood,intake_policy,schedule_routes}.go.
Store edits cover concurrency_authorized, dialect, intake_settings, node_state,
schedule_intake, schedule_transactions, source_release, state_authority,
unit_billing, unit_claim, unit_claim_rows and the new unit_unpriced.go.
The caller migration removes ScheduleWriterFixture from crons_test.go.
Recovery fixture SQL now rebinds for PostgreSQL. Other touched tests are the
schedule, cron, concurrency, flood, child/cycle, pricing and git-credential files.
Shared prose is C1/C2, docs/security.md and docs/observability.md. Generated files
are api/openapi.yaml, .apidiff snapshots and the two documentation mirrors.

Own session production Go: +269 / -65, net +204.
Combined W1/W1b store versus main: -329 production lines,
3921 over the -4,250 budget, using the lead's exclusions and including
untracked production source. These are working-tree measurements, including
the incoming integration merge.

The additions beyond the frozen sizing are concrete launch-scope adapters and
billing policy. This session adds the guarded pricing edge and filesystem rollback
required by the lead's rulings. It removes the fake boundary helpers and obsolete
mutation branches, but retires no whole legacy production file. The inherited
namespace, box and source integration gaps still prevent the corresponding cuts.
The lead must judge the remaining overage and carry it to Korey if retained.

Friction: Git staging is prohibited by the swarm brief. Native sockets are
unavailable and no PostgreSQL URL is configured here. One compile lost shared
cache entries during execution. Rebuilding passed, and the shared cache was kept.
Scratch is removed at session end.

STATUS: CONTINUE


## Continuation after native session 1, 2026-10-17

This section supersedes the open RepoInherited item above. The lead committed
the earlier merge at 9457be43f, and no unresolved conflicts remain. The four
blocker fixes remain intact. No lead-owned amendment or handoff file changed.

### Implemented and removed

- Sealed `Lineage.RepoInherited` distinguishes an omitted child repository from
  an explicit equal repository. The native client carries it. C1 checks the
  parent repository, seals the bit and reports it through run/trigger readers.
  Changing it under one replay key conflicts. Generated real-policy cases prove
  that it grants no source authority and cannot label a foreign repository as
  inherited even when that repository is otherwise authorized.
- Removed inherited-holder IDs, synthetic zero-cost holders, cost transfer,
  recursive supersession and their five obsolete tests. Both live writers
  already refused the selector. A real HTTP/store regression ran red at 403 and
  now requires 400 with no capacity row. Weighted costs, restrictive capacity,
  queue/backfill, cancellation and lease-continuity tests remain.
- Removed schema-forbidden coalesce admission, waiter promotion and draining,
  and the unused coalesced/cached outcomes. Real memo/result materialization and
  authorized leader-read transactions remain. No blob result-sharing path was
  removed.
- Moved the implemented capacity-adoption types and transaction unchanged from
  concurrency_contract.go to capacity_adoption.go. The scaffold is deleted.
- Reconciled three native failures with current contracts. The pricing property
  accepts C3 capacity observations and proves every generated launcher report
  reserves the sealed 16-core class at 70,000 microcredits/second. Publication
  uses a valid hex SHA. Complete source-bundle checkout needs no stored Git
  credential and requires zero credential-release audits while retaining exact
  SHA, unique-byte charging and fenced-read assertions.
- The tenancy fixture's approval and concurrency inserts now use placeholders
  accepted by both dialects. The PostgreSQL syntax failure was fixture SQL.
  The real cross-team response assertions remain.

### Verification

All commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOCACHE=/tmp/sw-gocache,
GOMAXPROCS=4, -p 4 and task scratch for TMPDIR/GOTMPDIR.

Passed: whole-repository build, scoped vet, full runspec and controller/client,
full internal/crons, full apiref/apispec and cmd/apidiff, commentcheck across
2,802 files, and whitespace checks. Generated OpenAPI and public API snapshots
are refreshed.

Store selection `Concurrency|Capacity|Cost|Materializ|Memo|Reporting|LatestRun|Retry|Schedule`
passes in 31.497s. It includes TestStoreScheduleBoundary. The earlier focused
capacity/materialization selection passed in 22.097s. The controller selection
`^(TestCredits_|TestProfilePublication|FuzzProfilePublication|TestSourceSnapshot|TestChildIntake|TestClaimRun_Child|TestEngineClaimHTTP|TestTeamBoundary_Concurrency)`
passes in 5.747s. The acceptance selection
`API|Drift|Snapshot|Concurrency|Child|PortInventory|MaterializedResult` passes in
0.925s. No native placement cell was newly enabled.

The full nodehost suite fails on three TestBurstSharedOwner groups,
FuzzBurstMachineWaitNeverConsumesClaim seeds and
TestOwnerDrainsPendingSupersessionIntent. A read-only Go overlay replacing every
changed Go file with HEAD reproduces all five failures. Burst fixtures refuse
with "burst work is reserved for the requester and selected Cloud executors".
The owner-drain case reaches its one-second context deadline. These are existing
integration failures for W3/W9, not claimed fixed here. The focused nodehost
Concurrency/Capacity/Cost selection passed earlier.

Focused race checks pass: store 96.336s, controller 25.404s and client 1.081s.
The race selection is
`TestConcurrencyHTTP|TestAuthorizedConcurrency|TestChildIntake|TestNativeChildPreserves|TestCredits_|TestSourceSnapshotRealClientStorageAndIntake|TestProfilePublicationImports|TestStoreScheduleBoundary`.
The three existing PostgreSQL lock tests skip because SPARKWING_TEST_PG_URL is
unset. No native PostgreSQL success is inferred from those skips.
The native harness report still contains FAIL. Engineering completion and a
clean native run are not claimed.

### Contracts, shared files and remaining work

C1 documents sealed inheritance and the legacy capacity cuts. C3/C4 document the
optional lineage bit. The port inventory records the retired tests and their
replacement proofs. Integration authority covers these changes. No service,
setting, limit, environment variable or compatibility surface was added.

Shared production files: internal/runspec/contracts.go,
pkg/controller/client/native_services.go, pkg/controller/concurrency.go,
pkg/store/capacity_owner.go, pkg/store/intake_runtime.go,
pkg/store/latest_run.go and pkg/store/store.go. The capacity_owner change only
migrates the removed request field. Shared fixture files:
pkg/controller/{server_test,team_boundary_test,publication_http_test,source_upload_test,credits_claim_reconciliation_test}.go.
The W2 run-existence privacy mechanism and its tests are unchanged except for the
dialect-safe fixture insert. Generated files: api/openapi.yaml and
.apidiff/pkg_store.txt.

Remaining real integration points: native client cross-repository/ref resolution,
audited credentials before private followed-branch lookup, persisted HTTP box
identity and accepted memo namespace metadata. Missing authority still refuses.
The rest of the legacy cuts and combined store budget remain open. The lead
harness must verify this tree on PostgreSQL and native sockets and resolve its
remaining W2 privacy failures. The nodehost baseline failures above also remain.

### Volume and assumptions

Production Go this session, counting the unchanged 138-line move on both sides:
+221 / -639, net -418. Excluding that move: +83 / -501. The five obsolete tests
and unused fixture helper were removed. No live accepted feature was cut.

Combined W1/W1b store versus main is -606 production lines, 3,644 over the -4,250
budget, including the untracked replacement file and using the lead's path
exclusions. Moving the 138 implemented lines out of a contract-named file makes
them count in that budget. No budget waiver or completion is claimed.

The new lineage bit is retained because sealed source equality cannot distinguish
an omitted repository from an explicit equal override. Removing it would lose
that reporting distinction. It is metadata and grants no authority. There is no
new execution mechanism.

Friction: the sandbox blocks native listeners and has no configured PostgreSQL
URL. Git writes remain prohibited, so the lead must stage and commit. All writes
are within this worktree or task scratch. Scratch is removed and the shared build cache retained.

STATUS: CONTINUE


## Continuation after native session 2, 2026-10-17

### Implemented and deleted

The four review blockers and real-policy/store replacements were already committed
at 40bd95239647d3199109e45fceca34845fdf5c97. The session-2 native report passes
both store lanes, the focused controller lane and local-controller/native
packages. Its remaining failures are the run-existence privacy properties
assigned to W2. Those tests and their expectations are unchanged here.

Invariant: a reported output digest comes from that job's exact committed result
selector. Foreign team/job selectors reveal no digest. GetNode and ListNodes now
resolve the selected record. Receipts use its digest, including after memo
materialization and deletion of the source run and memo. The first real-store
reporting test ran red on both readers with a missing digest, then green.
Generated cases exercise foreign selectors, including an existing unrelated
job's real committed record. Collected records omit the digest.

Removed OutputRef, output-prefix helpers and unused writers for the deleted
node_outputs/data_objects tables. Removed direct-upload key/SHA validators with
no remaining callers. Replaced the private-reference limit fuzz with a real
Tenant.CommitRecord fuzz, including boundary values and rollback on oversize.
The memo-limit and manifest tests remain. Empty blobs follow the current blob
contract's nonnegative-size rule.

Removed unused nodeChargeTx, old-plan decoding and profile-fallback pricing, and
three tests of that retired reader. The real-claim resource-pricing property now
sets a conflicting 96-core profile pin before each claim and still proves exact
sealed class, reservation, immutable rate, settlement and ledger survival after
run deletion. Removed the ExecutorResource alias and migrated its callers to
match.Resources. Removed unused lockTeamStorageTx and NodeLogSealGrace. Moved
the test-only holder lease predicate out of production.

Moved implemented step/annotation/summary declarations into result_metadata.go,
removed its three forwarding wrappers and deleted result_contract.go. The public
methods now are the existing transaction implementations. Their behavior is
unchanged.

### Verification

Commands use env GOWORK=off GOFLAGS=-buildvcs=false GOCACHE=/tmp/sw-gocache,
GOTMPDIR=/tmp/sw-astra-W1b/go-tmp TMPDIR=/tmp/sw-astra-W1b GOMAXPROCS=4,
go -C /home/bombuntu/.xwing/envs/1088/sparkwing and test -p 4.

- Whole-repository build passes after the API removals and wrapper cut.
- Scoped vet passes for pkg/store, pkg/controller and receipt.
- Full SQLite pkg/store passes in 318.224s. This run began before the behavior-
  preserving result wrapper cut. The focused Result/Step/Annotation/Summary/
  OutputReporting selection passes after that cut in 0.686s. The final foreign-
  selector fixture strengthens the property with a real unrelated record and
  passes in 0.199s.
- Output/BlobResult/MaterializedResult/MeteredAward/RateTable/ClassForResource/
  Storage/Retained/TenantSQL selection passes in 5.734s. The replacement output
  limit fuzz seeds pass in 0.185s. The real pricing property also passed before
  removing the obsolete reader in 0.424s.
- Controller ChildIntake/ClaimRun_Child/NativeChild/ConcurrencyHTTP/
  TeamBoundary_Concurrency/Cron/ControllerCrons/Schedule/Dedupe/IntakeFlood/
  Unpriced selection passes in 2.519s.
- Focused race checks for output reporting, materialized bytes and receipts,
  sealed pricing and result metadata pass: store 23.844s, receipt 1.018s.
- Full controller/client, receipt, apiref, apispec and apidiff tests pass.
- Acceptance selection API|Drift|Snapshot|Concurrency|Child|PortInventory|
  MaterializedResult passes in 0.983s. No new acceptance cell was enabled.
- API snapshot regenerated. The OpenAPI generator output remains identical.
  Documentation mirror and whitespace checks pass. Commentcheck is clean across
  2,799 files.

The existing PostgreSQL tests use real concurrent transactions. All three lock
regressions skip here because SPARKWING_TEST_PG_URL is unset. No PostgreSQL pass
is inferred from a skip. The latest native report remains the lead's session-2
report and still contains FAIL. Its next run must judge this uncommitted tree.

### Contracts and shared files

C1 records Node.OutputDigest, removal of the obsolete output and pricing APIs,
and the implemented result file. Public docs and CHANGELOG describe the cut.
The port inventory records the replacement limit fuzz, new reporting property
and the retained real-claim pricing proof. No lead-owned handoff or amendment
file changed.

Shared caller changes outside pkg/store: internal/orchestrator/receipt/receipt.go
and receipt_test.go. Shared store edits: store.go (report projection),
unit_billing.go (resource type only), concurrency.go (move a test-only predicate)
and tenant_sql_scope_guard_test.go (delete only the retired reader's exemption).
Other shared artifacts: .apidiff/pkg_store.txt, docs/api.md, CHANGELOG.md and their
embedded mirrors. All other production changes are the W1b legacy cuts above.

Remaining real integration points from the prior report remain open: native
child cross-repository/ref resolution, audited credentials before private
followed-branch lookup, persisted HTTP box identity and accepted memo namespace
metadata. Missing authority still refuses. There are no new foreign contract
requests, settings, services, environment variables or execution mechanisms.
W2 owns the native privacy failures. The lead retains PostgreSQL/socket proof,
independent review, commit and landing.

### Volume and friction

Production Go patch including the deleted contract scaffold: +82/-312, net -230.
Using the lead's metric, which excludes contract-named files: +82/-259, net -177.
The completed result declarations now count as production. Combined W1/W1b
store versus main (244d71bd238ffb5f4349b0a9980687c3a5a98988) is -783,
3,467 lines over the -4,250 budget. No budget waiver or completion is claimed.

The small reporting query replaces dead output-key writers because the accepted
result selector already owns the record identity. It adds no alternate output
storage or source of authority. The obsolete profile billing fallback has no
live caller, so it is deleted rather than adapted.

Friction: this sandbox has no PostgreSQL URL and cannot supply the lead's native
socket proof. No Git writes were made. Task scratch is removed at handoff and
/tmp/sw-gocache is retained.

STATUS: CONTINUE


## Continuation after native session 3, 2026-10-17

### Implemented and deleted

The four blockers and all rulings from the original review remain committed at
7e645ac54315c6e9ed1d3af8b74d3322f93bd733. The latest lead report passes both store
lanes and the focused controller/native packages. Its remaining failures are
W2's run-existence privacy properties. Their code and assertions are unchanged.

Invariant: the free-grant writer cannot create paid credit or reverse a payment.
The generated property reproduced a real -714,757 microcredit reversal through
RecordCreditGrant before the cut. The writer now accepts only free grants.
Removed the duplicate raw reversal path, its validator branches, unused
ValidCreditGrantKind helper and ErrReversalExceedsPayment sentinel. The existing
operator free grant remains. ConsoleRefund and verified provider settlement own
payment movements. The console test now checks grant history, ledger state and
deployment totals after the real refund and provider replay.

Invariant: a refund binding consumes only its exact tenant, configured provider
mode, payment and refund receipt, once. Porting the early-outcome test off the
obsolete callback helper reproduced lost failed, canceled and successful outcomes
through the real provider settlement transaction. MarkCardRefundMade now takes
the existing ledger lock, reads matching committed receipts using the existing
mode/object index and commits one resolved queue update. Success wins over a
stale failed worker response. Generated cases cover foreign tenant, mode, payment
and refund identities. A refused outcome update rolls back the binding; retry
retains the recorded outcome.

Deleted reportCardRefundTx and TestOnlyReportCardRefund completely. The failed
refund, early outcome and partial operator-refund tests now use real provider
settlement and worker binding, with a committed collection correlation. Callback
authority follows the provider object instead of a caller's attempt key. The
transaction rollback test calls the existing production refund mutation owner.

Removed unused Tenant.AppendEventOnce and Operator.DropCardAttempt. Tenant event
isolation uses the charged event writer. The quota test prepares a real internal
state event. The obsolete once-only event test is deleted. No compatibility
wrapper, new table, service, runtime setting, limit or environment variable was
added.

### Verification

All Go commands use GOWORK=off, GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-gocache, GOMAXPROCS=4, -p 4 and scratch under
/tmp/sw-astra-W1b. Tests use real SQLite. PostgreSQL is not configured here.

The initial full SQLite store run passed in 289.177s before the refund binding
join. A second full store run also passed (324.239s) with the initial receipt join.
The final single-update receipt selection is covered by the financial and race
selections below. The lead must run its final native matrix on the committed tree.

After the final receipt-selection change, the broad financial/store selection
passes in 35.291s and the focused refund/association/rollback selection passes
in 1.790s. The final race selection passes in 42.028s. It includes recorded
refund association, early outcomes, backoff, partial refunds, free-grant
refusals, console refund history and verified provider refund properties.

The socket-free controller/acceptance selection passes in 3.806s and 1.677s.
The earlier financial controller/acceptance selection passes in 2.318s and
0.302s. Native listener-dependent tests cannot execute here: the broader attempt
fails on api.sock setsockopt and httptest TCP listener permissions. The lead's
native report still contains FAIL and remains the completion authority.

Whole-repository build, scoped vet, apiref/apispec/apidiff, documentation mirror,
whitespace, sleep and comment checks pass. Commentcheck is clean across 2,799 Go files.
No acceptance cell was newly enabled. Existing SQLite billing-start and ledger
retention cells pass in the store suite. No PostgreSQL pass is inferred.

### Contracts, shared files and remaining work

C1 records free-only grants, the retired APIs and the atomic provider receipt
join. The port inventory records each retired test's replacement. The migration
guide, changelog and generated store API snapshot are updated. Lead-owned
amendments and handoffs are unchanged. No foreign contract change is requested.

Shared caller migrations stay within pkg/store: the external card-refund test
moves to operator_card_billing_test.go and uses the real console/provider flow;
payment rollback calls applyCardRefundTx directly. The only W1 guard edit removes
the two exemptions for deleted DropCardAttempt and reportCardRefundTx. Production
changes are credits.go, operator_card_billing.go, payment_mutations.go and
payment_provider_settlement.go. Other shared files are C1, the port inventory,
STORE-DATA.md, .apidiff/pkg_store.txt, CHANGELOG.md, the migration guide and its
embedded copies. No controller production file changed.

Remaining real integration points are unchanged: native child cross-repository
and ref resolution, audited credentials before private followed-branch lookup,
persisted HTTP box identity and accepted HTTP memo namespace metadata. The native
memo/result byte-copy integration remains implemented. Missing authority still
refuses. The lead owns native PostgreSQL/socket proof, W2 privacy integration,
independent review, commit and landing.

### Volume and friction

Production Go: +49/-135, net -86 using the lead's exclusions. Combined W1/W1b
store versus main is -869, 3,381 over the -4,250 budget. No budget waiver or
engineering completion is claimed. The only new production code joins retained
provider receipts to the existing refund transaction. A test-only callback had
hidden that missing production join; keeping it would not preserve real early
callbacks. The join needs no extra ledger or callback authority.

No Git writes were made. All writes stay inside the worktree or task scratch.
Scratch is removed at handoff; /tmp/sw-gocache is retained.

STATUS: CONTINUE
