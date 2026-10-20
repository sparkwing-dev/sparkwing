# W2 integration-merge recovery

<!-- design:begin -->

W2 is ready for lead review against the currently declared contracts. The
remaining real integrations and legacy fixture ports below require other areas.
This is not a claim that the native credential matrix or production composition
is complete. No lead-owned amendment or handoff file changed.

## Merge recovery and implementation

Recovered the working tree from the merge of bw-3095-rewrite into
bd905b7de15faa2dc7f3256ff1b618a8db179146. All 44 conflicted paths have a
working-file resolution and no conflict markers remain. Retired client,
orchestrator, localws and credential test files stay deleted. The amended
source_attempt and failure-stage wire wins in the lifecycle fixture. Both
migration sections survive. The independent W2 route table and final behavior
remain in place.

The merge index still records 44 unmerged paths. SWARM-BRIEF prohibits Git
writes, and LOCAL.md says this sandbox cannot write the shared worktree metadata.
The lead must stage the resolved paths and conclude the merge. No Git write,
abort, reset or alternate Git directory was attempted. The conflict paths are
listed at the end of this report.

Whole-repository build passed immediately after recovery. Test compilation then
found retired Client uses in acceptance and two old client fixture files. The
OIDC rotation proof now uses actual HTTP. The typed step test imports the final
engine client. Duplicate Client-only concurrency, log and metric fixtures are
removed, with their retained HTTP/final-client coverage mapped in the inventory.
The final client suite passes.

Deleted the unused WriteUnsupportedRoute helper and UnsupportedRouteError
constant, 16 production lines. The remaining caller assertions use the canonical
error envelope or route-miss message. The migration source, changelog, mirrors
and API snapshot describe the cut. No replacement alias or fallback is added.

Deleted six legacy fixture files, 767 lines against their integration-index
versions: runner_scopes_test.go, declared_repo_grants_nothing_test.go,
here_runner_test.go, team_boundary_paths_test.go, client/log_read_routes_test.go
and client/metric_routes_test.go. Two generated real-SQLite HTTP properties
replace the remaining declared-repository and encoded-path security assertions.
The here cases map to existing final intake, claim, lifecycle, sizing and
response-loss tests. The old fixed six-hour lifetime and no-reclaim expectations
retire with the token API. The contract's here selection has no reason field.

Invariant: a machine credential gains no member authority from grant names or
caller hints. CredentialMachineHTTPCannotBecomeAMemberProperty varies grants
and headers against real capability lookup and actual intake/retry/cancel
routes. Refusals create no run and persist no cancellation.

Invariant: URL spelling cannot expose another team's jobs or change its run.
CredentialMemberHTTPEncodedPathsPreserveTenantProperty mints real member roots
in two teams, generates encoded segments, verifies positive own reads and
foreign refusals, and checks nine noncanonical paths and unchanged run state.
The first version copied the old 404 expectation. The final policy requires
403 for this live foreign authority, and the test now checks forbidden.
No production refusal was weakened.

W2 production versus main is +13,196/-16,909, net **-3,713**. This is within
the -250 budget. Counts exclude tests, testdata, acceptance and contract paths,
using the lead's ownership rules. The merge also carries other areas' deletions.
Those are not attributed to W2's new source work.

## Lead decisions confirmed

| Decision | C2 implementation and evidence | Real integration still open |
| --- | --- | --- |
| 07:40 item 2 | PaymentEvent verification covers checkout, card setup, payment success/failure, refunds, warnings and dispute open/close. Full capability tests and Payment HTTP selection pass. | W1b settlement for the expanded verified events. |
| 07:40 item 3 | Startup forwards the fixed seeded bootstrap request through BootstrapAdminIssuer. EnsureBootstrap tests pass. | W1 EnsureBootstrapCapability and HasDeploymentAdministrator transactions/reads. |
| 07:40 item 4 | C2 honors rotation grace until RevokedAt. CapabilityManagement HTTP selection passes. | W1 readAuthorityTx still rejects every non-nil RevokedAt. |
| 07:40 item 5 | Server.HTTP supplies wingd.LocalHTTP with shared API/dashboard handlers and verified connection identity. LocalControllerHTTP, PolicyPeer and default-console tests pass. | W1 peer authority and console committing transactions. W8 owns serving/drain. |
| 07:40 item 7 | The legacy runner-permission, token-metering and credits.grant routes stay deleted. Final route expectations, discovery, payment and retired-route checks pass. | Native composition and lead ratification. |
| 07:40 item 10 | SyncLauncher forwards authenticated authority and immutable observations, then strips returned bearers. EngineLauncherSync and fuzz seeds pass. | W1 Operator.ReadLauncherSync and native W8 award/Job correlation evidence. |
| 09:40 item 1 | The independent table is in internal/controller/credential_route_expectations_test.go beside the route tests. internal/acceptance/credential_routes.go is absent. Declaration, ordering and drift tests pass. | None in W2. |

The C2 parts of A1-01/03/05/08/09/11/14/15/16/19/20/21/22/23,
A1-24 through A1-35, A1-38/39/40/49/55 remain implemented against their
existing interfaces. Typed step reports now compile and pass with the merged
C3 request. No foreign contract signature changes in this session.
A1-53 native evidence remains with T1/lead.

## Contract changes needed and waiting integrations

- W1's authentication_binding.go still never assigns BindingState.CancelReason.
  C2 already consumes it, with job intent before run intent. The separate
  cancellation intent columns exist. Do not reconstruct this snapshot in W2.
- W1 still returns the awarded attempt zero in the leaf capability while its
  job snapshot advances to attempt one. The explicit current-attempt fake in
  CredentialStartedClaimHTTPConsumesContractAttempt remains necessary. Preserve
  exact process-attempt fencing when projecting the held job credential.
- W1 must supply the existing authority-bearing credential, account, team,
  App, enrollment, identity-link, session, console, metering and billing
  transactions recorded in the prior W2 handoffs. Missing adapters continue
  to refuse with policy_missing. No raw store mutation is used as a fallback.
- W1 Tenant.ReadRunners and Operator.ReadLauncherSync remain absent. C2's shared
  snapshot type and readers already accept contract fakes. Production peer
  authentication is also absent from authenticationAuthorityTx, which accepts
  only bearer and session lookups. Roleless-session persistence remains open.
- W8/W9 must declare queue-depth ingestion. ClaimRequest still carries only
  available cores/memory. C2 preserves queue depth in RunnerSnapshot reads and
  adds no private wire field.
- W1/W9/lead must finish child/ref reads and claim-input transport and their
  authorized selection transactions. Final intake already carries lineage.
  Legacy ResolveClaimInput/EnqueueChildRun are not production adapters. The
  retained cross-run fixtures cannot be ported to a missing transport.
- W1/W7/W9/lead must finish final log HTTP composition. Signing needs persisted
  retention class and an exact writer fence before issuing a URL. Existing
  RecordSegments and W7 BatchSigner/UploadVerifier are insufficient to invent
  that fact. The final log read client now also expects C3 LogPage; the
  controller's dashboard-backed read path is not proof of that native contract.
  Retained log mutation fixtures remain open until the contracts are supplied.
- W9's final Controller interface has no native metric/usage transport. The
  controller HTTP boundaries and validation remain tested. The removed Client
  fixture is not replaced with an undeclared transport.

New-account admission and the old list projection were already failing in the
prior session. They are not claimed fixed or rerun unchanged here. Remaining
legacy identity/team/source fixture helpers still depend on W1 persistence.
No new security posture, billing policy, schema, limit or product decision is made.

## Verification

All Go commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-gocache and GOTMPDIR=/tmp/sw-astra-W2/go, with the absolute
worktree passed to go -C. Tests use -p 4 and -timeout=180s.

Passed:

- Whole-repository go build -p 4 ./....
- Full internal/capability and internal/apierr suites. Full controller/client
  suite after removing retired fixture dependencies and updating canonical errors.
- W2 executable acceptance selection in 31.511s:
  ^Test(Account|Authority|Credential|CapabilityManagement|ConcurrencyHTTP|Data|Secret|Source|Session|Console|Login|OAuth|OIDC|Identity|Operator|Deployment|Discovery|Payment|RootProvisioning|GitHubRunnerAdmin|RunnerMetering|Credit|Queue|Sizing|SelfService|Engine),
  plus FuzzCredential/FuzzData seeds. Existing native skips remain.
- Controller selection in 16.848s: Engine, RunnerInventory, QueueState,
  ClaimAttention, Policy, ServerPolicy, Credential, FinalCredential, Browser,
  OperatorPage, RequestPrincipal, CredentialLabel, Audit, EnsureBootstrap,
  LocalControllerHTTP, Console, TeamWriteHTTP, BillingPageHTTP, Reporting,
  ControllerRefusesAnonymous, RunControlHTTP, Business, MetricsHTTP,
  RouteGuard, ScheduleHTTP, Payment, AuthorizedHandlers and FuzzEngine seeds.
  StreamsOutlive and ReadsStayInside were excluded because they require sockets.
- Final new properties, independent route checks, console/peer/local HTTP and
  cancellation-intent selections pass. Targeted final store here tests pass.
- Native TypeScript: go test -p 4 ./sdk/typescript/test/nativehost
  -run '^TestNative' -count=1 -timeout=180s, 6.442s. No setup failure.
- Scoped vet for controller, controller/client, capability, apierr and acceptance.
- Catalog, port destinations and Here contract/client checks. Documentation
  symbol, mirror, migration and changelog tests. API snapshot regeneration.
- Commentcheck -base main . is clean across 2,106 Go files. git diff --check
  passes. No conflict markers remain. No shared Git configuration override was found.

No native credential-matrix cell is newly completed. No pipeline, deployment,
provider network, release or whole-repository test suite ran. Installed
sparkwing info was invoked once during bootstrap before the brief's executable
prohibition was loaded. No later Sparkwing binary was run. The lead owns
native integration, review, merge-index completion and landing.

## Shared files and friction

Outside W2-owned source, this continuation changes acceptance concurrency,
OIDC, lifecycle and reporting fixtures; client/unsupported_test.go and the two
removed client fixtures; the port inventory; migration/changelog sources and
mirrors; and regenerated API snapshots. The conflict recovery also resolves
other areas' deleted files listed below. No W1 or W8 production implementation
was edited. Scratch is removed at handoff. The shared build cache is retained.

Friction was the interrupted merge, retired-client fixture references, the
obsolete error-envelope assertion and the missing cross-area contracts above.

## Working-file conflict resolutions for the lead

- .apidiff/pkg_controller_client.txt - regenerate from final source
- cmd/sparkwing/crons_profile_test.go - delete retired surface or fixture
- docs/migrations/_unreleased.md - keep both migration sections
- internal/orchestrator/event_payload_mask_test.go - delete retired surface or fixture
- internal/orchestrator/hosted_retry.go - delete retired surface or fixture
- internal/orchestrator/hosted_retry_test.go - delete retired surface or fixture
- internal/orchestrator/httpconcurrency.go - delete retired surface or fixture
- internal/orchestrator/secret_args_execution_test.go - delete retired surface or fixture
- internal/orchestrator/trigger_host_here_test.go - delete retired surface or fixture
- internal/orchestrator/trigger_host_test.go - delete retired surface or fixture
- internal/controller/agents_test.go - delete retired surface or fixture
- internal/controller/claim_test.go - delete retired surface or fixture
- internal/controller/client/client.go - delete retired surface or fixture
- internal/controller/client/concurrency.go - delete retired surface or fixture
- internal/controller/client/concurrency_test.go - delete retired surface or fixture
- internal/controller/client/coordination.go - delete retired surface or fixture
- internal/controller/client/coordination_test.go - delete retired surface or fixture
- internal/controller/client/logs.go - delete retired surface or fixture
- internal/controller/client/oidc.go - delete retired surface or fixture
- internal/controller/credits_team_test.go - delete retired surface or fixture
- internal/controller/declared_repo_grants_nothing_test.go - delete retired surface or fixture
- internal/controller/doc.go - use integration shape with W2 behavior preserved
- internal/controller/livelogs_test.go - delete retired surface or fixture
- internal/controller/log_segments_internal_test.go - keep W2 signer deletion and remove stale imports
- internal/controller/node_claim_ownership_test.go - delete retired surface or fixture
- internal/controller/node_claim_test.go - delete retired surface or fixture
- internal/controller/node_claim_token_log_segments_internal_test.go - delete retired surface or fixture
- internal/controller/node_placement_test.go - delete retired surface or fixture
- internal/controller/run_limits_test.go - delete retired surface or fixture
- internal/controller/runner_claim_cross_run_test.go - delete retired surface or fixture
- internal/controller/runner_claim_test.go - delete retired surface or fixture
- internal/controller/runner_scopes_test.go - delete retired surface or fixture
- internal/controller/secrets_team_test.go - delete retired surface or fixture
- internal/controller/storage_test.go - delete retired surface or fixture
- internal/controller/trigger_parent_claim_test.go - delete retired surface or fixture
- pkg/docs/mirror/migrations/_unreleased.md - keep both migration sections
- pkg/localws/crons.go - delete retired surface or fixture
- pkg/localws/crons_test.go - delete retired surface or fixture
- pkg/localws/localws.go - delete retired surface or fixture
- pkg/localws/localws_test.go - delete retired surface or fixture
- pkg/localws/mux_specificity_test.go - delete retired surface or fixture
- pkg/localws/token_gate_test.go - delete retired surface or fixture
- tests/acceptance/engine_lifecycle_http_test.go - use integration shape with W2 behavior preserved
- tests/acceptance/engine_reporting_http_test.go - use integration shape with W2 behavior preserved

<!-- design:end -->

STATUS: COMPLETE
