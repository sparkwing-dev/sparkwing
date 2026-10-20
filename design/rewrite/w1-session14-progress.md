# W1 continuation

Starts at 21073158f. W1 remains incomplete. W1b implementation files are unchanged.
The lead owns review, Git writes, pipelines and landing.

## Implemented and deleted

Concurrency maintenance now belongs to Operator and requires the current
controller.reaper lease. It locks affected capacity entries in tenant/key order,
cleans holders and waiters, promotes queued work and checks capacity invariants
in one transaction. It rechecks the reaper fence immediately before commit.
Any failure rolls back the whole pass and returns zero counters.

Expired waiter leases cannot retain queue position. Terminal or missing jobs
cannot receive capacity. A live blocked waiter keeps its arrival and lease.
Terminal holders and waiters are removed in every scope, including global scope.
Promotion uses the existing owner/job liveness and generation helpers.

Removed Store.MaintainConcurrency, the Maintenance registry, its six unfenced
operations, Operator.CountConcurrencyCache, obsolete cache maintenance options
and counters, PurgeDeadLocalConcurrency and CountDeadLocalConcurrency. Deleted
maintenance.go (29 lines) and local_concurrency_repair.go (133 lines). The store
runtime changes remove **365 net production lines**. The new 17-line teststore
helper acquires the fixture's reaper lease and calls the final operator API.

Controller startup and periodic sweeps use the elected pass. Local daemon
maintenance acquires a reaper fence with a process-specific holder. Doctor's
independent local-key repair and its two report counters are removed. Those
31 caller lines no longer bypass the elected pass.

Deleted 399 lines from the old maintenance test suite, the 106-line local-repair
test file and the obsolete standalone zero-age no-op test. Retained fixture
helpers still used by unrelated tests. Ported promotion, abandoned-waiter cleanup,
live blocked-waiter preservation, terminal cleanup and repeated-pass assertions
to six final-schema tests. Removed the cache sweep assertions because the final
schema stores memos through blob records. Run foreign keys prevent orphan run
bindings. W1b retains record and blob retention ownership.

The fence property generates 96 forged names, holders and generations, then
checks exact expiry and takeover. The invariant is that only the current,
unexpired reaper holder and generation may commit maintenance. Statement-failure
and mid-pass fence-expiry tests prove full rollback. The old global-terminal
preservation assertion is reversed because final transitions release terminal
capacity in every scope.

## Acceptance and contracts

TestRecovery/recovery-concurrency-no-revival/store-sqlite now executes. It refuses
a forged fence, promotes a queued competitor after holder expiry, reopens the
database and makes the former owner queue as a new arrival. A repeated elected
pass preserves the competitor and cannot revive the old slot. Native placement
cells retain their required skips.

The local C1 contract adds the leased Operator.MaintainConcurrency signature to
OperatorStore and removes the old maintenance surfaces. No foreign type changes.
No schema, customer limit, environment variable, service or trust root is added.

The amendment and handoff audit retains the session 13 results, with these open
groups and this continuation's change:

| Entries | State and remaining integration |
| --- | --- |
| A1-07, A1-11, A1-25, A1-26, A1-44, A1-45 | Seals, classifications, shared resolution, source location and enrollment boundaries remain implemented. W2 must install trusted intake, enrollment and secret providers. |
| A1-08, A1-18, A1-55 | Mixed-kind claims, first ordinal and here first-claim/reclaim rules remain implemented. Repeated identical retry failures still need the source-attempt amendment in C1/C3 and W3/W9 transport. |
| A1-13 through A1-17 | Reconciliation, cancellation, failure reads and execution/expansion snapshots remain implemented. W1b must join record copying and result attachment transactionally. Failure-stage, GuardRefusal and generation-zero generator fences remain shared contract gaps. |
| A1-20 through A1-24 | Provisioning, digest/session/ancestry/binding, CSRF and source-release boundaries remain implemented. C2 BindingState still lacks cancellation reason. Result operation/digest transport, concurrency selectors, teamless sessions and production origin/source providers remain integration points. |
| A1-27, A1-28 | Committed-checkout settlement remains implemented. Metered claim lifecycle, customer-wait exclusion and C2 SettleBilling still need W1b/W2 integration. Metered awards continue to fail closed. |
| A1-29, A1-31 through A1-35 | W1b owns blob/upload/record/accounting persistence. Its files remain untouched. W1's transaction seams retain contract-fake coverage. |
| A1-38 | SyncLauncher remains a stub. LauncherObservation has UnitID, JobName and NodeUID but no claim generation. UnitID is stable across re-awards. NodeUID identifies the Kubernetes node rather than a Job instance. W8/W9 must supply immutable award and Kubernetes-instance correlation before safe cleanup can be implemented. Recorded in handoffs.md. |
| A1-40, A1-53 | W8/T1/lead retain local runtime readiness, real HTTP/api.sock, platform, PostgreSQL, provider and real-repository evidence. |
| A1-51 | The Maintenance registry, all six unfenced operations and independent local concurrency repair are now removed. StateStoreOld, Store.AcceptPlan/validator, legacy claims/triggers and offer rounds still require cuts and caller migration. |

The separate W2 cancellation/session handoff remains implemented at the store
boundary. The retry-attempt request remains in handoffs.md. ReuseSource still
needs a materialized-result identity where a job has no attempt. This continuation
does not redefine another area's wire or credential contract.

## Verification

All Go commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-astra-W1/cache, GOTMPDIR=/tmp/sw-astra-W1/tmp and
TMPDIR=/tmp/sw-astra-W1/tmp. Package parallelism is -p 4.

- Whole-repository go build ./... passes on the final Go tree.
- Scoped go vet passes for internal/store/..., internal/controller/..., internal/orchestrator,
  internal/opsview, internal/acceptance/... and tests/acceptance/....
- The final store selection passes **324 top-level tests / 964 cases**, without
  skips. Its file list is w1-session14-tests.txt. Extract Test names, omit names
  prefixed TestPostgres and join into an anchored -run expression.
- Controller TestEngine.*, TestSessionRoutePreservesCapabilityScopeProperty,
  TestReaperRecoversExpiredOwnerAndKeepsLiveOwner and
  TestReaperRunsInOneOfTwoOverlappingControllers pass. Both reaper tests also pass
  after the terminal-capacity cut.
- Full teststore, internal storetest and pkg/match pass. The doctor plain-report
  coverage test passes. Orchestrator tests compile through scoped vet.
- tests/acceptance with -skip '^TestProtocol$' passes **19 top-level tests /
  193 executed cases**, with **2,422 required skeleton/platform skips**. Both
  capacity recovery SQLite cells also pass by exact selection on the final tree.
- Required commentcheck -base main passes across 979 Go files. Session sleepcheck
  -base HEAD and git diff --check pass. Changed Go files are formatted.
- Store and teststore API snapshots match generator output. Source migration docs,
  changelog and generated embedded mirrors are updated. sync-docs.sh --check passes.

The complete protocol acceptance group was excluded because the TypeScript
compiler at sdk/typescript/node_modules/typescript/bin/tsc is absent. No native,
full legacy store/controller suite, PostgreSQL, race, lint, pipeline or independent
review pass is claimed. The lead retains those checks.

## Shared files and caller migrations

Production callers outside internal/store are internal/controller/server.go,
internal/orchestrator/wingd_daemon.go and internal/opsview/doctor.go. Test callers
are internal/orchestrator/wingd_runstore_test.go and
internal/opsview/doctor_plain_coverage_test.go. Store caller migrations are
concurrency_recovery_test.go and concurrency_property_test.go. They use the final
leased pass. Their broader inherited fixture migration remains open.

Shared acceptance files are internal/acceptance/store_recovery.go and the new
store_capacity_maintenance.go. Shared docs are contracts/c1-data.md, handoffs.md,
t1-acceptance.md, t1-port-inventory.md, docs/migrations/_unreleased.md, CHANGELOG.md
and their generated mirrors. API snapshots are .apidiff/pkg_store.txt and
pkg_store_teststore.txt. Other changes are W1 store files and this report/test list.

Friction came from the inherited maintenance code's references to the removed
concurrency_cache table and obsolete coalescing policy. The final schema refused
those fixtures. The implementation removes that obsolete maintenance rather than
recreating the table. An initial terminal-job fixture omitted finished_at and was
corrected to satisfy the final schema. No production workaround was added.

No Git write, Sparkwing binary, deployment, release, service, harness-memory write
or outside-worktree edit ran. Git has no local user.name, user.email or core.worktree
override. Scratch is removed before reporting.

STATUS: CONTINUE
