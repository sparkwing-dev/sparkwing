# W9 raw-client deletion inventory

Base: `caa62edf0`. The lead owns commits. This inventory records removals, not completed acceptance ports.

## Production files

| Path | Added | Removed |
| --- | ---: | ---: |
| `pkg/controller/client/client.go` | 0 | 1308 |
| `pkg/controller/client/concurrency.go` | 0 | 398 |
| `pkg/controller/client/coordination.go` | 0 | 212 |
| `pkg/controller/client/engine.go` | 2 | 2 |
| `pkg/controller/client/livelogs.go` | 0 | 36 |
| `pkg/controller/client/logsegments.go` | 0 | 262 |
| `pkg/controller/client/oidc.go` | 0 | 27 |
| `pkg/controller/client/output.go` | 0 | 138 |
| `pkg/controller/client/pace.go` | 0 | 75 |
| `pkg/controller/client/poll_advice.go` | 0 | 174 |
| `pkg/controller/client/retry.go` | 2 | 154 |
| `pkg/controller/client/secrets.go` | 0 | 91 |

The raw client had no production caller outside its own package. No production caller migration or foreign production edit was needed. Shared-controller fixture branches that used the removed API are retired below. The store-only branch of `internal/nodehost/events_test.go` remains.

## Removed test entrypoints

Source functions remain available in the base revision. These tests used retired raw routes, request bodies, token tables or host adapters. They do not count as ports to final authority or native execution.

| Path | Removed entrypoints |
| --- | --- |
| `pkg/controller/agents_test.go` | `TestAgents_HeadroomIncludesControllerObservationTime` |
| `pkg/controller/approvals_test.go` | `TestApprovals_RequestThenApprove`, `TestApprovals_ResolveTwiceIsConflict`, `TestApprovals_ResolveMissingIs404`, `TestApprovals_ListPendingAndForRun` |
| `pkg/controller/auth_claim_test.go` | `TestNodeClaim_AuthBlocksUnauthedCaller`, `TestNodeClaim_ARevokedTokenReachesTheClientAsDead` |
| `pkg/controller/claim_filter_test.go` | `TestClaim_PipelineFilter_BasicInclude`, `TestClaim_PipelineFilter_Empty204`, `TestClaim_PipelineFilter_NilMeansAll`, `TestClaim_SourceFilter_GithubWorkerSkipsManual`, `TestClaim_SourceFilter_WorkerClaimsMatchingSource`, `TestClaim_SourceFilter_AndWithPipeline` |
| `pkg/controller/claim_repo_allowlist_test.go` | `TestClaimsCarryTheRunnersRepositoryList`, `TestClaimsRefuseABadRepositoryPattern`, `TestPlacement_CloudIsNotHeldForALocalRunnerThatRefusesTheRepository` |
| `pkg/controller/claim_run_test.go` | `TestClaimRun_InputsFromAnotherRunFollowTheAcceptedPlan` |
| `pkg/controller/claim_test.go` | `TestClaim_TriggerPersistsThenClaims` |
| `pkg/controller/client/allow_repos_test.go` | `TestAllowReposIsSentOnlyToAControllerThatAdvertisesIt` |
| `pkg/controller/client/claim_profile_test.go` | `TestClaimsAdvertiseThePlatformWithoutConfiguration` |
| `pkg/controller/client/concurrency_test.go` | `TestObserveSlotMapsMissingHolder`, `TestHeartbeatSlotMapsLostOwnership` |
| `pkg/controller/client/coordination_test.go` | `TestClientTriggerLoopRoutesMatchTheStore`, `TestClientCapacityRoutesLandInTheSameRows`, `TestClientCapacityRoutesRefuseAnotherPipelineAndAbsurdInput`, `TestClientNodeUsageAndMetricsRoundTrip`, `TestClientMetricKindsRoundTrip` |
| `pkg/controller/client/error_test.go` | `TestReadHTTPErrorMapsMaskedNotImplementedToStorageSentinel` |
| `pkg/controller/client/foreign_notfound_test.go` | `TestEveryHelperRefusesA404NoControllerWrote` |
| `pkg/controller/client/jobs_read_test.go` | `TestJobStatusUsesFinalRoutes`, `TestJobStatusUsesCanonicalErrors`, `TestJobStatusRetainsRetryAdvice` |
| `pkg/controller/client/logsegments_subject_test.go` | `TestLogUploaderSealsOnlyItsGrantedSubject`, `TestLogUploaderRejectsChangedGrantTeam` |
| `pkg/controller/client/node_protocol_test.go` | `TestNodeRequestsNameTheNodeProtocolVersion` |
| `pkg/controller/client/output_transport_test.go` | `TestSignedOutputUploadUsesControllerSocketOnlyForSameOrigin`, `TestSignedOutputTransportRequiresControllerSchemeAndPort` |
| `pkg/controller/client/pace_test.go` | `TestClassifyHTTPError_ReadsTheTokenState`, `TestTokenPrefix_NeverPrintsTheSecret` |
| `pkg/controller/client/poll_advice_test.go` | `TestPollAdvice_ReadsTheControllerSuggestionOffAClaim`, `TestPollAdvice_CapsAnImplausibleSuggestion`, `TestLoadSignal_ReadsA429AsBackpressure` |
| `pkg/controller/client/profile_partial_test.go` | `TestRecordProfileObservationSendsPartialFlag` |
| `pkg/controller/client/redirect_auth_test.go` | `TestControllerBearerUsesStandardRedirectBoundary`, `TestControllerUnauthorizedResponseIsNotRetried`, `TestControllerAuthPreservesCustomClientAndRange` |
| `pkg/controller/client/retry_test.go` | `TestClientRetriesAnUnavailableReadThatNamesRetryAfter`, `TestClientDoesNotRetryAnUnavailablePost`, `TestExecutionStartRetriesTransientAnswers`, `TestExecutionStartStopsAfterRetryBudget`, `TestExecutionStartRetriesTransientTransportError`, `TestClientRetriesOnlyRepeatableMethods`, `TestClientDoesNotRetryAnUnavailableAnswerWithNoRetryAfter`, `TestClientStopsRetryingAtTheBound`, `TestClaimShedByTheBudgetIsAnUnavailableError`, `TestUnavailableWithoutRetryAfterStaysAPlainError`, `TestListNodesHonorsPollAfterOn429` |
| `pkg/controller/client/runner_identity_test.go` | `TestRunnerIdentity_EveryClaimMethodNamesItsRunner`, `TestRunnerIdentity_IsSafeToSetWhileTheClientServes` |
| `pkg/controller/client/secret_refusal_test.go` | `TestGetSecretForRunCarriesTheRefusalMessage` |
| `pkg/controller/client/unsupported_test.go` | `TestClientRecognisesAnUnsupportedRoute`, `TestEveryHelperReportsAnUnsupportedRoute`, `TestAPlainNotFoundKeepsItsMeaning` |
| `pkg/controller/compute_limits_test.go` | `TestComputeLimits_ClaimRefusedByTheRunnerGuard`, `TestComputeLimits_HeartbeatCancelsANodePastTheWallClockGuard`, `TestComputeLimits_RefusalStaysInsideTheRefusedPrincipal`, `TestComputeLimits_GlobalRunnerGuardRefusesAnUnownedClaim` |
| `pkg/controller/credits_team_test.go` | `TestCreditRefusalLandsOnTheRefusedTeamsRun` |
| `pkg/controller/credits_test.go` | `TestCredits_UnmeteredTokenClaimsAndIsNeverCharged`, `TestCredits_MeteredClaimRefusedOnAnEmptyBalance`, `TestCredits_MeteredTriggerClaimRefusedOnAnEmptyBalance`, `TestCredits_TriggerHeartbeatLedgerFailureRefusesRenewal`, `TestCredits_UnsettledTriggerClaimIsActionableWithoutBlockingQueue`, `TestCredits_MeteredTriggerClaimRefusesInProcessNodes`, `TestCredits_MeteredClaimChargesEachHeartbeat`, `TestCredits_MeteredHeartbeatRollsBackWhenLedgerFails`, `TestCredits_MeteredHeartbeatDoesNotReviveClaimExpiredDuringCharge`, `TestCredits_HeartbeatCancelsTheNodeAfterTheGracePeriod`, `TestCredits_NodeFinishSettlesAShortNodeAtTheMinimum`, `TestCredits_HistoryNamesTheClassAndRateEachChargeWasBilledAt` |
| `pkg/controller/declared_repo_grants_nothing_test.go` | `TestDeclaredRepo_TypingAVictimRepositoryReadsNoSecretOfIts`, `TestDeclaredRepo_ForgedTriggerEnvReadsNoCachedSource`, `TestDeclaredRepo_AWebhookRunStillResolvesItsOwnSecrets` |
| `pkg/controller/finish_run_retry_test.go` | `TestFinishRunRetriesDoNotRepeatProfileObservations` |
| `pkg/controller/git_credentials_team_test.go` | `TestTeamGitCredentialReachesOnlyARunnersClaim` |
| `pkg/controller/livelogs_test.go` | `TestLiveLog_AppendNeedsTheNodesOwnClaim`, `TestLiveLog_ReadIsNotFoundForANodeThatNeverWrote`, `TestLiveLog_ReaderSeesLinesAppendedAfterItConnected`, `TestLiveLog_FinishingTheNodeEndsTheStream`, `TestLiveLog_AppendRefusesANodeTheRunDoesNotHave`, `TestLiveLog_ReaderIsToldWhenTheBufferDroppedBytesItHadNotRead` |
| `pkg/controller/log_segments_internal_test.go` | `TestLogSegmentsRoundTripThroughTheController`, `TestLogSegmentsRefuseASupersededAttemptAndASecondManifest`, `TestSegmentStreamJoinsTheLiveRingByOffset`, `TestNodeLogReadsThroughTheControllerRoutes`, `TestAnEndedClaimFinishesOnlyItsOwnLogWithinTheGrace` |
| `pkg/controller/metering_license_test.go` | `TestUnlicensedControllerHasNoMetering` |
| `pkg/controller/node_claim_named_test.go` | `TestClaimNodeByID_AwardsTheNamedNodeWithItsFence`, `TestClaimNodeByID_RefusesANodeAnotherHolderHas`, `TestClaimNodeByID_ReportsANodeThatDoesNotExist`, `TestClaimNodeByID_NeedsTheClaimScope`, `TestClaimNodeByID_MeteredTokenReservesAndSettles`, `TestClaimNodeByID_FinishBeforeExecutionRefundsTheReservation`, `TestClaimNodeByID_RefusesAnUnreadyNodeWithoutTheRunsDispatchClaim`, `TestClaimNodeByID_AwardsAnUnreadyNodeToTheRunsDispatcher`, `TestClaimNodeByID_RefusesANodeOfAFinishedRun`, `TestClaimNodeByID_RefusesALabelledNodeWithoutTheRunsDispatchClaim`, `TestClaimNodeByID_AwardsALabelledNodeToTheRunsDispatcher` |
| `pkg/controller/node_claim_ownership_test.go` | `TestNodeClaimOwnership_StrangerTokenCannotWriteAnotherRunnersNode`, `TestNodeClaimOwnership_ReadinessRoutesRefuseANodeClaim`, `TestNodeClaimOwnership_HeartbeatRefusesAnotherPrincipalsHolderID`, `TestNodeClaimOwnership_ExpiredClaimStopsAdmittingWrites`, `TestNodeClaimOwnership_TerminalEventKeepsExactAttribution`, `TestTriggerClaimOwnership_RequiresExactLiveRunBoundGeneration`, `TestNodeClaimOwnership_ServerCapsTheRequestedLease`, `TestNodeClaimOwnership_TwoTokensSharingAPrincipalNameDoNotCrossAuthorize`, `TestNodeClaimOwnership_CrossRunReadsAndHeartbeatNeedAClaim`, `TestNodeReadResponsesHideClaimHolderAndReservations`, `TestNodeClaimOwnership_ProfilePinIsADispatcherWrite` |
| `pkg/controller/node_claim_test.go` | `TestNodeClaim_HTTPRoundTrip`, `TestNodeClaim_ExecutionStartAcknowledgement`, `TestNodeClaim_HTTPLabelFiltering` |
| `pkg/controller/node_claim_token_log_segments_internal_test.go` | `TestNodeClaimTokenUploadsItsNodesLogSegmentsUntilTheClaimEnds` |
| `pkg/controller/node_client_contract_test.go` | `TestNodeClientContract_EveryRouteTheNodeClientCalls` |
| `pkg/controller/node_placement_test.go` | `TestPlacement_CloudRunnerWaitsOutTheHoldWhileALocalRunnerPolls`, `TestPlacement_CloudRunnerTakesTheNodeAfterTheHold`, `TestPlacement_SaturatedLocalRunnerHoldsNothing`, `TestPlacement_NodeWithoutAPreferenceIsUnchanged`, `TestPlacement_ControllerDefaultPrefersHoldsANodeWithoutPrefers`, `TestPlacement_LivenessWindowForgetsASilentRunner`, `TestPlacement_AgentsViewCarriesSelfAssertedRunnerLabels`, `TestPlacement_ARunnerThatNeverClaimedHoldsNothing`, `TestPlacement_RunnerAdvertisingNoCapacityHoldsNothing`, `TestPlacement_PresenceIsBoundToTheClaimingCredential`, `TestPlacement_RunnerAtItsCeilingHoldsNothingBack` |
| `pkg/controller/oidc_token_test.go` | `TestOIDCTokenVerifiesAndNamesTheClaimedRun`, `TestOIDCSubjectNamesTheRepository`, `TestOIDCTokenTriggerComesFromTheIntakeRow`, `TestOIDCTokenRefusesAnyoneButTheClaimHolder`, `TestOIDCTokenValidatesTheRequestAndTheSubject`, `TestOIDCTokenIsRateLimitedPerClaim`, `TestOIDCRoutesWithAndWithoutAKey`, `TestOIDCKeyRotation`, `TestOIDCTokenExpiryAndTampering`, `TestOIDCTokenPullRequestNeverLooksLikeAPush`, `TestOIDCTokenTagPushNamesTagRef`, `TestOIDCTokenAnswersANodeClaimTokenForItsOwnRunOnly` |
| `pkg/controller/pipeline_latest_test.go` | `TestPipelineLatest_HTTPRoundTrip`, `TestPipelineLatest_NotFound404`, `TestPipelineLatest_MaxAgeFilter` |
| `pkg/controller/profiles_completion_test.go` | `TestFinishRunExcludesIncompleteAndRetriedNodes` |
| `pkg/controller/profiles_test.go` | `TestFinishRun_FoldsProfilesAndEmitsPinDrift`, `TestFinishRun_NoPinNoDrift` |
| `pkg/controller/reaped_run_conflict_test.go` | `TestCreateNode_ReapedRunAnswersConflictWithReason` |
| `pkg/controller/run_limits_test.go` | `TestAnnotationAndMetricBoundsAnswerWithTheirStatus`, `TestNodeMetricReadsArePaged`, `TestClientNamesTheMetricCapRefusal` |
| `pkg/controller/runner_claim_cross_run_test.go` | `TestRunnerClaim_NodeTokenReadsItsTeamsPipelineRefs`, `TestRunnerClaim_NodeTokenStartsAndAwaitsItsOwnChildRuns` |
| `pkg/controller/runner_claim_test.go` | `TestRunnerClaim_NodeTokenServesItsNodeUntilTheClaimEnds`, `TestRunnerClaim_RevokingTheRunnerStopsItsClaimTokens`, `TestRunnerClaim_RevokingTheRunnerStopsItsTriggerToken`, `TestRunnerClaim_TriggerTokenReadsDeclaredSecretsWhileItsClaimLives`, `TestSecrets_ABearerReadsByNameOnlyAsOperatorOrTeamAdmin` |
| `pkg/controller/runner_scopes_test.go` | `TestRunnerScopes_DocumentedSetCompletesARunWithoutAdmin`, `TestRunnerScopes_TriggerHolderControlsReadiness`, `TestRunnerScopes_ConcurrencySlotFollowsTheRunClaim`, `TestTriggerClaimMutation_RequiresExactTokenAndGeneration`, `TestRunnerScopes_TriggerOwnedExecutionAttemptRoundTrips`, `TestRunMutationFence_RechecksAfterRequestBodyUnblocks`, `TestRunnerScopes_PoolRunnerReadsItsPipelineSecret`, `TestRunnerScopes_SecretsReadIsNotAdmin`, `TestRunnerScopes_RunRepositoryComesFromTheTrigger`, `TestRunnerScopes_FinishRunKeepsTheFirstTerminalVerdict`, `TestRunnerScopes_SecretReadNamesItsRun`, `TestRunnerScopes_UnscopedSecretNeedsShared`, `TestRunnerScopes_StateWritesStillNeedTheNodeClaim`, `TestRunnerScopes_RunAndTriggerReadsStillNeedTheClaim`, `TestRunnerScopes_ProfilePinNeedsAClaimOnThatPipeline`, `TestRunnerScopes_ClaimScopedReadsExpireAndKeepSecretsNodeOnly`, `TestRunnerScopes_CoordinationRoutesRunOnATriggerClaim`, `TestRunnerScopes_CoordinationWritesNeedAClaimOnThatPipeline`, `TestRunnerScopes_ProfileWritesAreScopedToTheClaimedPipeline`, `TestRunnerScopes_PinRefusesAnUnboundedFigure`, `TestRunnerScopes_AdminRunnerAcknowledgesATriggerOwnedAttempt` |
| `pkg/controller/secrets_team_test.go` | `TestRunnerReadsTheClaimedRunsTeamSecrets` |
| `pkg/controller/secrets_test.go` | `TestSecrets_AdminReadResolvesTheNamedRunsPipeline` |
| `pkg/controller/source_credential_test.go` | `TestSourceCredential_TriggerClaimFetchesTheRunsSource` |
| `pkg/controller/storage_allowance_test.go` | `TestUnlicensedStorageWriteIgnoresCreditRate`, `TestUnlicensedStorageMaintenanceWritesNoCharges`, `TestAnEmptyBalanceRefusesAChargedWriteWithPaymentRequired` |
| `pkg/controller/storage_test.go` | `TestEventAppendRefusedPastTheBytesPerRunQuota`, `TestEventAppendRefusedPastTheBytesPerMonthQuota`, `TestLiveLogAppendIsNotChargedToStorage`, `TestStorageWritesUnboundedWithoutAQuota`, `TestHealthPublishesTheAlarmAndNoSizes`, `TestEventAppendRefusesAnUnboundedKind` |
| `pkg/controller/trigger_claim_ownership_test.go` | `TestTriggerClaim_StaleGenerationAnswersConflict`, `TestTriggerClaim_StrangerCannotFinishOrHeartbeat`, `TestTriggerClaim_HolderFinishesAndRetries`, `TestTriggerClaim_UnclaimedTriggerAnswersNotFound` |
| `pkg/controller/trigger_parent_claim_test.go` | `TestTriggerParentClaim_NodeClaimCreatesRunAndAwaitChild`, `TestWorkingTreeParentCannotAdmitChildWithoutItsOwnSource`, `TestTriggerParentClaim_TriggerClaimCreatesChild`, `TestTriggerParentClaim_RefusesAnythingButTheExactLiveParent`, `TestTriggerParentClaim_AdminPreservesOperatorAuthorization` |
| `pkg/controller/trigger_user_test.go` | `TestTrigger_UserIsTheAuthenticatedPrincipal` |
| `tests/acceptance/engine_reporting_http_test.go` | `TestReportingClientUsesJobRoutes` |
| `tests/acceptance/surface_contract_test.go` | `TestSurfaceRemovedClientRecoveryAPIs` |

209 old test entrypoints are removed. Helpers used only by those entries are also removed. The two unknown-route/method tests now use HTTP recorders. `ExampleNewRunReader` uses the final page schema and an explicit limit with a fake transport.

## Coverage obligations retained

| Concern from retired fixtures | Remaining evidence and integration obligation |
| --- | --- |
| Claims, matching, fencing, cancellation, execution starts and billing | Final store, engine and runner suites remain. Real HTTP/API-socket placement and store-backed lifecycle ports remain with W1/W2/W3/W8. |
| Pipeline/team secret selection, declaration and pool restrictions | Final secret client and authority-bearing HTTP properties remain. Real transactional secret-release composition remains W1/W2 work. |
| Source credentials and cross-run/repository reads | Existing source-route and `/data` properties remain. Port every old claim-token assertion to final capability ancestry and read-edge transactions. |
| Output integrity and transport isolation | Final data client and blob suites remain. Native host storage bindings still need final signed-transfer composition. |
| Log subject, attempt replacement, manifest consistency and post-completion grace | Direct handler and blob subject tests remain. Final host log writers and their authority/lease lifecycle still need real integration. |
| OIDC subject, audience, trust, expiry, rotation and cross-team refusal | The removed raw-client fixtures do not establish final capability-backed OIDC behavior. W2 must retain these source assertions in final signer/HTTP tests. |
| Profile observations, metrics, completion and concurrency | Store/direct-handler tests remain. Final telemetry, keeper and controller transactions must retain these assertions. |
| Steps, annotations, summaries and approval | Final client and HTTP components remain. W2 must implement the typed step report and acknowledgement already declared by W9/W3. |

No archived T1 source-function port, native cell or launch QA result is claimed by this deletion.
