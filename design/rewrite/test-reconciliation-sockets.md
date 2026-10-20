# Socket-dependent reconciliation checks

These tests were attempted and hit forbidden TCP, UDP/DNS or Unix socket operations in the sandbox. They remain enabled. PostgreSQL verification also requires socket access and was not attempted against the lead's database. A panic stops a package run, so additional socket-dependent cases can remain beyond the names listed for packages other than internal/controller.

- `bin`: `TestInstallToGreenBuildUsesPublicScaffoldAndCandidateSDK`.
- `bin`: `TestReleaseManifestCanBePublishedByDigestWithoutATag`.
- `cmd/sparkwing`: `TestResolveSparks_WithManifest_WritesOverlay`.
- `cmd/sparkwing-controller`: `TestConfigureSecrets_SingleTeamSQLiteCreatesItsKeyOnTheFirstSecret`.
- `internal/web`: `TestEventStreamsAreNeverEncoded`.
- `internal/wingd`: `TestAPIListenerClosesBeforeTheDrainIsAcknowledged`.
- `internal/wingd`: `TestAPISocketRefusesAPeerFromAnotherAccount`.
- `internal/wingd`: `TestAPISocketSitsBesideAdmissionAtMode0600`.
- `internal/wingd`: `TestTypeSafeJevAdvisorUsesTypedChoice`.
- `internal/controller`: `ExampleNew`.
- `internal/controller`: `TestAClosedFreeTierWaitlistsNewAccounts`.
- `internal/controller`: `TestAdminTokenReachesNoOperatorPower`.
- `internal/controller`: `TestAnUnreadableFreeTierWaitlistsNewAccounts`.
- `internal/controller`: `TestAnUnwiredFreeTierLogsOnceAtStartup`.
- `internal/controller`: `TestApprovingTheOldestRunsTheNotifier`.
- `internal/controller`: `TestBillingTrust_OperatorGrantsAndRevokes`.
- `internal/controller`: `TestCardBilling_OwnerSetsABudget`.
- `internal/controller`: `TestClaimRun_ChildRunsInheritTheParentAndAnswerOnlyIt`.
- `internal/controller`: `TestClaimRun_PlanRefusedWhenARequiredSecretIsMissing`.
- `internal/controller`: `TestComputeLimits_NodeCreationRefusedPastThePerRunCap`.
- `internal/controller`: `TestComputeLimits_RefusesBadSiblingWithoutChangingGuards`.
- `internal/controller`: `TestComputeLimits_RefusesExplicitNullWithoutChangingGuards`.
- `internal/controller`: `TestComputeLimits_RetriesCountAgainstTheRetryingPrincipal`.
- `internal/controller`: `TestComputeLimits_RunCreationRefusedPastTheHourlyCap`.
- `internal/controller`: `TestComputeLimits_ShowAndSet`.
- `internal/controller`: `TestComputeLimits_TriggeredRunsCountAgainstTheTriggeringPrincipal`.
- `internal/controller`: `TestControllerFixturesKeepCredentialsAndRunsSeparate`.
- `internal/controller`: `TestCreditSettings_ReadNeedsRunsReadAndWriteNeedsAdmin`.
- `internal/controller`: `TestCreditSettings_ReadsTheDefaultRateLadder`.
- `internal/controller`: `TestCreditSettings_RefusesARateTableTheLedgerCannotPrice`.
- `internal/controller`: `TestCreditSettings_RefusesTheScalarOnceATableExists`.
- `internal/controller`: `TestCreditSettings_TakesTheRateTableAsAListOrAsAnObject`.
- `internal/controller`: `TestCredits_AClaimAboveTheLargestClassFailsTheNode`.
- `internal/controller`: `TestCredits_AClaimsOwnCPUFigureDoesNotLowerTheBill`.
- `internal/controller`: `TestCredits_ANamedClaimThatDoesNotSizeToClassIsRefused`.
- `internal/controller`: `TestCredits_AWarmClaimPassesOverAClassAboveTheWarmPool`.
- `internal/controller`: `TestCredits_QuarterCorePinReservesTwoCoreMinimum`.
- `internal/controller`: `TestCredits_RoutesShowGrantAndHistory`.
- `internal/controller`: `TestCycleDetect_AllowsIndirectNonCycle`.
- `internal/controller`: `TestCycleDetect_ParentNotFound400`.
- `internal/controller`: `TestCycleDetect_RejectsDeepCycle`.
- `internal/controller`: `TestCycleDetect_RejectsSelfCycle`.
- `internal/controller`: `TestDashboardReadsStayInsideTheTeamBoundary`.
- `internal/controller`: `TestDashboardStreamsOutliveTheListenersWriteTimeout`.
- `internal/controller`: `TestDeleteAccountNeedsARecentSignIn`.
- `internal/controller`: `TestDeleteAccountOwningTheDefaultTeamIsABadRequest`.
- `internal/controller`: `TestDeleteTeamClosesItAtOnceAndThePassRemovesItsRows`.
- `internal/controller`: `TestDeleteTeamRefusesAnOpenCheckoutOrADisputeHold`.
- `internal/controller`: `TestDeleteTeamRefusesTheOnlyTeam`.
- `internal/controller`: `TestFloodPolicy_ATeamsTokensShareOneCap`.
- `internal/controller`: `TestFloodPolicy_CapsRunsPerPrincipalHour`.
- `internal/controller`: `TestFloodPolicy_DedupeIsScopedToThePrincipal`.
- `internal/controller`: `TestFloodPolicy_DedupesIdenticalSubmissionsInsideTheWindow`.
- `internal/controller`: `TestFloodPolicy_ShedsAboveTheQueueDepthThreshold`.
- `internal/controller`: `TestFloodPolicy_ShedsOnlyTheTeamWhoseQueueIsDeep`.
- `internal/controller`: `TestFloodPolicy_UnsetCapAdmitsEverySubmission`.
- `internal/controller`: `TestFloodPolicy_UnsetWindowDedupesNothing`.
- `internal/controller`: `TestGitHubAppAdditionalEventsRequireSubscription`.
- `internal/controller`: `TestGitHubAppAutomationConfigErrorsAndManualOverride`.
- `internal/controller`: `TestGitHubAppAutomationFailsClosedAndRemovalRevokesConsent`.
- `internal/controller`: `TestGitHubAppAutomationRequiresConsentAndUsesDefaultBranch`.
- `internal/controller`: `TestGitHubAppAutomationRevokedDuringDiscoveryStartsNoRun`.
- `internal/controller`: `TestGitHubAppBranchDeleteRunsOnGitHubsPayload`.
- `internal/controller`: `TestGitHubAppBranchFilterLeavesTagPatternsAlone`.
- `internal/controller`: `TestGitHubAppBranchFiltersGateBranchAndPullRequestEvents`.
- `internal/controller`: `TestGitHubAppCheckRunRerequestedRerunsItsPipeline`.
- `internal/controller`: `TestGitHubAppCheckSuiteRerequestedNeedsEachPipelinesOwnRun`.
- `internal/controller`: `TestGitHubAppCheckSuiteRerequestedRerunsEveryPipeline`.
- `internal/controller`: `TestGitHubAppConnectBindsAnInstallationItsOrgAdminProves`.
- `internal/controller`: `TestGitHubAppConnectNeedsTheAccountsAdmin`.
- `internal/controller`: `TestGitHubAppConnectRefusesForgedReplayedAndForeignState`.
- `internal/controller`: `TestGitHubAppConnectStateIsSingleUseAcrossReplicas`.
- `internal/controller`: `TestGitHubAppCronCannotProveManualGitHubURLDistinct`.
- `internal/controller`: `TestGitHubAppCronConflictsWithManualControllerSchedule`.
- `internal/controller`: `TestGitHubAppCronFailureDoesNotSuppressSubscribedPush`.
- `internal/controller`: `TestGitHubAppCronLifecycleWithdrawsOldBindings`.
- `internal/controller`: `TestGitHubAppCronPushGuardsPreserveExistingSchedules`.
- `internal/controller`: `TestGitHubAppCronRefusesManualScheduleAtOldRedirectedURL`.
- `internal/controller`: `TestGitHubAppCronRenameConflictStopsOldAppRow`.
- `internal/controller`: `TestGitHubAppCronRenameKeepsIdentityAndPauseAtRepoCap`.
- `internal/controller`: `TestGitHubAppCronTransferWithdrawsFormerTeamBeforeNewArm`.
- `internal/controller`: `TestGitHubAppDefaultPushArmsAndWithdrawsControllerSchedules`.
- `internal/controller`: `TestGitHubAppEventOlderThanTheReplayWindowIsRefused`.
- `internal/controller`: `TestGitHubAppExistingInstallationPicker`.
- `internal/controller`: `TestGitHubAppExistingPickerHidesVisibleInstallationWithoutAdminMembership`.
- `internal/controller`: `TestGitHubAppExistingPickerOnlyBindsListedInstallations`.
- `internal/controller`: `TestGitHubAppExistingPickerRefusesForeignIdentityAndBoundInstallation`.
- `internal/controller`: `TestGitHubAppFloodCapCountsTheTeamsRuns`.
- `internal/controller`: `TestGitHubAppForkPullRequestIsNotRun`.
- `internal/controller`: `TestGitHubAppInstallationBelongsToOneTeam`.
- `internal/controller`: `TestGitHubAppInstallationEventNamesThisApp`.
- `internal/controller`: `TestGitHubAppLargeInstallationKeepsSchedulesPastTheListingCap`.
- `internal/controller`: `TestGitHubAppLateRemovalKeepsARepositoryTheCutListingShows`.
- `internal/controller`: `TestGitHubAppPartialReleaseRedeliveryKeepsResolvedCommit`.
- `internal/controller`: `TestGitHubAppPullRequestBaseBranchSubscription`.
- `internal/controller`: `TestGitHubAppPushBranchSubscription`.
- `internal/controller`: `TestGitHubAppPushOfNoCommitIsIgnored`.
- `internal/controller`: `TestGitHubAppPushRunsOnlyInTheInstallationsTeam`.
- `internal/controller`: `TestGitHubAppRedeliveryChargesOnlyShedRuns`.
- `internal/controller`: `TestGitHubAppRedeliveryStartsOneRun`.
- `internal/controller`: `TestGitHubAppReleaseReplayAndCoverage`.
- `internal/controller`: `TestGitHubAppReplayAfterAMoveStartsNothing`.
- `internal/controller`: `TestGitHubAppRepositoryIdentitySurvivesRenameAndTransfer`.
- `internal/controller`: `TestGitHubAppRerequestedForkOrForeignCommitStartsNothing`.
- `internal/controller`: `TestGitHubAppRerequestedNeedsPriorEventsSubscription`.
- `internal/controller`: `TestGitHubAppRerequestedNeedsTheBinding`.
- `internal/controller`: `TestGitHubAppRerequestedSpendsTheRunBudget`.
- `internal/controller`: `TestGitHubAppRerequestedUsesCurrentBranchFilter`.
- `internal/controller`: `TestGitHubAppRunLimitRefusesWithoutConsumingDelivery`.
- `internal/controller`: `TestGitHubAppRunNeedsTheRepositoryStillCovered`.
- `internal/controller`: `TestGitHubAppRunRecordsTheDefaultBranch`.
- `internal/controller`: `TestGitHubAppSignedBodyKeepsItsEvent`.
- `internal/controller`: `TestGitHubAppStaleCronPushStillStartsSubscribedPipeline`.
- `internal/controller`: `TestGitHubAppSubscriptionRejectsInvalidBranchPatterns`.
- `internal/controller`: `TestGitHubAppTagBooleanRejected`.
- `internal/controller`: `TestGitHubAppTagPatternsFilterPushes`.
- `internal/controller`: `TestGitHubAppTagPatternsValidateBounds`.
- `internal/controller`: `TestGitHubAppTagPushRequiresTagSubscription`.
- `internal/controller`: `TestGitHubAppUndatedEventIsIgnored`.
- `internal/controller`: `TestGitHubAppUninstallUnbinds`.
- `internal/controller`: `TestGitHubAppWebhookSignature`.
- `internal/controller`: `TestGitHubAppWithdrawnRepoReleasesCapacity`.
- `internal/controller`: `TestGitHubOnboardingCookieSessionUsesTeamRole`.
- `internal/controller`: `TestGitHubOnboardingCookieWritesRejectInvalidCSRF`.
- `internal/controller`: `TestGitHubRunnerCredentialClaimsOnlyItsOwnPush`.
- `internal/controller`: `TestGitHubRunnerCredentialClaimsOnlyItsRepository`.
- `internal/controller`: `TestGitHubRunnerCredentialReachesNothingOutsideItsRoutes`.
- `internal/controller`: `TestGitHubRunnerCredentialsPerTeamAreBounded`.
- `internal/controller`: `TestGitHubRunnerOnboardingRefusesUnverifiedIdentity`.
- `internal/controller`: `TestGitHubRunnerOnboardingResolvesCoveredIdentity`.
- `internal/controller`: `TestInvitationEmailNamesTheInviterAndTeamAndStopsAtTheDailyCap`.
- `internal/controller`: `TestInvitationWithoutAMailServiceSendsNothing`.
- `internal/controller`: `TestLegacyMultiTeamLicenseAdvertisesBilling`.
- `internal/controller`: `TestMetricsAddr_FailsStartupWhenTheMetricsPortIsTaken`.
- `internal/controller`: `TestMetricsAddr_MovesMetricsOffTheAPIListener`.
- `internal/controller`: `TestOperatorConsoleRejectsRetiredDollarOverrides`.
- `internal/controller`: `TestOperatorConsole_FindsATeamAndActsOnItWithAReason`.
- `internal/controller`: `TestOperatorConsole_KeepsARevocationAndDisputeHolds`.
- `internal/controller`: `TestOperatorConsole_ListsTheWaitlistNewestFirstAndApprovesByID`.
- `internal/controller`: `TestOperatorConsoleKeepsActionsPrivateAndPlacementReadMinimal`.
- `internal/controller`: `TestOperatorDeletesAnAccountByEmailAndATeamBySlug`.
- `internal/controller`: `TestReadHostKeyTakesTheKeyTheServerPresents`.
- `internal/controller`: `TestRefundReportingRejectsTeamOwnerAndRunner`.
- `internal/controller`: `TestRunSourceCredential_MintsTheApprovedRepositoriesByID`.
- `internal/controller`: `TestRunSourceCredential_NeedsTheGitHubApp`.
- `internal/controller`: `TestSignUpGateRoutesAreTheOperators`.
- `internal/controller`: `TestSignUpVelocityWarnsThenClosesTheGate`.
- `internal/controller`: `TestSourceCredential_AppTokenReadsOnlyTheOwnersExtraRepos`.
- `internal/controller`: `TestSourceCredential_AppTokenReadsTheVerifiedRepository`.
- `internal/controller`: `TestSourceCredential_NamesTheRemedy`.
- `internal/controller`: `TestSourceCredential_NeedsTheRunsOwnClaimToken`.
- `internal/controller`: `TestSourceCredential_TypedRepositoryNeedsASubscription`.
- `internal/controller`: `TestTeamBillingCheckout_OpensASessionForTheOwnersActiveTeam`.
- `internal/controller`: `TestTeamBillingCheckout_ReportsAnUnusableCheckoutService`.
- `internal/controller`: `TestTeamBilling_EveryMemberReadsTheActiveTeamsBilling`.
- `internal/controller`: `TestTeamBoundary_AClaimWhoseCredentialNamesNoTeamIsRefused`.
- `internal/controller`: `TestTeamBoundary_AFreshAccountCannotReachAnotherTeamsRun`.
- `internal/controller`: `TestTeamBoundary_ANodeCannotAdoptAnotherTeamsAttemptHistory`.
- `internal/controller`: `TestTeamBoundary_ARunnerCreatesItsRunInItsOwnTeam`.
- `internal/controller`: `TestTeamBoundary_ARunnerOfAnotherTeamFinishesTheRunItClaimed`.
- `internal/controller`: `TestTeamBoundary_ATriggerCannotRetryAnotherTeamsRun`.
- `internal/controller`: `TestTeamBoundary_AttemptsNeverFollowAPointerIntoAnotherTeam`.
- `internal/controller`: `TestTeamBoundary_ConcurrencyHolderCannotCrossTeams`.
- `internal/controller`: `TestTeamBoundary_EveryRunRouteAnswersAnotherTeamWith404`.
- `internal/controller`: `TestTeamBoundary_InvitationsGoWithTheirInvitersAuthority`.
- `internal/controller`: `TestTeamBoundary_ListsNeverIncludeAnotherTeam`.
- `internal/controller`: `TestTeamBoundary_NoMemberReachesAnAdminRoute`.
- `internal/controller`: `TestTeamBoundary_ResolveWaiterDoesNotReadAnotherTeamsLeader`.
- `internal/controller`: `TestTeamBoundary_RolesStayInsideTheirGrant`.
- `internal/controller`: `TestTeamBoundary_SecretsStayWithTheirTeam`.
- `internal/controller`: `TestTeamBoundary_TriggerAncestryStaysInTheCallersTeam`.
- `internal/controller`: `TestTeamGitCredentialGoesToLocalMachinesOnlyWhenOptedIn`.
- `internal/controller`: `TestTeamGitCredentialIsOwnerManagedAndWriteOnly`.
- `internal/controller`: `TestTeamGitCredentialLosesToTheAppWhenTheAppCovers`.
- `internal/controller`: `TestTeamGitCredentialNeedsASecretsKey`.
- `internal/controller`: `TestTeamGitCredentialRotationAndRevocation`.
- `internal/controller`: `TestTeamGitCredentialSSHIsUnusableUntilConfirmed`.
- `internal/controller`: `TestTeamGitCredentialStaysWithTheTeamClaimAndHost`.
- `internal/controller`: `TestTeamLogReadsAreNotRefusedPastTheDownloadCap`.
- `internal/controller`: `TestTeamOwnerCanChooseWhetherCloudTrustsLocalBinaries`.
- `internal/controller`: `TestTeamPurgeWaitsForDeletionDelay`.
- `internal/controller`: `TestTriggerAndCreateRunRejectAGitSHAThatIsNotAnObjectID`.
- `internal/controller`: `TestTriggerRecordsTheRunFlagsOnTheRunRow`.
- `internal/controller`: `TestTrigger_CreatesPendingRunRow`.
- `internal/controller`: `TestTrigger_CrossRepoAwait_DoesNotInheritParentSHA`.
- `internal/controller`: `TestTrigger_IgnoresAUserNamedInTheBody`.
- `internal/controller`: `TestTrigger_ParentRepoInheritance`.
- `internal/controller`: `TestTrigger_ParentRepoInheritance_RespectsExplicit`.
- `internal/controller`: `TestTrigger_RecordsARunForRunnersToClaim`.
- `internal/controller`: `TestTrigger_RefusesATriggerThatNamesTwoRepositories`.
- `internal/controller`: `TestTrigger_RequiresAGitHubRepositorySlug`.
- `internal/controller`: `TestTrigger_RequiresJSONContentType`.
- `internal/controller`: `TestTrigger_ReservesGitHubProvenanceForTheWebhook`.
- `internal/controller`: `TestTrigger_StripsClientSuppliedLeaseTokenEnv`.
- `internal/controller`: `TestTrigger_ValidatesRepoURL`.
- `internal/controller`: `TestWaitlistedSignUpThroughTheAPI`.
- `internal/controller`: `TestYoungGitHubAccountsAreWaitlistedAtSignIn`.
- `pkg/docs`: `TestWebClient_Versions_HappyPath`.

## Continuation 5

The seven GitHub reporting cases for foreign tenants, concurrent controllers,
stale terminal phases, timeout conclusions, lost check IDs, delivery failures and delayed queued
updates now run as recorder tests in github_completion_test.go. The retired
in-memory queue drain test is replaced by durable cancellation/retry coverage.
Those former names are removed from this socket inventory. No remaining
native test is skipped.

GitHub App webhook intake and re-request integration tests still need a native
run after the verified-App intake owner is connected. Their fake GitHub server
needs TCP. Full controller HTTP, CLI/daemon and PostgreSQL controller/acceptance
lanes also remain unverified here. TCP and Unix bind probes both return EPERM.
The new store GitHubCommitStatusMode tests and the existing ReportGitHubCheck
lock tests must also run on the lead's PostgreSQL store. The new controller
recorder fixtures use the existing SQLite engine fixture.

Continuation 6 ports the run-report lifecycle, conclusion table, permission fallback
and bounded/private summaries to recorder transport in github_reporting_test.go
and github_completion_test.go. Those groups no longer need sockets. Their
verified App request and initial queued intent are fixture seeds. Actual running
and terminal transitions, the outbox, report lock and GitHub adapter execute.
They do not establish signed-webhook intake or native listener behavior.

The paused alias and arbitrary node-creation success tests are deleted because
their surfaces are retired. TestPlanTopologyStrangerLearnsNothingProperty
replaces the latter route's stranger-token security assertion at the current
plan acceptance boundary. Those obligations no longer require a listener.

Continuation 7 ports public health, trends, current RunDetail, content types,
unknown fields, secret/variable envelope listing, private cache headers and
object-store/bucket metrics to recorders. The current safe RunDetail replaces
the retired include=nodes wrapper and raw execution decorations. Retired public
run/node writers and their validation expectations are removed.

The two queue-arrival rendering tests use real member authentication and the
declared ConcurrencyStore read seam backed by a tenant snapshot. Tenant still
lacks the production ConcurrencyStore adapter. These prove transport/rendering,
not native authorization revalidation or queue mutations.

The remaining two dashboard secret groups now use real owner/editor/reader
sessions, CSRF and bearer credentials with recorder transport. The retired
operator-bearer plaintext bypass becomes explicit deployment-credential refusal
under C2. Generated masked values remain absent from member reads, lists and
logs. Envelope transplantation and claim release tests still need their own
reconciliation/native run as listed above.

Continuation 7 also ports legacy-envelope/plaintext-row read refusal and
bound/unbound list metadata. Refused reads still require 500, sanitized errors,
no plaintext and unchanged stored bytes. The remaining encryption-roundtrip and
envelope relocation/access-field groups require declared-claim fixtures before
their old operator/plaintext-read assumptions can be reconciled.

Continuation 8 ports the three remaining envelope encryption groups and all
seven secret-argument groups to recorder transport. Masked release uses live
launcher-issued job claims with declared names and real transactional reads.
Only the accepted run/spec facts are fixture seeds. Encryption transplantation
properties include tenant, name, pipeline and both access flags, with correctly
resealed positive controls. Public argument properties cover every read route
and preserve persisted plaintext. None needs a native listener.

The two SQL literal regression groups now use real capability authentication
and tenant storage through recorders. Their SQLite lanes pass locally. Their
PostgreSQL lanes and the two new SQL property lanes still require the lead's
reachable database. PostgreSQL is not attempted where socket creation fails.
No transport, webhook intake or PostgreSQL result is implied by recorder passes.

Continuation 8 replaces raw plan snapshot body-size coverage with a real plan
claim and controller-election transaction over recorders, including exact-limit
acceptance and unchanged snapshots after live/retired-claim refusals. The two
retired source-token/git-credential aliases are covered by the unregistered-route
table. Their obsolete native listener fixture is deleted.

## W1b continuation 10

Six historical entries now run through recorders with current persisted authority: TestTeamGitCredentialSSHIsUnusableUntilConfirmed, TestTeamGitCredentialStaysWithTheTeamClaimAndHost, TestTeamGitCredentialGoesToLocalMachinesOnlyWhenOptedIn, TestTeamGitCredentialRotationAndRevocation, TestTeamGitCredentialIsOwnerManagedAndWriteOnly and TestTeamGitCredentialNeedsASecretsKey. Their native SSH/OAuth transport and PostgreSQL coverage remain separate. TestTeamGitCredentialLosesToTheAppWhenTheAppCovers remains open and enabled.
