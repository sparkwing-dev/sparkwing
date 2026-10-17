# Test reconciliation

W1b integration scope. Categories: A deletes a retired surface, B fixes behavior still owed, C updates an expectation to the frozen contract. Socket failures remain failures requiring lead verification. No socket-dependent test is skipped or deleted to pass this sandbox.

- B internal/apiroutes: TestParseEngineEndpointsUsesActualRegistrations and TestParseSourceEndpointsUsesActualRegistrations expose repeated grants for distinct credential alternatives. Deduplicate displayed alternatives without changing route authorization.
- C internal/fssecure: TestConfigDirectoryWritersDoNotMkdirGroupReadable removes inventory entries for deleted configure_init.go and legacy_migrate.go. Scan all internal/userconfig writers, preserving the permission assertion.
- C internal/profile: TestLoadSaveRoundTrip now round-trips a controller connection, without the retired SQLite state backend. TestDefaultPath_XDG unsets the override because explicitly empty SPARKWING_CONFIG is invalid. TestSaveRewritesOnlyTheProfilesSection starts from a valid old controller connection. TestLoad_RefusesRemovedKeys checks located field paths rather than the retired YAML decoder text.
- B internal/userconfig: TestLoad_RefusesARemovedStateProfile restores the profile name, removal message and migration-guide link when strict schema validation rejects retired profile fields. Unknown fields remain rejected and values stay out of diagnostics.
- C internal/crons: schedule fixture git repositories disable inherited commit signing locally, so tests exercise schedule behavior without the user's GPG agent.
- C internal/pipelineentry and internal/repos: credential-filter/masking assertions retain all secret-absence checks, add the proxy username, and expect the public URL host to remain visible under C4's userinfo-only proxy masking rule.
- C internal/repos: TestResolverDescribesOverTheProtocol fake SDK describes each pipeline with the required args array. Sandbox path tests unset SPARKWING_CONFIG instead of treating an explicitly empty override as absent.

- C internal/crons: single-object schedule fixtures use the frozen list form. Fake launchers persist a reporting run before fire history links it. Pushed schedules use numeric installation/repository identity and AppScheduleID. Repository-rename history seeds its referenced run. The prior catch-up clamping test becomes TestArmRefusesCatchUpAboveCeiling, checking strict refusal and zero persisted rows.
- C internal/wingd: TestNodeHostAdmissionReadsItsRealRun generates unique participant IDs. Repeating an empty quick-check string previously requested a completed participant again and hit duplicate-request refusal. Generated arbitrary text still passes through identity encoding and real store terminal checks.
- C pkg/controller: TestEngineBootstrapExchangeReturnsOriginalUnitOnce gives the launcher exact gh:1 repository authority, preserving its metering, one-shot exchange and fencing assertions. An empty repository scope grants no repositories.
- C tests/acceptance: TestPortDestinations resolves the three moved enrollment/launcher tests under pkg/store and uses the canonical delete disposition for the retired claim/input route.
- C sparkwing: preference documentation checks follow the matcher contract: preferences rank eligible runners and cannot relax requirements, reservations or resource bounds. Update JobNode/JobGroup GoDoc and the generated SDK reference/mirror together. The existing placement-hold wording described the retired matcher.

- B pkg/controller: TestReviewNativeActionsRootIssuance and TestGitHubEnrollmentHTTPUsesDefaultTransactions retain the one-hour Actions expiry ceiling. Controller expiry now uses SQLite's millisecond precision and the shared limits constant. TestActionsExpiryFitsStoreClockProperty issues real credentials against generated clock offsets and rejects ceiling + 1ns. Its millisecond-boundary case failed before the fix. Each generated case uses an isolated store, preserving the live-credential quota.
- B pkg/controller: TestIdentityLinkHTTPRefusals and TestIdentityLinkHTTPProviderDiagnosticProperty exposed private provider errors in request logs. Identity-link error fallbacks return only their public envelope instead of joining private diagnostics. Both tests pass, including wrapped/joined typed provider errors. Invariant: private provider diagnostics reach neither the HTTP response nor application logs.
- C pkg/controller: TestOperationalQueueSamplerReadsFinalStoreStates expects zero observed live runners when it has seeded only node claims. Queue-state and pending-node assertions remain unchanged. Active runners come from runner observations.
- C pkg/controller: TestReviewConsoleCommitRevalidatesProofProperty uses a unique service registration per generated case, retaining direct-session, configured-operator, foreign-proof and derived-proof refusals.
- C pkg/controller: TestCronLauncherActive_TreatsAnUnclaimedRunAsStale seeds the pending run without calling the retired trigger writer. Both freshness windows and missing-run refusal pass. The three tick tests now supply numeric GitHub IDs and expose the native-admission regression below.
- A pkg/controller: delete TestTrigger_PendingTransitionsToRunning (41 lines). It called the reporting SeedRun twice to assert the retired CreateRun upsert. State changes belong to native transition transactions. Existing native lifecycle and fence tests remain.
- C bin: TestInstallToGreenTidiesSoACandidateDependencyResolves disables VCS stamping in its scratch fixture, preserving both the missing dependency-graph failure and successful post-tidy build. The sandbox mounts an empty /tmp/.git.
- C tests/acceptance: TestEngineLifecycleHTTP supplies retry mode failed, retaining all revoked/wrong-grant/body/race/private-error assertions.
- C tests/acceptance: TestDashboardRequestsReachControllerHandlers uses a non-GitHub source with the fake engine. GitHub identity resolution needs the real store/provider path. Actual dashboard request generation, registration, argument forwarding and commit counts still run.
- C tests/acceptance: TestEngineRunDetailRefusesUnrelatedAuthority expects NotFound for a foreign tenant, preserving zero reader calls and private-field exclusion. Selector resolution conceals foreign resources before relational authorization.
- C tests/acceptance: TestSessionPolicyHTTPLifetimeProperty seeds created_at at database millisecond precision. Its prior elapsed=0 case could otherwise appear to be from the future. Future issuance, exact maximum lifetime, no renewal and credential secrecy assertions remain.
- C tests/acceptance: FuzzStoreQueueClaimAuthorityW8 keeps the parent's binding and repository scope when minting its deliberately reduced-grant child. The test continues to refuse mixed claims lacking a grant, invalid kinds and revoked credentials.
- B verification: remove the untagged inline pinnedClock comment in pkg/store/store.go. The required commentcheck then passes. No store behavior changed.

## Verification

- GOWORK=off GOFLAGS=-buildvcs=false GOCACHE=/tmp/sw-gocache, go test -p 4: full internal/apiroutes, internal/fssecure, internal/profile, internal/userconfig, internal/crons, internal/repos, internal/pipelineentry and sparkwing suites pass. The initial full internal/nodehost suite also passed.
- Fourteen focused controller/acceptance groups pass across the final two batches. These cover native bootstrap exchange/metering, Actions expiry and enrollment/revocation, identity-link privacy, console provisioning proof, queue metrics, retry lifecycle, inventory destinations, dashboard requests, tenant-concealed run detail, session lifetime and runner claim authority.
- TestNodeHostAdmissionReadsItsRealRun, FuzzRetiredProfileFieldsKeepValuesPrivate seeds and TestInstallToGreenTidiesSoACandidateDependencyResolves pass. TypeScript SDK build completed before repository-description verification.
- Whole-repository go build -p 4 ./... passes. go vet -p 4 passes for all changed Go packages and their tested direct dependents. go run ./internal/commentcheck -base main . passes across 2660 files. git diff --check passes.
- The controller sweep attempted all 745 top-level tests in batches that resume after a panic. At that snapshot, 475 completed without failure, 247 required sockets (244 panics and three ordinary failures), and 23 had other failures. Five of those 23 are now fixed or deleted. Eighteen remain below. This was a failure census, not a green full-suite result.
- The acceptance sweep attempted all 405 top-level tests. At that snapshot 390 completed without failure and 15 failed. Eight failing groups were subsequently fixed and verified. Seven remain below. Contract skeletons still skip their cells and do not establish product acceptance.
- Full cmd/sparkwing, cmd/sparkwing-controller, internal/web and bin runs stop at socket failures. The attempted socket cases are named in [test-reconciliation-sockets.md](test-reconciliation-sockets.md). No socket-dependent test was skipped or deleted to pass this sandbox. PostgreSQL was not attempted because both TCP and Unix socket operations were refused. Lead verification must run the controller/acceptance paths with PostgreSQL and complete the native transport cases.

## Remaining reconciliation

Each entry stays enabled. B means behavior still owed or uncertain, so its assertion must survive. C identifies a fixture or expectation change still to make.

- B OPEN pkg/controller: TestCronTick_TwoControllersSharingAStoreFireOneTrigger, TestCronTick_ARepeatedInstantReachesTheFirstRun and TestCronTick_AScheduleFiresInTheTeamThatArmedIt need native scheduled admission in cronLauncher. Actual failure: triggers has no column named pipeline. Keep exclusive leased ticks, idempotency and tenant isolation. Existing legacy admitTrigger/CreateTriggerWithRun callers need migration, not a compatibility schema.
- B OPEN pkg/controller: TestReviewConsoleCredentialCommits. Creating a deployment token through a direct console session succeeds, but reading it returns Forbidden. managementAuthorityTx reloads a session without restoring/rechecking its console proof. Reuse the existing transaction-local console predicate, with generated current-configuration/ancestry tests before changing authority behavior.
- B OPEN pkg/controller: TestSourceSnapshotRealClientStorageAndIntake. The real source-upload client fails authorization. Preserve upload binding, byte proof and intake immutability.
- C OPEN pkg/controller: TestComputeLimits_CronBelowTheMinimumIntervalIsRefused, TestComputeLimits_CronLaunchesCarryTheArmingPrincipal and TestComputeLimits_CronRunNowAtTheCapAnswers429 stop in newFixture's stranger token provisioning because its requested grants exceed its account role. Seed current scoped authority before checking the limits. Do not relax provisioning or limits.
- C OPEN pkg/controller: TestControllerCrons_ASchedulesTeamIsTheOnlyOneThatSeesIt, TestControllerCrons_RunNowLaunchesInTheSchedulesTeam, TestControllerCrons_ATeamsSchedulesAreCapped, TestControllerCrons_RunNowCreatesOneTriggerAndPendingRunPerDueInstant and TestControllerCrons_ASecondLaunchOfOneDueInstantReachesTheFirstRun have the same stranger-provisioning fixture failure. Their tenant, quota, principal and replay assertions remain required. Native scheduled admission is also needed.
- B OPEN pkg/controller: TestGitHubRunnerWorkflowDocMatchesTheRenderedTemplate. Docs no longer include the emitted workflow. The production template still invokes the retired runner subcommand/flags, so synchronize the runnable workflow and docs against the current runner contract before changing this test.
- C OPEN pkg/controller: TestControllerPipelines_ListIsTheCallersTeamOnly has the same stranger-provisioning fixture failure. Preserve tenant filtering.
- C OPEN pkg/controller: TestFinishRunFoldsPartialSamplesRaiseOnly uses a stale finish/session fixture and gets session unavailable. Keep raise-only sizing evidence while moving the fixture to current authority and result reporting.
- C OPEN pkg/controller: TestTrendsRejectsStoreQueryFailures gets session unavailable before its query/scan/iteration failures. Restore current authenticated fixture evidence and retain loud query failure.
- B OPEN pkg/controller: TestListRoutes_ClampTheirLimit seeds retired triggers. The triggers half is retired, but event pagination still needs a current authenticated test. Split/port that obligation before deleting the legacy group.
- C OPEN tests/acceptance: TestDeploymentHTTPAuthorization assumes an unbound service with the exact deployment grant is forbidden. Current deployment authority permits it and the nil-store fixture panics after authorization. Add a current service-backed fixture with explicit allowed-route expectations, retaining all forbidden account/run-bound/session cases.
- C OPEN tests/acceptance: TestEngineJobsHTTP, TestEngineJobsHTTPRefusals and TestEngineJobsMemberReadsFinishedJobs expect the former bare JobDetail reader. GET job now returns DebugJob and reads a committed RunView. Supply current contract fakes/reader evidence and keep all masking, tenancy, claim fencing and finished-job read assertions. One foreign-tenant refusal also needs the selector's NotFound expectation.
- C OPEN tests/acceptance: TestLoginHTTPRefusesInvalidCredentialAndForm cannot provision its machine fixture under the presenting role. Seed a currently authorized machine origin while keeping login refusal for machine/operator roots.
- B OPEN tests/acceptance: TestOAuthHTTPProviderAndStoreRefusals receives a 200 page where the roleless/waitlisted fixture expects refusal. Check the recorded waitlist sign-in decision before changing the expectation. Keep absence of application authority and credential/privacy assertions.
- B OPEN tests/acceptance: TestOperatorHTTPRefusesNonConsoleAuthority sometimes returns Unauthenticated where its fake expects Forbidden. Reconcile credential presentation/class/liveness evidence without permitting any console action.

## Contracts, shared files and budget

No runtime contract shape changed. Only the port-inventory paths and the retired-route disposition were corrected in contract documents. Lead-owned amendments and handoffs are unchanged. No new production mechanism, configuration variable or compatibility API was added.

Session production Go delta: +21/-20, net +1 line across the integration slice. Combined pkg/store delta against main remains -4,170 lines, with the inherited 80-line budget justification in pkg/store/W1b-HANDOFF.md. This area remains incomplete because the open tests, legacy scheduled admission and socket/PostgreSQL verification remain.

The old upsert test deletes 41 lines. The old 42-line catch-up clamping test was replaced with strict refusal plus zero-persistence assertions. No security, masking, tenancy or billing assertion was deleted.

Shared paths touched by this session:

- `bin/install_to_green_test.go`.
- `design/rewrite/contracts/t1-credential-routes.md`.
- `design/rewrite/contracts/t1-port-inventory.md`.
- `docs/sdk-reference.md`.
- `internal/apiroutes/apiroutes.go`.
- `internal/apiroutes/apiroutes_test.go`.
- `internal/crons/arm_test.go`.
- `internal/crons/crons_test.go`.
- `internal/crons/pushed_test.go`.
- `internal/fssecure/owned_dirs_test.go`.
- `internal/pipelineentry/environment_test.go`.
- `internal/profile/profile_test.go`.
- `internal/repos/describe_test.go`.
- `internal/repos/sandbox_test.go`.
- `internal/userconfig/contract_test.go`.
- `internal/userconfig/userconfig.go`.
- `internal/userconfig/validate.go`.
- `internal/wingd/admission_run_identity_test.go`.
- `pkg/controller/crons_tick_test.go`.
- `pkg/controller/engine_amendment_test.go`.
- `pkg/controller/github_actions_exchange.go`.
- `pkg/controller/github_runners.go`.
- `pkg/controller/identity_link_routes.go`.
- `pkg/controller/operator_queue_internal_test.go`.
- `pkg/controller/review_actions_native_test.go`.
- `pkg/controller/review_authority_native_test.go`.
- `pkg/controller/trigger_test.go`.
- `pkg/docs/mirror/sdk-reference.md`.
- `pkg/store/store.go`.
- `sparkwing/combinator.go`.
- `sparkwing/plan.go`.
- `sparkwing/plan_runner_docs_test.go`.
- `tests/acceptance/dashboard_requests_test.go`.
- `tests/acceptance/engine_lifecycle_http_test.go`.
- `tests/acceptance/engine_run_detail_http_test.go`.
- `tests/acceptance/session_policy_http_test.go`.
- `tests/acceptance/store_queue_w8_test.go`.
- `design/rewrite/test-reconciliation.md`.
- `design/rewrite/test-reconciliation-sockets.md`.
