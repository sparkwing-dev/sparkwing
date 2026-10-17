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
