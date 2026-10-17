# W1 test reconciliation-a progress

Native starting report: 110 top-level failures, 48 in the assigned controller
prefixes. That file still reflects the last committed state and still has FAIL.
Local final evidence: 44 of 93 assigned controller groups pass, 49 fail.
The first complete local sweep after the connection/fixture fixes was 36/93.
No acceptance matrix or native transport cell is newly claimed complete.

Implemented the existing C2 GitHubConnectWriter transactions and compile-time
controller conformance. The invariant is that only the same live direct owner
session for the exact team/account can read the linked identity, consume the
nonce or bind the verified installation. The bind transaction rechecks the
provider identity, derives the connector and cannot take a foreign installation.
Generated authority refusals, exact expiry, one-use concurrency and rollback pass.

Runner removal now opens its enrollment-owned file through OpenPrivateConfig.
The invariant is that an unsafe credential mode cannot stop the service or revoke
authority. Generated permission bits and the original regression pass under race.
The shared mounted-credential reader is unchanged. The Actions credential quota
now returns the required RateLimited envelope instead of internal error.

Deleted both raw GitHub connection APIs. Their fixture callers migrate to the
explicit test-only seed and private nonce helper. Production delta this session:
119 added, 19 deleted, net +100 Go lines. The store share is +95. Against main,
W1+W1b store is -2,590 production lines under the lead's exclusions, still 1,660
over the -4,250 target. The inherited budget gap remains open. The added concrete
transactions implement the already-declared connection contract. No new option,
interface, setting, environment input, compatibility path or schema is added.

Reconciled current runner enrollment routes, real browser session/CSRF evidence,
fresh invitation replay keys, App schedule list syntax, current log job fields,
CLI error wording, selected-revision Git arguments, dependency-preparation fakes,
SDK replacement engine selection and the description fixture environment.
Every original security, tenant, masking and billing assertion remains enabled.
The per-group changes and open failures are appended to test-reconciliation.md.

## Verification

All checks use GOWORK=off, GOFLAGS=-buildvcs=false, GOCACHE=/tmp/sw-gocache,
GOMAXPROCS=4, -p 4 and TMPDIR/GOTMPDIR beneath /tmp/sw-astra-W1.

- Whole-repository go build ./... passes. Scoped go vet passes for store,
  controller, both changed commands, web and wingd. Final commentcheck is clean.
- Store GitHub connection/App/nonce selections pass normally and under race
  (76.489s). The scoped SQL guard selection also passes.
- Runner-removal regressions and generated permission property pass normally
  and under race (1.116s).
- Full cmd/sparkwing-controller and internal/web suites pass with temporary
  HTTP transport (1.953s and 2.547s).
- The final targeted CLI/wingd/controller selections pass (1.797s, 0.121s,
  5.139s). The separately corrected large-installation group passes (0.495s).
- Docs source/mirror guard passes. The source credential guide records the
  existing private-file removal requirement and sync-docs regenerates its mirror.
- git diff --check passes. No local Git identity/core.worktree override exists.

TCP/Unix sockets are forbidden here. A throwaway standard-library Go overlay
replaces only httptest listeners with net.Pipe and routes the default HTTP
transport to them. Actual HTTP handlers, provider fakes, authentication and
transactions still execute. This is component evidence, not native socket proof.
It is removed with the scratch directory. No test is skipped or disabled.

The outside-package sweep still fails when subprocesses require sockets or the
module proxy. The test process's memory listener cannot serve a Docker subprocess.
Keep bin install-to-green/release-manifest, offline run/output-daemon and native
wingd proofs with the lead. No environment failure is reported as a pass.

## Remaining work

- Trusted App delivery/consent must enter the single sealed admission transaction.
  The webhook still reaches CreateTriggerWithRun and retired trigger columns.
  Retain rate, replay, fork, tenant, signed-body and consent-race assertions.
- Four App/manual schedule conflict tests seed unresolved GitHub identities that
  the current store refuses. Reconcile the native manual schedule contract and
  its committing adapter before changing these assertions or seeding rows.
- Port three GitHub runner claim groups to current unit/run claims and sealed
  numeric repository/push facts. They still seed legacy trigger rows.
- Port seven source credential groups to accepted requests/current claims and
  connect App credential acquisition to the shared source contract. The old
  source helper still seeds removed columns. No App token assertion is dropped.
- Re-request/check-suite prerequisites still need native App intake. No failing
  test is deleted to conceal those missing integrations.
- Previous sealed schedule intake, legacy intake deletion and the store budget
  overage remain open. Completed earlier metering/placement/rotation work is
  not rebuilt. Lead-owned handoffs and amendments remain unchanged.

Contract changes made: C1 records fulfillment of the existing C2 connection
interface, the raw API cuts and nanosecond installation timestamps. No contract
signature or schema amendment is needed. No further cross-area contract changes
are proposed in this session.

## Remaining controller groups

- TestRunSourceCredential_MintsTheApprovedRepositoriesByID
- TestRunSourceCredential_NeedsTheGitHubApp
- TestGitHubAppAutomationRevokedDuringDiscoveryStartsNoRun
- TestGitHubAppAutomationRequiresConsentAndUsesDefaultBranch
- TestGitHubAppAutomationConfigErrorsAndManualOverride
- TestGitHubAppCronConflictsWithManualControllerSchedule
- TestGitHubAppCronRenameConflictStopsOldAppRow
- TestGitHubAppCronRefusesManualScheduleAtOldRedirectedURL
- TestGitHubAppCronCannotProveManualGitHubURLDistinct
- TestGitHubAppCronFailureDoesNotSuppressSubscribedPush
- TestGitHubAppStaleCronPushStillStartsSubscribedPipeline
- TestGitHubAppPushRunsOnlyInTheInstallationsTeam
- TestGitHubAppRunLimitRefusesWithoutConsumingDelivery
- TestGitHubAppTagPushRequiresTagSubscription
- TestGitHubAppTagPatternsFilterPushes
- TestGitHubAppPushBranchSubscription
- TestGitHubAppPullRequestBaseBranchSubscription
- TestGitHubAppBranchFilterLeavesTagPatternsAlone
- TestGitHubAppRedeliveryStartsOneRun
- TestGitHubAppForkPullRequestIsNotRun
- TestGitHubAppAdditionalEventsRequireSubscription
- TestGitHubAppRepositoryIdentitySurvivesRenameAndTransfer
- TestGitHubAppReleaseReplayAndCoverage
- TestGitHubAppUndatedEventIsIgnored
- TestGitHubAppRedeliveryChargesOnlyShedRuns
- TestGitHubAppPartialReleaseRedeliveryKeepsResolvedCommit
- TestGitHubAppFloodCapCountsTheTeamsRuns
- TestGitHubAppReplayAfterAMoveStartsNothing
- TestGitHubAppBranchFiltersGateBranchAndPullRequestEvents
- TestGitHubAppSignedBodyKeepsItsEvent
- TestGitHubAppBranchDeleteRunsOnGitHubsPayload
- TestGitHubAppEventOlderThanTheReplayWindowIsRefused
- TestGitHubAppRunRecordsTheDefaultBranch
- TestGitHubAppCheckRunRerequestedRerunsItsPipeline
- TestGitHubAppCheckSuiteRerequestedRerunsEveryPipeline
- TestGitHubAppCheckSuiteRerequestedNeedsEachPipelinesOwnRun
- TestGitHubAppRerequestedNeedsPriorEventsSubscription
- TestGitHubAppRerequestedUsesCurrentBranchFilter
- TestGitHubAppRerequestedForkOrForeignCommitStartsNothing
- TestGitHubAppRerequestedNeedsTheBinding
- TestGitHubAppRerequestedSpendsTheRunBudget
- TestGitHubRunnerCredentialClaimsOnlyItsRepository
- TestGitHubRunnerCredentialReachesNothingOutsideItsRoutes
- TestGitHubRunnerCredentialClaimsOnlyItsOwnPush
- TestSourceCredential_AppTokenReadsTheVerifiedRepository
- TestSourceCredential_TypedRepositoryNeedsASubscription
- TestSourceCredential_NamesTheRemedy
- TestSourceCredential_NeedsTheRunsOwnClaimToken
- TestSourceCredential_AppTokenReadsOnlyTheOwnersExtraRepos

## Shared files touched

- cmd/sparkwing-controller/secrets_key_test.go
- cmd/sparkwing/compile_toolchain_test.go
- cmd/sparkwing/docs_auth_test.go
- cmd/sparkwing/run_offline_unix_test.go
- cmd/sparkwing/runner_preferences_test.go
- cmd/sparkwing/runners.go
- cmd/sparkwing/schedules_repository_environment_test.go
- cmd/sparkwing/trigger_selection_unix_test.go
- cmd/sparkwing/update_release_workflow_test.go
- cmd/sparkwing/update_unified_test.go
- cmd/sparkwing/xrepo_describe_test.go
- cmd/sparkwing/xrepo_flags_test.go
- design/rewrite/contracts/c1-data.md
- design/rewrite/test-reconciliation.md
- docs/credentials.md
- internal/web/logpretty_test.go
- internal/wingd/idle_exit_parallel_test.go
- pkg/controller/browser_github_app_routes.go
- pkg/controller/github_actions_exchange.go
- pkg/controller/github_app_crons_identity_test.go
- pkg/controller/github_app_crons_test.go
- pkg/controller/github_app_native_routes_test.go
- pkg/controller/github_app_test.go
- pkg/controller/github_completion_test.go
- pkg/controller/github_onboarding_browser_test.go
- pkg/controller/github_runner_onboarding_test.go
- pkg/controller/github_runners_test.go
- pkg/controller/identity_fixture_test.go
- pkg/controller/intake_source_test.go
- pkg/docs/mirror/credentials.md
- pkg/store/claim_source_test.go
- pkg/store/flow_nonce_runtime_test.go
- pkg/store/github_app.go
- pkg/store/github_app_automation_test.go
- pkg/store/github_app_extra_repos_test.go
- pkg/store/github_app_mutations_test.go
- pkg/store/github_app_test.go
- pkg/store/github_connect.go
- pkg/store/github_connect_test.go

Friction: Xwing env context cannot create its read-only sessions-index lock.
The assigned environment was reused. Sockets, external module downloads and
subprocess transport remain unavailable. No Sparkwing candidate, pipeline,
Git write, external message or harness memory write is performed. Native
acceptance and independent security review remain lead-owned. W1 scratch is
removed after checks, and the shared Go cache is retained.

STATUS: CONTINUE
