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

- B pkg/controller and pkg/store: TestReviewConsoleCredentialCommits now passes. managementAuthorityTx uses the existing console-proof resolver, rechecking the direct session and current operator configuration in its transaction. TestConsoleInventoryRevalidatesSessionProperty generates configuration, ancestry and foreign-session changes. Invariant: cached console authorization never substitutes for a current direct configured operator session.
- C tests/acceptance: TestLoginHTTPRefusesInvalidCredentialAndForm supplies the required repository allowlist when provisioning its machine fixture. Machine and operator login remain forbidden, with no session or credential leakage.
- C tests/acceptance: TestOperatorHTTPRefusesNonConsoleAuthority expects Unauthenticated when a NodeHost-audience fake has no live lease. Every operator action remains refused.
- C tests/acceptance: replace the obsolete roleless-refusal case in TestOAuthHTTPProviderAndStoreRefusals with TestOAuthHTTPWaitlistedSessionHasNoTeamAuthority. The recorded roleless-session contract permits sign-in. The new HTTP test proves empty team/scopes and refusal of runs, tokens and console routes while retaining provider/session privacy assertions.
- B/C tests/acceptance and pkg/controller: TestEngineJobsHTTP, TestEngineJobsHTTPRefusals and TestEngineJobsMemberReadsFinishedJobs use DebugJob and the existing committed RunStatusFacts callback. Job detail now honors that callback for embedded controllers and tests, retains the concrete store default and rejects an empty persisted job identity. All claim, tenant, cancellation, masking and read-count assertions remain. Foreign tenant selectors expect NotFound.
- C pkg/controller: newCronsFixture gives its no-read token the valid runs.write grant instead of removed triggers.read and serves the real handler through an in-memory recorder. TestControllerPipelines_ListIsTheCallersTeamOnly passes with its tenant and missing-grant refusals. Cron fixtures use /schedules and DELETE /schedules/{id}; their missing concrete ScheduleWriter remains visible.
- C pkg/controller: TestTrendsRejectsStoreQueryFailures authenticates with current runs.read authority and first verifies a healthy response. Corrupt views retain team columns and memoized spelling so scan and iteration cases reach their intended failures.
- A/C pkg/controller: TestEventListClampsItsLimit replaces TestListRoutes_ClampTheirLimit. Remove the retired trigger-list assertion and writer, retain the current event-page ceiling and authenticate through the real issuer/router using an in-memory HTTP recorder. No event assertion was dropped.
- B pkg/controller and docs: GitHubRunnerWorkflow emits warm rather than the removed runner command. TestGitHubRunnerWorkflowUsesCurrentGrammar fails on the old template and parses the final invocation through the actual runner parser. TestGitHubRunnerWorkflowDocMatchesTheRenderedTemplate passes with the workflow restored to source docs and embedded mirror.

- C tests/acceptance: TestDeploymentHTTPAuthorization permits an unbound service bearing the exact deployment grant, as C2 requires. Use a real SQLite read store and a recording OperatorSettingsWriter fake for the missing committing adapter. Assert successful handler delivery, exact forwarded authority and zero writer calls for every refused credential. All account/run-bound/session/CSRF refusals remain. Production settings-write composition remains open.

- C pkg/controller: TestSourceSnapshotRealClientStorageAndIntake configures a bound cipher, machine ownership/consent and a confirmed git credential with username. Its prior policy_missing was a fixture omission. Actual upload, immutable intake/replay, source release, signed bundle transfer, checkout, release audit and storage accounting now run together. TestSourceSnapshotAuthorityAndRevisionProperty still rejects forged revisions/repositories and revoked-member uploads. No production authorization changed.

## Verification

- GOWORK=off GOFLAGS=-buildvcs=false GOCACHE=/tmp/sw-gocache, go test -p 4: full internal/apiroutes, internal/fssecure, internal/profile, internal/userconfig, internal/crons, internal/repos, internal/pipelineentry and sparkwing suites pass. The initial full internal/nodehost suite also passed.
- Fourteen focused controller/acceptance groups pass across the final two batches. These cover native bootstrap exchange/metering, Actions expiry and enrollment/revocation, identity-link privacy, console provisioning proof, queue metrics, retry lifecycle, inventory destinations, dashboard requests, tenant-concealed run detail, session lifetime and runner claim authority.
- TestNodeHostAdmissionReadsItsRealRun, FuzzRetiredProfileFieldsKeepValuesPrivate seeds and TestInstallToGreenTidiesSoACandidateDependencyResolves pass. TypeScript SDK build completed before repository-description verification.
- Whole-repository go build -p 4 ./... passes. go vet -p 4 passes for all changed Go packages and their tested direct dependents. go run ./internal/commentcheck -base main . passes across 2660 files. git diff --check passes.
- The controller sweep attempted all 745 top-level tests in batches that resume after a panic. At that snapshot, 475 completed without failure, 247 required sockets (244 panics and three ordinary failures), and 23 had other failures. That snapshot predates the continuation below. Twelve controller groups remain unresolved; the listed socket failures still need native verification. This was a failure census, not a green full-suite result.
- The acceptance sweep attempted all 405 top-level tests. At that snapshot 390 completed without failure and 15 failed. All fifteen failing groups have now been reconciled across the two sessions. The final full acceptance rerun passes, as recorded below. Contract skeletons still skip their cells and do not establish product acceptance.
- Full cmd/sparkwing, cmd/sparkwing-controller, internal/web and bin runs stop at socket failures. The attempted socket cases are named in [test-reconciliation-sockets.md](test-reconciliation-sockets.md). No socket-dependent test was skipped or deleted to pass this sandbox. PostgreSQL was not attempted because both TCP and Unix socket operations were refused. Lead verification must run the controller/acceptance paths with PostgreSQL and complete the native transport cases.

### Continuation verification

All Go commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOCACHE=/tmp/sw-gocache, GOMAXPROCS=4 and TMPDIR/GOTMPDIR=/tmp/sw-astra-W1b.

- `go test -json -p 4 ./tests/acceptance -count=1 -timeout=5m` passes in 187.844s: 406 top-level tests pass; all levels contain 5,386 passing and 2,991 skipped tests/cells. The skips pre-exist this continuation. A green Go suite does not prove skipped native/S3/PostgreSQL cells. No skip was added or widened.
- Focused controller checks pass for console issuance/inventory/revocation and generated proof revalidation, current debug projection and tenant-selector fuzz seeds, source snapshot acquisition/audit/accounting and authority/revision properties, pipeline tenant filtering, event pagination, trends query failures, and generated workflow grammar/docs. `go test -p 4 ./pkg/controller -run '^TestSourceSnapshot' -count=1 -timeout=2m` passes after the final audit assertion.
- `go test -p 4 ./pkg/store -run '^(Test.*(Inventory|CapabilityManagement|CapabilityRotation|Console|Credential).*|TestReview.*)$' -count=1 -timeout=2m` passes. This exercises the shared authority change beyond the controller regression.
- `go test -json -p 4 ./pkg/controller -run '^(TestCronTick_.*|TestComputeLimits_Cron.*|TestControllerCrons_.*|TestFinishRunFoldsPartialSamplesRaiseOnly)$' -count=1 -timeout=2m` confirms exactly the twelve enabled top-level failures below. They are real integration obligations, not socket refusals.
- Whole-repository `go build -p 4 ./...`, vet for controller/store/docs/acceptance, commentcheck against main, embedded-doc drift/privacy tests, CLI-doc grammar tests and `git diff --check` pass.
- The socket inventory from the prior continuation remains [test-reconciliation-sockets.md](test-reconciliation-sockets.md). This continuation reproduced TCP listen refusal before replacing the cron/pipeline fixture listener with a recorder. PostgreSQL was not attempted because socket creation is forbidden. Native controller transport and PostgreSQL lanes still need lead verification.
- Source diagnosis used a temporary Go overlay to expose the policy_missing boundary; no instrumentation remains in the repository. Git has no local user identity or core.worktree override. No Git writes, Sparkwing execution, services or releases were performed. Scratch is deleted after the final checks; the shared Go cache remains.

## Remaining reconciliation

Each entry stays enabled. B means behavior still owed or uncertain, so its assertion must survive. C identifies a fixture or expectation change still to make.

- B OPEN pkg/controller: TestCronTick_TwoControllersSharingAStoreFireOneTrigger, TestCronTick_ARepeatedInstantReachesTheFirstRun and TestCronTick_AScheduleFiresInTheTeamThatArmedIt need native scheduled admission in cronLauncher. Actual failure: triggers has no column named pipeline. Keep exclusive leased ticks, idempotency and tenant isolation. Existing legacy admitTrigger/CreateTriggerWithRun callers need migration, not a compatibility schema.
- B OPEN pkg/controller: TestComputeLimits_CronBelowTheMinimumIntervalIsRefused now reaches the missing concrete OperatorSettingsWriter and returns policy_missing. TestComputeLimits_CronLaunchesCarryTheArmingPrincipal and TestComputeLimits_CronRunNowAtTheCapAnswers429 reach the missing concrete ScheduleWriter on /schedules/repos. Fixture authority and transport are reconciled. Provisioning, principal and limit assertions remain unchanged.
- B OPEN pkg/controller: TestControllerCrons_ASchedulesTeamIsTheOnlyOneThatSeesIt, TestControllerCrons_RunNowLaunchesInTheSchedulesTeam, TestControllerCrons_ATeamsSchedulesAreCapped, TestControllerCrons_RunNowCreatesOneTriggerAndPendingRunPerDueInstant and TestControllerCrons_ASecondLaunchOfOneDueInstantReachesTheFirstRun reach the missing concrete ScheduleWriter on /schedules/repos. Retain tenant, quota, principal and replay assertions. Native scheduled admission is also needed. Existing schedule_routes_test.go exercises that declared interface with fakes; this suite retains the missing production-composition signal.
- B OPEN pkg/controller: TestFinishRunFoldsPartialSamplesRaiseOnly still uses an unauthenticated legacy finish request. The deeper obligation is the missing profile_fold completion-outbox delivery worker. Frozen v4 §3.9 requires terminal, idempotent folding through the elected reconciler; do not restore direct finish-handler side effects. Direct fold tests already pass. Port the HTTP fixture alongside the real outbox delivery and preserve raise-only partial-sample assertions.

## Contracts, shared files and budget

No signature or wire shape changed. C2 now records transaction-local console-proof revalidation for credential management and job detail's use of the existing committed ReadRunStatus callback. The default remains Tenant.ReadRunView. Earlier port-inventory corrections remain. Lead-owned amendments and handoffs are unchanged. No production interface, configuration variable or compatibility API was added. ScheduleWriter, OperatorSettingsWriter and completion-outbox composition remain real integration points.

Previous session production Go delta: +21/-20, net +1. This continuation: +18/-11, net +7, all in three production files. pkg/store has no net line growth, so the inherited combined store delta (-4,170) and its 80-line exception in pkg/store/W1b-HANDOFF.md are unchanged. The seven added production lines let the existing committed status callback serve job detail while preserving its concrete default. All other production changes replace existing lines.

The 44-line TestListRoutes_ClampTheirLimit becomes the 28-line TestEventListClampsItsLimit: remove the retired trigger-pagination writer/assertions and preserve authenticated event pagination (net -16 test lines). The old node-only ReadNode fake is replaced with committed RunStatusFacts. No security, tenant-boundary, masking or billing assertion is removed. No native acceptance skeleton is unskipped or claimed complete. W1b's earlier billing/retention amendments and handoffs are already completed in pkg/store/W1b-HANDOFF.md and were not reimplemented.

Shared paths touched by this continuation:

- `design/rewrite/contracts/c2-credentials.md`.
- `design/rewrite/test-reconciliation.md`.
- `docs/github-actions-runners.md`.
- `pkg/controller/compute_limits_test.go`.
- `pkg/controller/crons_team_test.go`.
- `pkg/controller/crons_test.go`.
- `pkg/controller/engine_jobs.go`.
- `pkg/controller/github_runners.go`.
- `pkg/controller/github_runners_test.go`.
- `pkg/controller/review_authority_native_test.go`.
- `pkg/controller/source_upload_test.go`.
- `pkg/controller/trends_test.go`.
- `pkg/controller/trigger_test.go`.
- `pkg/docs/mirror/github-actions-runners.md`.
- `pkg/store/capability_inventory.go`.
- `tests/acceptance/deployment_authorization_test.go`.
- `tests/acceptance/engine_jobs_http_test.go`.
- `tests/acceptance/login_http_test.go`.
- `tests/acceptance/oauth_http_test.go`.
- `tests/acceptance/operator_authorization_test.go`.
