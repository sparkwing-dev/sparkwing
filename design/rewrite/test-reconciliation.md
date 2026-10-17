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
- `go test -json -p 4 ./pkg/controller -run '^(TestCronTick_.*|TestComputeLimits_Cron.*|TestControllerCrons_.*|TestFinishRunFoldsPartialSamplesRaiseOnly)$' -count=1 -timeout=2m` confirmed twelve enabled top-level failures at that snapshot, before continuation 3. They are real integration obligations, not socket refusals.
- Whole-repository `go build -p 4 ./...`, vet for controller/store/docs/acceptance, commentcheck against main, embedded-doc drift/privacy tests, CLI-doc grammar tests and `git diff --check` pass.
- The socket inventory from the prior continuation remains [test-reconciliation-sockets.md](test-reconciliation-sockets.md). This continuation reproduced TCP listen refusal before replacing the cron/pipeline fixture listener with a recorder. PostgreSQL was not attempted because socket creation is forbidden. Native controller transport and PostgreSQL lanes still need lead verification.
- Source diagnosis used a temporary Go overlay to expose the policy_missing boundary; no instrumentation remains in the repository. Git has no local user identity or core.worktree override. No Git writes, Sparkwing execution, services or releases were performed. Scratch is deleted after the final checks; the shared Go cache remains.

## Remaining reconciliation

Continuation 4 leaves eleven enabled cron failures and the GitHub, metrics and parent-notification completion lanes. Terminal profile delivery is implemented below.

Each entry stays enabled. B means behavior still owed or uncertain, so its assertion must survive. C identifies a fixture or expectation change still to make.

- B OPEN pkg/controller: TestCronTick_TwoControllersSharingAStoreFireOneTrigger, TestCronTick_ARepeatedInstantReachesTheFirstRun and TestCronTick_AScheduleFiresInTheTeamThatArmedIt need native scheduled admission in cronLauncher. Actual failure: triggers has no column named pipeline. Keep exclusive leased ticks, idempotency and tenant isolation. Existing legacy admitTrigger/CreateTriggerWithRun callers need migration, not a compatibility schema.
- B OPEN pkg/controller: TestComputeLimits_CronBelowTheMinimumIntervalIsRefused now commits its ceiling through the concrete OperatorSettingsWriter, then reaches the missing concrete ScheduleWriter and returns policy_missing. TestComputeLimits_CronLaunchesCarryTheArmingPrincipal and TestComputeLimits_CronRunNowAtTheCapAnswers429 reach the missing concrete ScheduleWriter on /schedules/repos. Fixture authority and transport are reconciled. Provisioning, principal and limit assertions remain unchanged.
- B OPEN pkg/controller: TestControllerCrons_ASchedulesTeamIsTheOnlyOneThatSeesIt, TestControllerCrons_RunNowLaunchesInTheSchedulesTeam, TestControllerCrons_ATeamsSchedulesAreCapped, TestControllerCrons_RunNowCreatesOneTriggerAndPendingRunPerDueInstant and TestControllerCrons_RunNowReplaysTheSameRequest reach the missing concrete ScheduleWriter on /schedules/repos. Retain tenant, quota, principal and replay assertions. Native scheduled admission is also needed. Existing schedule_routes_test.go exercises that declared interface with fakes; this suite retains the missing production-composition signal.
- B CLOSED in continuation 4 pkg/controller: the profile_fold completion-outbox delivery worker is implemented. TestFoldRunProfilesPartialSamplesRaiseOnly now preserves the old finish test's sample assertions at the fold component. Frozen v4 §3.9 requires terminal, idempotent folding through the elected reconciler; do not restore direct finish-handler side effects. Direct fold tests already pass. Add real terminal-transition-through-outbox evidence when that worker exists.

## Contracts, shared files and budget from continuations 1-2

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


## Continuation 3: operator settings composition

- B pkg/controller: TestOperatorSettingsDefaultWriterCommitsThroughHTTP exercises all five settings writes with real bootstrap and service credentials. Operator implements the declared writer. Compute, storage-policy, quota and allowance changes reuse their existing transaction helpers. Sign-up batches preserve omitted fields and commit provenance with the mode and limits. TestComputeLimits_CronBelowTheMinimumIntervalIsRefused now sets its ceiling through HTTP and reaches the still-missing ScheduleWriter.
- B pkg/store: TestOperatorSettingsRevalidateAuthorityProperty generates stale-authority cases across every settings write. Revoked, expired, grantless, tenant-scoped, bound and account credentials cannot persist settings. TestOperatorSettingsConsoleProofProperty covers removed configuration, revoked and derived sessions, forged references and a gate removed before commit. TestOperatorSignUpBatchRollsBackInvalidSibling retains atomic mode/limit rollback.
- B pkg/store: TestDeploymentBindingAllowsOnlyUnboundTeamSelectionProperty fixes the quota route's refusal of an authorized deployment credential selecting a team. BindNone resolves current console proof and permits deployment team selection. Generated run/job selectors and foreign tenant credentials remain refused. Route grants still come from the shared policy table.
- C shared callers: move SignUpSettingsChange from controller to store because Operator owns its committing transaction. Migrate controller signup decoding, OperatorSettingsWriter, its fixture and the acceptance deployment writer. No alias or compatibility API remains.

- A/C pkg/controller: TestFinishRunFoldsPartialSamplesRaiseOnly becomes TestFoldRunProfilesPartialSamplesRaiseOnly. Remove the retired direct finish-handler side-effect expectation (§3.9), seed a terminal run, and exercise the existing fold component. Preserve partial-above, partial-below and unknown-sample assertions and apply them to both job and run profiles. The completion outbox still has no delivery worker and remains B OPEN.
- A/C pkg/controller: replace TestControllerCrons_ASecondLaunchOfOneDueInstantReachesTheFirstRun's retired CreateTrigger/FindTriggerByIdempotencyKey fixture with TestControllerCrons_RunNowReplaysTheSameRequest. Both calls now use the real current schedule route and the same request key, assert replay identity and exactly one persisted run. The due-instant key-format assertion moves to TestCronTick_ARepeatedInstantReachesTheFirstRun. Schedule HTTP fixtures carry required request keys. Native schedule integration remains B OPEN.

- B pkg/controller: TestOperatorSettingsConsoleCommitsThroughHTTP exercises the same five writes through real session authentication, CSRF, current operator configuration and the concrete writer. A configured operator can update another team's quota. Removing that configuration refuses every write. Signup provenance records the actual account.

### Continuation 3 evidence and limits

All Go checks use GOWORK=off, GOFLAGS=-buildvcs=false, GOCACHE=/tmp/sw-gocache, GOMAXPROCS=4 and TMPDIR/GOTMPDIR=/tmp/sw-astra-W1b.

- `go test -json -p 4 ./tests/acceptance -count=1 -timeout=5m` passes in 199.877s. All 406 top-level tests pass, with 5,386 passing and 2,991 pre-existing skipped tests/cells. No skip was added or widened. No acceptance skeleton was unskipped.
- `go test -p 4 ./pkg/controller -run '^(TestOperatorSettings.*|TestStoreDataStorage.*|TestReviewConsole.*|TestReviewNativeActions.*)$' -count=1 -timeout=3m` passes in 1.144s. The subsequently added TestOperatorSettingsConsoleCommitsThroughHTTP passes separately in 0.136s.
- Store checks covering compute limits, storage policy/quota/allowance, signup limits/mode, binding, console, credential inventory/management and runner metering pass in 9.473s. The final generated exact-grant property passes in 2.755s, including operator.limits without operator.object_store and the reverse. Console-proof and deployment-selector properties also pass.
- `go test -p 4 ./pkg/controller -run '^TestFoldRunProfiles' -count=1 -timeout=2m` passes in 0.398s. `TestScheduleHTTP*` passes through the declared ScheduleWriter fake in 0.258s. Those component tests do not establish native schedule intake or outbox delivery.
- A temporary Go overlay restoring HEAD's authority_binding.go makes TestDeploymentBindingAllowsOnlyUnboundTeamSelectionProperty fail on its first generated input. The real implementation passes. No overlay or instrumentation remains after scratch cleanup.
- Whole-repository `go build -p 4 ./...`, `go vet -p 4 ./pkg/store ./pkg/controller ./tests/acceptance`, commentcheck against main and `git diff --check` pass. Commentcheck examines 2,663 files.
- The focused cron/profile sweep confirms eleven remaining enabled cron failures and the passing ported profile test. The three tick cases still reach retired trigger SQL. Eight schedule HTTP cases still lack a concrete ScheduleWriter. The minimum-interval case now passes its operator settings write before reaching that missing owner. The profile completion-outbox worker remains an open production integration point.
- A broader controller selection stopped at TestOperatorDeletesAnAccountByEmailAndATeamBySlug because its httptest listener cannot open a socket. That case is already named in test-reconciliation-sockets.md. Fresh TCP and Unix bind probes both fail with Operation not permitted. PostgreSQL was not attempted. Lead verification still owes the listed native controller/CLI/web/bin cases and PostgreSQL controller/acceptance evidence. No socket test was skipped or removed.

### Continuation 3 contract, files and budget

C2 records the concrete Operator settings writer and transaction-local console/deployment team selection. The only declared type-location change moves SignUpSettingsChange to store. Its callers in controller/signup_gate.go, controller/operator_settings_writes.go, controller/operator_settings_test.go and acceptance/deployment_authorization_test.go are migrated. No wire shape, configuration variable, compatibility alias or interface was added. Lead-owned amendments and handoffs remain unchanged. ScheduleWriter and trusted scheduled admission, followed by the completion-effect worker, still need real integration.

Production Go changes are +198/-62, net +136 against this session's HEAD. Store accounts for net +154 and controller for net -18. This fills five already-declared committing methods. They share existing transaction helpers instead of duplicating settings SQL. Calling the raw setters from handlers would omit the required authority/rollback boundary and is not a smaller valid implementation. The lead's area-budget filters measure integrated pkg/store at -2,971 against main, versus -3,125 before this session. The -4,250 target is exceeded by 1,279 lines, including 1,125 inherited lines. The prior continuation's -4,170 figure belongs to its earlier snapshot. This remains CONTINUE and does not claim the area budget is satisfied.

The retired replay test's 29-line trigger-table fixture becomes an 18-line HTTP replay test. Seven direct-finish HTTP lines are removed from the profile test. All sample-value assertions survive, now for both job and run profiles. No security, tenant-boundary, masking or billing assertion is removed.

Shared files touched: design/rewrite/contracts/c2-credentials.md, design/rewrite/test-reconciliation.md; pkg/controller/{crons_test.go,crons_tick_test.go,operator_settings_native_test.go,operator_settings_test.go,operator_settings_writes.go,operator_settings_writes_test.go,profiles_test.go,signup_gate.go}; pkg/store/{authority_binding.go,compute_limits.go,operator_settings.go,operator_settings_test.go,signup_gate.go,storage_policy.go,storage_quota.go}; tests/acceptance/deployment_authorization_test.go.

Friction: the declared schedule writer and trusted scheduled intake have no concrete production composition, and the outbox has no completion delivery worker. Existing interface fakes and component tests remain runnable. The sandbox forbids native transport and PostgreSQL. No Git writes or Sparkwing candidate execution were performed. Local Git configuration has no user.name or core.worktree override. Scratch is removed after final checks and /tmp/sw-gocache is retained.


## Continuation 4: terminal profile delivery

- B pkg/controller: TestProfileCompletionFromRealTerminalTransition drives a real owner finish through the host HTTP adapter, then the elected completion reader and exact SQLite profiles. No observation appears before delivery. Three reconciliations produce one job and one run sample and acknowledge precisely revision one. The finish path stays free of profile side effects.
- B pkg/store: TestProfileCompletionFoldsAndAcknowledgesAtomicallySQLite aborts the outbox acknowledgement after both profile writes and proves rollback, then retries five times and sees one sample. Other effect types remain due. TestProfileCompletionInvalidSiblingRollsBackBatch retains complete-batch rollback. TestProfileCompletionCannotCommitAfterLeaseExpires and TestProfileCompletionRollsBackWhenLeaseLapsesDuringFold cover expiry before and during the committing transaction.
- B pkg/store: TestProfileCompletionConcurrentReplaysFoldOnce races eight deliveries through the real transaction and observes one committed fold. The replay, invalid-batch and generated authority cases use the suite-selected SQLite/PostgreSQL store. SQLite-specific trigger and injected-clock cases name their dialect.
- B pkg/store: TestProfileCompletionCannotOutliveLeaseOrCrossTenantProperty generates foreign tenants, holders, work names, generations, future revisions and wrong effect kinds. Refused deliveries preserve due work and profile state. Invariant: a terminal run's observations and exact delivered revision commit together under a current completion fence.
- B pkg/controller: TestProfileCompletionRetriesCorruptMetricsWithoutPartialFold retains the failed metric read, verifies a due retry without any profile mutation, repairs the data, advances the injected clock and observes one successful sample after replay. No sleeps or test skips are added.
- C pkg/controller: TestFoldRunProfilesPartialSamplesRaiseOnly and TestFoldRunProfilesRequiresSingleContributingNode retain their component assertions against collected observations. TestFoldRunProfilesMissingNodeEvidence now requires corrupt metric reads to fail loudly, then repairs the corrupt fixture and retains valid-sample, measured-zero, memoized and pin assertions. No security, tenant-boundary, masking or billing assertion changes.
- B internal/wingd/localcontroller: TestCompositionCompletesRunsAfterReadinessAndJoinsOnFailure checks the shared completion callback after readiness, with the elected store still open, and joined shutdown on callback failure. cmd/sparkwing/local_services.go binds the real callback. Completion is independent of the slower object-store scan.
- A pkg/store: move the fully implemented completion_contract.go to completion.go and update C1's file inventory. No declaration shim remains. The former controller fold's immediate profile writes are replaced by one atomic store batch. No test group is deleted.
- VERIFIED existing W1b handoffs: decision 112 and M4 already exist in billing_policy.go, billing_pages.go and unit_renewal.go with current generated Cloud renewal and billing suites. They are not reimplemented in this continuation.

### Continuation 4 contract changes made

C1 records DueProfileFolds, ApplyProfileFold, ReconcileProfileFolds and the local
Services.Complete callback. These extend existing completion transactions and
local worker composition without schema, wire, configuration or credential
changes. Lead-owned amendments and handoffs remain unchanged.

Assumptions/mechanisms: the existing completion lease and outbox own the profile
lane. A filtered reader prevents the other unfinished lanes from filling its
batch. The existing HTTP-controller and local
maintenance loops call the same concrete reconciler. No new timer or worker goroutine is added. Calling the old fold and
acknowledging separately cannot give atomic replay or crash recovery. Revert
these changes as one unit if the lead elects to implement every lane together.

Remaining: eleven cron groups still fail and stay enabled. Automatic schedule
intake needs committed arming authority and verified repository facts through
the sole sealed intake; the current launcher still calls the retired trigger
writer. The ScheduleWriter production adapter is still missing. GitHub, metrics
and parent-notification completion delivery also remain open. No native matrix
cell is newly claimed complete. Full PostgreSQL and socket-dependent controller
transport still require the lead's native environment.


### Continuation 4 verification and budget

All Go commands use GOWORK=off, GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-gocache, GOMAXPROCS=4 and
TMPDIR/GOTMPDIR=/tmp/sw-astra-W1b. Tests run with -p 4 or -race -p 2.

- Whole-repository `go build -p 4 ./...` passes. Vet passes for store, controller, localcontroller, cmd/sparkwing and acceptance. Final commentcheck against main is clean across 2,667 files.
- The focused profile, capacity, existing completion-revision, Cloud renewal and local-composition selection passes in store (5.426s), controller (0.652s) and localcontroller (0.270s). The final expanded race selection passes in store (5.674s), controller (4.569s) and localcontroller (1.594s).
- Full internal/wingd/localcontroller passes (2.820s). Production local-controller constructor and local-service composition checks pass in cmd/sparkwing (0.566s). These use recorder transport and do not establish native socket startup.
- Existing decision 112 billing/model/pages and M4 renewal selection passes in pkg/store (7.848s). No billing production code or expectation changes.
- Full `go test -json -p 4 ./tests/acceptance -count=1 -timeout=5m` passes in 208.519s: 406 top-level tests, 5386 passing tests/cells and 2991 pre-existing skips. No skip is added or widened. No native acceptance skeleton is unskipped. This full run precedes the final switch to the existing maintenance wake loop. The final profile/reaper selection passes after that simplification (0.570s).
- TCP and Unix socket probes both fail with Operation not permitted. No PostgreSQL connection is attempted. Native full controller transport and PostgreSQL controller/acceptance lanes still require the lead; the existing named socket inventory is test-reconciliation-sockets.md. The new portable store property and concurrent replay tests also need the suite's PostgreSQL lane. The forced-SQLite rollback and clock tests stay SQLite proofs.

Production delta against this session's HEAD, matching the renamed owner:
+156/-41, net +115 lines. This comprises the profile completion worker, atomic
observation/acknowledgement join and two local composition edits. The old direct
fold and its maxF helper are removed. No test group is deleted.
The whole W1+W1b store area measures -2,728 lines against main under the lead's
budget exclusions, versus -4,250 budget (1,522 over). Its inherited HEAD measures
-2,968 (1,282 over). Of the 240-line measured increase, 188 are the existing
implemented completion scaffold becoming countable production; actual store
growth is 52 lines. This continuation cannot claim the area complete. Its
115-line whole-repository growth supplies the profile slice of the completion
reconciler already sized in frozen v4 §3.9. The prior ledger's smaller inherited
overage is stale for this branch snapshot.

Shared files: cmd/sparkwing/local_services.go;
internal/wingd/localcontroller/{controller.go,controller_test.go};
pkg/controller/{profile_completion.go,profile_completion_test.go,profiles.go,
profiles_test.go,profiles_coverage_test.go,profiles_aggregation_test.go,server.go};
pkg/store/{capacity.go,completion.go,profile_completion_test.go,
profile_completion_fence_test.go}; design/rewrite/contracts/c1-data.md and this
ledger. pkg/store/completion_contract.go is removed by the move. No amendment or
handoff file is edited. No Git writes or Sparkwing candidate execution.

Friction: the scheduled committing-origin contract is still absent. The
remaining cron assertions stay enabled instead of bypassing authority through
reporting seeds or raw trigger writes. The profile worker handles its own lane
under the existing completion lease; unfinished effect types retain their due
revisions. Native transport and PostgreSQL remain unavailable. Local Git has no
user identity or core.worktree override. W1b scratch is deleted after final
checks; the shared build cache remains.


### Scheduled-origin contract still needed

Invariant: a schedule may admit only its committed repository/source snapshot
under current tenant and arming authority. `cron_schedules.armed_by` is an actor
string, not a tagged authority reference, and `Server.ResolveIntake` still
refuses verified App deliveries. A caller-selected origin or actor cannot fill
that gap. Next work should persist the authorized schedule origin with verified
numeric repository identity, define its current membership/consent recheck, and
load those facts by tenant/schedule ID inside the same transaction that calls
admitRun. Run-now must also recheck the requesting runs.control authority and
its replay key there. Browser-session expiry versus durable schedule consent
needs an explicit contract choice. This proposal is recorded for lead review;
no synthetic credential, auth bypass or compatibility trigger schema was added.

## Continuation 5: GitHub completion delivery

- B pkg/controller: TestGitHubCompletionFromRealTerminalTransition failed with zero outbound checks after a real successful owner finish. The shared completion worker now reads GitHub revisions and profile work under one lease. Delivery errors retain the exact revision for a retry.
- B pkg/controller: generated authority and summary-privacy tests exercise sealed numeric identity, current installation ownership, suspension, repository renames, forged source facts and private node/error text through a recorder transport. No socket test is skipped.
- B pkg/store: per-run GitHub commit-status fallback moves from a process map to triggers.github_status_fallback. The hard-cut baseline is version 5. No migration or compatibility path is added.
- A pkg/controller: remove the in-memory GitHub queue, its bounded retry/drop loop and queue shutdown lifecycle. TestDrainGitHubChecks_DoesNotInheritASpentBudget and its 138-line helper file assert that retired queue. TestGitHubCompletionCancellationLeavesDurableWork replaces its delivery-survival obligation.
- C shared callers: bind local completion to ReconcileRunEffects and remove the deleted controller queue Shutdown calls from test fixtures and internal/web/browserfixture. The HTTP controller calls the same worker on its existing maintenance cadence.
- B OPEN cron: the focused census reproduces all eleven previously recorded failures. They remain enabled. The committed schedule-origin/consent contract described above is still required. No caller-selected actor or synthetic credential is added.
- C pkg/controller: port TestGitHubAppRunFinishLeavesAnotherTeamsCheckUnderTheSameID to TestGitHubCompletionRejectsForeignTenant and the generated installation property. The final schema has global run IDs, so the old duplicate-ID trigger fixture cannot exist.
- C pkg/controller: port TestGitHubAppOverlappingControllersCreateOneCheckRun to TestGitHubCompletionTwoControllersDeliverOnce. Keep the existing SQLite/PostgreSQL ReportGitHubCheck lock tests. The real completion election admits only one reporter.
- C pkg/controller: port TestGitHubAppStaleUpdateNeverReopensAFinishedRun and TestGitHubAppDelayedQueuedUpdateLeavesARunningCheck to generated check/status phase cases and a recorder test for a delayed pending update.
- B/C pkg/controller: port TestGitHubAppStaleQueuedUpdateKeepsATimedOutConclusion to TestGitHubCompletionReaperPreservesTimeoutConclusion. The worker derives timed_out from committed timeout reasons instead of accepting a caller-selected outcome.
- C pkg/controller: port TestGitHubAppAdoptsACheckRunWhoseRecordWasLost to TestGitHubCompletionAdoptsUnrecordedCreate. A SQLite trigger refuses the check-ID write after GitHub accepted the create. Retry adopts that external ID and acknowledges only after the ID persists.
- B pkg/store: TestGitHubCommitStatusModeSurvivesReopen and TestGitHubCommitStatusModeIsScopedProperty preserve fallback state independently of check ID, reject foreign tenants and generated nonexistent IDs, and run through the suite-selected store dialect.
- C internal/githubapp/githubapptest and pkg/controller/onlyCheckTokens: support restricted numeric repository metadata lookup in the fake and require repository_ids [701] with checks:write alone. The old name-based token expectation is removed.
- C pkg/controller: port TestGitHubAppCheckRunSurvivesGitHubErrors to TestGitHubCompletionRetriesAfterControllerReplacement. Two 502s retain due work, obey the store-clock backoff and log each failed delivery. Neither changes the successful run. A replacement controller delivers the check on retry. The old immediate retry-and-drop expectation is retired by frozen v4 section 3.9.
- B pkg/controller: TestGitHubCompletionUnverifiedRunReportsNothing acknowledges non-App work without calling GitHub. TestGitHubCompletionMissingAppKeepsTerminalDue leaves verified work due while profile delivery continues, then delivers after the App configuration is restored.


### Continuation 5 verification

All Go checks use GOWORK=off, GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-gocache, GOMAXPROCS=4 and
TMPDIR/GOTMPDIR=/tmp/sw-astra-W1b. Normal tests use -p 4 and race tests -p 2.

- Whole-repository build passes. Vet passes for controller, store, GitHub client/fake, browserfixture, both CLI packages, localcontroller and acceptance. Commentcheck is clean across 2,672 files. The docs source is updated and its mirror regenerated with bin/sync-docs.sh. Both --check and TestPkgDocsContentMatchesDocsRoot pass.
- The focused completion, sealed GitHub identity and report-lock selection passes in controller (8.850s) and store (1.272s). The initial race selection passes in controller (41.914s) and store (11.133s). The expanded race selection covering status-mode persistence, generated scoping, both real reaper timeout paths, unverified runs and missing App configuration passes in controller (4.113s) and store (5.617s). The final two-failure retry test passes (0.238s).
- Local service/composition and version-schema selections pass in localcontroller (1.298s) and cmd/sparkwing (0.046s). These are recorder/component proofs, not native socket startup.
- Full acceptance passes in 226.600s, with 406 top-level tests, 5,386 passing tests/cells and 2,991 pre-existing skips. No skip is added or widened. No native matrix skeleton is newly unskipped.
- TCP and Unix bind probes both return Operation not permitted. PostgreSQL is not attempted. The named remaining transport checks and PostgreSQL obligations are in test-reconciliation-sockets.md. The new controller recorder fixtures use the existing SQLite engine fixture. The new store fallback tests and existing report-lock tests use the selected store dialect and still need the PostgreSQL lane.

### Continuation 5 contracts, budget and open work

Contract changes made: C1 records DueGitHubChecks, the store-clock ObservedAt,
committed terminal Reason, durable status fallback, the version-5 baseline and
the shared ReconcileRunEffects owner. C2 records sealed numeric GitHub delivery
and the removal of the queue Shutdown API. No lead-owned handoff or amendment
file is edited. No environment variable, compatibility path or timer is added.

Assumptions/mechanisms: one boolean moves existing per-run fallback state from
memory into the store because otherwise a restart after permission acceptance
leaves the old pending commit status unresolved. The existing report lock
serializes its use. Terminal timeout reasons come from the exact committed
state event. The existing worker and outbox own retries. Reverting this slice
means restoring the queue and its callers, not retaining a second delivery
owner. The warning-once map retains only diagnostic state.

Production delta against session HEAD 46f407598 is +286/-424, net -138 Go lines,
including the moved completion worker and the shared GitHub test fake under the
lead's counting rules. The retired queue drain test/helper file loses 138
lines. Seven native report groups are ported to recorder tests and removed
from github_checks_test.go, whose net size drops from 808 to 567 lines. The
replacement property and integration tests retain tenant, masking, source,
phase, retry and billing assertions. No financial or authorization guard is
weakened.

The W1+W1b store area is -2,685 production Go lines against main, versus the
-4,250 budget, or 1,565 over. This continuation adds 43 store lines for durable
fallback, committed terminal reason and store-clock retry observations, while
removing 138 production lines across the repository. This work is within the
completion reconciler already sized in frozen v4 section 3.9, but does not
close the inherited store overage.

Remaining real integration: eleven enabled cron failures still need the
committed arming-origin and consent transaction. Verified App intake still
refuses in Server.ResolveIntake, and the old webhook launch path still calls
retired intake. The new tests seed sealed App source facts, then drive real
owner finish or reaper transitions, the real outbox and the real provider
adapter with a recorder transport. They do not prove HMAC webhook-to-intake
composition. The remaining native GitHub run lifecycle, permission fallback,
summary and re-request fixtures still need their retired runner HTTP calls
ported. Keep their source, fork, binding, replay and run-budget assertions.
Metrics and parent-notification consumers remain unfinished. No acceptance or
PostgreSQL completion claim is made for these gaps.

Shared files and caller migrations touched in this continuation:
- cmd/sparkwing-controller/github_app_test.go
- cmd/sparkwing/local_services.go
- design/rewrite/contracts/c1-data.md
- design/rewrite/contracts/c2-credentials.md
- design/rewrite/test-reconciliation-sockets.md
- design/rewrite/test-reconciliation.md
- docs/github-app.md
- internal/githubapp/githubapp.go
- internal/githubapp/githubapptest/githubapptest.go
- internal/web/browserfixture/main.go
- pkg/controller/browser_pages_test.go
- pkg/controller/browser_session_test.go
- pkg/controller/completion.go
- pkg/controller/export_test.go
- pkg/controller/finish_run_followups_test.go
- pkg/controller/github_app.go
- pkg/controller/github_app_test.go
- pkg/controller/github_app_webhook.go
- pkg/controller/github_checks.go
- pkg/controller/github_checks_test.go
- pkg/controller/github_completion_test.go
- pkg/controller/github_runners_test.go
- pkg/controller/identity_fixture_test.go
- pkg/controller/intake_source_test.go
- pkg/controller/profile_completion.go
- pkg/controller/profile_completion_test.go
- pkg/controller/server.go
- pkg/controller/team_boundary_test.go
- pkg/docs/mirror/github-app.md
- pkg/store/completion.go
- pkg/store/github_app.go
- pkg/store/github_status_mode_test.go
- pkg/store/schema.sql
- pkg/store/store.go

Friction: native sockets remain forbidden, so no PostgreSQL connection, native
HTTP/CLI gate or Sparkwing candidate execution was attempted. The missing
schedule authority and verified-App intake owners are real integration work,
not retired assertions. No Git writes are performed. Local Git has no identity
or core.worktree override. W1b scratch is removed after all checks, and the
shared build cache is retained.


## Continuation 6: reporting fixture reconciliation

- C pkg/controller: port TestGitHubAppRunReportsOneCheckRun to the current owner transition and elected completion path with recorder transport. Retain queued/running/terminal phases, exact provider create/update identity, persisted check ID, commit, pipeline, aggregate counts, authenticated link, checks-only numeric repository token and zero commit statuses. Require all three exact revisions to be acknowledged.
- C pkg/controller: merge TestGitHubAppCheckRunConclusions into TestGitHubCompletionFromRealTerminalTransition. Pin success/failure/cancelled with an independent literal expectation table instead of calling the production conclusion mapper in the assertion. Real finish and repeated delivery remain.
- C pkg/controller: port TestGitHubAppWithoutChecksPermissionPostsStatuses. Retain pending/success on the first commit, pending on the second, one warning per installation, numeric repository restriction and only the required write permission. A new run after permission acceptance produces a queued check without changing prior statuses. The existing replacement test retains durable per-run fallback.
- C pkg/controller: port TestGitHubAppCheckRunSummaryIsBounded. Four hundred long private job names/errors and a 100,000-character run error reach the current reporting adapter. The fixture declares every added job in its accepted topology. UTF-8, GitHub's character bound, all 401 failed jobs and the authenticated link remain asserted.
- C pkg/controller: port TestGitHubAppCheckRunSummaryKeepsPrivateRunDataInConsole. Retain exclusion of private job names, job errors and run errors with exact aggregate count and link checks. TestGitHubCompletionSummaryPrivacyProperty generates adversarial private text and checks the whole outgoing check, including a run error verified present after finish. Previously finish cleared that error before the property exercised it. Invariant: GitHub receives aggregate state and the authenticated link, never arbitrary private job/run text.
- A pkg/controller: delete claimedRun, startRun, runnerCall, finishRun, addNode and onlyCheckTokens from github_checks_test.go. They called retired trigger-claim/run-creation surfaces or have no callers after these ports. The file loses 258 lines. No owed test group is dropped.
- B OPEN pkg/controller: the focused cron census still reproduces all eleven enabled failures. Three use retired trigger SQL and eight lack the concrete ScheduleWriter. No expectation or security assertion changes. Internal/crons passes in full.

The reporting fixture seeds the verified App request, initial pending row and
queued revision. The running and terminal edges are real store transitions;
terminal finish traverses the real private HTTP host adapter. The other two
permission-check runs are pending store fixtures. The missing signed webhook
intake composition is still open. No socket-dependent test is marked skipped,
and no native acceptance cell is newly claimed complete.

- A pkg/controller: delete TestController_ListPausesAlias (26 lines). Frozen v4 principle 3 and decisions 94/97 remove pre-launch aliases. TestBusinessRetiredRoutesAndExactMethods now requires /runs/{run}/paused to be unregistered and return 404 for every tested method. Canonical debug-pause tests remain.
- A pkg/controller: delete TestCreateNode_LiveRunStillAcceptsNodes and its unused postNode helper/reapReason. Arbitrary insertion through /runs/{run}/nodes is retired. The existing plan/expansion transaction suites own topology insertion. TestBusinessRetiredRoutesAndExactMethods asserts that the removed collection stays unregistered.
- C pkg/controller: replace TestCreateNode_StrangerTokenLearnsNothingAboutTheRun with TestPlanTopologyStrangerLearnsNothingProperty at /runs/{run}/plan. Generated private errors, running/failed/missing runs and machine grants including node.result all retain refusal, zero private-field/bearer disclosure, an unchanged full run snapshot and zero inserted jobs/events/effects. Invariant: a claimant without the current plan award cannot insert topology or learn private run state. reaped_run_conflict_test.go is deleted (124 lines), replaced by the smaller property file.


### Continuation 6 verification and remaining work

All successful Go checks use GOWORK=off, GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-gocache, GOMAXPROCS=4 and TMPDIR/GOTMPDIR under
/tmp/sw-astra-W1b. Normal tests use -p 4 and race tests use -p 2.

- Whole-repository build passes. Final vet passes for controller and acceptance. Final commentcheck against main is clean across 2,673 files. git diff --check passes.
- The reporting/completion selection passes in 10.423s. Its race selection passes in 52.496s. The topology-authority property and retired-route checks pass normally and with the race detector. The original full internal/crons package passes in 3.777s.
- The broader controller selection passes in 55.173s: 482 top-level tests and 895 passing tests/subtests. The command selects tests outside the named socket inventory and reproduced cron failures. It runs before the final topology-property/retired-route edits, which pass separately afterward. This is not a full controller-suite claim. No source skip is added or widened.
- Full acceptance passes in 202.401s: 406 top-level tests, 5,386 passing tests/cells and 2,991 pre-existing skips. No native matrix cell is newly claimed complete.
- A temporary Go source overlay makes githubCheckSummary append the private run error. The generated privacy property fails on its first input. The normal source passes. The overlay never modifies production files and is deleted with scratch.
- Fresh TCP and Unix bind probes both return Operation not permitted. PostgreSQL is not attempted. The remaining named native transport checks and PostgreSQL controller/acceptance lanes need the lead's environment. The new tests use the existing SQLite controller fixture. They do not establish a PostgreSQL result.

Production delta against session HEAD fef4c812f is zero. No production file,
contract, schema, credential, setting or provider interface changes. No new
production mechanism is added. The stale test/helper deletions total 408
lines; replacement tests and fixture changes give net -110 Go lines.
The W1+W1b store area stays at -2,685 production Go lines against main under
the lead's budget exclusions, 1,565 over its -4,250 budget. The inherited
overage remains open. No area-complete claim is made.

Contract changes made: none. Contract changes still needed are the previously
recorded schedule arming-origin/consent join and verified App intake composition.
Eleven cron failures remain enabled. Metrics and parent-notification consumers
remain unfinished. Signed-webhook intake, re-request, repository subscription,
fork, binding, replay and run-budget fixtures still need native reconciliation.
The source and security assertions in those tests are retained.

Shared files touched in this continuation:
- design/rewrite/test-reconciliation.md
- design/rewrite/test-reconciliation-sockets.md
- pkg/controller/github_checks_test.go
- pkg/controller/github_completion_test.go
- pkg/controller/github_reporting_test.go
- pkg/controller/reaped_run_conflict_test.go (deleted)
- pkg/controller/run_control_routes_test.go
- pkg/controller/server_test.go
- pkg/controller/topology_authority_test.go

Friction: forbidden sockets prevent native transport and PostgreSQL proofs.
The missing committing schedule origin and verified App intake are real
implementation obligations. This continuation uses fixture seeds only at those
external seams and does not count them as production acceptance. No lead-owned
amendment or handoff file is edited. No Git writes or Sparkwing candidate
execution is performed. Local Git has no identity or core.worktree override.
W1b scratch is removed after checks and the shared cache is retained.


## Continuation 7: public read reconciliation

- B OPEN pkg/controller: reproduce all eleven cron failures before edits. Three still call retired trigger SQL and eight still lack the committing ScheduleWriter. No cron assertion changes. The scheduled origin must retain the arming authority and revalidate it inside the sole sealed intake transaction.
- C pkg/controller: port TestController_Health, TestController_HealthHidesDatabaseErrors and TestController_HealthDoesNotExposeRunFailures to the real public handler through a recorder. Retain status, object-store health, sanitized database failure and zero workload-history disclosure. TestControllerPublicHealthHidesWorkloadProperty generates private pipeline/error/argument values and running/failed states. Invariant: unauthenticated health reveals deployment availability without private workload identity or content.
- C pkg/controller: port TestTrends_BucketsRuns, TestTrends_AvgWaitMs, TestTrends_AvgWaitMs_ExcludesLegacyRows and TestTrends_PipelineFilter to real runs.read authentication through the recorder. Preserve exact run counts, wait averages, missing-created-at exclusion and pipeline filtering. Decode failures now fail loudly. Delete the unauthenticated httpGet helper.
- C/B OPEN pkg/controller: port both TestController_ConcurrencyState queue-arrival groups to /queue/groups/{group}, real runs.read authentication and a fake implementing the declared ConcurrencyStore read seam, backed by a seeded tenant snapshot. Preserve omitted versus exact nanosecond arrival and verify the forwarded tenant, group and viewer authority. The concrete Tenant still does not implement ConcurrencyStore. Its committing adapter remains a real integration obligation, not a passing native proof.
- C pkg/controller: TestController_GetRun_IncludeNodes becomes TestController_GetRunIncludesJobs and TestController_GetRun_IncludeNodes_NoSnapshot becomes TestController_GetRunBeforePlanning. Seed reporting state without retired public run/node writers. Assert the shared top-level RunDetail, jobs, stable response under legacy include queries, no raw run/node wrapper and populated empty collections before planning.
- A pkg/controller: delete TestController_GetRun_IncludeNodes_Decorations. It required raw plan execution declarations (work, modifiers, groups) in the public status response. C3 defines JobDetail as a safe status projection and RunDetail as the shared display boundary. Existing engine detail/classification properties retain private-input exclusion. No public raw-declaration alias is restored.
- A pkg/controller: delete TestController_RunLifecycle and TestController_ValidationErrors. They assert client-selected run IDs/state, arbitrary node insertion, the old nodes/start/finish routes and the old mutable plan snapshot. Their missing-id refusal is invalid under server-allocated run IDs. Current intake, plan, job/run transition, event, dependency and replay obligations remain in TestEngineIntakeHTTP, TestEnginePlanHTTP, TestEngineJobLifecycleRenewsAndReplaysUnderStoreFences and TestEngineOwnerReconcilesExecutesAndFinishes.
- C pkg/controller: port TestRoutes_RejectSimpleRequestContentTypes to authenticated token creation, own-token rotation and secret creation. Reject missing/plain/form/multipart types, preserve authentication-before-decode, and assert no capability issuance/revocation or secret write. Logout no longer accepts a caller-selected session JSON body. Its current body/CSRF/refusal behavior remains in TestSessionFlowHTTP* and TestBrowserLogoutHTTP*.
- C pkg/controller: port TestRoutes_RejectUnknownJSONFields to the current grants array and canonical bad_request envelope, with no credential mutation and a real successful JSON issuance as control. TestRoutesUnknownJSONFieldsProperty generates adversarial field names/values and asserts zero mutation or private-value/bearer disclosure. Invariant: an unknown credential field cannot create, rotate or expose credentials.
- C pkg/controller: port both object-store public health groups and the three object-store/bucket metric export groups to recorders. Metrics use the existing operator.limits policy fixture. Preserve all numeric metric values and public-health exclusion of configured limits and stored totals. Delete unused scrape and public server helpers plus mustPost, mustPostJSON, mustPostRaw and postAs.
- C pkg/controller: port TestSecrets_ListOpensAVariablesEnvelope to a real account session, CSRF, bound cipher and committing writer. Assert both secret and variable are encrypted and bound at rest, the variable opens on list, and the secret stays absent.
- C pkg/controller: port TestSecrets_EveryResponseIsNoStore and TestAuthenticatedResponsesAreNoStoreAndPublicOnesAreNot to real session/CSRF transport and current /tokens, RunDetail and /jobs/{job} routes. Pin success, masked-read refusal and absent-delete statuses while retaining no-store/no-cache and secret exclusion. Public health returns 200 and JWKS returns its documented 404 without a signing key. Neither carries the private cache header.
- C pkg/controller: port TestSecrets_DashboardSessionsWriteSecretsAndReadVariables to real owner/editor/reader account sessions and CSRF through recorders. Preserve masked-list and direct-read secrecy, plaintext variable reads, scoped metadata, owner deletion, editor/reader write/delete refusal and unchanged rows after refusals. Match canonical forbidden instead of the retired write_only text.
- A/C pkg/controller: replace TestSecrets_MaskedValuesReachOnlyTheOperatorBearer with TestSecrets_MaskedValuesStayWriteOnlyForMembersAndDeploymentCredentials. C2's exact secret-route alternatives grant tenant members and live declared claims, not a deployment operator bypass. Preserve owner-bearer writes, masked-read refusal and variable reads. Require deployment credentials to receive 403 on reads and writes with no plaintext or inserted row. TestSecretsDashboardMaskedValuesProperty generates masked values across owner/editor/reader sessions and shared/pipeline scope. Invariant: masked values appear in neither member lists, member reads nor request logs.
- A pkg/controller: extend TestBusinessRetiredRoutesAndExactMethods to require the old nodes/{job}/start, nodes/{job}/finish and concurrency/{group}/state routes to remain unregistered. The current plan and state-transition boundaries retain their authorization tests.
- C pkg/controller: port TestSecrets_ReadRefusesEnvelopesSealedBeforeTeamBinding and TestSecrets_PlaintextRowFailsClosedOnceEncrypted to authenticated recorder reads through the real credential reader. Preserve 500, zero plaintext or storage-state disclosure and unchanged stored bytes. The stable private error is the current canonical internal/request failed envelope, replacing the retired string-only error.
- C pkg/controller: port TestSecrets_ListNamesUnboundRows to the real session, CSRF and bound writer. Preserve both exact bound flags, reject missing/duplicate/unexpected rows and exclude both plaintext values from the entire list response.
- C pkg/controller: TestSecretsMalformedEnvelopeReadsProperty generates plaintext and pre-binding encrypted bytes. Every read retains the exact stored bytes, returns canonical 500 and excludes the generated plaintext from response and request logs. Invariant: a credential read never releases or rewrites an envelope lacking its current tenant/name/pipeline/access binding.


### Continuation 7 verification and remaining work

All Go checks use GOWORK=off, GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-gocache, GOMAXPROCS=4 and TMPDIR/GOTMPDIR under
/tmp/sw-astra-W1b. Normal tests use -p 4 and race tests use -p 2.

- Whole-repository build passes. Final vet passes for controller and acceptance. Final commentcheck against main is clean across 2,674 files. git diff --check passes.
- The initial focused public-read selection passes in 1.121s. The expanded read/credential/object-store selection passes in 1.225s. Final dashboard-secret selection passes in 0.880s. The legacy-envelope/plaintext/list selection passes in 0.166s.
- The changed read/credential/secret/object-store groups pass under the race detector in 14.582s. The subsequently ported malformed-envelope/list groups and their generated property pass under the race detector in 3.058s.
- The final broader controller selection passes in 61.842s: 544 top-level tests/fuzz-seed groups and 1,149 passing tests/subtests, with zero skips in that selection. It excludes the named socket inventory and the eleven reproduced cron failures. This is not a full controller-suite claim. No source skip is added or widened.
- Full acceptance passes in 201.436s: 406 top-level tests, 5,386 passing tests/cells and 2,991 pre-existing skips. No native matrix cell is newly claimed complete.
- A temporary source overlay removes DisallowUnknownFields from the engine request decoder. TestRoutesUnknownJSONFieldsProperty fails on its first generated input. The final unmodified source passes. The overlay never changes production files and is removed with scratch.
- Fresh TCP and Unix bind probes both return Operation not permitted. PostgreSQL is not attempted. The named native controller/CLI/web/bin checks and controller/acceptance PostgreSQL lanes still need the lead's environment. Recorder tests here use SQLite.

Three retired test groups lose 160 lines. Seven dead HTTP/server helpers lose
87 lines. Across Go tests this continuation adds 500 lines and removes 750,
net -250. Production delta against session HEAD c69141102 is zero.
No production API, schema, credential policy, provider interface, setting or
mechanism is added or changed. The W1+W1b store area remains -2,685 production
Go lines against main under the lead's budget exclusions, 1,565 over its -4,250
budget. That inherited overage remains open.

Contract changes made: none. The handoff audit found the older M4 renewal rule
in unit_renewal.go, decision 112's single rung/tier table in billing_policy.go,
non-checkout settlement in payment_provider_settlement.go and the committed
materialization owner in blob_materialize.go. Those completed items are not
reimplemented. Lead-owned amendments and handoffs are unchanged.

Remaining production work: the committing ScheduleWriter and retained arming
origin/consent through sealed intake, verified App intake composition, the
concrete ConcurrencyStore adapter, and metrics/notify_parent outbox consumers.
All eleven cron failures remain enabled. Signed-webhook intake, re-request,
subscription/fork/binding/replay/run-budget fixtures and the three remaining
encryption roundtrip/relocation/access-field groups still need reconciliation
at their current declared-claim boundary. Their security assertions remain.
No security, tenant-boundary, masking or billing assertion is relaxed to pass.

Shared files touched in this continuation:
- design/rewrite/test-reconciliation.md
- design/rewrite/test-reconciliation-sockets.md
- pkg/controller/content_type_test.go
- pkg/controller/objectstore_test.go
- pkg/controller/prom_test.go
- pkg/controller/run_control_routes_test.go
- pkg/controller/secrets_dashboard_test.go
- pkg/controller/secrets_encryption_test.go
- pkg/controller/server_test.go
- pkg/controller/trends_test.go

Friction: forbidden sockets prevent native transport and PostgreSQL proofs.
The missing committing schedule and concurrency adapters are implementation
obligations. Only queue rendering uses the explicit fake read seam, as listed
above. No Git writes or Sparkwing candidate execution occur. Local Git has no
user identity or core.worktree override. W1b scratch is removed after checks,
and the shared build cache is retained.


## Continuation 8: envelope release and argument privacy

- C pkg/controller: port TestSecrets_EncryptionRoundTrip to real owner-session writes and real launcher-issued job claims over recorders. Both variables and masked secrets stay encrypted and bound at rest, declaring claims receive exact plaintext with no-store, and member reads still refuse masked values. Accepted run/spec facts are fixture seeds. Claim issuance, authentication, declared-secret selection, transactional release and decryption are real.
- C pkg/controller: port TestSecrets_EnvelopeIsBoundToNameAndPipeline and TestSecrets_EnvelopeIsBoundToAccessFields to authorized declaring claims. Relocated name, pipeline, unscoped fallback, shared and masked fields return sanitized 500 without plaintext or changed stored bytes. Each case also reseals to its selected metadata and requires a successful plaintext read, so policy refusal cannot hide a binding defect.
- C pkg/controller: TestSecretsEnvelopeBindingProperty generates private values across all five envelope binding fields plus unscoped fallback, including cross-tenant transplantation. Invariant: changing a stored envelope's tenant, name, pipeline, shared flag or masked flag never releases its plaintext.
- C pkg/controller: port all seven TestSecretArgs_* groups to real sealed intake, current member sessions and recorder reads. List/detail/latest/attempts retain the classified public argument and redact private values. Receipts expose neither raw invocation nor private input hashes under the current shared receipt projection. A legacy writer's hash-bearing body still returns bad_request without any admitted run. Retry uses explicit mode/idempotency and keeps plaintext plus classification in both private persisted requests, with redacted public detail.
- C pkg/controller: TestSecretArgsPublicReadsProperty generates private argument values and known/unknown classifications across list/detail/latest/attempts/receipt, with and without include=secret_values. Invariant: public reads never disclose private arguments or their input-hash oracle, widen execution authority, or rewrite private execution inputs. Unknown classification masks every argument.
- C pkg/controller: port TestRunFiltersTreatSQLMetacharactersAsLiteral and TestSecretMutationsTreatSQLMetacharactersAsLiteral to authenticated recorder transport through real per-tenant storage. Current status enums replace impossible injected status rows. Malicious status queries still select zero rows and a valid status selects exactly the tenant's rows. Preserve exact pipeline/branch/repository matching and count, exact replacement/deletion, absent-delete refusal, and all foreign-tenant rows. Invalid quoted secret names are refused on creation. Existing quoted rows still delete exactly, and valid-name SQL pipeline selectors still replace exactly. PostgreSQL lane remains enabled for native execution.
- C pkg/controller: TestSecretMutationsInvalidNamesCannotChangeRowsProperty and TestRunFilterTenantLiteralsProperty generate SQL metacharacter inputs. Invariant: credential mutation and run selection treat input as data and cannot change or select another tenant's rows.
- A/C pkg/controller: replace TestUpdatePlanSnapshot_BodyLimit and its obsolete postPlanSnapshot helper with TestPlanSubmissionBodyLimitPreservesCommittedPlan. A real launcher-issued plan claim and elected controller owner accept a valid submission exactly at the current request-body limit. A valid JSON body one byte over the limit returns bad_request while the claim is live. After acceptance the spent claim is refused as unauthenticated before decoding. Both refusals preserve the entire persisted snapshot. Arbitrary raw snapshot upserts are not restored.
- A pkg/controller: delete TestSourceCredential_RemovedRoutesAnswer404. Its global pre-router claim-token 403 rule belongs to the retired credential surface. Extend TestBusinessRetiredRoutesAndExactMethods instead: source-token and git-credential aliases stay unregistered and return 404 under every tested method. Current release and repository-ID authority remain covered by TestSourceControllerUsesStoreAuditAndBoundCipher and the enabled source credential integration groups.


### Continuation 8 verification and remaining work

All Go checks use GOWORK=off, GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-gocache, GOMAXPROCS=4 and TMPDIR/GOTMPDIR under
/tmp/sw-astra-W1b. Normal tests use -p 4 and race checks use -p 2.

- Whole-repository build passes. Final vet passes for controller and acceptance. Final commentcheck against main is clean across 2,674 files. git diff --check passes.
- The full TestSecrets selection passes in 3.397s. Final TestSecretArgs selection passes in 1.252s, including the current JSON RunPage and the default list representation. The four SQL literal/property SQLite groups pass in 0.406s.
- The changed secret/envelope/argument groups pass under the race detector in 45.788s. The four SQL SQLite groups pass under the race detector in 6.584s. The subsequently ported plan-size/retired-route selection passes normally and under the race detector (3.097s).
- The broader controller selection passes in 75.654s: 556 top-level tests/fuzz-seed groups, 1,172 passing tests/subtests and zero skips. It excludes the socket inventory, eleven reproduced cron failures and the four separately exercised SQL dialect groups. That selection precedes the final plan-size and source-alias edits, which pass separately afterward. This is not a full controller-suite claim.
- Full acceptance passes in 214.626s: 406 top-level tests, 5,386 passing tests/cells and 2,991 pre-existing skips. No native matrix cell is newly claimed complete.
- Two temporary Go overlays prove the generated privacy checks can go red. Binding secret decryption to constant original metadata makes the shared-field property release plaintext and fail on its first input. Returning unredacted requested arguments makes the public-read property fail on its first input. Neither overlay changes repository source.
- A fresh cron selection reproduces all eleven enabled failures: three tick cases still reach retired trigger SQL and eight schedule HTTP cases still need the committing ScheduleWriter. No cron assertion changes.
- Fresh TCP and Unix socket-creation probes both return Operation not permitted. PostgreSQL is not attempted. The native controller/CLI/web/bin checks in test-reconciliation-sockets.md and the controller/acceptance PostgreSQL lanes remain for the lead. The four SQL groups retain their explicit postgres subtests. No test skip call is added or widened.

The stale source-alias test group loses 23 lines, including its obsolete
pre-router claim-token comment. Eight obsolete listener/body/fixture helpers
lose 106 lines. Replacement tests and generated properties give net -1 Go
line for this continuation (418 added, 419 removed). Production delta against
session HEAD 7e4355b81 is zero. No production API, schema, credential policy,
provider interface, setting or mechanism changes. No docs impact because the
change reconciles tests without changing product behavior.

The W1+W1b store area remains -2,685 production Go lines against main under
the lead's budget exclusions, 1,565 over its -4,250 budget. The inherited
overage remains open. This test-only continuation neither adds to nor closes it.

Contract changes made: none. The handoff/amendment audit confirms the earlier
M4 renewal, decision 112 policy table, provider settlement, materialization,
store-harness relocation and legacy catalog/helper cuts remain implemented.
Those completed assignments are not rebuilt. Schedule origin/consent through
sealed intake, verified App intake composition, the concrete ConcurrencyStore
adapter, metrics and notify_parent consumers remain production integration
obligations. Signed-webhook intake, re-request, subscription/fork/binding/replay
and run-budget fixtures still need reconciliation and native verification.
Security, tenant-boundary, masking and billing assertions stay enabled.

Shared files touched in this continuation:
- design/rewrite/test-reconciliation.md
- design/rewrite/test-reconciliation-sockets.md
- pkg/controller/plan_snapshot_limit_test.go
- pkg/controller/run_control_routes_test.go
- pkg/controller/secret_args_display_test.go
- pkg/controller/secrets_encryption_test.go
- pkg/controller/source_credential_test.go
- pkg/controller/sql_literals_test.go

Friction: forbidden sockets prevent native transport and PostgreSQL proofs.
The new encryption fixture seeds accepted run/spec facts but uses real claim
issuance and release transactions. Argument intake uses the existing fake
external intake-policy seam with real persistence and authentication. No
production acceptance is claimed for those fixture seams. Lead-owned handoff
and amendment files remain unchanged. No Git writes or Sparkwing candidate
execution occurs. Local Git has no identity or core.worktree override. W1b
scratch is removed after checks, and the shared build cache is retained.

- B pkg/controller/pkg/store TestGitHubAppConnect* and TestGitHubAppExisting*: implement the declared GitHubConnectWriter on Tenant. Identity reads, one-use nonces and binding recheck a live direct owner session and exact account/team in the committing transaction. Linked-provider changes and foreign installation ownership still refuse. TestGitHubConnectionRequiresSameLiveOwnerSessionProperty generates authorization changes; nonce concurrency and exact store-clock expiry pass.
- A pkg/store GitHub connection APIs: remove BindGitHubAppInstallation and ConsumeGitHubAppConnectState. Fixture callers use TestOnlySeedGitHubInstallation and the existing private nonce helper. Extract one binding statement and store/read installation timestamps as nanoseconds. Controller compile-time conformance prevents missing production composition.
- C pkg/controller App/identity fixture: supply srv in the embedded identity fixture and present session API calls with the actual session/CSRF cookies, matching header, same Origin and fresh Idempotency-Key. Browser verification stays real. The shared fixture change is needed for App invitation/administration paths.
- C pkg/controller TestGitHubOnboardingCookieSessionUsesTeamRole: migrate runner enrollment/list/delete to /api/v1/runners/github. Reader/editor refusals and owner success pass through real authentication and transactions.
- C pkg/controller TestGitHubOnboardingCookieWritesRejectInvalidCSRF: use the current runner routes. Missing, forged, foreign-origin and planted-CSRF requests all refuse without creating consent or enrollment.
- C pkg/controller TestGitHubRunnerOnboardingResolvesCoveredIdentity and TestGitHubRunnerOnboardingRefusesUnverifiedIdentity: use current enrollment routes. Coverage, forged numeric identity, owner transfer, missing owner, truncated listing and unavailable provider assertions remain and pass.
- B/C pkg/controller TestGitHubRunnerCredentialsPerTeamAreBounded: configure the fixture cipher and server, use the current enrollment route and expect Created for a new credential. Map ErrGitHubRunnerCredentialLimit to the shared RateLimited envelope. Exactly twenty live credentials still succeed and the twenty-first returns 429.
- C pkg/controller TestGitHubAppCronRenameKeepsIdentityAndPauseAtRepoCap: use the frozen schedule list form. Rename keeps repository identity, row ID and pause at the cap.
- C pkg/controller TestGitHubAppCronLifecycleWithdrawsOldBindings: the schedule list fixture restores all nine lifecycle cases, including uninstall, unbind, suspension, repository withdrawal and transfer.
- C pkg/controller TestGitHubAppCronTransferWithdrawsFormerTeamBeforeNewArm: the schedule list fixture preserves former-team withdrawal before the new binding arms.
- C pkg/controller TestGitHubAppWithdrawnRepoReleasesCapacity: the schedule list fixture preserves capacity release and replacement admission.
- C pkg/controller TestGitHubAppLateRemovalKeepsARepositoryTheCutListingShows: the schedule list fixture preserves the still-listed repository.
- C pkg/controller TestGitHubAppDefaultPushArmsAndWithdrawsControllerSchedules: the schedule list fixture preserves tenant scope, repository-limited contents:read tokens, replay and declaration withdrawal.
- C pkg/controller TestGitHubAppCronPushGuardsPreserveExistingSchedules: the schedule list fixture preserves every stale/foreign/malformed/provider-failure assertion.
- C pkg/controller TestGitHubAppLargeInstallationKeepsSchedulesPastTheListingCap: use the schedule list fixture and expect the current shared Unavailable status 503 for failed subscription coverage. Existing installation-event 502s, bounded-list refusal and already-armed schedule preservation still pass.
- B cmd/sparkwing TestRunnerRemovalRefusesPublicCredentialBeforeStoppingOrRevoking: read the enrollment-owned credential with OpenPrivateConfig. The mounted-credential reader permits world-readable files and was the wrong contract here. TestRunnerRemovalUnsafeModesCannotStopOrRevokeProperty generates group/other permission bits. Every unsafe mode refuses before service or revocation effects. Both groups pass under race. Mounted-credential policy is unchanged.
- C cmd/sparkwing-controller TestConfigureSecrets_SingleTeamSQLiteCreatesItsKeyOnTheFirstSecret and TestConfigureSecrets_SingleTeamSQLiteRefusesASecondKeyForSealedRows: issue real owner member authority and use recorder transport. Lazy key creation, 0600, bound envelopes, restart and missing-key refusal remain and pass.
- C internal/web TestRenderJSONL_*: fixtures use LogRecord job instead of the retired node JSON field. Keep the existing node_start event spelling. Banner, text, JSONL conversion and ANSI assertions pass.
- A internal/wingd TestIdleExitRegressionsRunInParallel: remove the inventory entry for retired TestIdleExit_WaitsForWorkingConnections. Current connection lifecycle coverage lives in controlled-clock tests. The other seven parallelism entries remain.
- C cmd/sparkwing TestPipelineToolchainOverrideStaysInTheBuild: fake Go accepts the current dependency preparation mod command. Child-only toolchain override, retained process pin and probe/build counts remain and pass.
- C cmd/sparkwing TestTriggerCommandGuardsTheSelectedRevision, TestTriggerSelectedConfigurationIgnoresNestedAndDirtyFiles, TestTriggerSelectedConfigurationRefusesMalformedProject and TestRunHelpSelectsExecutionRefAndConfiguredSource: fake Git consumes the current hardened -c arguments. Profile pin, selected revision, dirty/nested isolation, malformed YAML and selected-source cleanup assertions pass.
- C cmd/sparkwing TestScheduleDiscoverySupportsAuthenticatedProxy: fake Git consumes hardened -c arguments. Authenticated proxy discovery and zero plaintext in error/stderr remain and pass.
- C cmd/sparkwing TestReleaseWorkflowUsesTheRunnerImageContract: include util-linux in the existing image dependency expectation. All digest, binary, release toolchain and immutable publication assertions remain.
- C cmd/sparkwing TestUnifiedSDKCheckReadsPinWithoutGoOrModuleWrites: a local SDK replacement selects the installed development engine under decisions 102/103. Preserve no-Go-invocation and no-module-write assertions.
- C cmd/sparkwing TestXrepoListDescribesEachRepositoryOnce: embed the marker path into the fixture program because Plan removes SPARKWING_TEST_* environment controls. Both repositories still describe exactly once.
- C cmd/sparkwing TestXrepoReportsFlagErrors: expect current shared parser wording unknown flag --bogus, retaining nonzero exit, empty stdout and flag identity.
- C cmd/sparkwing TestAuthGuideCLIExamplesUseFinalGrammar: parse executable shell-fence examples. Inline command names remain covered by docs honesty registration checks and do not require a full runnable set of flags.
- C OPEN cmd/sparkwing TestRun_PinnedPipelineRunsWithTheNetworkDenied: replace retired SPARKWING_LOG_FORMAT=quiet with plain. Native subprocess socket/network verification remains open.
- B OPEN W1 reconciliation-a pkg/controller TestRunSourceCredential_MintsTheApprovedRepositoriesByID: claim_source_test.go:52: SQL logic error: table repos has no column named repo (1). Reproduced through temporary net.Pipe HTTP transport; native proof remains with the lead.
- B OPEN W1 reconciliation-a pkg/controller TestRunSourceCredential_NeedsTheGitHubApp: claim_source_test.go:67: SQL logic error: table repos has no column named repo (1). Reproduced through temporary net.Pipe HTTP transport; native proof remains with the lead.
- B OPEN W1 reconciliation-a pkg/controller TestGitHubAppAutomationRevokedDuringDiscoveryStartsNoRun: github_app_automation_test.go:71: revoked discovery dispatched = map[error:internal server error]. Reproduced through temporary net.Pipe HTTP transport; native proof remains with the lead.
- B OPEN W1 reconciliation-a pkg/controller TestGitHubAppAutomationRequiresConsentAndUsesDefaultBranch: github_app_automation_test.go:192: consented main push=map[error:internal server error]. Reproduced through temporary net.Pipe HTTP transport; native proof remains with the lead.
- B OPEN W1 reconciliation-a pkg/controller TestGitHubAppAutomationConfigErrorsAndManualOverride: github_app_automation_test.go:240: manual/config duplicate=map[error:internal server error]. Reproduced through temporary net.Pipe HTTP transport; native proof remains with the lead.
- B OPEN W1 reconciliation-a pkg/controller TestGitHubAppCronConflictsWithManualControllerSchedule: github_app_crons_identity_test.go:158: ArmCronSchedule: GitHub repository requires a resolved numeric ID. Reproduced through temporary net.Pipe HTTP transport; native proof remains with the lead.
- B OPEN W1 reconciliation-a pkg/controller TestGitHubAppCronRenameConflictStopsOldAppRow: github_app_crons_identity_test.go:187: ArmCronSchedule: GitHub repository requires a resolved numeric ID. Reproduced through temporary net.Pipe HTTP transport; native proof remains with the lead.
- B OPEN W1 reconciliation-a pkg/controller TestGitHubAppCronRefusesManualScheduleAtOldRedirectedURL: github_app_crons_identity_test.go:220: ArmCronSchedule: GitHub repository requires a resolved numeric ID. Reproduced through temporary net.Pipe HTTP transport; native proof remains with the lead.
- B OPEN W1 reconciliation-a pkg/controller TestGitHubAppCronCannotProveManualGitHubURLDistinct: github_app_crons_identity_test.go:257: ArmCronSchedule: GitHub repository requires a resolved numeric ID. Reproduced through temporary net.Pipe HTTP transport; native proof remains with the lead.
- B OPEN W1 reconciliation-a pkg/controller TestGitHubAppCronFailureDoesNotSuppressSubscribedPush: github_app_crons_test.go:157: subscribed push with cron failure = 500, want 202. Reproduced through temporary net.Pipe HTTP transport; native proof remains with the lead.
- B OPEN W1 reconciliation-a pkg/controller TestGitHubAppStaleCronPushStillStartsSubscribedPipeline: github_app_crons_test.go:181: subscribed stale push = 500, want dispatch. Reproduced through temporary net.Pipe HTTP transport; native proof remains with the lead.
- B OPEN W1 reconciliation-a pkg/controller TestGitHubAppPushRunsOnlyInTheInstallationsTeam: github_app_test.go:613: push = 500 map[error:internal server error]. Reproduced through temporary net.Pipe HTTP transport; native proof remains with the lead.
- B OPEN W1 reconciliation-a pkg/controller TestGitHubAppRunLimitRefusesWithoutConsumingDelivery: github_app_test.go:661: limited app webhook = 500 map[error:internal server error], want compute-limit 429. Reproduced through temporary net.Pipe HTTP transport; native proof remains with the lead.
- B OPEN W1 reconciliation-a pkg/controller TestGitHubAppTagPushRequiresTagSubscription: github_app_test.go:704: tag push = map[error:internal server error]. Reproduced through temporary net.Pipe HTTP transport; native proof remains with the lead.
- B OPEN W1 reconciliation-a pkg/controller TestGitHubAppTagPatternsFilterPushes: github_app_test.go:739: refs/tags/v1.2.3 = map[error:internal server error], want dispatched. Reproduced through temporary net.Pipe HTTP transport; native proof remains with the lead.
- B OPEN W1 reconciliation-a pkg/controller TestGitHubAppPushBranchSubscription: github_app_test.go:803: matching push = 500 map[error:internal server error], want dispatched. Reproduced through temporary net.Pipe HTTP transport; native proof remains with the lead.
- B OPEN W1 reconciliation-a pkg/controller TestGitHubAppPullRequestBaseBranchSubscription: github_app_test.go:835: matching PR base = 500 map[error:internal server error], want dispatched. Reproduced through temporary net.Pipe HTTP transport; native proof remains with the lead.
- B OPEN W1 reconciliation-a pkg/controller TestGitHubAppBranchFilterLeavesTagPatternsAlone: github_app_test.go:890: refs/heads/main = map[error:internal server error], want dispatched. Reproduced through temporary net.Pipe HTTP transport; native proof remains with the lead.
- B OPEN W1 reconciliation-a pkg/controller TestGitHubAppRedeliveryStartsOneRun: github_app_test.go:905: first delivery refused. Reproduced through temporary net.Pipe HTTP transport; native proof remains with the lead.
- B OPEN W1 reconciliation-a pkg/controller TestGitHubAppForkPullRequestIsNotRun: github_app_test.go:966: control: a same-repository PR = map[error:internal server error], want dispatched. Reproduced through temporary net.Pipe HTTP transport; native proof remains with the lead.
- B OPEN W1 reconciliation-a pkg/controller TestGitHubAppAdditionalEventsRequireSubscription: github_app_test.go:1000: pull_request delivery = map[error:internal server error], want dispatched. Reproduced through temporary net.Pipe HTTP transport; native proof remains with the lead.
- B OPEN W1 reconciliation-a pkg/controller TestGitHubAppRepositoryIdentitySurvivesRenameAndTransfer: github_app_test.go:1101: push after rename = map[error:internal server error]. Reproduced through temporary net.Pipe HTTP transport; native proof remains with the lead.
- B OPEN W1 reconciliation-a pkg/controller TestGitHubAppReleaseReplayAndCoverage: github_app_test.go:1143: release = map[error:internal server error]. Reproduced through temporary net.Pipe HTTP transport; native proof remains with the lead.
- B OPEN W1 reconciliation-a pkg/controller TestGitHubAppUndatedEventIsIgnored: github_app_test.go:1193: control: a dated push = map[error:internal server error], want dispatched. Reproduced through temporary net.Pipe HTTP transport; native proof remains with the lead.
- B OPEN W1 reconciliation-a pkg/controller TestGitHubAppRedeliveryChargesOnlyShedRuns: github_app_test.go:1208: first delivery = map[error:internal server error], want one run and one shed. Reproduced through temporary net.Pipe HTTP transport; native proof remains with the lead.
- B OPEN W1 reconciliation-a pkg/controller TestGitHubAppPartialReleaseRedeliveryKeepsResolvedCommit: github_app_test.go:1236: first release = map[error:internal server error]. Reproduced through temporary net.Pipe HTTP transport; native proof remains with the lead.
- B OPEN W1 reconciliation-a pkg/controller TestGitHubAppFloodCapCountsTheTeamsRuns: github_app_test.go:1273: first push = 500 map[error:internal server error]. Reproduced through temporary net.Pipe HTTP transport; native proof remains with the lead.
- B OPEN W1 reconciliation-a pkg/controller TestGitHubAppReplayAfterAMoveStartsNothing: github_app_test.go:1304: olga's push = map[error:internal server error]. Reproduced through temporary net.Pipe HTTP transport; native proof remains with the lead.
- B OPEN W1 reconciliation-a pkg/controller TestGitHubAppBranchFiltersGateBranchAndPullRequestEvents: github_app_test.go:1376: create release = map[error:internal server error], want dispatched. Reproduced through temporary net.Pipe HTTP transport; native proof remains with the lead.
- B OPEN W1 reconciliation-a pkg/controller TestGitHubAppSignedBodyKeepsItsEvent: github_app_test.go:1434: push after refused re-sends = map[error:internal server error]. Reproduced through temporary net.Pipe HTTP transport; native proof remains with the lead.
- B OPEN W1 reconciliation-a pkg/controller TestGitHubAppBranchDeleteRunsOnGitHubsPayload: github_app_test.go:1477: GitHub-shaped delete = map[error:internal server error], want dispatched. Reproduced through temporary net.Pipe HTTP transport; native proof remains with the lead.
- B OPEN W1 reconciliation-a pkg/controller TestGitHubAppEventOlderThanTheReplayWindowIsRefused: github_app_test.go:1517: control: a push inside the window = map[error:internal server error], want dispatched. Reproduced through temporary net.Pipe HTTP transport; native proof remains with the lead.
- B OPEN W1 reconciliation-a pkg/controller TestGitHubAppRunRecordsTheDefaultBranch: github_app_test.go:1533: push = 500 map[error:internal server error]. Reproduced through temporary net.Pipe HTTP transport; native proof remains with the lead.
- B OPEN W1 reconciliation-a pkg/controller TestGitHubAppCheckRunRerequestedRerunsItsPipeline: github_checks_test.go:100: rerequested = 202 map[reason:the check_run names no head commit status:ignored], want one run. Reproduced through temporary net.Pipe HTTP transport; native proof remains with the lead.
- B OPEN W1 reconciliation-a pkg/controller TestGitHubAppCheckSuiteRerequestedRerunsEveryPipeline: github_checks_test.go:139: check_suite requested started runs: the team has 0, want 2. Reproduced through temporary net.Pipe HTTP transport; native proof remains with the lead.
- B OPEN W1 reconciliation-a pkg/controller TestGitHubAppCheckSuiteRerequestedNeedsEachPipelinesOwnRun: github_checks_test.go:155: push = 500 map[error:internal server error], want one run. Reproduced through temporary net.Pipe HTTP transport; native proof remains with the lead.
- B OPEN W1 reconciliation-a pkg/controller TestGitHubAppRerequestedNeedsPriorEventsSubscription: github_checks_test.go:171: push = 500 map[error:internal server error], want one run. Reproduced through temporary net.Pipe HTTP transport; native proof remains with the lead.
- B OPEN W1 reconciliation-a pkg/controller TestGitHubAppRerequestedUsesCurrentBranchFilter: github_checks_test.go:193: push = 500 map[error:internal server error], want one run. Reproduced through temporary net.Pipe HTTP transport; native proof remains with the lead.
- B OPEN W1 reconciliation-a pkg/controller TestGitHubAppRerequestedForkOrForeignCommitStartsNothing: prerequisite App intake failure/panic; assertions remain enabled. Reproduced through temporary net.Pipe HTTP transport; native proof remains with the lead.
- B OPEN W1 reconciliation-a pkg/controller TestGitHubAppRerequestedNeedsTheBinding: prerequisite App intake failure/panic; assertions remain enabled. Reproduced through temporary net.Pipe HTTP transport; native proof remains with the lead.
- B OPEN W1 reconciliation-a pkg/controller TestGitHubAppRerequestedSpendsTheRunBudget: github_checks_test.go:304: push = 500 map[error:internal server error], want one run. Reproduced through temporary net.Pipe HTTP transport; native proof remains with the lead.
- B OPEN W1 reconciliation-a pkg/controller TestGitHubRunnerCredentialClaimsOnlyItsRepository: github_runners_test.go:137: SQL logic error: table triggers has no column named pipeline (1). Reproduced through temporary net.Pipe HTTP transport; native proof remains with the lead.
- B OPEN W1 reconciliation-a pkg/controller TestGitHubRunnerCredentialReachesNothingOutsideItsRoutes: github_runners_test.go:180: SQL logic error: table triggers has no column named pipeline (1). Reproduced through temporary net.Pipe HTTP transport; native proof remains with the lead.
- B OPEN W1 reconciliation-a pkg/controller TestGitHubRunnerCredentialClaimsOnlyItsOwnPush: github_runners_test.go:221: SQL logic error: table triggers has no column named pipeline (1). Reproduced through temporary net.Pipe HTTP transport; native proof remains with the lead.
- B OPEN W1 reconciliation-a pkg/controller TestSourceCredential_AppTokenReadsTheVerifiedRepository: source_credential_test.go:54: SQL logic error: table triggers has no column named pipeline (1). Reproduced through temporary net.Pipe HTTP transport; native proof remains with the lead.
- B OPEN W1 reconciliation-a pkg/controller TestSourceCredential_TypedRepositoryNeedsASubscription: source_credential_test.go:54: SQL logic error: table triggers has no column named pipeline (1). Reproduced through temporary net.Pipe HTTP transport; native proof remains with the lead.
- B OPEN W1 reconciliation-a pkg/controller TestSourceCredential_NamesTheRemedy: source_credential_test.go:54: SQL logic error: table triggers has no column named pipeline (1). Reproduced through temporary net.Pipe HTTP transport; native proof remains with the lead.
- B OPEN W1 reconciliation-a pkg/controller TestSourceCredential_NeedsTheRunsOwnClaimToken: source_credential_test.go:187: SQL logic error: table triggers has no column named pipeline (1). Reproduced through temporary net.Pipe HTTP transport; native proof remains with the lead.
- B OPEN W1 reconciliation-a pkg/controller TestSourceCredential_AppTokenReadsOnlyTheOwnersExtraRepos: source_credential_test.go:54: SQL logic error: table triggers has no column named pipeline (1). Reproduced through temporary net.Pipe HTTP transport; native proof remains with the lead.
- W1 reconciliation-a verification: 44/93 assigned controller groups pass locally, 49 remain failing. Full cmd/sparkwing-controller and internal/web suites pass with temporary HTTP transport. Focused CLI, onboarding, connection, property, store and docs checks pass. Whole-repository build, scoped vet and commentcheck pass. Store race selection passes in 76.489s; runner-removal race selection passes in 1.116s. Native report remains unchanged at 110 top-level failures, including 48 assigned controller groups; no native completion claim.
