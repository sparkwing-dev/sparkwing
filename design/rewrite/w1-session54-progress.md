# W1 session 54: owner completion and unused APIs

W1 remains CONTINUE. Base is 5f4e25d8d. No Git writes ran.

## Implemented and deleted

Removed Tenant.FinishRun and finishRunOnceTx. Completion uses the existing
FinishRunAndUnit transaction. Its ported first-success check ran red because
the final transition omitted run.first_succeeded. The terminal transition now
records that event with the store timestamp. It shares the verdict, owner close,
capacity release and pipeline pin transaction. Event failure rolls everything
back. No new lifecycle owner or compatibility wrapper was added.

Timestamp ports drive real intake, acceptance, claims, job failure,
cancellation and owner completion. Exact replay preserves the full terminal
snapshot. Foreign and missing owner completions fail. The existing generated
owner-authority property now includes business-event counts in its mutation
snapshot. The rollback check refuses both lifecycle and business-event writes.
The first-success port completes a cancelled run and two successful retry runs,
then requires one team event naming a successful run.

Removed the unused concurrency heartbeat interval/timeout functions and both
associated constants. No production caller remained. The shared lease keeper
owns cadence and attempt deadlines. Their three timing tests are retired.

Removed profile_key.go and its four codec tests, including the Windows URL
case. JoinProfileKey and both repository-identity helpers had no production
caller. DisplayProfileKey was the last consumer of SplitProfileKey and its
historical decoding path. The profile view now displays its stored pipeline key.
Current profile tenant-isolation and mutation properties pass.

The legacy direct creation/upsert tests that supply terminal timestamps or
construct a finished pending row are retired. Those shapes are outside the
final lifecycle and schema. The raw intake writer still needs its separate cut.
Reporting, concurrency and transport fixtures now use SeedRunFinish. This
fixture sets status, error and finish time without implementing lifecycle,
authorization or replay. All lifecycle assertions use real transactions.

Two Cloud controller fixtures lacked credits after the placement-metering fix.
They now receive a free grant through RecordCreditGrant. Their plan/node
bootstrap and HTTP exchange tests assert metered provenance from an unmetered
launcher root. All 51 TestEngine functions now pass.

A merged local-state caller still referenced removed GetSecret and failed the
repository build. It now uses GetSecretRow with an explicit empty pipeline.
Its test uses the existing SeedSecret fixture. Exact secret-binding fuzz seeds
pass. No removed API was restored.

## Production volume

Store: **32 added, 222 deleted, net -190 Go lines**. The additions are three
lines in the terminal transition and the 29-line run fixture. Whole-session
production is **34 added, 224 deleted, net -190**, including the two one-line
caller migrations outside store.

Combined W1+W1b against main: **18,939 added, 22,076 deleted, net -3,137**.
The -4,250 target is still **1,113 lines away**. Counts exclude tests, testdata,
acceptance paths and contract files, matching the lead's budget script. There
is no budget-waiver claim.

## Verification

All Go commands use GOWORK=off, GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-gocache and TMPDIR/GOTMPDIR=/tmp/sw-astra-W1. Tests use -p 4.

- Whole repository go build -p 4 ./... passes after the local-state migration.
- Scoped vet passes for store, teststore, controller, web and localcontroller.
- The exact 21 W1 acceptance functions pass with completion, timestamp, tenant,
  profile-isolation and priority properties in one store selection: 14.386s.
- Priority properties cover placement metering, ordered preferences, live
  eligible runners, registered rotation grace, credential deadlines and
  launcher snapshot fences. These were already implemented and were not redone.
- Focused completion, timestamp and authority race checks pass in 8.930s after
  the business-event snapshot change.
- All 51 controller TestEngine functions pass in 3.948s after fixture funding.
  This includes real store claim/exchange paths through in-process HTTP.
- The two strengthened Cloud tests plus timestamp, generated metric HTTP
  authority, validation, paging and old-shape refusal pass in 0.524s.
- Web capacity/charge checks, local-state secret-binding fuzz seeds and teststore
  open checks pass. These are component checks, not native controller startup.
- Commentcheck passes across 2,364 files. API snapshots match generated output.
  Embedded docs/changelog and whitespace checks pass. Changelog checking passes
  with the existing rename-detection warning.

The initial broad controller selection failed the two unfunded Cloud fixtures,
then TestMetrics_ObjectStoreBudgetIsExported panicked on the sandbox's socket
refusal. Funding fixes the first two. The native socket case is still untested.
An initial first-success port tried to fail an empty successful plan and then
tried to recreate the same sign-in fixture. Those fixture errors were corrected
before the missing-event assertion ran red. No whole-repository test suite,
PostgreSQL, native matrix, pipeline, release or independent review is claimed.
The referenced check-changelog-style.sh does not exist in this checkout, so
that separate script could not run. The actual check-changelog.sh passes.

## Contract changes and remaining integration

Trusted schedule/App intake still needs the foreign C4/C2 change from sessions
52 and 53. C1 IntakeRequest requires AuthorityRef before IntakePolicy runs.
C4 IntakeFacts and SealedRequest persist that same credential reference.
A schedule's arming account and an HMAC-verified delivery are not credentials.
C4 needs a tagged committing origin, and C2 needs the verified-delivery fact
reader. The store must re-read current schedule/arming version or installation
and consent facts in the committing transaction. No synthetic session or second
sealed-admission implementation was introduced. Once those declarations land,
route both origins through admitRun and remove triggerIntake, triggerRows,
Tenant.CreateTriggerWithRun and the legacy insertion path.

Other open integrations remain: full enrolled-runner capacity/renewal
observations, legacy-schema reporting readers, publisher metric binding targets
and usage report identity for exact-body retry. In particular GetNode still
selects node_outputs, and raw run/trigger projections select removed columns.
C2's new TeamWriter and GitHubAppWriter groups also lack concrete tenant
implementations. Their defaults refuse writes. BillingWriter needs W1b's billing
composition. These are code-inspection gaps, not passing integration claims.
No W1b production file or materialization code was changed.

## Shared files and handoff

Production outside store:

- internal/web/capacity.go displays the actual stored pipeline key.
- internal/wingd/localcontroller/state.go selects the exact global secret row.

Fixture migrations outside store:

- internal/web/server_test.go
- internal/wingd/localcontroller/state_test.go
- internal/controller/compute_limits_test.go
- internal/controller/github_checks_test.go
- internal/controller/reaped_run_conflict_test.go
- internal/controller/retry_test.go
- internal/controller/secret_args_display_test.go
- internal/controller/trends_test.go

The two Cloud fixture and metering assertions are in controller's
engine_amendment_test.go and engine_claim_http_test.go. Store's shared acceptance
snapshot now counts business events. C1, the port inventory, migration note,
changelog, embedded copies and two API snapshots are updated. Lead-owned
handoffs.md and amendments files are unchanged.

The only added helper is SeedRunFinish in the existing teststore package,
because unrelated read/transport fixtures need terminal facts after the raw
writer is removed. It carries no production authority. No schema, setting,
flag, environment variable, limit or runtime service was added. Undoing the
fixture change means migrating those fixtures to complete live executions.

Friction: the merged local-state stale calls broke build/test compilation.
Legacy Cloud fixtures assumed free launcher awards. The socket-dependent metric
check needs the lead's native run. The worktree Git config has neither
core.worktree nor a fixture user.name. Scratch is removed after final checks.
The shared Go cache is retained. The lead owns review and committing.

STATUS: CONTINUE
