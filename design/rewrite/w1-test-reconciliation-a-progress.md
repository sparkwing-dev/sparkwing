# W1 test reconciliation-a progress

## Current continuation: Opus review rulings

Implemented rulings 1-8, with 6 covered by 2. Each finding ran red through its
new or adjusted regression before the production change. Ruling 9 leaves
PostgreSQL completion SQL unchanged for the lead's native run.

| Ruling | Result and invariant |
| --- | --- |
| 1 | Clearing a renamed source's extra repositories resolves its numeric ID even for an empty list. Replacement, deletion and per-source reads use repository_id. Generated HTTP tests prove later tokens lose the old extras and cannot follow a reused name. |
| 2, 6 | Source release validates in a short transaction, mints by exact IDs with no transaction open, then rechecks installation, subscription or schedule consent, approved IDs, enrollment and claim fences before auditing. Generated tests mutate those facts during minting. A source beyond the listing's first 1,000 repositories succeeds. Uncovered ID sets return 409; other mint failures return 503. |
| 3 | Both source paths enforce the profile Accept match and same-team machine root, except verified Cloud placement. App tokens remain exempt from owner opt-in. Generated foreign-team, service-root and nonmatching-profile cases return no token or audit. |
| 4 | Only the two operator quota routes use operator_settings to select another team. BindNone refuses cross-team targets. The existing settings positive property and the new refusal property pass. |
| 5 | ArmCronSchedule derives identity and ignores the caller's RepositoryKey. Manual GitHub schedules use the existing intake policy's server-owned source resolver. App schedules use verified IDs. Manual conflict tests now resolve through production. The webhook already passed IDs without a RepositoryKey. |
| 7 | Scheduled runs use ArmedBy for attribution, retaining the schedule ID in the sealed authority. Two schedules armed by one principal share its hourly cap. |
| 8 | Invalid committed App facts record one failed attempt and consume only that revision. The controller logs once. Transient provider failures retain exponential backoff. Generated tests include invalid execution and pull-request SHAs and a second reconciliation. |

## Verification

Whole-repository build passes. Scoped vet, commentcheck against main and diff
checks pass. Commentcheck is clean across 2,688 files. All Go commands used
GOWORK=off, GOFLAGS=-buildvcs=false, GOCACHE=/tmp/sw-gocache, GOMAXPROCS=4 and
at most -p 4, with temporary files under /tmp/sw-astra-W1.

- Final controller selection: **126 top-level groups pass**, no failures or
  skips, 38.677s. It includes App, runner, onboarding, source, completion,
  operator-settings, route-policy and tick tests.
- Focused store selection passes, 18.507s. It covers source release, scheduled
  consent, attribution, derived identity, extra repositories, binding,
  settings and completion. The final manual identity selection passes, 0.782s.
- Source/consent HTTP acceptance tests pass, 0.041s. The store schedule boundary
  and declared-repository guard pass, 1.048s. No native matrix cell is claimed.
- Focused store race selection passes, **141.544s**. It covers source minting,
  consent/fence changes, scheduled source, attribution and binding properties.
- Full internal/crons, internal/capability and pkg/docs suites pass (4.587s,
  0.094s and 1.481s). Source docs and embedded mirrors match.

HTTP component checks use a temporary standard-library net.Pipe overlay only.
The real handlers, authorization, SQLite transactions and fake GitHub provider
run. The Cloud source fixture now reports Cloud enrollment instead of an empty
location. Its two pre-fix refusals disappeared without changing production checks.
No test was removed, skipped or newly ported from the legacy inventory.

## Contracts, volume and handoff

C1 records the reduced GitHubSourceProvider interface, three-step release,
repository-ID reads, derived schedule identity, ArmedBy attribution and
RejectRunEffect. C2 records operator_settings and terminal invalid completion
facts. No other contract change is requested. Lead-owned amendments and handoffs
remain untouched. Earlier assigned handoffs are implemented or reassigned as
recorded below. Retry selection remains assigned to W3.

Session production change is **+42 lines**: **128 added, 86 deleted**. Removed
the in-transaction GitHub listing/mint flow, caller-key identity fallback and
slug-based extra selection. No production file was deleted. Store growth is
+30 lines. The cumulative store delta is **-2,421 versus main**, **1,829 over**
the -4,250 budget. This review slice adds no new policy options, cache, setting,
environment variable or schema. The inherited area reduction remains open.

Mechanisms added: one named binding rule confines quota targeting; the existing
intake policy optionally implements ScheduleRepositoryResolver so manual GitHub
schedules reuse production identity lookup instead of caller assertions; and
RejectRunEffect uses the existing revision-fenced delivery transaction. Removing
the resolver would leave manual GitHub schedules unable to derive identity.
Undo removes its interface, controller method and arming assertion together.

Shared files touched: internal/capability/policy.go and routes.go;
pkg/controller/completion.go, github_app.go, github_app_extra_repos.go,
intake_source.go and their route/source/completion tests; claim_source_test.go
and github_claim_fixture_test.go for the Cloud enrollment fixture;
github_app_crons_identity_test.go for production identity resolution; C1, C2,
docs/github-app.md, its embedded mirror, this report and the reconciliation
ledger. No foreign production caller required migration after the provider
interface reduction. Store's GitHubAppExtraRepos test callers now pass numeric
IDs, and its clearing fixture supplies a resolved source ID.

The latest native report contains only its session-0 header and opening fence.
It supplies no completed verdict. Live sockets, GitHub, PostgreSQL lock behavior,
completion SQL (ruling 9), runner execution and real-repository QA remain lead
integration evidence. The separate ScheduleWriter route integration belongs to
the other worker. These limits and the budget prevent COMPLETE.

No Git write occurred. The local Git config has no user identity or core.worktree
override. Independent review and commit remain with the lead. Scratch is removed
at session end and /tmp/sw-gocache is retained.

## Previous continuation (committed in 8e20b3733)

The lead's native report after session 4 has no failures under the assigned
controller prefixes. Its one failure outside `pkg/controller` is
`TestDeclaredRepoGuard_OnlyReviewedFilesReadTheField`. The focused command
reproduced both errors before the correction (0.272s) and passes afterward
(0.277s).

Removed the stale `pkg/controller/handlers.go` reader entry and reviewed the
`pkg/store/teststore/trigger.go` reader. The fixture copies display metadata into
the existing reporting seed without granting authority. The guard still rejects
unlisted readers, empty reasons and stale entries. No assertion, production code,
contract or acceptance cell changed. No test was deleted or skipped.

Verification uses GOWORK=off, GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-gocache, GOMAXPROCS=4 and -p 4, with scratch beneath
/tmp/sw-astra-W1. Whole-repository build and scoped store/teststore vet pass.
The declared-repository, SQL-scope, tenant-writer and reporting-seed selection
passes (store 0.640s, teststore 0.163s). Required commentcheck against main is
clean across 2,688 files. These checks use no socket substitution.

Current-session production delta is **0**. Deleted production lines: **0**.
Store remains **-2,451 versus main**, **1,799 over** its -4,250 budget under the
lead's filter. The inherited production reductions and source/schedule proofs
below are not repeated. No new mechanisms or external integration points exist.

Shared files touched are this report and the append-only reconciliation ledger.
The only Go edit is `pkg/store/declared_repo_guard_test.go`. The shared Git config
has no user identity or core.worktree override. No Git write occurred.

The 01:15 retry-selection redesign belongs to W3 (env 1081), as stated in the
lead-owned handoff. The previous report incorrectly treated it as W1 remaining
work. ScheduleWriter route failures remain with the other reconciliation worker.
No unfinished amendment assigned to this reconciliation slice was found.

The supplied native report still contains FAIL and predates this correction.
The lead must commit and rerun the native tests. Completion is not claimed.
Scratch is removed at session end. The shared build cache is retained.

## Previous continuation (committed in 000d06457)

All **97 assigned controller groups pass**, with no failures or skips (24.073s).
This includes the 96 previously reconciled groups and the new App-schedule
source regression. The supplied native report still contains FAIL. Its remaining
failures are outside this worker's assigned controller prefixes. Completion is
not claimed.

## Implemented and deleted

Controller schedules now enter the same `admitRun` transaction as other origins.
The persisted schedule is the unattended execution consent. A private schedule
service authority resolves only inside that transaction. It never fabricates a
capability, account or session from `ArmedBy`. Admission locks and rechecks the
current tenant, source, effective declaration and arming identity. Pending team
deletion and changed declarations refuse admission. App-managed schedules also
require the live same-team installation and automation consent.

The shared intake owns first sealing, settings and ceilings, run numbering,
run-rate guards, queue rows and completion intents. Exact due instants replay.
Different nanosecond instants stay distinct. Changed execution inputs under one
key conflict. A failing completion write rolls back the whole admission. Manual
run-now still accepts a paused schedule. Schedule history updates do not change
execution consent.

A real controller-to-source test then reproduced a 404: the source release path
recognized subscriptions but not schedule automation consent. Source release
now reads the sealed schedule authority ID and checks its exact tenant,
repository ID, pipeline and live automation relation. It locks the installation
before locking and rechecking that relation. The existing contents-only mint,
source/extra numeric-ID scope, claim fence and committing audit remain the only
release path. No App event or default-branch trust is manufactured.

Removed the generic trigger writers and their stale-column SQL:
`Tenant.CreateTrigger`, `CreateTriggerWithRun`, `FindTriggerByIdempotencyKey`,
`createTriggerTx`, `Store.createRunTx`, the duplicate-trigger sentinel errors,
controller `triggerIntake`, `triggerRows`, `triggerRunInvocation`,
`triggerReqGit` and `CronIdempotencyKey`. `WithCreatingPrincipal` is removed from
production. Its remaining reporting-fixture use is test-only. Required callers
use explicit-team current-schema fixtures.

Deleted the two tests for retired helper/error contracts. The tick tests now
assert schedule identity in the sealed request, replacing removed trigger-env
metadata. No security, tenant, masking or billing assertion was weakened.

## Volume and contract changes

Session production change is **-217 lines**: 163 added, 380 deleted under the
lead's exclusions. This includes the 37-line teststore fixture file. Store is
**-2,451 versus main**, still **1,799 over** the W1+W1b budget of -4,250.
The remaining overage needs a separate store reduction/review. This session cuts
more than it adds. The new production path is the missing scheduled-origin
adapter and its source-consent integration, not an additional intake owner.

C1 records `AdmitScheduledRun`, `VerifiedScheduleIntake`, durable schedule
consent, the transaction-only authority, exact-instant replay, source release
and the final raw-writer cut. No schema, public wire type or C2 interface changed.
The lead-owned amendments and handoffs remain untouched.

Mechanisms added: one private transaction proof and its field on `storeTx`, using
the existing authority resolver. A browser session would expire unattended work.
An armer string cannot prove a live credential. The raw writer bypassed sealing.
The service subject is the schedule ID, so the existing per-principal guard
counts that service. The deployment guard remains shared. The source lookup uses
that sealed ID rather than adding event facts, a credential or a schema column.
Undo removes this adapter and its policy/source branches together. There is no
legacy fallback, new environment variable, setting, limit, cache or service.

## Verification

All Go commands use GOWORK=off, GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-gocache, GOMAXPROCS=4 and at most -p 4. Scratch and GOTMPDIR stay
under /tmp/sw-astra-W1.

- Whole-repository build passes after the writer removal.
- Final assigned controller selection: 97/97 groups pass, zero skips, 24.073s.
- Scheduled intake, current tenant/declaration and App consent properties pass.
  Removing the declaration fence in a temporary overlay makes the generated
  property fail at the forged source. Production source is not edited by it.
- Real controller policy, sealed queue and both migrated tick groups pass.
- Schedule/tick/store-boundary race checks pass: store 64.761s, controller 4.592s.
- Scheduled source and existing source-release checks pass: store 3.860s and
  controller 0.257s. The HTTP regression fails with 404 before the source change.
- Schedule transport, source HTTP audit/refusal/consent acceptance checks and
  schema/dialect/SQL-scope checks pass. Existing store-sqlite schedule boundary
  remains green. No native matrix cell is newly claimed.
- teststore, controller CLI and web suites pass. Web uses the component overlay.
- bin, cmd/sparkwing and internal/wingd test packages compile. Their suites were
  not executed here, and no new native result is claimed.
- Full docs suite passes with the component overlay (1.439s). Source docs and
  embedded mirrors were synchronized through bin/sync-docs.sh.
- Scoped vet, commentcheck and diff checks pass.
- Final whole-repository build and scoped vet pass. Source-release race checks
  pass in 87.349s. Commentcheck is clean across 2,688 files. Diff checks pass.
- Final schedule/HTTP acceptance/schema selection passes: store 6.098s,
  controller 0.166s and acceptance 0.070s.

The controller/provider and web/docs component checks reuse the previous
standard-library httptest overlay: net.Pipe listeners and default-transport
routing only. Real routers, provider fakes, issuance, policy, source audits and
SQLite transactions run. Unmodified socket-dependent web/docs tests refuse
`listen` in this sandbox. That is an environment limit, not native passing proof.

## Remaining work and integration points

The scheduled-origin half of the 14:40 W1 handoff and its legacy insertion cut
are implemented. The App-delivery half was completed earlier. Enrollment,
rotation, ordered placement, Cloud metering and connection work are not repeated.

The lead's 2026-10-17 01:15 retry-selection redesign is assigned to W3 in env
1081 under integration authority. Its source-selection, replay and host-recovery
changes are outside this reconciliation slice. No fix is claimed here.

The controller `ScheduleWriter` administration/run-now interface is still absent
from Tenant. Its policy_missing route failures belong to the other reconciliation
worker. This session exercises the production cron launcher and shared store
adapter directly. Those handlers must call that owner inside their committing
authorization transaction when they are connected. No successful fake is used to
claim that HTTP integration is complete.

Native sockets, live GitHub installations, PostgreSQL lock-order/concurrency,
real runner execution and real-repository QA remain lead evidence. The source
provider is the existing fake GitHub API. The native report must be refreshed
after the lead commits. Its FAIL verdict and the store budget prevent COMPLETE.

## Shared files touched

- C1 contract, reconciliation ledger and this report.
- docs/schedules.md, docs/migrations/_unreleased.md and generated mirrors.
- pkg/controller/crons.go, handlers.go and intake_policy.go.
- pkg/controller/schedule_intake_test.go and source_credential_test.go.
- pkg/controller/crons_tick_test.go: required scheduled-intake caller migration.
- pkg/controller/compute_limits_test.go, concurrency_team_test.go,
  deletion_test.go, flood_team_test.go, flood_test.go, retry_team_test.go and
  team_boundary_test.go: minimal removed-writer fixture migrations only.
- pkg/controller/trigger_rows_internal_test.go: removed retired helper test.

Store changes: schedule_intake.go and tests, github_source_release.go and its
property, capabilities_contract.go, dialect.go, store.go, tenant_triggers.go,
compute_limits.go, crons.go, reporting_fixture_test.go, deletion_test.go,
identity_link_test.go, teststore/trigger.go and deletion of
maintenance_result_test.go. No other worker's production implementation changed.

Friction: sockets require component substitution and lead native evidence. The
new schedule source regression first needed the fake provider's main ref and
config installed before the real consent route would accept it. No Git write,
Sparkwing candidate execution, external message or harness-memory write occurred.
The shared Git config has no local identity or core.worktree override. Scratch is
removed at session end. The shared Go cache is retained.

STATUS: CONTINUE
