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
