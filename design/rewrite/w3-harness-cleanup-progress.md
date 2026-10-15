# W3 package-test cleanup continuation

<!-- design:begin -->

Priorities 1 and 2 remain green. Priority 3 remains incomplete. Session, Server,
the legacy scheduler and layers, and their orchestrator callers still exist.
This session closes W3's three cleanup assignments in “Lead decisions 09:40”.

## Implemented and deleted

- Removed all 26 `internal/acceptance/host_*.go` production files, **6,059 lines**.
  Retained checks live beside their packages as **33 nodehost tests, 10 nodehost
  fuzz targets and two lease tests**. The five external fuzz wrappers moved too.
- Consolidated the SDK processes, host stores, byte/record backends and remote
  caches into one scripted implementation each. Scenario fixtures hold their
  data and callbacks. Unscripted calls return errors instead of reaching nil
  embedded interfaces. The native-process observer uses the same process fake.
- Deleted the **92-line** duplicate reconciliation fixture. Existing
  `reconcile_test.go` and `state_follower_test.go` retain its reconciliation and
  subscription-race assertions. Keeper checks run once in `internal/lease`.
  The duplicate authority-rejection invocation inside the keeper suite is gone.
- Ported `TestHostLifecycleEvents`, `TestHostStepPersistence` and
  `TestClaimedHostForeignAwards` from the raw-SQL recovery fixture to the existing
  W3 fixture's tenant intake, accepted seals, release and reconciliation claims.
  The tests use real canonical hashing and strict validation. The retained SQL
  only reads persisted steps or injects a failing transaction with a trigger.
  The existing fixture still seeds its root credential directly. It does not
  establish production credential issuance or source authorization.

The port exposed invalid harness setup: a memo-key closure without its policy,
missing setting origins, missing expansion delegation, and an outer masker that
rewrote every field name. These setups now satisfy the existing contracts.
Fuzzing also found an invalid-UTF-8 fixture secret. The generator now normalizes
it before registration, matching JSON's string domain and the existing masking
property. The minimized byte remains an explicit seed.

Invariant retained by the masking property: no registered SDK secret appears in
rendered or persisted failure fields, and persisted errors keep their size bound
and canonical shape. Production masking code did not change.

**Legacy hosting runtime deleted this session: 0 lines.** The production-area
metric against `main`, excluding tests, testdata, acceptance and contract files,
is **+5,835 lines** (11,587 added, 5,752 deleted). It is unchanged this session
and **8,135 lines over** W3's -2,300 budget. Moving harness code does not reduce
that metric. W3 is incomplete until the old hosting stack is removed.

## Contract changes needed and integration left open

- **W2/W9:** an authorized persisted execution snapshot must expose the sealed
  request, accepted spec and plan, run state and owner, complete execution rows
  and accepted expansions. Controller.ReadSnapshot still returns only
  ScheduleSnapshot. Neither it nor ReadRun supplies OwnerStore/ClaimStore reads.
- **W2/W9:** declare the owner-authorized run-start operation corresponding to
  Tenant.TransitionRun(RunStart). AcceptPlan and executing unit renewal do not
  change the run to running. Capacity/adoption and remote delegation adapters
  also remain open.
- **W8/W9 with W3:** local RunHosted still receives legacy backends and
  invocation inputs. It needs an authenticated admitted RunInput and native
  composition. Trigger and pod migration also need the remote contracts above.
- **W9, lead decision 1:** Only, StartAt and StopAt still need sealed fields and
  ingestion. The already-ported selection test does not establish that sealing.
- **W3/W8:** native admission still refuses remote work with local run/box
  semaphores. Merely holding a daemon lease until owner shutdown would recreate
  the owner-loss gap in architecture §3.7. The daemon admission contract has no
  remote-node liveness/adoption binding. Retention needs that binding before it
  can share the native path safely. No weaker permit lifetime was introduced.
- **W3:** concrete service/record wiring, memo/retry source selection and native
  local/trigger/pod composition remain. The earlier W1 admission/approval,
  debug-persistence and interrupted-snapshot findings remain open.

No foreign contract was changed. The amendment audit in the previous
[service/store report](w3-service-store-port-progress.md) and the remaining work
in the [nodehost green report](w3-nodehost-green-progress.md) still apply.
No additional W3 amendment is closed by relocating tests. Decision 9's mismatch
refusals were already committed and still pass. No new human decision is needed
for the changes in this report.

## Evidence and scope

All commands use the assigned worktree, GOWORK=off, GOFLAGS=-buildvcs=false,
GOMAXPROCS=4, GOCACHE=/tmp/sw-gocache and temporary directories under
/tmp/sw-astra-W3. No Sparkwing binary or pipeline ran.

- `go build -p 4 ./...` passes.
- `go vet -p 4 ./internal/nodehost ./internal/lease ./internal/source ./internal/acceptance ./tests/acceptance` passes.
- Full nodehost, nodeproto, lease and source suites pass.
- All 43 moved nodehost test/fuzz roots pass under `-race -count=1`.
- `FuzzExecutorFailureMask` passes **1,857 executions** after the fixture fix.
- Both acceptance packages compile with `go test -p 4 ... -run '^$'`.
- `go run ./internal/commentcheck -base main .` and `git diff --check` pass.

No acceptance cell was added or activated. These remain component checks with
SQLite, native SDK subprocesses or contract fakes as before. They establish no
new complete placement, real controller, remote storage or native-platform cell.
The broader interrupted-SQLite-snapshot reproducer from earlier reports remains
outside this focused race pass. Whole-repository tests and pipeline gates remain
with the lead. No docs impact on product behavior because runtime code is unchanged.

Shared edits are `tests/acceptance/skeleton_test.go` and `protocol_test.go`, the
moved `artifact_publish_test.go`, `checkout_test.go`, `depcache_test.go`,
`failure_test.go` and `memo_test.go`, and the T1 port-inventory pointer. No foreign
production caller changed. Lead-owned handoffs and amendments are unchanged.
The scaffolded Harness and scenario-catalog deletion remain lead work.

Friction: moving the tests exposed dependence on another area's private fixture
and stale assumptions hidden behind it. The retained event checks now use W3's
real-store fixture. No production mechanism, setting, service, dependency or
customer limit was added. No Git writes occurred. Scratch is removed after all
checks join. The shared build cache is retained.

<!-- design:end -->

STATUS: CONTINUE
