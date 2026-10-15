# W2 browser connection and console continuation

<!-- design:begin -->

Started from a28a9ea7b. No Git writes. Lead-owned handoffs.md and amendments
are unchanged. The prior final-route and lead-decision reports retain the
payment, bootstrap, local HTTP, launcher and legacy API-route evidence.

## Implemented and deleted

All seven GitHub App browser routes register through PolicyRouter. Start,
picker, selection and completion verify the live session and active-team
team.admin alternative before running their handler. Form posts require
single-valued, session-bound CSRF and same origin. Setup and callback verify
signed, unexpired state and the browser verifier. Duplicate flow cookies and
repeated return parameters are refused. Both install and existing-installation
selection run through the actual HTTP routes in tests.

The flow consumes capability.Authorized and the new C2-local GitHubConnectWriter.
It passes current authority to the linked-identity read, one-use nonce transaction
and final installation bind. Missing or foreign-team adapters refuse before
provider access. The bind must recheck the linked GitHub subject after provider
verification. Raw AccountIdentity, ConsumeGitHubAppConnectState and
BindGitHubAppInstallation are no longer production fallbacks for this flow.
The browser Principal lookup, renewal wrapper, signedInBrowser,
resolveBrowserSession, githubAppOwner and standalone form middleware are deleted.

Nine console business mutations use C2-local ConsoleWriter: grants, trust,
freeze, unfreeze, account deletion, team deletion, waitlist approval, credit
settings and installation unbinding. No raw store write remains on those paths.
Account email resolution moves into the committing adapter, preserving the
ambiguous-email and last-owner conflicts. Freeze responses come from the
committed projection. A failed approval produces no notification. Waitlist
approval carries the route contract's Idempotency-Key to its transaction.
The dashboard sends that header and reads the canonical error.message.
Missing console adapters return policy_missing.

Deleted unused token-table metering, HTTP billing finalizers, credit/compute
refusal side-effect helpers, the unused principal.go helpers, auditPrincipal
and chargedPrincipal. The one live webhook compute-refusal caller uses the
smaller logging-only signature. Signed-data request budgeting derives its
team/token key directly and no longer manufactures a legacy Principal.
The active store settlement lifecycle is unchanged.

Production Go counts use the lead's ownership and exclusion rules, including
untracked production files. This session adds 322 and deletes 684, net -362.
W2 versus main is -2462, within its -250 budget. The deleted direct HTTP
settlement regression contains 71 test lines. Principal itself remains because
old unregistered metrics, budget, egress and tenant helpers still reference it.

## Invariants and acceptance evidence

Invariant: only a live account session with current team-owner authority can
connect an installation, and committing transactions recheck that authority
and the linked GitHub identity. GitHubBrowserConnectionAuthorityProperty and
StateProperty generate forged selectors, roles, cookies, forms, verifiers and
foreign signed states. ProviderAndReplay runs start, setup, callback, picker,
selection and completion through Server HTTP. CommitRefusal,
PrivateConsumeFailure and RequiresTransaction verify committing conflicts,
private errors, provider ordering and absent adapters. Provider and transaction
implementations are fakes. W1's durable races are not proved here.

Invariant: each console write rechecks the operator's direct session and derives
its audit actor in the committing transaction. ConsoleWriteHTTPAuthorityProperty
covers all nine real handlers, exact authority/target forwarding, replay-key
forwarding, policy refusal, CSRF, committing conflicts, private errors and missing
adapters. The writer is fake. TestOperatorHTTPForwardsActionsAndAudits and
TestConsoleAccountDeletionRetainsLastOwnerConflict inject an explicit transaction
fake over SQLite. These establish HTTP output, audit and error mapping, rather
than production persistence rechecks.

The old TestSettleFinishedNodeClearsTheWindowForAnUnmeteredFinisher retires with
its dead helper. The final store's TestMeteredUnitNodeLifecycleUsesImmutableAward,
TestMeteredUnitSettlementFailureRollsBackResult and
TestMeteredUnitStaleReportCannotSettleReplacementProperty pass. They preserve
award billing, atomic settlement and stale-report fencing. Anonymous HTTP
finalization is no longer an accepted path. The port inventory records these
limits. No native credential-matrix cell is newly completed.

## Contract changes needed and remaining work

C2's new interfaces and exact obligations are recorded at the end of
contracts/c2-credentials.md. No C1 signature or implementation was changed.

- W1 implements GitHubConnectWriter. It rechecks live session ancestry, exact
  team/account and current owner membership. Consume uses the store clock and
  commits a nonce once before provider access. Bind rechecks GitHubUserID against
  the current linked identity, derives ConnectedBy and refuses foreign ownership
  atomically. HTTP/provider tests use fakes until then.
- W1 implements ConsoleWriter on the operator adapter. Every method rechecks
  direct-session ancestry and the trusted configured-operator gate, derives
  actors and uses the store clock. C1 still needs the configuration-gate plumbing.
  Keep credit/audit/business effects atomic. Resolve account ID or unique email
  inside deletion. An unknown explicit unfreeze hold is not_found. Waitlist
  replay must never select another oldest batch or send another notification.
- W1 still lacks EnsureBootstrapCapability and HasDeploymentAdministrator.
  Startup seeds must enter only Store.Issue's gated, digest-only, once-only path.
- W1 authentication_binding.go still does not populate BindingState.CancelReason.
  C2 already consumes it. Project first job intent before run intent through the
  existing snapshot. This continuation adds no second reader or SQL projection.
- W1 still owes verified-local-peer snapshots, roleless account/console session
  representation, the console committing gate and the prior credential, source,
  data, account, sizing, concurrency, schedule, run-control and business adapters.
- W1/W8/W9 still owe the real ReadLauncherSync immutable-award/claim-generation/
  Kubernetes-instance fence. EngineController.SyncLauncher remains implemented
  against that contract. W1b still settles the verified non-checkout PaymentEvent
  kinds. No old financial service callbacks were restored.
- W2 must finish Principal-based helper deletion and migrate the broad old
  socket fixtures. The legacy metrics/usage handlers are unregistered, and
  the declared final job metrics/usage routes still need implementation and
  authority-bearing persistence. Old runner-budget/egress/tenant wrappers and
  their tests also need retirement or final adapter integration. Deployment
  settings mutations still need committing authority checks. W2 is incomplete.

The mechanisms added are the two required committing interfaces and browser
proof adapter. Calling the raw stores would bypass the frozen authority
contract. No new environment variable, cache, timeout, schema, customer limit
or compatibility path was added. The existing waitlist retry policy now reaches
the writer and dashboard caller. No new product decision is proposed.

## Verification and friction

Go commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-gocache and GOTMPDIR/TMPDIR=/tmp/sw-astra-W2/tmp. They select
this checkout with go -C. Tests use -p 4.

Passed:

- Whole-repository go build -p 4 ./... on the final source tree.
- Scoped vet for controller, controller/client, capability, apierr, executable
  acceptance and sparkwing-controller. Full capability and apierr suites.
- Controller selection from the prior final-route report, plus GitHubBrowser
  and ConsoleWrite, 6.000s. The actual owner/job lifecycle and cancellation
  regressions are included. Later boundary/data selections pass after the
  last changes, including the final browser private-error regression.
- Executable acceptance, 13.940s, with the same final-route regex:
  ^Test(Account|Authority|Credential|ConcurrencyHTTP|Data|Secret|Source|Session|Console|Login|OAuth|OIDC|IdentityLink|Operator|Deployment|Discovery|Payment|RootProvisioning|GitHubRunnerAdmin|RunnerMetering|Credit|Queue|Sizing|SelfService|Engine|ReportingClient).
- The three named final store settlement tests, 1.197s.
- Native Linux TypeScript: go test -p 4 ./sdk/typescript/test/nativehost
  -run '^TestNative' -count=1 -timeout=180s, 7.241s. No setup error.
- Web operator helper tests, 11 passing. tsc --noEmit --incremental false passes.
- apiref, apispec and API snapshot generator tests. Focused docs mirror,
  changelog, symbol and privacy tests pass. API snapshot is regenerated,
  embedded mirrors are synced, check-changelog and git diff --check pass.
- commentcheck -base main . is clean across 2182 Go files.

The full pkg/docs test command could not complete because
TestWebClient_Versions_HappyPath panics with
"listen tcp6 [::1]:0: socket: operation not permitted". The exact command was
`go test -p 4 ./pkg/docs ./internal/apiref ./internal/apispec ./cmd/apidiff -count=1 -timeout=180s`
with the environment above. Its three non-docs packages passed. Focused
non-network docs checks then passed. The lead owns the socket-dependent suite.

The existing handler-policy AST check mistook the provider's AuthorizeURL URL
builder for authorization. It now excludes that exact builder. No production
wrapper was added to satisfy the check. Initial test-only form Content-Type
omissions and an overbroad web header assertion were corrected. The report
insertion used the port inventory's real unmarked format after an end-marker
assumption failed. These issues are resolved.

No pipeline, candidate runtime, deployment, service, release or Git write ran.
The lead owns review and landing. No reusable scratch or shared cache is
removed. Session-owned /tmp/sw-astra-W2 is deleted at session end.

## Shared files touched

The production caller outside W2 is web/src/lib/operator.ts, with its helper
test updated. Shared acceptance caller changes are account_reads_http_test.go,
operator_authorization_test.go and the explicit console_writes_fake_test.go.
Other shared files are the C2 contract, port inventory, auth/migration docs,
CHANGELOG.md, generated mirrors and .apidiff/pkg_controller.txt. The remaining
source changes belong to W2. No W1, W8, W9 Go source file was edited.

<!-- design:end -->

STATUS: CONTINUE
