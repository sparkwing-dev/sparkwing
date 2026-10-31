# Orientation

Read this first. It names the one owner of each important rule and the test
that guards it, so a lane can go straight to the code. Paths are relative to
the repository root. Store paths are under `internal/store/` unless written
out. When a rule changes, update its row here in the same commit.

Longer contracts: `internal/store/STORE-DATA.md` (schema, log slots, claim
ranking, lock order, admission), `internal/secrets/MASKING.md`,
`internal/runners/launcher/CLOUD-JOBS.md`, `internal/runners/HOSTING.md`,
`design/rewrite/ADMISSION.md`, `design/rewrite/RUN-ANYWHERE.md`, the
credential model in `docs/auth.md`, and the decision log
`design/rewrite/contracts/handoffs.md` (index below).

## Owners and guards

### Tenancy

| Rule | Owner | Guard test |
| --- | --- | --- |
| Team handle | `tenant.go`: `Store.ForTeam` returns `*Tenant`; `NormalizeTeam` is the one spelling | `tenant_test.go`: `TestForTeamRefusesTheEmptyTeam`, `TestTenantRunsAreInvisibleToAnotherTeam` |
| Foreign-team refusal for an authority | `capabilities_contract.go`: `resolveTeamAuthorityTx`, `resolveTeamActorTx` (`sameTeam` / `sameTeamOrCloudLauncher`) | `team_authority_guard_test.go`: `TestTeamScopedFunctionsResolveThroughTeam` |
| Every tenant-table statement carries `team` | the SQL in each tenant method | `tenant_sql_scope_guard_test.go`: `TestTenantSQLScope_EveryTenantTableStatementCarriesTheKey`; `tenant_writer_guard_test.go` |

### Run execution

| Rule | Owner | Guard test |
| --- | --- | --- |
| Missing dedicated Cloud pool fails the exact award and refunds its unstarted credit hold once | `internal/runners/launcher/node_team.go`: `CapacityConfigured`, `internal/store/launcher_rejection.go`: `failLauncherAwards` | `TestPoolConfigurationIgnoresScalingAndExhaustion`, `TestUnconfiguredCloudCapacityFailsAndRefundsOnce`, `TestCloudCapacityFailureThroughController` |
| Unactivated Cloud awards expose elapsed capacity waiting until activation | `internal/store/cloud_capacity_notice.go`: `readCloudCapacityWaitTx`, `internal/controller/engine_run_detail.go`: `runViewStatus` | `TestCloudCapacityNoticeEndsAtActivation`, `TestCloudCapacityNoticeUsesStoreElapsedTime`, `TestRunStatusDisplaysCloudCapacityWaitAndConfigurationFailure` |
| Foreground cancellation waits for terminal state | `cmd/sparkwing/run_host.go`: `runLocalCommand`, `here_attach.go`: `observeHereRun` | `TestLocalRunInterruptCancelsAndWaitsForTerminalRun` |
| Standalone controller survives intake through hosting | `cmd/sparkwing/run_host.go`: `runLocalCommand` joins preparation signal handling without canceling the controller | `TestStandaloneControllerContextSurvivesIntake`; `TestStandaloneRunOwnsControllerAndJoinsShutdown` |
| A fresh scaffold previews before its first commit and explains the commit required for execution | `cmd/sparkwing/local_run.go`: `prepareNativeLocalRun` | `TestFreshScaffoldPlansAndExplainsInitialCommitBeforeRun` |
| Waiting warm runner owners leave job capacity available | `internal/runners/loop.go`: `Loop.run` accounts for trigger owners separately from jobs | `TestLoopOwnerDoesNotOccupyItsJobsOnlySlot` |
| Release subscription refusals name the missing tag filter | `internal/controller/github_app.go`: `handlePutGitHubAppTrigger` preserves subscription input validation reasons | `TestGitHubAppReleaseSubscriptionValidationExplainsTagFilter` |
| Final cancellation reports retain the hard claim lifetime | `internal/nodehost/executor.go`: `report`, `claimed_node.go`: `claimSetupFailure` | `TestCancellationReportsUseTheLiveClaimLifetime`, `TestOwnerFinishesCancellationAfterACommittedPolicyResult` |
| Foreground worker bound | `internal/nodehost/owner_schedule.go`: `OwnerHost.schedule` | `TestOwnerHostWorkersBoundLocalExecution` |
| Successful previews finish as `dry_run` | `internal/store/scheduling_contract.go`: `Decide`; `internal/runstate/state.go`: `RunDryRun` | `internal/store/run_dry_run_test.go` |
| Returning unstarted owner reservations removes only unsigned empty logs | `internal/store/unit_release.go`: `releaseOwnerUnitTx` | `TestReleasedUnstartedOwnersLeaveNoMissingLogs` |
| Paged logs drain committed records, then report unsealed terminal logs incomplete after the existing seal grace | `internal/controller/log_pages.go`: `readLogPage` | `TestPersistedLogPageEndsUnsealedTerminalObservationAfterGrace` |
| Recovery reads its persisted source failure | `internal/nodehost/native_assembly.go`, `assembly_native_client.go`; `internal/controller/private_host.go` | `TestNodeExecutorRecoveryReceivesPersistedFailure`, `TestPrivateNodeRecoveryReadsOnlyItsPersistedSource` |

### Trust and provenance

| Rule | Owner | Guard test |
| --- | --- | --- |
| Trust classes and named sets | `internal/runspec/provenance.go`: `TrustClass`, `TrustSet` (`AppRepositoryTrust`, `AppDeliveryTrust`, `GitHubEventTrust`, `AllTrust`); SQL renders them via `trustIn` in `claim_query.go` | `trust_matrix_test.go`: `TestTrustMatrix` |
| Verified default-branch work | intake-sealed `Provenance.DefaultBranchWork`; `claimSQL.defaultBranchWork` reads the stored fact | `trust_sql_test.go`: `TestDefaultBranchWorkSQLAgreesWithGo` |
| Sealed provenance | `internal/runspec/seal.go`: `SealRequest`; store side `intake_provenance.go`: `sealProvenanceTx`, `identityTx` | `internal/runspec/provenance_test.go`: `TestProvenanceRoundTripsAndRefusesForgedSeals`; `internal/runspec/spec_test.go`: `TestSealRefusals` |
| Proven repository (App, Actions, fetched and compared source) | `internal/controller/intake_source.go`: `selectFetchedSource`; `run_spec.go`: `FetchedRepository`; `intake_provenance.go`: `fetchedRepositoryTx`; `Provenance.FetchedFrom` | `internal/controller/intake_source_test.go`: `TestFetchedGitHubRepositoryFollowsSourceSelection` |
| Source selection leaves the shared temporary parent unchanged | `internal/source/select.go`: `readCommitFile` prepares only an explicit workspace; `fssecure.MkdirPrivateTemp` owns the private child | `internal/source/select_temp_unix_test.go`: `TestSelectLeavesTheSharedTemporaryParentUnchanged` |
| Remote configuration size is checked without downloading oversized blobs | `internal/source/select.go`: `readCommitFile` filters blobs and blocks transports during local inspection | `internal/source/select_size_test.go`: `TestTenantSelectionNeverDownloadsAnOversizedConfiguration`, `TestTenantSelectionAllowsAnAbsentConfiguration`; `select_size_unix_test.go`: `TestTenantSelectionRefusesOversizedConfigurationBehindSymlink` |
| Sealed host boundary | `claimant_facts.go` classifies at claim; `Capability.Host` persists it; `run_trust.go`: `checkRunHost` | `proven_repository_claim_test.go`; `proven_repository_host_test.go`; `claimant_facts_test.go`: `TestClaimantReadsSealedHostFacts`, `TestUnknownClaimantsCannotExecuteProvenRepositoryRuns` |
| GitHub compare outages admit asserted App runs without repository proof and explain why in status | `internal/controller/intake_source_credential.go`: `verifyIntakeSourceCredential`; `intake_provenance.go`: `identityTx`, `fetchedRepositoryTx` | `internal/controller/github_app_compare_test.go`: `TestGitHubAppCompareUnavailableAdmitsUnverifiedRun` |
| Branch-pinned pipeline checkout survives source initialization | `internal/source/checkout.go`: `PrepareCheckout` acquires a separate pipeline commit from its sealed source | `internal/runners/launcher/source_init_test.go`: `TestSourceInitPreservesBranchPinnedPipelineAcquisition` |
| App child intake preserves exact-revision trust and demotes changed source | `internal/controller/client/native_services.go`: `StartChild`; `intake_provenance.go`: `identityTx` | `internal/controller/child_app_intake_test.go`: `TestAppParentChildrenPreserveOnlyExactRevisionTrust` |
| Job source credentials | `source_release.go` reads sealed proof and compare; `controller/intake_source_credential.go` performs the intake compare | `source_release_test.go`: `TestSourceReleaseRequiresSealedRepositoryAndIntakeCompare`; `internal/controller/intake_source_credential_test.go`: `TestIntakeSourceCredentialComparesRepositoryRefsAndBothCommits` |
| Repository cache fallbacks require sealed proof; awards use the proven key | `intake_provenance.go`: `sealProvenanceTx`; `authority_issue.go`: `awardDataScope` | `cache_provenance_test.go`: `TestCacheDefaultLookupRequiresProvenRepository` |
| Latest pipeline references | `latest_run.go`, `binding_execution.go`; sealed reference `FromAnyRun` opt-out | `latest_run_test.go`; `binding_pipeline_test.go` |
| OIDC claims | `Provenance.OIDC`; `internal/oidcissuer/issuer.go`: `Claims`, `runnerEnvironment`; `authority_oidc.go`: `Tenant.MintOIDCToken` | `oidc_repository_test.go`: `TestOIDCRepositoryClaimReadsSealedProvenance`; `pool_launcher_test.go`: `TestLauncherPodOIDCIsFresh` |
| OIDC without an issuer preserves `not_found` and explains controller setup | `internal/nodehost/process_service_reads.go`: `ProcessServices.oidc` | `internal/nodehost/routes_oidc_test.go`: `TestOIDCWithoutIssuerExplainsControllerSetup` |

### Claims and placement

| Rule | Owner | Guard test |
| --- | --- | --- |
| One claim predicate (decision 44) | `claim_query.go`: `claimSQL.predicate`, `claimSQL.query`, `claimQueryTx` | `claim_predicate_regression_test.go`; `claim_search_bound_test.go`: `TestClaimSearchPreservesTeamFairShareBeforeArrival` |
| Fresh vs persistent host | `claimant_facts.go`: `claimantFactsTx` classifies once; `Capability.Host` seals freshness and cache scope; SQL `claimSQL.freshPod` | `claim_trust_test.go`: `TestVerifiedWorkOnAPersistentHostWritesItsHostScope`; `claimant_facts_test.go` |
| Pools and reserved secrets | `controller_pools.go`: `claimSQL.poolSatisfies`; `claimSQL.poolTrust` | `claim_trust_test.go`: `TestReservedSecretPoolTakesOnlyVerifiedDefaultBranchWork`; `pool_launcher_test.go` |
| Ranking and preference holds (decision 52) | `claim_query.go`: `rankingHold`, `firstPreferenceHold`, `placementCTEs` | `claim_ranking_test.go` |
| Team holds (operator, fraud) | `credit_reversals.go`: `teamHoldTx`, `Tenant.HoldByOperator` | `hold_stop_test.go` |
| Concurrency admission (decision 45) | `capacity_admission.go`: `Tenant.MayStart`, `admitWorkTx`; `capacity_owner.go`: `tryAdmissionLockTx` | `admission_concurrent_test.go`; `capacity_order_postgres_test.go`: `TestPostgresAdmissionLocksFollowTheirStoreSchema` |
| Owner placement uses one shared eligibility read for a run snapshot | `state_owner_read.go`: `readOwnerSnapshot`; `owner_placement.go`: `ownerEligibleNodesTx` | `owner_snapshot_scale_test.go`: `TestOwnerSnapshotPlacementWorkDoesNotMultiplyByJobCount` |
| Whole snapshots load node rows and retry counts together | `state_read.go`: `readSnapshotTx`, `scanNode` | `state_snapshot_batch_test.go`: `TestReadSnapshotLoadsNodeRowsTogether` |
| Private owner-job reads select only the requested job | `state_owner_read.go`: `Tenant.ReadOwnerNode` | `owner_node_scale_test.go`: `TestOwnerNodeReadDoesNotLoadUnrelatedJobs` |
| Single-job reads and child authority checks retain live owner fences without recomputing placement | `state_owner_read.go`: `Tenant.ReadOwnerNode`; `internal/controller/private_host.go`: `localHostNode`, `localHostAuthority` | `internal/controller/private_placement_test.go`: `TestPrivateHostNodeReadsStateWithoutPlacement`; `private_host_authority_test.go`: `TestLocalHostChildAuthorityDoesNotReadUnrelatedJobs` |
| Ready jobs reuse the scheduling snapshot until a debug pause needs a fresh cancellation check | `internal/nodehost/owner_schedule.go`: `OwnerHost.release` | `internal/nodehost/owner_release_scale_test.go`: `TestOwnerReleaseDoesNotReadThePlanAgainForEachJob` |
| Local queue priority follows a node participant's owning run | `internal/wingd/server.go`: `runConnsLocked`; `internal/admission/ledger.go`: `specBelongsToRun` | `internal/wingd/set_priority_node_test.go`: `TestSetPriority_NodeParticipantRecognizesOwningRun`; `internal/admission/ledger_node_priority_test.go`: `TestNodeParticipantPrioritySurvivesOtherParticipantRelease` |

### Secrets

| Rule | Owner | Guard test |
| --- | --- | --- |
| Which secrets a run receives (decisions 66, 68, 70) | `binding_secret.go`: `runSecretSelection` | `secret_selection_test.go`; `internal/controller/secrets_encryption_test.go`: `TestSecrets_AssertedRunReadsOnlySharedSecrets` |
| Optional secret declarations survive checkout planning and plan acceptance | `internal/nodehost/checkout_planning.go`: `PrepareCheckout`; `plan_seal.go`: `preparePlanSealTx` | `internal/nodehost/owner_secret_preflight_test.go`: `TestCheckoutSecretOptionalitySurvivesPlanAcceptance` |
| Masking algorithm | `internal/secrets/mask.go`: `Masker`, `Maskable`, `Stream` | `internal/secrets/mask_property_test.go`: `TestMaskLeavesNoSecretVisibleProperty` |
| Team secret count (decision 73) | `credential_mutations.go`: `Tenant.WriteSecret`; `secrets.go`: `maxSecretsPerTeam` | `credential_mutations_runtime_test.go`: `TestCredentialMutationsSecretLimitsPreserveExistingValues` |
| Host masker registration | `internal/nodehost/mask_registry.go`: `MaskRegistry` | `internal/nodehost/accepted_masks_test.go`: `TestSealedSecretInputsNeverEscapeHostBoundaries` |

### Billing

| Rule | Owner | Guard test |
| --- | --- | --- |
| Financial row shapes and one open interval per award | `schema.sql`, translated by `schema.go` for both dialects | `schema_batch_test.go`: `TestSchemaRejectsIncompleteFinancialDecisions`, `TestSchemaAllowsOnlyOneOpenBillingIntervalPerAward` |
| Ledger writer | `ledger.go`: `ledgerTx` (`lockLedgerTx`, `insertGrant`, `insertCharge`, `debitFunded`) | `ledger_guard_test.go`: `TestLedgerTablesAreWrittenOnlyThroughTheLedger` |
| `payment_mode` on money statements (decision 63) | every money query filters `payment_mode` | `ledger_guard_test.go`: `TestMoneyStatementsCarryPaymentMode`; `payment_mode_test.go` |
| Refill eligibility | `refill.go`: `SpendStanding.refillEligible` | `refill_model_test.go`: `TestRefillModel`; `refill_test.go` |
| Storage admission | `storage_admission.go`: `admitStorageGrowthTx` | `storage_admission_dialect_test.go` |
| Storage quota refusals name tier, limit, usage and remedy | `storage.go`: `StorageQuotaError.Error`; `internal/controller/engine_routes.go`: `engineLookupError` | `internal/controller/source_upload_test.go`: `TestSourceQuotaRefusalNamesTierUsageAndRemedy`, `TestSourceAttachmentQuotaRefusalNamesTierAndRemedy`; `upload_quota_runtime_test.go` |
| Log slots | `result_logs.go`: `openLogSlotTx`, `commitLogSlotTx`; `controller_reporting.go`: `Tenant.OpenLogSlot` | `storage_admission_dialect_test.go`: `TestLogSlotsCountAgainstTheRunObjectLimit` |
| Metering and lease renewal | `metered_accounting.go`: `renewBillingAwardTx`, `settleBillingChargesTx`; `state_renewal.go`: `Tenant.RenewNodeClaim` | `metered_accounting_test.go`; `refill_test.go`: `TestRenewalPaysTheNextSliceAndSettlementReturnsTheRest`; `unit_billing_test.go`: `TestFirstRenewalRunsAJobItsAdmittedSliceCovers` |
| Free-tier limits only where usage is metered | `free_tier.go`: `Store.holdsFreeAllowance` | `free_tier_mode_test.go`: `TestUnmeteredDeploymentAppliesNoFreeTierLimits` |
| Payment bounds ($10 to $1,000) | `refill.go`: `MinCardPaymentCents`, `MaxCardPaymentCents`; checked in `billing_pages.go` and `RefillSettings.validate` | `internal/controller/checkout_contract_internal_test.go` |
| Checkout Session creation (decision 73) | `billing_pages.go`: `Tenant.PrepareAuthorizedBillingPage`, `checkoutSessionsPerHour` | `billing_pages_test.go`: `TestBillingPagesShareHourlySessionLimitAndReplay`, `TestBillingPagesConcurrentSessionLimit`; `internal/controller/team_billing_test.go`: `TestTeamBillingCheckoutHourlyLimitRefusesBeforeProvider` |
| Controller and checkout service contract | `internal/controller/testdata/checkout_contract.json`, byte-identical with sparkwing-platform's `internal/billing/testdata` copy; final versus retried refusals in `internal/controller/billing_checkout.go`: `billingCheckout.post` | `checkout_contract_internal_test.go`; `refund_contract_internal_test.go`: `TestCapturedRefundSettlesExactlyOnce`; `internal/capability/payment_contract_test.go`; `payment_deployment_contract_test.go` |

### Lock order

| Rule | Owner | Guard test |
| --- | --- | --- |
| Declared order and row locks | `lock_order.go` (`acquireLockSet`, `lockFinancialTeamsTx`, `lockClaimQueueTx`); prose in `STORE-DATA.md` "Lock order" | `lock_order_guard_test.go`: `TestRowLockClausesLiveInLockOrder`; `financial_lock_order_test.go`; `*_lock_order_postgres_test.go` |
| Snapshot reads skip writer lock bookkeeping across teams | `lock_order.go`: `lockFinancialTeamsTx`, `lockWorkRowTx` | `launcher_multiteam_test.go`: `TestLauncherSnapshotAcrossTeamsInObservationOrder` |

### Identity

| Rule | Owner | Guard test |
| --- | --- | --- |
| Email address ownership | `identity.go`: `claimEmailTx`, `releaseEmailTx`, `dropUnprovenEmailOwnershipTx` | `identity_test.go`: `TestIdentitySignInRefusesAnUnverifiedEmail`, `TestIdentityRecycledEmailNeverLinksIntoTheOldAccount` |
| Invitations | `identity.go`: `Tenant.createInvitationTx`, `Operator.acceptAccountInvitationTx` | `identity_test.go`: `TestIdentityInvitationNeedsTheInvitedVerifiedEmail`; `team_mutations_test.go` |
| Team creation has no lifetime count cap (decision 73) | `identity.go`: `createAccountTeamTx` | `identity_test.go`: `TestIdentityTeamCreationHasNoLifetimeCap`; `deletion_review_test.go`: `TestTeamDeletionDoesNotRestrictFutureTeamCreation` |
| Team membership has no count cap | `identity.go`: `Operator.acceptAccountInvitationTx`; `team_mutations.go`: `CreateAuthorizedInvitation` | `signup_gate_test.go`: `TestTeamMembershipHasNoCap`; `internal/controller/browser_refusal_test.go`: `TestInvitationAcceptanceHasNoMemberCap` |
| Unscoped runtime awards require the owner machine and unproven source | `internal/runners/repository.go`: `machineHost.Host` | `internal/runners/repository_test.go`: `TestUnscopedMachineHostsOnlyItsOwnersAssertedAwards` |
| Personal enrollment may omit repository scopes; shared enrollment requires them | `enrollment_runtime.go`: `authorizeEnrollmentTx`, `enrolledClaimProfileTx`; `authority_provision.go`: `provisionCapabilityTx` | `enrollment_runtime_test.go`: `TestUnscopedEnrollmentIsPersonalOnly`; `internal/controller/burst_overflow_test.go`: `TestBurstPersonalEnrollmentAndClaimsUseTheAccountOwner` |
| Headless runner enrollment preserves independent account authority | `cmd/sparkwing-controller/admin.go`: `admin session`; `internal/capability/policy.go`: enrollment refusal | `cmd/sparkwing-controller/admin_session_test.go`: `TestAdminSessionEnrollsSharedRunnerAndReservedLauncher`, `TestAdminSessionRequiresExistingTeamAdministrator`; `internal/capability/authorization_test.go`: `TestRunnerEnrollmentRouteRequiresStoreOriginAuthority` |
| Controller standby explains its advisory-lock wait | `controller_lock.go`: `OpenControllerPostgres` | `controller_lock_test.go`: `TestControllerStandbyLogsBeforeWaiting` |
| Stdin enrollment attempts logout after session authentication on success or failure | `cmd/sparkwing/runners.go`: `runRunnersAdd` | `cmd/sparkwing/runners_personal_test.go`: `TestRunnersPersonalEnrollmentCLIThroughController`; `cmd/sparkwing/runners_test.go`: `TestRunnersPersonalEnrollmentUsesEphemeralSession` |
| Break-glass session mints commit an audit row with no secret or reason | `identity.go`: `Operator.CreateBreakGlassSession` | `cmd/sparkwing-controller/admin_session_test.go`: `TestAdminSessionEnrollsSharedRunnerAndReservedLauncher` |
| Sessions | `auth.go`: `Operator.LookupSession`, `CreateCapabilitySession`; `identity.go`: `createAccountSession` | `session_runtime_test.go`; `internal/controller/session_store_runtime_test.go`: `TestMemberTokenHTTPSessionTimeoutAndLogout` |
| Tokens and capability issuance | `authority_issue.go`: `Store.issueTx`, `awardCapabilityTx` | `security_audit_test.go`: `TestDelegatedTokenRequiresAudit` |

### GitHub App

| Rule | Owner | Guard test |
| --- | --- | --- |
| Durable webhook inbox (decision 41) | `github_delivery_inbox.go`: `EnqueueGitHubDelivery`, `ClaimGitHubDelivery`, `githubDeliveryResultTx` (effect receipts) | `github_delivery_inbox_test.go`: `TestGitHubDeliveryFailuresNeverRetry` |
| Installations | `github_app.go`: `Tenant.bindGitHubInstallationTx`, `GitHubAppInstallations` | `github_app_test.go`: `TestGitHubAppInstallationBelongsToOneTeam` |
| Browser connection requires the current team owner and consumes its state once | `internal/controller/browser_github_app_routes.go`: `verifyGitHubAppBrowser`; `github_connect.go`: `Tenant.ConsumeGitHubConnect` | `internal/controller/github_app_test.go`: `TestGitHubAppCompletionRechecksOwnerTeamAndExplainsReplay` |
| Existing-installation discovery retries within its original session, account, team and expiry; selection consumes the state once | `internal/controller/browser_github_app.go`: `handleGitHubAppCallback`; `internal/controller/github_app.go`: `githubAppAvailable`, `githubAppSelect` | `internal/controller/browser_github_app_routes_test.go`: `TestGitHubBrowserAvailableRetry`; `web/tests/browser/github-app-callback.spec.ts` |
| Teardown on uninstall | `github_app.go`: `Tenant.unbindGitHubAppInstallationTx`, `Operator.UnbindGitHubAppInstallation` | `github_enrollment_test.go`: `TestInstallationUnbindEndsActionsConsentAndAutomation`; `github_app_test.go` |

### Launcher and Cloud

| Rule | Owner | Guard test |
| --- | --- | --- |
| Cloud Docker client and writable temporary directory | `build/Dockerfile.runner`, `internal/cluster/production_runtime.go`: `runnerInstallBase`, `productionRuntime` | `bin/check-runner-image.sh`, `TestRunnerImageRequiresDockerAndItsPlugins`, `TestCloudProcessesKeepTheSidecarDockerHost`, `TestCloudHostRestoresTemporaryDirectoryPermissions`, `TestCloudWorkloadsUseTheSidecarSocket` |
| Job lifecycle | `internal/runners/launcher/job.go`: `BuildJob`; `internal/runners/launcher/sync.go`: `Launcher.Sync` | `internal/runners/launcher/launcher_test.go`: `TestSyncAdoptsLiveJobWithoutCreatingAnother`; `internal/runners/launcher/sync_batch_test.go` |
| Unsupported Cloud sizes fail at intake or planning with the catalogue choices | `pkg/match/cloud_sizes.go`: `ValidateCloudSizeLabels`; `internal/settings/resolve.go`: `validNames` | `internal/settings/cloud_sizes_test.go`: `TestCloudSizeValidationAtRequestAndPlan` |
| Working-tree sources require a local checkout before queueing; uploaded committed snapshots remain valid | `internal/runspec/seal.go`: `SealRequest` | `internal/store/source_intake_test.go`: `TestAdmissionRefusesRemoteWorkingTreeBeforeQueueing`; `internal/runspec/source_test.go`: `TestSourceSealPreservesLocalWorkingTreesAndRemoteSnapshots` |
| Internal Cloud sync failures appear in pending plan status | `internal/store/launcher_sync_failure.go`: `Operator.RecordLauncherSyncFailure`; `internal/store/run_state.go`: `writeOwnerRecoveryTx` | `internal/controller/engine_launcher_failure_test.go` |
| Node reuse labels (one run per node) | `internal/runners/launcher/node_team.go`: `usedTeamLabel`, `usedRunLabel`, `runNodeValue`; `internal/runners/launcher/boundary.go`: `KubernetesBoundary.Reserve` | `internal/runners/launcher/boundary_test.go`: `TestBoundaryReusesAWarmNodeOnlyWithinItsRun` |
| Pod admission policy | `internal/runners/launcher/admission_policy.go` | `internal/runners/launcher/admission_policy_test.go`: `TestAdmissionPolicyOnAPIServer` (skips without `KUBEBUILDER_ASSETS`) |
| NetworkPolicy (decision 71) | `charts/sparkwing-runner-bundle/templates/launcher-networkpolicy.yaml` | `charts/cloud_test.go`: `TestCloudExampleRendersCompleteInstall` |

### Memoization

| Rule | Owner | Guard test |
| --- | --- | --- |
| Local repository memo lookups miss after scope checks; publication is skipped | `blob_lookup.go`: `lookupMemoTx`; `internal/nodehost/executor_memo.go`: `saveMemo` | `blob_local_cache_test.go`: `TestLocalDepcacheRoundTrip`; `internal/nodehost/native_memo_test.go`: `TestNativeExecutorLocalRepositoryMemoRunsBody`; `internal/nodehost/executor_memo_test.go`: `TestExecutorLocalRepositorySkipsMemoPublication` |

### Metrics

| Rule | Owner | Guard test |
| --- | --- | --- |
| Completed Cloud jobs with no resource samples report metrics unavailable; stored points and exhausted pages stay readable | `internal/controller/metrics.go`: `handleGetJobMetrics` | `TestMetricsHTTPCompletedCloudJobWithoutSamples` |

### Logs

| Rule | Owner | Guard test |
| --- | --- | --- |
| CLI snapshots drain available pages and report pending final logs with exit 3 | `cmd/sparkwing/runs_logs.go`: `observeRunLogs` | `TestRunLogsReportsUnsealedSnapshotAfterDrainingPages` |
| Owner streams register with log-writing authority; unexchanged bootstrap requeues do not keep finished aggregates incomplete | `internal/store/authority_issue.go`: `Store.issueTx` | `internal/controller/log_pages_test.go`: `TestRunLogPageAccountsForRequeuedPlanWriters`; `internal/store/unit_renewal_test.go`: `TestStartedUnitsRegisterLogsBeforeOutput` |
| Retry advances the log writer fence to the new attempt; old tails are refused and current terminal tails retain seal grace | `internal/controller/log_submission_routes.go`: `logSubmission`; `internal/store/result_logs.go`: `checkLogWriterTx`; `authentication_binding.go`: `authenticationNodeTx` | `internal/controller/retry_logs_test.go`: `TestRetryLogUploadsUseTheirAwardedWriter`; `internal/store/log_persistence_test.go`: `TestLogPersistenceRetryFencesPreviousAttempt` |
| Paged reads of capped logs report truncation and name the 64 MiB attempt cap | `internal/controller/log_pages.go`: `appendSubjectLines` | `TestPersistedLogByteCapNamesTheTruncationLimit` |
| Bound node commands flush final logs at report, retry or release, within the attempt's tail grace | `internal/nodehost/assembly_native_logs.go`: `logHostStore.TransitionNode` reads the command payload | `internal/nodehost/assembly_native_logs_test.go`: `TestStalledLogsDoNotDelayLifecycleCommits`, `TestLogLifecycleDetachesFlush` |
| Runner frames and lifecycle failures render as text; retries persist the next attempt ordinal | `cmd/sparkwing/runs_logs.go`: `writeLogRecord`, `eventDetail`; `internal/store/node_state.go`: `transitionNodeStateTx` | `TestRunLogsRendersPersistedRunnerAndRetryPayloads`; `TestTransitionApplicationRetryKeepsClaimAndOrdinal` |

### Console

| Rule | Owner | Guard test |
| --- | --- | --- |
| Unstarted runs have no execution duration and use queued creation time when present | `web/src/lib/runTime.ts`; run rows and timing panels | `web/src/lib/runTime.test.ts`, `runDetail.test.ts`; `SetupPanel.test.tsx`, `SummaryPanel.test.tsx`; `web/tests/browser/dashboard.spec.ts`: queued timing |
| Run controls wrap on phones and queued runs can be cancelled | `web/src/app/runs/page.tsx`: `RunDetailPane` | `web/tests/browser/dashboard.spec.ts`: phone viewport geometry, pending and queued cancellation |
| Run submission refusals explain the remedy and link repository access to Team > GitHub | `web/src/lib/api.ts`: `triggerRun`; `web/src/components/TriggerForm.tsx` | `web/src/lib/triggerRun.test.ts`; `web/tests/browser/trigger-refusal.spec.ts` |
| Login refusals always explain nonempty reasons; token text has readable contrast and a main landmark | `internal/controller/browser_login.go`: `handleLoginPage`, `loginHTML` | `internal/controller/browser_login_presentation_test.go`; `web/tests/browser/login-accessibility.spec.ts` |

### CLI and API surface

| Rule | Owner | Guard test |
| --- | --- | --- |
| Local dashboard sessions authenticate the peer that minted the link without forwarding browser credentials | `internal/wingd/local_login.go`: `serveSession` | `internal/wingd/localcontroller/dashboard_login_test.go`: `TestDashboardLoginAuthorizesControllerAPIAsMintingPeer` |
| Plan preview JSON preserves normalized runner labels | `cmd/sparkwing/action_plan.go`: `previewPlan` | `cmd/sparkwing/action_plan_test.go`: `TestPlanPreviewJSONRetainsNormalizedRunnerLabels` |
| Schedule health follows the running controller evaluator or observed native timer activation | `internal/controller/crons.go`: `runCronTick`; `internal/crontimer/crontimer.go`: `Status` | `internal/controller/schedule_routes_test.go`: `TestScheduleHTTPLocalHealthDoesNotInventControllerLoop`, `TestScheduleHTTPHealthFollowsControllerLoopLifetime`; `internal/crontimer/status_test.go`: `TestStatusReportsNativeActivation` |
| Schedule time zones resolve without host zoneinfo | `cmd/sparkwing-controller/main.go`, `cmd/sparkwing/main.go`: embedded `time/tzdata` | `cmd/sparkwing-controller/schedule_timezone_test.go`: `TestScheduleTimeZonesWithEmptyZoneInfo`; `cmd/sparkwing/schedules_contract_test.go`: `TestScheduleManagementTimeZonesWithEmptyZoneInfo` |
| Team schedule count (decision 73) | `schedule_transactions.go`: `Tenant.replaceConsentedSchedules`; `internal/limits/cron.go`: `MaxSchedulesPerTeam` | `schedule_transactions_test.go`: `TestScheduleTeamCapSpansRepositories`, `TestScheduleTeamCapAllowsFiftyInOneRepository` |
| Invalid cron overrides name the field and a valid example | `internal/cronspec/override.go`: `Override.Validate` | `internal/cronspec/override_test.go`: `TestOverrideCronErrorNamesFieldAndExample`; `cmd/sparkwing/schedules_contract_test.go`: `TestScheduleManagementCronErrorNamesFieldAndExample`; `internal/controller/schedule_routes_test.go`: `TestScheduleHTTPInvalidCronNamesFieldAndExample` |
| Local argument discovery | `cmd/sparkwing/local_run.go`: `prepareNativeLocalRun` inspects declarations before invocation values are parsed; `internal/nodeproto/arguments.go`: public enum errors list choices | `cmd/sparkwing/local_describe_unix_test.go`: `TestNativeLocalPreparationInspectsBeforeParsingRequiredEnum` |
| Undeclared child refusal | `internal/controller/child_source.go`: `resolveChildRevisions`; `internal/store/intake_runtime.go`: intake authorization names `StartsChildren` | `internal/controller/child_source_test.go`: `TestUndeclaredChildSourceNamesDeclarationMethod`; `internal/store/provenance_reference_test.go`: `TestChildPipelinesMustBeDeclared` |
| TypeScript scaffold and fleet inventory | `cmd/sparkwing/action_new_typescript.go`: `scaffoldTypeScriptPipeline`; `internal/repos/fleet.go`: `inventoryPin` delegates npm pins to `engine.ReadPin` | `cmd/sparkwing/action_new_test.go`: `TestPipelineNewTypeScriptBootstrapsAndAddsPipelines`; `internal/repos/fleet_test.go`: `TestDeriveFleetIncludesSDKAndTypeScriptLockedPin` |
| TypeScript main preserves Node package imports through symlinks | `sdk/typescript/src/main.ts`: `pipe` uses bigint stats to preserve Node's realpath state | `sdk/typescript/test/main-imports.test.ts`: CommonJS and ESM default/named imports match plain Node |
| Default CLI profile | `cmd/sparkwing/profiles.go`: `setProfile` switches one saved default; `profile_flag.go`: `applyConfiguredProfile` applies it only to optional-profile CLI invocations; internal local clients bypass it | `cmd/sparkwing/profile_cmd_test.go`: `TestProfileDefaultSwitchControlsCLIAndKeepsLocalHelpersLocal` |
| Command typo recovery | `internal/clisurface/parse.go`: `Parse` suggests sibling commands through `sparkwingruntime.SuggestClosest` | `internal/clisurface/parse_test.go`: `TestMisspelledCommandSuggestsNearestSibling` |
| Run status timing | `cmd/sparkwing/runs_view.go`: `writeRunDetail` prints recorded run and job timestamps and completed durations | `cmd/sparkwing/runs_status_contract_test.go`: `TestRunStatusTextIncludesRunAndJobTiming` |
| Runner warm Actions identity requires HTTPS except on loopback; removed flags and chart values fail | `internal/cluster/production_runtime.go`, `internal/controller/client/github_actions.go`, `charts/sparkwing-runner-bundle/templates/validate.yaml` | `internal/cluster/actions_runtime_test.go`: `TestProductionWarmExchangesActionsIdentity`; `internal/controller/client/github_actions_test.go`: `TestExchangeGitHubActionsRequiresHTTPSExceptLoopback`; `runner_cli_test.go`: `TestWarmRejectsInertFlags`; `internal/clisurface/parse_test.go`: `TestLintRejectsRemovedStaticFlag`; `charts/render_test.go`: `TestRunnerRejectsRemovedWarmModules` |
| Command registry | `internal/clisurface/registry.go`: `Registry` | `internal/clisurface/contract_test.go` |
| SDK upgrades verify with isolated local Git metadata, including linked worktrees; the acquisition guard permits only the local metadata clone form outside `internal/source` | `internal/engine/upgrade.go`: `copyCheckout` | `internal/engine/upgrade_test.go`: `TestUpgradeVerificationRetainsIsolatedGitMetadata`; `internal/source/ownership_test.go`: `TestGitAcquisitionHasOneOwner`, `TestGitAcquisitionGuardRecognizesCommandArguments` |
| Build and hosted Go toolchains select the fixed patch while module floors stay portable | `go.mod`, `.sparkwing/go.mod`, `.github/workflows/` | `.sparkwing/jobs/security_workflow_test.go`: `TestBuildToolchainsUseFixedGoPatch` |
| Route table | `internal/capability/tables.go`: `Routes`; dispatch in `internal/routepolicy` | `internal/capability/policy_test.go` |
| Inventory and run-detail list pagination (decision 73) | `internal/controller/list_pages.go`: `pageCollection`; `internal/controller/client/list_pages.go`: `requestList`; `web/src/lib/listPages.ts` | `internal/controller/list_pages_test.go`, `list_pages_http_test.go`, `list_pages_attempt_http_test.go`; `internal/controller/client/list_pages_test.go`; `web/src/lib/listPages.test.ts` |
| Grant refusals name required authority without exposing resource state | `internal/capability/refusal.go`: `RequiredAuthority`; `internal/controller/data_request_binding.go`: `resolveDataTarget` | `internal/controller/refusal_guidance_test.go`: `TestPermissionRefusalsExplainRequiredAuthority`, `TestPermissionRefusalsKeepForeignRunsIndistinguishableFromAbsentRuns` |
| OpenAPI | `internal/apispec` writes `api/openapi.yaml`; regenerate with `bin/gen-api-docs.sh` | `bin/check-api-spec.sh`; `internal/apispec/final_contract_test.go` |
| Docs mirror and drift | `docs/` is the source; `bin/sync-docs.sh` copies it to `pkg/docs/mirror/`; `bin/regen-all.sh` reruns every generator | `bin/sync-docs.sh --check`; `internal/doccheck/cliverbs_test.go`, `internal/doccheck/apiroutes_test.go` |
| Local readiness probes do not wait for the database writer | `internal/wingd/localcontroller/state.go`: `localState.Ready`; `internal/wingd/localcontroller/admission.go`: `localController.Ready` | `internal/wingd/localcontroller/state_readiness_test.go`: `TestStateReadinessDoesNotWaitForStoreWriter` |
| Private child authentication resolves the requested identity | `internal/controller/private_host.go`: `localHostAuthority` | `internal/controller/private_host_authority_test.go`: `TestLocalHostChildAuthorityDoesNotReadUnrelatedJobs` |
| Jobs that never started keep their terminal explanation | `state_read.go`: `readNodeTx`; `internal/controller/engine_run_detail.go`: `fillJobDeclarations`; `internal/controller/engine_snapshot.go`: `snapshotJob` | `internal/controller/unstarted_status_test.go`: `TestRunDetailExplainsJobsThatNeverStarted` |
| Local approval deadlines and pending listings | `internal/wingd/localcontroller/controller.go`: `maintain`; `tenant_reads.go`: `ListPendingApprovals` | `internal/wingd/localcontroller/approval_deadline_test.go`: `TestMaintenanceSettlesTenSecondApprovalWithinOneSecond`; `approval_reaper_runtime_test.go`: `TestCancelledApprovalLeavesPendingListAndKeepsHistory` |
| Local owner-selected debug boundaries persist and follow release | `debug_pause_owner.go`: `Tenant.OwnerDebugPause`; `internal/nodehost/native_assembly.go` binds `storeDebugBoundary` | `internal/nodehost/native_debug_test.go`: `TestNativeForegroundOwnerPersistsDebugPauseUntilReleased` |
| Run statistics include nearest-rank P50 | `cmd/sparkwing/runs_stats.go`: `aggregateRunDetails` | `cmd/sparkwing/runs_stats_median_test.go`: `TestRunStatisticsReportsNearestRankP50` |
| Compiled pipeline reuse emits a cache-hit diagnostic | `internal/bincache/materialize.go`: `MaterializePipeline` | `internal/bincache/materialize_test.go`: `TestMaterializePipelineReusesBinaryWithoutGo` |
| Missing local secrets carry local repair commands | `secret_preflight.go`: `secretPreflightTx`, `secretPreflightError.detail` | `secret_preflight_local_test.go`: `TestLocalSecretPreflightGivesLocalSetCommand` |
| Required-secret plan refusals finish unaccepted runs | `internal/nodehost/owner.go`: `OwnerHost.accept`, `OwnerHost.preparationFailure` | `internal/nodehost/owner_secret_preflight_test.go`: `TestOwnerHostMissingRequiredSecretFinishesUnacceptedRun` |
| Repository release home is checked before intake opens the store | `.sparkwing/jobs/release.go`: release factory captures `requireReleaseHome` and `Plan` returns a refusal during discovery | `.sparkwing/jobs/release_home_test.go`: `TestReleaseRefusesOperationalHomeDuringPlanning` |
| Release schema checks compare the neutral SQL baseline with the previous tag | `.sparkwing/jobs/release.go`: `checkSchemaBreak`, `schemaSQLDrift` | `.sparkwing/jobs/release_schema_changelog_test.go`: `TestSchemaGateComparesTheNeutralBaseline` |
| Release notes stay uncommitted until every gate passes | `.sparkwing/jobs/release.go`: `prepareChangelogJob`, `commitChangelogJob`, `Release.Plan` | `.sparkwing/jobs/release_atomic_test.go`: `TestReleaseGateFailureAfterPreparationLeavesHEADUnchanged`; `.sparkwing/jobs/release_plan_test.go`: `TestReleasePlanCommitsOnlyAfterAllGates` |
| Release gates fetch comparison tags missing from a shallow job checkout | `.sparkwing/jobs/release.go`: `fileAtTag` | `.sparkwing/jobs/release_checkout_tags_test.go`: `TestReleaseGatesFetchThePreviousTagInTheJobCheckout`, `TestReleaseMissingTagExplainsTheJobCheckoutAndRecovery` |
| npm uploads require matching `@sparkwing/sdk` 0.x archives, safe dist-tags, a dry run and provenance | `bin/publish-typescript.mjs` | `bin/typescript_publish_test.go`: `TestTypeScriptPublicationRequiresAnExactImmutablePackage`; `bin/typescript_release_test.go`: `TestTypeScriptReleaseRequiresBothNativeBridges` |

## Test and gate workflow

A lane test script exports its environment once and runs every command with
`-C`:

```bash
export GOWORK=off GOFLAGS=-buildvcs=false TMPDIR=/var/tmp/sw-lead-tmp \
  KUBEBUILDER_ASSETS=/var/tmp/sw-lead-tmp/vapreview/envtest/k8s/1.37.0-linux-amd64
W=/home/bombuntu/.xwing/envs/<env>/sparkwing
go -C $W build ./... && go -C $W vet ./...
go -C $W test -count=1 ./internal/store/ -run 'TestRefill'
SPARKWING_TEST_STORE=postgres \
  SPARKWING_TEST_PG_URL='postgres://sw:sw@127.0.0.1:55432/sw_<env>?sslmode=disable' \
  go -C $W test -count=1 ./internal/store/ -run 'TestRefill'
go -C $W run ./internal/commentcheck -base origin/main .
```

- PostgreSQL: `SPARKWING_TEST_STORE=postgres` switches every test that opens
  through `internal/store/internal/storetest` to PostgreSQL;
  `SPARKWING_TEST_PG_URL` names the server, and each test gets its own schema.
  Without the first variable the suite runs on SQLite.
- Isolated HOME: tests call `testhome.Set` (`internal/testhome`). Run any `sparkwing`
  binary with a private `HOME` and `SPARKWING_HOME`, never the real ones.
- `.golangci.yml` owns the depguard dependency rules, checked with the other
  linters against changes since `origin/main`.
- Comment-tag gate: `internal/commentcheck` allows GoDoc on exported API and
  body comments tagged `hack:`, `safety:`, `bug:` or `perf:` (at most 4 lines
  of 120 characters), and refuses ticket ids in comments. The gate job runs
  it with `-staged` or `-base`.
- `KUBEBUILDER_ASSETS` points at envtest binaries; without it the API-server
  admission tests skip rather than fail.
- Check tiers, budgets and what must not be skipped: `DELIVERY.md`.

## Known gotchas

- zsh: quote every glob meant for the tool. `grep --include=*.go` fails with
  "no matches found" before grep runs; write `--include='*.go'` or
  `rg -g '*.go'`. In `rg`, `-h` is `--help`; use `--no-filename`.
- `GOWORK=off` travels with every `go` command in a worktree; `go -C` alone
  still reads the parent checkout's `go.work`.
- A private `HOME` hides asdf installs, so `go`, `helm` and `node` shims fail
  ("unknown command: helm. Perhaps you have to reshim?"). Export
  `ASDF_DATA_DIR=/home/bombuntu/.asdf` and `ASDF_DIR` with the private HOME.
- deadcode offline: `@version` fetches fail with `GOPROXY=off`. Run the cached
  module through a scratch modfile that requires
  `golang.org/x/tools v0.49.0`:
  `GOPROXY=off go -C $W run -mod=mod -modfile=<scratch>/go.mod golang.org/x/tools/cmd/deadcode ./...`.
- Lint without `origin/main`: `.golangci.yml` judges only code new since
  `origin/main`, so a checkout that cannot resolve it reports the whole tree.
  Fetch the ref or ignore findings outside your diff.
- Codex sandboxes create empty `.git` mount points in writable roots, which
  breaks Go VCS stamping; keep `GOFLAGS=-buildvcs=false` and
  `TMPDIR=/var/tmp/sw-lead-tmp`.
- `go build ./cmd/sparkwing` collides with the `sparkwing/` SDK directory; use
  `go build -o /dev/null ./cmd/...`.

## Decision index (handoffs.md, decisions 26-72)

Earlier numbers and lead rulings sit above these in the same file. A later
decision amends an earlier one where noted.

- 26: Cloud billing is prepaid credits with optional automatic refill; usage is metered slice by slice at lease renewal and hard-stops at zero.
- 27: Auto-refill is opt-in; the team sets threshold, amount (minimum $10) and weekly/monthly limits; standing tiers cap refill size and frequency.
- 28: Structural simplification plan approved; feature freeze until its stop rule.
- 29: One active controller per database, held by a PostgreSQL advisory lock (fail-stop).
- 30: Kubernetes pools holding reserved secrets run one fresh pod per claim.
- 31: Admin-armed, unmodified default-branch schedules seal as verified.
- 32: $1,000 maximum per payment.
- 33: An early fraud warning on a 3-D Secure liability-shifted payment holds and freezes instead of refunding.
- 34: Children inherit verified identity and secrets only at the parent's exact commit; branch-pinned `source:` gets pipeline secrets.
- 35: Wave 2 proceeds; healthy growth is fine; a repeated review finding builds a single owner.
- 36: Member tokens keep their own expiry (90-day ceiling) independent of session timeout.
- 37: Run-level and job-level concurrency groups are separate namespaces; no run-level box scope; raw group API deleted.
- 38: A run that loses its run-level group waits to reacquire it.
- 39: Audit rows carry no personal data; retention by age.
- 40: Audit append-only guards against application code, not credential theft.
- 41: Webhook deliveries are never retried automatically; admins can process a failed one again.
- 42: A job's predicate runs inside its concurrency slot on the executing host.
- 43: One host-side masker (refusal rules amended by 49 and 57).
- 44: Runner sizes are labels; one SQL claim predicate by fair share then arrival; receipts for every delivery effect.
- 45: One durable admission owner in the store.
- 46: A restore may lose usage, never a payment (amended by 47).
- 47: Restore payment recovery is a manual runbook.
- 48: Masking is a host feature; code run without a host gets none.
- 49: Only the length rule refuses a secret; `***` is accepted.
- 50: Overbuilt mechanisms cut; argument, plan, secret and label validation moved from the SDKs to the host.
- 51: Approvals hold no concurrency slots.
- 52: Claim ranking and preferences are holds on the job, with a 5-second window.
- 53: SDK support packages stay internal, behind an explicit depguard list.
- 54: A restore signs no one out unless `--sign-out-everyone` is passed.
- 55: The `sparkwing-` label prefix belongs to Sparkwing Cloud.
- 56: Harsh defaults narrowed (App misconfiguration, storage brake, log persistence, node faults, repository pagination, child limit).
- 57: Secrets shorter than 4 characters are accepted but not masked, with a one-time warning.
- 58: Child-run limit defaults to 50, team-settable up to 1,000.
- 59: Child-run limit errors explain the fix (plan-time refusal dropped by 61).
- 60: Verified default-branch work runs only on fresh pods.
- 61: No plan-time child fan-out preflight.
- 62: Financial records outlive team deletion.
- 63: One one-way switch from test to live payments.
- 64: One deletion-only lane; growth is judged by reason; new internal packages over 500 lines need a paying deletion.
- 65: Stripe holds all card data.
- 66: Pipeline secrets are scoped to repository and pipeline.
- 67: Pre-release cleanups and a list of ratified lead calls.
- 68: Pipeline secrets go only to proven-repository runs; non-fresh hosts write only their own cache scope unless the team turns on high trust; a Cloud node serves one run; storage tiers apply automatically.
- 69: A Cloud pod may reach its own node's kubelet (deleted by 71).
- 70: OIDC carries `runner_environment` (fresh or persistent), the repository for fetched runs, and `trigger:actions`.
- 71: No runtime network probes; enforcing NetworkPolicy is the operator's job.
- 72: Fetched runs carry their ref and sha in OIDC.
- 73: Teams get 1,000 secrets and 50 schedules; keep OIDC at 30 per five minutes; remove lifetime team-creation caps; paginate unbounded lists; allow 20 Checkout Sessions per team per hour.

- 2026-10-30: One sealed repository proof and host classification supersede consumer-specific trust rules.
- 2026-10-31: Compare outages admit asserted App runs. Trust restrictions and unscoped personal enrollment are ratified. v0.68.3 resets stores to schema 28. The final production wipe follows the release cut and a clean staging grid.
- 74: Teams have no member cap, regardless of funding or payment holds; remove the setting, invitation warning and acceptance refusal.
