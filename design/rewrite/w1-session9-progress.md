# W1 continuation

Starts at 10bcfaf93. W1 remains incomplete. The lead owns review and commits.
W1b implementation and test files are unchanged.

## Changes

Removed the eight remaining raw Store profile methods: RecordProfileObservation,
RecordWaitObservation, RecordContention, SetProfilePin, UpsertProfilePin,
GetPipelineProfile, ListPipelineProfiles and CacheExcludedCounts. This deletes
**24 implementation lines**, plus their obsolete API documentation. Tenant owns
the complete profile API. The affected local orchestration, stats and diagnostic
callers select the registered DefaultTeam and propagate handle errors.

The invariant is that profile reads, observations, pins, contention and resets
cannot expose or alter another team's data, including shared adversarial keys.
TestProfileMutationsRemainTenantScopedProperty adds 96 generated pipeline/job
pairs. It checks each mutation against an unchanged foreign listing, checks the
selected tenant's persisted values, and compares complete listing sizes.
The existing 96-case reset/read property also passes.

Existing profile tests now call Tenant. Test fixtures for obsolete sample
documents supply the required team column. TestCacheExcludedCounts now seeds
the final schema and uses memoized outcomes. It no longer invokes the legacy
run/node state writers. The local profile portion of
TestLocalStateServesEveryRunCoordinationMethod now runs independently as
TestLocalStateServesProfileMethods against the final store. It checks pins,
resource observations, waits and contention. No test assertion was discarded.

The shared teststore.ForTeam helper obtains an explicit registered tenant and
fails the fixture on error. It replaces repeated fixture setup across packages.
It supplies no default team or compatibility API. The production callers use
Store.ForTeam directly. Removing the helper and inlining those checks reverses
this test-only addition.

Added the passing SQLite acceptance cell
TestRecovery/recovery-started-lapse-cap/store-sqlite. It starts, expires, reaps
and reopens the same job three times. The first two losses requeue and advance
the next generation. The third fails and stays terminal under reconciliation.
Attempt ordinals remain one through three, application retries remain zero,
each attempt records agent_lost, and no sibling run appears. Exact event lists
and stale-holder refusals check every loss. The enrollment resolver is a contract
fake. Native processes, transport and metered settlement remain unproved.

This extends the existing recovery acceptance coverage. No new archived
source-function identity is claimed in T1, and no native skip was removed.

## Amendment audit and remaining work

handoffs.md still assigns its entries to W3, W4, W5, W8 and W9. It adds no W1
action. amendments-1.md has no new W1 entry beyond the previous audit.

| Entries | Status |
| --- | --- |
| A1-07, A1-11, A1-25, A1-44, A1-45 | Existing intake, seal and plan/settings enforcement remains. Trusted origin/decryption integration belongs to W2. |
| A1-08, A1-13 through A1-18, A1-26, A1-55 | Existing claim selection, matcher, reconciliation, failure reads, cancellation, expansion replay, ordinals and here reclaim remain. This continuation adds the three-loss acceptance proof. |
| A1-20 through A1-24 | Existing authority/provisioning/binding/source adapters remain. Origin policy, process authority, exact-body replay evidence and resolved concurrency targeting still require their external owners. |
| A1-27 | Metered lifecycle integration remains open. The unstarted refund, customer-runner wait operation and C2 calculator gaps below remain. |
| A1-28 through A1-35 | Accounting, payment, blob, upload, record and log persistence belong to W1b. W1's transaction interfaces still need that owner's implementation. |
| A1-38 | SyncLauncher remains a stub pending W8/W9's generation-to-Kubernetes identity contract. |
| A1-40 | Existing controller recovery adapters remain. W8 owns LocalRuntime wiring. |
| A1-51, A1-53 | Shared corpus and native drivers retain their owners. The new proof runs below transport and process boundaries. |

Contract changes needed in other areas remain:

- W1b's at_award helper opens an interval, while settlement rejects
  RefundUnstarted if intervals exist. Unstarted release needs an agreed
  transaction operation that preserves immutable pricing/provenance and fully
  refunds work that never executed.
- C2 SettleBilling still panics. Its production calculator is required before
  enabling metered lifecycle awards.
- RenewUnit's executing flag records first execution. No host operation names
  entry to or exit from customer-owned runner waits.
- W1b must implement materializationRecords.CopyMaterializedRecords and
  nodeResultRecords.AttachResultRecords inside the calling transaction. Public
  record methods opening their own transaction cannot substitute for them.
- W8/W9 must define launcher observation correlation across claim generations
  and the identity named by LauncherSyncResponse.Remove.

Legacy retirement remains incomplete. Store.AcceptPlan and its validator still
have W1b-owned output/log fixture callers through dispatchRun. Those fixtures
cannot migrate in this continuation because their files are outside W1's scope.
StateStoreOld, old claim/trigger writers, offer rounds and old maintenance APIs
also retain callers. Their remaining production callers must migrate or be
deleted with the smallest compiling change. No replacement shim was added.

Completion delivery, physical retention/GC, real enrollment and origin policies,
exact-body replay transport, the generation-zero materialized-generator fence
and native drivers retain the integration points in w1-amendments-progress.md.
No foreign contract shape, schema, billing policy, setting, limit or environment
variable changed. No human decision is requested.

## Verification

Go commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-astra-W1/cache, GOTMPDIR=/tmp/sw-astra-W1/tmp and
TMPDIR=/tmp/sw-astra-W1/tmp. Package parallelism is -p 4.

- Whole-repository go build -p 4 ./... passes.
- go vet -p 4 passes for internal/store/..., internal/controller/..., internal/acceptance,
  tests/acceptance, cmd/sparkwing, internal/opsview, internal/web,
  internal/orchestrator and internal/runners/k8s.
- The exact store selection passes **296 top-level tests / 828 cases** without
  skips. Extract Test names from the files in w1-session9-tests.txt, omit names
  starting TestPostgres, join them into an anchored alternation and pass it as
  go test -run. The profile-only subset passes **42 tests / 112 cases**.
- Controller -run '^(TestEngine|TestCapacityReadUsesRequestTenantProperty)' and
  CLI -run '^(TestRunCapacityReset|TestGroupCapacityStats_)' pass.
- internal/web -run '^TestCapacity' passes. The two localState profile tests
  pass. All migrated shared test packages compile.
- Full tests/acceptance and pkg/match pass. internal/acceptance has no tests.
  Native/platform skeleton skips remain. The started-lapse cell also passes
  independently.
- Required commentcheck -base main passes across 908 Go files.
  commentcheck -base HEAD passes this continuation. The previous detached
  HereBearer comment is gone from the merged baseline.
- Edited Go files are formatted, and git diff --check passes. No local
  user.name, user.email or core.worktree override is configured.

The full legacy suite, PostgreSQL, native transport and placements, race, lint,
pipelines and independent review remain lead checks. Only the eight removed
symbols and the test helper were updated in their API snapshots. Aggregate
snapshot regeneration remains integration work.

## Shared files and friction

Production callers outside internal/store changed only in
cmd/sparkwing/runs_capacity.go, internal/opsview/doctor.go and
internal/orchestrator/backends.go.

The other shared Go changes are internal/acceptance/store_recovery.go and the
new store_started.go, plus fixture migrations in:

- cmd/sparkwing/{doctor,runs_capacity}_test.go
- internal/orchestrator/{backends_coordination,capacity_command_cpu,
  capacity_completion,capacity_coverage,capacity_poison,capacity_reconcile_unix,
  capacity_record,capacity_sustained,capacity_usage,metrics_availability,
  nodehost_usage}_test.go
- internal/runners/k8s/{k8s,profile_cpu_policy}_test.go
- internal/web/capacity_test.go
- internal/controller/{finish_run_followups,finish_run_recovery,finish_run_retry,
  profiles_aggregation,profiles_completion,profiles_coverage,profiles,
  runner_scopes}_test.go
- internal/controller/client/coordination_test.go

C1, T1, two API snapshots, this handoff and the exact test-file list record
the change. W1b files are untouched.

Friction was limited to cold compilation, strict final-schema fixtures and
mechanical test migration corrections. The first property had an unused loop
index. A mechanical replacement wrapped an existing Tenant, missed a writer
alias and omitted one import. A web fixture also returned raw Store as
ProfileReader. The started-lapse fixture needed int64 attempt comparisons.
All were corrected before the passing verification. No production workaround
was added for those fixture errors.

No Sparkwing binary, daemon, provider call, Git write, deployment or release ran.
Scratch is deleted before reporting.

STATUS: CONTINUE
