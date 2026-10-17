# Socket-dependent reconciliation checks

These tests were attempted and hit forbidden TCP, UDP/DNS or Unix socket operations in the sandbox. They remain enabled. PostgreSQL verification also requires socket access and was not attempted against the lead's database. A panic stops a package run, so additional socket-dependent cases can remain beyond the names listed for packages other than pkg/controller.

- `bin`: `TestInstallToGreenBuildUsesPublicScaffoldAndCandidateSDK`.
- `bin`: `TestReleaseManifestCanBePublishedByDigestWithoutATag`.
- `cmd/sparkwing`: `TestResolveSparks_WithManifest_WritesOverlay`.
- `cmd/sparkwing-controller`: `TestConfigureSecrets_SingleTeamSQLiteCreatesItsKeyOnTheFirstSecret`.
- `internal/web`: `TestEventStreamsAreNeverEncoded`.
- `internal/wingd`: `TestAPIListenerClosesBeforeTheDrainIsAcknowledged`.
- `internal/wingd`: `TestAPISocketRefusesAPeerFromAnotherAccount`.
- `internal/wingd`: `TestAPISocketSitsBesideAdmissionAtMode0600`.
- `internal/wingd`: `TestTypeSafeJevAdvisorUsesTypedChoice`.
- `pkg/controller`: `ExampleNew`.
- `pkg/controller`: `TestAClosedFreeTierWaitlistsNewAccounts`.
- `pkg/controller`: `TestAdminTokenReachesNoOperatorPower`.
- `pkg/controller`: `TestAnUnreadableFreeTierWaitlistsNewAccounts`.
- `pkg/controller`: `TestAnUnwiredFreeTierLogsOnceAtStartup`.
- `pkg/controller`: `TestApprovingTheOldestRunsTheNotifier`.
- `pkg/controller`: `TestBillingTrust_OperatorGrantsAndRevokes`.
- `pkg/controller`: `TestCardBilling_OwnerSetsABudget`.
- `pkg/controller`: `TestClaimRun_ChildRunsInheritTheParentAndAnswerOnlyIt`.
- `pkg/controller`: `TestClaimRun_PlanRefusedWhenARequiredSecretIsMissing`.
- `pkg/controller`: `TestComputeLimits_NodeCreationRefusedPastThePerRunCap`.
- `pkg/controller`: `TestComputeLimits_RefusesBadSiblingWithoutChangingGuards`.
- `pkg/controller`: `TestComputeLimits_RefusesExplicitNullWithoutChangingGuards`.
- `pkg/controller`: `TestComputeLimits_RetriesCountAgainstTheRetryingPrincipal`.
- `pkg/controller`: `TestComputeLimits_RunCreationRefusedPastTheHourlyCap`.
- `pkg/controller`: `TestComputeLimits_ShowAndSet`.
- `pkg/controller`: `TestComputeLimits_TriggeredRunsCountAgainstTheTriggeringPrincipal`.
- `pkg/controller`: `TestControllerFixturesKeepCredentialsAndRunsSeparate`.
- `pkg/controller`: `TestCreditSettings_ReadNeedsRunsReadAndWriteNeedsAdmin`.
- `pkg/controller`: `TestCreditSettings_ReadsTheDefaultRateLadder`.
- `pkg/controller`: `TestCreditSettings_RefusesARateTableTheLedgerCannotPrice`.
- `pkg/controller`: `TestCreditSettings_RefusesTheScalarOnceATableExists`.
- `pkg/controller`: `TestCreditSettings_TakesTheRateTableAsAListOrAsAnObject`.
- `pkg/controller`: `TestCredits_AClaimAboveTheLargestClassFailsTheNode`.
- `pkg/controller`: `TestCredits_AClaimsOwnCPUFigureDoesNotLowerTheBill`.
- `pkg/controller`: `TestCredits_ANamedClaimThatDoesNotSizeToClassIsRefused`.
- `pkg/controller`: `TestCredits_AWarmClaimPassesOverAClassAboveTheWarmPool`.
- `pkg/controller`: `TestCredits_QuarterCorePinReservesTwoCoreMinimum`.
- `pkg/controller`: `TestCredits_RoutesShowGrantAndHistory`.
- `pkg/controller`: `TestCycleDetect_AllowsIndirectNonCycle`.
- `pkg/controller`: `TestCycleDetect_ParentNotFound400`.
- `pkg/controller`: `TestCycleDetect_RejectsDeepCycle`.
- `pkg/controller`: `TestCycleDetect_RejectsSelfCycle`.
- `pkg/controller`: `TestDashboardReadsStayInsideTheTeamBoundary`.
- `pkg/controller`: `TestDashboardStreamsOutliveTheListenersWriteTimeout`.
- `pkg/controller`: `TestDeleteAccountNeedsARecentSignIn`.
- `pkg/controller`: `TestDeleteAccountOwningTheDefaultTeamIsABadRequest`.
- `pkg/controller`: `TestDeleteTeamClosesItAtOnceAndThePassRemovesItsRows`.
- `pkg/controller`: `TestDeleteTeamRefusesAnOpenCheckoutOrADisputeHold`.
- `pkg/controller`: `TestDeleteTeamRefusesTheOnlyTeam`.
- `pkg/controller`: `TestFloodPolicy_ATeamsTokensShareOneCap`.
- `pkg/controller`: `TestFloodPolicy_CapsRunsPerPrincipalHour`.
- `pkg/controller`: `TestFloodPolicy_DedupeIsScopedToThePrincipal`.
- `pkg/controller`: `TestFloodPolicy_DedupesIdenticalSubmissionsInsideTheWindow`.
- `pkg/controller`: `TestFloodPolicy_ShedsAboveTheQueueDepthThreshold`.
- `pkg/controller`: `TestFloodPolicy_ShedsOnlyTheTeamWhoseQueueIsDeep`.
- `pkg/controller`: `TestFloodPolicy_UnsetCapAdmitsEverySubmission`.
- `pkg/controller`: `TestFloodPolicy_UnsetWindowDedupesNothing`.
- `pkg/controller`: `TestGitHubAppAdditionalEventsRequireSubscription`.
- `pkg/controller`: `TestGitHubAppAutomationConfigErrorsAndManualOverride`.
- `pkg/controller`: `TestGitHubAppAutomationFailsClosedAndRemovalRevokesConsent`.
- `pkg/controller`: `TestGitHubAppAutomationRequiresConsentAndUsesDefaultBranch`.
- `pkg/controller`: `TestGitHubAppAutomationRevokedDuringDiscoveryStartsNoRun`.
- `pkg/controller`: `TestGitHubAppBranchDeleteRunsOnGitHubsPayload`.
- `pkg/controller`: `TestGitHubAppBranchFilterLeavesTagPatternsAlone`.
- `pkg/controller`: `TestGitHubAppBranchFiltersGateBranchAndPullRequestEvents`.
- `pkg/controller`: `TestGitHubAppCheckRunRerequestedRerunsItsPipeline`.
- `pkg/controller`: `TestGitHubAppCheckSuiteRerequestedNeedsEachPipelinesOwnRun`.
- `pkg/controller`: `TestGitHubAppCheckSuiteRerequestedRerunsEveryPipeline`.
- `pkg/controller`: `TestGitHubAppConnectBindsAnInstallationItsOrgAdminProves`.
- `pkg/controller`: `TestGitHubAppConnectNeedsTheAccountsAdmin`.
- `pkg/controller`: `TestGitHubAppConnectRefusesForgedReplayedAndForeignState`.
- `pkg/controller`: `TestGitHubAppConnectStateIsSingleUseAcrossReplicas`.
- `pkg/controller`: `TestGitHubAppCronCannotProveManualGitHubURLDistinct`.
- `pkg/controller`: `TestGitHubAppCronConflictsWithManualControllerSchedule`.
- `pkg/controller`: `TestGitHubAppCronFailureDoesNotSuppressSubscribedPush`.
- `pkg/controller`: `TestGitHubAppCronLifecycleWithdrawsOldBindings`.
- `pkg/controller`: `TestGitHubAppCronPushGuardsPreserveExistingSchedules`.
- `pkg/controller`: `TestGitHubAppCronRefusesManualScheduleAtOldRedirectedURL`.
- `pkg/controller`: `TestGitHubAppCronRenameConflictStopsOldAppRow`.
- `pkg/controller`: `TestGitHubAppCronRenameKeepsIdentityAndPauseAtRepoCap`.
- `pkg/controller`: `TestGitHubAppCronTransferWithdrawsFormerTeamBeforeNewArm`.
- `pkg/controller`: `TestGitHubAppDefaultPushArmsAndWithdrawsControllerSchedules`.
- `pkg/controller`: `TestGitHubAppEventOlderThanTheReplayWindowIsRefused`.
- `pkg/controller`: `TestGitHubAppExistingInstallationPicker`.
- `pkg/controller`: `TestGitHubAppExistingPickerHidesVisibleInstallationWithoutAdminMembership`.
- `pkg/controller`: `TestGitHubAppExistingPickerOnlyBindsListedInstallations`.
- `pkg/controller`: `TestGitHubAppExistingPickerRefusesForeignIdentityAndBoundInstallation`.
- `pkg/controller`: `TestGitHubAppFloodCapCountsTheTeamsRuns`.
- `pkg/controller`: `TestGitHubAppForkPullRequestIsNotRun`.
- `pkg/controller`: `TestGitHubAppInstallationBelongsToOneTeam`.
- `pkg/controller`: `TestGitHubAppInstallationEventNamesThisApp`.
- `pkg/controller`: `TestGitHubAppLargeInstallationKeepsSchedulesPastTheListingCap`.
- `pkg/controller`: `TestGitHubAppLateRemovalKeepsARepositoryTheCutListingShows`.
- `pkg/controller`: `TestGitHubAppPartialReleaseRedeliveryKeepsResolvedCommit`.
- `pkg/controller`: `TestGitHubAppPullRequestBaseBranchSubscription`.
- `pkg/controller`: `TestGitHubAppPushBranchSubscription`.
- `pkg/controller`: `TestGitHubAppPushOfNoCommitIsIgnored`.
- `pkg/controller`: `TestGitHubAppPushRunsOnlyInTheInstallationsTeam`.
- `pkg/controller`: `TestGitHubAppRedeliveryChargesOnlyShedRuns`.
- `pkg/controller`: `TestGitHubAppRedeliveryStartsOneRun`.
- `pkg/controller`: `TestGitHubAppReleaseReplayAndCoverage`.
- `pkg/controller`: `TestGitHubAppReplayAfterAMoveStartsNothing`.
- `pkg/controller`: `TestGitHubAppRepositoryIdentitySurvivesRenameAndTransfer`.
- `pkg/controller`: `TestGitHubAppRerequestedForkOrForeignCommitStartsNothing`.
- `pkg/controller`: `TestGitHubAppRerequestedNeedsPriorEventsSubscription`.
- `pkg/controller`: `TestGitHubAppRerequestedNeedsTheBinding`.
- `pkg/controller`: `TestGitHubAppRerequestedSpendsTheRunBudget`.
- `pkg/controller`: `TestGitHubAppRerequestedUsesCurrentBranchFilter`.
- `pkg/controller`: `TestGitHubAppRunLimitRefusesWithoutConsumingDelivery`.
- `pkg/controller`: `TestGitHubAppRunNeedsTheRepositoryStillCovered`.
- `pkg/controller`: `TestGitHubAppRunRecordsTheDefaultBranch`.
- `pkg/controller`: `TestGitHubAppSignedBodyKeepsItsEvent`.
- `pkg/controller`: `TestGitHubAppStaleCronPushStillStartsSubscribedPipeline`.
- `pkg/controller`: `TestGitHubAppSubscriptionRejectsInvalidBranchPatterns`.
- `pkg/controller`: `TestGitHubAppTagBooleanRejected`.
- `pkg/controller`: `TestGitHubAppTagPatternsFilterPushes`.
- `pkg/controller`: `TestGitHubAppTagPatternsValidateBounds`.
- `pkg/controller`: `TestGitHubAppTagPushRequiresTagSubscription`.
- `pkg/controller`: `TestGitHubAppUndatedEventIsIgnored`.
- `pkg/controller`: `TestGitHubAppUninstallUnbinds`.
- `pkg/controller`: `TestGitHubAppWebhookSignature`.
- `pkg/controller`: `TestGitHubAppWithdrawnRepoReleasesCapacity`.
- `pkg/controller`: `TestGitHubOnboardingCookieSessionUsesTeamRole`.
- `pkg/controller`: `TestGitHubOnboardingCookieWritesRejectInvalidCSRF`.
- `pkg/controller`: `TestGitHubRunnerCredentialClaimsOnlyItsOwnPush`.
- `pkg/controller`: `TestGitHubRunnerCredentialClaimsOnlyItsRepository`.
- `pkg/controller`: `TestGitHubRunnerCredentialReachesNothingOutsideItsRoutes`.
- `pkg/controller`: `TestGitHubRunnerCredentialsPerTeamAreBounded`.
- `pkg/controller`: `TestGitHubRunnerOnboardingRefusesUnverifiedIdentity`.
- `pkg/controller`: `TestGitHubRunnerOnboardingResolvesCoveredIdentity`.
- `pkg/controller`: `TestInvitationEmailNamesTheInviterAndTeamAndStopsAtTheDailyCap`.
- `pkg/controller`: `TestInvitationWithoutAMailServiceSendsNothing`.
- `pkg/controller`: `TestLegacyMultiTeamLicenseAdvertisesBilling`.
- `pkg/controller`: `TestMetricsAddr_FailsStartupWhenTheMetricsPortIsTaken`.
- `pkg/controller`: `TestMetricsAddr_MovesMetricsOffTheAPIListener`.
- `pkg/controller`: `TestOperatorConsoleRejectsRetiredDollarOverrides`.
- `pkg/controller`: `TestOperatorConsole_FindsATeamAndActsOnItWithAReason`.
- `pkg/controller`: `TestOperatorConsole_KeepsARevocationAndDisputeHolds`.
- `pkg/controller`: `TestOperatorConsole_ListsTheWaitlistNewestFirstAndApprovesByID`.
- `pkg/controller`: `TestOperatorConsoleKeepsActionsPrivateAndPlacementReadMinimal`.
- `pkg/controller`: `TestOperatorDeletesAnAccountByEmailAndATeamBySlug`.
- `pkg/controller`: `TestReadHostKeyTakesTheKeyTheServerPresents`.
- `pkg/controller`: `TestRefundReportingRejectsTeamOwnerAndRunner`.
- `pkg/controller`: `TestRunSourceCredential_MintsTheApprovedRepositoriesByID`.
- `pkg/controller`: `TestRunSourceCredential_NeedsTheGitHubApp`.
- `pkg/controller`: `TestSignUpGateRoutesAreTheOperators`.
- `pkg/controller`: `TestSignUpVelocityWarnsThenClosesTheGate`.
- `pkg/controller`: `TestSourceCredential_AppTokenReadsOnlyTheOwnersExtraRepos`.
- `pkg/controller`: `TestSourceCredential_AppTokenReadsTheVerifiedRepository`.
- `pkg/controller`: `TestSourceCredential_NamesTheRemedy`.
- `pkg/controller`: `TestSourceCredential_NeedsTheRunsOwnClaimToken`.
- `pkg/controller`: `TestSourceCredential_TypedRepositoryNeedsASubscription`.
- `pkg/controller`: `TestTeamBillingCheckout_OpensASessionForTheOwnersActiveTeam`.
- `pkg/controller`: `TestTeamBillingCheckout_ReportsAnUnusableCheckoutService`.
- `pkg/controller`: `TestTeamBilling_EveryMemberReadsTheActiveTeamsBilling`.
- `pkg/controller`: `TestTeamBoundary_AClaimWhoseCredentialNamesNoTeamIsRefused`.
- `pkg/controller`: `TestTeamBoundary_AFreshAccountCannotReachAnotherTeamsRun`.
- `pkg/controller`: `TestTeamBoundary_ANodeCannotAdoptAnotherTeamsAttemptHistory`.
- `pkg/controller`: `TestTeamBoundary_ARunnerCreatesItsRunInItsOwnTeam`.
- `pkg/controller`: `TestTeamBoundary_ARunnerOfAnotherTeamFinishesTheRunItClaimed`.
- `pkg/controller`: `TestTeamBoundary_ATriggerCannotRetryAnotherTeamsRun`.
- `pkg/controller`: `TestTeamBoundary_AttemptsNeverFollowAPointerIntoAnotherTeam`.
- `pkg/controller`: `TestTeamBoundary_ConcurrencyHolderCannotCrossTeams`.
- `pkg/controller`: `TestTeamBoundary_EveryRunRouteAnswersAnotherTeamWith404`.
- `pkg/controller`: `TestTeamBoundary_InvitationsGoWithTheirInvitersAuthority`.
- `pkg/controller`: `TestTeamBoundary_ListsNeverIncludeAnotherTeam`.
- `pkg/controller`: `TestTeamBoundary_NoMemberReachesAnAdminRoute`.
- `pkg/controller`: `TestTeamBoundary_ResolveWaiterDoesNotReadAnotherTeamsLeader`.
- `pkg/controller`: `TestTeamBoundary_RolesStayInsideTheirGrant`.
- `pkg/controller`: `TestTeamBoundary_SecretsStayWithTheirTeam`.
- `pkg/controller`: `TestTeamBoundary_TriggerAncestryStaysInTheCallersTeam`.
- `pkg/controller`: `TestTeamGitCredentialGoesToLocalMachinesOnlyWhenOptedIn`.
- `pkg/controller`: `TestTeamGitCredentialIsOwnerManagedAndWriteOnly`.
- `pkg/controller`: `TestTeamGitCredentialLosesToTheAppWhenTheAppCovers`.
- `pkg/controller`: `TestTeamGitCredentialNeedsASecretsKey`.
- `pkg/controller`: `TestTeamGitCredentialRotationAndRevocation`.
- `pkg/controller`: `TestTeamGitCredentialSSHIsUnusableUntilConfirmed`.
- `pkg/controller`: `TestTeamGitCredentialStaysWithTheTeamClaimAndHost`.
- `pkg/controller`: `TestTeamLogReadsAreNotRefusedPastTheDownloadCap`.
- `pkg/controller`: `TestTeamOwnerCanChooseWhetherCloudTrustsLocalBinaries`.
- `pkg/controller`: `TestTeamPurgeWaitsForDeletionDelay`.
- `pkg/controller`: `TestTriggerAndCreateRunRejectAGitSHAThatIsNotAnObjectID`.
- `pkg/controller`: `TestTriggerRecordsTheRunFlagsOnTheRunRow`.
- `pkg/controller`: `TestTrigger_CreatesPendingRunRow`.
- `pkg/controller`: `TestTrigger_CrossRepoAwait_DoesNotInheritParentSHA`.
- `pkg/controller`: `TestTrigger_IgnoresAUserNamedInTheBody`.
- `pkg/controller`: `TestTrigger_ParentRepoInheritance`.
- `pkg/controller`: `TestTrigger_ParentRepoInheritance_RespectsExplicit`.
- `pkg/controller`: `TestTrigger_RecordsARunForRunnersToClaim`.
- `pkg/controller`: `TestTrigger_RefusesATriggerThatNamesTwoRepositories`.
- `pkg/controller`: `TestTrigger_RequiresAGitHubRepositorySlug`.
- `pkg/controller`: `TestTrigger_RequiresJSONContentType`.
- `pkg/controller`: `TestTrigger_ReservesGitHubProvenanceForTheWebhook`.
- `pkg/controller`: `TestTrigger_StripsClientSuppliedLeaseTokenEnv`.
- `pkg/controller`: `TestTrigger_ValidatesRepoURL`.
- `pkg/controller`: `TestWaitlistedSignUpThroughTheAPI`.
- `pkg/controller`: `TestYoungGitHubAccountsAreWaitlistedAtSignIn`.
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
