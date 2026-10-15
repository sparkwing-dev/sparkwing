# W1 continuation 17

W1 remains incomplete. This continuation starts at 4b935db9f with a clean
worktree and no merge conflict. The lead owns Git writes, review, pipelines and
landing. W1b implementation files are unchanged.

## Implemented and deleted

Schedule operations now require Tenant. Removed thirteen implicit-default-team
Store methods for arming, reading, editing, resolving and deleting schedules.
Local CLI and dashboard constructors select the registered DefaultTeam.
Controller request services keep their authenticated Tenant. Deployment ticks
refresh and evaluate each team's schedules through the existing forTeam owner.
The local tick explicitly selects deployment enumeration.

Operator owns RecordCronTick and GetCronTick. Their transaction stores all four
tick fields in settings. ArmCronSchedule persists repo_key through the existing
runspec.CanonicalRepository owner. The cadence guard reads the settings rows its
writer already commits. Non-run history stores NULL for the optional run foreign
key. Fired history retains the final same-team run constraint.

Deleted **67 source lines** containing the thirteen raw wrappers, **7 lines**
containing the default schedule fallback, and **19 lines** containing seventeen
obsolete default-team guard exemptions and two unused reasons. The two raw tick
methods also leave Store. Their implementation moves to Operator. No forwarding
alias, migration, table, environment variable, pricing policy or limit is added.

Store.AcceptPlan and its validator, StateStoreOld, legacy claim/trigger writers
and readiness paths remain. Their live host/controller callers still require
migration. This continuation does not claim their retirement.

## Acceptance and retained tests

Invariant: a schedule read, edit or fire stays within the selected tenant even
when repository paths and schedule names overlap.

TestCronOperationsRemainInTheirTenantProperty generates 32 two-team pairs,
randomizes foreign mutation order, and checks reads, every schedule mutation,
history and bulk deletion. Refused mutations preserve both rows and their exact
history. TestCronSchedulePersistsCanonicalRepository checks local, ported remote
and numeric GitHub identity against the shared canonicalizer.

The new passing cell is
TestSurface/surface-schedules-and-managed-timers/store-sqlite. It checks foreign
read/edit/history refusal, failed history-write rollback, nanosecond persistence,
reopening, operator tick metadata and tenant deletion. It establishes the store
boundary. It does not exercise managed timers, HTTP, launch admission or native
processes.

The retained store cron suite now uses Tenant and Operator. Fired-history fixtures
seed final-schema runs instead of dangling references. Cursor, override, pause,
history retention, deduplication and tenant assertions remain active. The new
internal/crons test drives both deployment and single-team Tick with real stores,
refresh and cadence reads. Three compute-setting tests check invalid input,
atomic batches and rollback against settings. No new archived source-function
identity was ported. Earlier T1 ports remain active.

## Amendment and handoff audit

The earlier completed W1 work is retained. These groups name the remaining
integration points rather than replaying finished work.

| Entries | W1 state and remaining owner |
| --- | --- |
| A1-07, A1-44, A1-45 | Shared resolution and secret classification remain implemented. Trusted intake/checkout providers need production installation. |
| A1-08, A1-18, A1-55 | Mixed-kind matching, push ancestry, initial ordinals, here reclaim and source-attempt fencing remain implemented. W9/W3 must transport and consume SourceAttempt. |
| A1-11, A1-20, A1-25, A1-26 | Source seals, provisioning, secret validation and enrollment interfaces remain implemented. W2 owns trusted providers and installation. |
| A1-13 through A1-17 | Reconciliation, failure persistence, cancellation and expansion replay remain implemented. W1b owns transactional record copying. W9 still needs failure stage, GuardRefusal, materialized-result identity and generation-zero expansion transport. |
| A1-21 through A1-24 | Cancellation intent, session digests/CSRF, binding and audited source release remain implemented. C2 BindingState lacks cancellation reason. Exact digest/operation transport, concurrency target selectors, teamless sessions and origin adapters remain foreign integrations. |
| A1-27, A1-28 | Committed-checkout settlement remains implemented. W1b owns lifecycle accounting integration under the lead's priority handoff. C2 SettleBilling remains a stub. Metered claims still refuse without that integration. |
| A1-29, A1-31 through A1-35 | Billing, blob/upload/record/log persistence and retention remain W1b's ownership. No W1b implementation changed. |
| A1-38 | W8/W9 must supply immutable award-generation and Kubernetes Job-instance correlation. SyncLauncher remains a stub because the present fields cannot distinguish re-awards. |
| A1-40, A1-53 | W8/T1/lead retain readiness, HTTP/api.sock, native platform/provider/Postgres and real-repository evidence. |
| A1-51 | The schedule cut is complete. Legacy AcceptPlan, StateStoreOld, claims/triggers and readiness callers still need migration. |

The cancellation/session, unit-reaper and concurrency-maintenance handoffs remain
done. Seventeen stale default-team exemptions left by those earlier cuts are
removed. W1b's two obsolete output table classifications remain untouched because
the lead assigned those exact edits to W1b. No new foreign contract shape or human
decision is requested.

## Verification

All Go commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-astra-W1/cache, GOTMPDIR=/tmp/sw-astra-W1/tmp and
TMPDIR=/tmp/sw-astra-W1/tmp. Package parallelism is at most four.

- Whole-repository go build -p 4 ./... passes.
- Scoped go vet -p 4 passes for pkg/store/..., internal/crons/..., internal/acceptance/...,
  tests/acceptance/..., pkg/controller/..., pkg/localws/... and cmd/sparkwing/....
- The W1 selection passes **369 top-level tests / 1,041 cases**, without skips.
  Extract Test names from the files in w1-session17-tests.txt, omit TestPostgres
  names, and pass their anchored alternation to go test -json -p 4 ./pkg/store.
- TestTickRefreshesAndEvaluatesThroughEachTenant and the three setting tests
  TestSetComputeLimitRejectsUnknownAndNegative,
  TestSetComputeLimitsWritesEveryNamedGuardOrNone and
  TestSetComputeLimitsRollsBackWhenOneGuardWriteFails pass separately.
- internal/acceptance and tests/acceptance with -skip '^TestProtocol$' pass
  **19 top-level tests / 198 cases**. There are **2,423 native/platform skips**
  in that selection. The new schedule cell also passes separately.
- Required commentcheck -base main passes. Sleepcheck -base HEAD and diff
  whitespace checks pass. The generated pkg_store API snapshot contains only
  this continuation's removals and Operator additions.

No full legacy store/controller/cron suite, PostgreSQL, race, lint, pipeline,
provider, native placement or independent review pass is claimed. The lead still
owns those checks. No candidate binary, service, release or deployment ran.
Bootstrap ran installed sparkwing info before the swarm brief was read.

## Shared caller migrations

Production callers outside pkg/store:

- cmd/sparkwing/crons.go and crons_run.go
- internal/crons/arm.go, crons.go, health.go, override.go, pushed.go and tick.go
- pkg/localws/crons.go

Retained foreign fixtures:

- cmd/sparkwing/crons_profile_test.go
- internal/crons/arm_test.go, crons_test.go and pushed_test.go
- pkg/controller/compute_limits_test.go, crons_team_test.go, crons_test.go and crons_tick_test.go
- pkg/localws/crons_test.go

New shared proof files are internal/crons/tenant_store_test.go,
internal/acceptance/store_schedules.go and tests/acceptance/surface_test.go.
Shared documentation is c1-data.md, handoffs.md, t1-acceptance.md,
t1-port-inventory.md and .apidiff/pkg_store.txt. No W1b source or fixture changed.

## Friction and limits

The first schedule run failed on the missing repo_key and removed metadata table.
The property ran red at that same persistence boundary before the fix. Final
foreign keys then exposed dangling fired-run fixtures, which now seed runs.
The first reopening assertion compared time locations instead of instants. It
now compares the tick instant with Time.Equal.

The broader internal/crons suite still fails on legacy scalar schedule YAML,
fixture Git signing under the sandbox, missing numeric identity on old GitHub
push callers, and dangling run references. The focused real Tick test exposed
the old cadence reader, which is fixed. TestComputeLimitsAreUnlimitedUntilSet
passes its settings assertions but still fails later in seedClaimedNode because
legacy CreateRun names the removed runs.args_json column. It is excluded from
the three passing setting-write tests. No old table or column was restored.

The default-team guard now passes. The tenant writer guard still reports
output_runs and output_tombstones, the two catalog deletions assigned to W1b.
The earlier W9 source-attempt wire omission and W1b result fixture omission are
unchanged. This continuation does not claim those controller/result tests pass.

All writes stay in the worktree and designated scratch. No direct Git write or
harness-memory write ran. Test Git fixtures ran only beneath the owned scratch
and their signed commits were refused. Git identity/worktree configuration is
unchanged. Scratch is removed before reporting.

STATUS: CONTINUE
