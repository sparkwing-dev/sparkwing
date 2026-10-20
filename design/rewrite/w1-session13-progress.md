# W1 continuation

Starts at ae3b9d81f. W1 remains incomplete. The lead owns Git writes, review,
pipelines and landing. W1b implementation files are unchanged.

## Merge resolution

The only conflict was contracts/handoffs.md. The retry-attempt contract request
and W9's Windows TypeScript packaging handoff are independent. Both remain.
No conflict marker remains in the file. The index still reports UU because the
swarm brief prohibits Git writes. The lead must stage the resolution and finish
the merge. No contract signature was replaced.

## Completed cut

Removed Store.ReapExpiredNodeClaims, Maintenance.RecoverExpiredNodeClaims,
Maintenance.ReapExpiredTriggers, AgentLossRecovery and the private dispatcher
expiry path. That path consumed application retry budget and suppressed recovery
errors. ReapUnits is the sole unit-expiry API and requires the elected fence.
Removed its now-unused leaseLost report field and five obsolete SQL-scope guard
exemptions. The shared SQL argument helpers move to sql_args.go because filters,
membership and schedules still use them.

The legacy production files shrink by **407 net lines**, including that helper
move. A separate **17-line teststore fixture helper** acquires the elected lease
and invokes one bounded ReapUnits pass. It returns final tagged ReapedUnit values.
It adds no runtime compatibility API.

Deleted the two legacy TestLease_Reaper tests and the two TestExpiry tests that
treated disruptions as application retries or required independent passes to
suppress errors. Their useful assertions are covered by final unit/owner tests
and the existing SQLite recovery cells. Those two store test files shrink by
**148 net lines**. The old **174-line controller reaper fixture** is replaced with
the real elected controller pass, using direct calls without sockets or timers.
It checks one remote-owner requeue, unchanged generation, preserved run and one
event. A live owner stays held.

Added TestReaperFenceProperty. Its invariant is that only the current unexpired
reaper holder and generation may change work. Thirty-two generated cases exercise
forged names, holders, generations, exact expiry and takeover. Refused passes
leave the persisted snapshot unchanged. The valid successor recovers the unit.

Added TestRecovery/recovery-local-orphans/store-sqlite. An unstarted local owner
requeues without a disruption. A started owner fails in place. Repeated passes
create no second transition. Reopening preserves the original run number, owner
generation, counters, cleared claim, terminal start time and single lifecycle
event. No replacement run or owner row appears. Native lifecycle cells retain
their required skips.

Ported teststore's isolation test from CreateRun/GetRun to the final schema and
Tenant.ReadRun. This closes its removed-sparkwing_meta fixture failure. The test
still checks schema version, private file mode, isolation and refusal to overwrite
an existing file.

## Amendment and handoff audit

The detailed inherited audit is in w1-session12-progress.md. This continuation
does not redefine foreign contracts.

| Rows | W1 state and remaining integration |
| --- | --- |
| A1-07, A1-44, A1-45 | Acceptance and expansion retain secret classifications and persisted per-job settings through shared W6 resolution. Shared resolver integration remains where W6 is a stub. |
| A1-08, A1-18, A1-55 | Mixed claims, zero initial ordinal, Applied.Attempt and first-claim/reclaim rules are implemented. Repeated retry failures still need the explicit source-attempt amendment in C1/C3 and W3/W9 transport. |
| A1-11, A1-25, A1-26 | Trusted source location, plan-secret selection and enrollment adapters are implemented with contract fakes. W2 must install the production adapters. |
| A1-13 through A1-17 | Reconciliation, failure reads, cancellation, private execution snapshots and expansion persistence are implemented. Atomic record copying/attachment still needs W1b. Failure-stage, GuardRefusal and generation-zero materialized-generator wire gaps remain. |
| A1-20 through A1-24 | Store provisioning, digest/session/ancestry/binding, CSRF and audited source boundaries are implemented. C2 BindingState still lacks cancellation reason. Result operation/digest transport, teamless sessions, concurrency selectors and production origin/source policies remain shared integration points. |
| A1-27 | Immutable billing helpers exist, but metered awards still fail closed. W1b must supply reservation/refund/customer-wait integration across claim/start/renew/release/reap/finish. C2 SettleBilling still panics. The removed legacy reaper must not be restored for settlement. |
| A1-28 | Committed-checkout settlement is implemented. W2 must retire caller-selected grants and provide verified provider events. |
| A1-29, A1-31 through A1-35 | W1b owns blob/upload/record/accounting persistence. W1 uses the transactional data checker and tests the materialization/result seams with fakes. Those atomic joins still need the owner's implementation. |
| A1-38 | EngineController.SyncLauncher remains a panic stub and needs W8/W9's immutable Kubernetes/generation correlation. |
| A1-40 | Store handles exist. W8 owns LocalRuntime readiness, wiring and native lifecycle. |
| A1-51 | The three unfenced unit reapers and private dispatcher-expiry path are now deleted. StateStoreOld, Store.AcceptPlan/validator, old claims/triggers, offer rounds and remaining unleased concurrency maintenance still require cuts and caller migration. |
| A1-53 | Native HTTP/api.sock, platforms, PostgreSQL, provider, process and real-repository receipts remain with T1/lead. SQLite/component cells do not replace them. |

The separate cancellation/session handoff remains implemented at the store
boundary. C2 still has no cancellation-reason projection field. The retry-attempt
contract request remains in handoffs.md. Its prior reproducer remains valid
against the unchanged signatures and was not rerun here. No new foreign shape,
customer limit, environment variable, trust policy or schema was introduced.

W1b's credits.go now has unused expiredClaim and settleExpiredClaimTx definitions.
They were left untouched under the area split. Their owner can delete them when
this cut reaches its branch. Metered settlement remains a launch blocker.

## Verification

All Go commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-astra-W1/cache, GOTMPDIR=/tmp/sw-astra-W1/tmp and
TMPDIR=/tmp/sw-astra-W1/tmp. Package parallelism is -p 4.

- Whole-repository go build ./... passes.
- Scoped go vet passes for internal/store/..., internal/controller/..., internal/acceptance/...
  and tests/acceptance/....
- The existing final store selection plus the new property passes **318 top-level
  tests / 950 cases**, with no skips. Its file list is w1-session13-tests.txt.
  Extract Test names, omit TestPostgres-prefixed names and join them into an
  anchored -run expression.
- The final reaper selection passes **16 top-level tests / 70 cases** without
  skips. Selection is TestReaperFenceProperty, TestOwnerReaper.*, TestUnitReaper.*
  and TestOwnerRequeue.*.
- Controller TestEngine.*, TestSessionRoutePreservesCapabilityScopeProperty,
  TestReaperRecoversExpiredOwnerAndKeepsLiveOwner and
  TestReaperRunsInOneOfTwoOverlappingControllers pass **36 top-level tests /
  61 cases** without skips.
- Full teststore passes both tests. Internal storetest passes both tests. Full
  pkg/match passes 14 top-level tests / 42 cases. Orchestrator tests compile with
  -run '^$'. No native orchestrator runtime pass is claimed.
- tests/acceptance with -skip '^TestProtocol$' passes **19 top-level tests /
  192 executed cases**, with **2,422 required skeleton/platform skips**. The
  new local-orphan cell also passes by its exact name.
- Required commentcheck -base main passes across 976 Go files. Session
  sleepcheck -base HEAD and git diff --check pass. Changed Go files are formatted.
- The store and teststore API snapshots match generator output. The store snapshot
  also closes inherited drift for previously removed wrappers and projections.
  Other package snapshots remain the aggregate generation owner's work.
- Source docs, migration notes, changelog and generated embedded mirrors are
  updated. sync-docs.sh --check passes. Acceptance catalog/documentation and
  port-destination checks pass.

The full acceptance attempt failed in W5's newly merged native pipes cell because
sdk/typescript/node_modules/typescript/bin/tsc is absent. No W5 source or skip was
changed. The full teststore attempt first failed in its old sparkwing_meta fixture.
That fixture is ported and its complete package passes afterward.

An overbroad TestReaper.* controller expression also selected the unchanged
TestReaperSamplesTheOperationalSeries, which fails in its legacy CreateRun fixture
on missing sparkwing_meta. An overbroad TestOpen.* store expression selected six
unchanged URI/permission/read-only fixture tests with the same legacy-table
failure. They were not counted as passes or changed outside the reaper work.
The explicit final selections above pass. No full legacy store/controller suite,
PostgreSQL, race, lint, provider, native platform, pipeline or review pass is claimed.

## Shared files and caller migrations

Outside internal/store, caller migrations are internal/controller/reaper_test.go,
internal/orchestrator/trigger_host_test.go and trigger_host_generators_test.go.
The orchestrator fixtures use one tagged node+trigger pass and expect the lost
node to be pending. Their full host/credential fixture port remains W3 integration.

Store caller migrations are claim_tokens_test.go, controller_dispatch_test.go,
credit_billing_start_test.go, credit_expired_claim_settle_test.go, credits_test.go,
node_claim_test.go, run_control_test.go, trigger_claim_credits_test.go and
trigger_claim_identity_test.go. Billing fixtures have only the allowed caller
changes. Their final accounting behavior still needs W1b's integration. Legacy
claim/bootstrap fixture migration remains part of retiring those APIs.

Shared acceptance files are internal/acceptance/store_recovery.go and the new
store_orphans.go. Shared docs are contracts/c1-data.md, handoffs.md,
t1-acceptance.md, t1-port-inventory.md, docs/observability.md,
docs/migrations/_unreleased.md, CHANGELOG.md and their generated mirrors. API
snapshots are .apidiff/pkg_store.txt and pkg_store_teststore.txt. Other changes
are W1 store files and this handoff/test-selection record.

Friction came from the cold Go cache, legacy fixture SQL and the newly imported
TypeScript dependency requirement. Initial test assertions incorrectly compared
the observation clock as persisted state and expected a terminal owner's start
time to clear. Those assertions were corrected to match the frozen contract.
The valid implementation required no workaround. No production mechanism was added.

No Git write, Sparkwing binary, deployment, release, service, harness-memory
write or outside-worktree edit ran. Git has no local user.name, user.email or
core.worktree override. Scratch and owned check processes are removed before
reporting. The lead still needs to stage the conflict resolution.

STATUS: CONTINUE
