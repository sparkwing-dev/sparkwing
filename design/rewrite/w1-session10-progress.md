# W1 continuation

Starts at 450e1e989. W1 remains incomplete. The lead owns commits and review.
W1b's metered, blob and log files are unchanged.

## Changes

Removed twelve raw Store concurrency APIs: ConcurrencyHolder,
AcquireConcurrencySlot, HeartbeatConcurrencySlot, ReleaseConcurrencySlot,
ReleaseAndNotify, ResolveCoalesceFollowers, PromoteNextWaiters, ResolveWaiter,
CancelWaiter, GetConcurrencyState, ForceReleaseSupersededHolders and
ListConcurrencyStates. This deletes **70 API implementation lines**. The unused
legacy inherited-holder constant is also gone. Tenant owns the scoped methods.
The deployment-wide listing had no caller and has no replacement.

The local concurrency adapter selects the registered DefaultTeam and propagates
handle errors. Existing test callers select explicit tenant handles. No forwarding
shim or new interface was added. Stale Tenant instructions that demanded keeping
raw Store twins and described removed migrations are deleted. The API snapshot
removes only this continuation's twelve symbols.

Concurrency acquisition and promotion now persist the work's generation.
Queued arrivals persist their lease expiry. Expired waiters lose arrival order
and cannot promote through queue polling or coalesced polling. Capacity operations
and cache expiry use the store clock. The promotion and waiter-maintenance queries
use the current owner lease or a live job claim instead of the removed
runs.last_heartbeat_at column. Their run reads carry the tenant key.

The tenant invariant is that concurrency mutations cannot alter another team's
capacity, holders or waiters, even with shared adversarial keys and holder names.
TestConcurrencyMutationsRemainTenantScopedProperty checks 96 generated key/holder
pairs against an unchanged foreign state and a complete foreign listing.

The continuity invariant is that expired capacity never renews or adopts.
TestConcurrencyExpiredCapacityNeverRevivesProperty generates 96 lease lengths
and exact-expiry boundaries. It checks renewal refusal, AcquireAgain and zero
remaining capacity. Other final-schema tests cover generation persistence,
waiter lease persistence, owner-lease promotion and a returning expired waiter
queuing behind an earlier live arrival.

Added the passing SQLite acceptance cell
TestRecovery/recovery-concurrency-continuous-adoption/store-sqlite. It acquires
node and plan groups, queues another run, loses the host owner, renews the remote
job, reaps the owner and reopens the store. ClaimUnit awards a successor owner,
which adopts both groups. The competitor stays queued until job completion
releases node capacity and run completion releases plan capacity. Each release
admits exactly one competitor. Repeated promotion admits none. Enrollment is a
contract fake. Native processes and placement transports remain unproved.

Ported the archived T1 function TestConcurrencyGuard_CanonicalSQLSitesOnly from
design-found-hotfixes to TestHolderLivenessUsesStoreClock and the generated
expiry property. Deleted the old **30-line source-count test**. The behavioral
proof checks clocked acquisition, expired capacity, renewal/adoption refusal
and a fresh acquisition. C1 and T1 record the implementation and evidence.

## Amendment audit and remaining work

handoffs.md adds no W1 assignment. amendments-1.md has no new W1 entry beyond
the previous audit. Existing intake, acceptance, claims, reconciliation,
authority and reaper implementations were exercised by the expanded selection.
This continuation strengthens admission and recovery without changing a foreign
contract shape, schema, billing policy, setting, limit or environment variable.

- A1-27 remains open. Metered awards still fail as unavailable. W1b's at_award
  interval and RefundUnstarted rules need an agreed unstarted-release operation.
  C2 SettleBilling remains a panic stub. The host contract still lacks entry/exit
  operations for customer-owned runner waits. W1 must connect awards, interval
  starts, renewal, release, finish and reaping after those contracts exist.
- A1-29 through A1-35 retain W1b/W7 integration work.
  materializationRecords.CopyMaterializedRecords and
  nodeResultRecords.AttachResultRecords remain contract seams tested with fakes.
  Public record methods opening independent transactions cannot replace them.
- The W1b snapshot-lock report is stale on this merged branch. Snapshot
  transactions already omit PostgreSQL row locks through snapshotRead. The real
  binding/data tests pass in the selected SQLite suite. PostgreSQL is unproved.
- A1-38 SyncLauncher still panics. W8/W9 must define generation-to-Kubernetes
  identity correlation and the identity named by Remove.
- A1-40's existing controller recovery adapters pass their tests. LocalRuntime
  wiring remains W8's integration.
- A1-51 and A1-53 retain native transport, process, placement and platform work.
  The new acceptance cell proves SQLite persistence only.
- StateStoreOld, legacy plan acceptance, old claim/trigger writers, offer rounds
  and unleased maintenance APIs still have callers. ReapExpiredNodeClaims has no
  production caller but retains W1b-owned billing test callers. Those fixtures
  must migrate before deletion. Legacy coalesce waiter writes are refused by the
  final schema, whose waiter policy CHECK excludes coalesce. Retire that old
  mechanism with its W3/W9 consumers rather than widening the final schema.
- Completion delivery, physical retention/GC, final custom-event sequencing,
  real enrollment/origin policies and exact-body replay transport retain the
  integration points in w1-amendments-progress.md and the prior handoff.

No new human decision or foreign contract change is requested. The only new
private helpers derive persisted capacity generations and share the existing
lease-liveness rule across promotion and maintenance. A call-site patch would
leave another writer on the old heartbeat or default generation. Removing those
helpers and restoring their former reads reverses this implementation change.

## Verification

Go commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-astra-W1/cache, GOTMPDIR=/tmp/sw-astra-W1/tmp and
TMPDIR=/tmp/sw-astra-W1/tmp. Package parallelism is -p 4.

- Whole-repository go build -p 4 ./... passes.
- go vet -p 4 passes for internal/store/..., internal/controller/..., internal/acceptance/...,
  tests/acceptance/..., internal/orchestrator/... and internal/nodehost/....
- The exact store selection passes **302 top-level tests / 836 cases**, without
  skips. Extract Test names from w1-session10-tests.txt, omit TestPostgres-prefixed
  names and join them into an anchored -run alternation.
- Full tests/acceptance and pkg/match pass. The new SQLite capacity cell also
  passes independently. Native/platform skeleton skips remain.
- The migrated nodehost tests compile with -run '^$'. Controller and orchestrator
  test packages compile in the focused selection and vet checks.
- Required commentcheck -base main passes across 925 Go files.
  sleepcheck -base HEAD passes. Go files are formatted and git diff --check passes.
- Git has no local user.name, user.email or core.worktree override.

The full legacy suite was not run. Mechanical caller migration does not claim
that legacy run/node/token fixtures satisfy the final schema. PostgreSQL, race,
lint, provider, daemon, native placement, pipelines and independent review remain
lead checks. Aggregate API/doc regeneration remains integration work.

## Shared files and friction

The only shared production caller change is internal/orchestrator/backends.go.
Shared acceptance files are internal/acceptance/store_recovery.go and the new
store_capacity.go. Other shared callers are fixture changes in:

- internal/nodehost/{admission,events,run_admission}_test.go
- internal/orchestrator/{envelope_coverage,wingd_runstore}_test.go
- internal/controller/{queue_view,server}_test.go

Store test callers changed in concurrency_{burst,cost,heartbeat_busy,maintenance,
observable,property,recovery,sweep,test,v091}, delete_run_cache, postgres and
sqlite_hardening test files. The new admission/property tests, guard port,
scope-exemption cleanup, C1, T1, the API snapshot and this handoff complete the
diff. w1-session10-tests.txt records the reproducible store selection.

Friction came from the final schema exposing missing generation/lease writes and
removed heartbeat reads. The first property fixture named the removed heartbeat
column. The wider selection found two stale scope exemptions. Mechanical caller
migration missed one nodehost call and an internal-package fixture import. A
coalesced-waiter probe confirmed the final schema refuses that removed policy.
These do not justify restoring a compatibility column or policy. The passing
proofs use final-schema fixtures and the final node/owner operations.

No Sparkwing binary, service, provider call, Git write, deployment or release ran.
Scratch is removed before reporting. No owned background process remains.

STATUS: CONTINUE
