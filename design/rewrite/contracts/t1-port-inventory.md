## W1 store review fixes

The token-table mint and mutable node/trigger billing tests retire with their APIs.
Current claims, immutable billing awards, refunds and financial retention remain
covered through their real-store suites. `TestComputeUsageFollowsLiveBillingAwards`
tracks a metered claim through release. The retired API and schema checks reject
the removed public writers, credit columns and storage tables.

`TestUnitClaimRenewReleaseCapacityContentionProperty` generates capacity boundaries
and races claims and renewal/release across two store handles in SQLite and
PostgreSQL. `TestUnitOperationsShareControllerFence` holds a PostgreSQL shared
fence open across real claim, renewal, release and reaping calls. Singleton release
has a PostgreSQL contention check. Clock skew has a PostgreSQL database-time test.
These PostgreSQL branches skip without `SPARKWING_TEST_PG_URL`.

The claim, release and renewal negative fixtures check their refusal type and
reason. Revocation goes through the committing API rather than corrupting the
capability projection. Sealed credential properties generate values, exercise
actual encryption, refuse plaintext and foreign sealed writes, and preserve
row binding through a copied accessor. Reap failure and run-counter refusal
checks preserve committed work and transaction rollback respectively.

## W1 tenant event and compute-control continuation

The concurrent append tests now select their tenant. The generated
`TestTenantEventAppendIsolationProperty` refuses foreign selectors without
changing their counters, preserves event order and uses the store clock.
`TestTenantEventAppendFailureRollsBackSequenceAndBytes` refuses an event insert
and verifies that the next successful append still receives sequence one.
Compute-limit validation, zero removal and atomic batch rollback use Operator.
The duplicate queue-count assertions and unused compute-alarm test retire with
their deleted APIs. The retained queue partition property and StoreQueueOrder
cell use the shared state projection. The cadence fixture supplies its resolved
numeric GitHub identity before testing the operator interval guard.

`Tenant.FinishRun` and `finishRunOnceTx` are removed. The retained timestamp

## W1 raw run writer continuation

Controller and web reporting fixtures migrate from `Tenant.CreateRun` to
`teststore.SeedRun`. `TestRunReportingSeedUsesSealedSchemaWithoutExecutableWork`
checks current-schema reads, masked unclassified arguments, duplicate refusal,
unregistered tenant refusal and absence of owner, job and credential rows.
`TestOperatorActiveRunnersUseStoreClockAndWindow` retires with the unused
lease-window gauge. `TestRunnerInventorySeparatesRenewalLivenessFromHeadroom`
and the runner inventory properties retain current observation liveness.
Profile aggregation and evidence-coverage cases use the final `memoized`
outcome. Both suites pass through real SQLite, including memoized jobs beside
measured jobs. No native matrix cell is newly claimed.

checks in `TestATerminalRunAlwaysCarriesAFinishTime` now drive real admission,
plan acceptance, claims and `FinishRunAndUnit` for success, failure and
cancellation. Exact replay preserves the full terminal snapshot.
`TestAPendingRunCarriesNoFinishTime` reads the admitted final-schema row.
`TestTenantWritesReportNotFoundForAnotherTeamsRun` now refuses foreign and
missing owner completions. `TestRunCompletionAuthorityProperty` retains forged
owner, tenant and generation refusal. Its mutation snapshot now counts business
events too. The rollback check covers failures in both event tables.

`TestBusinessEventFirstRunSucceededOncePerTeamSQLite` and its PostgreSQL variant
now finish a cancelled run followed by two successful retry runs through the
real lifecycle. The team gets one first-success event naming a successful run.
The PostgreSQL case still requires native execution. Legacy creation/upsert
checks that supply their own terminal timestamp or construct a finished pending
row target abandoned insertion semantics and are retired. Final schema
constraints reject that shape. The remaining raw intake caller cut stays open.
Unrelated reporting, concurrency and transport fixtures use `SeedRunFinish`.
That fixture sets three columns and implements no lifecycle or replay policy.

The three `concurrency_heartbeat_interval_test.go` checks are retired with the
unused timing API. The shared `internal/lease` keeper owns cadence and bounded
attempts. The profile-key round-trip, bare-shape, pre-encoding and Windows URL
checks are retired with the unused codec and duplicate identity helpers. No
production writer calls them. The profile view displays its stored pipeline key.
Current profile tenant-isolation and mutation properties remain.

`TestFinishAtGeneration_ConcurrentReclaimKeepsTheCurrentOutcome` now runs as
`TestOwnerFinishRaceRejectsStaleAwardProperty` through real `ClaimUnit`,
`ReleaseUnit` and `FinishRunAndUnit` transactions. It varies the initial
generation and races stale failure against current success. Same-verdict stale
replay and release retain the completed snapshot. The two old team-generation
tests are retired with their API. `TestRunCompletionAuthorityProperty` retains
foreign-tenant and forged-owner refusal against the final schema. The colliding
trigger fixture is invalid under the final composite run foreign key. Existing
completion rollback and concurrent-winner tests remain. Native PostgreSQL
evidence is separate.

The old release-generation test now uses `TestReleaseUnitOwnerFenceProperty`,
which generates 192 forged trigger, plan and eval releases without changing
their unit rows, run snapshot or billing. `TestReleaseUnitReturnsUnstartedWorkWithoutAttempt`
retains successful release and second-release refusal. The stale finish billing
assertion uses `TestMeteredUnitStaleReportCannotSettleReplacementProperty` and
`TestOwnerUnstartedRecoveryFencesPriorAwardsProperty`. The test for a leftover
mutable trigger credit window is retired because final awards have immutable,
generation-bound billing records. No raw release can leave that window behind.
The two here tests that expected only one claim are removed because A1-55 allows
reclaim. `TestClaimUnitHereWindowAndReclaim` retains the initial window,
requester authority, reservation and expiry checks. The failed-trigger test
becomes `TestFailedRunClosesOwnerAndKeepsVerdict`: its run keeps the failure and
masked error, its queue owner closes, replay is exact and success cannot replace
the committed failure. Old foreign-tenant generation assertions move to the
current completion and release properties.

The raw cancellation tests now run through `TransitionRun`, `ClaimUnit`,
`RenewUnit` and `ReapUnits`. `TestRunCancellationPreventsUnclaimedAwards`
retains unclaimed trigger/plan cancellation and reaper replay.
`TestRunCancellationReachesClaimedOwner` retains cooperative cancellation
through both trigger and exchanged plan authority.
`TestRunCancellationRacesOwnerAwardProperty` ports cancel-versus-claim races.
`TestRunCancellationCannotOverwriteTerminalProperty` replaces
`TestCancellingATriggerDoesNotOverwriteARunThatAlreadyEnded`.
`TestRunCancellationRequiresCurrentTeamAuthorityProperty` checks tenant,
membership, role, session, expiry and revocation refusals without state,
event or effect writes. Existing holder-result and cancellation-intent
properties remain. Legacy transport tests seed cancellation state because
they test response to that state rather than its mutation authority.

The unused in-memory `FilterRuns` selector is removed. Its parallel assertions
are deleted while the SQL listing, counting, case, literal and cursor checks
remain. Those legacy SQL fixtures still need their final-schema migration.

The legacy claim-token heartbeat/start cell
`TestClaimRun_PodRoutesServeOnlyTheirOwnClaim` is retired with those store
methods and HTTP branches. Its retained requirements run in
`TestRenewUnitRecordsOwnerExecutionForRecovery`,
`TestRenewUnitRefusesStaleAndRootAuthority`,
`TestRenewUnitDeliversCancellationWithoutStarting`,
`TestRenewUnitNodeRequiresCommittedStart`, `TestTransitionHolderRights`,
`TestSourceReleaseRefusesChangedAuthorityBeforeReleasing` and the final
engine lifecycle HTTP tests. `TestRenewUnitRequiresCurrentAwardProperty`
generates 384 invalid root, unknown-prefix, ancestor-revocation, lease-expiry,
generation and holder cases, retaining the lease after every refusal.
The old source-mint-spent assertion described a retired counter and is removed.
Three final transition HTTP subcases still fail strict request decoding against
the session baseline as well. The continuation report records those failures.

The three raw git-credential mutation APIs have no production callers.
Their maintenance and HTTP fixtures now use the current mutation transactions
or the package-private transaction writer when seeding a rotation fixture.
The existing confirmation, replacement, deletion, tenant-isolation and
credential-mutation properties retain the behavior. No W1b fixture is changed.

The two resealing tests and startup plaintext-conversion test are retired
because the final pre-launch schema has no historical backfill. The sealed
sample test moves unchanged into `secrets_sample_test.go`. Keyless access is
checked directly at its cipher boundary. HTTP key-refusal and encryption-failure
properties retain transport status, private errors and no-persistence checks.

Store recovery continuation moves `store_started.go`, `store_unstarted.go`,
`store_retry.go`, `store_capacity.go`, `store_capacity_maintenance.go`,
`store_orphans.go` and the cancellation/reconciliation checks in
`store_recovery.go` into ordinary `pkg/store` tests. Their final test names are
`TestStoreRecoveryStartedLapseCap`, `TestStoreRecoveryUnstartedLapse`,
`TestStoreRecoveryApplicationRetry`, `TestStoreRecoveryCapacityAdoption`,
`TestStoreRecoveryCapacityMaintenance`, `TestStoreRecoveryLocalOrphans`,
`TestStoreRecoveryCancellation` and `TestStoreRecoveryReconciliation`.
They seed through storetest, current sign-in, issuance, intake and accepted plans.
No moved test contains a hand-written INSERT. Competing capacity uses a second
admitted host-owned run. Recovery projection updates simulate expiry; they do
not claim real transport, process-death or native-placement evidence. Their old
SQLite wrappers are removed. Host-event and billing consumers still share the
remaining harness fixture.

The six legacy git-credential tests are replaced by
`TestSourceGitCredentialConfirmationAndReplacement` and
`TestSourceGitCredentialHTTPSDeletionAndTenant`, plus the retained
`TestSourceConsentRequiresCurrentMachineOwner`,
`TestSourceConsentCannotChangeAnotherOwnersMachine`,
`TestSourceReleaseRefusesChangedAuthorityBeforeReleasing` and
`TestSourceReleaseCannotWidenPersistedFacts`. These retain confirmation replay,
rotation under the same fingerprint, changed-fingerprint refusal, HTTPS automatic
confirmation, deletion, tenant isolation, owner consent, ancestry, lease and audit
rollback through capability transactions. The source-mint counter and its two
controller tests retire with the unused counter implementation. Claim ancestry
and source liveness remain in the final generated store tests. This does not
establish GitHub App token-provider integration.


Store continuation moves three more harness files into `pkg/store` tests.
`store_actions_enrollment.go` becomes
`acceptance_actions_enrollment_test.go:TestStoreActionsEnrollment`.
`store_enrollment.go` becomes
`acceptance_enrollment_test.go:TestStoreEnrolledClaims`.
`store_launcher_sync.go` becomes
`acceptance_launcher_sync_test.go:TestStoreLauncherSync`.
They open through storetest and use sign-in, issuance, admission, acceptance and
release transactions to seed their rows. They retain reopening, enrollment
revocation, granted identity, award exchange and replacement-generation cleanup.
The placement wrappers are removed. Native placement scenarios remain pending.

The retired `internal/runretry.Create`, `Store.CreateRetryWithRun`,
`Tenant.SetRetriedAs` and `InheritSecretArgs` have no production callers.
`TestManualRetryPreservesOnlyItsTeamsSealedSourceProperty` ports tenant ownership,
source selection and secret classification through real admission. It generates
twelve pinned sources, rejects foreign tenants and forged team selectors, and
checks the complete retained request seal. `TestManualRetryRefusesMissingSource`
retains missing-source refusal. `TestManualRetryRetainsVerifiedRepositoryFacts`
retains verified App repository identity and proves the retry does not repeat a
webhook delivery. The prior reverse-link and invocation-copy tests are removed
because the sealed lineage and classification replace those representations.
`TestPipelineRefDispatchRecreatesSourceAfterCleanup` keeps the dispatch-side
source reacquisition check with its recorded inputs supplied directly. That
runtime check remains unverified in the socket-restricted sandbox.

`TestTerminalRecordSelectorsProperty` generates optional output and manifest keys
across reports, memo hits, manual retries and coalesced results. It checks the
committed selectors in reconciliation, replay, ReadNode and ReadSnapshot even
after event removal. Foreign tenants cannot read them. Attachment/copy failure
and event-write failure leave no selectors. Record authorization and byte
verification remain the record implementation's separate tests.

The final unit-billing suite in `pkg/store/unit_billing_test.go` ports the
unfunded-claim, last-reservation race, unstarted-refund and consumed-minimum
assertions from the retained `pkg/store/credits_test.go` tests. It uses final
ClaimUnit, TransitionNode, RenewUnit, ReleaseUnit, FinishRunAndUnit and ReapUnits
with W1b's real ledger helpers and a C2 calculator fake. Two properties cover
stale reports against replacement awards and expiry during issuance. The new
`TestBilling/billing-start-award-versus-start/store-sqlite` cell proves award,
exchange, price and compute-start persistence through reopening. Native billing
and provider cells remain pending. No additional archived source identity below
is claimed closed by these retained-suite ports.

The retained `pkg/store/crons_test.go` suite now uses Tenant for schedule
operations and Operator for tick metadata. Fired history fixtures seed the final
same-team run reference. Non-run entries store NULL. Existing cursor, override,
pause, retention, deduplication and tenant assertions remain active. The SQLite
`surface_test.go:TestSurface/surface-schedules-and-managed-timers/store-sqlite`
cell adds rollback and reopening evidence. Native timer and HTTP cells remain
pending. This adds no archived source-function port identity.

# Acceptance test port inventory

W1's persisted-enrollment continuation ports active-name uniqueness, team scope,
reuse after revocation or expiry, rotation grace and concurrent mint limits to
`TestMachineEnrollmentUsesStoreOrigin`, `TestMachineEnrollmentNameReuseProperty`,
`TestMachineEnrollmentNamesAreTeamScoped` and
`TestMachineEnrollmentSerializesNamesAndLimits`. These retain the concerns from
`TestRunnerTokenRefusesADuplicateAgentName`, `TestAgentNameIsUniqueOnEveryMintPath`
and `TestIdentityRunnerTokenCapHoldsUnderConcurrentMints`. Their superseded
raw-token tests are removed with the unused team token constructors and inventory.
The final machine origin requires current team administration, as C2 specifies.

`TestProvisionMemberCurrentScopesProperty` ports current-role, requested-subset
and removal assertions from `TestMemberTokenScopesAreBoundedByTheMintersRole`,
`TestMemberTokenMintUsesCommittedMembershipAuthority` and the member portion of
`TestIdentityTeamTokensNeverCarryAdmin`. It generates grant subsets, roles and
missing memberships, checks that rejected requests persist no credential and
preserves the supplied grant slice. The actual HTTP transaction race remains
covered by `TestCredentialIssuanceRechecksMembershipInTransaction`.

`TestMachineEnrollmentAuthorityProperty` retains the minter recheck from
`TestRunnerTokenMintRechecksTheMintersRole` and the machine-scope refusals.
`TestCapabilityInventoryScopesAndMetadata`, `OwnershipProperty` and
`FiltersAndRefusals` retain own-credential selection, foreign-team exclusion
and execution-credential hiding from the removed TeamTokens and TeamToken tests.
`TestCapabilityMintWaitsOnAnUncommittedMembershipDrop` replaces the raw mint race
with member and machine `ProvisionCapability` requests. It configures the sealed
session key and uses a real account session; PostgreSQL execution remains pending.

The raw family/prefix tests, legacy cross-kind counting and legacy-name-index
assertions retire with their unused APIs. Current registered machine names,
caps, rotation and reuse use the enrollment tests named above. This cut adds no
member-root quota to C2. Sliding expiry and member-removal raw-token fixtures
remain until the remaining token authentication and identity paths migrate.

`TestMachineEnrollmentAuthorityProperty` generates invalid registration, role,
grant, repository, pool, data and name inputs even with a permissive extra policy.
`TestEnrolledClaimRepositoryAncestryProperty` varies direct and delegated claims
against allowed and foreign repositories. `CheckStoreEnrolledClaims` extends
`placement-granted-identity-and-capacity/store-sqlite` with default store enrollment,
profile reopening, filtered claims and persisted award ancestry. Its run and owner
rows remain fixtures. The HTTP root proofs now use the production enrollment
transaction. Bootstrap origin, provider proof, host observations and native
placements remain separate integrations. No native cell is newly closed.
The [W3 unused executor cut](../w3-legacy-executor-cut-progress.md) removes the
uncalled RunNodeOnce path and ports its blocked credential-release cancellation
to source.PrepareCheckout. Repository refusal checks retain the live trigger
policy. Native placement composition and live legacy hosting deletion remain
incomplete. No acceptance cell is added or activated.

The [W3 package-test cleanup](../w3-harness-cleanup-progress.md) moves the retained
host checks into `internal/nodehost` and `internal/lease` test files, consolidates
their fakes and removes duplicate runners. Lifecycle, step and foreign-award
checks use tenant intake, acceptance and claims. No acceptance cell is added or
activated. Production placement migration remains incomplete.

The [W3 nodehost green continuation](../w3-nodehost-green-progress.md) ports
`TestOnlyRunsTheMatchedNodesAndWhatTheyNeed` to tenant intake, claims, accepted
seals and the shared scheduler. The [W3 merge and selection continuation](../w3-merge-step-window-progress.md)
now drives those exclusions from sealed Only inputs through final-store
transitions. Sealed StartAt and StopAt inputs reach the shared executor, with
native Go SDK process evidence. No acceptance cell is added or activated.

The [W3 owner assembly continuation](w3-owner-assembly-continuation.md) replaces
the removed pre-intake stalled-describe assertion with native planning under a
live owner. Failure finishes the owner without accepting topology and joins its
SDK child. The native assembly fixture now enters `OwnerHost.Run` and finishes
after self-claimed execution. No acceptance cell is added or activated.

## W1b raw cancellation writer retirement

`TestMeteredUnpricedClaimLeavesWorkUnchanged` replaces
`TestFailNodeForUnpricedClassEndsTheNodeWithAnEvent`. Named and queued claims on
runner and Cloud paths refuse an unpriced resource request without changing the
job, balance, attempts, events or billing rows. The uncalled cancellation writer
and its separate event kind are removed. Final terminal changes require the
state machine's current actor and attempt fence.

`TestPaymentDeclineHoldsPrepaidClaimsUntilCollectionSucceeds` replaces
`TestADeclineHoldsATeamWithPrepaidBalanceAtNew`. It submits trusted failure and
success facts through the provider settlement owner, binds the retry through
RecordAttemptIntent, and exercises the real ClaimUnit refusal while the team has
prepaid credit. The hold lowers billing trust until collection succeeds. Event
receipts commit with the outcomes. Provider verification and final payment schema
remain contract fixtures.

`TestMeteredFailureReportSettlesAndReleasesClaimProperty` replaces
`TestCancelNodeForExhaustedCreditsFailsAndReleasesTheNode`,
`TestCancelNodeForComputeLimitFailsTheNodeAndReleasesTheClaim` and
`TestStaleCancellationSettlesAtTheExecutionStart`. It exercises real capability
issuance, terminal transitions and settlement for runner and Cloud claims,
before and after execution starts. It checks persisted failure reasons, claim
release, closed compute intervals, nonnegative settlement, exact replay and
refusal of changed replay. Job and attempt finish times use the store clock
and cannot precede execution. Unstarted Cloud claims refund their reservation.
This ports result persistence and settlement. Renewal-time exhaustion and grace
remain separate integration requirements.

W2 runner-observation ports: the five tests in agents_test.go move to
TestRunnerInventoryHTTPUsesCommittedSnapshot,
TestRunnerInventoryHTTPNameProperty, TestRunnerInventoryHTTPTenantProperty,
TestRunnerInventoryHTTPRequiresMemberCredential and
TestRunnerInventoryHTTPRefusesMissingAndCorruptFacts. They retain empty arrays,
private storage-error refusal, distinct authority behind equal names, activity,
liveness and separate headroom observation time through the actual authenticated
HTTP handler. The inventory reader is a fake. Durable poll/renewal correlation,
revocation, replacement and restarting the controller remain W1 integration
obligations. The old newest-holder-wins display rule retires with holder parsing.

TestBusyRunnerHeartbeatUpdatesObservedLivenessWithoutPolling is replaced at the
HTTP projection boundary by the separate LastSeen/headroom snapshot assertions.
The actual renewal already runs in TestEngineJobLifecycleRenewsAndReplaysUnderStoreFences.
W1 still needs to project that accepted renewal into ReadRunners and preserve
liveness on refused renewal. A fake snapshot does not close that persistence
obligation. The heartbeat-cache pruning test retires with that removed cache.

TestClaimAttentionNamesWhyARunWaits and
TestClaimAttentionPassWrapsDespiteNewNodes now run against the final SQLite
capabilities, execution seals and node-attention projection. Their old token-table
and HTTP-listener fixtures are deleted. Cloud matching remains covered by
TestClaimAttentionCountsSparkwingCloud. The platform-change registry test retires
with that cache. Current enrolled platform/pool/repository/capacity preservation
runs in TestClaimAttentionPreservesEnrollmentWhenARunnerIsOnline.
TestClaimAttentionCommittedLivenessProperty generates past/future observations
and checks the existing two-minute boundary. TestEngineClaimHTTPCommitsRunnerObservations
proves accepted claim polls persist C1's filtered labels and store time.

The runner-presence credential-carrying test retires with that registry.
TestQueueStateShowsOnlyTheCallersTeamsRunners now consumes the same final
snapshot interface. QueueRead HTTP acceptance tests keep their existing policy,
concurrency-group and error assertions with a runner-snapshot fake.

The two auth_claim_test.go tests move to TestEngineClaimHTTPRefusalsDoNotAward,
TestEngineClaimHTTPDeadCredentialProperty and the final claim-kind fuzz test.
They check anonymous/unknown/legacy/revoked credentials, root-versus-claim grants,
canonical 401/403 and zero persisted awards on refusal. The old TokenDeadError
classification and admin-only readiness route retire with that legacy protocol.
These ports complete no native credential-matrix cell.


W2 signing/fixture cleanup ports: tokens_test.go's six scope validation tests move
to TestCredentialIssuanceValidatesEveryGrant and RefusedGrantProperty. They exercise
the final issuer with real SQLite, all declared grants, empty grants and generated
unknown grants. Invalid/padded/duplicate grants refuse without creating a row.
The source-scanning Scope* allowlist test retires with the old constants.

The browser-session and page fixtures now use direct account sessions, row-bound
CSRF protection and capability issuance. Session-header writes require CSRF, API
refusals preserve cookies, and nonlocal pages require sign-in before bootstrap.
OperatorPageServesOnlyTheOperatorsSession covers the configured gate and refuses
a capability-parented session. The local-directory deletion port uses a fake
committing TeamWriter. It proves post-commit directory handling and exact authority
forwarding, not production deletion atomicity.

The four chunked_body_test.go regressions move to EngineClaimHTTPChunkedBody and
CapabilityRotationChunkedBodyPreservesGrace. Final claims keep decoded kinds and
refuse removed pipeline/lease selectors. Empty claim bodies now refuse.

The three execution_location_test.go tests retire with principal/holder-string
inference. EngineClaimUsesEnrollmentAndChecksRunnerPolicy and
EngineRunDetailReadsPersistedHistoryAndTaggedLogs retain granted runner identity
and attempt generation/history through the final projections.

Duplicate controller signer tests retire with their implementations. W7's shared
backend tests and TestDataStorageSignedFilesystemRoutes retain transfer/signature
proofs. CloudFrontDataBackendSignedObjectProperty checks generated exact-object
policies, expiry and RSA signatures. DataReadHTTPChoosesCloudFrontOnlyForIngress
checks the actual HTTP handoff and charge-before-sign order with fake data facts.
RequestPrincipalMetricLabelsProperty bounds labels to seven constants, and
RequestPrincipalMetricsCountsAuthenticatedPolicy checks counting through HTTP.
The old token-cache/argon2 test retires with the removed cache. W1's retained
legacy hashing collector is not asserted as final authentication behavior.
These are component ports. No native credential-matrix cell is newly completed.


W2 seeded-bootstrap and login ports: bootstrap_token_test.go no longer calls
the removed token-table startup path. Its forwarding property checks the exact
seed, fixed operational authority, issuer result and missing-adapter refusal.
The old no-window, populated-table, malformed and empty-input cases split
between those component tests and W1's pending once-only issuance transaction.
No fake gate is counted as a durable race/restart proof.

The three multi_team_auth_test.go functions and four auth_visibility_test.go
functions move to TestEnableAuthFromStoreRefusesAnonymousBeforeBootstrapProperty.
It runs without sockets against the actual final and legacy muxes with real
SQLite. Single-team and both license-installation orders retain anonymous
refusal, and health reports authentication enabled before seeding. The old
single-team-open expectation retires with that removed startup mode. Late
legacy-token acceptance retires with token-table startup provisioning.
TestCheckRequireAuthUsesCapabilityRead ports the command's readiness check
through the named read interface, including absence and backend errors.
Production administrator-read integration remains W1 work.

The two deleted files session_hash_test.go and session_lifetime_test.go held
TestLogin_StoresOnlyTheSessionDigest and
TestLoginSessionStartsWithSevenDaysOfValidity. Their assertions now run in
TestLoginHTTPAccountCapabilitySession against actual account-capability login,
real SQLite and the production handler. The final session is stored by digest,
not raw presentation, the schema has no plaintext csrf_token column, and the
fresh expiry is seven days. Operator bootstrap credentials cannot sign in.

TestPaymentHTTPFinancialProviderHandoffProperty exercises actual signed HTTP
refund, failed-payment and dispute handoffs in both modes, generated metadata
and forged signatures. Existing HTTP refusals retain private ledger errors.
TestPaymentProviderOutcomes covers card setup, paid-card and warning enrichment
with a fake provider transport. These are verifier/HTTP component proofs.
W1b's existing card/refund/warning/dispute tests still need their final
SettlePayment ports and real ledger integration. No native matrix cell is
newly unskipped.


W2 execution credential retirement removes the seven tests in
claim_route_internal_test.go with that router. Their principal-with-no-scopes
expectation is replaced by the final credential/grant policy. The existing
TestCredentialHTTPAuthenticationBoundary, TestCredentialHTTPTerminalGrace,
TestCredentialHTTPCancelDrainsReporting and TestCredentialPolicyAlternatives
retain wrong-kind, binding, ended-authority, replay and cancellation behavior
against the final snapshot interface. Final engine and store properties retain
generation and result fencing. These are interface/component proofs, not a
new native claim or log-tail cell.

TestEngineRetiredExecutionRoutesProperty reaches the actual API and browser
handlers with generated run/job IDs, old and malformed bearers, session headers
and cookies. Retired paths return canonical 404 and the raw event method returns
canonical 405 before session lookup. No refusal changes cookies.
TestNodeStoreErrorReportsTransientPostgresContention moves to
TestEngineTransactionContentionProperty through the final transition route.
It preserves 503 and one-second retry advice for all three PostgreSQL contention
states, safe 500 for other database faults, and private error details through
direct, wrapped and joined errors. It uses fake committing transactions.

TestControllerEventsUseTenantAndReplica retires with raw event append. Events
are emitted by committing state transitions. The retained store event accounting
suite checks tenant/replica charges, rollback and reopening. Final engine-to-event
accounting composition remains an integration requirement. The legacy node
projection test retires with that unused projection. EngineJobsHTTP retains
the public job shape and private-field exclusion. Black-box legacy runtime
suites outside these deleted files still need their final caller/fixture ports.

The four tests in local_execution_attempt_test.go and the local-mode budget
and poll-advice tests retire with Server.WithLocalExecution. A local request
cannot submit a caller-selected unfenced execution attempt. The final transition
and claim-input constructors derive executor identity and generation from the
award. TestEngineRetiredExecutionRoutesProperty refuses the old execution-start
and execution-finish routes. The existing LocalControllerHTTP and EngineLifecycleHTTP
components retain verified-peer selection and final transaction authority. Native
local self-claim execution remains pending with W1/W3/W8/W9 composition.

W2 browser-link ports: the deleted
TestLinkRefusesAStateFromAnotherAccountSessionOrAReplay,
TestLinkCompletionNeedsARecentSignIn, TestLinkAttemptsAreRateLimitedPerAccount
and TestLinkStartRefusesAProviderAlreadyLinked move to the IdentityLinkHTTP
tests in tests/acceptance/identity_link_http_test.go. They exercise the final
start/callback/completion handlers with real SQLite sessions, the actual GitHub
provider client and fake committing nonce/identity adapters. Generated checks
cover account/session selection, ambiguous cookies, form CSRF, state and PKCE
mutation, and arbitrary provider errors. Real durable nonce races and identity
attachment remain C1 integrations. The surviving legacy link/unlink tests still
carry their separate persistence obligations.

TestLoginHTTPOpenController is replaced by
TestLoginHTTPNonlocalRequiresSession. Nonlocal page access and the login page
follow browser-session proof independently of the legacy token authenticator.
The local-mode exception is selected through Dashboard.Local.

TestLocalControllerHTTPUsesSharedPolicy exercises Server.HTTP through Wingd's
HTTP serving/drain code over net.Pipe. Generated requests check that explicit
credentials cannot fall back to verified peer identity. Retired paths reach no
legacy handler. This uses fake committed peer facts and does not complete a
native api.sock or placement cell.

W2 account mutation ports: the deleted `TestActiveTeamSwitchNeedsMembership`
and `TestIdentityRoutesRefuseABearerToken` use
`TestAccountMutationHTTPIdentityProperty`,
`TestAccountMutationHTTPAuthorityRefusals` and
`TestAccountMutationHTTPTransactionRefusals`. They retain account-only policy,
normalization and nonmember/missing-team refusal through the final HTTP boundary.
The identity and CSRF properties use real SQLite session authentication and fake
account transactions. Successful atomic team/session changes and the sticky
selection on a later sign-in remain C1 integration obligations. Existing C1
identity tests retain those persistence requirements. No native matrix cell
is completed by these component ports.


W2 browser-login ports: the ten tests in the deleted 271-line
pkg/controller/browser_login_test.go move to TestLoginHTTPAccountCapabilitySession,
TestLoginHTTPProofProperty, TestLoginHTTPPageAndMethods,
TestLoginHTTPRefusesInvalidCredentialAndForm, TestLoginHTTPSafeReturnPath and
TestLoginHTTPNonlocalRequiresSession in tests/acceptance/login_http_test.go. These cover
cookies, form proof, invalid credentials, safe next, stale sessions, replacement,
nonlocal page authentication, script escaping and audit attribution. The old admin-only
form rule is replaced by C1's unbound account-capability contract. Machine,
service, deployment and executable credentials still cannot open a session.
The logout audit half retains the prior active browser-logout HTTP port.
The local-mode caller in browser_pages_test.go no longer depends on the
removed test helper. These are component proofs, not native matrix evidence.


W2 route-cut ports: TestCredits_SetMeteredRouteMarksAnExistingToken moves to
TestRunnerMeteringHTTPConsoleProperty and TestRunnerMeteringHTTPRefusals.
The old admin-bearer expectation is reversed to console-only authority.
The marker reaches a fake authority-bearing transaction. Real root metering
and immutable-award provenance remain W1 integration obligations.
TestGitHubRunnerBindingsAreOwnerOnlyAndUnbindingRevokes moves to
TestGitHubRunnerAdminHTTPAuthorityProperty, TestGitHubRunnerAdminHTTPRefusals,
TestGitHubRunnerAdminHTTPInputAndMethods and
TestGitHubRunnerAdminHTTPEmptyStoreAndMissingWriter. These prove final HTTP
policy, tenant selection, public responses and missing-writer refusal.
Atomic revocation of real Actions roots remains pending with W1.

TestPaymentHTTPServiceRoutesRemoved replaces the old credits.grant route
allowlist, anonymous-service-route and service-seeding assertions with removal
of the entire credential path. TestPaymentHTTPAuthenticationBoundary and
TestPaymentHTTPRefusals retain signed checkout and private-error checks.
The deleted credit_reversals_test.go and six removed card_billing_test.go
transport tests still require their final provider-event ports. Existing
pkg/store tests retain partial reversal, dispute/payment identity, saved-card
ordering, warned-card repayment, duplicate-payment refund and failed-refund
retry requirements. None is counted as a completed provider-HTTP port here.
The surviving owner-budget regression stays in card_billing_test.go. Source
functions remain available in the parent revision. No native matrix cell is
completed by these component tests.

W2 OAuth ports: TestOAuthCallbackRefusesAFlowThisBrowserDidNotStart moves to
TestOAuthHTTPProofProperty, TestOAuthHTTPPKCEProperty,
TestOAuthHTTPSessionAndReplay and TestOAuthHTTPDeclineAndSafeNext in
tests/acceptance/oauth_http_test.go. They retain missing flow, wrong state,
wrong provider, replay and consent refusal and add ambiguous selectors,
credential privacy and real session authentication. TestOAuthStartRefusesAnUnknownProvider
moves to TestOAuthHTTPMethodsAndUnknownProvider. TestOAuthSignInAuditNamesTheAccount
moves to TestOAuthHTTPSessionAndReplay. The 97-line old browser_oauth_test.go
is deleted. The provider transport is fake. SQLite identity/session creation,
PKCE client requests and the registered controller boundary are real. These
three ports establish component behavior and complete no native matrix cell.

W2 root provisioning ports: `TestMintingAMachineTokenRequiresValidRepositories`
is replaced by `TestRootProvisioningHTTPInput` and the scope property. It
retains host/path and shell-input refusals and adds server-derived origin,
CSRF, dead-authority and transaction checks. The old test is deleted.
`TestMintedMachineCommandCarriesTheRepositoryAllowlist`,
`TestTheConnectCommandSplitsAfterItsTokenSetup`,
`TestRunnerConnectArgsNamesTheControllerAndRepos`,
`TestRunnerConnectArgsRunsTheLoopsTheScopesAllow`,
`TestRunnerTokenSetupReplacesAnOpenTokenFileWithAPrivateOne` and
`TestTheConnectCommandStopsWhenItCannotReplaceTheTokenFile` move to the
`runnerConnectCommand` suite in web/src/lib/teams.test.ts, where the final
command is produced. Shell tests execute that command with a fake runner and
assert repository arguments, a private replacement file, no bearer in runner
argv, and no launch after setup failure. All seven old regressions are deleted.
This proves component behavior, not a native credential-matrix cell.

The raw `RequeueUnstartedClaim` suite and its controller/orchestrator callers are
retired. `TestOwnerUnstartedRecoveryFencesPriorAwardsProperty` varies initial
generations, pending/running runs, runner/launcher owners and repeated recovery.
It retains claimant clearing, no execution/disruption consumption, no repeated
refund, stale renewal/release/finish refusal and terminal absorption. Existing
owner-reaper and cancellation tests retain started-local failure, remote recovery,
cancellation precedence, team scope and the elected fence. The impossible
trigger-without-run fixture is removed because final owner rows require a run.
C2's at-award compute keeps its charge. Unstarted pod reservations refund.
The old metered tests that refunded already-started host compute are removed
with their raw API. The final unit-billing suite retains immutable prices,
settlement and generation-specific reservations.

`recovery-unstarted-lapse/store-sqlite` now also uses real intake and claiming
for a trigger owner. It proves three unstarted requeues and reclaims through
SQLite reopening, stable intake identity, stale plan/renewal refusal and final
plan acceptance by the replacement. Intake and enrollment remain contract fakes.
The removed legacy HTTP tests do not count as final HTTP integration evidence.
No additional archived source-function identity is claimed closed by this port.

`TestPlacement/placement-ordered-matching/store-sqlite` adds final-store evidence
for scoped trigger order, job requirements and soft preferences. The claim queue
property generates 32 selectors and checks that failed polls change no row or
event. Push properties vary repository/ref/SHA across named and queued node and
trigger claims, delegated credentials and awarded hosts. Direct issuance also
refuses an out-of-scope award. The old offer-window, idle-registry and concurrent
finalization tests are deleted with the warm dispatcher. Their matching,
tenant-scope and no-fallback concerns move to the final claim proofs. Enrollment
remains a fake and native Actions/launcher transport is still required.

`pkg/store/concurrency_maintenance_fence_test.go` ports expired-holder promotion,
aged-waiter liveness, abandoned-work cleanup and repeated-pass assertions from the
old maintenance suite. The fence property generates forged names, holders and
generations. Rollback tests cover statement failure and lease expiry during the
pass. The SQLite `recovery-concurrency-no-revival/store-sqlite` cell also checks
reopening and returning-owner queue order. Obsolete concurrency-cache sweeps are
removed because blob records own memo retention. Missing-run capacity is prevented
by cascading run foreign keys. The isolated zero-age no-op helper is removed with
the unfenced API. A zero age on the final pass retains its existing default.
`TestLeasedConcurrencyMaintenanceRemovesTerminalWorkInEveryScope` ports the old
local-repair liveness and idempotency assertions. Terminal global holders now
release with local holders. The separate repair-count API is removed.


The [concurrency and self-claim continuation](w3-ingress-lease-continuation.md#concurrency-and-self-claim-continuation)
ports `TestRetryLeavesTheRowRunningThroughItsBackoff` to the common executor under
synctest and replaces the owner input-rewrite test with a generated identity
property. The plan-concurrency tests observe early results and join their eviction
workers. Their removed-schema failures remain visible. No acceptance cell changes.

The [W3 runtime ingress continuation](w3-ingress-lease-continuation.md) ports
three claimed-heartbeat assertions to `lease.Keeper`, sends the existing SDK
generator fixtures through the scheduler's raw-byte ingress, and migrates the
startup/body/dry-run assertions to procenv. The debug wait now reports an early
host failure. It adds no acceptance cell. Store-backed legacy runtime ports and
native placements remain open.


This repository-owned inventory records every parked source function and its required port. Acceptance destinations name existing skeletons; package destinations name required focused proofs to implement with their owning area. A scenario is complete only when every mapped case is implemented. Product skeletons remain skipped until their required areas exist; a wrong result must fail once activated.

Branch labels below retain the descriptive suffix of each source branch. Tracker prefixes are omitted because repository files cannot contain tracker identifiers. The full Git revision identifies the exact branch snapshot independently of ref movement. Resolve source files with `git show <revision>:<file>`; removed predecessors are present before the branch comparison range. This inventory requires no personal filesystem path or live branch ref.

The primary inventory has 74 changed or added functions: 6 lifecycle, 6 state-machine, 4 placement, 1 credential matrix, 23 run-input, 24 storage, and 10 state/security. Placement also contributes 10 existing functions. Six removed predecessors bring the total to 90 unique revision/file/function identities. Subcases do not increase that count. Source objects were inspected statically; this is not evidence that old or target product tests pass.

Capability management ports the legacy TestRotateToken, its concurrent-revocation
regression and both revocation tests to pkg/store/capability_inventory_test.go.
The ports retain grace validity, explicit-revocation precedence, terminal
revocation refusal and distinct replacements. Generated deadlines now exercise
nanosecond boundaries and descendants. Independent SQLite connections race
rotation and revocation. A failed origin check or write returns no bearer.
The old four functions are deleted. The added
credentials-delegation-subset/store-sqlite cell proves persistence through two
reopenings. TestCapabilityManagementHTTPUsesStoreTransactions exercises the
production manager through the actual controller policy, session proof and CSRF.
These components do not complete native placement or PostgreSQL cells.

## Source snapshots

`TestCapabilityManagementHTTPRotation` replaces the legacy controller tests
`TestRotateToken_GraceCapRejected` and `TestRotateToken_GraceAtCapAccepted`.
It checks the existing seven-day ceiling, default and zero grace, null and
overflow refusal, and propagation of the authorized actor and absolute expiry
through both member and deployment routes. `TestCapabilityManagementHTTPAuditProperty`
replaces `TestAuditKeepsABearerInATargetOutOfTheLog`. Generated bearers pasted
into token selectors or invalid roles never reach logs. Token targets retain
only their public 16-character prefix. The three old tests and their obsolete
rotation fixture are deleted. These are fake-backed HTTP proofs. They do not
prove the committing rotation, grace, source-consent transfer or native route
matrix and add no archived source-function identity to the inventory.

`pkg/store/download_accounting_runtime_test.go` ports
`TestChargeDownloadHoldsATeamToItsDailyCap` and
`TestChargeDownloadRaceForTheLastBytesAdmitsExactlyOne` from the retired raw
Store method. Final-schema tests preserve cap crossing, delivered-byte counting,
funding changes, operator exemption and UTC-day reset. Independent SQLite
connections race the last capacity and reopen the exhausted budget. A generated
property varies byte counts and hostile replica strings while checking tenant
isolation and conservation. Expired generic download holds release without
double subtraction. Failed counter writes and overflow roll back admission.

Event accounting ports the five charged-storage tests and seven event-cap tests
from `pkg/store/storage_test.go` and `event_limits_test.go` to
`event_accounting_runtime_test.go` and `event_policy_runtime_test.go`.
The no-quota assertion now requires additive usage because final counters record
all tenant writes. The ports retain byte/month/run/count caps, kind validation,
kind byte cost, concurrent admission, uncharged event counters and failed-write
rollback. They add shared upload capacity, reopening, month rollover, funding,
free share and a generated tenant-isolation/conservation property.
The replica SQLite acceptance cell now races the real event writer and verifies
its remaining capacity after reopening. No native source row is marked complete.

The existing `storage-replica-safe-accounting/store-data-sqlite` cell now includes
real tenant download charges, their last-capacity race and exhausted-budget
reopening. The controller proof exercises delivered-byte accounting, trusted
replica identity, tenant isolation and log/live-log exemptions without a socket.
No new native acceptance cell or archived source row is marked complete.

`pkg/store/storage_standing_runtime_test.go` ports retained event-byte release
after deletion and the funded/free/held storage-standing matrix to final rows.
It checks unique blob/log inventory, commit/abort/expiry holds, corrupt totals
and reopening. `FuzzStorageStandingTenantInventory` generates own and foreign
inventory sizes and replica identities, with stale raw counters present. Reports
must stay tenant-scoped and leave accounting unchanged. The controller proof uses
the same final tenant snapshot. The existing upload crash/replay SQLite cell now
checks live holds before commit and retained bytes after reopening. The raw
standing/usage reads, event-byte repair API and duplicate event cache are removed.
No archived source row or native acceptance cell is newly closed.

The unused unleased `Store.ReleaseExpiredStorage` scan and its two legacy tests
are removed. `TestBlobUploadExpiredReservationFreesCapacity` ports zero-byte
expiry and capacity release to final tickets. Empty uploads reserve object
capacity, expired tickets refuse late commits, and aborting an expired ticket
cannot release its successor's hold. Committed replay remains covered separately.

The raw paid-credit route and its handler tests are retired.
`pkg/store/credit_grant_runtime_test.go` ports free-grant idempotency, team
scoping, validation, balance/history accounting and payment-linked reversals
to the final schema. Paid fixtures use committed checkout settlement facts.
`payment_refunds_test.go` retains refund replay and ports pre-settlement fraud
warning refusal. Generated properties exercise every raw store grant surface
and the registered retired HTTP route. The existing ledger-retention SQLite
cell also refuses raw paid grants. Native provider and controller adapters
remain open. No archived source row or native acceptance cell is newly closed.

The service-token storage and download counter handlers and their rolling-block
tests are retired. `pkg/controller/store_data_retirement_internal_test.go` adds
`FuzzLegacyStorageCountersAreGone` over generated credentials, tenant strings and
byte counts. Every retired route returns 404. Final ticket tests retain scoped
quota admission, immutable replay, concurrent-client and abort coverage.
The two signup source tests use the final SQLite schema without a listener.
They preserve full-tier and unreadable-source refusal and the server's own gate
projection. Slot policy persistence and sealed-write refusal are covered in
`pkg/store/credit_settings_runtime_test.go`. Native signup and data adapters
remain open. No archived source row or native acceptance cell is newly closed.

The independent bucket-listing storage pass and its tests are retired.
`pkg/controller/storage_inventory_internal_test.go:TestStorageMaintenanceUsesFinalInventoryAndLeavesReplicaUsage`
checks the replacement maintenance path against the final SQLite schema. It
requires a clean maintenance pass, billing from committed blob inventory despite
stale raw counters, and unchanged replica usage. Existing retained-storage and
replica-accounting proofs cover rollback, concurrent clients and reopening.
Listing reconciliation, receipt replay after one-day pruning, and free-share
log deletion tests are deleted with those mechanisms. Allowance-driven physical
eviction and native transport remain open. No archived source row is newly closed.

W3's [router and native continuation](w3-router-native-continuation.md) ports the
existing route and cleanup assertions to the shared v2 handlers. The three
daemon lease-policy tests retain ownership, resources, priority/class and
semaphore-only checks at the resolved W3 seam. Native assembly and automatic
persisted generator reconstruction use existing process/replay components.
No acceptance cell is added or activated by this continuation. The remaining
legacy scheduler/store fixtures still fail against the removed schema.


The unfenced node/trigger reapers and their dispatcher-expiry helpers are removed.
`pkg/controller/reaper_test.go:TestReaperRecoversExpiredOwnerAndKeepsLiveOwner`
replaces its private ticker-based reaper with the real elected controller pass.
It reverses the old assertion that a remote owner loss fails the run.
`pkg/store/reaper_fence_property_test.go:TestReaperFenceProperty` generates forged
holders and generations, exact expiry and takeover. Refusals change no snapshot.
`recovery_test.go:TestRecovery/recovery-local-orphans/store-sqlite` ports local
orphan persistence through repeat passes and reopening. Existing same-run owner,
attempt, cancellation and bounded-reaper tests retain the other recovery assertions.
Native heartbeat and process-termination proofs remain required.
The two legacy `TestLease_Reaper*` tests are replaced by that controller pass and
the final owner/reclaim tests. `TestExpiry_ALapsedClaimRetriesThroughSettle` is
deleted because disruptions no longer consume application retries. Its ordinal,
stale-report and exhaustion checks live in the unstarted/started-lapse SQLite
cells and final unit-reaper tests. The private-pass error-suppression test
`TestExpiry_ADispatchFailureDoesNotBlockTheOrchestratorPass` is deleted with those
passes. `TestOwnerRequeueRollsBackWhenEventCannotCommit` instead requires a loud
error and transactional rollback through the sole reaper.

The SQLite store proofs
`recovery_test.go:TestRecovery/recovery-cancel-beats-loss/store-sqlite` and
`recovery_test.go:TestRecovery/recovery-reconciler-waits-and-reclaims/store-sqlite` port
`TestNodeRules_TheReaperEndsACancelledRunsLostNodeAsCancelled` and both orders of
`TestNodeRules_AnAdoptedRowIsNotFinishedTwice` from `node-state-machine`.
They check the final store transactions, persisted attempts and events, reopening,
and racing reconciliations.
Enrollment is a contract fake. Native owner processes and HTTP remain required
by the existing placement cells.

`recovery_test.go:TestRecovery/recovery-unstarted-lapse/store-sqlite` exercises
five unstarted job lapses, reopening after each requeue. Attempts, disruptions
and application retries stay zero. Each next award advances generation. The
first execution then starts attempt one. Old holders cannot finish the job,
and the five lapses create no sibling run. This adds coverage to the existing
recovery ports without completing the native process cells.

`recovery_test.go:TestRecovery/recovery-started-lapse-cap/store-sqlite` exercises
three started job lapses with reopening after each loss. Generations advance,
attempts retain ordinals one through three, and application retries stay zero.
Each lost attempt closes with agent_lost. The third loss fails the job and
reconciliation keeps it terminal without another claim. The proof checks exact
events and refuses stale reports after every loss. It adds coverage without a
new archived source-function identity or native process evidence.

`recovery_test.go:TestRecovery/recovery-concurrency-continuous-adoption/store-sqlite`
checks node and plan capacity through owner loss, remote renewal, reopening and
successor adoption. Node and run terminal transactions release their own groups.
Each release admits exactly one queued competitor. Enrollment is a contract fake.
Native processes and placement transports remain required.

`recovery_test.go:TestRecovery/recovery-application-retry-ordinal/store-sqlite`
adds persistence evidence for two identical failed attempts followed by success.
It checks the unchanged holder, generation and lease, zero disruptions, distinct
source ordinals, delayed-report refusal, retry replay after reopening, terminal
replay and exactly two retry events. `TestHolderReportsAffectOnlyTheirSourceAttemptProperty`
generates repeated failures, replay counts and invalid source ordinals at the
same store boundary. Native process continuity remains required. The old local
`TestReapExpiredNodeClaims_KeepsTheClaimGeneration` fixture is deleted because
the active unstarted-lapse and reconciler cells already check exact generation
preservation on reaping and advancement on each next award.

`pkg/store/maintenance_identity_test.go` ports the preservation assertions from
`TestPruneSpentIdentityKeepsWhatStillAdmits` to the final leased API. It covers
bounded invitation cleanup, expiry and repeated passes. The generated session
property preserves live sessions and capability-parent rows. Rollback and expired
maintenance fences leave both tables unchanged. Token-table assertions are
removed with that table. Capability expiry remains covered by the final retention
and ancestry tests. The invitation API proof checks nanosecond persistence for
creation, listing, email delivery, acceptance and withdrawal.

`pkg/store/concurrency_admission_runtime_test.go:TestHolderLivenessUsesStoreClock`
replaces `TestConcurrencyGuard_CanonicalSQLSitesOnly` from design-found-hotfixes.
Acquisition uses the injected clock. Expired capacity stops counting, heartbeat
and adoption cannot revive it, and reacquisition creates a new lease. The
96-case `TestConcurrencyExpiredCapacityNeverRevivesProperty` varies lease length
and exact-expiry boundaries. The old source-count assertion is deleted.

`placement_test.go:TestPlacement/placement-granted-identity-and-capacity/store-sqlite`
extends the existing package ports into active SQLite acceptance. It checks
named and queued claims for capacity, authenticated name, self-asserted labels,
repository enrollment, metering and granted pool toleration. Matcher, authority,
claims, events and billing-award persistence are real. Enrollment is a contract
fake. Refusals must come from matching, leave no events or billing awards, and
permit a subsequent claim when the missing requirement is supplied. Metered
success and launcher settlement remain outside this proof.

The archived cases exercised by that cell are
TestQueueClaimNeverTakesANodeLargerThanTheRunnersCapacity,
TestQueueClaimBindsNameToTheTokenPrincipal,
TestNamedClaimAppliesTheClaimantsProfile,
TestNamedClaimOfAMeteredAgentObeysItsAcceptList,
TestNamedClaimFromADispatcherThatSendsNoLabels,
TestPlacementToday_TriggerClaimMatchesNoLabels and
TestPlacementToday_SelfAssertedLabelsSatisfyRequires from the placement snapshot.
The package ports already replaced their old fixtures. This continuation adds
no new archived source-function identity to the inventory.

`credentials_test.go:TestCredentials/credentials-operator-console-split/store-sqlite`
checks trusted bootstrap/service issuance, rejection of console grants and
independent root revocation through the final store API. Origin verification is
a contract fake. It adds a store proof without completing the native route
matrix or changing the archived-test port count.

| Branch label | Exact revision | Comparison |
|---|---|---|
| `node-lifecycle` | `136fbf58144d9bd6c300b0ecc08a9925b468c95e` | This commit's additions |
| `node-state-machine` | `7b1fc6d111c272612d4c970d061063a369598e30` | This commit's additions |
| `placement` | `8f114747eba050739f6ce8b775e6eb84edb2e1b0` | This commit's additions |
| `credential-matrix` | `1e16668363ecbd99ba922436ab1a6f9cae63335b` | This commit's additions |
| `run-input-divergences` | `5ba4cb0ec1ec222defc58f5eaee6ca332a058303` | `073a533c3ef17f63794df99cfabf2e110f740d08..5ba4cb0ec` |
| `storage-hotfixes` | `80ce329157569a0d0d1cf1d3f67e3e06060512ed` | `694dbfcf6d3bead5954c640462f45ecea7ebba43..80ce32915` |
| `design-found-hotfixes` | `c5aa3b88f1b6debf856e887bfc6df0894355b6cd` | `694dbfcf6d3bead5954c640462f45ecea7ebba43..c5aa3b88f` |

The final three comparisons include all changes since the recorded merge base. Earlier branches use the named commit's additions. Helper changes travel with their owning tests. Final architecture contracts supersede old behavior: remove bug-dependent skips, duplicate events, cache-server credentials, arbitrary artifact reads and the here runner's one-claim restriction.

## Function inventory

<!-- port-inventory:begin -->
| Branch label | Revision | Source file | Source function | Cases | Disposition | New home and expected behavior |
|---|---|---|---|---|---|---|
| `node-lifecycle` | `136fbf58144d9bd6c300b0ecc08a9925b468c95e` | `internal/nodehost/lifecycle_test.go` | `TestLifecycleLayersPerPlacement` | `TestLifecycleLayersPerPlacement`, table rows `local`, `run host`, `dispatched`, `claimed`, `claimed retry_auto` | Replace | `internal/nodehost/lifecycle_test.go:TestLayerOwnership` and `protocol_test.go:TestProtocol/protocol-host-layers-risk-cleanup`. One owner/executor layer table governs every placement. Delete the `retry_auto` row and obsolete bounce/workers layers. Approvals remain owner-side. Application retry remains executor-side under the same claim. Proves §3.1-3.3 and §6.6. |
| `node-lifecycle` | `136fbf58144d9bd6c300b0ecc08a9925b468c95e` | `internal/nodehost/lifecycle_test.go` | `TestLifecycleLocalNodeStartsAndEndsOnce` | `TestLifecycleLocalNodeStartsAndEndsOnce` | Keep | `lifecycle_test.go:TestLifecycle/lifecycle-transition-replay-and-fencing/local` and `/retry`. Ordinary and retried jobs have one `job_started` and one terminal event. A retried attempt adds `job_retrying`, retains its claim and generation, and increments the attempt ordinal. The store emits events. Proves §3.3. |
| `node-lifecycle` | `136fbf58144d9bd6c300b0ecc08a9925b468c95e` | `internal/nodehost/lifecycle_test.go` | `TestLifecycleAFailedRecordEndsTheNodeOnce` | `TestLifecycleAFailedRecordEndsTheNodeOnce/refused output`; `TestLifecycleAFailedRecordEndsTheNodeOnce/store refuses the row` | Replace/Reverse | `lifecycle_test.go:TestLifecycle/lifecycle-result-commit-atomicity/refused_output`. An invalid output cannot become a successful result. Use the final 16 MiB cap instead of the fixture's old 64 MiB error. A committed failure has one store event. Proves §3.3, §7.6, §8.1. `lifecycle_test.go:TestLifecycle/lifecycle-result-commit-atomicity/store_unavailable`. Recording failure is loud. Do not retain the host-written `node_failed` event when the store refused the transition. State and lifecycle event commit together or neither commits. Proves §3.3. |
| `node-lifecycle` | `136fbf58144d9bd6c300b0ecc08a9925b468c95e` | `internal/nodehost/lifecycle_test.go` | `TestLifecycleADispatchedNodeEndsOnce` | `TestLifecycleADispatchedNodeEndsOnce/node host wrote the row`; `TestLifecycleADispatchedNodeEndsOnce/a writer that emits no event wrote the row` | Reverse/Replace | `lifecycle_test.go:TestLifecycle/lifecycle-transition-replay-and-fencing/adopt_finished`. Change the expected terminal-event count from two to one. Adoption emits no second event. Proves §3.3/A8. `lifecycle_test.go:TestLifecycle/lifecycle-transition-replay-and-fencing/executor_completion`. Every successful terminal transition emits its event in the store. Remove the run host's fallback event emission. Proves §3.3. |
| `node-lifecycle` | `136fbf58144d9bd6c300b0ecc08a9925b468c95e` | `internal/nodehost/lifecycle_test.go` | `TestLifecycleRunLeaseRejectedAfterRestart` | `TestLifecycleRunLeaseRejectedAfterRestart` | Reverse | `recovery_test.go:TestRecovery/recovery-keeper-causes-and-backoff/rejected_owner`. Rejected reattach cancels the unit with the same cause as rejected node reattach. The old expected successful continuation is forbidden. Proves §3.3/A9. |
| `node-lifecycle` | `136fbf58144d9bd6c300b0ecc08a9925b468c95e` | `internal/nodehost/memo_unix_test.go` | `TestMemo` | `TestMemo/ALeaderEndsOnceAndAHitWithItsCacheHit` | Replace | `storage_test.go:TestStorage/storage-memo-materialization-failures`. Keep one terminal event for the leader. Replace the hit's old `cache_hit`-only lifecycle assertion with one store-owned `job_memoized` transition. Proves §3.3/A25. |
| `node-state-machine` | `7b1fc6d111c272612d4c970d061063a369598e30` | `internal/nodehost/node_end_event_pin_test.go` | `TestDispatchedNodeEndsWithOneEndEvent` | `TestDispatchedNodeEndsWithOneEndEvent` | Keep | `lifecycle_test.go:TestLifecycle/lifecycle-transition-replay-and-fencing/adopt_finished`. One store event, with a failing assertion instead of a defect-dependent skip. Proves §3.3. |
| `node-state-machine` | `7b1fc6d111c272612d4c970d061063a369598e30` | `pkg/store/node_state_pins_test.go` | `TestNodeRules_ATriggerFenceCannotEndALiveHoldersRow` | `TestNodeRules_ATriggerFenceCannotEndALiveHoldersRow` | Keep | `credentials_test.go:TestCredentials/credentials-holder-mutation-races/owner_cannot_finish_executor` and `pkg/store/node_state_test.go:TestTransitionHolderRights`. A trigger capability cannot overwrite a live competing node holder. The refused transaction changes no row or event. Proves §3.3, §5.1. |
| `node-state-machine` | `7b1fc6d111c272612d4c970d061063a369598e30` | `pkg/store/node_state_pins_test.go` | `TestNodeRules_TheReaperEndsACancelledRunsLostNodeAsCancelled` | `TestNodeRules_TheReaperEndsACancelledRunsLostNodeAsCancelled` | Keep | `recovery_test.go:TestRecovery/recovery-cancel-beats-loss/cancel_requested`. A cancel-requested unit ends cancelled. No retry run is created. Proves §3.4. |
| `node-state-machine` | `7b1fc6d111c272612d4c970d061063a369598e30` | `pkg/store/node_state_pins_test.go` | `TestNodeRules_AnAdoptedRowIsNotFinishedTwice` | `TestNodeRules_AnAdoptedRowIsNotFinishedTwice/reaper_first=false` and `/reaper_first=true` | Replace | `recovery_test.go:TestRecovery/recovery-reconciler-waits-and-reclaims/host_first` and `/reaper_first`. The stale host report is refused in either order. The reaper requeues within the same run, and the reconciler follows the persisted state. Proves §3.3-3.4. |
| `node-state-machine` | `7b1fc6d111c272612d4c970d061063a369598e30` | `pkg/store/node_state_pins_test.go` | `TestNodeRules_SettleRunsARecoveryNodeThatListsItsParentAsADep` | `TestNodeRules_SettleRunsARecoveryNodeThatListsItsParentAsADep` | Keep | `lifecycle_test.go:TestLifecycle/lifecycle-dependency-verdicts/parent_failure`. A failed recovery parent releases recovery once its other dependencies permit it. The parent's failure does not count as an ordinary failed prerequisite. Proves §3.3, §3.6. |
| `node-state-machine` | `7b1fc6d111c272612d4c970d061063a369598e30` | `pkg/store/lifecycle_guard_test.go` | `TestLifecycleGuard_StatusWritersAreCounted` | `TestLifecycleGuard_StatusWritersAreCounted` | Replace | `pkg/store/lifecycle_guard_test.go:TestStateWriterOwnership`. Restrict state writers to the final owner files instead of pinning today's counts. Creation goes through `AcceptPlan` or `AcceptExpansion`. Keep this as a package architectural check, not a runtime acceptance case. Proves §4.1. |
| `placement` | `8f114747eba050739f6ce8b775e6eb84edb2e1b0` | `pkg/store/placement_characterization_test.go` | `TestPlacementToday_HereTriggerIsStrandedAfterItsLeaseLapses` | `TestPlacementToday_HereTriggerIsStrandedAfterItsLeaseLapses` | Reverse | `recovery_test.go:TestRecovery/recovery-here-resume-window/lease_lapse`. The here capability reclaims the same run before expiry through `pipeline trigger --resume`. Other runners cannot claim it. Expiry fails with the documented reason. Proves §3.4, §5.1, §9.3. |
| `placement` | `8f114747eba050739f6ce8b775e6eb84edb2e1b0` | `pkg/store/placement_characterization_test.go` | `TestPlacementToday_TriggerClaimMatchesNoLabels` | `TestPlacementToday_TriggerClaimMatchesNoLabels` | Reverse | `placement_test.go:TestPlacement/placement-granted-identity-and-capacity/trigger_requirements`. A claimant missing the trigger's requirements cannot host it. Replace the old fixture, which puts `gpu` only on a node, with explicit run/trigger demand. Proves §3.5. |
| `placement` | `8f114747eba050739f6ce8b775e6eb84edb2e1b0` | `pkg/store/placement_characterization_test.go` | `TestPlacementToday_SelfAssertedLabelsSatisfyRequires` | `TestPlacementToday_SelfAssertedLabelsSatisfyRequires` | Replace | `placement_test.go:TestPlacement/placement-granted-identity-and-capacity` and `pkg/match/evaluate_test.go:TestGrantedKeys`. Ordinary labels such as `prod` remain self-asserted. `pool=`, `name=`, and `class=` come from authority, not supplied labels. Replace `Requires("local")` and `location=coordinator` with `Inline`. Proves §3.5, §6.6. |
| `placement` | `8f114747eba050739f6ce8b775e6eb84edb2e1b0` | `pkg/store/placement_characterization_test.go` | `TestPlacementToday_CoordinatorPinnedNodeIsInvisibleToClaims` | `TestPlacementToday_CoordinatorPinnedNodeIsInvisibleToClaims` | Drop | `placement_test.go:TestPlacement/placement-granted-identity-and-capacity/eligible_recovery_work`. Delete coordinator-pin SQL and the pin mechanism. Matching pending work remains visible to claims and attention. Proves §3.4-3.5. |
| `placement` | `8f114747eba050739f6ce8b775e6eb84edb2e1b0` | `pkg/store/here_runner_test.go` | `TestCreateHereTriggerWithRun_PinsTheTriggerToItsCredentialOnce` | `TestCreateHereTriggerWithRun_PinsTheTriggerToItsCredentialOnce` | Replace | `recovery_test.go:TestRecovery/recovery-here-resume-window/run_binding`. Keep confinement to one run and the recorded reason. Reverse the second-claim refusal and remove `runner_pin`. |
| `placement` | `8f114747eba050739f6ce8b775e6eb84edb2e1b0` | `pkg/store/here_runner_test.go` | `TestCreateHereTriggerWithRun_RefusesAClaimAfterTheWindow` | `TestCreateHereTriggerWithRun_RefusesAClaimAfterTheWindow` | Keep | `credentials_test.go:TestCredentials/credentials-here-award-parent/initial_claim_window`. Keep the initial ten-minute claim window. Subsequent reclaim is governed by the established owner's expiry. |
| `placement` | `8f114747eba050739f6ce8b775e6eb84edb2e1b0` | `pkg/store/agent_match_test.go` | `TestQueueClaimNeverTakesANodeLargerThanTheRunnersCapacity` | `TestQueueClaimNeverTakesANodeLargerThanTheRunnersCapacity` | Keep | `placement_test.go:TestPlacement/placement-granted-identity-and-capacity/capacity`. A two-core runner cannot claim a four-core job. An eight-core runner can. |
| `placement` | `8f114747eba050739f6ce8b775e6eb84edb2e1b0` | `pkg/store/agent_match_test.go` | `TestQueueClaimBindsNameToTheTokenPrincipal` | `TestQueueClaimBindsNameToTheTokenPrincipal` | Keep | `placement_test.go:TestPlacement/placement-granted-identity-and-capacity/name`. Supplying another runner's name does not grant that identity. |
| `placement` | `8f114747eba050739f6ce8b775e6eb84edb2e1b0` | `pkg/store/agent_match_test.go` | `TestNamedClaimAppliesTheClaimantsProfile` | `TestNamedClaimAppliesTheClaimantsProfile` | Keep | `placement_test.go:TestPlacement/placement-granted-identity-and-capacity/named_claim`. Repository acceptance and job demand apply to named claims as they do to queue claims. |
| `placement` | `8f114747eba050739f6ce8b775e6eb84edb2e1b0` | `pkg/store/agent_match_test.go` | `TestRunnerTokenRefusesADuplicateAgentName` | `TestRunnerTokenRefusesADuplicateAgentName` | Replace | `pkg/store/capability_test.go:TestRunnerNameUniqueness`. Preserve team-scoped active runner identity and reuse after revocation through the final issuance API. |
| `placement` | `8f114747eba050739f6ce8b775e6eb84edb2e1b0` | `pkg/store/agent_match_test.go` | `TestNamedClaimOfAMeteredAgentObeysItsAcceptList` | `TestNamedClaimOfAMeteredAgentObeysItsAcceptList` | Keep | `placement_test.go:TestPlacement/placement-granted-identity-and-capacity/metered_runner`. Billing provenance confers no repository or placement authority. |
| `placement` | `8f114747eba050739f6ce8b775e6eb84edb2e1b0` | `pkg/store/agent_match_test.go` | `TestCloudPlacementMatchesThePreChangeBehavior` | `TestCloudPlacementMatchesThePreChangeBehavior` | Replace | `placement_test.go:TestPlacement/placement-granted-identity-and-capacity/launcher`. Use the granted launcher profile. Delete the special case where metering plus a named claim manufactures cloud placement. |
| `placement` | `8f114747eba050739f6ce8b775e6eb84edb2e1b0` | `pkg/store/agent_match_test.go` | `TestNamedClaimFromADispatcherThatSendsNoLabels` | `TestNamedClaimFromADispatcherThatSendsNoLabels` | Replace | `placement_test.go:TestPlacement/placement-granted-identity-and-capacity/missing_labels`. An unmet requirement is refused with its cause. Remove old-dispatcher compatibility framing. |
| `placement` | `8f114747eba050739f6ce8b775e6eb84edb2e1b0` | `pkg/store/agent_match_test.go` | `TestAgentNameIsUniqueOnEveryMintPath` | `TestAgentNameIsUniqueOnEveryMintPath` | Replace | `pkg/store/capability_test.go:TestRunnerNameRotation`. Preserve uniqueness during authorized rotation. Replace old token-kind APIs with capability issuance. |
| `run-input-divergences` | `5ba4cb0ec1ec222defc58f5eaee6ca332a058303` | `internal/api/event_test.go` | `TestPublicEventsRemoveClaimGenerationFromAttemptLifecycle` | `TestPublicEventsRemoveClaimGenerationFromAttemptLifecycle`, subtests `executor_selected`, `execution_attempt_started`, `execution_attempt_finished` | Replace | `internal/api/event_test.go:TestPublicEventsHideFences`. Preserve private fencing-data exclusion under the final event vocabulary. The old branch change only adds the masking argument to the projection call. |
| `run-input-divergences` | `5ba4cb0ec1ec222defc58f5eaee6ca332a058303` | `internal/api/event_test.go` | `TestPublicEventsDropMalformedProtectedLifecyclePayload` | `TestPublicEventsDropMalformedProtectedLifecyclePayload` | Keep | `internal/api/event_test.go:TestMalformedProtectedEvent`. A malformed protected payload is not returned. |
| `run-input-divergences` | `5ba4cb0ec1ec222defc58f5eaee6ca332a058303` | `internal/orchestrator/receipt/receipt_test.go` | `TestBuildReceipt_AnUnclassifiedRunHasNoInputsHash` | `TestBuildReceipt_AnUnclassifiedRunHasNoInputsHash` | Keep | `internal/orchestrator/receipt/receipt_test.go:TestUnclassifiedInputsHaveNoHash` and `credentials_test.go:TestCredentials/credentials-secret-display-classification/receipt`. An unclassified secret cannot become a guessable inputs hash. |
| `run-input-divergences` | `5ba4cb0ec1ec222defc58f5eaee6ca332a058303` | `internal/orchestrator/run_inputs_test.go` | `TestTriggerHost_TheRunAPIRedactsASecretArgument` | `TestTriggerHost_TheRunAPIRedactsASecretArgument` | Replace | `credentials_test.go:TestCredentials/credentials-secret-display-classification/runner_hosted`. Display reads redact secrets. Execution reads use a live bound capability rather than a machine-root token. Finished claims lose execution authority. |
| `run-input-divergences` | `5ba4cb0ec1ec222defc58f5eaee6ca332a058303` | `internal/orchestrator/run_inputs_test.go` | `TestLaunchedNode_TheRunRecordsItsSecretArguments` | `TestLaunchedNode_TheRunRecordsItsSecretArguments` | Keep | `credentials_test.go:TestCredentials/credentials-secret-display-classification/launcher`. The planner records the complete classification before ordinary reads can expose arguments. |
| `run-input-divergences` | `5ba4cb0ec1ec222defc58f5eaee6ca332a058303` | `internal/orchestrator/run_inputs_test.go` | `TestTriggerHost_KeepsTheIntakesInvocationKeys` | `TestTriggerHost_KeepsTheIntakesInvocationKeys` | Replace | `lifecycle_test.go:TestLifecycle/lifecycle-request-and-plan-seals/intake_fields`. The host cannot overwrite intake values. Replace invocation upsert with the two sealed records. |
| `run-input-divergences` | `5ba4cb0ec1ec222defc58f5eaee6ca332a058303` | `internal/orchestrator/run_inputs_test.go` | `TestHostedRun_ADefaultBranchGuardReadsTheCheckoutsOriginHead` | `TestHostedRun_ADefaultBranchGuardReadsTheCheckoutsOriginHead` | Keep | `environment_test.go:TestEnvironment/environment-origin-head-and-guards/origin_head`. Discover `trunk` from origin HEAD. `hermetic-checkout-planning/default_reject_feature` and `/default_reject_trunk` port the host guard verdicts. Missing-default when/unless cases fail closed. Direct-source tests cover real origin-HEAD acquisition. Native placement execution remains open. |
| `run-input-divergences` | `5ba4cb0ec1ec222defc58f5eaee6ca332a058303` | `internal/orchestrator/run_inputs_test.go` | `TestHostedRunContext_CarriesThePullRequest` | `TestHostedRunContext_CarriesThePullRequest` | Replace | `lifecycle_test.go:TestLifecycle/lifecycle-request-and-plan-seals/pull_request` and `internal/runspec/spec_test.go:TestPullRequestFields`. Carry number, action, base/head refs and SHAs through the sealed spec. `hermetic-checkout-planning` asserts all six fields in the SDK planning request. The shared projection retains Action and BaseSHA for attempts/evals too. |
| `run-input-divergences` | `5ba4cb0ec1ec222defc58f5eaee6ca332a058303` | `internal/orchestrator/run_inputs_test.go` | `TestLaunchedNode_RefusesASecretDefaultInAGroupName` | `TestLaunchedNode_RefusesASecretDefaultInAGroupName` | Keep | `internal/nodehost/host_planning_test.go:TestPlanningDefaults` checks SDK-default group refusal through the real shared validator, checkout resolver and SQLite owner lifecycle. Rejection finishes intake without a plan or jobs and returns no secret value. `FuzzPlanningDefaultsRefuseSecretIdentifiers` generates escaped, Unicode and control-bearing defaults across job, group, step and concurrency identifiers. SDK processes and source acquisition are test adapters. Native placement refusal remains open. |
| `run-input-divergences` | `5ba4cb0ec1ec222defc58f5eaee6ca332a058303` | `internal/orchestrator/run_inputs_test.go` | `TestHosts_RefuseASecretDefaultInANodeID` | `TestHosts_RefuseASecretDefaultInANodeID`, subtests `trigger host`, `launcher` | Keep | `internal/nodehost/host_planning_test.go:TestPlanningDefaults` supplies the real-validator and SQLite proof for default registration, short secrets and refusal before acceptance. Its safe case verifies persisted SDK defaults and classification. The generated property covers four identifier kinds. Native execution on every supported placement remains open. |
| `run-input-divergences` | `5ba4cb0ec1ec222defc58f5eaee6ca332a058303` | `pkg/controller/claim_run_test.go` | `TestClaimRun_PodRoutesServeOnlyTheirOwnClaim` | `TestClaimRun_PodRoutesServeOnlyTheirOwnClaim` | Replace | `credentials_test.go:TestCredentials/credentials-matrix-authentication-status/arguments_and_binding`. Planning receives real values for its own run. Replace old trigger routes and 401-on-live-denial answers with final route bindings and 403. |
| `run-input-divergences` | `5ba4cb0ec1ec222defc58f5eaee6ca332a058303` | `pkg/controller/run_inputs_test.go` | `TestAcceptPlan_NeverDropsAClassifiedSecretArgument` | `TestAcceptPlan_NeverDropsAClassifiedSecretArgument` | Keep | `credentials_test.go:TestCredentials/credentials-secret-display-classification/classification_union` and `pkg/store/run_spec_test.go:TestClassificationNeverShrinks`. A plan cannot remove an intake classification. |
| `run-input-divergences` | `5ba4cb0ec1ec222defc58f5eaee6ca332a058303` | `pkg/controller/run_inputs_test.go` | `TestTriggerReads_RedactTheRunsSecretArguments` | `TestTriggerReads_RedactTheRunsSecretArguments` | Replace | `credentials_test.go:TestCredentials/credentials-secret-display-classification/run_list_and_detail`. Move deleted `/triggers` list/detail assertions to run views. Preserve classified public arguments and hide every unclassified value. |
| `run-input-divergences` | `5ba4cb0ec1ec222defc58f5eaee6ca332a058303` | `pkg/controller/run_inputs_test.go` | `TestSecretArgs_AFinishedClaimReadsRedacted` | `TestSecretArgs_AFinishedClaimReadsRedacted`, subtests `node`, `run` | Replace | `credentials_test.go:TestCredentials/credentials-secret-display-classification/finished_job` and `/finished_run`. Authority ends at terminal state before lease expiry. A dead bound credential gets 401. A separate account display read remains redacted. |
| `run-input-divergences` | `5ba4cb0ec1ec222defc58f5eaee6ca332a058303` | `pkg/controller/run_inputs_test.go` | `TestApprovalReads_MaskSecretArgumentsInTheirText` | `TestApprovalReads_MaskSecretArgumentsInTheirText` | Keep | `credentials_test.go:TestCredentials/credentials-secret-display-classification`. Mask message, approver and comment across decorations, approval detail/list, pending approvals and events. Include SDK defaults at the write boundary. |
| `run-input-divergences` | `5ba4cb0ec1ec222defc58f5eaee6ca332a058303` | `pkg/controller/run_inputs_test.go` | `TestAcceptPlan_RefusesASecretInANodeID` | `TestAcceptPlan_RefusesASecretInANodeID` | Keep | `credentials_test.go:TestCredentials/credentials-secret-identifiers/controller_accept`. Refuse before insertion with the shared error object and no secret value. |
| `run-input-divergences` | `5ba4cb0ec1ec222defc58f5eaee6ca332a058303` | `pkg/controller/secret_args_display_test.go` | `TestSecretArgs_ControllerRedactsEveryArgOfAnUnclassifiedRun` | `TestSecretArgs_ControllerRedactsEveryArgOfAnUnclassifiedRun`, loop values `pending`, `success` | Keep | `credentials_test.go:TestCredentials/credentials-secret-display-classification/unclassified`. The display fails closed in both phases. |
| `run-input-divergences` | `5ba4cb0ec1ec222defc58f5eaee6ca332a058303` | `pkg/store/plan_secret_names_test.go` | `TestSecretInPlanName_RefusesEveryRenderedName` | `TestSecretInPlanName_RefusesEveryRenderedName` | Replace | `internal/nodeproto/validate_test.go:TestSecretIdentifiers` and `credentials_test.go:TestCredentials/credentials-secret-identifiers`. Use the exact rows below, with the short-value question recorded. |
| `run-input-divergences` | `5ba4cb0ec1ec222defc58f5eaee6ca332a058303` | `pkg/store/retry_team_test.go` | `TestManualRetryKeepsActorAndPullRequest` | `TestManualRetryKeepsActorAndPullRequest` | Replace | `lifecycle_test.go:TestLifecycle/lifecycle-retry-sealed-lineage/actor_and_pull_request`. Retain sealed actor, PR, original SHA and verified branch facts. Remove trigger-env copying and agent-loss retry-run framing. |
| `run-input-divergences` | `5ba4cb0ec1ec222defc58f5eaee6ca332a058303` | `pkg/store/run_inputs_test.go` | `TestCreateRun_TheIntakesInvocationKeysWin` | `TestCreateRun_TheIntakesInvocationKeysWin` | Replace | `pkg/store/run_spec_test.go:TestRequestSealIsImmutable` and `lifecycle_test.go:TestLifecycle/lifecycle-request-and-plan-seals/intake_precedence`. Fill resolved fields at the second seal without rewriting the request. |
| `run-input-divergences` | `5ba4cb0ec1ec222defc58f5eaee6ca332a058303` | `pkg/store/run_inputs_test.go` | `TestCreateRun_ConcurrentWritersNeverShrinkTheSecretArgs` | `TestCreateRun_ConcurrentWritersNeverShrinkTheSecretArgs` | Replace | `pkg/store/run_spec_test.go:TestConcurrentClassification`. Interleave final API writes on Postgres and prove classification never shrinks. Replace the pending-upsert hook with the owning transaction boundary. |
| `run-input-divergences` | `5ba4cb0ec1ec222defc58f5eaee6ca332a058303` | `pkg/store/secretargs_test.go` | `TestRedactedForDisplay_RedactsEveryArgOfAnUnclassifiedRun` | `TestRedactedForDisplay_RedactsEveryArgOfAnUnclassifiedRun` | Keep | `pkg/store/reporting_seals_test.go:TestReportingDistinguishesUnknownAndEmptyClassification` and `TestReportingReadsSealedFactsWithoutSecretValuesProperty`. Real sealed reports and receipts hide absent, nil, duplicate and invalid classification. |
| `run-input-divergences` | `5ba4cb0ec1ec222defc58f5eaee6ca332a058303` | `pkg/store/secretargs_test.go` | `TestRedactedForDisplay_AnEmptyClassificationShowsEveryArg` | `TestRedactedForDisplay_AnEmptyClassificationShowsEveryArg` | Keep | `pkg/store/reporting_seals_test.go:TestReportingDistinguishesUnknownAndEmptyClassification`. Explicit empty classification exposes public arguments and permits their receipt input hash. Absent classification hides values and omits the hash. |
| `storage-hotfixes` | `80ce329157569a0d0d1cf1d3f67e3e06060512ed` | `internal/artifacts/retention_test.go` | `TestCaptureRewritesStoredContentSoItsAgeRestarts` | `TestCaptureRewritesStoredContentSoItsAgeRestarts` | Replace | `storage_test.go:TestStorage/storage-reachability-and-retention-tails/reused_content`. Referenced bytes survive GC. Delete the exact two-PUT assertion because reachability and pinned records replace object-age refresh. |
| `storage-hotfixes` | `80ce329157569a0d0d1cf1d3f67e3e06060512ed` | `internal/artifacts/retention_test.go` | `TestPresentIsBoundedAndAnswersAMissPastItsBudget` | `TestPresentIsBoundedAndAnswersAMissPastItsBudget` | Replace | `internal/blob/store_test.go:TestHasManyBudget` and `storage_test.go:TestStorage/storage-memo-materialization-failures/budget`. Presence lookup respects its budget. A timed-out memo lookup is a miss. Do not pin the old worker count. |
| `storage-hotfixes` | `80ce329157569a0d0d1cf1d3f67e3e06060512ed` | `internal/cache/scope_test.go` | `TestArtifactsAreScopedToTheGrantsRepository` | `TestArtifactsAreScopedToTheGrantsRepository`, subtests `volume`, `bucket` | Replace | `storage_test.go:TestStorage/storage-reference-edge-laundering/filesystem` and `/s3`. Preserve foreign read/existence refusal and own-upload authority. Repository membership alone grants no read. Delete cache-server paths and credentials. |
| `storage-hotfixes` | `80ce329157569a0d0d1cf1d3f67e3e06060512ed` | `internal/cache/scope_test.go` | `TestStoredArtifactRewritePassesAFrozenCeiling` | `TestStoredArtifactRewritePassesAFrozenCeiling`, subtests `volume`, `bucket` | Replace | `storage_test.go:TestStorage/storage-hasmany-read-set/deduplicated_put`. Existing authorized content adds no quota bytes. New content is still refused beyond the ceiling. Use controller accounting instead of the mutable global ceiling. |
| `storage-hotfixes` | `80ce329157569a0d0d1cf1d3f67e3e06060512ed` | `internal/nodehost/memo_artifacts_test.go` | `TestMemoUnclaimedFollowerWithoutLeaderOutputRuns` | `TestMemoUnclaimedFollowerWithoutLeaderOutputRuns` | Keep | `storage_test.go:TestStorage/storage-memo-materialization-failures/follower_output`. A follower executes when its leader's output is gone. It cannot return successful empty output. |
| `storage-hotfixes` | `80ce329157569a0d0d1cf1d3f67e3e06060512ed` | `internal/nodehost/memo_artifacts_test.go` | `TestMemoCopyWhoseManifestWriteFailsRunsAsAMiss` | `TestMemoCopyWhoseManifestWriteFailsRunsAsAMiss`, subtests `Hit`, `Follow` | Keep | `storage_test.go:TestStorage/storage-memo-materialization-failures/manifest_commit`. A failed manifest-edge transaction becomes a memo miss for hits and followers. |
| `storage-hotfixes` | `80ce329157569a0d0d1cf1d3f67e3e06060512ed` | `internal/nodehost/rehydrate_test.go` | `TestARetryRunsAProducerWhoseArtifactsCannotBeCopied` | `TestARetryRunsAProducerWhoseArtifactsCannotBeCopied`, subtests `absent`, `refused` | Keep | `storage_test.go:TestStorage/storage-memo-materialization-failures/missing_bytes` and `/refused_edge`. A retry reruns the producer when bytes or authorized records cannot be copied. |
| `storage-hotfixes` | `80ce329157569a0d0d1cf1d3f67e3e06060512ed` | `pkg/controller/cache_ref_internal_test.go` | `TestCacheRefs_FollowTheGrantsSignedRefs` | `TestCacheRefs_FollowTheGrantsSignedRefs` | Replace | `internal/capability/data_scope_test.go:TestDataRefScope` and `storage_test.go:TestStorage/storage-memo-and-depcache-scope`. Use server-derived sealed scopes, manual namespaces and original retry trust. Memo reads its own scope with verified-default fallback. Depcache restores own ref, PR base, then verified default. Delete signed cache grants and unscoped/empty-repo fallback. |
| `storage-hotfixes` | `80ce329157569a0d0d1cf1d3f67e3e06060512ed` | `pkg/controller/claim_run_test.go` | `TestClaimRun_InputsFromAnotherRunFollowTheAcceptedPlan` | `TestClaimRun_InputsFromAnotherRunFollowTheAcceptedPlan` | Keep | `storage_test.go:TestStorage/storage-reference-edge-laundering`. Memo, coalesced and declared prior-run references work only through authorized edges. Preserve the full case inventory below. |
| `storage-hotfixes` | `80ce329157569a0d0d1cf1d3f67e3e06060512ed` | `pkg/controller/claim_run_test.go` | `TestClaimRun_MemoEntriesAreScopedToTheRunsRef` | `TestClaimRun_MemoEntriesAreScopedToTheRunsRef` | Replace | `storage_test.go:TestStorage/storage-memo-and-depcache-scope/poisoning_and_fallback`. Manual runs cannot poison verified default entries. Own scope precedes trusted fallback. Retry preserves trust. A planted false-provenance entry is refused. Seed trust through verified App intake instead of SQL `trigger_source=github`. |
| `storage-hotfixes` | `80ce329157569a0d0d1cf1d3f67e3e06060512ed` | `pkg/controller/claim_run_test.go` | `TestNodeClaimMemoKeysFollowTheRunsRef` | `TestNodeClaimMemoKeysFollowTheRunsRef` | Replace | `storage_test.go:TestStorage/storage-memo-and-depcache-scope/server_owned_key`. The server computes the scope. Naming main's scope or another job's key grants no authority. |
| `storage-hotfixes` | `80ce329157569a0d0d1cf1d3f67e3e06060512ed` | `pkg/controller/data_download_claim_provenance_test.go` | `TestDataDownloadClaimUsesCloudProvenance` | `TestDataDownloadClaimUsesCloudProvenance` | Replace | `storage_test.go:TestStorage/storage-binary-provenance` and `placement_test.go:TestPlacement/placement-direct-source`. Preserve trusted binary execution and source binding. Artifact access follows the read set. See the exact seven cases below and the provenance question. |
| `storage-hotfixes` | `80ce329157569a0d0d1cf1d3f67e3e06060512ed` | `pkg/controller/data_download_output_boundary_test.go` | `TestDataDownloadSharesCommittedArtifactsWithinTheRepository` | `TestDataDownloadSharesCommittedArtifactsWithinTheRepository` | Reverse | `storage_test.go:TestStorage/storage-reference-edge-laundering/same_repo_unrelated_run`. Repository membership alone cannot read another run's artifact. Require a dependency, declared reference, memo edge or own receipt. |
| `storage-hotfixes` | `80ce329157569a0d0d1cf1d3f67e3e06060512ed` | `pkg/controller/data_download_test.go` | `TestLegacyBinarySignerRefusesArtifactNames` | `TestLegacyBinarySignerRefusesArtifactNames` | Drop | `storage_test.go:TestStorage/storage-reference-edge-laundering/object_kind`. Delete the legacy signer. Retain object-kind and read-set refusal on final `/data` signing routes, with no legacy fallback. |
| `storage-hotfixes` | `80ce329157569a0d0d1cf1d3f67e3e06060512ed` | `pkg/controller/direct_artifact_scope_test.go` | `TestDirectArtifactReadsStayInsideTheGrantsRepository` | `TestDirectArtifactReadsStayInsideTheGrantsRepository` | Replace | `storage_test.go:TestStorage/storage-reference-edge-laundering/foreign_repository`. Foreign authority is refused. A verified own upload creates a receipt. Add same-team and same-run unrelated-job cases. |
| `storage-hotfixes` | `80ce329157569a0d0d1cf1d3f67e3e06060512ed` | `pkg/controller/direct_artifact_scope_test.go` | `TestDirectArtifactRedeclarationRenewsItsAge` | `TestDirectArtifactRedeclarationRenewsItsAge` | Replace | `storage_test.go:TestStorage/storage-reachability-and-retention-tails/live_reference`. A live reference prevents collection. Delete assertions about mutable `committed_at` and the object's `renewed` metadata. |
| `storage-hotfixes` | `80ce329157569a0d0d1cf1d3f67e3e06060512ed` | `pkg/controller/direct_artifact_scope_test.go` | `TestNodeArtifactManifestMustBeStoredByTheRunsRepository` | `TestNodeArtifactManifestMustBeStoredByTheRunsRepository` | Replace | `storage_test.go:TestStorage/storage-reference-edge-laundering/record`. Check existence and authority for the manifest and every member. Repository-scoped names alone are insufficient. |
| `storage-hotfixes` | `80ce329157569a0d0d1cf1d3f67e3e06060512ed` | `pkg/controller/direct_artifact_scope_test.go` | `TestClaimedAttemptRecordsOnlyAManifestItsRepositoryStored` | `TestClaimedAttemptRecordsOnlyAManifestItsRepositoryStored` | Replace | `storage_test.go:TestStorage/storage-reference-edge-laundering/result_transaction`. Apply existence and edge authorization atomically with the attempt result. |
| `storage-hotfixes` | `80ce329157569a0d0d1cf1d3f67e3e06060512ed` | `pkg/controller/direct_artifact_scope_test.go` | `TestRunnerNodeManifestMayLiveInItsOwnStore` | `TestRunnerNodeManifestMayLiveInItsOwnStore` | Reverse | `storage_test.go:TestStorage/storage-reference-edge-laundering/unseen_remote_manifest`. Remote result commit refuses a manifest the controller cannot validate. Remove `SPARKWING_CACHE_URL`. Local-run files remain local until an explicit publication. |
| `storage-hotfixes` | `80ce329157569a0d0d1cf1d3f67e3e06060512ed` | `pkg/controller/direct_artifact_scope_test.go` | `TestClaimedAttemptManifestIsCheckedAgainstTheCacheBucket` | `TestClaimedAttemptManifestIsCheckedAgainstTheCacheBucket` | Drop | `storage_test.go:TestStorage/storage-reference-edge-laundering/filesystem` and `/s3`. Delete cache-bucket HEAD fallback. Authoritative blob rows and authorized members govern both final backends. |
| `storage-hotfixes` | `80ce329157569a0d0d1cf1d3f67e3e06060512ed` | `pkg/controller/direct_upload_test.go` | `TestDirectCommitAdoptsVerifiedCopyAfterOldReservationExpires` | `TestDirectCommitAdoptsVerifiedCopyAfterOldReservationExpires` | Replace | `internal/blob/store_test.go:TestCommitAfterInterruptedUpload` and `storage_test.go:TestStorage/storage-cas-integrity/retry`. Adopt only verified immutable bytes, preserve uploader provenance, and issue receipts only to authorized uploaders. |
| `storage-hotfixes` | `80ce329157569a0d0d1cf1d3f67e3e06060512ed` | `pkg/controller/direct_upload_test.go` | `TestDirectCommitRefusesExistingObjectWithDifferentChecksum` | `TestDirectCommitRefusesExistingObjectWithDifferentChecksum` | Keep | `storage_test.go:TestStorage/storage-cas-integrity/checksum_mismatch`. Refuse conflicting bytes and publish no successful record. |
| `storage-hotfixes` | `80ce329157569a0d0d1cf1d3f67e3e06060512ed` | `pkg/controller/direct_upload_test.go` | `TestClaimBinaryUploadIsReservedUnderTheRunsRef` | `TestClaimBinaryUploadIsReservedUnderTheRunsRef` | Replace | `storage_test.go:TestStorage/storage-binary-provenance/ref_scope`. Manual writes use the manual namespace. Retry retains its original awarded scope. Replace the hand-minted cache-grant fixture. |
| `storage-hotfixes` | `80ce329157569a0d0d1cf1d3f67e3e06060512ed` | `pkg/store/generated_node_test.go` | `TestAGeneratedNodesRecordedSpecIsAdmitted` | `TestAGeneratedNodesRecordedSpecIsAdmitted` | Replace | `pkg/store/expansion_test.go:TestGeneratedSpecAuthority` and `lifecycle_test.go:TestLifecycle/lifecycle-generator-reconstruct-code/declared_references`. Persisted spec/hash controls generated-job references. Forged hash and undeclared references are refused. |
| `design-found-hotfixes` | `c5aa3b88f1b6debf856e887bfc6df0894355b6cd` | `internal/nodehost/onfailure_test.go` | `TestRecoveryWaitsOnItsOwnDeps` | `TestRecoveryWaitsOnItsOwnDeps` | Keep | `lifecycle_test.go:TestLifecycle/lifecycle-dependency-verdicts/own_dependencies`. Recovery waits for its own dependencies and generated groups as well as its parent. Only the failed parent is exempt from ordinary failure propagation. |
| `design-found-hotfixes` | `c5aa3b88f1b6debf856e887bfc6df0894355b6cd` | `internal/nodehost/onfailure_test.go` | `TestRecoveryIsCancelledByAFailedDepOfItsOwn` | `TestRecoveryIsCancelledByAFailedDepOfItsOwn` | Keep | `lifecycle_test.go:TestLifecycle/lifecycle-dependency-verdicts/failed_own_dependency`. An ordinary failed prerequisite prevents recovery. Use reason `upstream_failed`. |
| `design-found-hotfixes` | `c5aa3b88f1b6debf856e887bfc6df0894355b6cd` | `internal/runners/k8s/k8s_test.go` | `TestResolveResources_StaysQuietUnderTheCeiling` | `TestResolveResources_StaysQuietUnderTheCeiling` | Replace | `internal/runners/launcher/launcher_test.go:TestSizingBelowCeiling`. Missing measurements are normal and under-ceiling work emits no warning event. Delete the old k8s dispatcher and use the shared error envelope. |
| `design-found-hotfixes` | `c5aa3b88f1b6debf856e887bfc6df0894355b6cd` | `pkg/controller/github_runners_test.go` | `TestGitHubRunnerCredentialReadsOnlyAClaimedPipelinesProfile` | `TestGitHubRunnerCredentialReadsOnlyAClaimedPipelinesProfile` | Replace | `credentials_test.go:TestCredentials/credentials-holder-mutation-races/github_actions`. No claim and another pipeline are denied. An authorized missing sizing record returns not-found. Use `pipelineOfBoundRun` on the final sizing route. |
| `design-found-hotfixes` | `c5aa3b88f1b6debf856e887bfc6df0894355b6cd` | `pkg/controller/log_segments_internal_test.go` | `TestTriggerFencedLogWritesRefuseALiveHolder` | `TestTriggerFencedLogWritesRefuseALiveHolder` | Replace | `credentials_test.go:TestCredentials/credentials-holder-mutation-races/log_mutations`. A competing holder cannot append, sign or seal logs. Delete the unfenced-admin bypass. Recheck after request-body consumption and before mutation. |
| `design-found-hotfixes` | `c5aa3b88f1b6debf856e887bfc6df0894355b6cd` | `pkg/controller/runner_scopes_test.go` | `TestRunnerScopes_ProfileWritesAreScopedToTheClaimedPipeline` | `TestRunnerScopes_ProfileWritesAreScopedToTheClaimedPipeline` | Replace | `credentials_test.go:TestCredentials/credentials-holder-mutation-races/read_and_write`. Exact sealed repo and pipeline bind observations and pins. Delete shared bare-key authority. |
| `design-found-hotfixes` | `c5aa3b88f1b6debf856e887bfc6df0894355b6cd` | `pkg/store/concurrency_guard_test.go` | `TestConcurrencyGuard_CanonicalSQLSitesOnly` | `TestConcurrencyGuard_CanonicalSQLSitesOnly` | Replace | Implemented in `pkg/store/concurrency_admission_runtime_test.go:TestHolderLivenessUsesStoreClock` and `pkg/store/concurrency_scope_property_test.go:TestConcurrencyExpiredCapacityNeverRevivesProperty`. Canonical liveness uses the injected store clock. Expired capacity cannot renew or adopt. The old source-count test is deleted. |
| `design-found-hotfixes` | `c5aa3b88f1b6debf856e887bfc6df0894355b6cd` | `pkg/store/controller_dispatch_test.go` | `TestReportAttempt_ARecoveryNodeWaitsOnItsOwnDeps` | `TestReportAttempt_ARecoveryNodeWaitsOnItsOwnDeps` | Keep | `lifecycle_test.go:TestLifecycle/lifecycle-dependency-verdicts/controller` and `pkg/store/decide_test.go:TestRecoveryDependencies`. Success of an own dependency releases recovery. Failure cancels it. A successful recovery does not erase the original run failure. |
| `design-found-hotfixes` | `c5aa3b88f1b6debf856e887bfc6df0894355b6cd` | `pkg/store/node_claim_fence_holder_test.go` | `TestTriggerFencedNodeWritesRefuseALiveHolder` | `TestTriggerFencedNodeWritesRefuseALiveHolder` | Replace | `credentials_test.go:TestCredentials/credentials-holder-mutation-races/node_mutations` and `recovery_test.go:TestRecovery/recovery-reconciler-waits-and-reclaims`. Refuse competing live writes and terminal reversal. Reverse the last old assertion allowing the host to finish after lease lapse. The reaper and reconciler own that recovery. Delete host lifecycle-event append and `ResetNodeForAutoRetry`. |
| `design-found-hotfixes` | `c5aa3b88f1b6debf856e887bfc6df0894355b6cd` | `pkg/store/profile_claim_test.go` | `TestPrincipalHoldsProfileClaimMatchesTheClaimedRunsExactKey` | `TestPrincipalHoldsProfileClaimMatchesTheClaimedRunsExactKey`, subtests `build`, `ci/build`, `3:abcbuild` | Replace | `pkg/store/capability_test.go:TestSizingBinding` and `credentials_test.go:TestCredentials/credentials-holder-mutation-races/pipeline_names`. Preserve full pipeline names and exact repo identity. Delete authorization through bare shared profile keys. |
| `credential-matrix` | `1e16668363ecbd99ba922436ab1a6f9cae63335b` | `pkg/controller/credential_matrix_test.go` | `TestCredentialMatrix` | `TestCredentialMatrix`; 8,466 route/target/credential cells | Replace | `credentials_test.go:TestCredentials/credentials-matrix-authentication-status` and `pkg/controller/route_policy_test.go:TestEveryRouteHasPolicy`. Independently specify permissions, cover final registrations and binding alternatives, isolate controller/store per cell, and use reference-token lookup. |
| `run-input-divergences` | `5ba4cb0ec1ec222defc58f5eaee6ca332a058303` | `pkg/controller/secret_args_display_test.go` | `TestSecretArgs_ControllerPendingTriggerRowIsNotYetClassified` | `TestSecretArgs_ControllerPendingTriggerRowIsNotYetClassified`; removed predecessor | Reverse | `credentials_test.go:TestCredentials/credentials-secret-display-classification/unclassified`. Refuse plaintext exposure; unclassified display fails closed. |
| `run-input-divergences` | `5ba4cb0ec1ec222defc58f5eaee6ca332a058303` | `pkg/controller/secret_args_display_test.go` | `TestSecretArgs_ControllerGrandfathersOldRuns` | `TestSecretArgs_ControllerGrandfathersOldRuns`; removed predecessor | Drop | `credentials_test.go:TestCredentials/credentials-secret-display-classification/unclassified`. Remove grandfathering; unclassified display fails closed. |
| `run-input-divergences` | `5ba4cb0ec1ec222defc58f5eaee6ca332a058303` | `pkg/store/secretargs_test.go` | `TestRedactedForDisplay_GrandfathersRunsWithoutClassification` | `TestRedactedForDisplay_GrandfathersRunsWithoutClassification`; removed predecessor | Reverse | `credentials_test.go:TestCredentials/credentials-secret-display-classification/unclassified`. Refuse plaintext exposure; unclassified display fails closed. |
| `storage-hotfixes` | `80ce329157569a0d0d1cf1d3f67e3e06060512ed` | `pkg/controller/cache_ref_internal_test.go` | `TestCacheRefs_FollowTheRunsRefAsGitHubActionsDoes` | `TestCacheRefs_FollowTheRunsRefAsGitHubActionsDoes`; removed predecessor | Replace | `storage_test.go:TestStorage/storage-memo-and-depcache-scope`. Use immutable sealed scope instead of mutable trigger-derived scope. |
| `storage-hotfixes` | `80ce329157569a0d0d1cf1d3f67e3e06060512ed` | `pkg/controller/data_download_output_boundary_test.go` | `TestDataDownloadSharesCommittedArtifactsWithinTheTeam` | `TestDataDownloadSharesCommittedArtifactsWithinTheTeam`; removed predecessor | Reverse | `storage_test.go:TestStorage/storage-reference-edge-laundering`. Refuse unrestricted team sharing; require authorized read-set edges. |
| `design-found-hotfixes` | `c5aa3b88f1b6debf856e887bfc6df0894355b6cd` | `internal/nodehost/onfailure_test.go` | `TestRecoveryWaitsOnItsParentAlone` | `TestRecoveryWaitsOnItsParentAlone`; removed predecessor | Reverse | `lifecycle_test.go:TestLifecycle/lifecycle-dependency-verdicts/own_dependencies`. Recovery waits on its own dependencies as well as its failed parent. |
<!-- port-inventory:end -->

## Required case details

`CheckArtifactStaging` and `CheckExecutorArtifactStaging` exercise the host's
required artifact read boundary under `storage-cas-integrity`. Missing records,
missing or corrupt manifest/member bytes, reader-close errors and invalid paths
fail. Verified downloads replace destinations after EOF and close. The real
executor stages once around application retries, uses the committed attempt's
storage binding, and reports failure before SDK startup when staging fails.
These checks carry the consumed-artifact failure and workspace-safety behaviors
of the old artifact tests. CheckExecutorArtifactStaging now uses ArtifactRecords.Prepare.
`hermetic-artifact-publication` and `hermetic-executor-publication` add capture,
verified uploads, manifest commit and one publication after application retries.
They preserve directory globs, internal symlinks, modes and empty captures.
Upload changes, receipt/record mismatches, cancellation and publication failures
produce no successful reference. Fuzz drivers cover workspace confinement and
attempt/scope binding. Real byte/record authorization uses separate drivers.
Memo materialization, native assembly and the legacy runtime cut remain open.
No additional parked-test row is claimed complete by these publication checks.

`storage-memo-and-depcache-scope/hermetic-depcache-layer` and
`hermetic-executor-depcache` exercise the shared directory-cache layer with
storage fakes. They cover cold save/restore, warm directories, fixed lockfile
keys, failed-job suppression, read integrity/budget failures, safe diagnostics,
and one restore/save envelope across retries with the final attempt binding.
`storage-local-remote-cache-opt-in/hermetic-depcache-opt-in` checks directory
reads through an explicit remote binding without a remote write method.
Fuzz drivers generate archive paths, capture symlinks and mismatched upload
bindings. Native Go/npm resolver tests use the captured job environment.
These preserve dependency-cache behaviors from the retained depcache tests.
They close no additional parked memo/materialization row. Real fallback and
provenance transactions, memo reuse, native composition and the legacy host
cut remain open. See [the continuation](w3-depcache-continuation.md).

`storage-memo-materialization-failures/hermetic-memo-records` verifies local
memo lookup/copy responses through the declared byte and record interfaces.
Missing output/manifest/member references or bytes, refused atomic copies,
budget failures and integrity/close failures produce a miss. It carries the
read-side requirements behind follower-output, manifest-commit and retry-artifact
ports. The fixture exercises the real W3 adapter, with fake W1/W7 transactions.
`storage-local-remote-cache-opt-in/hermetic-memo-opt-in` checks explicit remote
reads without local authority widening or remote writes. Fuzz drivers generate
failed streams and mismatched copy scopes. These checks close no additional
parked row because native owner/follower composition and C1's single terminal
transaction remain unimplemented. See [the memo continuation](w3-memo-continuation.md).

Amendment 1 adds `CheckHostServices` under the secret-identifier and SDK service
scenarios. It drives registered endpoint handlers with policy/persistence fakes,
checks acknowledged dynamic masks, short identifier collisions, optional step
membership, end_line and masked markdown. Native authentication and SDK clients
remain separate requirements. The application-retry cell also drives
`CheckExecutorEnvelope` from persisted ordinal zero through start and retry.
The source/output/replay fixtures now use sealed Source.Location and committed
start ordinals. See [W3's handoff](w3-amendment-handoff.md) for unresolved native
and cross-area contracts.

`internal/nodehost/lifecycle_test.go:TestLayerOwnership` ports
`TestLifecycleLayersPerPlacement` to the final owner/executor table. The old
retry-auto, bounce and workers rows are removed from that proof.
`TestOwnerHostApprovalStopsBeforeComputeLayers` verifies the approval boundary
through `OwnerHost.Run`. Native placement and the risk/cleanup acceptance cell
remain separate requirements.

The legacy retry-auto dispatch event and duplicate start-event conditions are
removed from the scheduler and pod envelope. Its dedicated fixture and test rows
are deleted. Application retry budget and backoff tests remain. The target
`CheckExecutorEnvelope/retry` case checks one generation, advancing attempt
ordinals and fresh process bindings without redispatch.

`CheckHostReconciliation` uses `StateFollower.WaitNode` for terminal and requeued
holders. Both cases advance persistence before subscription, send no notification
for that earlier commit and require a joined subscription before reconciliation
returns. `TestStateFollower*` also covers later notifications, spurious wake-ups,
run/job/empty-expansion changes, sequence regression and cancellation.
`TestOwnerHostWaitRetainsFollowerCleanupFailure` closes the case where a completed
job or cancelled owner discarded an independently failed subscription join.
Native controller event subscriptions remain an integration requirement.

`CheckClaimedNodeLifecycle` adds five contract-fake cases under
`recovery-keeper-causes-and-backoff/hermetic-claimed-node`: successful startup,
startup rejection, requested cancellation, silence and lease loss during work.
Every case checks joined renewal shutdown. Full native placement cells remain open.

The removed `run_node_claim_test.go` command tests used environment-supplied
claim fences. Their replacements are
`TestClaimedNodeHostDoesNotRenewAChangedAward`,
`TestClaimedNodeHostReportsSetupFailuresThroughTheClaim` and
`TestClaimedNodeHostAcquiresSourceAndJoinsNativeSDK`. These check persisted award
identity, fenced reports, exact-source acquisition, route revocation and process
cleanup after lease loss. The native test uses real git and protocol pipes with
fake store, engine policy, installer, validator, canonical hasher and protocol codecs.

`TestDispatchedNodeFetchesAnSSHMirrorThroughItsOwnGrant` and the runner's
`TestRunNodeDispatchesToTheSharedImplementation` are deleted with `run-node`.
The cache-mirror protocol has no replacement. Direct SSH, App and HTTPS source
behavior remains covered by `internal/source`. Native runner and launcher source
release through the final controller authority remains an integration requirement.

`placement-direct-source/hermetic-native-git` adds direct fetch and verified
bundle-plus-delta acquisition against a native git origin. The origin advances
past the sealed commit and renames its default branch. Both acquisition paths
retain the sealed source and default branch, restore the selected engine, install
with an explicit environment and release their owned files. A second acquisition
checks the same inputs after cleanup. Source release, engine policy, installer
and signed-object transport are fakes. These are acquisition proofs after source
authorization. `TestDataDownloadClaimUsesCloudProvenance`'s bound/unbound signing
and binary provenance cases still require credential and storage implementations.

`placement-direct-source/hermetic-bundle-failures` covers checksum mismatch,
invalid git bundle, missing object, redirect, failed download/read/close,
cancellation and lease loss. Each case stops before engine resolution or SDK
installation and cleans up its reader and workspace. Signed URLs stay out of
error messages.
`TestAcquireAuthorityLossBeforeInstallationStartsNoInstaller` covers lease loss
during pin reading and engine resolution. Neither case starts installation, and
the original lease-loss cause remains matchable.

`CheckExecutorEnvelope` drives `ClaimedNodeHost` and the common executor under
`protocol-host-layers-risk-cleanup/hermetic-claimed-executor`. Its eight cases
cover success, retries, drift, risk refusal, hook refusal, cleanup failure,
output refusal and terminal-transaction refusal. The store, SDK, engine policy,
validator, hasher, storage and route delegate are contract fakes. Native placement
cells remain open. Every process closes and cleans up. Application retries keep
the generation, advance the ordinal and get a fresh process binding. Storage
layers wrap the retries once. Executor error text is masked in both returned
errors and terminal requests.

The unused launcher host and its tests are deleted. These dispositions retain
their behavior requirements without retaining the removed claimed-mode runtime:

| Removed test | Replacement or integration obligation |
|---|---|
| `TestLaunchedNode_PlansHostsAndReports` | `CheckExecutorEnvelope/success`, `TestClaimedNodeHostAcquiresSourceAndJoinsNativeSDK` and `TestSDKPlanningNativeProcessesMaskStreamsAndReusePlanCache` cover host execution, source, pipes and masking. Native environment policy, controller authority and log-manifest wiring remain open. |
| `TestLaunchedNode_PlansWithTheTriggersActorAndPullRequest` | `TestSDKPlanningRetainsSealedRunContext` preserves actor, PR, git, start time, DryRun and NoMemo through planning. Native intake-to-SDK placement wiring remains open. |
| `TestLaunchedNode_PlanDriftFailsTheNode` | `CheckExecutorEnvelope/drift` refuses before the body. |
| `TestPodNode_WithoutAnAcceptedHashFailsClosed` | `TestClaimedNodeHostDoesNotRenewAChangedAward/missing_job_hash` and `/job_hash` refuse before acquisition or renewal. |
| `TestLaunchedNode_RetryAutoRunsAfterRunLast` | Drop redispatch and retry-auto. `CheckExecutorEnvelope/retry` checks one before hook, one final after hook and three application attempts under the same generation. |
| `TestLaunchedNode_RetryAutoNeverRunsABodyItsBeforeRunRefused` | Drop redispatch and retry-auto. `CheckExecutorEnvelope/before_refused` prevents the body and masks the error. |
| `TestLaunchedNode_PlainRetryRunsInThePod` | `CheckExecutorEnvelope/retry` advances ordinals within one claim. |
| `TestLaunchedNode_RunsItsConcurrencyGroupThroughTheClaim` | Replace executor-owned admission with owner admission and claim-renewed holders. Native store/admission integration remains open. |
| `TestLaunchedNode_PlanKeepsThePipelinesRequires` | Checkout declarations and accepted settings must retain YAML placement. Native configuration/acceptance wiring remains open. |
| `TestLaunchedNode_MemoHitsThroughTheClaim` | Memo is an owner decision committed by reconciliation. Missing output, missing artifacts and refused manifest edges remain required under `storage-memo-materialization-failures`. Native store/storage materialization remains open. |
| `TestLaunchedNode_HonorsTheTriggersRunFlags` | `TestSDKPlanningRetainsSealedRunContext` covers DryRun propagation. `TestOnlyRunsTheMatchedNodesAndWhatTheyNeed`, `TestNodeExecutorUsesSealedStepWindow` and `TestNativeExecutorSteps/window` cover sealed selections through the owner, executor and real Go SDK process. Native launcher placement remains open. |
| `TestLaunchedNode_NoCacheMissesTheMemo` | NoMemo remains an owner-side read bypass. `TestOwnerPolicyMemoKeyRequiresStringOrExplicitNull` covers key evaluation with that bypass. Native memo reads and writes remain open. |
| `TestLaunchedNode_LayersTheCheckoutsYAMLArgsAndGuards` | `environment-origin-head-and-guards/hermetic-checkout-planning` covers shared YAML argument precedence and guard evaluation through owner acceptance. `TestOwnerHostReadsAcquiredProjectForSharedPlanning` reads real acquired checkout files. Native placement execution remains open. |
| `TestLaunchedNode_AGuardJudgesTheSDKsDefaults` | The checkout-planning cell evaluates argument guards after SDK describe defaults. Refusals bypass SDK planning and plan cache, then seal refusal evidence through acceptance. Native placement execution remains open. |
| `TestApplyGoEnvFile_ReadsOnlyTheModuleSettings` | Drop the file reader and ambient environment mutation. `TestOwnerHostAcquiresSealedSourceWithExplicitEnvironment` checks source installation's explicit environment for new and resumed owners. The legacy fetch-source file writer remains in the runner area. |

`TestOwnerHostSourceProjectionFailureFinishesOnlyUnacceptedRuns` checks source
failure settlement. The real-git owner test resolves a new checkout's pin and
ensures the accepted engine on resume, with a fake engine resolver and installer.
Neither path removes the user's checkout. `TestMemoFollowerWithNoOutputRunsTheNode`
retains the missing-output miss proof after removal of the unused claimed-memo
mode. The final storage/reconciliation acceptance cell remains open.

`environment-startup-versus-body/hermetic-native-environment-policy` checks the
native plan, attempt and eval adapters at the procenv boundary. The fake policy
receives the captured base and sealed runtime identity. Attempts and evals split
the persisted job environment before startup. Planning has no job startup values.
A refused build discards partial environment and revokes the route once.
`TestNativeSDKStartsUseEnvironmentPolicyAndRevokeFailures` also launches real
fixture processes and checks their environment. The store and procenv policy
remain fakes. W6 filtering, actual SDK body environment and placement bootstrap
are unverified by these cells.

The same environment cell and native startup test include retry attempt/eval
cases whose accepted envelope retains the source run ID. Environment policy
receives the destination ID. Accepted git, arguments and settings remain sealed.

`lifecycle-result-commit-atomicity/hermetic-output-records` checks the typed
output publisher against fake blob and record stores. It covers explicit null,
the exact 16 MiB boundary, oversized or malformed JSON, cancellation, incorrect
attempt/generation/run/scope, receipt mismatches and failed or inconsistent
record commits. `hermetic-executor-output` ports the host half of
`TestLifecycleAFailedRecordEndsTheNodeOnce`: failed upload/commit reports a failed
job without an output reference; a refused result transaction returns an error.
Only the fake store handles lifecycle requests. Real transaction/event atomicity
remains C1's proof.

`storage-cas-integrity/hermetic-required-output` checks required output reads
through authorized record lookup and verified EOF. Missing records/bytes,
unauthorized bytes, corrupt or unverified streams, invalid JSON, oversize and
reader-close errors return no output. Null and the exact byte boundary pass.
Native byte signing, read-set enforcement and placement integration remain open.

The host's automatic retry-cause traversal and terminal-agent-loss reset hook
are deleted. `TestAnAutomaticRetryRunsOnlyItsCausesAndWhatTheyReach` is dropped
because recovery requeues inside the same run. The remaining retry fixtures
request successful-result reuse without cause lists. The generator-waiter case
is retained as `TestARetryRunsTheWaiterOfAFailedGenerator`. The existing
`CheckHostReconciliation/requeued` and `/terminal` cases cover the host's
replacement recovery boundary; only store-committed state permits execution.

`TestNativeSDKStartupChangeRefusesDriftBeforeBody` executes the common node host
with real protocol pipes. A stable startup value permits the body. A value that
changes the fixture's replan records plan_drift before execution and still joins
route revocation and cleanup. Its store, engine resolver, validator and procenv
policy are fakes.

`TestSDKEnvironmentProjectsSealedValuesAndPersistedGeneratedJob` checks that
generated job startup reads persistence rather than the initial plan.
`TestSDKEnvironmentRefusesMissingOrChangedPersistedIdentity` refuses changed
run, job, generation, declaration, hash and seal inputs before calling Build.
The previous launcher run-flag and environment requirements now have this native
adapter projection proof alongside the planning and source tests above.

### Complete old credential dimension

| Exact old label | Final fixture and decision |
|---|---|
| `anon` | Keep anonymous request coverage. |
| `open` | Replace network-open trust with local-mode authority and the loopback/socket boundary. Test local identity separately. |
| `root` | Replace blanket admin with the distinct deployment-admin and operator-console subjects. Bound routes have no admin bypass. |
| `service-credits` | Replace with the final billing/service authority. It must not grant unrelated operator-console powers. |
| `service-launch` | Keep a launcher service root that can Award across teams through the fixed table. |
| `service-storage` | Drop `storage.count`. There is no cache service or cache audience. |
| `session` | Store/component evidence: `TestSessionMembershipChangesApplyWithoutCachedGrantsProperty`, `TestSessionRoutePreservesCapabilityScopeProperty` and `TestCredentials/credentials-session-digest-csrf/store-sqlite` check current roles, capability ceilings, digest lookup and sealed CSRF persistence. `TestSessionFlowHTTPProofProperty`, `TestSessionFlowHTTPRenewalCeilingProperty` and `TestSessionFlowHTTPParentRevocationEndsRenewalAndLogout` exercise real SQLite session proof, CSRF, absolute lifetime and parent revocation through the controller handler. `TestSessionPolicyHTTPTransports`, `ProofProperty`, `LifetimeProperty` and `Refusals` cover Session-header/cookie policy requests against SQLite. `TestBrowserLogoutHTTPUsesNamedProof`, `ProofProperty` and `Refusals` port `TestBrowserLogoutNeedsTheSessionsTokenAndEndsIt`, add canonical refusal/audit checks, and prove failed proof cannot renew or delete. `TestSessionFlowHTTPLifetimeCases` ports the four legacy SessionRoute lifetime tests to nanosecond storage and a real hashed account session. `TestSessionFlowHTTPRenewsWithNamedProof` replaces the raw-ID/CSRF resolution test. `TestSessionFlowHTTPLogoutUsesVerifiedSession` replaces the body-selected hashed-row logout test. The replaced tests are deleted. Bootstrap login tests remain with the login cutover. Native route-matrix evidence remains required. |
| `member` | Keep team-member grants. A member does not gain work-claim grants directly. |
| `machine` | Keep machine root claim scopes. Secret/source/data/OIDC access requires a live award. |
| `machine+trigger` | Replace caller-supplied trigger fence with the awarded trigger capability. A root plus forged headers gains nothing. |
| `machine+node` | Replace caller-supplied node fences with the awarded node capability. A root plus forged headers gains nothing. |
| `trigger-claim` | Keep the trigger award with `runOfTrigger`, bounded by the store's holder rights. |
| `node-claim` | Keep a node-bound award with live generation/holder binding. |
| `plan-claim` | Keep a plan-unit award with its fixed grants. Add an eval-unit award for expansion. |
| `work-claim` | Keep a work award and add its delegated nodehost bearer with the separate audience. |
| `here` | Replace with the here case of Award. Its parent must retain `runs.write`, its first claim has a ten-minute window, and it can reclaim only its run before expiry. |
| `github` | Keep push-bound GitHub Actions exchange authority. It cannot manufacture App-verified branch trust. |

The old fixture is multi-team and licensed except for `open`, which is single-team. Preserve cross-team cases without treating a license as authorization.

### Complete old target dimension

| Old route class | Exact variants |
|---|---|
| `/api/v1/runs/{id}` routes containing `{nodeID}` | `home`, `otherNode`, `otherRun`, `otherTeam` |
| Other `/api/v1/runs/{id}` and `/api/v1/triggers/{id}` routes | `home`, `otherRun`, `otherTeam` |
| `/api/v1/concurrency/*`, `/api/v1/triggers`, `/api/v1/runs`, `/api/v1/triggers/spawned-child`, `/api/v1/secrets/{name}` | `home`, `otherNode`, `otherRun`, `otherTeam` |
| `/api/v1/pipelines/{name}*` | `home`, `otherPipeline` |
| Every other registered authenticated route | `home` |

The old request builder names the target in concurrency bodies, run creation, child submission/query, and secret queries. Other request bodies are `{}`. Positive final cells need valid minimal requests, because a handler's 400 from an empty body does not prove successful authorization.

### Final matrix obligations

Keep the old golden as source evidence in Git. Do not copy its expected statuses into the final suite. The tables above map credential presentations and target variants. [The route appendix](t1-credential-routes.md) enumerates all 229 old routes, their dispositions, scenario IDs, independent permission rules and unresolved rights. This completes the source inventory, not the expected permission matrix. Removed routes go to `surface-api-and-detail-parity`. Surviving cells remain launch-blocked until their expected rights are independently specified.

| Old behavior or omission | Final expected behavior |
|---|---|
| `/pipelines/{name}/profile`, `otherPipeline`, credentials `github`, `machine`, `machine+node`, `machine+trigger` are the four annotated gap cells | Reverse permission. The sizing route requires authority for the exact bound repo and pipeline. |
| Trigger claim reaches every node in its run | Keep `runOfTrigger`, but a competing holder still blocks writes in the transaction. |
| Admin/open bypasses concurrency slot binding | Reverse permission. `slotOfBoundNode` applies without an admin bypass. |
| `claim/input` ignores the node named in the path | Reverse permission. Require `ownNode` for the path and operation. |
| Here and GitHub credentials disagree on `/services` | Unresolved. Frozen §10/L72 records the disagreement but gives neither permission answer. The lead must resolve `U-SERVICES` in the route appendix. These cells block launch acceptance. Do not derive their expected answers from the production policy table. |
| Deps route converts fence failures to 500 | Replace with the shared `lock_held` or `illegal_transition` error. |
| Live trigger/plan/work claims get 401 on 339 disallowed routes each | Reverse status. Live credentials lacking grant or binding get 403. Dead credentials get 401. |
| Trigger fence loses a race after middleware | Keep transaction recheck and return 409 with the shared error object. |
| Handler authorizes create-run or parent binding | Move the rule to `bodyNamesRun` and the shared policy. Store mutations still recheck authority. |
| Matrix derives route discovery from `server.go` | Test the complete final registration surface against the policy registry. Independently specify expected permissions so the table under test is not its own answer key. |

Add dimensions for expired, revoked, terminal-bound, stale-generation, wrong-audience, wrong-holder and revoked-parent capabilities. Add Delegate narrowing, fixed Award grant tables, bootstrap Exchange and single-use revocation, here parent liveness, and expansion authority.

The old matrix excludes public routes, browser routes, the wingd socket, `/node/v1`, cache grants, GitHub exchange, webhooks, signed output/log URLs, and public data upload. Cover their surviving boundaries explicitly. Test `/data` signing with capability grants and signed URLs with their separate expiry contract. Test Unix peer identity and Windows SID/private-file behavior through the local fixture. Proves §5.1-5.6, §6.5, §8.1, §9.5.

The exact rows in `TestSecretInPlanName_RefusesEveryRenderedName` are `node id`, `group`, `step id`, `step group`, `concurrency group`, `runs_on label`, `below the floor`, and `clean`. Translate node to job and the removed `runs_on` field to the corresponding placement declaration. Add approval IDs and generated expansion IDs. The old `below the floor` row accepts secret `42` in `node-42`. The frozen rule has no minimum length, so the proposed target refuses this case. See open questions.

`TestClaimRun_InputsFromAnotherRunFollowTheAcceptedPlan` has four allowed table entries: a memo hit from `run-origin`, a coalesced leader read from `run-leader`, a coalesced cache fallback from `run-leader`, and a declared last-run reference from `run-last`. Its complete named refusal cases are:

- `a cache entry on a node that declares no memoization`
- `a sibling node's key for a known hash`
- `a bare memo key for a known hash`
- `an entry another node wrote under its key`
- `an entry another pipeline wrote under its key`
- `a leader another repository wrote under its key`
- `a cache entry another repository wrote under its key`
- `an undeclared pipeline reference`
- `a pipeline reference on a node that declares none`
- `another node's coalesce waiter`
- `a key with no waiter and no cache entry`
- `another team's cache entry`
- `another team's run of the node under its key`
- `another team's entry for the same node and hash`

Retain each authorization/miss distinction through the final API. Use final `apierr` codes rather than mechanically preserving every old HTTP status.

`TestCacheRefs_FollowTheGrantsSignedRefs` has no named subtests. Its table covers main scope, PR/base/default restore order, manual-main scope with default fallback, `manual:` with an empty ref, a claim with no scope, a non-claim legacy grant, and a separate other-team rejection. Replace the manual-empty and non-claim unscoped assumptions with final sealed repo/ref validity and authority rules. Do not preserve empty namespace sharing. The old PR-base row is source evidence, not a new memo fallback requirement. Frozen §8.1 specifies own-ref/PR-base/default order for depcache, while memo has its own scope and verified-default fallback. A PR-base memo fallback needs a separate resolution.

`TestDataDownloadClaimUsesCloudProvenance` has these exact subtests: `local artifact`, `cloud artifact`, `local binary`, `cloud binary`, `legacy binary`, `bound source`, `unbound source`. Cloud binaries require trusted provenance unless `trust_local_builds` is enabled. Legacy binaries have no fallback. Source access remains bound. The frozen architecture explicitly makes artifacts depend on read-set authority. It does not state the same cloud-only rule for every artifact.

## Cases the parked reports require beyond their tests

The source reports describe residual failures that their committed tests do not close. Add these cases without treating the reports as passing tests.

| Evidence and reproducible case | New home and expected behavior |
|---|---|
| state/security re-review: a Widgets claimant changes its pipeline to `23:github.com/acme/gadgetsbuild`, then reaches Gadgets sizing through bare/scoped key confusion | `credentials_test.go:TestCredentials/credentials-holder-mutation-races/scoped_key_impersonation`. Sealed pipeline identity cannot change. A typed pipeline string cannot become another repo's sizing authority. |
| state/security re-review: a log request passes its fence, stalls while its body is read, then completes after another holder claims the job | `credentials_test.go:TestCredentials/credentials-holder-mutation-races/stalled_log_body`. Check append, segment-signing and manifest-sealing mutations after the stall. All refuse stale authority. |
| state/security re-review: source A generates deploy and rollback, while rollback waits on independent generated group B. B's expansion drops rollback's recovery-parent edge and ancestry to A | `lifecycle_test.go:TestLifecycle/lifecycle-generator-reconstruct-code/recovery_ancestry`. Expansion keeps required ancestry. Code replay can read declared A output and execute rollback after B resolves. `hermetic-replay/recovery_ancestry` proves reconstruction across source expansions and delivery of the persisted source failure to the body. Persistence, validation and SDK transport are fakes. Store insertion and native placement remain open. |
| storage report: bare memo writers include trigger hosts and local runs, while input-side checks cover only claim paths | `storage_test.go:TestStorage/storage-memo-and-depcache-scope/all_writers`. Every writer uses server-derived repo/ref/pipeline/job scope. Untrusted bare keys are refused. |
| storage report: a stored entry can name an origin from another ref, and retry/default lookup misses parent-run ancestry | `storage_test.go:TestStorage/storage-memo-and-depcache-scope/origin_and_lineage`. Check recorded origin authority at read time and retain the sealed original trust through child/retry lineage. |
| storage report: no-artifact-store retry copies digests without checking existence | `storage_test.go:TestStorage/storage-memo-materialization-failures/server_existence`. Remote materialization checks authoritative existence even when no local artifact backend is configured. |
| Frozen §8.1/A66: unrelated jobs A and B share a run. A uploads D, then B names D in a manifest | `storage_test.go:TestStorage/storage-reference-edge-laundering/unrelated_jobs`. Refuse with `forbidden` and reason `digest_not_readable` unless B gains an allowed edge or uploads its own verified copy. |
| run-input report: SDK defaults do not reach the controller, and generated IDs are not checked after initial planning | `credentials_test.go:TestCredentials/credentials-secret-identifiers/defaults_and_expansion`. Planning hosts register defaults. The shared validator checks both plans and expansions. |
| storage report: presence checks charge the object's full size against download caps | `storage_test.go:TestStorage/storage-hasmany-read-set/presence_query`. `HasMany` is a metadata query limited to the read set and transfers no object bytes. |
| storage report: reused/copied bytes can race collection | `storage_test.go:TestStorage/storage-reachability-and-retention-tails/reference_and_gc`. Keep referenced records, apply the 24-hour reachability/tombstone rule, and fail loudly or miss if bytes are gone. Do not require power-loss durability beyond §1.11. |

## W3 recovery port after amendment integration

The [shared steps continuation](w3-ingress-lease-continuation.md#shared-steps-and-alias-caller-continuation)
removes the retained executor's separate step scheduler. Both executors use the
common graph driver. `TestHostStepWindows` now drives the real Go SDK fixture
with a fake step writer, preserving the start-boundary and unknown-bound checks
and covering refused persistence before a body. It does not prove sealed native
step-window intake, store writes or placement. Those inventory obligations remain
open. Existing native step-policy and hermetic lifecycle components pass, and no
acceptance cell is added or activated by this port.

The [execution continuation](w3-execution-continuation.md) adds
`lifecycle-transition-replay-and-fencing/hermetic-store-executor-events`.
Five cases run the common claimed host and executor against real SQLite
transitions. Success and retry have one start and terminal event. Failed hooks
and output writes fail the job. A SQLite trigger refuses a result transaction,
leaving both terminal state and its event absent. Reconciliation adopts each
committed terminal result twice without another award or event. SDK execution,
engine verification, source acquisition and process delegation are fakes.
This ports the final-store start/retry/end and adoption assertions. It does not
complete the old dispatcher-event test's approval, expansion, artifact and
concurrency diagnostics or a native placement cell.

The [settlement continuation](w3-settlement-continuation.md) adds ten real Go SDK
process cases through the common executor. These port the result-read, parallel,
failure, Finally, optional and SkipIf parts of TestHostDrivesSteps. Step windows,
dry-run variants and stored step rows remain open. The existing generator-barrier
component cell now has twelve cases using the real owner scheduler, shared graph
projection/decider and strict validator. Its SDK and persistence are fakes.
No additional whole inventory row or native placement cell is claimed complete.

The [W3 services continuation](w3-services-continuation.md) adds alias selection,
complete service registration and native startup/cleanup environment proofs.
Existing service acceptance helpers exercise all eleven handlers. It closes no
additional parked row. The old dispatcher-event test still needs its final-store
port and is recorded as failed evidence.

`lifecycle-dependency-verdicts/hermetic-recovery-graph` ports
`TestRecoveryWaitsOnItsOwnDeps`, `TestRecoveryIsCancelledByAFailedDepOfItsOwn`
and reverses `TestRecoveryWaitsOnItsParentAlone`. The remote host projects the
wire snapshot and calls the real shared store decider. Recovery waits for its
ordinary prerequisites and complete generated groups. A failed prerequisite
settles it as cancelled with `upstream_failed`. Only the failed recovery parent
is exempt from ordinary failure propagation. Empty accepted groups release it.
These close the component findings in rows 101, 102 and 117. Native placement
and transactional lifecycle cells remain open.

The 89-line `internal/nodehost/onfailure_test.go` is deleted. Its cancelled-parent
and optional-recovery verdict checks move into the same acceptance fixture.
The obsolete assertion that recovery ignores its own dependencies is reversed.
The legacy recovery implementation remains until the native host cut.

`hermetic-recorded-failure` and `TestNodeExecutorRecordsVerifyTimeoutStage` cover
persisted action/verify failures, retry failures and successful clearing.
`FuzzExecutorFailureMask` generates hostile text and error codes before persistence.
`FuzzClaimedAwardFence` mutates claim identity and sealed settings and requires
rejection before renewal, source acquisition, SDK startup or result writes.

## W3 process authority component

The [native grant continuation](w3-native-grant-continuation.md) adds
`lifecycle-approval-decisions-and-owner-rights/hermetic-native-owner-failures`.
Four cases use the real Go SDK, shared evaluator/validator/hash/environment
builder and native pipes. Throwing generator, predicate and memo closures
cannot become topology, skip decisions or memo misses. `NoMemo` keeps the same
error. Every process revokes before cleanup and invokes no job body. The
95-line legacy eval regression file is removed after this port.

The native step-policy component's eleven cases now use native `Start` with
the sealed local environment and validated grant. The direct Git/native
claim-host test also uses the host's grant handoff, without changing session
configuration to install its revoker. `FuzzNativeStartupGrantIsolation` generates
changed bindings and failed delegation, requiring only validated grants to reach
environment construction and refusing stale configuration revokers.

Owner-seal and claimed-node cancellation cases preserve the reason supplied by
the store before source acquisition. The keeper's cancellation/watch case checks
the same reason, and `FuzzKeeperCancellationReason` generates arbitrary reasons
plus the absent-reason fallback.

The old host-trigger heartbeat loop and its 163-line HTTP regression file are
removed. `recovery-keeper-causes-and-backoff/hermetic-library` covers their
rejected-claim, full-lease-silence and operator-cancellation invariants through
`lease.Keeper`, without mutable timing globals. The CLI caller and retained
legacy host fixture consume that keeper. They still use the old HTTP renewal
shape until their W8/W9 lifecycle cut. This does not close a native placement cell.

The [process authority continuation](w3-authority-continuation.md) adds
`credentials-delegation-subset/hermetic-process-authority` using the real memory
adapter and registered summary handler. C2 validators, parent persistence and
resource relations are explicit fakes. The native credential matrix stays blocked.

The retained bound-admission, unique-bearer, invalid-token and revoke/close cases
from `internal/nodehost/routes_test.go` now also run against `ProcessAuthorities`.
The old HTTP tests remain until the actual C2 router supplies their replacement.
Pipe-process tests cover revocation after failed spawn, rejected hello and Close,
and require revoked authority before cleanup. The fence/revocation fuzz property
generates changed run, job, holder, generation and attempt identities.

The [process grant continuation](w3-grant-continuation.md) extends that component
through the common executor with real memory delegations. It covers successful
execution, three retry processes, changed binding, factory failure after issuance
and failed spawn. Separate executor and owner fuzz properties vary run, job,
bearer and factory failure. The native source/claim-host test uses the real
memory registry and pipe child for successful completion and lost authority.
No further old test is retired because the production router and runtime cut
remain open.

## Open questions for the lead

The [W3 diagnostics continuation](w3-diagnostics-continuation.md) ports the
step-log assertions to
`protocol_test.go:TestProtocol/protocol-step-failure-policies/hermetic-native-go-steps`.
Eleven cases use real Go SDK processes and the common executor. Seven SQLite
step cases verify committed diagnostic ordering and refused writes. The old
`TestHostDrivesSteps` window cases now use `TestNodeExecutorUsesSealedStepWindow`
and `TestNativeExecutorSteps/window`, driven by sealed StartAt and StopAt.
Only uses the owner/store test above. Native placement remains open.
The same continuation ports the old orchestrator foreign-holder refusal to
`lifecycle_test.go:TestLifecycle/lifecycle-transition-replay-and-fencing/hermetic-store-foreign-awards`.
Four SQLite cases refuse changed holder, capability, generation and ordinal
before renewal or execution and preserve state/events.

The [W3 step continuation](w3-step-continuation.md) ports the stored-step checks
from `TestHostDrivesSteps` through the common executor. Native Go processes prove
step states for ordering, parallel work, fail-fast, Finally, collect-all,
ContinueOnError, Optional, SkipIf, panic and dry-run. Six SQLite cases prove
attempt/generation persistence, blocked cancellation, retries and refused writes.
Step windows and host-generated diagnostic records remain open. The old test is
retained until those assertions have their final port.

1. **Short secret values in identifiers.** The old `below the floor` case accepts a two-character registered value. Decision 90 and §6.3 name no length exemption. The proposed port refuses every registered nonempty value in an ID. If the final registry excludes short values, the test must distinguish an unregistered value from an exemption in validation.
2. **Artifact provenance.** `TestDataDownloadClaimUsesCloudProvenance` refuses local artifacts as well as local binaries. Frozen §8.1 explicitly restricts binary execution by provenance and governs artifact reads through the read set. The proposed port uses read-set authority for artifacts and `trust_local_builds` for binaries. It does not add a new artifact restriction.
3. **Canonical writer checks.** The old source-grep tests pin implementation counts. The architecture requires one state writer and shared holder rules. The proposed port keeps the writer-owner assertion as a focused package architectural check and replaces helper call counts with transaction behavior. The lead can choose the repository's existing static-check mechanism without changing acceptance behavior.
4. **Coverage availability.** No required source is absent or unreadable. The old Postgres classification test needs a real Postgres run because the source report says SQLite did not establish that race. Skipping that backend must be reported as unverified coverage, not a pass.
5. **Unspecified route rights.** The frozen design fixes common capability boundaries but does not enumerate every retained API's account, service or operator grants. The route appendix lists the affected rights, including `/services`, as C2 assumptions requiring a source or lead decision. Those matrix cells remain launch blockers until their expected answers exist independently of the production policy.

## Review conditions

- Every old function and predecessor above has a disposition. A dropped mechanism still names the surviving behavioral concern when one exists.
- No test imports or invokes the old cache server, recreates agent-loss retry runs, depends on caller-supplied claim headers, or accepts duplicate lifecycle events.
- Final positive permission tests use valid requests. Expected grants come from independent architecture cases, not from the policy table's own output.
- Race cases coordinate at a real transaction or request-body boundary. A sleep that merely hopes to produce an ordering is insufficient evidence.
- Run package tests for local invariants and acceptance scenarios for cross-process behavior. The parked test counts are inventory counts, not launch coverage claims.

## W1 legacy planner retirement

The legacy dispatchRun fixture and its planner tests are removed with
`Store.AcceptPlan`. The retained concerns use the final contracts below.
These are SQLite or package proofs. No native placement or HTTP cell is closed
by this cut, and no additional archived T1 source identity is claimed closed.

| Retired tests or mechanism | Retained evidence |
| --- | --- |
| AcceptPlan insertion, one-plan replay and invalid-plan rollback | `TestPlanSealPersistsReplayAndExecutionSettings`, `TestPlanControllerHandoffKeepsExactReplay`, `TestAcceptedPlanUsesSharedStrictValidator` and `TestLifecycle/lifecycle-request-and-plan-seals/store-sqlite`. The SQLite cell also refuses the second insert and proves full rollback. |
| Required-secret scope diagnostics and optional absence | `TestPlanSecretPreflightNamesUnreadableScopes` checks every unreadable scope, the persisted detail, canonical error, one diagnostic, store time and successful admission of the readable required subset. Optional names are absent from that subset by the resolved contract. |
| Identifier secrecy and stale diagnostic writes | `TestAcceptedPlanIdentifierSecretsProperty` generates short, escaped and Unicode values across job, step and group IDs. `TestPreflightDiagnosticCannotOutlivePlanOwnerProperty` varies the owner's deadline across acceptance and diagnostic transactions. |
| Source depth validation and silent resource/retry clamping | Strict validation refuses negative source depth and explicit zero resources. Shared settings tests retain ceilings and resolved winners. Silent clamps, caller-selected spec hashes, v1 modifiers and RetryAuto are removed by the frozen design. |
| ReportAttempt output ownership and failed-attempt reuse | `TestBlobResultRecordsRejectForgedResultKeys` covers record team, namespace, job, run, attempt and generation. `TestResultRecordsFromAnEarlierAttemptCannotFinishARetry` refuses the earlier record before and after the retry ordinal advances. W1b's result fixture supplies SourceAttempt. |
| Child invocation deduplication and live-parent rights | `TestChildIntakePersistsLineageAndInvocationReplay` uses explicit intake keys. `TestChildIntakeCannotEscapeItsLiveParentProperty` varies parent run, job, team, generation, cancellation and terminal state. New invocation keys replace implicit live-child reuse. |
| Dependency verdicts, cancellation and application retry | Existing scheduling, cancellation-intent, holder-ordinal, leased-recovery and unit-billing tests retain these concerns through the final interfaces. The old settle-based automatic retries are removed. |
| Old launcher Job synchronization tests | `ReleaseUnit`, renewal and unit billing retain the store concerns. `TestLauncherSnapshotRetainsCurrentAwardThroughExchange`, `TestLauncherSnapshotFencingProperty`, `TestEngineLauncherSyncUsesStoreSnapshot` and `placement-run-host-to-launcher/store-sqlite` cover current award reconstruction and exact observed Job cleanup after reopening. Native Kubernetes adoption and teardown remain required. |

The selected package files are in `../w1-session19-tests.txt`. The session report
lists the remaining legacy surfaces and shared caller changes.

The removed `TestLaunchBilling_ReleaseRefundsAndTheFirstBeatBills` is covered by
`TestMeteredUnitUnstartedCloudReleaseRefundsReservation` and
`TestMeteredUnitCloudRenewalStartsComputeWithoutAnAttempt`. The first proves a
full refund before compute. The second proves that the first Cloud renewal opens
compute without consuming an execution attempt and that a later release settles
the job minimum. The old sync-side prohibition on handing back a billed Job is
replaced by the final `ReleaseUnit` transaction and immutable accounting.

## W1 run-row heartbeat retirement

`TouchRunHeartbeat`, `TouchNodeHeartbeat`, `Run.LastHeartbeatAt`, and the
independent scheduler and Kubernetes dispatcher timestamp loops are removed with
Doctor's heartbeat-age finalizer. The following ports replace
those mechanisms' retained concerns. They close no additional archived T1
source-function identity or native placement cell.

| Retired concern | Retained evidence |
| --- | --- |
| Owner liveness, heartbeat expiry and unrelated job activity | `TestOwnerLeaseAloneDeterminesLivenessProperty` varies 24 renewal timelines through real claims. The exact owner lease alone controls recovery. Expired renewal refuses without changing work. Remote renewal preserves only the remote claim. |
| Doctor orphan finalization and recent-run grace | `TestRecovery/recovery-local-orphans/store-sqlite` uses the elected reaper, store clock and owner execution state through reopening. Started local orphans fail in place. Unstarted owners requeue. A separate heartbeat-age grace no longer governs recovery. |
| Owner renewal persistence and stale-host refusal | `TestRecovery/recovery-unstarted-lapse/store-sqlite` checks committed owner renewal before expiry, then recovery/reclaim and stale renewal/plan refusal through reopening. |
| Viewer notifications | Existing unit renewal fields and `EngineController.Renew` retain the owner transport contract. The legacy node-claim observer test retains node watch delivery. Real owner watched-job delivery and W2's production route binding remain integration obligations. |

The two Kubernetes dispatch-loop tests retire with that loop. The final store
renewal tests retain revoked, expired and stale-fence refusal. Native host shutdown
on renewal loss still needs W8's lease-keeper integration. The PostgreSQL
execution-start contention fixture now races its existing activity mutation
against starts. It remains a legacy fixture and has not been run here. The node
ordering fixture still updates rows in reverse order through StartNode. The local
API contention fixture uses its existing activity route instead of the removed
touch route. These are caller migrations, not final native proofs.

The package selection is in `../w1-session21-tests.txt`.

## W1 raw node status and withdrawal retirement

`SetNodeStatus` and `RevokeNodeReady` are removed from Store, Tenant and Client
with their HTTP handlers. Debug pauses use activity detail. The raw readiness
publisher remains pending its caller migrations.

| Retired coverage | Final proof or remaining integration |
| --- | --- |
| `TestNodeClaim_RevokeOnlyWhenUnclaimed` and the HTTP revoke assertions | `TestReadinessWithdrawalPreservesHolderAndAuthorityProperty` varies 32 sequences of withdrawal, replay and republication before probing expired or forged owners and claimed, running or terminal jobs. Refused transactions preserve the complete snapshot and event list. |
| Ready withdrawal, holder exclusion and terminal replay | `TestLifecycle/lifecycle-transition-replay-and-fencing/store-sqlite` uses real admission, plan acceptance, claims and transitions. It checks withdrawal after reopening, stale-start refusal, cleared terminal claims, absorbing terminal state and one continuous event sequence. Enrollment is a contract fake. |
| Raw status writes in client, scope and tenant fixtures | Redundant status writes retire. Existing activity writes carry debug detail. W3's debug fixture still calls CreateRun against the final schema and fails on the removed args_json column before reaching a pause. Production debug lifecycle evidence remains open with that integration. |
| `TestRunNode_MissingJobFinalizesDoneNodeWithEmptyOutcome` | Retired with the raw setter's two-write terminal state. Final completion records outcome and terminal state atomically. The adjacent missing-Job tests retain the Kubernetes dispatch behavior. Native Job and launcher correlation remain W8/W9 obligations. |

The package selection is in `../w1-session22-tests.txt`. Native route and
platform cells remain pending.

## W1 raw dependency writer retirement

`UpdateNodeDeps` and its store/client/HTTP callers are removed. Generated
membership is accepted once and scheduling projects its dependencies.
The legacy host keeps its own membership for execution ordering.

| Assertion | Final evidence |
| --- | --- |
| Generated members hold their dependent until every member succeeds | `TestLifecycle/lifecycle-generator-barrier/store-sqlite` accepts two members and an empty group, reopens SQLite, completes the members separately and checks the dependent's release. |
| Expansion preserves initial declarations and terminal member state | The same cell checks unchanged initial plan/hash and dependent spec, terminal replay and atomic refusal of a different body. |
| Mutable dependency projections cannot alter accepted topology | `TestGeneratedDependenciesUseAcceptedTopologyProperty` generates 32 expansions with zero to six members and adversarial deps_json values, then compares the full graph, decisions and replay. |
| Old host resolves generated waits without a dependency writer | `TestGeneratedWaitsUsesMembershipWithoutRewritingDeclarations` checks unresolved, populated and empty groups through the host's wait calculation. |
| Expansion validation receives initial plan concurrency on submission and replay | `TestExpansionValidationReceivesAcceptedPlanConcurrency` checks the contract context. Enforcement remains with the shared validator. |

The store checks port the persisted-dependency expectation from
`TestExpansionFansOutAndHoldsItsWaiter`. That host test retains execution-order
assertions and expects its original declaration to stay unchanged. Its complete
runtime fixture still needs W3's final admission/lifecycle migration. No archived
T1 source-function identity or native cell is newly claimed complete.
## Egress persistence cutover

The three tests in `pkg/store/egress_test.go` are replaced by final-schema proofs
in `egress_accounting_runtime_test.go`. Round-trip, monotone usage and expiration
now use additive tenant and replica rows, public day/month reports and the leased
pruner. Invalid writer identity and negative increments fail through `AddUsage`.
The controller's old flush/retry/restore tests are replaced by request delivery,
tenant attribution, nil-meter persistence, short-write, restart-alarm and loud
write-failure proofs in `egress_accounting_internal_test.go`. Timer-flush and
closing-month backlog expectations retire with that implementation.
The SQLite replica acceptance cell reads public egress totals after reopening.
Native/platform cells remain unclaimed.

The raw default-team `Store.GrantCredits`, `Store.ListCreditCharges` and
`Store.AppendEventOnce` wrappers are retired. Remaining fixtures select the
intended Tenant explicitly. The raw-paid-grant property now probes the retained
tenant entry points. Deployment price and policy fixtures use Operator.
Existing final-schema settings proofs retain defaults, validation, corruption,
restart, atomic rollback and serialized table-update checks through that owner.
`TestStoreDataUnmeteredSettlementNeedsNoCalculator` closes the shared settlement
helper's dependency on a calculator for unmetered work. No native cell is newly
marked complete by these caller migrations.

## W1 launcher-only claim retirement

`Store.ClaimLaunch`, its request/result types and in-process scan cursor are
removed. The final queue has one transactional owner for runner and launcher
awards. The deleted tests have these dispositions.

| Retired test | Retained evidence |
| --- | --- |
| `TestClaimLaunch_BillsEveryClaimAndPagesPastUnpaidTeams` | `TestMeteredUnitQueuePassesOverUnaffordableWork` and `TestLauncherQueueReachesFundedWorkBehindLargeUnpaidQueue` retain funded selection, reservations and refusal of unfunded teams. Metering follows the authenticated root, independently of launcher placement. |
| `TestClaimLaunch_BoundsEachPollAndResumesWhereItStopped` | Replaced by `TestLauncherQueueReachesFundedWorkBehindLargeUnpaidQueue`, which reaches two funded jobs behind 257 unpaid jobs without local cursor state. The old implementation's per-poll scan budget is retired. |
| `TestClaimLaunch_ConcurrentPollsNeverRollTheResumePointBack` | Replaced by `TestLauncherConcurrentQueueClaimsEachAwardOnce`. Four concurrent polls reach every funded job once behind 65 unpaid jobs. No per-process resume point remains. Existing claim-fence properties retain stale-generation refusal. |
| `TestClaimLaunch_ReservesAtTheClaimAndBillsFromTheFirstRenewal` | `TestMeteredPlanRenewalExcludesSchedulingWait` retains reservation at award and computation from first renewal. Plan billing has zero minimum under C2. `TestMeteredUnitCloudRenewalStartsComputeWithoutAnAttempt` retains the separate job minimum. |
| `TestRepoDispatch_OnlyAnOptedInRepoSkipsTheTriggerPath` | Retired with mutable repository dispatch opt-in. `TestClaimUnitOwnerKindsAndEvalFences` and the intake/plan acceptance cells retain distinct owner kinds from sealed dispatch. |
| `TestRepoDispatch_ARetryOfAnOptedInRunIsControllerDispatched` | Mutable opt-in is retired. `TestAdmissionRetryCarriesSealsAndKeepsRetrierSeparate` retains the source seals, including dispatch. |

Controller fixture callers now provision a service root, call `ClaimUnit` and
exchange its bootstrap. `TestLauncherFixtureUsesCurrentUnitAward` exercises
plan and job awards through those real store methods. The bootstrap-origin
verifier and initial run rows are fixtures. Legacy controller HTTP scenarios
still need their final wire migration. The SQLite placement, billing and
lifecycle cells retain the final store evidence.

## W1 acceptance tests move beside the store

The lead's harness cleanup moves these checks from `internal/acceptance` into
`pkg/store/acceptance_*_test.go`. Each is a plain package test. The old transport
wrappers are removed, and the native scenario catalog is unchanged.

| Former SQLite check | Package test |
| --- | --- |
| lifecycle-request-and-plan-seals | `TestStorePlanSeals` |
| lifecycle-generator-barrier | `TestStoreGeneratedDependencies` |
| lifecycle-transition-replay-and-fencing | `TestStoreTransitions` |
| recovery-unstarted-lapse, owner portion | `TestStoreOwnerUnstartedRecovery` |
| surface-schedules-and-managed-timers | `TestStoreScheduleBoundary` |
| credentials-delegation-subset | `TestStoreCapabilityManagement` |
| credentials-operator-console-split | `TestStoreProvisioningOrigins` |
| credentials-session-digest-csrf | `TestStoreSessions` |

The moved checks open through `pkg/store/internal/storetest`. Sign-in and
invitation acceptance create accounts and membership. The planner enrolls a
machine, admits the run with a member session and claims the owner through
`ClaimUnit`. Expansion authority comes from `DelegateCapability`, including
its parent's expiry and deadline. No hand-written row insertion remains in
these moved files. Database triggers still inject failed writes, and UPDATEs
still expire leases and sessions for recovery checks.

`TestAccountAdmissionAuditRemainsOutsideTenantQueriesProperty` generates
provider identities and checks account audit rows have no tenant and never
appear in tenant event reads. It closes the signup foreign-key failure exposed
by using real account admission in the moved checks. Existing shared recovery
fixtures remain in `internal/acceptance` until their host, billing and remaining
store consumers migrate. No native cell is newly claimed complete.

## W1 planning-node and raw run-completion retirement

`Store.CreatePlanNode`, `Store.FinishRunIfActive`, `Tenant.FinishRunIfActive`,
`Store.FinishRunsIfActive` and `Tenant.FinishRunsIfActive` are removed.
Admission owns planning units. Completion uses `FinishRunAndUnit`.

| Removed check | Final evidence |
| --- | --- |
| `TestFinishRunIfActiveHasOneCommittedWinner` | `TestRunCompletionHasOneCommittedWinner` races eight completions across two SQLite handles. It observes one new commit and seven exact replays, then refuses changed terminal reports and preserves the original finish time. |
| `TestFinishRunIfActivePreservesMutationFences` | `TestRunCompletionAuthorityProperty` generates forty authority, owner, holder, run and tenant mutations. Invalid terminal states and all forged owners leave run, owner, billing, events and completion intents unchanged. |
| `TestFinishRunsIfActiveRollsBackEveryMemberWhenOneUpdateFails` | The cross-run batch API is removed. `TestRunCompletionFailureRollsBackOwnerAndVerdict` injects an event failure and proves the run and owner roll back together. Existing `TestFinishRunAndUnitCommitsTogetherAndReplays` also refuses failed owner and outbox writes. |
| `placement-ordered-matching/store-sqlite` | `TestStoreQueueOrder` covers FIFO and preference order, registered Actions scope, an unchanged blocked job and later eligible acquisition through real admission and plan acceptance. |
| `placement-granted-identity-and-capacity/store-sqlite` | `TestStoreClaimMatching` retains sixteen named/queued cases. Setup provisions actual machine and deployment-authorized metered roots. Refused claims preserve the snapshot, events, billing and completion intents. |

The two moved harness files and their placement wrappers are deleted.
The queue-count helper remains beside its recovery caller until that check moves.
The shared recovery fixture is still used by other store, host and billing tests.
The two controller fixtures lose their obsolete planning-node insertion.
Their legacy routes and log transport still require their owners' cutovers.
No native placement is newly unskipped.

## W1 secret tenant selection and unused fence readers

The seven default-team secret methods on Store are removed. Existing secret
CRUD, scope, limits, sampling and team-isolation checks use explicit Tenant
handles. Their assertions remain. Test fixtures use teststore.ForTeam so failed
tenant selection fails the fixture instead of dropping an error.

| Retired check | Retained evidence |
| --- | --- |
| Live/stale node lookup assertions in `TestClaimNamedNodeAwardsAnUnqueuedNode` | The lookup reader has no production caller and is removed. The legacy named fixture is also removed. The generated named-job property checks the current award and immutable accepted spec. `TestStoreClaimMatching` and `TestStoreLauncherSync` cover current award selection and immutable launcher correlation. |
| `TestNodeExecutionAttemptBelongsToLiveClaimOutlivesTheAttempt` | Both unused node-attempt readers and the old fixture are removed. `TestAuthenticationSnapshotRetainsFinalAttemptFenceAndLogState` covers final attempt state. Tagged-log tests retain claim fencing and tail grace. |
| `TestTriggerExecutionAttemptBelongsToLiveClaimOutlivesTheAttempt` | Both unused trigger-attempt readers are removed. Current tagged logs use `TestLogPersistenceOwnerUnitsHaveIndependentFences`, `TestLogPersistenceTriggerReadRechecksItsOwnerLease` and `TestLogPersistenceLaterNodeFinishCannotExtendAttemptGrace`. |

The current authentication, secret authority and store acceptance checks pass.
Legacy named-claim and execution-reporting fixtures still fail while inserting
removed runs.args_json. The old missing-run and missing-trigger lookups query
removed columns. These failures also reproduce with the session's changed Go
files restored from HEAD through an overlay. They remain part of the legacy
cutover, not passing final-schema evidence. No native cell is newly unskipped.

## W1 membership and table inventory ports

The identity role, last-owner and cross-team tests now use `UpdateMembership`
with real account sessions. The old runner-token minter-revocation test is
replaced by `TestMembershipRevokesOnlyTheAffectedMintersCredentialsProperty`.
It generates removal, promotion, demotion, existing rotation grace, other
creators, foreign teams, deployment roots and descendants. Separate tests cover
invitation withdrawal and rollback after a refused revocation. The raw member
mutation methods and token-table revocation helper are deleted.

`TestTeamPurgeIsolationProperty` generates adversarial tenant keys and compares
all surviving rows through two purges. It covers restrictive pins, blob/member
references, tagged logs, billing intervals, quota holds and team-bound operator
rows. This tests row deletion directly. Team-deletion admission and account
anonymization still use legacy paths and are not proven by this check.

The created-table checks and SQL guards use the final inventory. No native cell
or acceptance registry changes. `pool_access_rules` and `repos` have no production
writer. Their writer backlog is explicit rather than omitted from classification.

`TestGitHubAppRepositoryKeysSurviveRenamesProperty` generates two-team numeric
repository identities and renamed displays. Both subscription and automation
writers retain canonical keys, and subscription reads retain nanoseconds.
The legacy automation-admission test still fails on removed trigger columns.

Metric pagination, wall-clock guard and trigger-claim identity fixtures select
an explicit tenant after their unused default-team Store twins are removed.
Their legacy fixture behavior is not new final-schema execution evidence.

The membership session property covers refusal after the last membership and
transfer to an existing reader membership. Capability-backed sessions retain
their original team and end with the parent. The existing removed-member session
and authority-generation properties retain their refusal assertions.


## W1 issuance and report retirement

`TestRotatedTokenKeepsItsTeam` and `TestTokenRotationCarriesTheMeteredMarker`
are replaced by the existing capability rotation scope property and
`TestCapabilityRotationRetainsMeteredMachine`. The ambiguous-prefix lookup and
revocation checks remain. The removed raw rotation's prefix case has no final
analogue because capability prefixes have a primary key.

`TestGitHubRunnerCredentialMintIsBoundedAndSweepsExpiredOnes` is replaced by
`TestGitHubEnrollmentAndIssuanceLimitsSerialize` and
`TestGitHubActionsExpiryFreesQuotaWithoutDeletingAudit`. The final quota counts
live capabilities while retaining expired rows for their audit period. The
raw-token sweep assertion is dropped under the final retention contract.
`TestGitHubRunnerCredentialRemembersItsPush` is replaced by
`TestStoreActionsEnrollment`, `TestGitHubActionsRequiresCurrentEnrollmentProperty`
and the existing push-scope properties.

`TestGitHubRunnerAttemptRecordsWorkflowRun` is replaced by
`TestAttemptExecutorComesFromClaimAncestryProperty`. Its 48 cases check direct
and trigger-host awards, Actions workflow identity, machine subject identity,
node-wide attempt identity and forged executor metadata through claim, start
and the run-view read. Provisioning-origin proofs remain separate.

Concurrency callers use `ReleaseAndNotify` and consume its committed promotion
and follower results. The tenant mutation property still compares every foreign
holder row after release. The legacy fixture suites continue to fail at their
raw CreateRun calls against args_json. That failure reproduces against the
unmodified HEAD overlay. They are not passing final-schema evidence.

`TestOperatorUsageMetrics_WeeklyTractionFromOwnTables` is dropped with the
retired deployment usage report required by frozen architecture section 9.3.
The unused child-trigger reader's foreign-team assertion is removed with that
reader. Existing child-intake authorization and ancestry properties remain.
No native acceptance cell is newly activated.
The raw secret CRUD and size/count tests move to the current mutation suite.
`TestCredentialMutationsUseCurrentActorAndStoreClock` retains replacement,
actor, timestamp and missing-delete checks. The serialized-cap test retains
replacement at the cap. `TestCredentialMutationsSecretLimitsPreserveExistingValues`
checks oversized replacement rollback, the existing default-team exemption,
count and sorted listing. `TestCredentialMutationsDeletionPreservesOtherScopesProperty`
generates tenant and pipeline isolation checks. The unscoped-run fallback test
moves to `TestSecretReadClaimScopePrecedenceProperty` through current claim
authority. The old raw launcher mint refusal test is retired with its API.
Current award fences remain covered by
`TestAuthorityAwardRefusesForeignStaleAndUnreservedMeteredClaims`,
`TestClaimUnitRejectsForeignAndMissingRequirements` and the generated award
renewal properties. The old six-hour launcher token maximum is removed with
the legacy issuer. Legacy token transport tests seed presentation rows and
still require their final-schema fixture migration.

## W1 split trigger-queue retirement

`Store.ClaimNextTrigger` and `Store.ClaimNextTriggerFor` are removed with their
scan cursor and candidate decoder. Legacy fixtures that need a particular
trigger claim select that known ID through the retained named path. No
production fallback or forwarding method is added.

| Retired assertion | Retained evidence |
| --- | --- |
| `pkg/controller/claim_test.go:TestClaim_FIFOOrdering` | `TestQueuedTriggerRepositoryScopeProperty` verifies FIFO through real admission and `ClaimUnit`, with generated excluded queues longer than 64 entries. |
| `TestClaimNextTriggerForTakesOnlyAnAllowedRepositoryPastTheQueueHead` | The same property provisions a registered root, delegates its repository scope, finds eligible work past excluded runs, refuses the remaining queue and compares every excluded run snapshot. |
| Trigger portion of `TestConcurrentClaimsNeverCrossTeams` | `TestQueuedTriggerClaimsNeverCrossTeamsProperty` races eight workers across two real tenants and checks every award's tenant and uniqueness. The old test retains its node portion as `TestConcurrentNodeClaimsNeverCrossTeams`. |
| Legacy context-scoped GitHub trigger checks | `TestStoreActionsEnrollment`, `TestGitHubActionsRequiresCurrentEnrollmentProperty`, `TestStoreQueueOrder` and the push-ancestor properties use registered Actions authority and persisted seals. The legacy node assertions remain with that path's retirement. |
| Finished-attempt ownership reader | `TestAuthenticationSnapshotRetainsFinalAttemptFenceAndLogState`, current holder properties and tagged log persistence retain the final-attempt and tail-grace requirements. The unused reader and its fixture are deleted. |

The legacy financial fixture changes only replace queue selection with their
known trigger IDs. Their assertions and the accounting implementation are
unchanged. Those legacy fixtures are not new final-schema execution evidence.

## Named job and renewal hard cut

| Retired check | Retained evidence |
| --- | --- |
| `node_claim_named_test.go` and `node_claim_award_internal_test.go` | `TestNamedJobClaimCannotBypassReadinessOrAuthorityProperty` covers absent, unready, held and terminal jobs and terminal runs with real intake, plan acceptance, enrollment and transitions. Naming work never bypasses readiness. `ReconcileNode` owns authorized self-claims. |
| Named claim credit reservation and insufficient-funds checks | `TestMeteredUnitClaimAndReservationCommitTogether`, `TestMeteredUnitConcurrentClaimsCannotSpendTheSameCredit` and the unit billing lifecycle suite cover atomic reservation, refusal and settlement on the current named award. |
| `node_spec_hash_test.go` | The named-job property checks the accepted job's nonempty spec hash before and after claiming and completion. |
| Five `node_claim_test.go` heartbeat/principal/cap checks and `TestClaimToken_HeartbeatRenewsItUntilTheLeaseLapses` | `TestRenewUnitCannotMutateAStaleJobProperty` generates revoked, expired, superseded, wrong-holder, root and forged authorities. `TestRenewUnitRefusesStaleAndRootAuthority`, `TestRenewUnitNodeRequiresCommittedStart`, `TestRenewUnitRequiresBootstrapExchange` and owner-liveness properties retain current renewal, execution and deadline behavior. |
| Expired-node heartbeat assertion in `agent_loss_retry_test.go` | The renewal property covers exact-boundary expiry and refuses both execution observations without changing the row. The old execution-start assertion remains. |
| Older-credential renewal projection assertion in `agents_test.go` | The old heartbeat route is removed. `TestEngineClaimHTTPLauncherExchangesAndRenewsItsOwnUnit` verifies current claim exchange, identity-scoped renewal and refusal of both removed routes. Persisted host observations remain an integration task. |
| `TestANamedClaimSizingToClassNeedsAMeteredToken` | The sizes-to-class input is removed. `TestClaimUnitRepositoryEnrollmentAndMeteringDoNotGrantPlacement`, `TestClaimUnitGrantedProfileKeys` and `TestStoreClaimMatching` check that metering and observed labels cannot grant cloud placement. |

The remaining legacy finance and host fixtures receive only callsite migrations.
They still require their owners' current-schema setup and are not reported as
passing. The old PostgreSQL race test retains its queue branch. Native current
named-award locking evidence remains with the lead.

The legacy `TestNamedClaimRefusesAnotherTeamsNode`, the metered team's named
lookup assertion and `TestToolNodeIsNeverClaimedByAnAgentWithoutTheTool` are
ported to the named-job property and `TestStoreClaimMatching`. The property
provisions a real second tenant's machine capability before attempting the
foreign award. Both the complete job snapshot and the award/event counts remain
unchanged. The matching suite retains granted cloud-tool and repository checks.
## W3 store admission and lease-ending port

`internal/nodehost/local_admission_test.go:TestAdmissionLeaseEndings` replaces
its legacy admission_test.go body. All three cases execute the common node
executor and check the fenced terminal report, process close/cleanup and both
lease releases. Box-group eviction and rejected reattachment report superseded.
Daemon cancellation reports cancelled. The first port reproduced cancelled
reports in both supersession cases. The 39-line legacy function and its trailing
blank line are deleted. Other legacy admission tests remain because their full
terminal-state and waiter-cleanup assertions still need owner policy integration.
No acceptance cell is added, unskipped or marked complete.


## W3 real-store native test port

[The native store port report](../w3-native-store-port-progress.md) records the
retained admission, approval, scheduling, dispatch, generator, retry and masking
ports. Intake, owner and job awards, transitions, expansion delegation and capacity
use actual SQLite. SDK compute and selected foreign record boundaries remain
fakes. Atomic expansion replaces partial batch repair. A skipped source skips its
generated consumers, following frozen section 3.6.

The [debug port](../w3-debug-store-port-progress.md) replaces the four debug roots' old
scheduler/store fixture with native owner scheduling and real tenant intake,
claims and transitions. A fake implements the declared pause boundary. The
retained host assertions cover before/after/failure ordering, downstream release,
gate/recovery exceptions, cancellation and sealed timeout propagation. Actual
pause persistence, expiry, activity and event assertions still need W1's adapter.
The old cancellation assertion that rewrote a terminal job is removed because
terminal results are absorbing.

The [service and store continuation](../w3-service-store-port-progress.md) ports
HTTP service tests to the real policy router and process authorities over an
in-memory transport. Cleanup tests use native cleanup processes. Generated retry
tests use native scheduling and real tenant state. The report distinguishes the
target-side reuse assertions from unverified source materialization and lists
the retired legacy adapter assertions that still need native integration.

The selection test remains in its old fixture because sealed selection fields
are absent. The complete nodehost package passes after the retained store ports.
No native acceptance cell is added, unskipped or claimed complete.

## W3 shared process transport port

[The process transport cut](../w3-process-transport-cut-progress.md) removes
Session and StartSession. Their retained real-pipe tests, split-secret/EOF/CRLF
tests, describe withholding, generator fixtures, granted-identity fuzz property
and step-window fixture now use ProtocolSession. The private writeFailed unit
test retires with its method. Real-pipe SDK-exit and blocked-write tests remain.
The late-reply fixture replies after request-pipe shutdown and checks that
cancellation prevents successful settlement. Concurrent work uses distinct
jobs because duplicate live job/step requests are refused. This ports transport
evidence and does not establish native placement composition.

## W3 remote semaphore continuation

[The remote semaphore continuation](../w3-remote-semaphore-progress.md) removes
the unused Run.Add and Config.Execute surfaces and their unused test fixtures.
The previously ported native scheduling tests retain their assertions.
The parent-admission property now exercises local and remote jobs. Native
dispatch tests use real tenant claims and transitions with the existing fake
admission daemon. They check run/box lease retention for fresh, ready and held
jobs, absence of local compute reservations, cancellation intent and holder
drain before release. A generated-input property refuses foreign or regressed
remote job snapshots. No acceptance cell is added or activated.

## W3 consumer and intake cut

[The consumer cut](../w3-consumer-cut-progress.md) removes the raw consumer and
orphan hosting adapters. Detached receipt, replay and retained-environment
assertions now use the typed intake in run_detached_native_test.go. Alias risk
checks use the resolved declaration. The schedule launcher delegates source and
environment selection to ScheduleManager with a stable due-instant key.
The native nodehost suite remains green. Native workers, source overrides,
sealed step selections and usage/profile bindings remain open. No additional
archived inventory row or full native acceptance cell is claimed complete.

## W3 native store composition

`TestNativeHostFromIntakeThroughSDKAndPersistedCompletion` replaces
`TestNativeHostRunsSharedTransportForOwnerAndClaimedWork` and its fake owner
state machine. Both roles use real tenant intake, claims, acceptance, starts,
step/result writes and completion. Acquired local Git source, captured
environments, the Go SDK and process-authority validators are real. The claimed
case runs the same accepted work under its own keeper, then resumes its owner
to settle the run. The typed SDK result now crosses NewSignedStore, signed
filesystem PUT, verified CAS publication, receipt settlement and record commit.
The test checks the exact stored result and removal of staged files. One scripted
DataSigner connects the real tenant and object operations. Engine resolution,
installation, admission and the in-process HTTP transport remain test adapters.
Full placement cells remain blocked.

The owner/claimed composition now registers masks through the actual PolicyRouter,
shared authenticator and SQLite parent relations instead of a registrar fake.
`TestProcessAuthorityRoutesUsePersistedParentRelations` reproduces the former
401 and verifies registration, generated selector/fence refusals and revocation
before terminal reporting. Two duplicate authority-test fakes are removed in
favor of the existing scripted parent and binding callback. This adds component
evidence without activating a native placement cell or claiming another archived
test port.

Scope-label tests and their tuple-injectivity property move beside the shared
codec in nodehost. The CLI queue fixture uses that codec. The state-path test
moves to paths when the orchestrator aliases are deleted. These preserve
existing assertions and claim no additional archived source identity.

W2 account discovery continuation: the GET /me branch of
TestIdentityRoutesRefuseABearerToken is ported to
TestAccountReadHTTPRefusesUnrelatedAuthority. It distinguishes a live unrelated
bearer (403) from dead authority (401). The POST /teams branch remains with
its unmigrated mutation. TestAccountReadHTTPDoesNotRenewOrSwitchSession uses
real SQLite session persistence. TestAccountReadHTTPIdentityProperty generates
forged selectors against all three account reads. TestCreditSettingsReadHTTP
covers the final settings policy and authenticated feature availability.
The two profile tests reintroduced by the integration conflict remain retired
in favor of the existing sizing HTTP ports. No native cell is unskipped.

## W2 OIDC mint retirement

The twelve legacy controller OIDC tests and their token/trigger-row fixtures
are replaced by TestOIDCMintHTTPIdentityAndExpiryProperty, Refusals,
InputAndBudget, ClientAndKeyRotation and CommittedSubjects in
`tests/acceptance/oidc_mint_http_test.go`, plus the existing issuer and public
metadata tests. Real RS256/JWKS verification, the existing client, stable
repository-specific subjects, all event/ref spellings, PR/tag subjects,
audience validation, subject-injection refusal, 30-token isolation, distinct
JTIs, disabled issuer, expiry and tampering are retained. Authority tests now
refuse roots and accept only live bound oidc.mint awards/delegates.

These are component tests against the actual registered HTTP handler and a
committed-fact reader fake. C1 must prove original verified-event selection,
schedule provenance, inheritance, registered runner identity, complete ancestry
expiry and transactional cancellation/fencing in ReadOIDCFacts. The obsolete six-line dispatched-claim OIDC refusal assertion in
TestClaimRun_SecretsReachOnlyALiveWorkClaimAndOnlyDeclaredNames is reversed by
the successful awarded-claim mint tests. The old
Trigger/ClaimedRun readers are deleted from W2. No native credential matrix
cell or production C1 identity projection is claimed complete by this port.

## W2 concurrency transport cut

TestObserveSlotMapsMissingHolder and TestHeartbeatSlotMapsLostOwnership move to
`pkg/controller/client/concurrency_test.go:TestConcurrencyClientDomainErrors`.
The tests now use in-process transport and canonical errors. They preserve
errors.Is for the store sentinels and errors.As for apierr.Error. Untyped legacy
error bodies cannot become lost-ownership results. TestConcurrencyClientDistinguishesAdmissionFromRateLimits
pins the refusal of a canonical 429 as an admission result.

TestConcurrencyHTTPTransactions exercises the eight actual group registrations
with job, trigger and plan authority and fake committing adapters. The generated
TestConcurrencyHTTPBindingProperty varies body/query run, job, holder and
encoded group selectors against independent binding facts. Refused requests
never enter the transaction adapter. TestConcurrencyHTTPRefusals covers current
liveness, cancellation-sensitive acquire, cancellation-reporting release,
stale generation, transaction refusal, safe errors, tenant selection, malformed
inputs and unsupported methods. The client round trip uses final paths and job
fields. The retired-route property reaches no adapter.

The old claim_run_test.go concurrency properties and the cross-team leader
resolution test remain unported native requirements. Fake adapters do not
establish sealed group or memo policy, foreign leader resolution, lease ceilings,
inheritance refusal or persistence races. W1 must implement C2's authority-bearing
ConcurrencyStore and SlotKey/SlotHolderID checker before those cells can close.
No native acceptance matrix cell is newly completed in this continuation.

W2's typed step continuation updates the existing TestEngineStepHTTP and
reporting-authority cases to StepReportRequest. TestEngineTypedStepHTTP uses
the actual typed engine client. TestEngineTypedStepAttemptProperty generates
captured and current ordinals and nanosecond timestamps for job and owner
reports. Mismatched ordinals return lock_held without entering RecordStep.
These are HTTP component proofs with fake transactions. The legacy
StateStoreOld step callers and native placement/store composition remain open.


## W2 reporting credential cut

TestCapacityReadUsesRequestTenantProperty now calls the final authorized sizing
handler and proves that a forged legacy Principal and request selectors cannot
replace Authorized.Target.Team. Missing authority and an unregistered team have
separate exact canonical refusals. The existing anonymous-bootstrap property
uses /runners after the /agents cut. The retained agents liveness fixture passes
the explicit target to its read handler.

TestReportingRouteAuthorityProperty generates revoked, expired, wrong-audience,
nonmember, deployment and wrong-grant presentations for all 22 reporting read
registrations. It exercises the real verifier and policy selector against
committed-fact fakes, with negative requests repeated through Server.Handler.
TestReportingRetiredRoutes covers the removed read paths. The actual controller
client's NodeLogs read, stream, completeness and whole-run requests retain exact
escaped selectors in TestLogReaderUsesFinalRoutesAndAuthenticatedTransport. The retired NodeLogs client fixture is deleted with that API. The prior raw-event method
refusal property still requires 405 with Allow after registration moves.

The 258-line independent expected route table moved from
internal/acceptance/credential_routes.go to
pkg/controller/credential_route_expectations_test.go. Its independent comparison,
mutation sensitivity and alternative-order tests moved beside it. The executable
policy-alternative tests use the runtime rows, while the moved tests retain the
hand-written permission oracle. No oracle is regenerated from implementation.
No scenario, Harness method, registry or skipped native cell was added.

These changes prove authorization transport and the real SQLite sizing projection.
They do not establish final-schema run/reporting SQL or native composed log,
receipt and runner-read implementations. Those retained tenant reads still need
their owning store integration. The legacy verifier and remaining schedules,
team mutations and other registrations are unfinished work.


## W2 schedule and review credential continuation

Thirteen legacy controller cron HTTP tests are ported to
TestScheduleHTTPReadWriteAndReplay, TestScheduleHTTPOverrideAndWithdrawal,
TestScheduleHTTPInvalidInputAndBackend and
TestScheduleHTTPRetainedHistoryAndCounts, plus the expanded
TestReportingRouteAuthorityProperty. The replaced test functions are deleted;
the old shared fixture remains for unported legacy callers. The ports preserve
stored metadata, withdrawal, invalid URL/cadence/branch refusal, pause/resume,
override/reset, explicit empty arguments and zero catch-up, whole retained
history, health before the first tick, deletion and capability refusal.

TestRunControlHTTPAuthorityProperty and Refusals exercise verified team/run/actor
forwarding and rejected approver, team, lifecycle and replay-body selectors.
TestBusinessRetiredRoutesAndExactMethods checks the crons family and raw gate
request are absent, with canonical methods on shared read/write paths.
TestMetricsHTTPDeploymentPolicy exercises the deployment boundary.

These are component proofs with fake authentication facts and committing
writers. Schedule metadata/history use real SQLite. Native gate creation,
run-now intake/provenance, per-team and admission limits, atomic replay and
masking at the real committing edge remain W1/W3 integration obligations.
The surviving legacy run-now, cron-team, compute-limit and approval fixtures
must be ported to final intake and lifecycle before their old state APIs go.
No native matrix cell is newly complete in this continuation.

## W2 final business routes and verifier retirement

The final seventeen legacy business registrations are migrated. The token-table
Authenticator and its positive/negative caches, here/Actions fences, legacy
tenant route table, requireScope and redundant cookie API middleware are deleted.
Their cache implementation tests and open/admin-superset expectations retire.
No argon2 or cached-verification behavior remains to port. The common capability
authentication and policy suites retain missing/malformed/expired/revoked,
ancestry/membership, grant and tenant refusals. TestControllerRefusesAnonymousBeforeBootstrapProperty
retains the actual Server startup refusal, without a separate enable call.

TestReportingRouteAuthorityProperty now exercises all seventeen route policies,
including denied calls through actual Server registrations. The generated
TestTeamWriteHTTPAuthorityProperty covers six team/run writes and App unbind,
trigger deletion and automation withdrawal through committing fakes. It checks
verified team/actor forwarding, wrong-team adapter refusal, changed-authority
conflicts and private backend errors. TestBillingPageHTTPAuthorityAndReplayProperty
covers all three hosted-page kinds, reservation-before-provider order, completion,
replay without provider access, provider failure, committing conflict and budget
forwarding. Malformed/forged bodies reach no successful mutation. Neither
property proves real transaction races, financial reservation persistence,
provider delivery or App consent after discovery.

The four dashboard self-service account-deletion tests are removed with their
retired helper and UI. Console deletion and last-owner handling remain covered
by the earlier real console-route port. Teams and billing HTTP fixtures now use
canonical failures and the required replay headers. Old broad team/billing/App
fixtures still need migration off retired token/trigger APIs. No native matrix
cell is newly enabled and no fake-backed result is native integration evidence.

## Browser connection and console continuation

W2 moves the seven GitHub App browser registrations to PolicyRouter and deletes
the old browser Principal lookup, renewal wrapper and standalone form middleware.
TestGitHubBrowserConnectionAuthorityProperty, StateProperty, ProviderAndReplay,
CommitRefusal and RequiresTransaction cover the real HTTP/proof/provider adapter.
The provider and committing writer are fake. They port the connection ownership,
forged/foreign state, admin proof and single-use concerns from github_app_test.go.
That broad socket fixture still needs its other App/trigger callers migrated.

ConsoleWriteHTTPAuthorityProperty covers all nine business writes against the
new ConsoleWriter. The executable TestOperatorHTTPForwardsActionsAndAudits and
TestConsoleAccountDeletionRetainsLastOwnerConflict inject an explicit transaction
fake over SQLite. These prove HTTP forwarding, output, audit and error mapping.
They do not prove the real committing authority checks. W1 owns those checks.

TestSettleFinishedNodeClearsTheWindowForAnUnmeteredFinisher is removed with its
unused finalizer. TestMeteredUnitNodeLifecycleUsesImmutableAward,
TestMeteredUnitSettlementFailureRollsBackResult and
TestMeteredUnitStaleReportCannotSettleReplacementProperty pass against the final
store and retain its award-billing, settlement rollback and stale-report fencing.
An anonymous/unmetered HTTP request no longer finalizes another claim's window.
No native credential-matrix cell is newly completed by this continuation.


## W2 final credential helper and reporting cut

The remaining legacy Principal API, Scope exports, tenant fallback, unused
metering wrappers and live-log HTTP handlers are deleted. TestRequestTenantRefusesACallerWithNoRegisteredTeam
and TestRunTeamRefusesAnotherTeamsRun retire with those helpers. Final route
properties retain the authenticated-tenant boundary. Tenant cache expiry remains.
TestMetricTimestamp is ported to the final HTTP path with a fake committing
writer, preserving malformed, omitted and explicit timestamp coverage and adding
nanosecond-range refusal. TestMetricsHTTPAuthorityProperty, ValidationAndLimit
cover live authority, selected job/tenant, committing conflicts, canonical errors,
missing/foreign adapters and invalid metric/usage bodies. The client transport
fixture TestMetricAndUsageClientsUseFinalJobRoutes is retired with the removed Client API. TestMetricTimestamp and TestMetricsHTTPReadsPagedSamplesFromAuthorizedTenant retain the HTTP timestamp, accounting and read assertions. A final native metric/usage client contract is still required. These tests do not prove persistence.

The old socket-based request-budget tests and claim-budget test are ported to
real authenticated /runners/claim HTTP with SQLite. The ports cover empty-poll
charges, free awards, independent runner names, name caps, an unlimited default,
fleet cadence and the existing idle-advice/enforcement cases. The heartbeat
property varies headers while retaining one authenticated holder. The obsolete
trigger-schema fixture is removed. The advice-reset test calls the queue-activity
edge directly. Production admission integration stays covered by its owner.

The old audit identity fixtures and pending-offer test are removed. Final-policy
HTTP ports cover credential/secret-path privacy with generated inputs, sensitive
reads, authenticated denials, forged credentials, run/job path identifiers,
request-ID replacement and successful empty-poll suppression. The existing span
and budget-key tests use capability.Principal. The final log-concurrency test
checks the canonical rate_limited envelope before the handler runs.

The retired direct-download wrapper test is removed. TestDataReadHTTPDownload
and DownloadFailures retain signed-data charge-before-sign behavior, and the
store's download-charge tests retain tenant/replica accounting. No real download
charging adapter or native placement matrix is newly proved by this cut.

Five operator settings writes use a fake committing writer in their existing
round-trip, invalid-body and private-failure tests. CommitAuthorityProperty adds
current-authority/target forwarding and missing-writer refusal. The signup fake
now commits one request once, rather than treating mode and limits as separate
writes. TestServerPolicyConsoleUsesDirectSessionAndConfiguredGate uses real
SQLite session and CSRF persistence through the production console adapter and a
fake committing writer. The roleless-session and durable transaction gates remain
W1 work. No new scenario catalog or harness cell was added.


## W2 member, claim and GitHub identity fixture ports

The legacy tests in `pkg/controller/team_tokens_test.go` retire with the combined
team-token route. `TestCredentialMemberHTTPRoleCeilingProperty` exercises every
role ceiling and exact requested grants through real SQLite issuance.
`TestCredentialMemberHTTPMembershipAndSessionLifecycle` verifies root independence
from the minting session, membership removal and current-role narrowing through
real HTTP delegation. Owner member roots may request team.admin under the final
role contract. A reader retains read authority after demotion and cannot delegate
runs.write. Narrow delegation is permitted. The old terminal/no-delegation rule,
swt_ encoding, setup-command response and legacy token compatibility assertions
are removed because they describe the retired API.

Member metadata ownership, expiry and rotation are covered by
`TestCapabilityManagementHTTPReads`, `ScopeProperty`, `ReplacementProperty` and
`MissingAdaptersAndCSRF`. Machine enrollment, allowed grant subsets, trusted
origin, expiry and refusal are covered by `TestRootProvisioningHTTPOrigin`,
`Refusals`, `Input`, `ExpiryAndCSRF` and `ClaimGrantCeilings`. These management and
enrollment tests use explicit transaction fakes. W1 still owns registry limits,
origin invalidation, personal inventory and rotation persistence.

The five legacy tests in `pkg/controller/runner_claim_test.go` are replaced by
`TestCredentialClaimHTTPAncestryAndFenceProperty`, which obtains real node and
trigger awards, reads their run, refuses root/claim grant confusion, and refuses
revoked ancestry, replacement fences and terminal holders despite forged headers.
`TestCredentialStartedClaimHTTPConsumesContractAttempt` proves the positive
annotation, sibling refusal and committing revocation check using the declared
current-attempt authentication snapshot and real SQLite writes. Its snapshot is
an explicit fake because W1's real snapshot retains the awarded attempt zero
after start advances the job to attempt one. Real post-start HTTP integration
remains open. Secret selection and refusal are covered by `TestSecretReadHTTPUsesPolicyAndCommittedRow`,
`AuthorityProperty`, `BindingProperty` and `RefusalsNeverOpenValues`. Their reader
is fake. Release-audit persistence remains W1 work. The old operator secret
bypass and 403-for-dead-claim assertions retire.

The five tests in `pkg/controller/identity_github_test.go` move to the final OAuth
start/callback and identity-link surfaces. `TestOAuthHTTPGitHubPrimaryEmailProperty`
generates hostile profile, unverified-primary and verified-secondary addresses.
None issues a session. `TestOAuthHTTPGitHubUsesNumericIdentityProperty` reads real
committed identities with arbitrary display names and distinguishes two numeric
subjects. OAuth start checks the exact read:user/user:email scope.
`TestOAuthHTTPSessionAndReplay`, `ProviderAndStoreRefusals`, `ProofProperty` and
`TestIdentityLinkHTTPCommitsVerifiedIdentityOnce` retain session/proof/provider
and link transport behavior. The provider and link writer are fake.

New-account creation, personal-space naming and same-email provider linking
remain real W1 integrations. Targeted existing W1 tests
`TestIdentitySignInCreatesOnePersonalSpace`,
`TestIdentitySecondSubjectFromTheSameProviderGetsItsOwnAccount` and
`TestSignInRefusesANewProviderOnAnExistingAccountsAddress` all fail at
`ResolveSignIn` with an admission foreign-key constraint. No new-account or
link-persistence success is claimed by the component ports.

The remaining token/claim helper declarations support other unported fixtures.
The cross-run, identity, team and log fixtures still need their final ports.
No native cell, scenario catalog, Harness or boundary registry changed.

## W2 identity and team HTTP fixture ports

The 16 tests in `pkg/controller/identity_signin_test.go`, 14 tests in
`identity_team_test.go` and five tests in `identity_link_test.go` are removed.
The three files lose 1,019 lines. Their account-response and shared team fixture
declarations move into `identity_fixture_test.go`, because other retained
integration fixtures use them. The unused license splitter is removed there.

| Retired HTTP assertions | Final evidence and remaining integration |
| --- | --- |
| Google sign-in, repeated sign-in, recycled email and provider subject selection | `TestGoogleOAuthHTTPSubjectAndProofProperty` verifies signed Google ID tokens through the real OAuth handler and real SQLite sessions. Two existing accounts share an email and have distinct provider subjects. Arbitrary names never choose the account. Fresh session presentations and identity are checked. New-account admission, personal-space creation and slug collisions remain in W1's `TestIdentitySignInCreatesOnePersonalSpace`, `TestIdentitySecondSubjectFromTheSameProviderGetsItsOwnAccount` and `TestIdentityConcurrentFirstSignInsShareALocalPart`. |
| Unverified Google email, another flow's verifier, redirect allowlist | The Google property refuses unverified signed claims and unrelated PKCE verifiers without changing session state. `TestOAuthHTTPProviderAndStoreRefusals` and `TestOAuthHTTPProofProperty` retain redirect and browser-proof refusal. |
| Public capabilities and unusable/tampered licenses | `TestOAuthHTTPLicenseControlsPublicProviders` checks both providers through real HTTP registration for absent, valid, expired, wrong-key, featureless, changed-signature and changed-payload licenses. Invalid licenses offer no provider and refuse OAuth start before provider I/O. |
| Roleless services, unknown sessions, lost license and token-parented sessions | `TestAccountRolelessHTTPKeepsOnlyAccountAuthorityProperty` permits account reads and refuses services, members and runners despite caller team headers. Its snapshot is fake. `TestSessionPolicyHTTPRefusals`, `TestSessionRoutePreservesCapabilityScopeProperty` and `TestLoginHTTPAccountCapabilitySession` retain real session, scope, digest and license checks. W1 still supplies durable roleless sessions. |
| Team creation, duplicate/reserved slugs and account team cap | `TestAccountMutationHTTPIdentityProperty`, `TestAccountMutationHTTPInputAndMissingWriter` and `TestAccountTeamHTTPTransactionGuards` retain authenticated identity, canonical slugs, exact transaction forwarding and safe refusals. Account writers are fakes. W1 retains slug collision, account-cap and atomic owner/session-selection requirements. |
| Invitation creation, matching email, replay/expiry and team switching | `TestTeamWriteHTTPAuthorityProperty`, `TestAccountReadHTTPIdentityProperty`, `TestAccountMutationHTTPIdentityProperty` and `TestAccountTeamHTTPTransactionGuards` retain authority forwarding, invitation visibility and committing-writer error propagation. Closed invitations now use canonical 409. W1's invitation transaction must prove verified-email matching, one-use state and atomic membership/session selection. |
| Reader/editor restrictions, role changes, last owner, tenant boundary and demotion | `TestAccountMembershipHTTPCurrentRoleProperty` uses real SQLite authentication and binding checks for owner/editor/reader grants, then a fake committing writer. `TestAuthorityMutationHTTPStoreGuards`, `TestAuthorityMemberDirectoryHTTP`, `TestTeamWriteHTTPAuthorityProperty` and `TestCredentialMemberHTTPMembershipAndSessionLifecycle` retain last-owner conflicts, current role, tenant and demotion checks. Last-owner refusal is canonical 409. |
| Machine root scope, owner revocation, reader/name refusal and per-team cap | Existing `TestRootProvisioningHTTPAuthorityProperty`, `TestRootProvisioningHTTPInput`, `TestRootProvisioningHTTPOrigin`, `TestAuthorityMutationHTTP` and the member lifecycle property replace the retired combined `/team/tokens` fixture. Enrollment and inventory writers remain fakes. W1 still must prove registration caps/races, ownership, origin invalidation and rotation persistence. |
| Removed member's personal-team fallback and last-team removal | The roleless property preserves account-only policy. W1's `TestIdentityLastOwnerCannotLeaveAndAMemberKeepsTheirTeams` and `TestIdentityUserWithNoTeamGetsAFreshSpace` retain fallback and durable session-selection requirements. The C2 snapshot does not prove their persistence. |
| Linking a provider with another address, identity ownership conflicts and recent sign-in | `TestIdentityLinkHTTPAddressDoesNotSelectAccountProperty`, `TestIdentityLinkHTTPCommittedIdentityConflicts`, `TestIdentityLinkHTTPCommitsVerifiedIdentityOnce` and `TestIdentityLinkHTTPRefusals` retain exact account/provider/subject handoff, cross-account refusal, one-use transport and recent-sign-in checks. The link writer is fake. |
| Unlinking the last method, reauthentication, future sign-in and retained App installation | `TestAccountUnlinkHTTPPreservesLastMethodAndReauthenticationRefusals` checks canonical reasons and unchanged real session state on writer refusal. `TestAccountMutationHTTPIdentityProperty` checks exact unlink authority. W1's `TestUnlinkIdentityKeepsTheLastSignInMethod` and `TestUnlinkedSignInNoLongerReachesTheAccount` retain durable method/session effects. App installation retention remains an integration obligation. |

The three W1 signup tests selected in the preceding continuation still fail with
an admission foreign-key error. Removing the explicit contract snapshot from
`TestCredentialStartedClaimHTTPConsumesContractAttempt` still makes its first
post-start annotation fail with 401. The fake-backed ports do not close either
integration gap. Cross-run and log fixtures remain pending the contracts listed
in `../W2-identity-ports-handoff.md`. No native matrix cell is newly completed.

The duplicate controller S3 log presigner and its private client are deleted.
`TestS3LogSegmentURLsSignTheSizeAndMD5` retires with that signer. W7's
`TestS3LogSigningBindsCreateOnlyHeader` and `TestS3LogSigningRejectsBeforeSigning`
check the final immutable-slot signer. Upload bytes are checked after transfer
by `TestLogUploadReplayVerifiesExactObject` and `TestLogUploadReplayRefusals`.
The legacy `TestDiskLogSegmentURLsAcceptOnlyTheSignedBytes` is replaced by
`TestSignedLogKeyCannotBeReplaced` and `TestDataStorageSignedFilesystemRoutes`
through actual controller transfer registration, plus the shared filesystem
tagged-log round trip and retry tests. Batch signing no longer binds the future
payload to MD5. Exact stored SHA-256 and size precede record commit.
The controller's duplicate log-limit constants are removed, and the retained
deletion delay and legacy fixture assertions use `internal/limits` values.
Final log-signing/verification HTTP composition remains open. These signer
ports do not prove a final controller log mutation or native log-tail cell.


## W2 integration-merge recovery and fixture retirement

`TestCredentialMachineHTTPCannotBecomeAMemberProperty` replaces the two
remaining declared-repository credential tests. It uses real SQLite capability
lookup and actual Server routes. Generated machine grants and caller hints
cannot authorize intake, retry or cancellation as a team member. The test also
checks that refusal creates no run and records no cancellation. The positive
member retry path remains in `TestEngineLifecycleHTTP`. The obsolete
runner_scopes_test.go helper file is deleted after its last callers are ported.

`TestCredentialMemberHTTPEncodedPathsPreserveTenantProperty` replaces the two
legacy URL-path tenant tests. It issues real member capabilities in two teams,
generates percent-encoded literal and identifier segments, checks positive own
job reads, and refuses foreign reads without disclosing job data. It also checks
nine noncanonical paths and unchanged persisted run state. A live foreign member
now receives the final policy's canonical 403. The old 404 expectation is not
copied. The raw invalid-HTTP-escape assertion retires because it tests Go's HTTP
parser before Sparkwing receives a request.

The seven token-table here-runner tests are deleted. Their final evidence is
`TestAdmissionHereAwardRetainsRequestingAuthority`,
`TestClaimUnitHereWindowAndReclaim`,
`TestAuthenticationSnapshotHereClaimHistorySurvivesRequeue`,
`TestLocalAndHereClaimsDoNotInventAnExecutionDeadline`,
`TestAuthenticationHereWindowAndSessionParent`, `TestHereClaimStaysNamedAcrossReclaim`,
`TestHereResumeClaimContract` and `TestHereClaimResponseLossIsNotRetried`.
`TestSizingHTTPRefusals` retains the bound-pipeline boundary. The old fixed
six-hour lifetime, machine-token scope table, token cache restart and
no-reclaim expectations retire with their APIs. The final here selection has
no reason field. These owner/component checks do not complete a native
credential-matrix cell.

The retired client fixtures `TestNodeLogReaderUsesJobRoutes`,
`TestMetricAndUsageClientsUseFinalJobRoutes` and
`TestConcurrencyHTTPClientUsesGroupRoutes` are removed. Final log transport
coverage is `TestLogReaderUsesFinalRoutesAndAuthenticatedTransport`. Final queue
client tests cover its retained read/priority contract. The existing
`TestConcurrencyHTTPTransactions`, `TestConcurrencyHTTPRefusals`, `TestMetricTimestamp`,
`TestMetricsHTTPAuthorityProperty`, `TestMetricsHTTPValidationAndLimit` and
`TestMetricsHTTPReadsPagedSamplesFromAuthorizedTenant` retain the HTTP concerns.
The final native metric/usage client contract and log mutation composition remain
cross-area work. `TestOIDCMintHTTPKeyRotation` keeps the four-stage JWKS rotation
proof through actual HTTP without the retired Client constructor.

`TestUnregisteredRouteAnswersCanonicalNotFound` replaces the obsolete string
error-envelope expectation. The unused `WriteUnsupportedRoute` and
`UnsupportedRouteError` production API is deleted. Remaining legacy tenant
fixtures check the canonical route-miss message until their persistence ports
are supplied. No compatibility symbol is retained.

The final independent route expectation table remains beside the route tests in
pkg/controller/credential_route_expectations_test.go. The internal/acceptance
copy remains absent. The catalog, port destinations and route declaration,
ordering and drift checks pass. No scenario catalog, Harness or boundary registry
entry is added.

## W1 seeded bootstrap and raw claim retirement

| Retired test | Retained evidence |
| --- | --- |
| `TestCreateTokenIfNoneExist_WritesOnceOnEmptyTable`, `TestCreateTokenIfNoneExist_SkipsWhenTheSameCredentialIsAlreadyStored` | `TestBootstrapSeedIsOnceOnlyProperty`, `TestBootstrapGateSurvivesConcurrentReplicasAndRestart` and `TestBootstrapDifferentSeedsRaceForOneGate` exercise the real issuing transaction. They retain only a digest and prefix, permit a live no-op and refuse another seed after issuance. |
| `TestCreateTokenIfNoneExist_WritesOverTokensThatNoLongerAuthenticate` | Its rebootstrap behavior retires with the first-administrator gate. `TestBootstrapSeedIsOnceOnlyProperty` verifies that revocation and retention never reopen the gate. |
| `TestCreateTokenIfNoneExist_RefusesMalformedRaw` | `TestIssueRejectsMalformedBootstrapPresentations` checks the adapter and direct Issue path. `TestBootstrapSeedCannotEnterOtherOriginsProperty` generates seeds across every non-bootstrap origin. `TestValidateRawToken_AcceptsAMintedShape` remains for legacy token constructors. |
| `TestClaimToken_LiveClaimAuthorizesEveryClassBoundToItsClaim`, `TestClaimToken_UnknownTamperedOrExpiredTokenIsInvalid`, `TestClaimToken_CancelRefusesSensitiveButNotReportingOrResult` | `TestCapabilityClaimLivenessProperty` uses real awarded bearers, C2 authentication/authorization and SQLite snapshots. It generates tampering, cancellation and lease boundaries. `TestAuthenticationSnapshotRechecksAncestryAndMembership` retains credential expiry and class checks. |
| `TestClaimToken_FinishOrLossEndsItOnTheNextRequest`, `TestClaimToken_ALaterGenerationFencesTheEarlierToken`, `TestRunnerClaimToken_BindsTheClaimAndDiesWithItsGeneration` | The same property generates holder and generation replacement, expiry, revocation and run deletion. `TestCapabilityResultCannotChangeAfterCompletionOrLossProperty` preserves terminal rows and event counts after refused writes. |
| `TestClaimToken_ResultCommitsOnceAndReplaysOnlyTheSameResult`, `TestClaimToken_ReplayAnswersOnlyTheCommittedDigest`, `TestClaimToken_EndedClaimWithNoResultRefusesEveryResult` | `TestCapabilityResultCannotChangeAfterCompletionOrLossProperty` commits and replays actual node transitions, then generates changed reports and verifies no node or event changes. Unstarted release refuses an uncommitted result. `TestTransitionApplicationRetryKeepsClaimAndOrdinal` retains revoked-actor replay refusal. Exact HTTP result provenance is a separate open integration. |
| `TestClaimToken_TriggerlessRunRefusesSensitiveOnceItsRunEnds`, `TestClaimRunCancelled_AMissingRunReadsAsCancelled` | Current intake creates the owner unit with the run. The liveness property deletes the bound run and verifies that no sensitive, reporting or result access survives. Triggerless raw execution is removed. |
| `TestRunnerClaimToken_OnlyARunnersBoundClaimIsDelegated` | The raw launcher/runner discriminator retires. Current units and bootstrap exchange are covered by `TestExchangeCapabilityCommitsOnceAndPreservesTheAward`, `TestRenewUnitRequiresBootstrapExchange` and the named job authority property. |

`TestBootstrapStartupUsesDurableCapabilityAuthority` joins the actual startup
helper, store issuance, readiness read, authenticated operator inventory and
revocation through the shared router. It uses no server socket.

The dead `ReportAttempt` and `EnqueueChildRun` writers have no remaining callers.
Their replacement requirements remain in the current transition, result-record,
recovery and child-intake suites. `TestChildIntakePersistsLineageAndInvocationReplay`
and `TestChildIntakeCannotEscapeItsLiveParentProperty` retain child identity,
exact replay and current parent authority. No native
acceptance cell is newly claimed by this cut.

## W1 credential reader and here issuer retirement

| Retired evidence | Current evidence |
| --- | --- |
| `TestCreateAndLookupToken`, the four `TestLookupToken_*` cases and collided-prefix lookup | `TestAuthenticationDigestSelectsOnlyLiveAuthorityProperty` generates changed presentations and digests, verifies the exact issued authority, preserves stored expiry and refuses revoked and expired credentials. `TestAuthenticationSnapshotRechecksAncestryAndMembership` retains ancestry, membership, class and audience refusal. Current capability prefixes are unique. |
| `TestListTokens_FiltersKindAndRevoked`, `TestAmbiguousPrefixRefusesLookupAndRevoke` | `TestCapabilityInventoryFiltersAndRefusals`, `TestCapabilityInventoryOwnershipProperty`, `TestCapabilityInventoryDeploymentIsolation` and capability rotation properties cover the current inventory and committing mutations. Legacy ambiguity recovery retires with the token-table reader. The legacy mint uniqueness test remains for retained fixture writers. |
| `TestRunnerTokenExpiryFollowsItsLastUse`, `TestTokenUseIsWrittenAtMostHourly` | Their token-table sliding and throttled-write mechanism retires. Current authentication checks persisted absolute expiry without changing it. The digest property covers that invariant. Runner poll activity now has its own store-clock write. |
| `TestCreateHereTriggerWithRun_RefusesAClaimAfterTheWindow` | `TestClaimUnitHereWindowAndReclaim`, `TestAdmissionHereAwardRetainsRequestingAuthority` and `TestAuthenticationSnapshotHereClaimHistorySurvivesRequeue` cover the live here award, first acquisition and reclaim. |
| The four `TestClaimedRunFor*` functions | `TestSecretReadClaimScopePrecedenceProperty`, `TestSecretReadAuthorityAndSelectionProperty` and `TestSecretReadCommitsBoundReleaseAndNeverDetachesAnImplicitRun` preserve team, run, pipeline, current holder and declared-secret selection through the current committing reader. Triggerless raw execution is removed. |
| `TestAgentClaimsUseTheStoreDialect` | The unused raw agent-claim report is removed. `TestRegisteredAgentsUseLiveCapabilityIdentityAndSavedLabels` retains current registration identity. The complete runner inventory and active-job projection remain open integration work. |

`TestClaimPlacementOnlyLiveEligibleRunnersCanHoldWorkProperty` covers configured
and declared preferences through named and queued claims. It generates labels
and checks stale, revoked, expired, foreign, repository-excluded, grantless,
under-capacity and hard-selector-mismatched runners, the exact hold deadline and
granted-name preferences against forged labels. Held polls preserve the entire
node and award count. `TestEngineClaimHTTPLocalFirstUsesPersistedPoll` carries
server configuration through the real HTTP handler and store with only enrollment
and software-policy fakes. No native cell is newly claimed.
The retired node queue and named trigger claims use the current
`TestClaimUnitNamedAndQueuedUseCapacityAndIdentity`,
`TestClaimUnitRejectsForeignAndMissingRequirements`,
`TestClaimUnitRepositoryEnrollmentAndMeteringDoNotGrantPlacement`,
`TestClaimUnitGrantedProfileKeys`, `TestClaimUnitHereWindowAndReclaim`,
`TestClaimUnitMismatchPreservesQueueAndDoesNotHideEligibleWork`,
`TestClaimQueueMismatchPreservesPositionProperty`,
`TestClaimPlacementOnlyLiveEligibleRunnersCanHoldWorkProperty`,
`TestCapabilityClaimLivenessProperty` and `TestReaperFenceProperty`.
The final-schema `TestMeteredUnit*` proofs retain atomic reservation, concurrent
spend, immutable awards, checkpoint, unstarted refund, retry and reaper settlement
coverage. Legacy tests that called the deleted APIs are retired with them.
Their token-table, mutable credit-window and old claim-column assertions are not
current acceptance evidence. Card cap/fraud/warm-pool regressions from those
fixtures still need final-schema ports by the accounting owner. Native PostgreSQL
concurrent-claim evidence remains open.

`TestRunnerMeteringChangesOnlyFutureAwards` uses real provisioning, claim,
release, readiness and billing persistence. Origin verification and C2's billing
calculator are fakes. The generated console-authority property refuses expired,
revoked, delegated, forged and delisted authority without changing the root.
`TestRunnerMeteringHTTPCommitsThroughCurrentConsoleSession` uses the production
router, authentication, CSRF verification and store writer. Only provisioning
origin verification is faked. `TestRegisteredAgentsHonorRotationGraceProperty`
covers randomized deadlines and exact expiry. These are package tests. They add
no native acceptance claim.

## W1 execution-attempt and matcher retirement

The raw execution writers and their local/trigger helpers have no production
callers. Their tests no longer exercise the final schema. These replacements
use real claims, transitions and persisted billing:

| Retired tests | Current evidence |
| --- | --- |
| Local execution host/outcome, retry sequencing, replay and unnamed executor tests | `TestExecutionAttemptsStayInsideTheNodesTeamProperty`, `TestAttemptExecutorComesFromClaimAncestryProperty`, and current attempt-transition properties retain tenant isolation, stored attribution, ordinal and replay checks. |
| Dispatched execution start and first-renewal billing, early finish and reserved grace fixtures | `TestMeteredUnitNodeLifecycleUsesImmutableAward`, `TestMeteredUnitCloudRenewalStartsComputeWithoutAnAttempt`, `TestMeteredUnitRenewalCheckpointsAfterComputeStarts`, `TestMeteredUnitApplicationRetryKeepsOneReservation` and `TestMeteredUnitSettlementFailureRollsBackResult` cover current reservations, starts, retries and rollback. Spend exhaustion and grace for all unit kinds remain open. |
| `TestTriggerNamesGitHubRepoAcceptsEverySpellingOfTheRepository` | The name-based matcher is removed. `TestStoreActionsEnrollment`, `TestStoreQueueOrder`, `TestQueuedTriggerRepositoryScopeProperty` and current push-scope properties retain canonical repository and push authority. |
| Old attempt-history lineage fixture | `TestExecutionAttemptsStayInsideTheNodesTeamProperty` uses real node transitions and generated hostile tenant/run selectors against final `node_attempts`. |

`TestPlacementMeteringUsesEnrolledOriginProperty` covers all combinations of
machine/service origin, root metering, direct/delegated claimant, named/queued
claim and funded/unfunded payer. `TestMeteredRootPolicyCannotRewriteAwardProperty`
now passes for Cloud awards from unmetered roots, including settlement replay.

Provider-payment fixtures no longer replace the receipt and correlation schema.
The existing provider payer, mode, replay, rollback and conservation properties
exercise the installed final tables. `TestTeamPurgeIsolationProperty` also keeps
unbound warnings and other teams' financial rows across repeated purges.


## W1 metadata and approval writer ports

`summary_test.go` moves to `metadata_ports_test.go` through the real sealed
intake, claim and start fixture. The ports cover job/step overwrite, exact
repeat, reopen persistence, absent targets and closing unfinished steps for
success, failure and cancellation. Retry settlement checks both attempt rows.
The old placeholder behavior is replaced by explicit missing-step refusal.

`annotation_concurrent_test.go` moves to the current metadata ports. Concurrent
job and step writers retain every distinct entry and the run counter. SQLite
runs here. The matching PostgreSQL test uses the same native fixture and requires
a configured database. The old uncommitted-placeholder interleaving has no
remaining writer. Metadata requires a committed step, and all writes hold the
run lock. Byte/count limits and refusal rollback are exercised through the
current API. The generated fence property refuses foreign holder, authority,
job/run selectors and generations without changing either target.

`step_status_test.go` moves to terminal-step, skipped-step and immutable-replay
checks through `RecordStep`. `approvals_test.go` moves to
`TestApprovalTransitionsPersistDecisionsAndRefuseOverwrite` and
`TestApprovalTransitionsKeepPendingRequestOrder`, alongside the existing
approval reaper/race/rollback cases. Gate creation, decision attribution,
comments, lists, tenant isolation, missing targets, exact replay and refusal
to reopen a resolved gate use the current transitions.

`generated_node_test.go` loses the removed input-resolver fixture. Its sealed
spec/reference assertions move to
`TestGeneratedSpecReferencesAreSealedWithTheAcceptedDeclarationProperty`.
Generated specs retain their canonical hash and declared references, and a
changed expansion replay cannot replace either. The native controller
pipeline-latest reference lookup remains open with W2. This property does not
claim that integration cell.

The orphaned `internal/acceptance/store_recovery.go` is deleted. Its checks
already run in `acceptance_recovery_*_test.go` beside the store, and W3 has
migrated its final host consumer. The dead settlement export fixture is removed
with the unused legacy settlement engine. No native placement cell is enabled.

## Debug-pause resume port

| Legacy test | Final coverage |
|---|---|
| `TestReleaseDebugPause_RecordsAuthPrincipal` | `TestResumeDebugPauseCommitsPrincipalAndReplays` exercises the final route through the concrete tenant transaction. It checks subject attribution, store time, unchanged job lifecycle and one event across repeat delivery. |
| `TestReleaseDebugPause_AuthOverridesBody` | `TestResumeDebugPauseRejectsForgedActorsAndDeadCredentials` refuses body actor fields because only current credential authority may select the releasing subject. |
| `TestReleaseDebugPause_Unauthenticated` | The same HTTP port verifies revoked credentials. `TestResumeDebugPauseCurrentAuthorityProperty` generates membership loss, role changes and foreign selectors before commit. |
| `TestReleaseDebugPause_AuthDisabledFallback` | Deleted because the final credential policy has no anonymous release fallback. The retired release path returns 404. |


### W2 native origin and launcher continuation

The legacy TestLaunchBilling_ReleaseRefundsAndTheFirstBeatBills and
SyncLaunchJobs are deleted. Native release coverage remains in
TestMeteredUnitUnstartedCloudReleaseRefundsReservation,
TestReleaseUnitReturnsUnstartedWorkWithoutAttempt and
TestReleaseUnitRefusesForgedStartedAndCancelledClaims. Launcher sync now has
TestEngineLauncherSyncNativeFenceProperty for live, replacement and exchanged
awards without bearer delivery.

TestCommittingAccountOriginProperty covers both store entrypoints formerly
reachable by capability-derived sessions. TestBootstrapSeedStoreOnceProperty
uses the real seeded issuing transaction. Native peer transport runs through
the default SQLite authentication snapshot in TestLocalControllerHTTPUsesSharedPolicy.
TestAuthenticationCancellationIntentPrecedenceProperty covers separate job/run
intent. TestControllerClaimProjectsOnlyItsCurrentAttemptProperty keeps controller
holder generations and process attempt fences distinct. No native topology
matrix cell is inferred from these component and in-process HTTP checks.


### W2 native log egress continuation

The eight tests in pkg/controller/egress_test.go replace their token-table and
retired node-route fixtures with native awards, account sessions and committed
log segments. They retain separate readers, authentication before byte delivery,
bodyless refusal accounting, download/stream slots, daily alarm privacy, and
caller-header isolation. HEAD is refused by the final router. The old live and
dashboard stream aliases stay removed and share one final job-log stream route.
Canonical rate_limited envelopes replace the retired flat egress error shape.

TestNativeLogReadAuthorityProperty generates foreign paths, stale holders and
generations, unknown presentations and membership loss. TestNativeLogReadEndsWithItsRoot
uses the store's real revocation transaction. Both refuse before physical I/O or
egress charging. Physical object bytes are fake. No native topology matrix cell
is claimed by these HTTP components. The merged legacy launch-release test stays
deleted, with the native release coverage listed above.


### W2 native console settings and runner error ports

`TestCreditSettingsHTTPPolicyWritesPersist` and
`TestCreditSettingsHTTPInvalidBodiesLeavePolicyUnchanged` now create a real
account session with a sealed CSRF key. Both reach the default console's SQLite
commit gate. The fake session and CSRF bypass are removed from their fixture.

`TestAgentRoutesMaskStoreFailures` is replaced by
`TestRunnerRoutesMaskDependencyFailuresProperty`. Its authenticated requests use
`GET /api/v1/runners` and `POST /api/v1/runners/claim`. Generated private dependency
errors produce a canonical internal error and no private message. The retired
agent and node-claim registrations stay absent. Inventory and runner policy are
fake dependencies. These ports add no native acceptance matrix cell.


## W1 ordered placement and unused reader retirement

`TestOrderedPlacementPreferencesHoldOnlyForLiveEarlierRunnersProperty` exercises
real named and queued claims with generated preference orders, holds, exact
boundaries, runner liveness and forged identity labels. The existing placement
property also covers stale polls followed by unrelated credential activity.
Both test that waiting leaves the node and award count unchanged. The HTTP
local-first test retains the existing controller flags through the real store.

The unused raw debug-pause create/release APIs and their legacy tenant fixture
are deleted. `TestResumeDebugPauseCurrentAuthorityProperty` and the native
resume HTTP tests retain current membership, tenant, target, actor, replay and
execution-fence coverage. The obsolete ancestor-query tests leave with the
unused reader. Sealed child intake and its generated live-parent property retain
lineage and parent-fence coverage. The unused child-trigger lookup and legacy
mutable-token `NodeSettlement` have no callers or retained tests.

No native acceptance matrix cell is enabled by these package properties.

## Raw node-start retirement

`Store.StartNode` and `Tenant.StartNode` are removed.

| Port | Current evidence |
| --- | --- |
| `TestListNodesKeepsInsertionOrderAcrossUpdates` | `TestRunSnapshotKeepsDeclarationOrderAcrossStarts` admits and accepts five jobs, reconciles and starts them in reverse order, then reads their original declaration order through the typed snapshot. |
| `TestPostgresListNodesKeepsInsertionOrderAcrossUpdates` | `TestPostgresRunSnapshotKeepsDeclarationOrderAcrossStarts` uses the same body against the configured PostgreSQL store. Native execution remains pending when no server is configured. |
| `TestTenantNodeWritesStayInTheRunsTeam` | Uses real admission, acceptance and claims. The foreign tenant cannot start the job or write steps. The actual holder starts and records two steps. |
| `TestTenantMetricsStayInTheRunsTeam` | Uses a real claimed and started job. Foreign reads and writes stay empty, and identical samples replay once. |
| Metric identity and replay | `TestMetricIdentityPreservesTenantAndConflictingRetriesProperty` generates 64 readings with foreign tenants, exact retries and changed CPU, memory or process-time readings. Refused writes preserve the recorded sample. |

The dashboard log-reader fixture no longer starts a node before seeding its
terminal result. Four default-team Store write wrappers are removed. Existing
callers select explicit tenant handles. Remaining raw run fixtures still need
migration from the retired run schema to sealed intake.

`Store.SetNodeArtifactManifest` and its tenant twin are removed.
`TestSetNodeArtifactManifest_RoundTrips` and
`TestSetNodeArtifactManifest_SurvivesFinishNode` retire with the raw digest
column. `TestBlobResultRecordsCommitAndReplayWithTerminalState` verifies
committed manifest selectors with terminal state and replay.
`TestBlobResultRecordsRejectForgedResultKeys` generates tenant, run, attempt,
generation and record-key forgeries. `TestNode_DefaultsToEmptyManifest` retires
with that column. `TestResultRecordsCommitWithTerminalState` verifies absent
selectors after refused attachment. Those current tests are unchanged.


### W1 reporting transactions and cron lease continuation

`TestMetrics_RoundTrip`, `TestMetricTimestampConflicts`,
`TestMetricTimestampConcurrentWriters`, `TestMetricKindsRoundTrip`,
`TestMetricKindsRejectInvalidCombinations`, `TestAddNodeMetricSample_Validation`,
`TestNodeMetricsCapAndPages`, `TestMetricIdentityPreservesTenantAndConflictingRetriesProperty`,
`TestTenantMetricsStayInTheRunsTeam` and all four `TestAddNodeUsage*` checks now
use real admission, plan acceptance and claims with `WriteMetric`/`WriteUsage`.
They retain timestamp limits, timezone-equivalent replay, conflicts, concurrent
writers, resource kinds, pagination, sample and marker limits, integer boundaries,
overflow rollback and foreign-tenant refusal. Identical retries at the cap now
pass. The former raw writers are removed.

`TestMetricTransactionsRecheckReportingAuthorityProperty` generates 96 forged
team/run/job/fence/authority/rule combinations and verifies refusal without
writes. `TestMetricReportsFollowAttemptAndCancellation` covers retries,
cancellation reporting and terminal refusal. `TestMetricRunHostReportsFinishedJobsUnderItsOwnFence`
retains post-process accounting under a live run host.

The `TestRunCronTickLeased_*` checks now use Operator and the final lease table.
`TestSweepClaimGenerationSQLDialects` retains repeated-failure generation checks.
`TestCronTickUsesStoreClockAndFencesStaleCompletionProperty` generates 32 takeover
and tick-window cases. `TestSingletonReleaseCannotClearAnotherFenceProperty`
generates 64 invalid releases and checks stale takeover refusal.

These checks do not promote native PostgreSQL or end-to-end HTTP acceptance.


## W1 sealed reporting and terminal metadata ports

`TestFinishNodeWithReason_PersistsStructuredMetadata`,
`TestFinishNodeWithReason_DoesNotOverwriteTerminalNode` and
`TestFinishNode_LeavesReasonEmpty` are replaced by
`TestJobFinishPersistsMetadataAndRefusesOverwrite`. It uses real intake,
acceptance, claims and starts. Success and failure preserve reason, masked
error, exit code and finish time. Exact replay succeeds and changed terminal
metadata fails without changing the reported job or attempt history.
`TestFinishNodeWithReason_FinalizesDoneNodeWithEmptyOutcome` retires because
the final schema refuses that state. Existing schema shape tests retain that
constraint.

`TestStoreFinishRefusesAnotherTeamsNode` is replaced by
`TestTenantFinishRejectsForeignAndMissingJobs`. Both refusals retain the
holder, generation, attempt, state sequence and digest.

`TestReportingReadsSealedFactsWithoutSecretValuesProperty` generates private
and public arguments, reads before/after acceptance, compares run and trigger
lists to individual reads, and refuses foreign tenant reads.
`TestReportingReadsCurrentJobLifecycle` checks the final reporting projection
against typed job state and attempt history. Pending paging, queue-status and
literal repository filters are covered by
`TestReportingPagesUnstartedRunsAndFiltersQueueState`.
`TestReportingRefusesMalformedSeals` requires errors for corrupt requests.
The unused dispatch-route fixture had no tests or consumers and is removed.
No native topology matrix cell is claimed by these component tests.

## W1 job insertion, ready replay and reporting ports

- `TestNodesPerRunGuardRefusesAMeteredPrincipalPastTheCap`, the node half of
  `TestPerPrincipalGuardsLeaveLocalWorkAlone`, and
  `TestPerPrincipalGuardsUseTheRunsTeamForMetering` move to
  `TestPlanInsertionLimitsAreAtomicProperty`. Metered scope comes from committed
  awards. Unmetered and unrelated-run awards do not spend the per-run guard.
- `TestGlobalNodesPerRunGuardCountsEveryRun`,
  `TestConcurrentNodeCreationNeverPassesThePerRunGuard`, and the raw job-recreate
  assertion move to `TestPlanInsertionGlobalLimitAndReplay`,
  `TestConcurrentPlanAcceptanceCannotExceedInsertionLimit` and
  `TestExpansionInsertionLimitRollsBackBatch`. Initial and generated batches
  respect the configured cap, roll back together, and replay after a cap falls.
  `TestStorePlanInsertionLimitUsesCanonicalRefusal` drives real shared validation
  and seals, checks `plan_invalid` plus `ErrComputeLimit`, and retries/replays
  through configured cap changes.
  The run-rate assertions remain in `TestGuardsLetAnIdempotentRunRecreateThrough`
  and `TestRunRateGuardsUseTheRunsTeamForMetering` until raw run intake retires.
- `TestNodeClaim_MarkReadyIsIdempotent` moves to
  `TestOwnerReadyReplayPreservesQueueAge`. The real owner transition preserves
  the original ready timestamp and state sequence on replay.
- `TestGitHubCheckRunIsRecordedOnce` moves to
  `TestGitHubCommitReportingUsesSealedIdentityProperty`. Sealed intake supplies
  runs. The generated probe retains first-check replay, foreign-team refusal,
  exact SHA and numeric repository isolation, ordering and rename behavior.
- Legacy `RedactedForDisplay`, `RedactedRuns` and receipt raw-invocation tests
  move to `TestReportingReadsSealedFactsWithoutSecretValuesProperty` and
  `TestReportingDistinguishesUnknownAndEmptyClassification`. These exercise
  real intake and accepted seals, then report and receipt output. Unknown,
  nil, malformed and explicit empty classification stay distinct. Receipt
  component tests retain cost, topology, hash stability and optional-secret
  behavior. Raw invocation rewriters and their historical compatibility tests
  are removed with the old API.

Reporting and legacy claim fixtures use explicit `teststore.SeedNode` and
`SeedNodeReady`. They grant no production authority and run no admission logic.
`TestJobReportingSeedUsesCurrentSchemaAndRejectsMissingRun` checks their schema,
nanosecond timestamps and failure when no target exists. New lifecycle and
acceptance probes use `AdmitRun`, `AcceptPlan` and `TransitionNode` instead.

## W1 authorized team mutation ports

The identity invitation limits, email normalization, expiry, withdrawal and role
checks now invoke `CreateAuthorizedInvitation` and `DeleteAuthorizedInvitation`
through fresh account-session fixtures. Native generated authorization checks
cover membership loss, role loss, revoked/missing session ancestry, delegated
credentials and foreign tenants. Concurrent replay writes one invitation. A
failed insertion rolls back the row and returns no result.

`TestTeamWritesUseNativeTenantTransactions` exercises all six team mutation
routes through the concrete store adapter. CSRF transport is a fixture, while
credential snapshots, authorization and committing writes use SQLite.
`TestTeamDeletionRetainsCheckoutAndHoldGuardsOnStoreClock` ports the deletion
admission guards to the final checkout and hold contracts.
`TestAuthorizedRunDeletionPreservesBillingAndAllowsCleanupReplay` completes a
sealed run before removing it and verifies current authority, ownership,
financial retention and direct-store replay. It replaces the raw run-control
fixture. The obsolete `TestDeleteRun_DropsMemoEntriesFromDeletedRun` is removed
because `concurrency_cache` and its writers were already deleted. Blob memo
selection and retention remain covered by W1b's current record tests.

The broader legacy deletion fixtures still need final run/trigger seeding and
checkout amounts. Their pre-mutation failures are recorded in W1's session 57
report. No native placement acceptance cell is enabled by these ports.


## W1 GitHub App administration and reservation continuation

`TestGitHubAppMutationsRequireCurrentAdministrationProperty` covers all six
committing mutations over direct/delegated authority, foreign tenants, lost
membership, downgrade, revoked ancestry, wrong grants and replay modes.
`TestGitHubAppMutationsCommitActorTimeAndConsentGeneration` ports the old
`TestGitHubAppAutomationAdmissionChecksConsentGeneration` fence through the
current consent transaction. It also checks store time and derived actors.
`TestGitHubAppMutationRefusalsRollBack` covers incomplete identities, foreign
owners/installations, suspension, actor forgery, late authority loss and insert
failure. The extra-repository tests now use one names-and-IDs write. The
name-only intermediate state is retired.

`TestLauncherReservationOwnershipProperty` extends snapshot fencing to original
bootstrap evidence across expiry, exchange, replacement and delegated launcher
presentations. `TestStoreLauncherReservationW8` and
`FuzzStoreLauncherReservationFenceW8` run the actual store/controller/launcher
path with a fake Kubernetes boundary. They add no native Kubernetes claim.

## W1 store fixture and measurement continuation

Reporting fixtures now seed sealed request columns and current owner projections.
They do not grant execution authority. Bootstrap exchange uses ClaimUnit instead
of awarding onto an already-running row. Paid funding fixtures settle a committed
provider checkout. The shared storetest opener configures its session cipher and
test payment mode. Cloud lifecycle fixtures carry funds, and delegated fixtures
retain repository scope.

Removed tests target deleted invocation columns, token-table minting, independent
run/trigger IDs, raw run upserts, the separate sweep claim, the trigger-filter scan
horizon, concurrency_cache and on_limit=coalesce. Current record/materialization
and fenced capacity tests retain those execution invariants. Retained reporting,
queue, capacity cost and cancellation tests continue against current columns.

The new generated properties check bucket-measurement successor fencing and
execution-activity tenant isolation against the store clock. Existing random
capacity tests now generate retained policies and seed live owner/job rows.


## Sealed intake rate and deletion ports

The eleven legacy run-rate/raw-attribution/oldest-ready tests in
compute_limits_test.go are replaced by TestSealedIntakeChecksRunRateAndReplaysWithoutSpendingAgain,
TestSealedIntakeRateCountsOnlyTheAuthenticatedTeam and
TestConcurrentSealedIntakeNeverPassesTheHourlyGuard. They exercise AdmitRun,
check every concurrent error, count both run and owner rows, reject a spent
principal and permit replay without charging another slot. TestSealedIntakeRateKeepsPrincipalKindsSeparateProperty generates account/machine
ID collisions against the committed actor kind. Token-metered-only intake, pending-row re-attribution and the uncalled oldest-ready diagnostic are
retired. Matcher selection and claim eligibility retain their separate tests.

Deletion fixtures provision real member roots through current account sessions.
They verify capability revocation, cooperative run cancellation, foreign-tenant
preservation, account-subject relabeling and whole-cent checkout guards. The
removed storage-watermark test now checks both replica storage-usage rows and
preserves another tenant's rows. The recheck test first proves the foreign key
refuses a post-purge tenant write. Business-event expectations use the verified
payment-provider actor and nullable deployment team.

Run-control property tests cover live membership, exact tenant/run/job selectors,
encoded secret masking, changed-body replay conflicts and rollback of state,
events and receipts. TestRunControlHTTPUsesStoreTransactions drives the canonical
controller routes through the concrete Tenant adapter with real member issuance.
No acceptance registry or native-platform cell is added.


## Committed runner inventory

`Tenant.ReadRunners` now supplies C2's production `RunnerInventoryReader`.
`TestRunnerInventoryWithSealedAdmissionAndEnrollment` uses storetest's isolated
store through the real admission, plan, enrollment, claim and renewal paths.
`TestRunnerInventoryHTTPUsesStoreSnapshot` drives the real default HTTP adapter.
The fake-reader HTTP checks stay because they cover malformed foreign results
and missing dependency refusals. The missing-reader case now explicitly injects
an absent reader instead of assuming Tenant cannot implement the contract.

`TestRunnerInventoryAttributesOnlyCurrentSameTenantAuthorityProperty` generates
same-name roots, foreign tenants, revoked ancestry and leaf authority, expired
leases, superseded generations, holder changes and cancellation. Only exact
current same-team authority contributes activity. The liveness/headroom test
checks that accepted renewal changes only last-seen. Cloud, local and here tests
check immutable placement identity and separation from machine registrations.
Observation-write failures prove award and renewal rollback. Corrupt observations
return no partial snapshot. These are component and SQLite proofs. They do not
claim a native placement cell or missing concurrency/queue-depth ingestion.

## Explicit tenant reporting reader cut

`TestStoreBackendTenantReadsProperty` generates 24 tenants and checks that the
same backend returns only their run, jobs and events. It also refuses rows from
the default team and checks the reverse direction. The original backend fails
on the first nondefault job listing. The retained backend round-trip fixture now
uses the current schema. Existing reader callers use Tenant explicitly, while
cross-team query tests select Operator. Billing test files receive only reader
caller substitutions.

The two source-scanning default-team twin guards are deleted because the
production twins and their backend fallback are removed. The compiler now
rejects those calls. `TestSecrets_PipelineScopeResolution` retires with the raw
fallback reader. `TestSecretReadClaimScopePrecedenceProperty`,
`TestSecretReadAuthorityAndSelectionProperty` and
`TestSecretReadMemberScopesAndMaskedRefusal` retain authorized scope coverage.
The encryption and tenant-key tests inspect exact rows with `GetSecretRow`.

## W1 account transactions and raw team-switch ports

The four raw session-switch fixture calls now use `SelectAccountTeam`.
`TestIdentitySessionSwitchNamesTheTeamItLeaves`,
`TestRolelessSessionsHaveNoTeamAuthorityProperty` and `TestStoreSessions`
retain stale-team and membership refusal and roleless selection coverage.
`TestAccountMutationsRequireTheSameLiveDirectSessionProperty` generates changed
identity, team, route rule, revoked/expired sessions, lost membership and
capability ancestry against all four final account writers. The other account
transaction tests cover atomic session selection, rollback, recent-sign-in
boundaries, last-method preservation, other-session revocation and one invitation
winner across two sessions. `TestAccountMutationsHTTPUseCommittedStoreAdapter`
checks all four concrete defaults through socket-free HTTP and asserts C2
interface conformance. `TestAccountMutationsKeepNanosecondTimestamps` checks
public account/identity times, raw membership/update/unlink facts and signup
window cutoffs without losing subsecond precision.

`TestCacheExcludedCounts_CountsCacheDominantRunsPerPipeline` retires with its
unused API and SQL. The profile-folding suites retain memoized-job exclusion.

## Raw account writer retirement

The remaining creation, invitation and unlink fixtures now call
`CreateAccountTeam`, `AcceptAccountInvitation` and `UnlinkAccountIdentity`
through fresh authenticated direct sessions. `storetest.AccountAuthority`
supports member and roleless accounts. `SessionAuthority` preserves an existing
session when a test checks unlink revocation.

`TestIdentityInvitationLifetime` retains the seven-day lifetime assertion.
`TestAccountInvitationExpiresOnStoreClock` checks refusal and unchanged rows
at the exact expiry and one second later while the accepting session remains
live. The PostgreSQL unlink contention fixtures create their authority before
acquiring database gates. Concurrent unlink requests share the session they
retain. Those native tests still require PostgreSQL execution.

`TestLogTreeTraversesEveryTenantDescendantProperty` exercises the retained
tenant run listing through the real HTTP log reader. Generated trees cross the
100-row internal page, contain a nested descendant and a cycle, and include a
foreign tenant's child naming the same parent. Every selected descendant appears
once and foreign events stay absent.

## Store billing review ports

`billing_price_runtime_test.go` replaces the legacy token/charge-window pricing
fixtures. Real ClaimUnit, RenewUnit and ReleaseUnit transactions cover CPU and
memory pins, reservation minimums, immutable award prices, unpriced refusal,
refund attribution after card replacement, and ledger survival after run deletion.
The existing unit-billing tests retain unfunded admission, last-credit races and
unstarted refunds. The obsolete token lookup and separate FinalizeNodeCredits
writer are removed with their fixtures. No archived acceptance cell is newly
claimed by this cut.

`credit_exhaustion_runtime_test.go` covers the grace boundary and restoration on
job, plan, eval and host renewals. A grant cannot restart grace while a spend cap
still binds. `upload_concurrency_dialect_test.go` runs commit/abort contention
through independent SQLite clients and through storetest.NewPostgres when a
server URL is configured. Upload GC properties cover pending tickets, abort,
expiry, prior quota release, run deletion and cross-team manifest graphs.

## W9 public retry history port

`TestRetry_ListAttempts` is replaced by
`TestRetryHistoryUsesPublicProjectionForEverySelector`. It reads the real
controller and SQLite through stable IDs and numeric selectors for the root,
siblings and descendants. `TestRetryHistoryRechecksEachRunBinding` and
`FuzzRetryHistoryKeepsPrivateInputsAndTeams` cover run-scoped owner refusals,
revocation, lost membership, hostile selectors, cross-team lineage pointers and
private input projection. No native placement cell is newly claimed.
`TestRetryHistoryTraversesLongChainsAndCycles` covers a 274-run tree through root,
leaf and numeric selectors, including a cycle back to the leaf.
`TestRetryHistoryOrdersEqualTimestampsByRunNumber` pins deterministic order.

## W9 sealed retry action ports

`pkg/controller/retry_test.go` is deleted. Its raw trigger response, `full`
query flag and environment provenance transport are removed contracts.

| Archived test | Replacement and status |
| --- | --- |
| `TestRetry_CreatesNewTriggerWithSameInputs` | `TestRetryActionPreservesSealsAndCreatesPendingExecution` checks original and accepted seals, SHA, classified arguments and pending owner creation through the shared HTTP client and SQLite. |
| `TestRetry_WorkingTreeRunRetainsDesktopClaimSource` | The same test retains sealed local source and origin instead of the removed trigger-source selector. |
| `TestRetry_WorkingTreeRunIgnoresAnotherTeamsTriggerWithSameID` | `FuzzRetryActionCannotCrossAuthorityOrChangeSource` refuses foreign stable IDs and numeric selectors and verifies both teams' row counts. The final schema makes run IDs globally unique, so the old duplicate-ID fixture cannot be inserted. |
| `TestRetry_OfRetryInheritsOriginalCheckoutInsteadOfEphemeralSnapshot` | The sealed-input test checks three retry generations against the original checkout, plan and source identity. |
| `TestRetry_PreAllocatesPendingRunRow` | The sealed-input test reads the new pending run and executable owner before execution and checks exact replay. |
| `TestRetry_UnknownRunReturns404` | `TestRetryActionUnknownRunAndRemovedRequestShapes` checks canonical not_found and malformed retry bodies. |
| `TestRetry_FullQueryParam` | **Open:** `TestReviewRetryModeIsPartOfCommittedReplay` behind rewrite_integration is red. The mode is validated at the controller but omitted from the committing intake request. C1/C2 must preserve it independently of the original execution seals and include it in replay identity. No mode-selection behavior is claimed complete. |
| `TestTrigger_DropsEnvKeysOutsideTheAllowList` | `FuzzRunIntakeRejectsLegacyEnvironmentProvenance` refuses the removed trigger/env document and unknown raw environment fields before intake without echoing their values. Captured environment snapshots use their existing typed contract. |

`FuzzRetryActionValidatesBeforeAdmission` checks generated modes and keys against
the shared validator through the real HTTP router. Refused requests create no
run. `TestRetryActionRechecksAuthorityBeforeReplay` covers revocation and
membership loss after a committed retry. No native placement cell is newly
claimed by these ports.

## Decision 112 billing policy

The dollar-ceiling, daily-cap, purchase-window, shared-card bucket and operator
cap-override tests retire with those APIs. `billing_model_test.go` and
`billing_signup_test.go` cover the replacement through claims, renewal, real
settlement, collection, sign-in and explicit operator admission. Generated
admissions and independent SQLite clients pin the standing capacity invariant.
`billing_model_internal_test.go` carries the controller claim, signup, status and
card-prepay read paths through the real store. It also drives signed checkout
and lost-dispute webhooks through verification and settlement to Cloud
admission, renewal and completion. Queue proofs cover full teams and oversized
jobs ahead of eligible work. The identity provider and runner profile are
external contract fakes. Generated dispute delivery orders preserve Held after
a chargeback until an operator releases its hold. `billing_docs_test.go` checks
the policy table against the source documentation. Existing immutable-price, refund,
retention and payment replay proofs remain. No native Stripe or PostgreSQL cell
is promoted by these SQLite and controller-handler proofs.
