# W2 final business routes and verifier retirement

<!-- design:begin -->

Started from b6a2a4157. No Git writes. Lead-owned handoffs.md and amendments
are unchanged. This report supersedes the business report's remaining-route
list. Prior reports retain the other amendment and lead-decision evidence.

## Implemented and deleted

All seventeen remaining legacy registrations use PolicyRouter: run deletion,
team rename/deletion, invitation creation/deletion, build trust, App unbind,
trigger writes, automation read/write/delete, extra-repository configuration,
and the four billing mutations. Actual discovery follows the three new
endpoint groups. Shared paths have one policy router and exact method discovery.
The old private mux is removed from network, local and browser API routing.

Deleted the token-table Authenticator, its positive and negative caches,
concurrent verification flights, argon2 admission/failure machinery, requireScope,
here/Actions route fences, legacy tenant route table, token-budget middleware
wrapper and redundant browser API cookie/session middleware. The old enable,
authenticator-injection, visibility and role-projection APIs are removed.
Constructors already install the common authority boundary. Health reports that
authentication is enabled. Callers use the constructor, WithAuthorization,
or store.ScopesForRole as appropriate. The data request budget remains in use.

Added three C2-local transaction interfaces in real route files: TeamWriter,
GitHubAppWriter and BillingWriter. Each receives Authorized, uses an exact
team resolver and refuses absent or wrong-team adapters. No raw write fallback
remains on these routes. Their exact signatures and committing obligations
are in c2-credentials.md. No W1/W1b implementation or foreign signature changed.

App extra-repository names and IDs now cross one committing boundary. App
configuration passes provider-resolved installation/repository facts rather
than a caller-selected actor. Run deletion commits before physical log-prefix
and local-directory cleanup. Empty team-deletion confirmation is refused.

Invitation creation and all three hosted billing-page POSTs require one replay
header. Billing reservations precede provider access. The committed reservation
ID becomes the service request key, and card setup also sends setup_id. Recorded
replay returns its URL without another provider request. Completion receives
provider identity and bounded expiry. Failed delivery preserves reconciliation
through its committing adapter. Billing failures use canonical errors and
business reasons under details. URL validation also applies to completed replay.

Dashboard invitation/billing helpers supply the required headers and read the
canonical error envelope. The account page and helper no longer call retired
self-service deletion. That action remains in the operator console. Removed
four obsolete web tests with that helper/UI.

W2 production delta this session: 564 added, 1658 deleted, net -1094.
W2 production delta versus main: -2100, within the -250 budget. Counts use the
lead's ownership/exclusion rules and include untracked production files.
Thirty-two Go tests of retired verifier/cache/legacy-boundary behavior are
removed. Two existing behavioral tests are renamed to reflect final APIs.
Reusable HTTP/store fixtures remain. Cache implementation, open-mode and
admin-superset expectations have no retained equivalent.

## Invariants and evidence

Invariant: the verified authority and matched resource select team and actor;
request selectors and legacy credentials cannot grant access. The existing
TestReportingRouteAuthorityProperty now includes all seventeen final routes,
with generated expiry, revocation, audience, membership, grant, deployment and
foreign-team refusals through real policy and actual Server registrations.

TestTeamWriteHTTPAuthorityProperty exercises six team/run writes and App
unbind, trigger deletion and automation withdrawal. Generated cases verify
actor/team forwarding, wrong-team adapters, committing authority conflicts and
private backend errors. The writers are fakes. Provider discovery and real
committing App-consent races are not proved by these cases.

Invariant: a hosted payment page is opened only for committed, verified
reservation facts, and a completed replay performs no provider request.
TestBillingPageHTTPAuthorityAndReplayProperty covers credit, card and recovery
pages, provider ordering, replay, provider faults, completion conflicts, expiry
ceilings, budget forwarding and strict bodies. The transaction and provider are
fake. This is not financial persistence or external delivery evidence.

No new acceptance registry cell or native credential matrix cell is enabled.
The existing executable W2 acceptance selection passes. t1-port-inventory.md
records retired expectations, retained proofs and remaining integration.

## Remaining implementation and foreign integration

- W1 implements TeamWriter: live authority/membership checks at commit,
  team/deletion safeguards, invitation/email/expiry/use/replay rules, build-trust
  and run lifecycle/ownership. Run deletion must allow repeat physical cleanup.
- W1 implements GitHubAppWriter: current installation ownership/suspension,
  verified numeric repository facts, consent recheck after discovery, atomic
  extra-repository names/IDs and transaction-derived actors. Existing raw App
  methods cannot satisfy it.
- W1/W1b implement BillingWriter. Prepare owns current card/trust/budget,
  purchase/balance/deletion guards, mode, immutable reservation/replay and
  provider facts. Complete rechecks current authority and exact reservation.
  Failure records delivery without erasing the request key or creating a new
  reservation after an ambiguous provider response. The checkout service must
  persist idempotent key/setup_id. C2 supplies the HTTP/provider transport.
- W1 still lacks EnsureBootstrapCapability and HasDeploymentAdministrator.
  Seeded startup remains fail-closed. Issue must accept a seed only through the
  gated bootstrap transaction, persist only its digest, and never revive it.
- W1's authentication_binding.go still does not populate CancelReason. C2
  BindingState and consumers already accept it. Project independent first
  cancellation intent with job before run intent through the single snapshot.
- The default store authentication switch still accepts only bearer/session.
  W1 must supply verified local-peer snapshots, roleless sessions and the
  required console/committing authority representation. LocalController.HTTP,
  verified-transport extraction and the console adapter remain implemented.
- ReadLauncherSync still needs W1's real immutable award fence and W8/W9's
  Kubernetes instance correlation. EngineController.SyncLauncher is implemented
  against that contract, with no bearer in its response.
- W1b still owns settlement of the already verified card setup/payment,
  refund, warning and dispute PaymentEvent kinds. Prior payment handoff and
  provider properties remain. W2 does not restore service-bearer callbacks.
- The prior credential/source/data, account, sizing, concurrency, schedule and
  run-control transaction/read adapters remain open. See the corresponding
  earlier reports. No duplicate SQL reader or alternate issuer is introduced.
- Remaining W2 cleanup includes Principal-based browser page/App-provider
  flows and old unregistered runtime helpers. Principal remains because those
  callers still use it; the old token verifier does not. W3/W9 still own the
  StateStoreOld approval/step clients and host cutover. Broad old team/App/
  billing fixtures still depend on retired token/trigger surfaces and need
  migration with their production integrations. W2 is incomplete.

No new product decision or foreign contract-signature change is requested.
The new mechanisms are the three required committing transaction seams and
shared BillingPage facts. Calling unfenced writes would bypass the frozen
authority contract. No new environment variable, cache, timeout, customer
limit, compatibility route or schema is added.

## Verification and friction

Final Go checks use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-gocache and GOTMPDIR/TMPDIR=/tmp/sw-astra-W2/tmp. Tests use
-p 4 and commands select this checkout with go -C.

Passed:

- Whole-repository go build -p 4 ./... and scoped vet for controller/client,
  capability, apierr, apiroutes, executable acceptance and controller command.
- Controller selection in 4.729s:
  ^Test(Payment|EnsureBootstrapAdminToken|EngineLauncherSync|LocalController|PolicyPeer|Console|ScheduleHTTP|Reporting|RunControlHTTP|Business|MetricsHTTP|Policy|RouteGuard|CredentialRoute|StoreDataStorageRoutes|CallerStorageStanding|TeamWriteHTTP|BillingPageHTTP|ControllerRefusesAnonymousBeforeBootstrapProperty|AuthorizedHandlersDoNotAuthorize).
- Full capability, apierr, apiroutes, apiref and apispec suites.
- Real-store EngineJobLifecycleRenewsAndReplaysUnderStoreFences,
  EngineOwnerReconcilesExecutesAndFinishes,
  EngineCancelRechecksMembershipAndCommitsEffectsOnce and
  EngineCancellationIntentPreservesHolderProperty, 1.143s.
- Executable acceptance, 13.589s:
  ^Test(Account|Authority|Credential|ConcurrencyHTTP|Data|Secret|Source|Session|Console|Login|OAuth|OIDC|IdentityLink|Operator|Deployment|Discovery|Payment|RootProvisioning|GitHubRunnerAdmin|RunnerMetering|Credit|Queue|Sizing|SelfService|Engine|ReportingClient).
- Native TypeScript: go test -p 4 ./sdk/typescript/test/nativehost
  -run '^TestNative' -count=1 -timeout=180s, 21.126s. No setup failure.
- Web: node --import tsx --test src/lib/billing.test.ts src/lib/teams.test.ts.
  Direct suite output also establishes 40 teams and 34 billing tests passed.
  tsc --noEmit --incremental false passes.
- API docs and public snapshot regeneration, docs mirror/changelog/private
  content/symbol tests, commentcheck -base main . across 2177 Go files,
  and git diff --check.

The first generated team-write run expected 503 for private backend faults;
canonical unclassified faults are internal/500. Its expectation is corrected.
Automation's strict query grammar also refused the property's unknown keys;
that case now supplies the exact valid query. No production refusal is weakened.
Detailed direct web output revealed the retired account-deletion helper's old
reauth expectation. Deleting that caller/UI fixed it. The runner-refusal shell
fixture prints an expected permission-denied diagnostic and passes. Prettier
is not installed at web/node_modules/.bin/prettier; no dependency was added.

No socket-dependent broad controller suite, candidate runtime, pipeline,
service, release or repository Git write ran. The lead owns review and landing.
Shared Git configuration has no core.worktree override. Scratch is deleted at
session end; the shared build cache is retained.

## Shared files touched

Production caller migration outside W2: cmd/sparkwing-controller/main.go and
internal/web/browserfixture/main.go remove the retired enable call. Test caller
migration: cmd/sparkwing/crons_profile_test.go; internal/orchestrator's
trigger_host_test.go, trigger_host_here_test.go, event_payload_mask_test.go and
secret_args_execution_test.go; internal/controller/client/coordination_test.go; and
tests/acceptance/storage_filesystem_test.go. These are constructor/API changes.

Discovery: internal/apiroutes/apiroutes.go and its business registration test.
Web: src/lib/teams.ts, billing.ts, their tests and app/account/page.tsx.
Documentation: c2-credentials.md, t1-port-inventory.md, auth/migration docs,
CHANGELOG.md, generated embedded mirrors and .apidiff/pkg_controller.txt.
All other source changes are W2-owned.

<!-- design:end -->

STATUS: CONTINUE
