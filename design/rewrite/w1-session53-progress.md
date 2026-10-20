# W1 session 53: reporting transactions and schedule leases

W1 remains CONTINUE. Trusted schedule/App admission, remaining legacy APIs and
the store budget are open. This work starts at e64be7d8c. No Git writes were made.

## Implemented

`Tenant.WriteMetric` and `WriteUsage` implement C2's declared MetricWriter.
Invariant: each write rechecks current ancestry, tenant, selected route rule and
reporting fence in its committing transaction. Captured job attempts cannot move
to a later retry. A cancelling holder can still report while its claim is live.
A live run host can report process accounting after its job has finished.

The generated property refuses 96 forged tenant/run/job/fence/authority/rule
combinations without writing. Revoking the award's parent then refuses both
writes. Separate checks cover retry, cancellation, terminal refusal and owner
reporting. The controller's default writer is now the concrete tenant, checked
at compile time. The optional resolver still supports test doubles.

An identical metric retry previously failed once the sample cap was reached.
The new regression ran red. Metric insertion now resolves exact replay before
applying the cap to a new row. Conflicting readings and rejected overflow leave
stored values unchanged. Existing resource limits are unchanged.

`Operator.RunCronTickLeased` now uses the declared `schedules.tick` singleton
lease and the store clock. The last successful tick lives in settings. Invariant:
a former tick cannot stamp success or release a replacement's lease. Generated
checks cover 32 takeover/window cases and 64 invalid releases. The existing
failure, cancellation, panic and concurrent-tick checks pass on the final schema.
`Operator.ReleaseSingletonLease` releases an exact name/holder/generation tuple.
Each tick has a fresh holder identity because singleton acquisition permits
renewal by the same holder, while overlapping ticks must exclude one another.

## Deleted and volume

Removed Store/Tenant `AddNodeMetricSample` and `AddNodeUsage`, plus the Store
`RunCronTickLeased` surface. Reporting callers use the authority-bearing tenant
transactions. Cron callers select Operator. Removed `nodeClaimLiveSQL`,
`triggerClaimLiveSQL` and `splitScopes`, which had no callers.

Session store production delta is **237 added, 175 removed, net +62 Go lines**.
Controller production adds another net 2, so the whole session is **net +64**.
These counts exclude tests, testdata, acceptance paths and contract files using
the lead's budget rules, and include the new teststore metric seed helper.
The new transaction authorization and final schedule lease replace previously
missing/broken implementations. This is progress, not a budget exception.

Combined W1+W1b against main is **18,923 added, 21,870 removed, net -2,947**.
That remains **1,303 lines over** the -4,250 target. The remaining raw run/trigger
and token issuers still have callers. No COMPLETE or budget-waiver claim is made.

## Verification and ports

Go used GOWORK=off, GOFLAGS=-buildvcs=false, GOCACHE=/tmp/sw-gocache,
GOTMPDIR=/tmp/sw-astra-W1 and TMPDIR=/tmp/sw-astra-W1. Tests used -p 4.

- Whole-repository `go build -p 4 ./...` passes.
- Vet passes for internal/store, internal/controller and internal/store/teststore.
- All 21 functions declared in internal/store/acceptance*_test.go pass. The same
  selection includes the changed metric, usage and cron checks: 8.773s.
- Focused store race checks pass in 26.588s. They cover forged reporting
  authority, retry/cancellation, owner reporting, conflicting metric writers,
  usage overflow races, cron takeover, invalid release and concurrent ticks.
- The strengthened reporting property revokes the parent authority and passes
  separately under the race detector after that test change.
- Controller metric timestamp, generated HTTP authority, validation/limit and
  paged-read checks pass in 0.585s. Their writer is still a test double. The
  missing-writer case now supplies an explicit nil resolver result because
  clearing the resolver selects the newly implemented production tenant.
- Placement metering, ordered preferences, eligible-runner liveness, registered
  rotation grace, rotation deadlines and launcher-fencing properties pass.
- Commentcheck passes across 2,296 Go files. API snapshot, embedded docs,
  changelog and whitespace checks pass.

Ported metric and usage checks retain kinds, timestamps, timezone replay,
conflicts, concurrent writes, pagination, sample/marker limits, tenant boundaries,
integer boundaries, overflow rollback and invalid stored totals. Their fixtures
now use real admission, acceptance and claims. The cron generation dialect check
now reads the final lease generation. The inventory records the exact test names.

The broad store short run was interrupted after 196 failing test/subtest records
and a legacy cron test waiting for a callback that never entered. Initial
failures include removed args_json/tokens/concurrency_cache/sparkwing_meta,
legacy data authority and unfunded Cloud fixtures. Cron leasing is now fixed,
but no broad-suite pass is claimed. A later broad TestStore selection still
fails in TestStoreCacheScope, TestStoreReferenceEdges and
TestStoreFinishRefusesAnotherTeamsNode. Those failures reproduce the legacy
blob-authority and raw CreateRun fixtures. The final selection names W1's 21
acceptance functions explicitly rather than claiming those other tests passed.
An overbroad controller metric selection also hit a socket refusal in the sandbox.

Native PostgreSQL is not exercised. The optional PostgreSQL generation case
skips without a server. No native matrix cell, sockets, pipelines, release or
whole-repository test pass is claimed. Independent review and commits remain
with the lead.

## Contract changes needed and integration left open

The trusted-intake gap from session 52 remains. C1 requires AuthorityRef before
IntakePolicy runs. C4's IntakeFacts and SealedRequest carry that same credential
reference. A schedule's arming account and an HMAC-verified delivery cannot pass
as a session or capability. The required foreign change is a tagged committing
origin in C4 plus a C2 verified-delivery fact reader. The store must select the
current schedule/arming version or installation/consent facts transactionally.
No synthetic credential or second sealed-admission implementation was added.
After that contract lands, route schedules and App deliveries through admitRun
and delete triggerIntake, triggerRows and the raw insertion path.

C2's publisher alternative on metric/usage routes still uses BindData, while
EngineController.runTarget supplies no Data target. The store binding relation
requires that target for BindData. This is a code-inspection integration gap,
not a passing publisher-through-HTTP claim. W2 owns the route relation and
request-target contract. Claim and run-host store transactions are covered.

NodeUsage remains additive and carries no report identity. The usage route's
RetryExactBody policy cannot distinguish a repeated observation from a second
identical process observation. C2/C3 need a report identity or retry-policy
decision before claiming idempotent usage delivery. No new identity was invented.

W1b's RunBucketMeasureLeased still uses the old sweep-window helpers and the
removed sparkwing_meta table. It is the remaining production caller preventing
meta.go deletion. W1b must migrate that measurement path to its maintenance
contract. W1b files and closed materialization are unchanged.

Other retained gaps from session 52 include full enrolled runner inventory
observations and legacy-schema readers/writers. In particular the legacy
GetNode projection still selects node_outputs, which does not exist in the
final schema. Reporting tests read their three accounting totals directly so
they do not pretend that legacy projection works.

## Shared files and assumptions

Outside store, production changes are limited to internal/controller/crons.go
(select Operator) and internal/controller/metric_writes.go (concrete adapter).
Shared controller tests are metric_timestamp_test.go, profiles_test.go,
profiles_coverage_test.go and profiles_aggregation_test.go. Profile projection
fixtures use teststore.SeedMetric after the raw production writer is removed.
Their broader legacy run setup is not claimed to pass.

C1, the port inventory, CHANGELOG, the unreleased migration and their embedded
mirrors are updated. Both store and teststore API snapshots are regenerated.
Lead-owned handoffs.md and amendments files are unchanged.

The only new public store lifecycle operation is ReleaseSingletonLease, which
uses the existing lease table. Exact fenced release is needed for failed ticks
to hand work back without clearing a replacement. Removing it would require a
second private implementation of the same release. No schema, environment knob,
capability class or new customer limit was added.

Friction: legacy fixtures make broad checks fail before they reach much current
behavior. Changelog checking warns about rename detection but exits successfully.
The worktree Git config has neither core.worktree nor a fixture user.name.
W1 scratch is removed after checks. The shared build cache is retained.

STATUS: CONTINUE
